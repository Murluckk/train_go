// Package catalog reads task definitions from disk and keeps an immutable
// snapshot of them in memory.
//
// The files under ./tasks are the source of truth. Readers take the current
// snapshot through an atomic pointer, so a rescan never blocks a request and
// a request never observes a half-loaded catalog.
package catalog

import (
	"errors"
	"slices"
	"strings"
	"sync/atomic"
)

// ErrNotFound is returned when a task id is not in the snapshot.
var ErrNotFound = errors.New("task not found")

// File is one source file belonging to a task.
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Task is a fully loaded task definition.
type Task struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Topic       string `json:"topic"`
	Difficulty  int    `json:"difficulty"`
	EstimateMin int    `json:"estimate_minutes"`

	// Race controls whether the runner passes -race. It defaults to true;
	// tasks where a data race is impossible by construction can turn it off
	// to keep the feedback loop short.
	Race bool `json:"race"`

	Readme string `json:"readme"`

	Starter  []File `json:"starter"`
	Tests    []File `json:"-"` // never leaves the server
	Solution []File `json:"-"` // gated behind a green run

	Dir         string `json:"-"`
	ContentHash string `json:"content_hash"`
}

// Snapshot is an immutable view of every task found on disk.
type Snapshot struct {
	tasks  map[string]*Task
	order  []string
	errs   []LoadError
	fprint string
}

// LoadError records a directory that could not be turned into a task. Bad
// tasks are reported rather than silently skipped: a typo in task.yaml should
// be visible, not invisible.
type LoadError struct {
	Dir string `json:"dir"`
	Err string `json:"error"`
}

// NewSnapshot builds a snapshot from already loaded tasks.
func NewSnapshot(tasks []*Task, errs []LoadError, fingerprint string) *Snapshot {
	s := &Snapshot{
		tasks:  make(map[string]*Task, len(tasks)),
		order:  make([]string, 0, len(tasks)),
		errs:   errs,
		fprint: fingerprint,
	}
	for _, t := range tasks {
		s.tasks[t.ID] = t
		s.order = append(s.order, t.ID)
	}
	slices.SortFunc(s.order, func(a, b string) int {
		ta, tb := s.tasks[a], s.tasks[b]
		if c := strings.Compare(ta.Topic, tb.Topic); c != 0 {
			return c
		}
		if ta.Difficulty != tb.Difficulty {
			return ta.Difficulty - tb.Difficulty
		}
		return strings.Compare(a, b)
	})
	return s
}

// Task returns a task by id.
func (s *Snapshot) Task(id string) (*Task, error) {
	if t, ok := s.tasks[id]; ok {
		return t, nil
	}
	return nil, ErrNotFound
}

// All returns every task, ordered by topic, difficulty and id.
func (s *Snapshot) All() []*Task {
	out := make([]*Task, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.tasks[id])
	}
	return out
}

// Len reports how many tasks loaded successfully.
func (s *Snapshot) Len() int { return len(s.tasks) }

// Errors returns the directories that failed to load.
func (s *Snapshot) Errors() []LoadError { return slices.Clone(s.errs) }

// Fingerprint identifies the state of the directory tree the snapshot was
// built from.
func (s *Snapshot) Fingerprint() string { return s.fprint }

// Catalog holds the current snapshot.
type Catalog struct {
	snap atomic.Pointer[Snapshot]
}

// New returns an empty catalog.
func New() *Catalog {
	c := &Catalog{}
	c.snap.Store(NewSnapshot(nil, nil, ""))
	return c
}

// Snapshot returns the current immutable snapshot. It never blocks.
func (c *Catalog) Snapshot() *Snapshot { return c.snap.Load() }

// Store replaces the current snapshot.
func (c *Catalog) Store(s *Snapshot) { c.snap.Store(s) }
