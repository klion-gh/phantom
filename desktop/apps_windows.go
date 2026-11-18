package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Which program a connection belongs to, for "Приложения под обход".
//
// Every flow netstack forwards still has its socket in the program that opened
// it, and Windows keeps the TCP/UDP tables of who owns which local port
// regardless of which interface the packets take. So: the flow's source port
// -> the owning PID (GetExtendedTcpTable/GetExtendedUdpTable) -> that
// process's exe -> is its file name listed. Looked up once per new flow the
// site rules sent direct, never per packet.

const appsSupported = true

var (
	procGetExtendedTCPTable = iphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUDPTable = iphlpapi.NewProc("GetExtendedUdpTable")
)

const (
	tcpTableOwnerPIDAll = 5 // TCP_TABLE_OWNER_PID_ALL
	udpTableOwnerPID    = 1 // UDP_TABLE_OWNER_PID
	errInsufficientBuf  = 122
)

// applyAppsPlatform hands the list to the engine, with the process-table
// lookup as its way of attributing a flow.
func applyAppsPlatform(apps []SmartApp) {
	keys := smartAppKeys(apps)
	if len(keys) == 0 {
		engine.SetApps(0, nil)
		return
	}
	engine.SetApps(len(keys), func(network, source, _ string) bool {
		exe := flowOwnerExe(network, source)
		return exe != "" && keys[exeKey(exe)]
	})
}

// flowOwnerExe returns the exe path of the process that owns source, or "".
func flowOwnerExe(network, source string) string {
	host, portStr, err := net.SplitHostPort(source)
	if err != nil {
		return ""
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return ""
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	pid, ok := owningPID(network == "tcp", ip, uint16(port))
	if !ok || pid == 0 || pid == 4 {
		return ""
	}
	return exePathCached(pid)
}

// owningPID finds the PID owning the local endpoint ip:port. An exact address
// match wins; a socket bound to the wildcard address (UDP, mostly) on that
// port is the fallback.
func owningPID(tcp bool, ip net.IP, port uint16) (uint32, bool) {
	v4 := ip.To4()
	family := uint32(windows.AF_INET6)
	if v4 != nil {
		family = windows.AF_INET
	}
	buf, err := ownerTable(tcp, family)
	if err != nil || len(buf) < 4 {
		return 0, false
	}
	n := int(binary.LittleEndian.Uint32(buf[0:4]))

	// Row layouts (MIB_TCPROW_OWNER_PID & co): the port is the low 16 bits of
	// a DWORD in network order, i.e. its first two bytes big-endian.
	var rowSize, addrOff, addrLen, portOff, pidOff int
	switch {
	case tcp && v4 != nil:
		rowSize, addrOff, addrLen, portOff, pidOff = 24, 4, 4, 8, 20
	case !tcp && v4 != nil:
		rowSize, addrOff, addrLen, portOff, pidOff = 12, 0, 4, 4, 8
	case tcp:
		rowSize, addrOff, addrLen, portOff, pidOff = 56, 0, 16, 20, 52
	default:
		rowSize, addrOff, addrLen, portOff, pidOff = 28, 0, 16, 20, 24
	}
	want := []byte(ip.To16())
	if v4 != nil {
		want = v4
	}
	var fallback uint32
	found := false
	for i := 0; i < n; i++ {
		off := 4 + i*rowSize
		if off+rowSize > len(buf) {
			break
		}
		row := buf[off : off+rowSize]
		if binary.BigEndian.Uint16(row[portOff:portOff+2]) != port {
			continue
		}
		pid := binary.LittleEndian.Uint32(row[pidOff : pidOff+4])
		addr := row[addrOff : addrOff+addrLen]
		if string(addr) == string(want) {
			return pid, true
		}
		if !found && isZero(addr) {
			fallback, found = pid, true
		}
	}
	return fallback, found
}

func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func ownerTable(tcp bool, family uint32) ([]byte, error) {
	proc, class := procGetExtendedUDPTable, uintptr(udpTableOwnerPID)
	if tcp {
		proc, class = procGetExtendedTCPTable, uintptr(tcpTableOwnerPIDAll)
	}
	size := uint32(64 * 1024)
	for attempt := 0; attempt < 4; attempt++ {
		buf := make([]byte, size)
		ret, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(family), class, 0)
		if ret == 0 {
			return buf[:size], nil
		}
		if ret != errInsufficientBuf {
			return nil, fmt.Errorf("owner table: code %d", ret)
		}
		size += 16 * 1024 // grew between the calls - retry with room to spare
	}
	return nil, fmt.Errorf("owner table kept growing")
}

// PIDs get reused, so the cache only bridges the burst of connections one
// program opens at once.
const exeCacheTTL = 30 * time.Second

type exeEntry struct {
	path string
	at   time.Time
}

var (
	exeCacheMu sync.Mutex
	exeCache   = map[uint32]exeEntry{}
)

func exePathCached(pid uint32) string {
	now := time.Now()
	exeCacheMu.Lock()
	if e, ok := exeCache[pid]; ok && now.Sub(e.at) < exeCacheTTL {
		exeCacheMu.Unlock()
		return e.path
	}
	exeCacheMu.Unlock()

	path, _ := exePathForPID(pid)
	exeCacheMu.Lock()
	if len(exeCache) > 512 {
		exeCache = map[uint32]exeEntry{}
	}
	exeCache[pid] = exeEntry{path: path, at: now}
	exeCacheMu.Unlock()
	return path
}

func exePathForPID(pid uint32) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:size]), nil
}

// runningApps lists the programs running now, one entry per exe, leaving out
// the system's own (anything under the Windows folder) and this app.
func runningApps() []SmartApp {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return []SmartApp{}
	}
	defer windows.CloseHandle(snap)

	winDir := strings.ToLower(windowsDir())
	self := windows.GetCurrentProcessId()
	seen := map[string]bool{}
	out := []SmartApp{}
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if entry.ProcessID == 0 || entry.ProcessID == 4 || entry.ProcessID == self {
			continue
		}
		path, perr := exePathForPID(entry.ProcessID)
		if perr != nil || path == "" {
			continue
		}
		lower := strings.ToLower(path)
		if winDir != "" && strings.HasPrefix(lower, winDir) {
			continue
		}
		key := exeKey(path)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, SmartApp{Name: appDisplayName(path), Path: path})
	}
	return out
}

func windowsDir() string {
	dir, err := windows.GetWindowsDirectory()
	if err != nil || dir == "" {
		return `c:\windows\`
	}
	return strings.TrimRight(dir, `\`) + `\`
}
