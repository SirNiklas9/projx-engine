//go:build windows

package core

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	hook "github.com/BananaLabs-OSS/Pulp-ext-hook"
	"golang.org/x/sys/windows"
)

// Windows AppContainer confinement constants.
const (
	procThreadAttributeSecurityCapabilities uintptr = 0x00020009
	// procThreadAttributePseudoConsole attaches a pseudoconsole (ConPTY) to the
	// child via the proc-thread attribute list. PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE
	// = ProcThreadAttributeValue(22, FALSE, TRUE, FALSE) = 0x00020016.
	procThreadAttributePseudoConsole uintptr = 0x00020016
	// hresultAlreadyExists is HRESULT_FROM_WIN32(ERROR_ALREADY_EXISTS). Compared
	// against uint32(hr) (the low 32 bits of the syscall return) — the prior
	// int32 form held the wrong value, so the "profile already exists → derive
	// the existing SID" path never matched and every re-run failed.
	hresultAlreadyExists uint32 = 0x800700B7

	// seGroupEnabled activates a capability SID in SECURITY_CAPABILITIES.
	seGroupEnabled uint32 = 0x4

	// internetClientCapabilitySID is the well-known SID for the "internetClient"
	// AppContainer capability (S-1-15-3-1). Granting it allows the container to
	// make outbound TCP connections.
	// NOTE: this grants access to ANY host directly — the egress proxy is
	// cooperative (HTTP(S)_PROXY env), not a hard wall. Tier 2 (WFP per-container
	// firewall) would close this gap but requires admin and is a separate build.
	internetClientCapabilitySID = "S-1-15-3-1"
)

type securityCapabilities struct {
	AppContainerSid *windows.SID
	Capabilities    *windows.SIDAndAttributes
	CapabilityCount uint32
	Reserved        uint32
}

var (
	userenv = windows.NewLazySystemDLL("userenv.dll")

	procCreateAppContainerProfile = userenv.NewProc("CreateAppContainerProfile")
	procDeriveAppContainerSid     = userenv.NewProc("DeriveAppContainerSidFromAppContainerName")

	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procInitializeProcThreadAttrList  = kernel32.NewProc("InitializeProcThreadAttributeList")
	procUpdateProcThreadAttribute     = kernel32.NewProc("UpdateProcThreadAttribute")
	procDeleteProcThreadAttributeList = kernel32.NewProc("DeleteProcThreadAttributeList")

	// ConPTY (pseudoconsole) API, kernel32.dll. Lets us give an AppContainer
	// child a console the parent fully controls (it reads/writes the child's
	// console via anonymous pipes) — the only Windows mechanism that survives
	// AppContainer console-object isolation.
	procCreatePseudoConsole = kernel32.NewProc("CreatePseudoConsole")
	procClosePseudoConsole  = kernel32.NewProc("ClosePseudoConsole")
	procResizePseudoConsole = kernel32.NewProc("ResizePseudoConsole")
)

type appcontainerConfiner struct{}

func (appcontainerConfiner) Level() string   { return "os-fs:appcontainer" }
func (appcontainerConfiner) Available() bool { return true }

// Apply is a no-op on Windows; confinement is applied at child-process creation
// in LaunchConfined.
func (appcontainerConfiner) Apply(p Policy) error { return nil }

// LaunchConfined launches argv[0] inside a Windows AppContainer.
// Grant rules:
//   - policy.Root and every policy.ReadWrite path → Modify (RW+X)
//   - policy.ReadOnly paths (excluding Windows system dirs) → RX
//   - dir(argv[0]) → RX
func (c appcontainerConfiner) LaunchConfined(policy Policy, argv []string, env []string, dir string) (int, error) {
	if len(argv) == 0 {
		return 0, fmt.Errorf("confine/windows: LaunchConfined: empty argv")
	}

	// Enable virtual-terminal processing on the host's real console BEFORE launch
	// so a caged agent that inherits these handles renders its ANSI/VT TUI. A
	// normal program (e.g. claude) enables this itself via SetConsoleMode, but an
	// AppContainer child is blocked from that call — so the host (Medium IL, owns
	// the console) must turn it on. Best-effort; harmless if already on / no console.
	enableVTOnConsole()

	containerName := appContainerName(policy.Root)
	sid, err := createOrDeriveSID(containerName)
	if err != nil {
		return 0, fmt.Errorf("confine/windows: AppContainer SID: %w", err)
	}
	sidStr := sid.String()

	cdbg("granting paths (SID=%s)...", sidStr)
	if err := grantPaths(sidStr, policy, argv, env); err != nil {
		return 0, fmt.Errorf("confine/windows: icacls grant: %w", err)
	}
	cdbg("grantPaths complete")

	// When policy.NetAllow is non-empty the agent needs to reach the local egress
	// proxy. Grant the internetClient capability (S-1-15-3-1) so the AppContainer
	// can make outbound TCP connections. When empty, no capability is granted
	// (full network denial).
	var capBuf []windows.SIDAndAttributes
	if len(policy.NetAllow) > 0 {
		capSIDStr, capStrErr := windows.UTF16PtrFromString(internetClientCapabilitySID)
		if capStrErr != nil {
			return 0, fmt.Errorf("confine/windows: UTF16PtrFromString internetClient SID: %w", capStrErr)
		}
		var capSID *windows.SID
		if capErr := windows.ConvertStringSidToSid(capSIDStr, &capSID); capErr != nil {
			return 0, fmt.Errorf("confine/windows: ConvertStringSidToSid internetClient: %w", capErr)
		}
		capBuf = []windows.SIDAndAttributes{
			{Sid: capSID, Attributes: seGroupEnabled},
		}
	}

	var capsPtr *windows.SIDAndAttributes
	var capsCount uint32
	if len(capBuf) > 0 {
		capsPtr = &capBuf[0]
		capsCount = uint32(len(capBuf))
	}

	secCaps := securityCapabilities{
		AppContainerSid: sid,
		Capabilities:    capsPtr,
		CapabilityCount: capsCount,
		Reserved:        0,
	}

	// Interactive caged agents (a TUI like claude) need a console the
	// AppContainer is actually allowed to access. An AppContainer process CANNOT
	// use the parent terminal's console object — its ACL excludes the
	// AppContainer SID — so passing inherited std handles (STARTF_USESTDHANDLES)
	// yields a process that runs but can't render or read its TUI (observed:
	// claude.exe launches, stays alive, draws nothing).
	//
	// Three modes, in precedence order:
	//   1. PROJX_CONFINE_CONPTY=1 — attach a pseudoconsole (ConPTY) to the child
	//      and relay its I/O to the host's REAL terminal. This is the OS-standard
	//      mechanism for giving a child a console the parent controls; the child
	//      renders into the host terminal in place. Takes precedence.
	//   2. PROJX_CONFINE_NEW_CONSOLE=1 — give the child its OWN console window via
	//      CREATE_NEW_CONSOLE (it opens a separate window it owns). Fallback when
	//      ConPTY is off.
	//   3. PROJX_CONFINE_HEADLESS=1 — one-shot / non-interactive agents
	//      (`claude -p`, caged subagents) launched by a CONSOLE-LESS host (the
	//      cell's HTTP server). Give the child explicit NUL std handles via
	//      STARTF_USESTDHANDLES + CREATE_NO_WINDOW so it neither needs nor waits on
	//      a console. The inherited-console default (mode 4) HANGS when the host has
	//      no console; this is the headless equivalent of the WSL/Landlock path
	//      (the agent reports results by committing to the store, so stdout capture
	//      is not required).
	//   4. default (none set) — inherited std handles. Correct for interactive
	//      launches from a real terminal and the test suite (captures child output).
	// Mode is chosen PER-LAUNCH: the flag is read from the child env first (so the
	// cell can pick headless vs interactive per request via policy.Env), falling
	// back to the host process env.
	conpty := launchFlag(env, "PROJX_CONFINE_CONPTY")
	newConsole := !conpty && launchFlag(env, "PROJX_CONFINE_NEW_CONSOLE")
	headless := !conpty && !newConsole && launchFlag(env, "PROJX_CONFINE_HEADLESS")

	// attrCount is the number of proc-thread attributes: the AppContainer
	// security-capabilities attribute is always present; ConPTY adds a second.
	attrCount := uint32(1)
	if conpty {
		attrCount = 2
	}

	var attrListSize uintptr
	procInitializeProcThreadAttrList.Call(0, uintptr(attrCount), 0, uintptr(unsafe.Pointer(&attrListSize)))
	attrListBuf := make([]byte, attrListSize)
	ret, _, le := procInitializeProcThreadAttrList.Call(
		uintptr(unsafe.Pointer(&attrListBuf[0])),
		uintptr(attrCount), 0,
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
		return 0, fmt.Errorf("confine/windows: UpdateProcThreadAttribute(secCaps): %w", le)
	}
	defer procDeleteProcThreadAttributeList.Call(uintptr(unsafe.Pointer(&attrListBuf[0])))

	// ConPTY plumbing. Populated only when conpty is true; cleaned up here so the
	// relay can run after CreateProcess and the handles close when LaunchConfined
	// returns.
	var (
		hpc           windows.Handle // HPCON
		ptyInRead     windows.Handle // child reads (stdin into the pty); closed in parent post-spawn
		ptyInWrite    windows.Handle // parent writes user keystrokes here
		ptyOutRead    windows.Handle // parent reads child output here
		ptyOutWrite   windows.Handle // child writes (stdout/err from the pty); closed in parent post-spawn
		restoreConIn  func()
		restoreConOut func()
	)
	if conpty {
		// Anonymous pipes. Input pipe: host writes (ptyInWrite) / child reads
		// (ptyInRead). Output pipe: child writes (ptyOutWrite) / host reads
		// (ptyOutRead).
		if err := windows.CreatePipe(&ptyInRead, &ptyInWrite, nil, 0); err != nil {
			return 0, fmt.Errorf("confine/windows: CreatePipe(in): %w", err)
		}
		if err := windows.CreatePipe(&ptyOutRead, &ptyOutWrite, nil, 0); err != nil {
			windows.CloseHandle(ptyInRead)
			windows.CloseHandle(ptyInWrite)
			return 0, fmt.Errorf("confine/windows: CreatePipe(out): %w", err)
		}

		// Size the pseudoconsole from the real console; fall back to 120x30.
		coord := windows.Coord{X: 120, Y: 30}
		var csbi windows.ConsoleScreenBufferInfo
		if err := windows.GetConsoleScreenBufferInfo(windows.Handle(os.Stdout.Fd()), &csbi); err == nil {
			w := csbi.Window.Right - csbi.Window.Left + 1
			h := csbi.Window.Bottom - csbi.Window.Top + 1
			if w > 0 && h > 0 {
				coord = windows.Coord{X: w, Y: h}
			}
		}

		// CreatePseudoConsole(COORD size, HANDLE hInput, HANDLE hOutput, DWORD
		// flags, HPCON* phPC) -> HRESULT. COORD is two int16 packed into a DWORD.
		coordDW := uintptr(uint32(uint16(coord.X)) | uint32(uint16(coord.Y))<<16)
		hr, _, _ := procCreatePseudoConsole.Call(
			coordDW,
			uintptr(ptyInRead),
			uintptr(ptyOutWrite),
			0,
			uintptr(unsafe.Pointer(&hpc)),
		)
		if hr != 0 {
			windows.CloseHandle(ptyInRead)
			windows.CloseHandle(ptyInWrite)
			windows.CloseHandle(ptyOutRead)
			windows.CloseHandle(ptyOutWrite)
			return 0, fmt.Errorf("confine/windows: CreatePseudoConsole: HRESULT 0x%08X", uint32(hr))
		}

		// Second attribute: PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE carrying the HPCON.
		// lpValue MUST be the HPCON handle VALUE itself — uintptr(hpc) — exactly as
		// Microsoft's EchoCon sample passes hPC (not &hPC) with cbSize=sizeof(HPCON).
		// The prior form passed uintptr(unsafe.Pointer(&hpc)) (the ADDRESS of the
		// handle variable), so the proc-thread attribute stored a pointer to a host
		// stack variable as the pseudoconsole handle. At CreateProcess the child then
		// attached to a bogus console object: it DLL-init-failed with 0xC0000142 (or,
		// when it did spawn, the pseudoconsole's output pipe never produced bytes —
		// the host read 0 from ptyOutRead and an interactive TUI rendered nothing).
		// Proven in scratchpad/conpty-relay: &hpc => exit 0xC0000142 + 0-byte capture;
		// uintptr(hpc) => child runs + full pseudoconsole VT stream captured.
		ret, _, le = procUpdateProcThreadAttribute.Call(
			uintptr(unsafe.Pointer(&attrListBuf[0])),
			0,
			procThreadAttributePseudoConsole,
			uintptr(hpc),
			unsafe.Sizeof(hpc),
			0,
			0,
		)
		if ret == 0 {
			procClosePseudoConsole.Call(uintptr(hpc))
			windows.CloseHandle(ptyInRead)
			windows.CloseHandle(ptyInWrite)
			windows.CloseHandle(ptyOutRead)
			windows.CloseHandle(ptyOutWrite)
			return 0, fmt.Errorf("confine/windows: UpdateProcThreadAttribute(conpty): %w", le)
		}

		// Put the host's REAL terminal into VT mode so the relayed escape
		// sequences render, and stdin into raw VT-input so keystrokes pass
		// through unbuffered. Saved + restored on return.
		stdoutH := windows.Handle(os.Stdout.Fd())
		stdinH := windows.Handle(os.Stdin.Fd())
		var origOut uint32
		if windows.GetConsoleMode(stdoutH, &origOut) == nil {
			newOut := origOut | windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING | windows.ENABLE_PROCESSED_OUTPUT | windows.DISABLE_NEWLINE_AUTO_RETURN
			windows.SetConsoleMode(stdoutH, newOut)
			restoreConOut = func() { windows.SetConsoleMode(stdoutH, origOut) }
		}
		var origIn uint32
		if windows.GetConsoleMode(stdinH, &origIn) == nil {
			// Clear LINE/ECHO/PROCESSED input: outer terminal does no cooked-mode
			// processing — keys pass through to the inner ConPTY as raw VT.
			newIn := (origIn | windows.ENABLE_VIRTUAL_TERMINAL_INPUT) &^
				(windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT)
			windows.SetConsoleMode(stdinH, newIn)
			restoreConIn = func() { windows.SetConsoleMode(stdinH, origIn) }
		}
		if restoreConOut != nil {
			defer restoreConOut()
		}
		if restoreConIn != nil {
			defer restoreConIn()
		}
		_ = procResizePseudoConsole // referenced for future SIGWINCH-style resize wiring
	}

	var siEx windows.StartupInfoEx
	siEx.StartupInfo.Cb = uint32(unsafe.Sizeof(siEx))
	siEx.ProcThreadAttributeList = (*windows.ProcThreadAttributeList)(unsafe.Pointer(&attrListBuf[0]))

	// Default interactive mode: let the AppContainer child inherit the host's
	// console NATURALLY — do NOT set STARTF_USESTDHANDLES. Passing explicit console
	// handles yields "invalid handle" inside the AppContainer (Node's
	// isTTY/GetConsoleMode then fails and a TUI renders nothing — the observed
	// blank-claude bug). Without STARTF_USESTDHANDLES, conhost assigns the child
	// fresh, VALID console std handles. (Headless output redirection — passing
	// pipe/file handles for a non-interactive `claude -p` — is a separate future
	// mode; the interactive cage is the goal here.)
	_ = newConsole
	_ = conpty

	// Headless mode: explicit NUL std handles so the child has valid (non-console)
	// stdio and never blocks on a console the console-less host can't provide.
	var nulHandle windows.Handle
	if headless {
		nul, nerr := openInheritableNUL()
		if nerr != nil {
			return 0, fmt.Errorf("confine/windows: open NUL for headless: %w", nerr)
		}
		nulHandle = nul
		siEx.StartupInfo.Flags |= windows.STARTF_USESTDHANDLES
		siEx.StartupInfo.StdInput = nulHandle
		siEx.StartupInfo.StdOutput = nulHandle
		siEx.StartupInfo.StdErr = nulHandle
	}
	defer func() {
		if nulHandle != 0 {
			windows.CloseHandle(nulHandle)
		}
	}()

	cmdLine := buildCmdLine(argv)
	cmdLinePtr, err := windows.UTF16PtrFromString(cmdLine)
	if err != nil {
		return 0, fmt.Errorf("confine/windows: UTF16 cmdline: %w", err)
	}

	envBlock, err := buildEnvBlock(env)
	if err != nil {
		return 0, fmt.Errorf("confine/windows: env block: %w", err)
	}

	var dirPtr *uint16
	if dir != "" {
		dirPtr, err = windows.UTF16PtrFromString(dir)
		if err != nil {
			return 0, fmt.Errorf("confine/windows: UTF16 dir: %w", err)
		}
	}

	creationFlags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	if newConsole {
		creationFlags |= windows.CREATE_NEW_CONSOLE
	}
	if headless {
		// No console at all — the child has explicit NUL std handles.
		creationFlags |= windows.CREATE_NO_WINDOW
	}
	if conpty {
		// Interactive: create SUSPENDED so the libuv-pipe hook (projxhook) can be
		// injected + armed before the Node/libuv TUI executes a single instruction.
		creationFlags |= windows.CREATE_SUSPENDED
	}

	// bInheritHandles: for ConPTY, FALSE — the child must receive ONLY the
	// pseudoconsole's console handles (via the PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE
	// attribute), NOT inherit the host's std handles. Inheriting them gave the
	// child a non-console fd-1 (GetConsoleMode failed → Node's isTTY=undefined →
	// Ink/claude rendered nothing) — this matches Microsoft's EchoCon sample,
	// which passes FALSE. For the non-ConPTY paths the child inherits the console
	// naturally, so TRUE.
	inheritHandles := !conpty
	var pi windows.ProcessInformation
	createErr := windows.CreateProcess(
		nil,
		cmdLinePtr,
		nil,
		nil,
		inheritHandles,
		creationFlags,
		envBlock,
		dirPtr,
		&siEx.StartupInfo,
		&pi,
	)
	if createErr != nil {
		if conpty {
			procClosePseudoConsole.Call(uintptr(hpc))
			windows.CloseHandle(ptyInRead)
			windows.CloseHandle(ptyInWrite)
			windows.CloseHandle(ptyOutRead)
			windows.CloseHandle(ptyOutWrite)
		}
		return 0, fmt.Errorf("confine/windows: CreateProcess %q: %w", argv[0], createErr)
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)
	cdbg("CreateProcess ok pid=%d headless=%v conpty=%v newConsole=%v", pi.ProcessId, headless, conpty, newConsole)

	if conpty {
		// Arm the AppContainer libuv-pipe hook BEFORE the (suspended) child runs:
		// stage the hook DLL inside the already-granted project root (so the AC SID
		// can read+exec it), inject it, and arm it (IAT-splice "\LOCAL\" into
		// libuv's pipe name + CreateProcess child-propagation). This is what lets a
		// Node/libuv TUI (claude) work under the AppContainer token. Best-effort:
		// on any failure the child still resumes (it just won't have the pipe fix —
		// a libuv TUI would then spin, so this is logged loudly).
		if dllPath, serr := hook.StageDLL(filepath.Join(policy.Root, ".projx")); serr == nil {
			if ierr := hook.Inject(uintptr(pi.Process), dllPath); ierr != nil {
				cdbg("hook inject FAILED (libuv TUI may spin): %v", ierr)
			} else {
				cdbg("hook injected + armed")
			}
		} else {
			cdbg("hook StageDLL failed: %v", serr)
		}
		windows.ResumeThread(pi.Thread)
	}

	if conpty {
		// The child/pseudoconsole now own duplicates of the child-side pipe ends
		// (input read, output write); close the parent's copies so EOF propagates
		// correctly. Keep the host-side ends (input write, output read) for the
		// relay.
		windows.CloseHandle(ptyInRead)
		windows.CloseHandle(ptyOutWrite)

		// Relay: pump child output → host stdout, and host stdin → child input.
		// os.NewFile wraps the raw handles so io.Copy can drive them. Both pumps
		// unblock when their handle closes (output pump on child exit closing the
		// pty's write end; input pump when ptyInWrite is closed below).
		outFile := os.NewFile(uintptr(ptyOutRead), "conpty-out")
		inFile := os.NewFile(uintptr(ptyInWrite), "conpty-in")

		// Diagnostic tee: when PROJX_CONPTY_TEE is set, mirror everything the
		// pseudoconsole emits (the agent's raw VT/TUI stream) to that file so we can
		// see whether the agent is actually drawing.
		var sink io.Writer = os.Stdout
		var teeF *os.File
		if tp := os.Getenv("PROJX_CONPTY_TEE"); tp != "" {
			if f, e := os.Create(tp); e == nil {
				teeF = f
				sink = io.MultiWriter(os.Stdout, f)
			}
		}

		// Filter the relayed OUTPUT so the inner pseudoconsole's win32-input and
		// focus-reporting mode-sets never reach the OUTER terminal. The inner
		// ConPTY emits ESC[?9001h (Win32 Input Mode) and ESC[?1004h (focus
		// reporting) at startup; if relayed through, the outer terminal switches
		// ITS own stdin into win32-input/focus mode and floods the relay with
		// ESC[..._ key records and ESC[I / ESC[O focus events, which then
		// double-encode against the inner ConPTY into on-screen garbage. Stripping
		// the four mode-sets (ESC[?9001h/l, ESC[?1004h/l) keeps the outer terminal
		// a plain raw-VT pipe while the inner ConPTY still translates the child's
		// input. The filter is stateful so a mode-set split across reads is still
		// caught; the diagnostic tee therefore captures the filtered stream.
		outW := &win32InputFilter{w: sink}

		outDone := make(chan struct{})
		go func() {
			io.Copy(outW, outFile)
			close(outDone)
		}()
		go func() {
			io.Copy(inFile, os.Stdin)
		}()
		_ = teeF

		if _, waitErr := windows.WaitForSingleObject(pi.Process, windows.INFINITE); waitErr != nil {
			procClosePseudoConsole.Call(uintptr(hpc))
			outFile.Close()
			inFile.Close()
			return 0, fmt.Errorf("confine/windows: WaitForSingleObject: %w", waitErr)
		}

		// Child exited. Closing the pseudoconsole signals the output pipe's write
		// side to drain/close so the output pump reaches EOF; wait for it, then
		// tear down the host-side pipe ends (this also unblocks the input pump,
		// which is parked on os.Stdin).
		procClosePseudoConsole.Call(uintptr(hpc))
		<-outDone
		outFile.Close()
		inFile.Close()
		if teeF != nil {
			teeF.Close()
		}

		var exitCode uint32
		if exitErr := windows.GetExitCodeProcess(pi.Process, &exitCode); exitErr != nil {
			return 0, fmt.Errorf("confine/windows: GetExitCodeProcess: %w", exitErr)
		}
		return int(exitCode), nil
	}

	cdbg("waiting for child exit (non-conpty)")
	if _, waitErr := windows.WaitForSingleObject(pi.Process, windows.INFINITE); waitErr != nil {
		return 0, fmt.Errorf("confine/windows: WaitForSingleObject: %w", waitErr)
	}

	var exitCode uint32
	if exitErr := windows.GetExitCodeProcess(pi.Process, &exitCode); exitErr != nil {
		return 0, fmt.Errorf("confine/windows: GetExitCodeProcess: %w", exitErr)
	}
	cdbg("child exited code=%d", exitCode)

	return int(exitCode), nil
}

// RunConfinedLaunch is a no-op stub on Windows; confinement happens at
// LaunchConfined (child-process creation), not by self-restricting the
// current process.
func RunConfinedLaunch(policy Policy, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "pulp-ext-confine: confined-launch: no command given")
		os.Exit(1)
	}
	// On Windows we just exec the command directly using os/exec; AppContainer
	// is created at LaunchConfined time, not in a self-exec child.
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ex, ok := err.(*exec.ExitError); ok {
			os.Exit(ex.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "pulp-ext-confine: confined-launch: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// launchFlag reports whether a PROJX_CONFINE_* mode flag is set to "1", checking
// the per-launch child env first (so a caller can choose the mode per request)
// and falling back to the host process environment.
func launchFlag(env []string, key string) bool {
	pfx := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, pfx) {
			return e[len(pfx):] == "1"
		}
	}
	return os.Getenv(key) == "1"
}

// openInheritableNUL opens the NUL device with an inheritable handle, usable as
// the child's std in/out/err in headless mode (valid, non-console, discards I/O).
func openInheritableNUL() (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString("NUL")
	if err != nil {
		return 0, err
	}
	sa := &windows.SecurityAttributes{InheritHandle: 1}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	return windows.CreateFile(
		p,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		sa,
		windows.OPEN_EXISTING,
		0,
		0,
	)
}

// cdbg emits a confiner trace line to stderr when PROJX_CONFINE_DEBUG is set.
// Silent (zero cost beyond an env lookup) otherwise.
func cdbg(format string, a ...any) {
	if os.Getenv("PROJX_CONFINE_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[confine-dbg] "+format+"\n", a...)
	}
}

func appContainerName(root string) string {
	h := sha256.Sum256([]byte(strings.ToLower(root)))
	return fmt.Sprintf("pulp-confine-%x", h[:8])
}

// enableVTOnConsole turns on ENABLE_VIRTUAL_TERMINAL_PROCESSING (stdout/stderr)
// and ENABLE_VIRTUAL_TERMINAL_INPUT (stdin) on the host's real console so a caged
// agent inheriting these handles can render its ANSI TUI and receive VT input.
// Best-effort: silently no-ops if the handles aren't consoles.
func enableVTOnConsole() {
	const (
		enableVTProcessing = 0x0004
		enableVTInput      = 0x0200
	)
	for _, fd := range []uintptr{os.Stdout.Fd(), os.Stderr.Fd()} {
		h := windows.Handle(fd)
		var m uint32
		if windows.GetConsoleMode(h, &m) == nil {
			_ = windows.SetConsoleMode(h, m|enableVTProcessing)
		}
	}
	h := windows.Handle(os.Stdin.Fd())
	var m uint32
	if windows.GetConsoleMode(h, &m) == nil {
		_ = windows.SetConsoleMode(h, m|enableVTInput)
	}
}

func createOrDeriveSID(name string) (*windows.SID, error) {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	displayPtr, err := windows.UTF16PtrFromString("Pulp Confine Sandbox")
	if err != nil {
		return nil, err
	}
	descPtr, err := windows.UTF16PtrFromString("Filesystem confinement for Pulp-ext-confine")
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
	if uint32(hr) == hresultAlreadyExists {
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

func toWindowsAbsPath(p string) (string, bool) {
	if p == "" {
		return "", false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	if len(abs) < 2 || abs[1] != ':' {
		return "", false
	}
	return abs, true
}

func grantPaths(sidStr string, policy Policy, argv []string, env []string) error {
	type grant struct {
		path string
		rw   bool
	}
	var grants []grant

	addPath := func(p string, rw bool) {
		abs, ok := toWindowsAbsPath(p)
		if !ok {
			return
		}
		if isWindowsSystemPath(abs) && !rw {
			return
		}
		grants = append(grants, grant{abs, rw})
	}

	addPath(policy.Root, true)
	for _, p := range policy.ReadWrite {
		addPath(p, true)
	}
	for _, p := range policy.ReadOnly {
		addPath(p, false)
	}
	if len(argv) > 0 {
		agentDir := filepath.Dir(argv[0])
		if agentDir != "" && agentDir != "." {
			addPath(agentDir, false)
		}
	}

	// PROJX_JAIL_DIR from env → RX so the agent can exec jail shims.
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
		cdbg("main grant path=%q rw=%v", g.path, g.rw)
		out, err := exec.Command("icacls", g.path, "/grant", grantArg).CombinedOutput()
		if err != nil {
			return fmt.Errorf("icacls %q: %v\n%s", g.path, err, out)
		}
	}

	// Node and other runtimes realpath the full ancestor chain of the script,
	// the agent binary, and cwd up to the drive root (lstat C:\, C:\Users, …).
	// The AppContainer must be able to TRAVERSE those ancestors or path
	// resolution fails with EPERM (observed: Node dies at resolveMainPath lstat
	// 'C:\'). Grant non-inheriting (RX) — traverse+list of the dir itself, NOT
	// recursive into its contents — on each ancestor of root + the agent dir.
	// Best-effort: system roots (C:\, C:\Users) usually already grant ALL
	// APPLICATION PACKAGES, and may be unmodifiable unprivileged; ignore failures.
	cdbg("main grants done; traversing ancestors")
	traversed := map[string]bool{}
	grantTraverse := func(start string) {
		abs, ok := toWindowsAbsPath(start)
		if !ok {
			return
		}
		for dir := filepath.Dir(abs); ; {
			lower := strings.ToLower(dir)
			if traversed[lower] {
				break
			}
			traversed[lower] = true
			cdbg("traverse grant dir=%q", dir)
			// Best-effort + BOUNDED. Some standard ancestors stall icacls — most
			// notably C:\Users\<u>\AppData\Local, whose legacy "Application Data"
			// junction self-references AppData\Local, plus large/locked profile
			// trees. These dirs already grant ALL APPLICATION PACKAGES traverse,
			// so a grant that times out is safe to skip rather than hang the whole
			// caged launch forever. Cap each grant; on timeout, stop climbing (every
			// ancestor above is also a system/profile dir that already permits app
			// packages).
			tctx, tcancel := context.WithTimeout(context.Background(), 4*time.Second)
			tErr := exec.CommandContext(tctx, "icacls", dir, "/grant", fmt.Sprintf("*%s:(RX)", sidStr)).Run()
			timedOut := tctx.Err() == context.DeadlineExceeded
			tcancel()
			if timedOut {
				cdbg("traverse grant dir=%q TIMED OUT — stopping ancestor climb", dir)
				break
			}
			if tErr != nil {
				cdbg("traverse grant dir=%q failed (ignored): %v", dir, tErr)
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break // reached the volume root
			}
			dir = parent
		}
	}
	grantTraverse(policy.Root)
	if len(argv) > 0 {
		grantTraverse(argv[0])
	}
	cdbg("grantPaths returning")
	return nil
}

// buildCmdLine serialises argv into a single Windows command-line string
// following the CommandLineToArgvW quoting rules:
//
//   - An argument that contains spaces, tabs, or literal double-quotes is
//     wrapped in double-quotes.
//   - Inside a quoted segment, backslashes are doubled only when they
//     immediately precede a double-quote (literal or the closing quote).
//   - Backslashes that do NOT precede a double-quote are passed through
//     unchanged.
//
// The naive earlier implementation only escaped the quote character itself,
// leaving trailing backslashes (e.g. a Windows path ending in `\`) before the
// closing quote unescaped.  CommandLineToArgvW interprets `\"` as an escaped
// literal quote, so `"C:\foo\"` was parsed as an unclosed quoted string rather
// than the intended argument `C:\foo\`.
func buildCmdLine(argv []string) string {
	var sb strings.Builder
	for i, arg := range argv {
		if i > 0 {
			sb.WriteByte(' ')
		}
		needsQuote := strings.ContainsAny(arg, " \t\"") || arg == ""
		if !needsQuote {
			sb.WriteString(arg)
			continue
		}
		sb.WriteByte('"')
		slashes := 0
		for _, c := range arg {
			switch c {
			case '\\':
				slashes++
			case '"':
				// Emit 2*slashes backslashes (each must be escaped) then \".
				for j := 0; j < slashes*2; j++ {
					sb.WriteByte('\\')
				}
				slashes = 0
				sb.WriteString(`\"`)
			default:
				// Backslashes before a non-quote char are literal.
				for j := 0; j < slashes; j++ {
					sb.WriteByte('\\')
				}
				slashes = 0
				sb.WriteRune(c)
			}
		}
		// Trailing backslashes before the closing quote must be doubled.
		for j := 0; j < slashes*2; j++ {
			sb.WriteByte('\\')
		}
		sb.WriteByte('"')
	}
	return sb.String()
}

// buildEnvBlock converts env (a slice of "KEY=VALUE" strings) into the
// UTF-16LE double-null–terminated environment block that CreateProcess expects.
//
// Each string is null-terminated by UTF16FromString; the final extra null
// creates the mandatory double-null block terminator.  An empty env slice
// must produce exactly [0x0000, 0x0000] (two null code units); the previous
// implementation produced only [0x0000] because the `len(block) == 0` guard
// was unreachable (block already had the first null appended before the check).
func buildEnvBlock(env []string) (*uint16, error) {
	// An empty environment is represented as a block containing only the
	// double-null terminator.
	if len(env) == 0 {
		return &[]uint16{0, 0}[0], nil
	}
	var block []uint16
	for _, kv := range env {
		encoded, err := windows.UTF16FromString(kv)
		if err != nil {
			return nil, fmt.Errorf("buildEnvBlock: UTF16FromString %q: %w", kv, err)
		}
		block = append(block, encoded...)
	}
	// Each kv is already null-terminated by UTF16FromString.  Append one more
	// null to form the required double-null block terminator.
	block = append(block, 0)
	return &block[0], nil
}

// platformConfiner selects the Windows confiner. The default is the AppContainer
// cage (unchanged). When PROJX_CONFINE_WIN_INTERACTIVE=1 is set, the PROTOTYPE
// restricted-token + Low-integrity confiner is used instead, which keeps an
// inherited console for interactive TUIs (see confine_windows_restricted.go).
func platformConfiner() Confiner {
	if os.Getenv("PROJX_CONFINE_WIN_INTERACTIVE") == "1" {
		return restrictedTokenConfiner{}
	}
	return appcontainerConfiner{}
}

// win32InputFilter is a stateful io.Writer that strips the inner pseudoconsole's
// win32-input / focus-reporting DEC private mode-sets from the relayed OUTPUT
// stream before it reaches the OUTER terminal. It drops exactly these four
// sequences:
//
//	ESC[?9001h / ESC[?9001l  — Win32 Input Mode (CSI ? 9001 h/l)
//	ESC[?1004h / ESC[?1004l  — focus event reporting (CSI ? 1004 h/l)
//
// Without the strip, the inner ConPTY's startup ESC[?9001h/ESC[?1004h would
// switch the OUTER terminal into win32-input/focus mode; the outer terminal then
// floods the relay with ESC[..._ key-event records and ESC[I/ESC[O focus events
// that double-encode against the inner ConPTY into garbage. The inner ConPTY
// still does its own input translation for the child — only the OUTER terminal
// is kept in plain raw-VT mode.
//
// Every other byte (including all other CSI/DEC private sequences such as
// ESC[?25l cursor-hide, ESC[?1049h alt-screen, ESC[2J, OSC title sets, and all
// cursor-addressing / box-drawing) passes through verbatim. The parser is a
// byte-at-a-time state machine, so a target mode-set split across two reads is
// still recognised and dropped.
type win32InputFilter struct {
	w     io.Writer
	state int    // fsNormal | fsEsc | fsCSI | fsPrivate
	hold  []byte // candidate sequence bytes buffered pending a keep/drop decision
}

const (
	fsNormal  = iota // copying bytes through
	fsEsc            // saw ESC, expecting '['
	fsCSI            // saw ESC[, expecting '?' (a DEC private intro)
	fsPrivate        // inside ESC[? ... collecting params until the final byte
)

func (f *win32InputFilter) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p))
	for _, b := range p {
		f.feed(b, &out)
	}
	if len(out) > 0 {
		if _, err := f.w.Write(out); err != nil {
			return 0, err
		}
	}
	// Report the full input length consumed: held bytes are buffered internally,
	// not dropped from the caller's accounting (io.Copy treats a short write as an
	// error).
	return len(p), nil
}

func (f *win32InputFilter) feed(b byte, out *[]byte) {
restart:
	switch f.state {
	case fsNormal:
		if b == 0x1b { // ESC
			f.state = fsEsc
			f.hold = append(f.hold[:0], b)
			return
		}
		*out = append(*out, b)
	case fsEsc:
		if b == '[' {
			f.state = fsCSI
			f.hold = append(f.hold, b)
			return
		}
		// Not a CSI; flush the held ESC and reprocess b from scratch (it may itself
		// be another ESC starting a fresh sequence).
		*out = append(*out, f.hold...)
		f.hold = f.hold[:0]
		f.state = fsNormal
		goto restart
	case fsCSI:
		if b == '?' {
			f.state = fsPrivate
			f.hold = append(f.hold, b)
			return
		}
		// A non-private CSI (e.g. ESC[2J, ESC[H, ESC[m); not a target. Flush and
		// reprocess b so the rest of the sequence passes through untouched.
		*out = append(*out, f.hold...)
		f.hold = f.hold[:0]
		f.state = fsNormal
		goto restart
	case fsPrivate:
		// Collect parameter (0x30-0x3f) and intermediate (0x20-0x2f) bytes.
		if b >= 0x20 && b <= 0x3f {
			f.hold = append(f.hold, b)
			if len(f.hold) > 64 { // malformed/oversized — give up and flush
				*out = append(*out, f.hold...)
				f.hold = f.hold[:0]
				f.state = fsNormal
			}
			return
		}
		// Final byte (0x40-0x7e) terminates a CSI sequence.
		if b >= 0x40 && b <= 0x7e {
			if b == 'h' || b == 'l' {
				params := string(f.hold[3:]) // strip ESC '[' '?'
				if params == "9001" || params == "1004" {
					// Drop the entire mode-set including its terminator.
					f.hold = f.hold[:0]
					f.state = fsNormal
					return
				}
			}
			// Any other private sequence: emit it whole.
			*out = append(*out, f.hold...)
			*out = append(*out, b)
			f.hold = f.hold[:0]
			f.state = fsNormal
			return
		}
		// Unexpected control byte mid-sequence (e.g. an embedded ESC): flush what we
		// held and reprocess b so we don't lose a new sequence.
		*out = append(*out, f.hold...)
		f.hold = f.hold[:0]
		f.state = fsNormal
		goto restart
	}
}
