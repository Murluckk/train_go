package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"drill/internal/store"
)

// noteResponse is one entry of the divergence journal.
type noteResponse struct {
	ID        int64  `json:"id"`
	AttemptID int64  `json:"attempt_id"`
	TaskID    string `json:"task_id"`
	TaskTitle string `json:"task_title"`
	Topic     string `json:"topic"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Day       string `json:"day"`
}

func toNoteResponse(n store.Note) noteResponse {
	return noteResponse{
		ID: n.ID, AttemptID: n.AttemptID, TaskID: n.TaskID, TaskTitle: n.TaskTitle,
		Topic: n.Topic, Body: n.Body,
		CreatedAt: n.CreatedAt.Format(time.RFC3339),
		UpdatedAt: n.UpdatedAt.Format(time.RFC3339),
		Day:       n.AttemptDay,
	}
}

const maxNoteBytes = 64 << 10

func (s *Server) handleSaveNote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AttemptID int64  `json:"attempt_id"`
		Body      string `json:"body"`
	}
	if err := decodeJSON(r, &body, maxNoteBytes); err != nil {
		writeError(w, r, s.deps.Logger, http.StatusBadRequest, "bad request body", err)
		return
	}
	if strings.TrimSpace(body.Body) == "" {
		writeError(w, r, s.deps.Logger, http.StatusBadRequest, "заметка не может быть пустой", nil)
		return
	}

	note, err := s.deps.Store.SaveNote(r.Context(), body.AttemptID, body.Body)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, r, s.deps.Logger, http.StatusNotFound, "unknown attempt", err)
			return
		}
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot save the note", err)
		return
	}
	writeJSON(w, r, s.deps.Logger, http.StatusOK, toNoteResponse(note))
}

func (s *Server) handleListNotes(w http.ResponseWriter, r *http.Request) {
	limit := min(max(queryInt(r, "limit", 50), 1), 200)
	offset := max(queryInt(r, "offset", 0), 0)
	topic := r.URL.Query().Get("topic")

	notes, err := s.deps.Store.Notes(r.Context(), topic, limit, offset)
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot load the journal", err)
		return
	}
	out := make([]noteResponse, 0, len(notes))
	for _, n := range notes {
		out = append(out, toNoteResponse(n))
	}
	writeJSON(w, r, s.deps.Logger, http.StatusOK, map[string]any{
		"notes":  out,
		"limit":  limit,
		"offset": offset,
	})
}

func (s *Server) handleDeleteNote(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusBadRequest, "bad note id", err)
		return
	}
	if err := s.deps.Store.DeleteNote(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, r, s.deps.Logger, http.StatusNotFound, "unknown note", err)
			return
		}
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot delete the note", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
