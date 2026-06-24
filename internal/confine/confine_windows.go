//go:build windows

package confine

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows AppContainer confinement constants.
const (
	// PROC_THREAD_ATTRIBUTE_SECURITY_CAPABILITIES is the attribute ID used to
	// attach an AppContainer SID to a new process via UpdateProcThreadAttribute.
	procThreadAttributeSecurityCapabilities uintptr = 0x00020009

	// HRESULT for "profile already exists".
	hresultAlreadyExists = int32(-2147023281) // 0x800700B7
)

// securityCapabilities mirrors the Windows SECURITY_CAPABILITIES struct.
type securityCapabilities struct {
	AppContainerSid  *windows.SID
	Capabilities     *windows.SIDAndAttributes
	CapabilityCount  uint32
	Reserved         uint32
}

var (
	userenv = windows.NewLazySystemDLL("userenv.dll")

	procCreateAppContainerProfile = userenv.NewProc("CreateAppContainerProfile")
	procDeriveAppContainerSid     = userenv.NewProc("DeriveAppContainerSidFromAppContainerName")

	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procInitializeProcThreadAttrList  = kernel32.NewProc("InitializeProcThreadAttributeList")
	procUpdateProcThreadAttribute     = kernel32.NewProc("UpdateProcThreadAttribute")
	procDeleteProcThreadAttributeList = kernel32.NewProc("DeleteProcThreadAttributeList")
)

// appcontainerConfiner uses Windows AppContainer to confine a child process.
type appcontainerConfiner struct{}

func (appcontainerConfiner) Level() string   { return "os-fs:appcontainer" }
func (appcontainerConfiner) Available() bool { return true }

// Apply is a no-op on Windows (confinement happens at child-process creation
// via LaunchConfined, not by self-restricting the current process).
func (appcontainerConfiner) Apply(p Policy) error { return nil }

// LaunchConfined launches argv[0] (with argv[1:] as arguments) inside a
// Windows AppContainer, physically preventing it from accessing any filesystem
// path that has not been explicitly granted to the AppContainer SID.
//
// Grant rules:
//   - policy.Root and every policy.ReadWrite path → (OI)(CI)(M) (modify = RW + execute)
//   - policy.ReadOnly paths (excluding Windows system dirs) → (OI)(CI)(RX)
//   - dir(argv[0]) (agent dir) → (OI)(CI)(RX)
//   - PROJX_JAIL_DIR (from env) → (OI)(CI)(RX)
//
// Windows system paths (C:\Windows, Program Files) are SKIPPED for explicit
// grants — they already permit ALL_APPLICATION_PACKAGES by default.
//
// On ANY setup failure this returns an error. The caller MUST fail closed.
func (c appcontainerConfiner) LaunchConfined(policy Policy, argv []string, env []string, dir string) (int, error) {
	if len(argv) == 0 {
		return 0, fmt.Errorf("confine/windows: LaunchConfined: empty argv")
	}

	// ── Step 1: create/derive AppContainer profile ───────────────────────────
	// Use a stable name derived from the policy root so the same root always
	// maps to the same container profile (ACEs accumulate on disk but that is
	// harmless — repeated grants to the same SID are idempotent).
	containerName := appContainerName(policy.Root)
	sid, err := createOrDeriveSID(containerName)
	if err != nil {
		return 0, fmt.Errorf("confine/windows: AppContainer SID: %w", err)
	}
	sidStr := sid.String()

	// ── Step 2: grant SID to the required directories via icacls ────────────
	if err := grantPaths(sidStr, policy, argv, env); err != nil {
		return 0, fmt.Errorf("confine/windows: icacls grant: %w", err)
	}

	// ── Step 3: build SECURITY_CAPABILITIES ─────────────────────────────────
	secCaps := securityCapabilities{
		AppContainerSid: sid,
		Capabilities:    nil,
		CapabilityCount: 0,
		Reserved:        0,
	}

	// ── Step 4: build PROC_THREAD_ATTRIBUTE_LIST ─────────────────────────────
	var attrListSize uintptr
	// First call: query required size.
	procInitializeProcThreadAttrList.Call(0, 1, 0, uintptr(unsafe.Pointer(&attrListSize)))
	attrListBuf := make([]byte, attrListSize)
	ret, _, le := procInitializeProcThreadAttrList.Call(
		uintptr(unsafe.Pointer(&attrListBuf[0])),
		1, 0,
		uintptr(unsafe.Pointer(&attrListSize)),
	)
	if ret == 0 {
		return 0, fmt.Errorf("confine/windows: InitializeProcThreadAttributeList: %w", le)
	}

	ret, _, le = procUpdateProcThreadAttribute.Call(
		uintptr(unsafe.Pointer(&attrListBuf[0])),
		0,
		procThreadAttributeSecurityCapabilities,
		uintptr(unsafe.Pointer(&secCaps)),
		unsafe.Sizeof(secCaps),
		0,
		0,
	)
	if ret == 0 {
		procDeleteProcThreadAttributeList.Call(uintptr(unsafe.Pointer(&attrListBuf[0])))
		return 0, fmt.Errorf("confine/windows: UpdateProcThreadAttribute: %w", le)
	}
	defer procDeleteProcThreadAttributeList.Call(uintptr(unsafe.Pointer(&attrListBuf[0])))

	// ── Step 5: build StartupInfoEx ──────────────────────────────────────────
	var siEx windows.StartupInfoEx
	siEx.StartupInfo.Cb = uint32(unsafe.Sizeof(siEx))
	siEx.StartupInfo.Flags = windows.STARTF_USESTDHANDLES
	siEx.ProcThreadAttributeList = (*windows.ProcThreadAttributeList)(unsafe.Pointer(&attrListBuf[0]))

	// Wire standard handles so stdio passes through to the child.
	siEx.StartupInfo.StdInput = windows.Handle(os.Stdin.Fd())
	siEx.StartupInfo.StdOutput = windows.Handle(os.Stdout.Fd())
	siEx.StartupInfo.StdErr = windows.Handle(os.Stderr.Fd())

	// ── Step 6: build command line ────────────────────────────────────────────
	cmdLine := buildCmdLine(argv)
	cmdLinePtr, err := windows.UTF16PtrFromString(cmdLine)
	if err != nil {
		return 0, fmt.Errorf("confine/windows: UTF16 cmdline: %w", err)
	}

	// ── Step 7: build environment block ──────────────────────────────────────
	envBlock, err := buildEnvBlock(env)
	if err != nil {
		return 0, fmt.Errorf("confine/windows: env block: %w", err)
	}

	// Working dir.
	var dirPtr *uint16
	if dir != "" {
		dirPtr, err = windows.UTF16PtrFromString(dir)
		if err != nil {
			return 0, fmt.Errorf("confine/windows: UTF16 dir: %w", err)
		}
	}

	// ── Step 8: CreateProcess ─────────────────────────────────────────────────
	var pi windows.ProcessInformation
	createErr := windows.CreateProcess(
		nil,
		cmdLinePtr,
		nil,  // process security attrs
		nil,  // thread security attrs
		true, // inherit handles (for stdio)
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT,
		envBlock,
		dirPtr,
		&siEx.StartupInfo,
		&pi,
	)
	if createErr != nil {
		return 0, fmt.Errorf("confine/windows: CreateProcess %q: %w", argv[0], createErr)
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)

	// ── Step 9: wait for child ────────────────────────────────────────────────
	if _, waitErr := windows.WaitForSingleObject(pi.Process, windows.INFINITE); waitErr != nil {
		return 0, fmt.Errorf("confine/windows: WaitForSingleObject: %w", waitErr)
	}

	var exitCode uint32
	if exitErr := windows.GetExitCodeProcess(pi.Process, &exitCode); exitErr != nil {
		return 0, fmt.Errorf("confine/windows: GetExitCodeProcess: %w", exitErr)
	}

	return int(exitCode), nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// appContainerName returns a stable, short container name for the given root
// path (max 64 chars, no spaces, safe for CreateAppContainerProfile).
func appContainerName(root string) string {
	h := sha256.Sum256([]byte(strings.ToLower(root)))
	return fmt.Sprintf("projx-engine-%x", h[:8])
}

// createOrDeriveSID creates the AppContainer profile (or derives the SID if it
// already exists). Both outcomes return a valid SID.
func createOrDeriveSID(name string) (*windows.SID, error) {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	displayPtr, err := windows.UTF16PtrFromString("ProjX Engine Sandbox")
	if err != nil {
		return nil, err
	}
	descPtr, err := windows.UTF16PtrFromString("Filesystem confinement for projx-engine agent")
	if err != nil {
		return nil, err
	}

	var sid *windows.SID
	hr, _, _ := procCreateAppContainerProfile.Call(
		uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(displayPtr)),
		uintptr(unsafe.Pointer(descPtr)),
		0, 0,
		uintptr(unsafe.Pointer(&sid)),
	)
	if hr == 0 {
		return sid, nil
	}
	if int32(hr) == hresultAlreadyExists {
		// Profile exists — derive SID.
		hr2, _, _ := procDeriveAppContainerSid.Call(
			uintptr(unsafe.Pointer(namePtr)),
			uintptr(unsafe.Pointer(&sid)),
		)
		if hr2 != 0 {
			return nil, fmt.Errorf("DeriveAppContainerSidFromAppContainerName: HRESULT 0x%08X", uint32(hr2))
		}
		return sid, nil
	}
	return nil, fmt.Errorf("CreateAppContainerProfile: HRESULT 0x%08X", uint32(hr))
}

// isWindowsSystemPath returns true for paths under C:\Windows or
// C:\Program Files / C:\Program Files (x86) — these already allow
// ALL_APPLICATION_PACKAGES by default and must not be granted explicitly
// (it would pollute ACLs unnecessarily and may be denied by policy).
func isWindowsSystemPath(p string) bool {
	abs := strings.ToLower(filepath.ToSlash(p))
	sysRoot := strings.ToLower(filepath.ToSlash(os.Getenv("SystemRoot")))
	if sysRoot == "" {
		sysRoot = "c:/windows"
	}
	pfx86 := strings.ToLower(filepath.ToSlash(os.Getenv("ProgramFiles(x86)")))
	pf := strings.ToLower(filepath.ToSlash(os.Getenv("ProgramFiles")))

	for _, prefix := range []string{sysRoot, pfx86, pf, "c:/windows", "c:/program files", "c:/program files (x86)"} {
		if prefix != "" && (abs == prefix || strings.HasPrefix(abs, prefix+"/")) {
			return true
		}
	}
	return false
}

// toWindowsAbsPath converts p to an absolute Windows path (backslash form).
// Returns ("", false) if p cannot be made into a Windows-native absolute path
// (e.g., a Unix-only path like /usr, /etc that has no Windows counterpart even
// if Go resolves it via Cygwin/MSYS layer).
func toWindowsAbsPath(p string) (string, bool) {
	if p == "" {
		return "", false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	// A genuine Windows absolute path must start with a drive letter (e.g. C:\).
	// Paths that resolve to /... even after Abs are Unix-layer artefacts
	// (Cygwin/MSYS2/Git Bash /tmp, /usr, etc.) — skip them.
	if len(abs) < 2 || abs[1] != ':' {
		return "", false
	}
	return abs, true
}

// grantPaths runs icacls to grant the AppContainer SID access to all required
// directories. Paths that cannot be resolved to a native Windows absolute path
// (e.g. Unix-layer /tmp, /usr, /etc from Git Bash / MSYS) are silently skipped.
func grantPaths(sidStr string, policy Policy, argv []string, env []string) error {
	type grant struct {
		path string
		rw   bool // true = Modify (RW+X), false = RX (read-execute)
	}
	var grants []grant

	addPath := func(p string, rw bool) {
		abs, ok := toWindowsAbsPath(p)
		if !ok {
			return // skip non-Windows or non-existent paths
		}
		if isWindowsSystemPath(abs) && !rw {
			return // system paths already allow ALL_APPLICATION_PACKAGES
		}
		grants = append(grants, grant{abs, rw})
	}

	// Root + ReadWrite paths → Modify (RW+X).
	addPath(policy.Root, true)
	for _, p := range policy.ReadWrite {
		addPath(p, true)
	}

	// ReadOnly paths → RX (skip system dirs).
	for _, p := range policy.ReadOnly {
		addPath(p, false)
	}

	// Agent dir (dir of argv[0]) → RX so the AppContainer can read+exec the agent.
	if len(argv) > 0 {
		agentDir := filepath.Dir(argv[0])
		if agentDir != "" && agentDir != "." {
			addPath(agentDir, false)
		}
	}

	// PROJX_JAIL_DIR from env → RX (so the agent can exec shims).
	jailDir := ""
	for _, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "PROJX_JAIL_DIR=") {
			jailDir = kv[len("PROJX_JAIL_DIR="):]
			break
		}
	}
	if jailDir != "" {
		addPath(jailDir, false)
	}

	// Deduplicate (prefer rw=true if the same path appears as both).
	// Walk in reverse so the last-added rw entry wins when deduplicating.
	seen := map[string]bool{}
	deduped := grants[:0:0]
	for i := len(grants) - 1; i >= 0; i-- {
		g := grants[i]
		lower := strings.ToLower(g.path)
		if seen[lower] {
			continue
		}
		seen[lower] = true
		deduped = append(deduped, g)
	}

	for _, g := range deduped {
		perm := "(OI)(CI)(RX)"
		if g.rw {
			perm = "(OI)(CI)(M)"
		}
		grantArg := fmt.Sprintf("*%s:%s", sidStr, perm)
		out, err := exec.Command("icacls", g.path, "/grant", grantArg).CombinedOutput()
		if err != nil {
			return fmt.Errorf("icacls %q: %v\n%s", g.path, err, out)
		}
	}
	return nil
}

// buildCmdLine builds a Windows command-line string from argv, quoting
// arguments that contain spaces or special characters.
func buildCmdLine(argv []string) string {
	var sb strings.Builder
	for i, arg := range argv {
		if i > 0 {
			sb.WriteByte(' ')
		}
		needsQuote := strings.ContainsAny(arg, " \t\"")
		if needsQuote {
			sb.WriteByte('"')
			for _, c := range arg {
				if c == '"' {
					sb.WriteByte('\\')
				}
				sb.WriteRune(c)
			}
			sb.WriteByte('"')
		} else {
			sb.WriteString(arg)
		}
	}
	return sb.String()
}

// buildEnvBlock converts a []string env ("KEY=VALUE" pairs) into a
// double-null-terminated UTF-16 block suitable for CreateProcess with
// CREATE_UNICODE_ENVIRONMENT. Each entry is null-terminated; the block ends
// with an extra null (double-null total). Cannot use StringToUTF16 because
// that function panics on embedded NUL characters.
func buildEnvBlock(env []string) (*uint16, error) {
	// Build a flat slice of UTF-16 code units.
	var block []uint16
	for _, kv := range env {
		encoded, err := windows.UTF16FromString(kv)
		if err != nil {
			return nil, fmt.Errorf("buildEnvBlock: UTF16FromString %q: %w", kv, err)
		}
		// UTF16FromString appends a terminating NUL — keep it.
		block = append(block, encoded...)
	}
	// Final terminating NUL (double-null at end of block).
	block = append(block, 0)
	if len(block) == 0 {
		block = []uint16{0, 0}
	}
	return &block[0], nil
}

func platformConfiner() Confiner { return appcontainerConfiner{} }
