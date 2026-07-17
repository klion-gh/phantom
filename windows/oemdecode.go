package main

import (
	"syscall"
	"unsafe"
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procMultiByteToWideChar = kernel32.NewProc("MultiByteToWideChar")
)

// cpOEMCP is CP_OEMCP: MultiByteToWideChar interprets the input using the
// system's current OEM code page (CP866 on a Russian Windows, CP850/CP437 on
// others).
const cpOEMCP = 1

// oemToUTF8 decodes bytes produced by the console tools we shell out to
// (route/netsh via runNetCmd), which emit their output in the OS OEM code page,
// not UTF-8 - so logging them verbatim turned every localized route-table dump
// into mojibake. This converts to UTF-8 via the OS itself, so it's correct for
// any locale without hardcoding a code page. ASCII bytes are unchanged, so on
// an English system it's effectively a no-op. Only used for the human-readable
// log line; the raw bytes are still what callers parse (route tables are parsed
// on ASCII-only patterns - IP addresses, "On-link" - which the OEM encoding
// leaves untouched). Falls back to the raw string if conversion fails.
func oemToUTF8(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	// First pass: 0 output length asks for the required wide-char count.
	n, _, _ := procMultiByteToWideChar.Call(
		uintptr(cpOEMCP), 0,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)),
		0, 0,
	)
	if n == 0 {
		return string(b)
	}
	buf := make([]uint16, n)
	ret, _, _ := procMultiByteToWideChar.Call(
		uintptr(cpOEMCP), 0,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(n),
	)
	if ret == 0 {
		return string(b)
	}
	return syscall.UTF16ToString(buf[:ret])
}
