// Command drill is a local trainer for writing Go by hand.
//
// It serves a single page editor, runs the task's hidden tests against what
// you typed, records how long each solve took and schedules repetitions. It
// binds to loopback and has no authentication because it executes arbitrary
// code on request: it is meant for one person on one machine.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"drill/internal/catalog"
	"drill/internal/config"
	"drill/internal/httpapi"
	"drill/internal/runner"
	"drill/internal/store"
	"drill/internal/web"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "drill: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.Parse(args, os.Getenv, os.Stderr)
	if err != nil {
		return err
	}
	log := cfg.Logger(os.Stderr)

	// Signals cancel this context; everything below hangs off it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	log.Info("database ready", "path", cfg.DBPath)

	testRunner, err := runner.New(runner.Config{
		GoBin:       cfg.GoBin,
		CacheDir:    cfg.CacheDir,
		WorkDir:     cfg.WorkDir,
		RunTimeout:  cfg.RunTimeout,
		HardTimeout: cfg.HardTimeout,
		MaxParallel: cfg.MaxParallel,
		Logger:      log,
	})
	if err != nil {
		return err
	}
	// A broken sandbox makes every submission look like the user's mistake,
	// so prove it works before serving anything.
	if !cfg.SkipSelfCheck {
		started := time.Now()
		if err := testRunner.SelfCheck(ctx); err != nil {
			return err
		}
		log.Info("runner self-check passed", "took", time.Since(started).Round(time.Millisecond))
	}
	log.Info("runner ready", "go", testRunner.GoVersion(), "cache", cfg.CacheDir, "parallel", cfg.MaxParallel)

	cat := catalog.New()
	watcher := catalog.NewWatcher(cfg.TasksDir, cat, cfg.ScanInterval, log,
		func(ctx context.Context, snap *catalog.Snapshot) error {
			metas := make([]store.TaskMeta, 0, snap.Len())
			for _, t := range snap.All() {
				metas = append(metas, store.TaskMeta{
					ID: t.ID, Title: t.Title, Topic: t.Topic,
					Difficulty: t.Difficulty, EstimateMin: t.EstimateMin,
					ContentHash: t.ContentHash,
				})
			}
			return db.SyncTasks(ctx, metas)
		})

	// The first scan is synchronous: a typo in -tasks should stop startup,
	// not show up as an empty list in the browser.
	if err := watcher.Reload(ctx); err != nil {
		return fmt.Errorf("load tasks from %s: %w", cfg.TasksDir, err)
	}

	static, err := web.Handler()
	if err != nil {
		return err
	}

	api := httpapi.New(httpapi.Deps{
		Store:   db,
		Catalog: cat,
		Runner:  testRunner,
		Reload:  watcher.Reload,
		Logger:  log,
		Static:  static,
	})

	srv := &http.Server{
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// No write timeout: the run event stream is a long lived response and
		// a deadline here would cut it off mid-test.
		WriteTimeout: 0,
		IdleTimeout:  2 * time.Minute,
		ErrorLog:     slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr, err)
	}

	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		if err := watcher.Run(ctx); err != nil {
			log.Error("task watcher stopped", "error", err)
		}
	}()

	serverErr := make(chan error, 1)
	go func() {
		log.Info("drill is listening", "addr", "http://"+ln.Addr().String(), "tasks", cfg.TasksDir)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	select {
	case err := <-serverErr:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
	}
	stop()

	return shutdown(srv, api, testRunner, watcherDone, cfg.ShutdownGrace, log)
}

// shutdown drains in the order that actually works: stop the streams first,
// then the HTTP server, then kill the sandboxes.
func shutdown(srv *http.Server, api *httpapi.Server, testRunner *runner.Runner,
	watcherDone <-chan struct{}, grace time.Duration, log *slog.Logger) error {

	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()

	// Server.Shutdown waits for active requests, and an event stream never
	// ends by itself: without this it would always burn the whole grace
	// period.
	api.Shutdown()

	var errs []error
	if err := srv.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("http server: %w", err))
	}
	// Cancelling the runs kills each process group and removes each sandbox.
	if err := testRunner.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}

	select {
	case <-watcherDone:
	case <-ctx.Done():
		errs = append(errs, errors.New("task watcher did not stop in time"))
	}

	if err := errors.Join(errs...); err != nil {
		return err
	}
	log.Info("bye")
	return nil
}
