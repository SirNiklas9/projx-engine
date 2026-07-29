# projx-verify

The **forbidden-knowledge wall**. Compares the architecture you *declared* (boundary
rules, from `store`) against what the code *actually does* (the call graph, from
`core`) and flags every divergence. **No AI — deterministic; you are the verdict.**

Completes the triangle: `store` *keeps* the architecture, `graph` *shows* it,
`verify` *checks* it.

## Shape

- **`Rule{From, To, Note}`** — a declared boundary: a caller matching `From` must not
  call a callee matching `To` (matched by stable-ID prefix — symbol, file, or module).
- **`Violation{Rule, Edge}`** — a broken rule + the offending call edge.
- **`Check(rules, *core.Project) []Violation`** — the mechanical diff.
- **`RulesFromStore(store.Store) []Rule`** — loads declared rules from the store
  (records of kind `KDeclaredStructure`, JSON body). The seam between declared and actual.

## Status

P5, fresh build. Pure-Go. Reads `core` + `store` (local `replace` for dev).

```sh
go test ./...
```
