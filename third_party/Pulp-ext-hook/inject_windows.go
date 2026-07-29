//go:build windows

// Package hook is the Windows half of the ProjX defensive AppContainer cage. It
// installs a sandbox-compatibility hook into a confined child BEFORE it runs, so
// a Node/libuv TUI (e.g. claude) works inside an AppContainer token. The hook DLL
// (dll/hook.go) IAT-hooks the named-pipe/file APIs to splice the AppContainer-
// local "\LOCAL\" namespace into libuv's hardcoded global pipe name, and patches
// CreateProcess so the hook propagates across the whole child tree. This is the
// productionized form of the proven projx-wincage-poc (achook + projxhook): same
// mechanism, no functional change — purely a defensive sandbox shim.
package hook

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	pVirtualAllocEx     = kernel32.NewProc("VirtualAllocEx")
	pWriteProcessMemory = kernel32.NewProc("WriteProcessMemory")
	pCreateRemoteThread = kernel32.NewProc("CreateRemoteThread")
	pGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
	pGetProcAddress     = kernel32.NewProc("GetProcAddress")
	pLoadLibraryW       = kernel32.NewProc("LoadLibraryW")
	pWaitForSingleObj   = kernel32.NewProc("WaitForSingleObject")
	pGetExitCodeThread  = kernel32.NewProc("GetExitCodeThread")
	pCloseHandle        = kernel32.NewProc("CloseHandle")
	pEnumProcModulesEx  = kernel32.NewProc("K32EnumProcessModulesEx")
	pGetModuleBaseNameW = kernel32.NewProc("K32GetModuleBaseNameW")
)

const (
	memCommit     = 0x1000
	memReserve    = 0x2000
	pageReadWrite = 0x04
	infinite      = 0xFFFFFFFF
)

// Inject loads the hook dllPath into the target process (which the caller created
// CREATE_SUSPENDED) and calls its exported Arm() so the IAT hooks are installed
// before the child executes a single instruction. The caller resumes the child
// after Inject returns. Best-effort by contract: the caller treats a non-nil
// error as "run unhooked" (degrade, never block) — except the interactive cage,
// where an unhooked libuv child would spin, so callers gate accordingly.
//
// Mechanics: remote LoadLibraryW(dllPath) via CreateRemoteThread, then resolve
// Arm's RVA locally and call it in the target at base+RVA. The DLL must already
// be readable + path-traversable by the target's AppContainer SID.
func Inject(proc uintptr, dllPath string) error {
	pathU16, err := syscall.UTF16FromString(dllPath)
	if err != nil {
		return fmt.Errorf("hook: dll path: %w", err)
	}
	nbytes := uintptr(len(pathU16) * 2)
	remote, _, e := pVirtualAllocEx.Call(proc, 0, nbytes, memCommit|memReserve, pageReadWrite)
	if remote == 0 {
		return fmt.Errorf("hook: VirtualAllocEx: %v", e)
	}
	if r, _, e := pWriteProcessMemory.Call(proc, remote, uintptr(unsafe.Pointer(&pathU16[0])), nbytes, 0); r == 0 {
		return fmt.Errorf("hook: WriteProcessMemory: %v", e)
	}

	k32, _, _ := pGetModuleHandleW.Call(uintptr(unsafe.Pointer(mustU16("kernel32.dll"))))
	loadLib, _, _ := pGetProcAddress.Call(k32, uintptr(unsafe.Pointer(mustCStr("LoadLibraryW"))))
	if loadLib == 0 {
		return fmt.Errorf("hook: resolve LoadLibraryW")
	}
	th, _, e := pCreateRemoteThread.Call(proc, 0, 0, loadLib, remote, 0, 0)
	if th == 0 {
		return fmt.Errorf("hook: CreateRemoteThread(LoadLibrary): %v", e)
	}
	pWaitForSingleObj.Call(th, infinite)
	pCloseHandle.Call(th)

	// Resolve Arm's RVA in our own copy, then call it at the target's module base.
	localMod, _, _ := pLoadLibraryW.Call(uintptr(unsafe.Pointer(mustU16(dllPath))))
	if localMod == 0 {
		return fmt.Errorf("hook: local LoadLibraryW(%q)", dllPath)
	}
	localArm, _, _ := pGetProcAddress.Call(localMod, uintptr(unsafe.Pointer(mustCStr("Arm"))))
	if localArm == 0 {
		return fmt.Errorf("hook: local GetProcAddress(Arm)")
	}
	armRVA := localArm - localMod
	targetBase := findModuleBase(proc, filepath.Base(dllPath))
	if targetBase == 0 {
		return fmt.Errorf("hook: DLL not in target module list (LoadLibrary failed inside the AppContainer?)")
	}
	th2, _, e := pCreateRemoteThread.Call(proc, 0, 0, targetBase+armRVA, 0, 0, 0)
	if th2 == 0 {
		return fmt.Errorf("hook: CreateRemoteThread(Arm): %v", e)
	}
	pWaitForSingleObj.Call(th2, infinite)
	var armed uint32
	pGetExitCodeThread.Call(th2, uintptr(unsafe.Pointer(&armed)))
	pCloseHandle.Call(th2)
	if armed == 0 {
		return fmt.Errorf("hook: Arm patched 0 IAT slots (pipe APIs imported under a different name/module?)")
	}
	return nil
}

// findModuleBase returns the load base of the named module inside proc via
// EnumProcessModulesEx (reliable on a process we created with full access).
func findModuleBase(proc uintptr, name string) uintptr {
	const listModulesAll = 0x03
	mods := make([]uintptr, 1024)
	var needed uint32
	r, _, _ := pEnumProcModulesEx.Call(proc, uintptr(unsafe.Pointer(&mods[0])),
		uintptr(len(mods)*int(unsafe.Sizeof(uintptr(0)))), uintptr(unsafe.Pointer(&needed)), listModulesAll)
	if r == 0 {
		return 0
	}
	n := int(needed) / int(unsafe.Sizeof(uintptr(0)))
	if n > len(mods) {
		n = len(mods)
	}
	want := strings.ToLower(name)
	for i := 0; i < n; i++ {
		var buf [260]uint16
		pGetModuleBaseNameW.Call(proc, mods[i], uintptr(unsafe.Pointer(&buf[0])), 260)
		if strings.ToLower(syscall.UTF16ToString(buf[:])) == want {
			return mods[i]
		}
	}
	return 0
}

func mustU16(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }
func mustCStr(s string) *byte  { b := append([]byte(s), 0); return &b[0] }
