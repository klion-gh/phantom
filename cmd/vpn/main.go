package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var (
	clientProcess *exec.Cmd
	connected     = false
	logFile       *os.File
	exeDir        string
)

func logMsg(format string, args ...interface{}) {
	msg := fmt.Sprintf("[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	fmt.Print("  " + msg)
	if logFile != nil {
		logFile.WriteString(msg)
		logFile.Sync()
	}
}

func setSystemProxy(enable bool) error {
	var psScript string
	if enable {
		psScript = `
Set-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings' -Name ProxyEnable -Value 1
Set-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings' -Name ProxyServer -Value '127.0.0.1:1081'
$signature = @'
[DllImport("wininet.dll", SetLastError = true)]
public static extern bool InternetSetOption(IntPtr hInternet, int dwOption, IntPtr lpBuffer, int dwBufferLength);
'@
$type = Add-Type -MemberDefinition $signature -Name WinInet -Namespace Win32 -PassThru
$type::InternetSetOption([IntPtr]::Zero, 39, [IntPtr]::Zero, 0)
$type::InternetSetOption([IntPtr]::Zero, 37, [IntPtr]::Zero, 0)
`
	} else {
		psScript = `
Set-ItemProperty -Path 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings' -Name ProxyEnable -Value 0
$signature = @'
[DllImport("wininet.dll", SetLastError = true)]
public static extern bool InternetSetOption(IntPtr hInternet, int dwOption, IntPtr lpBuffer, int dwBufferLength);
'@
$type = Add-Type -MemberDefinition $signature -Name WinInet -Namespace Win32 -PassThru
$type::InternetSetOption([IntPtr]::Zero, 39, [IntPtr]::Zero, 0)
$type::InternetSetOption([IntPtr]::Zero, 37, [IntPtr]::Zero, 0)
`
	}
	cmd := exec.Command("powershell", "-NoProfile", "-Command", psScript)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("powershell error: %w: %s", err, string(output))
	}
	return nil
}

func getProxyStatus() bool {
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		"(Get-ItemProperty 'HKCU:\\Software\\Microsoft\\Windows\\CurrentVersion\\Internet Settings').ProxyEnable")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "1"
}

func isPortOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 1*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func findFile(name string, searchPaths []string) string {
	for _, dir := range searchPaths {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			abs, _ := filepath.Abs(candidate)
			return abs
		}
	}
	if _, err := os.Stat(name); err == nil {
		abs, _ := filepath.Abs(name)
		return abs
	}
	return ""
}

func startVPN() error {
	if connected {
		logMsg("Already connected")
		return nil
	}

	searchPaths := []string{exeDir, filepath.Join(exeDir, "..", "bin"), filepath.Join(exeDir, "bin"), "."}
	clientPath := findFile("client.exe", searchPaths)
	if clientPath == "" {
		return fmt.Errorf("client.exe not found (searched: %s)", strings.Join(searchPaths, ", "))
	}
	configPath := findFile("client.yaml", append(searchPaths, filepath.Join(exeDir, "..", "configs")))
	if configPath == "" {
		return fmt.Errorf("client.yaml not found")
	}

	logMsg("Client: %s", clientPath)
	logMsg("Config: %s", configPath)

	logFile := filepath.Join(exeDir, "vpn-client.log")
	logF, err := os.Create(logFile)
	if err != nil {
		logMsg("Warning: cannot create log file: %v", err)
	}

	logMsg("Starting client.exe...")

	clientProcess = exec.Command(clientPath, "-config", configPath)
	clientProcess.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	clientProcess.Dir = filepath.Dir(clientPath)

	if logF != nil {
		clientProcess.Stdout = logF
		clientProcess.Stderr = logF
	}

	if err := clientProcess.Start(); err != nil {
		return fmt.Errorf("start client failed: %w", err)
	}

	logMsg("Client PID: %d, waiting for SOCKS5...", clientProcess.Process.Pid)

	proxyReady := false
	for i := 0; i < 15; i++ {
		time.Sleep(500 * time.Millisecond)

		if clientProcess.Process != nil {
			checkCmd := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", clientProcess.Process.Pid), "/NH")
			checkOut, _ := checkCmd.Output()
			if !strings.Contains(string(checkOut), "client.exe") {
				errMsg := "client.exe exited unexpectedly"
				if logF != nil {
					logF.Seek(0, 0)
					scanner := bufio.NewScanner(logF)
					var lastLines []string
					for scanner.Scan() {
						lastLines = append(lastLines, scanner.Text())
						if len(lastLines) > 10 {
							lastLines = lastLines[1:]
						}
					}
					errMsg += "\n  Last log lines:"
					for _, l := range lastLines {
						errMsg += "\n    " + l
					}
				}
				return fmt.Errorf("%s", errMsg)
			}
		}

		if isPortOpen(1081) {
			proxyReady = true
			break
		}
		logMsg("  Waiting for port 1081... (%d/15)", i+1)
	}

	if !proxyReady {
		clientProcess.Process.Kill()
		errMsg := "HTTP proxy not listening on port 1081 after 7s"
		if logF != nil {
			logF.Seek(0, 0)
			scanner := bufio.NewScanner(logF)
			var lastLines []string
			for scanner.Scan() {
				lastLines = append(lastLines, scanner.Text())
				if len(lastLines) > 15 {
					lastLines = lastLines[1:]
				}
			}
			errMsg += "\n  Client log:"
			for _, l := range lastLines {
				errMsg += "\n    " + l
			}
		}
		return fmt.Errorf("%s", errMsg)
	}

	logMsg("Port 1080 is open, setting system proxy...")

	if err := setSystemProxy(true); err != nil {
		clientProcess.Process.Kill()
		return fmt.Errorf("set system proxy failed: %w", err)
	}

	connected = true
	logMsg("CONNECTED")
	logMsg("  Proxy: 127.0.0.1:1081 (HTTP)")
	logMsg("  System proxy: ON")
	return nil
}

func stopVPN() {
	if !connected {
		logMsg("Not connected")
		return
	}

	logMsg("Disconnecting...")

	setSystemProxy(false)
	logMsg("System proxy: OFF")

	if clientProcess != nil && clientProcess.Process != nil {
		clientProcess.Process.Kill()
		clientProcess.Wait()
		clientProcess = nil
		logMsg("Client stopped")
	}

	connected = false
	logMsg("DISCONNECTED")
}

func printStatus() {
	proxyOn := getProxyStatus()
	portOpen := isPortOpen(1081)
	fmt.Println()
	if connected && proxyOn {
		fmt.Println("  Status:       CONNECTED")
		fmt.Println("  Server:       2.26.17.213:8443")
		fmt.Println("  Proxy port:   127.0.0.1:1081 (HTTP)")
		if portOpen {
			fmt.Println("  Port 1081:    OPEN")
		} else {
			fmt.Println("  Port 1081:    CLOSED (client may have crashed)")
		}
		fmt.Println("  System proxy: ON")
	} else {
		fmt.Println("  Status: DISCONNECTED")
		if portOpen {
			fmt.Println("  Port 1081: OPEN (stale process?)")
		}
	}
	fmt.Println()
}

func main() {
	exePath, _ := os.Executable()
	exeDir = filepath.Dir(exePath)

	logPath := filepath.Join(exeDir, "vpn.log")
	var err error
	logFile, err = os.Create(logPath)
	if err != nil {
		fmt.Printf("  Warning: cannot create log file: %v\n", err)
	}
	defer func() {
		if logFile != nil {
			logFile.Close()
		}
	}()

	logMsg("=== Phantom VPN Manager started ===")
	logMsg("Working dir: %s", exeDir)

	fmt.Println()
	fmt.Println("  +---------------------------+")
	fmt.Println("  |   Phantom VPN Manager  |")
	fmt.Println("  +---------------------------+")
	fmt.Println()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println()
		if connected {
			stopVPN()
		}
		os.Exit(0)
	}()

	reader := bufio.NewReader(os.Stdin)

	for {
		if connected {
			fmt.Print("[ON] > ")
		} else {
			fmt.Print("[OFF] > ")
		}

		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(strings.ToLower(input))

		switch input {
		case "on", "connect", "start", "1":
			if err := startVPN(); err != nil {
				logMsg("ERROR: %v", err)
			}

		case "off", "disconnect", "stop", "0":
			stopVPN()

		case "status", "s":
			printStatus()

		case "quit", "exit", "q":
			if connected {
				stopVPN()
			}
			logMsg("Exit")
			return

		case "log":
			logPath := filepath.Join(exeDir, "vpn-client.log")
			data, err := os.ReadFile(logPath)
			if err != nil {
				fmt.Printf("  No client log found\n")
			} else {
				fmt.Printf("  --- client.log ---\n%s\n", string(data))
			}

		case "help", "h", "?":
			fmt.Println()
			fmt.Println("  Commands:")
			fmt.Println("    on     - connect VPN")
			fmt.Println("    off    - disconnect VPN")
			fmt.Println("    s      - status")
			fmt.Println("    log    - show client log")
			fmt.Println("    q      - quit")
			fmt.Println()

		case "":
			continue

		default:
			fmt.Println("  Unknown command. Type 'help'.")
		}
	}
}
