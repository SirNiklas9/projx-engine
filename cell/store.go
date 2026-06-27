package main

import (
	"strings"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
	store "github.com/SirNiklas9/projx-store"
)

// storeRecordView is the string-typed record shape the Workbench frontend speaks.
type storeRecordView struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Scope string `json:"scope"`
	Key   string `json:"key"`
	Body  string `json:"body"`
}

func handleStoreList(c *pulpgin.Context) {
	s, err := openStore()
	if err != nil {
		c.JSON(503, pulpgin.H{"error": "store unavailable: " + err.Error()})
		return
	}
	views := []storeRecordView{}
	for _, rec := range s.List(store.Filter{}) {
		views = append(views, storeRecordView{rec.ID, rec.Kind.String(), rec.Scope.String(), rec.Key, rec.Body})
	}
	c.JSON(200, pulpgin.H{"records": views})
}

func handleStorePut(c *pulpgin.Context) {
	var req struct {
		ID    string `json:"id"`
		Kind  int    `json:"kind"`
		Scope int    `json:"scope"`
		Key   string `json:"key"`
		Body  string `json:"body"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(400, pulpgin.H{"error": "bad request"})
		return
	}
	s, err := openStore()
	if err != nil {
		c.JSON(503, pulpgin.H{"error": "store unavailable: " + err.Error()})
		return
	}
	rec := store.Record{ID: req.ID, Kind: store.Kind(req.Kind), Scope: store.Scope(req.Scope), Key: req.Key, Body: req.Body}
	if strings.TrimSpace(rec.ID) == "" {
		base := slugID(rec.Key)
		if base == "" {
			base = slugID(rec.Body)
		}
		if base == "" {
			base = "rec"
		}
		rec.ID = rec.Kind.String() + "/" + base
	}
	before, had := s.Get(rec.ID)
	if err := s.Put(rec); err != nil {
		c.JSON(400, pulpgin.H{"error": err.Error()})
		return
	}
	var bp *store.Record
	if had {
		bp = &before
	}
	recordStoreOp("put", rec.ID, rec.Kind.String(), rec.Key, bp, &rec)
	syncClaudeMD(s)
	c.JSON(200, pulpgin.H{"ok": true})
}

func handleStoreDelete(c *pulpgin.Context) {
	id := c.Query("id")
	if id == "" {
		c.JSON(400, pulpgin.H{"error": "missing id"})
		return
	}
	s, err := openStore()
	if err != nil {
		c.JSON(503, pulpgin.H{"error": "store unavailable: " + err.Error()})
		return
	}
	before, had := s.Get(id)
	if err := s.Delete(id); err != nil {
		c.JSON(500, pulpgin.H{"error": err.Error()})
		return
	}
	if had {
		recordStoreOp("delete", id, before.Kind.String(), before.Key, &before, nil)
	}
	syncClaudeMD(s)
	c.JSON(200, pulpgin.H{"ok": true})
}

func handleStoreHistory(c *pulpgin.Context) {
	revs := readRevisions()
	for i, j := 0, len(revs)-1; i < j; i, j = i+1, j-1 {
		revs[i], revs[j] = revs[j], revs[i]
	}
	if revs == nil {
		revs = []revision{}
	}
	c.JSON(200, pulpgin.H{"revisions": revs})
}

func handleStoreUndo(c *pulpgin.Context) {
	s, err := openStore()
	if err != nil {
		c.JSON(503, pulpgin.H{"error": "store unavailable"})
		return
	}
	rev, ok := undoLast(s)
	if !ok {
		c.JSON(200, pulpgin.H{"ok": false, "msg": "nothing to undo"})
		return
	}
	syncClaudeMD(s)
	c.JSON(200, pulpgin.H{"ok": true, "undid": rev.Seq, "id": rev.ID})
}

// slugID turns a key into an id-safe slug — mirrors the engine/CLI scheme.
func slugID(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// syncClaudeMD regenerates the managed block in CLAUDE.md from the store via the
// shared projx-store renderer, written through storage.fs.
func syncClaudeMD(s store.Store) {
	const path = "CLAUDE.md"
	existing, _ := pulp.FS.Read(path)
	out := store.SpliceManagedBlock(string(existing), store.ManagedBlock(s))
	_ = pulp.FS.Write(path, []byte(out))
}
