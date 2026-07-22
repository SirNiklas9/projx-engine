//go:build !windows

package main

// The headless adapter is Windows-only. This stub keeps cross-platform package
// discovery and `go test ./...` deterministic without producing a release asset.
func main() {}
