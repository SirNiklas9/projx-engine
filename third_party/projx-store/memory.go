package store

import "sort"

// Mem is an in-memory Store — the first implementation behind the interface. It
// exists so context/workflow/verify can be built and tested against the contract
// today; a SQLite-backed Store swaps in later without touching any caller.
//
// List returns records in deterministic ID order so callers and tests reproduce.
type Mem struct {
	byID map[string]Record
}

// NewMem returns an empty in-memory store.
func NewMem() *Mem { return &Mem{byID: map[string]Record{}} }

// Put inserts or replaces a record by ID.
func (m *Mem) Put(r Record) error {
	if r.ID == "" {
		return ErrNoID
	}
	if r.UpdatedAt == 0 { // mirror SQLite.Put: stamp fresh writes, preserve merged ones
		r.UpdatedAt = stamp()
	}
	m.byID[r.ID] = r
	return nil
}

// Get returns the record with the given ID, if present.
func (m *Mem) Get(id string) (Record, bool) {
	r, ok := m.byID[id]
	return r, ok
}

// Delete removes a record by ID. Deleting a missing ID is a no-op (not an error).
func (m *Mem) Delete(id string) error {
	delete(m.byID, id)
	return nil
}

// List returns all records matching the filter, sorted by ID.
func (m *Mem) List(f Filter) []Record {
	var out []Record
	for _, r := range m.byID {
		if f.match(r) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// compile-time assertion that Mem satisfies Store.
var _ Store = (*Mem)(nil)
