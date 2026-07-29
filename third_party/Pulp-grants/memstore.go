package grants

import "sync"

// MemStore is an in-memory GrantStore — the reference implementation, used for
// fast unit tests and as a non-persistent fallback. Safe for concurrent use.
type MemStore struct {
	mu     sync.RWMutex
	grants map[string]Grant // keyed by ID
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{grants: make(map[string]Grant)}
}

func (s *MemStore) Lookup(kind Kind, subject string, want int) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := nowFn()
	best := 0
	for _, g := range s.grants {
		if g.Kind != kind || !g.IsActive(now) {
			continue
		}
		if SubjectCovers(kind, g.Subject, subject) && g.Access > best {
			best = g.Access
		}
	}
	return best, best >= want && best > 0
}

func (s *MemStore) Put(g Grant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants[g.ID] = g
	return nil
}

func (s *MemStore) Active(cellID string, kind Kind) ([]Grant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := nowFn()
	var out []Grant
	for _, g := range s.grants {
		if g.CellID == cellID && g.Kind == kind && g.IsActive(now) {
			out = append(out, g)
		}
	}
	return out, nil
}

func (s *MemStore) List(cellID string) ([]Grant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Grant
	for _, g := range s.grants {
		if g.CellID == cellID {
			out = append(out, g)
		}
	}
	return out, nil
}

func (s *MemStore) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g, ok := s.grants[id]; ok && g.RevokedAt == 0 {
		g.RevokedAt = nowFn()
		s.grants[id] = g
	}
	return nil
}

func (s *MemStore) RevokeMatching(kind Kind, query string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowFn()
	n := 0
	for id, g := range s.grants {
		if g.Kind != kind || !g.IsActive(now) {
			continue
		}
		// Revoke any grant whose subject covers the query (remove access to X),
		// and any grant subsumed by the query (remove a broader sweep).
		if SubjectCovers(kind, g.Subject, query) || SubjectCovers(kind, query, g.Subject) {
			g.RevokedAt = now
			s.grants[id] = g
			n++
		}
	}
	return n, nil
}
