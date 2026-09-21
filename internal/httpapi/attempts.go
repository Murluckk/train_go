package httpapi

import (
	"errors"
	"net/http"
	"time"

	"drill/internal/catalog"
	"drill/internal/srs"
	"drill/internal/store"
	"drill/internal/textdiff"
)

const maxBodyBytes = 4 << 20

// attemptResponse is the state the editor needs to render its stopwatch.
type attemptResponse struct {
	ID         int64  `json:"id"`
	TaskID     string `json:"task_id"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	ElapsedMS  int64  `json:"elapsed_ms"`
	ActiveMS   int64  `json:"active_ms"`
	DurationMS int64  `json:"duration_ms"`
	RunCount   int    `json:"run_count"`
	FailCount  int    `json:"fail_count"`
	Resumed    bool   `json:"resumed"`
}

func (s *Server) attemptResponse(a store.Attempt, resumed bool) attemptResponse {
	elapsed := a.DurationMS
	if a.InProgress() {
		elapsed = s.deps.Now().Sub(a.StartedAt).Milliseconds()
	}
	return attemptResponse{
		ID: a.ID, TaskID: a.TaskID, Status: a.Status,
		StartedAt: a.StartedAt.Format(time.RFC3339), ElapsedMS: elapsed,
		ActiveMS: a.ActiveMS, DurationMS: a.DurationMS,
		RunCount: a.RunCount, FailCount: a.FailCount, Resumed: resumed,
	}
}

// handleStartAttempt starts the stopwatch. The server owns the clock so that a
// page reload cannot reset it and the browser cannot flatter the numbers.
func (s *Server) handleStartAttempt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.deps.Catalog.Snapshot().Task(id); err != nil {
		writeError(w, r, s.deps.Logger, http.StatusNotFound, "unknown task", err)
		return
	}

	var body struct {
		Restart bool `json:"restart"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &body, 1<<10); err != nil {
			writeError(w, r, s.deps.Logger, http.StatusBadRequest, "bad request body", err)
			return
		}
	}

	if body.Restart {
		if err := s.deps.Store.AbandonOpen(r.Context(), id); err != nil {
			writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot abandon the previous attempt", err)
			return
		}
		if err := s.deps.Store.ClearDraft(r.Context(), id); err != nil {
			writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot clear the draft", err)
			return
		}
	}

	attempt, resumed, err := s.deps.Store.StartAttempt(r.Context(), id)
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot start an attempt", err)
		return
	}
	writeJSON(w, r, s.deps.Logger, http.StatusOK, s.attemptResponse(attempt, resumed))
}

func (s *Server) handleAttempt(w http.ResponseWriter, r *http.Request) {
	attempt, ok := s.lookupAttempt(w, r)
	if !ok {
		return
	}
	writeJSON(w, r, s.deps.Logger, http.StatusOK, s.attemptResponse(attempt, false))
}

func (s *Server) handleAbandonAttempt(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusBadRequest, "bad attempt id", err)
		return
	}
	if err := s.deps.Store.AbandonAttempt(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, r, s.deps.Logger, http.StatusNotFound, "no open attempt with that id", err)
			return
		}
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot abandon the attempt", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// maxHeartbeat bounds a single activity report. The browser sends one every
// few seconds; anything larger is a bug or a clock jump, and silently
// accepting it would corrupt the only metric that matters.
const maxHeartbeat = 2 * time.Minute

// handleHeartbeat credits focused editing time. Wall clock alone is a poor
// measure - walking away for coffee would ruin the sample - so the client
// reports only the time the editor was focused and being typed in.
func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	attempt, ok := s.lookupAttempt(w, r)
	if !ok {
		return
	}

	var body struct {
		ActiveMS int64 `json:"active_ms"`
	}
	if err := decodeJSON(r, &body, 1<<10); err != nil {
		writeError(w, r, s.deps.Logger, http.StatusBadRequest, "bad request body", err)
		return
	}
	d := time.Duration(body.ActiveMS) * time.Millisecond
	if d < 0 || d > maxHeartbeat {
		writeError(w, r, s.deps.Logger, http.StatusBadRequest,
			"active_ms must be between 0 and two minutes", nil)
		return
	}

	if err := s.deps.Store.AddActive(r.Context(), attempt.ID, d); err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot record activity", err)
		return
	}
	updated, err := s.deps.Store.Attempt(r.Context(), attempt.ID)
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot reload the attempt", err)
		return
	}
	writeJSON(w, r, s.deps.Logger, http.StatusOK, s.attemptResponse(updated, false))
}

func (s *Server) handleSaveDraft(w http.ResponseWriter, r *http.Request) {
	attempt, ok := s.lookupAttempt(w, r)
	if !ok {
		return
	}

	var body struct {
		Files map[string]string `json:"files"`
	}
	if err := decodeJSON(r, &body, maxBodyBytes); err != nil {
		writeError(w, r, s.deps.Logger, http.StatusBadRequest, "bad request body", err)
		return
	}
	for path := range body.Files {
		if err := catalog.ValidateUserPath(path); err != nil {
			writeError(w, r, s.deps.Logger, http.StatusBadRequest, "bad file path", err)
			return
		}
	}
	if err := s.deps.Store.SaveDraft(r.Context(), attempt.TaskID, body.Files); err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot save the draft", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reviewResponse is the post-solve screen: my code, the reference, what gofmt
// would have done, and any note I already wrote.
type reviewResponse struct {
	Attempt    attemptResponse     `json:"attempt"`
	TaskID     string              `json:"task_id"`
	TaskTitle  string              `json:"task_title"`
	Diffs      []textdiff.FileDiff `json:"diffs"`
	GofmtDiffs []textdiff.FileDiff `json:"gofmt_diffs"`
	Note       string              `json:"note"`
	NoteID     int64               `json:"note_id,omitempty"`
	NextDueOn  string              `json:"next_due_on,omitempty"`
	NextInDays int                 `json:"next_in_days"`
}

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	attempt, ok := s.lookupAttempt(w, r)
	if !ok {
		return
	}
	if attempt.Status != store.StatusPassed {
		writeError(w, r, s.deps.Logger, http.StatusConflict,
			"разбор доступен после зелёного прогона", nil)
		return
	}

	task, err := s.deps.Catalog.Snapshot().Task(attempt.TaskID)
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusNotFound, "the task is no longer on disk", err)
		return
	}
	mine, err := s.deps.Store.AttemptFiles(r.Context(), attempt.ID)
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot load the snapshot", err)
		return
	}

	resp := reviewResponse{
		Attempt:   s.attemptResponse(attempt, false),
		TaskID:    task.ID,
		TaskTitle: task.Title,
		Diffs:     textdiff.Files(mine, catalog.FileMap(task.Solution)),
	}

	// gofmt runs only after the fact: formatting by hand is part of the
	// practice, so the editor never does it.
	for _, path := range catalog.SortedPaths(mine) {
		d, err := textdiff.GofmtDiff(path, mine[path])
		if err != nil || d.Identical {
			continue
		}
		resp.GofmtDiffs = append(resp.GofmtDiffs, d)
	}

	if note, err := s.deps.Store.NoteByAttempt(r.Context(), attempt.ID); err == nil {
		resp.Note = note.Body
		resp.NoteID = note.ID
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot load the note", err)
		return
	}

	if review, err := s.deps.Store.Review(r.Context(), attempt.TaskID); err == nil && review.Exists {
		resp.NextDueOn = review.DueOn
		resp.NextInDays = srs.IntervalDays(review.Stage - 1)
	}

	writeJSON(w, r, s.deps.Logger, http.StatusOK, resp)
}

func (s *Server) lookupAttempt(w http.ResponseWriter, r *http.Request) (store.Attempt, bool) {
	id, err := pathInt64(r, "id")
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusBadRequest, "bad attempt id", err)
		return store.Attempt{}, false
	}
	attempt, err := s.deps.Store.Attempt(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, r, s.deps.Logger, http.StatusNotFound, "unknown attempt", err)
			return store.Attempt{}, false
		}
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot load the attempt", err)
		return store.Attempt{}, false
	}
	return attempt, true
}
