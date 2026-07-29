# projx-core

The deterministic root of ProjX. **No AI, no UI** — just code as data.

`core` parses source into a **language-neutral abstract control-flow model** and a
flat **symbol list**, with a **stable identity scheme** that both context-deltas and
verify-snapshots key on. Everything above it (gloss, graph, context, verify) reads
this model; nothing reads a language's native tree.

## Shape

- **`Span`** — a byte range; the editor handoff and the anchor for surgical edits.
- **`Node` / `Kind` / `Slot`** — the abstract model. Control-flow-shaped: structure
  (if / loop / switch / …) is modeled; expressions stay opaque `Slot` text. This is
  why adding a language is cheap.
- **`Symbol` / `SymKind`** — the shallow tier (func / method / type / const / var).
- **`StableID`** — `path::Recv.Name` for methods, `path::Name` otherwise. Keyed on
  name, never line number.
- **`Normalizer`** (normalizer.go) — the agnostic seam. The Go normalizer
  (`lang_go.go`, gotreesitter) is the first impl; tree-sitter normalizers for
  other languages plug in behind the same interface.
- **`Parse`** (cells.go) — the foundation cell: bytes → `*File`.

## Status

Actively built. All five language packs (Go, Python, JavaScript/TypeScript/Astro,
C#, Odin) are live with full test coverage. One external dependency:
`github.com/odvcencio/gotreesitter` (pure-Go tree-sitter runtime — no CGo,
cross-compiles to wasip1). `pith`/`projx` are reference for the approach only,
no code copied.

```sh
go test ./...
```
