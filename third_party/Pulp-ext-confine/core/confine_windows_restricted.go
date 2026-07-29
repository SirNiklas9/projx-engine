//go:build windows

package core

// restrictedTokenConfiner is a PROTOTYPE, env-gated alternative to the
// AppContainer confiner (appcontainerConfiner) for INTERACTIVE launches on
// Windows. It is selected only when PROJX_CONFINE_WIN_INTERACTIVE=1 is set in
// the host environment; otherwise platformConfiner() returns the unchanged,
// working AppContainer cage. This file adds a confiner — it does NOT modify the
// AppContainer path.
//
// Why a second confiner: an AppContainer process cannot use the host terminal's
// console object (its ACL excludes the AppContainer SID), so a TUI such as
// claude.exe launches but renders nothing. A *restricted + Low-integrity*
// primary token is an ordinary process: it inherits the parent's console
// normally (so a TUI can render and take keys) while the mandatory-integrity
// "no write-up" policy walls the filesystem to the project tree.
//
// FS-confinement mechanism (the project-scoped wall):
//   - The child runs at LOW integrity (S-1-16-4096).
//   - The project root and every ReadWrite path are labeled Low via
//     `icacls <path> /setintegritylevel (OI)(CI)Low`, so the Low child may write
//     INSIDE them.
//   - Everything else on the system is >= Medium integrity, so the no-write-up
//     mandatory policy denies all writes outside the labeled paths — unprivileged,
//     no ACL surgery.
//   - Reads down the integrity ladder (Low -> read Medium) remain allowed, so the
//     agent can still read system DLLs / its own binary. ReadOnly paths are
//     therefore a no-op here (read-down is already permitted). To *hide* reads,
//     explicit deny ACEs would be required — out of scope for this prototype.
//
// Token construction is unprivileged: lowering integrity and dropping privileges
// never requires elevation (only *raising* does). Proven on a Medium-IL,
// non-elevated shell.

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	// CreateRestrictedToken is not wrapped by golang.org/x/sys/windows, so it is
	// declared directly against advapi32.dll.
	advapi32                  = windows.NewLazySystemDLL("advapi32.dll")
	procCreateRestrictedToken = advapi32.NewProc("CreateRestrictedToken")
)

// CreateRestrictedToken flags.
const (
	// disableMaxPrivilege drops ALL privileges from the new token.
	disableMaxPrivilege uint32 = 0x1
	// integrityLevelLow is the RID of the Low mandatory integrity SID
	// (S-1-16-4096 == 0x1000).
	integrityLevelLow uint32 = 0x1000
)

type restrictedTokenConfiner struct{}

func (restrictedTokenConfiner) Level() string   { return "os-fs:restricted-token" }
func (restrictedTokenConfiner) Available() bool { return true }

// Apply is a no-op on Windows; confinement is applied at child-process creation
// in LaunchConfined (same contract as the AppContainer confiner).
func (restrictedTokenConfiner) Apply(p Policy) error { return nil }

// LaunchConfined launches argv[0] under a restricted + Low-integrity primary
// token with an INHERITED host console.
//
// Grant rules:
//   - policy.Root and every policy.ReadWrite path -> labeled (OI)(CI)Low so the
//     Low-integrity child may write inside them.
//   - policy.ReadOnly paths -> no-op (Low->read-Medium is already permitted).
//
// It waits for the child and returns its exit code. Fails closed on any error.
func (c restrictedTokenConfiner) LaunchConfined(policy Policy, argv []string, env []string, dir string) (int, error) {
	if len(argv) == 0 {
		return 0, fmt.Errorf("confine/windows-restricted: LaunchConfined: empty argv")
	}

	// 1. Label the project tree (+ extra RW paths) Low so the Low child may write
	//    inside them. Everything else stays >= Medium and is denied by no-write-up.
	if err := labelLowIntegrity(policy); err != nil {
		return 0, fmt.Errorf("confine/windows-restricted: label low integrity: %w", err)
	}

	// 2. Open our own primary token (we will derive a restricted copy from it).
	var hToken windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_DUPLICATE|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_QUERY|
			windows.TOKEN_ADJUST_DEFAULT|windows.TOKEN_ADJUST_GROUPS,
		&hToken); err != nil {
		return 0, fmt.Errorf("confine/windows-restricted: OpenProcessToken: %w", err)
	}
	defer hToken.Close()

	// 3. Create a restricted token: drop all privileges (DISABLE_MAX_PRIVILEGE).
	//    No restricting-SIDs are added — those require the resource ACL to grant a
	//    restricting SID too, which would deny everything (including the agent
	//    binary). The Low integrity wall is the load-bearing FS mechanism.
	//
	// CreateRestrictedToken(existing, flags, disableSidCount, disableSids,
	//   deletePrivCount, deletePrivs, restrictedSidCount, restrictedSids, *newToken)
	var restricted windows.Token
	ret, _, callErr := procCreateRestrictedToken.Call(
		uintptr(hToken),
		uintptr(disableMaxPrivilege),
		0, 0, // disable-SIDs: none
		0, 0, // delete-privileges: none (DISABLE_MAX_PRIVILEGE covers it)
		0, 0, // restricting-SIDs: none
		uintptr(unsafe.Pointer(&restricted)),
	)
	if ret == 0 {
		return 0, fmt.Errorf("confine/windows-restricted: CreateRestrictedToken: %w", callErr)
	}
	defer restricted.Close()

	// 4. Lower the restricted token to LOW integrity (S-1-16-4096).
	if err := setTokenIntegrityLevel(restricted, integrityLevelLow); err != nil {
		return 0, fmt.Errorf("confine/windows-restricted: setTokenIntegrityLevel(LOW): %w", err)
	}

	// 5. Launch the child INHERITING the host console: STARTF_USESTDHANDLES with
	//    os.Std* handles, and explicitly NO CREATE_NEW_CONSOLE / NO ConPTY. This
	//    is precisely why a TUI renders here where it cannot under AppContainer.
	var si windows.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput = windows.Handle(os.Stdin.Fd())
	si.StdOutput = windows.Handle(os.Stdout.Fd())
	si.StdErr = windows.Handle(os.Stderr.Fd())

	cmdLine := buildCmdLine(argv)
	cmdLinePtr, err := windows.UTF16PtrFromString(cmdLine)
	if err != nil {
		return 0, fmt.Errorf("confine/windows-restricted: UTF16 cmdline: %w", err)
	}

	envBlock, err := buildEnvBlock(env)
	if err != nil {
		return 0, fmt.Errorf("confine/windows-restricted: env block: %w", err)
	}

	var dirPtr *uint16
	if dir != "" {
		dirPtr, err = windows.UTF16PtrFromString(dir)
		if err != nil {
			return 0, fmt.Errorf("confine/windows-restricted: UTF16 dir: %w", err)
		}
	}

	var pi windows.ProcessInformation
	if err := windows.CreateProcessAsUser(
		restricted,
		nil,
		cmdLinePtr,
		nil, nil,
		true, // inherit handles -> child shares our console
		windows.CREATE_UNICODE_ENVIRONMENT,
		envBlock,
		dirPtr,
		&si,
		&pi,
	); err != nil {
		return 0, fmt.Errorf("confine/windows-restricted: CreateProcessAsUser %q: %w", argv[0], err)
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)

	if _, waitErr := windows.WaitForSingleObject(pi.Process, windows.INFINITE); waitErr != nil {
		return 0, fmt.Errorf("confine/windows-restricted: WaitForSingleObject: %w", waitErr)
	}

	var exitCode uint32
	if exitErr := windows.GetExitCodeProcess(pi.Process, &exitCode); exitErr != nil {
		return 0, fmt.Errorf("confine/windows-restricted: GetExitCodeProcess: %w", exitErr)
	}
	return int(exitCode), nil
}

// setTokenIntegrityLevel sets the mandatory integrity level of token to the
// given RID (e.g. integrityLevelLow). Builds the S-1-16-<rid> label SID and
// applies it via SetTokenInformation(TokenIntegrityLevel).
func setTokenIntegrityLevel(token windows.Token, rid uint32) error {
	sidStr := fmt.Sprintf("S-1-16-%d", rid)
	p, err := windows.UTF16PtrFromString(sidStr)
	if err != nil {
		return err
	}
	var sid *windows.SID
	if err := windows.ConvertStringSidToSid(p, &sid); err != nil {
		return err
	}
	var tml windows.Tokenmandatorylabel
	tml.Label.Attributes = windows.SE_GROUP_INTEGRITY
	tml.Label.Sid = sid
	return windows.SetTokenInformation(token, windows.TokenIntegrityLevel,
		(*byte)(unsafe.Pointer(&tml)), tml.Size())
}

// labelLowIntegrity applies a Low mandatory-integrity label (inheritable) to the
// project root and every ReadWrite path so the Low-integrity child may write
// inside them. ReadOnly paths are intentionally skipped (read-down is already
// permitted). System paths are skipped defensively — we never relabel Windows or
// Program Files.
func labelLowIntegrity(policy Policy) error {
	seen := map[string]bool{}
	apply := func(p string) error {
		abs, ok := toWindowsAbsPath(p)
		if !ok {
			return nil
		}
		if isWindowsSystemPath(abs) {
			return nil
		}
		lower := strings.ToLower(abs)
		if seen[lower] {
			return nil
		}
		seen[lower] = true
		// icacls /setintegritylevel with object+container inheritance so new files
		// created by the child also inherit Low. Idempotent (re-labeling is safe).
		out, err := exec.Command("icacls", abs, "/setintegritylevel", "(OI)(CI)Low").CombinedOutput()
		if err != nil {
			return fmt.Errorf("icacls /setintegritylevel %q: %v\n%s", abs, err, out)
		}
		return nil
	}

	if err := apply(policy.Root); err != nil {
		return err
	}
	for _, p := range policy.ReadWrite {
		if err := apply(p); err != nil {
			return err
		}
	}
	return nil
}
