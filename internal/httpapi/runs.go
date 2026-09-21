package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"drill/internal/catalog"
	"drill/internal/runner"
	"drill/internal/srs"
	"drill/internal/store"
)

// startRunRequest is what the editor submits.
type startRunRequest struct {
	AttemptID int64             `json:"attempt_id"`
	Files     map[string]string `json:"files"`
}

// handleStartRun launches a run and returns its id. The stream is a separate
// GET, because EventSource only does GET and because a page reload must
// reconnect to a run rather than kill it.
func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	var body startRunRequest
	if err := decodeJSON(r, &body, maxBodyBytes); err != nil {
		writeError(w, r, s.deps.Logger, http.StatusBadRequest, "bad request body", err)
		return
	}
	if len(body.Files) == 0 {
		writeError(w, r, s.deps.Logger, http.StatusBadRequest, "nothing to run: no files were submitted", nil)
		return
	}
	for path := range body.Files {
		if err := catalog.ValidateUserPath(path); err != nil {
			writeError(w, r, s.deps.Logger, http.StatusBadRequest, "bad file path", err)
			return
		}
	}

	attempt, err := s.deps.Store.Attempt(r.Context(), body.AttemptID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, r, s.deps.Logger, http.StatusNotFound, "unknown attempt", err)
			return
		}
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot load the attempt", err)
		return
	}

	task, err := s.deps.Catalog.Snapshot().Task(attempt.TaskID)
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusNotFound, "the task is no longer on disk", err)
		return
	}

	// Saving the buffers before running means a crash mid-run still leaves
	// the work on disk.
	if err := s.deps.Store.SaveDraft(r.Context(), task.ID, body.Files); err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot save the draft", err)
		return
	}

	run, err := s.deps.Runner.Start(r.Context(), task.ID, attempt.ID, runner.Spec{
		TaskID: task.ID,
		Files:  body.Files,
		Tests:  catalog.FileMap(task.Tests),
		Race:   task.Race,
	})
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusBadRequest, "cannot start the run", err)
		return
	}

	go s.recordWhenDone(run, attempt.ID, body.Files)

	writeJSON(w, r, s.deps.Logger, http.StatusAccepted, map[string]any{
		"run_id":     run.ID,
		"attempt_id": attempt.ID,
		"task_id":    task.ID,
		"race":       task.Race,
	})
}

// recordWhenDone books the run against the attempt once it finishes. It runs
// detached from the request: the verdict must be recorded even if the browser
// closed the moment after pressing the button.
func (s *Server) recordWhenDone(run *runner.Run, attemptID int64, files map[string]string) {
	ctx, cancel := contextWithTimeout(s.shutdown, 5*time.Minute)
	defer cancel()

	res, err := run.Wait(ctx)
	if err != nil {
		s.deps.Logger.Warn("gave up waiting for a run", "run", run.ID, "error", err)
		return
	}
	// A canceled run is not an attempt at solving: counting it would inflate
	// the "runs before green" number for no reason.
	if res.Outcome == runner.OutcomeCanceled {
		return
	}

	if err := s.deps.Store.RecordRun(ctx, attemptID, res.OK()); err != nil {
		s.deps.Logger.Error("record run", "run", run.ID, "error", err)
		return
	}
	if !res.OK() {
		return
	}
	if _, _, err := s.deps.Store.PassAttempt(ctx, attemptID, files, srs.Advance); err != nil {
		s.deps.Logger.Error("record a solved attempt", "run", run.ID, "attempt", attemptID, "error", err)
	}
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.deps.Runner.Run(r.PathValue("id"))
	if !ok {
		writeError(w, r, s.deps.Logger, http.StatusNotFound, "unknown run", nil)
		return
	}
	res, finished := run.Result()
	writeJSON(w, r, s.deps.Logger, http.StatusOK, map[string]any{
		"run_id":     run.ID,
		"task_id":    run.TaskID,
		"attempt_id": run.AttemptID,
		"finished":   finished,
		"result":     res,
	})
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.deps.Runner.Run(r.PathValue("id"))
	if !ok {
		writeError(w, r, s.deps.Logger, http.StatusNotFound, "unknown run", nil)
		return
	}
	run.Cancel()
	w.WriteHeader(http.StatusAccepted)
}

// heartbeatInterval keeps the SSE connection alive through anything that
// buffers, and lets the client notice a dead server.
const heartbeatInterval = 15 * time.Second

// handleRunEvents streams a run over SSE, replaying from the beginning (or
// from Last-Event-ID) so that a late or reconnecting client sees everything.
func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	run, ok := s.deps.Runner.Run(r.PathValue("id"))
	if !ok {
		writeError(w, r, s.deps.Logger, http.StatusNotFound, "unknown run", nil)
		return
	}

	after := lastEventID(r)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	// A stream has no meaningful write deadline.
	_ = rc.SetWriteDeadline(time.Time{})
	if err := rc.Flush(); err != nil {
		return
	}

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	events := make(chan []runner.Event)
	done := make(chan bool, 1)
	ctx := r.Context()

	go func() {
		defer close(events)
		for {
			batch, finished := run.Follow(ctx, after)
			if len(batch) > 0 {
				after = batch[len(batch)-1].Seq
				select {
				case events <- batch:
				case <-ctx.Done():
					return
				}
			}
			if finished && len(batch) == 0 {
				done <- true
				return
			}
			if ctx.Err() != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return

		case <-s.shutdown:
			// http.Server.Shutdown waits for active requests, and this one
			// would never end by itself.
			_, _ = fmt.Fprint(w, "event: closing\ndata: {}\n\n")
			_ = rc.Flush()
			return

		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}

		case <-done:
			return

		case batch, open := <-events:
			if !open {
				return
			}
			for _, e := range batch {
				if err := writeSSE(w, e); err != nil {
					return
				}
			}
			if err := rc.Flush(); err != nil {
				return
			}
			ticker.Reset(heartbeatInterval)
		}
	}
}

func writeSSE(w http.ResponseWriter, e runner.Event) error {
	data, err := e.Data()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.Kind, data)
	return err
}

// lastEventID honours the browser's reconnect header, falling back to a query
// parameter so the stream can also be resumed by hand.
func lastEventID(r *http.Request) int64 {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
