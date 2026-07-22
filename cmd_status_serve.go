package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const statusDashboardAddr = "127.0.0.1:47632"

//go:embed status-dashboard/index.html
var statusDashboardHTML []byte

type statusServeOptions struct {
	Addr    string
	Session string
	NoOpen  bool
}

func parseStatusServeOptions(args []string) statusServeOptions {
	opts := statusServeOptions{Addr: "127.0.0.1:0"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--addr":
			if i+1 < len(args) {
				i++
				opts.Addr = strings.TrimSpace(args[i])
			}
		case "--session":
			if i+1 < len(args) {
				i++
				opts.Session = strings.TrimSpace(args[i])
			}
		case "--no-open":
			opts.NoOpen = true
		}
	}
	return opts
}

func runStatusServe(absRoot string, args []string) {
	opts := parseStatusServeOptions(args)
	if opts.Session == "" {
		opts.Session = latestStatusSession(absRoot)
	}
	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		die("status --serve: listen: %v", err)
	}
	defer ln.Close()
	if !isLoopbackListener(ln.Addr()) {
		die("status --serve: refusing non-loopback listener %s", ln.Addr())
	}

	server := &http.Server{
		Handler:           statusDashboardHandler(absRoot, opts.Session),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	url := "http://" + ln.Addr().String() + "/"
	fmt.Printf("ProjX status dashboard: %s\n", url)
	fmt.Println("Press Ctrl+C to stop.")
	if !opts.NoOpen {
		if err := openStatusBrowser(url); err != nil {
			fmt.Fprintf(os.Stderr, "status --serve: browser did not open: %v\n", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		die("status --serve: %v", err)
	}
}

// startStatusServerInProcess attaches the persistent dashboard to the MCP
// process lifecycle. A short-lived SessionStart hook cannot safely own a
// detached child under Windows job containment; the already long-lived MCP
// process can serve the same loopback dashboard without a console or orphan.
func startStatusServerInProcess(absRoot string) error {
	sid := latestStatusSession(absRoot)
	baseURL := "http://" + statusDashboardAddr
	if activateStatusServer(baseURL, absRoot, sid) == nil {
		return nil
	}
	_, _, err := startStatusServerBackground(absRoot, sid, statusDashboardAddr)
	if err != nil {
		// Another MCP may have won the listen race; activate it before failing.
		if activateStatusServer(baseURL, absRoot, sid) == nil {
			return nil
		}
	}
	return err
}

func startStatusServerBackground(absRoot, sid, addr string) (*http.Server, net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	if !isLoopbackListener(ln.Addr()) {
		_ = ln.Close()
		return nil, nil, fmt.Errorf("refusing non-loopback listener %s", ln.Addr())
	}
	server := &http.Server{
		Handler:           statusDashboardHandler(absRoot, sid),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() { _ = server.Serve(ln) }()
	return server, ln, nil
}

func ensureStatusServer(absRoot string, args []string, show bool) error {
	opts := parseStatusServeOptions(args)
	if opts.Addr == "127.0.0.1:0" {
		opts.Addr = statusDashboardAddr
	}
	if opts.Session == "" {
		opts.Session = latestStatusSession(absRoot)
	}
	url := "http://" + opts.Addr
	if activateStatusServer(url, absRoot, opts.Session) == nil {
		if show {
			return openStatusBrowser(url + "/")
		}
		return nil
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}
	childArgs := []string{"--root", absRoot, "status", "--serve", "--addr", opts.Addr}
	if opts.Session != "" {
		childArgs = append(childArgs, "--session", opts.Session)
	}
	cmd := exec.Command(self, childArgs...)
	cmd.Dir = absRoot
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = detachSysProcAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func activateStatusServer(baseURL, root, sid string) error {
	body, _ := json.Marshal(map[string]string{"root": root, "session": sid})
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Post(baseURL+"/api/activate", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("dashboard activate: HTTP %d", resp.StatusCode)
	}
	return nil
}

func latestStatusSession(root string) string {
	home := nearestProjxDir(root)
	if home == "" {
		return ""
	}
	entries, err := os.ReadDir(filepath.Join(home, ".projx"))
	if err != nil {
		return ""
	}
	var newest string
	var newestTime time.Time
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "statusline-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := entry.Info()
		if err == nil && (newest == "" || info.ModTime().After(newestTime)) {
			newest = strings.TrimSuffix(strings.TrimPrefix(name, "statusline-"), ".json")
			newestTime = info.ModTime()
		}
	}
	return newest
}

func isLoopbackListener(addr net.Addr) bool {
	tcp, ok := addr.(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}

type statusDashboardState struct {
	mu      sync.RWMutex
	root    string
	session string
}

func (s *statusDashboardState) current() (string, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.root, s.session
}

func (s *statusDashboardState) activate(root, session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(root) != "" {
		s.root = root
	}
	s.session = session
}

func statusDashboardHandler(root, sid string) http.Handler {
	state := &statusDashboardState{root: root, session: sid}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Write(statusDashboardHTML)
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		activeRoot, session := state.current()
		if q := strings.TrimSpace(r.URL.Query().Get("session")); q != "" {
			session = q
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_ = json.NewEncoder(w).Encode(buildStatusSnapshot(activeRoot, session))
	})
	mux.HandleFunc("POST /api/activate", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Root    string `json:"root"`
			Session string `json:"session"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
			http.Error(w, "invalid activation", http.StatusBadRequest)
			return
		}
		root := strings.TrimSpace(body.Root)
		if root == "" {
			http.Error(w, "root is required", http.StatusBadRequest)
			return
		}
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
		state.activate(root, strings.TrimSpace(body.Session))
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func openStatusBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
