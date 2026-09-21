package httpapi

import (
	"context"
	"errors"
	"time"
)

var errShuttingDown = errors.New("server is shutting down")

// contextWithTimeout builds a context that is bounded by a timeout and also
// cancelled when the server starts shutting down. Background bookkeeping uses
// it so that it outlives the request that triggered it but not the process.
func contextWithTimeout(shutdown <-chan struct{}, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeoutCause(context.Background(), d, errors.New("timed out"))
	stop := make(chan struct{})
	go func() {
		select {
		case <-shutdown:
			cancel()
		case <-ctx.Done():
		case <-stop:
		}
	}()
	return ctx, func() {
		close(stop)
		cancel()
	}
}
