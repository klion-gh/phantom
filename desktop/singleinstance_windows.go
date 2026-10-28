package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                  = syscall.NewLazyDLL("user32.dll")
	procFindWindowW         = user32.NewProc("FindWindowW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

const swRestore = 9

// singleInstanceMutex is the acquired lock's handle, kept around so
// releaseSingleInstanceLock (called by updater.go right before relaunching
// after a self-update) can free the name before the new process starts -
// otherwise the brief window where both the old (mid-exit) and new process
// are alive could make the new one think it lost the single-instance race
// against its own predecessor.
var singleInstanceMutex windows.Handle

// acquireSingleInstanceLock returns true if this process is the only running
// instance of Phantom and should proceed to start normally. If another
// instance already holds the lock, it returns false after best-effort
// bringing that instance's window to the front (it may be hidden in the
// tray - see App.beforeClose) - the caller should then exit immediately
// without creating a second window, tray icon, or tunnel state.
//
// Uses a named kernel mutex rather than e.g. a lock file or port bind: it's
// automatically released if the process dies unexpectedly (crash, kill),
// so a stale lock can never wrongly block a legitimate relaunch - which a
// lock file would need extra staleness-detection logic to get right.
func acquireSingleInstanceLock() bool {
	name, err := windows.UTF16PtrFromString(`PhantomVPN-SingleInstance-Mutex`)
	if err != nil {
		return true // can't even check - fail open rather than refuse to start
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		bringExistingWindowToFront()
		return false
	}
	singleInstanceMutex = handle
	return true
}

// releaseSingleInstanceLock frees the mutex ahead of a self-update relaunch
// (see the comment on singleInstanceMutex) - a no-op if the lock was never
// acquired.
func releaseSingleInstanceLock() {
	if singleInstanceMutex != 0 {
		windows.CloseHandle(singleInstanceMutex)
		singleInstanceMutex = 0
	}
}

func bringExistingWindowToFront() {
	titlePtr, err := syscall.UTF16PtrFromString("Phantom")
	if err != nil {
		return
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(titlePtr)))
	if hwnd == 0 {
		return
	}
	procShowWindow.Call(hwnd, uintptr(swRestore))
	procSetForegroundWindow.Call(hwnd)
}
