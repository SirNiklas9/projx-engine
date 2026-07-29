package store

import "sort"

// Workspace ties up to THREE physical stores into one logical Store, realizing the
// multi-LEVEL model: machine/user GLOBAL, an optional WORKSPACE (a multi-repo folder
// with its own rules), and the per-repo PROJECT store. Any level except Global may be
// nil — point it at a bare repo (project + global, no workspace), a workspace with no
// project open, or just global. Reads COMPOSE whatever is present; writes route by
// Record.Scope.Owner(), falling back UP a level when the owning store is absent, so a
// missing level never drops a write and project-only just works.
type Workspace struct {
	Global  Store // machine/user level (also absorbs workspace-scoped writes when Space is nil)
	Space   Store // OPTIONAL workspace level (nil = not in a workspace)
	Project Store // per-repo level (nil = not pointed at a repo)
}

// NewWorkspace is the 2-level (back-compat) constructor: yours holds global + workspace
// records, project holds project records. Equivalent to NewComposite(yours, nil, project).
func NewWorkspace(yours, project Store) *Workspace {
	return &Workspace{Global: yours, Project: project}
}

// NewComposite is the 3-level constructor. space may be nil (project-only / no workspace);
// project may be nil (a workspace with nothing open). global is always required.
func NewComposite(global, space, project Store) *Workspace {
	return &Workspace{Global: global, Space: space, Project: project}
}

// owning returns the physical store for a scope's level, falling back UP (project→global,
// workspace→global) when that level is absent — so a missing level never drops a write.
func (w *Workspace) owning(s Scope) Store {
	switch s.Owner() {
	case "project":
		if w.Project != nil {
			return w.Project
		}
	case "workspace":
		if w.Space != nil {
			return w.Space
		}
	}
	return w.Global
}

// present lists the non-nil stores in Project→Space→Global order (Get precedence: the
// most specific level wins).
func (w *Workspace) present() []Store {
	var ss []Store
	if w.Project != nil {
		ss = append(ss, w.Project)
	}
	if w.Space != nil {
		ss = append(ss, w.Space)
	}
	if w.Global != nil {
		ss = append(ss, w.Global)
	}
	return ss
}

// Put routes the record to the store owning its scope's level (falling back up).
func (w *Workspace) Put(r Record) error { return w.owning(r.Scope).Put(r) }

// Get returns the record with the given ID, checking Project → Space → Global; the most
// specific level's hit wins.
func (w *Workspace) Get(id string) (Record, bool) {
	for _, s := range w.present() {
		if r, ok := s.Get(id); ok {
			return r, true
		}
	}
	return Record{}, false
}

// Delete removes the ID from every present store (a missing ID is a no-op).
func (w *Workspace) Delete(id string) error {
	for _, s := range w.present() {
		if err := s.Delete(id); err != nil {
			return err
		}
	}
	return nil
}

// List composes records across the present levels. A scope-pinned filter queries only
// the store owning that level; otherwise all present levels are merged and sorted by ID
// so output is deterministic.
func (w *Workspace) List(f Filter) []Record {
	if f.Scope != nil {
		return w.owning(*f.Scope).List(f)
	}
	var out []Record
	for _, s := range w.present() {
		out = append(out, s.List(f)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// compile-time assertion that Workspace satisfies Store.
var _ Store = (*Workspace)(nil)
