//go:build windows

package hook

import (
	_ "embed"
	"os"
	"path/filepath"
)

// projxhookDLL is the prebuilt, statically-linked hook DLL (the defensive cage
// hook: it IAT-hooks the named-pipe/file APIs to splice the AppContainer-local
// "\LOCAL\" segment into libuv's hardcoded global pipe name so a Node/libuv TUI
// runs under an AppContainer token, and propagates itself across the child tree).
// Source: dll/hook.go — rebuild with:
//
//	cd dll && go build -buildmode=c-shared -ldflags '-linkmode external -extldflags "-static"' -o ../projxhook.dll .
//
//go:embed projxhook.dll
var projxhookDLL []byte

// StageDLL writes the embedded hook DLL into dir and returns its absolute path.
// The caller must grant the AppContainer SID read + path-traverse on it before
// injecting (an AppContainer can only LoadLibrary a DLL it can reach + read).
func StageDLL(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, "projxhook.dll")
	if err := os.WriteFile(p, projxhookDLL, 0o644); err != nil {
		return "", err
	}
	return p, nil
}
