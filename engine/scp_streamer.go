package engine

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// FormatSSHError inspects raw SSH / TCP errors and transforms them into clear, actionable guidelines.
func FormatSSHError(err error) error {
	if err == nil {
		return nil
	}
	errStr := strings.ToLower(err.Error())

	// 1. Password / Credential Authentication Failures
	if strings.Contains(errStr, "unable to authenticate") ||
		strings.Contains(errStr, "auth failed") ||
		strings.Contains(errStr, "permission denied (publickey,password)") ||
		strings.Contains(errStr, "attempted methods [password]") {
		return fmt.Errorf("Wrong password input. Kindly input the correct password.")
	}

	// 2. SSH Daemon Offline or Port 22 Closed / Refused
	if strings.Contains(errStr, "connection refused") {
		return fmt.Errorf("SSH service is offline or unreachable on target. Kindly restart SSH service ('sudo systemctl restart ssh') or verify the target IP.")
	}

	// 3. Network Timeouts, Host Down, Deadlocked SSH daemon, or Packet Drops
	if strings.Contains(errStr, "no route to host") ||
		strings.Contains(errStr, "i/o timeout") ||
		strings.Contains(errStr, "connection timed out") ||
		strings.Contains(errStr, "connection reset by peer") ||
		strings.Contains(errStr, "broken pipe") {
		return fmt.Errorf("Unable to connect or authenticate via SSH due to technical network problems. Kindly restart the target machine, network connection, or SSH daemon.")
	}

	// General Fallback
	return fmt.Errorf("SSH communication failure: %v. If this persists, kindly restart the SSH server or target system.", err)
}

// CheckRemotePayloadExists checks whether the remote target already possesses the
// full payload matching the exact byte count, avoiding redundant wire re-transmissions.
func CheckRemotePayloadExists(client *ssh.Client, remotePath string, expectedSize int64) bool {
	session, err := client.NewSession()
	if err != nil {
		return false
	}
	defer session.Close()

	// Dual stat check supporting standard Linux GNU stat and BSD/macOS stat
	cmd := fmt.Sprintf("stat -c %%s %s 2>/dev/null || stat -f %%z %s 2>/dev/null", remotePath, remotePath)
	out, err := session.CombinedOutput(cmd)
	if err != nil {
		return false
	}

	sizeStr := strings.TrimSpace(string(out))
	if remoteSize, parseErr := strconv.ParseInt(sizeStr, 10, 64); parseErr == nil {
		return remoteSize == expectedSize
	}
	return false
}

// PushPayloadOverSSH streams a local bootable image into remote scratch space.
// It targets /var/tmp/ (disk-backed) with a symlink at /tmp/os_image.payload so RAM isn't exhausted.
// It is fully idempotent: re-invocations when the file is present will finish instantly.
func PushPayloadOverSSH(user, ip, password, srcPath string, onProgress func(written, total int64)) error {
	info, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("local payload error: %w", err)
	}
	totalBytes := info.Size()

	// Hardware-accelerated cipher suite prioritizes low CPU overhead and high throughput
	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
		Config: ssh.Config{
			Ciphers: []string{
				"chacha20-poly1305@openssh.com",
				"aes128-gcm@openssh.com",
				"aes256-gcm@openssh.com",
			},
		},
	}

	hostPort := net.JoinHostPort(ip, "22")

	// Enable TCP_NODELAY and expand socket buffers to saturate local LAN interfaces
	tcpConn, err := net.DialTimeout("tcp", hostPort, 15*time.Second)
	if err != nil {
		return FormatSSHError(err)
	}
	if tcp, ok := tcpConn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
		_ = tcp.SetReadBuffer(4 * 1024 * 1024)
		_ = tcp.SetWriteBuffer(4 * 1024 * 1024)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(tcpConn, hostPort, config)
	if err != nil {
		return FormatSSHError(err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	remoteDestPath := "/var/tmp/os_image.payload"

	// 1. Pre-flight Idempotency Check: Skip upload if already intact
	if CheckRemotePayloadExists(client, remoteDestPath, totalBytes) {
		if linkSession, lErr := client.NewSession(); lErr == nil {
			_ = linkSession.Run("ln -sf " + remoteDestPath + " /tmp/os_image.payload")
			linkSession.Close()
		}
		if onProgress != nil {
			onProgress(totalBytes, totalBytes)
		}
		return nil
	}

	// 2. Prepare remote staging scratch directory
	if prepSession, pErr := client.NewSession(); pErr == nil {
		_ = prepSession.Run("mkdir -p /var/tmp && rm -f /var/tmp/os_image.payload.part")
		prepSession.Close()
	}

	srcFile, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("opening local payload: %w", err)
	}
	defer srcFile.Close()

	session, err := client.NewSession()
	if err != nil {
		return FormatSSHError(err)
	}
	defer session.Close()

	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return FormatSSHError(err)
	}

	var stderrBuf bytes.Buffer
	session.Stderr = &stderrBuf

	// 3. Stream into atomic temporary part file, rename upon completion, and link to /tmp
	remoteCmd := "cat > /var/tmp/os_image.payload.part && mv -f /var/tmp/os_image.payload.part /var/tmp/os_image.payload && ln -sf /var/tmp/os_image.payload /tmp/os_image.payload"
	if err := session.Start(remoteCmd); err != nil {
		return FormatSSHError(err)
	}

	// 4MB streaming buffer for throughput saturation
	buf := make([]byte, 4*1024*1024)
	var written int64

	for {
		n, rErr := srcFile.Read(buf)
		if n > 0 {
			if _, wErr := stdinPipe.Write(buf[:n]); wErr != nil {
				errMsg := strings.TrimSpace(stderrBuf.String())
				if errMsg != "" {
					return fmt.Errorf("remote write error: %s", errMsg)
				}
				return fmt.Errorf("network pipe write failure: %w (check remote disk space with 'df -h /var/tmp')", wErr)
			}
			written += int64(n)
			if onProgress != nil {
				onProgress(written, totalBytes)
			}
		}
		if rErr == io.EOF {
			break
		}
		if rErr != nil {
			return fmt.Errorf("reading source payload: %w", rErr)
		}
	}

	_ = stdinPipe.Close()

	if err := session.Wait(); err != nil {
		errMsg := strings.TrimSpace(stderrBuf.String())
		if errMsg != "" {
			return fmt.Errorf("remote error: %s", errMsg)
		}
		return FormatSSHError(err)
	}

	return nil
}

// InspectStagedPayload checks for staged payloads in /var/tmp and /tmp.
func InspectStagedPayload() (bool, int64, string) {
	candidates := []string{
		"/var/tmp/os_image.payload",
		"/tmp/os_image.payload",
	}

	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Size() > 0 {
			return true, info.Size(), p
		}
	}

	// Scan search paths for any other bootable archives
	searchDirs := []string{"/var/tmp", "/tmp"}
	for _, dir := range searchDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if IsSupportedPayload(name) {
				fullPath := filepath.Join(dir, name)
				if info, err := entry.Info(); err == nil && info.Size() > 0 {
					return true, info.Size(), fullPath
				}
			}
		}
	}

	return false, 0, ""
}

// PurgeStagedPayload autonomously removes scratch payloads and parts from both /var/tmp and /tmp.
func PurgeStagedPayload() error {
	_ = os.Remove("/tmp/os_image.payload")
	_ = os.Remove("/var/tmp/os_image.payload")
	_ = os.Remove("/tmp/os_image.payload.part")
	_ = os.Remove("/var/tmp/os_image.payload.part")

	searchDirs := []string{"/var/tmp", "/tmp"}
	for _, dir := range searchDirs {
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				name := entry.Name()
				if IsSupportedPayload(name) || strings.HasPrefix(name, "os_image.payload") {
					_ = os.Remove(filepath.Join(dir, name))
				}
			}
		}
	}
	return nil
}
