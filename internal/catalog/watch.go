package catalog

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Syncer mirrors a freshly loaded snapshot somewhere else, typically the
// database. It runs after every successful scan.
type Syncer func(context.Context, *Snapshot) error

// Watcher rescans the tasks directory on a timer and publishes new snapshots.
type Watcher struct {
	root     string
	catalog  *Catalog
	interval time.Duration
	log      *slog.Logger
	sync     Syncer

	// scanning serialises scans so that a manual reload and a tick cannot
	// publish snapshots on top of each other.
	scanning sync.Mutex
}

// NewWatcher builds a watcher. It does not scan until Run or Reload is called.
func NewWatcher(root string, c *Catalog, interval time.Duration, log *slog.Logger, sync Syncer) *Watcher {
	return &Watcher{
		root:     root,
		catalog:  c,
		interval: interval,
		log:      log,
		sync:     sync,
	}
}

// Run scans once and then polls until ctx is cancelled. The first scan is
// synchronous so that a startup failure is reported before the server starts
// serving.
func (w *Watcher) Run(ctx context.Context) error {
	if err := w.scan(ctx, true); err != nil {
		return err
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := w.scan(ctx, false); err != nil {
				// A transient read error must not kill the watcher; the
				// previous snapshot stays live and the next tick retries.
				w.log.Error("rescan failed", "error", err)
			}
		}
	}
}

// Reload forces an immediate rescan and waits for it. It is safe to call
// before Run, concurrently with it, or after it has stopped.
func (w *Watcher) Reload(ctx context.Context) error {
	return w.scan(ctx, true)
}

// scan reloads the catalog when the directory fingerprint changed, or always
// when force is set.
func (w *Watcher) scan(ctx context.Context, force bool) error {
	w.scanning.Lock()
	defer w.scanning.Unlock()

	if !force {
		fp, err := Fingerprint(w.root)
		if err != nil {
			return err
		}
		if fp == w.catalog.Snapshot().Fingerprint() {
			return nil
		}
	}

	snap, err := Load(w.root)
	if err != nil {
		return err
	}
	for _, e := range snap.Errors() {
		w.log.Warn("task failed to load", "dir", e.Dir, "error", e.Err)
	}

	if w.sync != nil {
		if err := w.sync(ctx, snap); err != nil {
			return err
		}
	}
	w.catalog.Store(snap)
	w.log.Info("catalog loaded", "tasks", snap.Len(), "broken", len(snap.Errors()))
	return nil
}
