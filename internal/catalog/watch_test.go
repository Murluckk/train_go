package catalog

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The watcher is driven by a ticker, so its behaviour is a question about
// time. synctest gives a fake clock and a way to wait until every goroutine in
// the bubble is blocked, which removes the sleeps this test would otherwise
// need.
func TestWatcherPicksUpNewTasks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		writeTask(t, root, "a", validTask("a"))

		c := New()
		w := NewWatcher(root, c, 2*time.Second, discardLogger(), nil)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		done := make(chan error, 1)
		go func() { done <- w.Run(ctx) }()

		synctest.Wait()
		if got := c.Snapshot().Len(); got != 1 {
			t.Fatalf("after the initial scan: %d tasks, want 1", got)
		}

		writeTask(t, root, "b", validTask("b"))

		time.Sleep(3 * time.Second)
		synctest.Wait()
		if got := c.Snapshot().Len(); got != 2 {
			t.Errorf("after a rescan: %d tasks, want 2", got)
		}

		cancel()
		synctest.Wait()
		if err := <-done; err != nil {
			t.Errorf("Run() error = %v", err)
		}
	})
}

func TestWatcherSkipsUnchangedTree(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		writeTask(t, root, "a", validTask("a"))

		var syncs atomic.Int64
		c := New()
		w := NewWatcher(root, c, time.Second, discardLogger(), func(context.Context, *Snapshot) error {
			syncs.Add(1)
			return nil
		})

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go w.Run(ctx) //nolint:errcheck // checked through the catalog below

		synctest.Wait()
		time.Sleep(10 * time.Second)
		synctest.Wait()

		if got := syncs.Load(); got != 1 {
			t.Errorf("sync ran %d times over 10 idle ticks, want 1; an unchanged tree must not be reloaded", got)
		}
	})
}

func TestWatcherKeepsGoodTasksWhenOneBreaks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		writeTask(t, root, "a", validTask("a"))

		c := New()
		w := NewWatcher(root, c, time.Second, discardLogger(), nil)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go w.Run(ctx) //nolint:errcheck

		synctest.Wait()
		if c.Snapshot().Len() != 1 {
			t.Fatal("initial scan failed")
		}

		// A half-written task directory appears. It must be reported, but the
		// task that already loaded has to keep working.
		writeTask(t, root, "b", map[string]string{"task.yaml": "id: b\n"})
		time.Sleep(3 * time.Second)
		synctest.Wait()

		snap := c.Snapshot()
		if snap.Len() != 1 {
			t.Errorf("tasks = %d, want the healthy task still served", snap.Len())
		}
		if errs := snap.Errors(); len(errs) != 1 || errs[0].Dir != "b" {
			t.Errorf("errors = %+v, want the broken directory reported", errs)
		}
	})
}

func TestWatcherReloadBeforeRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTask(t, root, "a", validTask("a"))

	c := New()
	w := NewWatcher(root, c, time.Millisecond, discardLogger(), nil)

	if err := w.Reload(t.Context()); err != nil {
		t.Fatalf("Reload() before Run() error = %v", err)
	}
	if c.Snapshot().Len() != 1 {
		t.Error("Reload() before Run() should still populate the catalog")
	}
}

func TestWatcherReloadIsImmediate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		writeTask(t, root, "a", validTask("a"))

		c := New()
		w := NewWatcher(root, c, time.Hour, discardLogger(), nil)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go w.Run(ctx) //nolint:errcheck

		synctest.Wait()
		writeTask(t, root, "b", validTask("b"))

		if err := w.Reload(ctx); err != nil {
			t.Fatalf("Reload() error = %v", err)
		}
		if got := c.Snapshot().Len(); got != 2 {
			t.Errorf("after Reload(): %d tasks, want 2 without waiting an hour for the tick", got)
		}
	})
}

func TestWatcherPropagatesSyncFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTask(t, root, "a", validTask("a"))

	boom := errors.New("database is down")
	c := New()
	w := NewWatcher(root, c, time.Hour, discardLogger(), func(context.Context, *Snapshot) error {
		return boom
	})

	err := w.Run(t.Context())
	if !errors.Is(err, boom) {
		t.Fatalf("Run() error = %v, want the sync failure; startup must not proceed with an unsynced catalog", err)
	}
	if c.Snapshot().Len() != 0 {
		t.Error("a failed sync must not publish the snapshot")
	}
}

func TestWatcherFirstScanFailureIsFatal(t *testing.T) {
	t.Parallel()
	c := New()
	w := NewWatcher(filepath.Join(t.TempDir(), "missing"), c, time.Hour, discardLogger(), nil)
	if err := w.Run(t.Context()); err == nil {
		t.Error("Run() should fail when the tasks directory does not exist")
	}
}
