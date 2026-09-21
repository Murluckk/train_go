package httpapi

import (
	"net/http"

	"drill/internal/store"
)

func (s *Server) handleStatsTasks(w http.ResponseWriter, r *http.Request) {
	series, err := s.deps.Store.TaskSeriesAll(r.Context())
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot load the history", err)
		return
	}
	since := store.AddDays(s.deps.Now(), -90)
	activity, err := s.deps.Store.DailyActivity(r.Context(), since)
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot load the activity", err)
		return
	}
	writeJSON(w, r, s.deps.Logger, http.StatusOK, map[string]any{
		"series":   series,
		"activity": activity,
		"today":    store.DayOf(s.deps.Now()),
	})
}

func (s *Server) handleStatsTopics(w http.ResponseWriter, r *http.Request) {
	topics, err := s.deps.Store.TopicStats(r.Context())
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot load topic statistics", err)
		return
	}
	writeJSON(w, r, s.deps.Logger, http.StatusOK, map[string]any{"topics": topics})
}
