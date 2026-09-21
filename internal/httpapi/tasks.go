package httpapi

import (
	"errors"
	"net/http"
	"time"

	"drill/internal/catalog"
	"drill/internal/srs"
	"drill/internal/store"
)

// taskSummary is a task as the list screens see it: metadata plus progress,
// never the tests and never the reference solution.
type taskSummary struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Topic        string `json:"topic"`
	Difficulty   int    `json:"difficulty"`
	EstimateMin  int    `json:"estimate_minutes"`
	Race         bool   `json:"race"`
	Attempts     int    `json:"attempts"`
	Passes       int    `json:"passes"`
	BestActiveMS int64  `json:"best_active_ms"`
	LastActiveMS int64  `json:"last_active_ms"`
	Stage        int    `json:"stage"`
	DueOn        string `json:"due_on,omitempty"`
	Mastered     bool   `json:"mastered"`
	Solved       bool   `json:"solved"`
	Changed      bool   `json:"changed"` // task edited since it was last solved
	Missing      bool   `json:"missing"` // recorded in the database but gone from disk
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	summaries, err := s.taskSummaries(r)
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot load tasks", err)
		return
	}
	snap := s.deps.Catalog.Snapshot()
	writeJSON(w, r, s.deps.Logger, http.StatusOK, map[string]any{
		"tasks":  summaries,
		"errors": snap.Errors(),
	})
}

func (s *Server) taskSummaries(r *http.Request) ([]taskSummary, error) {
	states, err := s.deps.Store.TaskStates(r.Context())
	if err != nil {
		return nil, err
	}
	snap := s.deps.Catalog.Snapshot()

	byID := make(map[string]store.TaskState, len(states))
	for _, st := range states {
		byID[st.ID] = st
	}

	out := make([]taskSummary, 0, len(states))
	for _, task := range snap.All() {
		out = append(out, summarise(task, byID[task.ID]))
		delete(byID, task.ID)
	}
	// Tasks that vanished from disk still carry history worth showing.
	for _, st := range states {
		if _, still := byID[st.ID]; !still {
			continue
		}
		out = append(out, taskSummary{
			ID: st.ID, Title: st.Title, Topic: st.Topic, Difficulty: st.Difficulty,
			EstimateMin: st.EstimateMin, Attempts: st.Attempts, Passes: st.Passes,
			BestActiveMS: st.BestActiveMS, LastActiveMS: st.LastActiveMS,
			Stage: st.Stage, DueOn: st.DueOn, Solved: st.Passes > 0, Missing: true,
		})
	}
	return out, nil
}

func summarise(t *catalog.Task, st store.TaskState) taskSummary {
	return taskSummary{
		ID:           t.ID,
		Title:        t.Title,
		Topic:        t.Topic,
		Difficulty:   t.Difficulty,
		EstimateMin:  t.EstimateMin,
		Race:         t.Race,
		Attempts:     st.Attempts,
		Passes:       st.Passes,
		BestActiveMS: st.BestActiveMS,
		LastActiveMS: st.LastActiveMS,
		Stage:        st.Stage,
		DueOn:        st.DueOn,
		Mastered:     srs.Mastered(store.Review{Stage: st.Stage, Exists: st.DueOn != ""}),
		Solved:       st.Passes > 0,
		Changed:      st.Passes > 0 && st.ContentHash != "" && st.ContentHash != t.ContentHash,
	}
}

// taskDetail is what the editor screen loads. Tests and the reference solution
// are absent by construction, not filtered out later.
type taskDetail struct {
	taskSummary
	Readme  string            `json:"readme"`
	Starter []catalog.File    `json:"starter"`
	Draft   map[string]string `json:"draft"`
}

func (s *Server) handleTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := s.deps.Catalog.Snapshot().Task(id)
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusNotFound, "unknown task", err)
		return
	}
	states, err := s.deps.Store.TaskStates(r.Context())
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot load progress", err)
		return
	}
	var st store.TaskState
	for _, candidate := range states {
		if candidate.ID == id {
			st = candidate
			break
		}
	}
	draft, err := s.deps.Store.Draft(r.Context(), id)
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot load the saved draft", err)
		return
	}

	writeJSON(w, r, s.deps.Logger, http.StatusOK, taskDetail{
		taskSummary: summarise(task, st),
		Readme:      task.Readme,
		Starter:     task.Starter,
		Draft:       draft,
	})
}

// handleSolution serves the reference implementation, but only once the task
// has actually been solved. The gate is here rather than in the UI because the
// entire value of the exercise depends on not peeking.
func (s *Server) handleSolution(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := s.deps.Catalog.Snapshot().Task(id)
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusNotFound, "unknown task", err)
		return
	}
	passed, err := s.deps.Store.TaskPassed(r.Context(), id)
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot check progress", err)
		return
	}
	if !passed {
		writeError(w, r, s.deps.Logger, http.StatusForbidden,
			"эталон открывается после первого зелёного прогона", nil)
		return
	}
	writeJSON(w, r, s.deps.Logger, http.StatusOK, map[string]any{
		"task_id":  id,
		"solution": task.Solution,
	})
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if s.deps.Reload == nil {
		writeError(w, r, s.deps.Logger, http.StatusNotImplemented, "reloading is not wired up", nil)
		return
	}
	if err := s.deps.Reload(r.Context()); err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "rescan failed", err)
		return
	}
	snap := s.deps.Catalog.Snapshot()
	writeJSON(w, r, s.deps.Logger, http.StatusOK, map[string]any{
		"tasks":  snap.Len(),
		"errors": snap.Errors(),
	})
}

// todayResponse drives the home screen: everything due, plus one new task.
type todayResponse struct {
	Day          string        `json:"day"`
	Reviews      []taskSummary `json:"reviews"`
	New          *taskSummary  `json:"new"`
	SolvedToday  int           `json:"solved_today"`
	Streak       int           `json:"streak"`
	TotalTasks   int           `json:"total_tasks"`
	SolvedTasks  int           `json:"solved_tasks"`
	MinutesToday int           `json:"minutes_today"`
}

func (s *Server) handleToday(w http.ResponseWriter, r *http.Request) {
	now := s.deps.Now()
	today := store.DayOf(now)

	summaries, err := s.taskSummaries(r)
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot load tasks", err)
		return
	}
	byID := make(map[string]taskSummary, len(summaries))
	solved := 0
	for _, t := range summaries {
		byID[t.ID] = t
		if t.Solved {
			solved++
		}
	}

	due, err := s.deps.Store.DueReviews(r.Context(), today)
	if err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot load the schedule", err)
		return
	}

	resp := todayResponse{Day: today, TotalTasks: len(summaries), SolvedTasks: solved}
	for _, d := range due {
		if t, ok := byID[d.TaskID]; ok {
			resp.Reviews = append(resp.Reviews, t)
		}
	}

	switch newID, err := s.deps.Store.PickNewTask(r.Context()); {
	case err == nil:
		if t, ok := byID[newID]; ok {
			resp.New = &t
		}
	case errors.Is(err, store.ErrNotFound):
		// Everything is already in rotation: nothing new to offer today.
	default:
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot pick a new task", err)
		return
	}

	if resp.SolvedToday, err = s.deps.Store.SolvedToday(r.Context(), today); err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot count today's solves", err)
		return
	}
	if resp.Streak, err = s.deps.Store.Streak(r.Context(), today); err != nil {
		writeError(w, r, s.deps.Logger, http.StatusInternalServerError, "cannot compute the streak", err)
		return
	}
	resp.MinutesToday = int(s.minutesToday(r, today) / time.Minute)

	writeJSON(w, r, s.deps.Logger, http.StatusOK, resp)
}

func (s *Server) minutesToday(r *http.Request, today string) time.Duration {
	series, err := s.deps.Store.TaskSeriesAll(r.Context())
	if err != nil {
		return 0
	}
	var total int64
	for _, ts := range series {
		for _, p := range ts.Points {
			if p.Day == today {
				total += p.ActiveMS
			}
		}
	}
	return time.Duration(total) * time.Millisecond
}
