package orchestrator

import (
	"fmt"
	"sync"
)

// Store keeps runs for the life of the process. Postgres persistence replaces
// it in §25; the runner only needs Get and Put, so the swap stays local.
type Store struct {
	mu   sync.RWMutex
	runs map[string]*Run
	seq  int
}

// NewStore returns an empty run store.
func NewStore() *Store {
	return &Store{runs: make(map[string]*Run)}
}

// NextID returns a stable, human-readable run id.
func (s *Store) NextID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return fmt.Sprintf("run-%04d", s.seq)
}

// Put stores or replaces a run.
func (s *Store) Put(run *Run) {
	if run == nil || run.ID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.ID] = run
}

// Get returns a run by id.
func (s *Store) Get(id string) (*Run, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.runs[id]
	return run, ok
}

// Len reports how many runs are stored.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.runs)
}
