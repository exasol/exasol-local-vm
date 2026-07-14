// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Code-Hex/vz/v3"
	"github.com/ulikunitz/xz"
	"golang.org/x/crypto/ssh"
)

//go:embed vm-package.tar.xz
var vmPackage []byte

//go:embed init-assets.tar.xz
var initAssets []byte

// InitOutput represents the JSON output from the VM init scripts
type InitOutput struct {
	IP    string         `json:"ip"`
	Ports map[string]int `json:"ports"`
}

type RuntimeConfig struct {
	SSHPrivateKey string `json:"ssh_private_key"`
}

type VersionCheckRuntimeConfig struct {
	Enabled         bool   `json:"enabled"`
	IntervalSeconds int    `json:"interval_seconds"`
	Identity        string `json:"identity"`
	URL             string `json:"url"`
	OperatingSystem string `json:"operating_system"`
}

type VersionCheckOptions struct {
	Enabled         bool
	IntervalSeconds int
	Identity        string
	URL             string
}

const (
	defaultSSHPrivateKeyPath           = "vm-ssh-key"
	runtimeConfigPath                  = "vm-config.json"
	sharedDirName                      = "vm-shared"
	authorizedKeysName                 = "authorized_keys"
	versionCheckRuntimeConfigName      = "version-check.json"
	defaultVersionCheckIntervalSeconds = 86400
	defaultVersionCheckIdentity        = "NONE"
	vmSocketPath                       = "vm.sock"
)

var defaultVersionCheckURL = "https://metrics-test.exasol.com/v1/version-check"

// readLastLines reads the last n lines from a file
func readLastLines(filePath string, n int) ([]string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) <= n {
		return lines, nil
	}
	return lines[len(lines)-n:], nil
}

// generateSSHKeyPair generates an ED25519 SSH key pair and returns the private key path
func generateSSHKeyPair(privateKeyPath, publicKeyPath string) error {
	// Generate ED25519 key pair
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate ED25519 key: %w", err)
	}

	// Marshal private key to OpenSSH format
	privKeyPEM, err := ssh.MarshalPrivateKey(privKey, "")
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}

	// Write private key to file with restrictive permissions
	if err := os.WriteFile(privateKeyPath, pem.EncodeToMemory(privKeyPEM), 0600); err != nil {
		return fmt.Errorf("failed to write private key: %w", err)
	}

	// Convert public key to SSH authorized_keys format
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return fmt.Errorf("failed to convert public key to SSH format: %w", err)
	}

	authorizedKey := ssh.MarshalAuthorizedKey(sshPubKey)

	// Write public key to authorized_keys file
	if err := os.WriteFile(publicKeyPath, authorizedKey, 0644); err != nil {
		return fmt.Errorf("failed to write public key: %w", err)
	}

	return nil
}

func expandHome(path string) (string, error) {
	if path == "~" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to resolve home directory: %w", err)
		}
		return homeDir, nil
	}
	if strings.HasPrefix(path, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to resolve home directory: %w", err)
		}
		return filepath.Join(homeDir, path[2:]), nil
	}
	return path, nil
}

func normalizeSSHPrivateKeyPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("SSH private key path must not be empty")
	}

	expandedPath, err := expandHome(path)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(expandedPath)
	if err != nil {
		return "", fmt.Errorf("failed to stat SSH private key %s: %w", expandedPath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("SSH private key path is a directory: %s", expandedPath)
	}

	absPath, err := filepath.Abs(expandedPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve SSH private key path %s: %w", expandedPath, err)
	}
	return absPath, nil
}

func authorizedKeyFromPrivateKey(privateKeyPath string) ([]byte, error) {
	privateKeyData, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read SSH private key %s: %w", privateKeyPath, err)
	}

	// Derive the authorized_keys entry from the private key. We only support
	// unprotected keys because init runs non-interactively and cannot prompt for
	// a passphrase.
	signer, err := ssh.ParsePrivateKey(privateKeyData)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH private key %s; only unprotected private keys are supported: %w", privateKeyPath, err)
	}

	return ssh.MarshalAuthorizedKey(signer.PublicKey()), nil
}

func writeRuntimeConfig(config RuntimeConfig) error {
	configData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal runtime config: %w", err)
	}
	if err := os.WriteFile(runtimeConfigPath, configData, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", runtimeConfigPath, err)
	}
	return nil
}

func loadRuntimeConfig() (RuntimeConfig, error) {
	config := RuntimeConfig{SSHPrivateKey: defaultSSHPrivateKeyPath}

	configData, err := os.ReadFile(runtimeConfigPath)
	if os.IsNotExist(err) {
		return config, nil
	}
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("failed to read %s: %w", runtimeConfigPath, err)
	}

	if err := json.Unmarshal(configData, &config); err != nil {
		return RuntimeConfig{}, fmt.Errorf("failed to parse %s: %w", runtimeConfigPath, err)
	}
	if config.SSHPrivateKey == "" {
		config.SSHPrivateKey = defaultSSHPrivateKeyPath
	}
	return config, nil
}

func defaultVersionCheckOptions() VersionCheckOptions {
	return VersionCheckOptions{
		Enabled:         true,
		IntervalSeconds: defaultVersionCheckIntervalSeconds,
		Identity:        defaultVersionCheckIdentity,
		URL:             defaultVersionCheckURL,
	}
}

func versionCheckOperatingSystem(goos string) string {
	switch goos {
	case "darwin":
		return "MacOS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	case "":
		return "unknown"
	default:
		return goos
	}
}

func versionCheckRuntimeConfigFromOptions(options VersionCheckOptions) VersionCheckRuntimeConfig {
	url := strings.TrimSpace(options.URL)
	if url == "" {
		url = defaultVersionCheckURL
	}

	identity := strings.TrimSpace(options.Identity)
	if identity == "" {
		identity = defaultVersionCheckIdentity
	}

	intervalSeconds := options.IntervalSeconds
	if intervalSeconds <= 0 {
		intervalSeconds = defaultVersionCheckIntervalSeconds
	}

	return VersionCheckRuntimeConfig{
		Enabled:         options.Enabled,
		IntervalSeconds: intervalSeconds,
		Identity:        identity,
		URL:             url,
		OperatingSystem: versionCheckOperatingSystem(runtime.GOOS),
	}
}

func writeVersionCheckRuntimeConfig(sharedDir string, config VersionCheckRuntimeConfig) error {
	if err := os.MkdirAll(sharedDir, 0755); err != nil {
		return fmt.Errorf("failed to create shared directory for version-check config: %w", err)
	}

	configPath := filepath.Join(sharedDir, versionCheckRuntimeConfigName)
	configData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal version-check runtime config: %w", err)
	}
	if err := os.WriteFile(configPath, configData, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", configPath, err)
	}
	return nil
}

func writeVersionCheckRuntimeConfigFromOptions(sharedDir string, options VersionCheckOptions) {
	config := versionCheckRuntimeConfigFromOptions(options)
	if err := writeVersionCheckRuntimeConfig(sharedDir, config); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to write version-check runtime config: %v\n", err)
	}
}

func displayPath(path string) string {
	if filepath.IsAbs(path) || strings.HasPrefix(path, ".") {
		return path
	}
	return "./" + path
}

// createSparseDataDisk creates a sparse raw disk image for VM data storage
// The VM will format it on first boot if needed
func createSparseDataDisk(path string, sizeGB int) error {
	// Create sparse file (allocates inode, not blocks)
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create disk file: %w", err)
	}
	defer f.Close()

	// Set logical size without allocating space
	// Sparse files only allocate blocks as data is written
	sizeBytes := int64(sizeGB) * 1024 * 1024 * 1024
	if err := f.Truncate(sizeBytes); err != nil {
		return fmt.Errorf("failed to set disk size: %w", err)
	}

	// Note: The file is created as a raw disk image without any filesystem.
	// The VM will detect this on first boot and format it as ext4 with the
	// label "exasol-data" automatically via an init script.

	return nil
}

// LoopbackForwarder forwards TCP connections from host to guest
type LoopbackForwarder struct {
	name       string
	listener   net.Listener
	guestHost  string
	guestPort  int
	closeOnce  sync.Once
	closeError error
	wg         sync.WaitGroup
}

// StartLoopbackForwarder starts a TCP proxy from hostPort to guestHost:guestPort
// If hostPort is 0, the OS will allocate a free port dynamically
func StartLoopbackForwarder(ctx context.Context, name string, hostPort int, guestHost string, guestPort int) (*LoopbackForwarder, error) {
	listener, err := (&net.ListenConfig{}).Listen(
		ctx,
		"tcp",
		fmt.Sprintf("127.0.0.1:%d", hostPort),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on 127.0.0.1:%d: %w", hostPort, err)
	}

	forwarder := &LoopbackForwarder{
		name:      name,
		listener:  listener,
		guestHost: guestHost,
		guestPort: guestPort,
	}
	forwarder.wg.Add(1)
	go forwarder.acceptLoop(ctx)

	return forwarder, nil
}

// classifyDialErr turns a raw dial error into the small state vocabulary
// ("reachable", "refused", "blocked", or "timeout") reported over the status
// socket, rather than exposing OS-specific errors.
func classifyDialErr(err error) string {
	if err == nil {
		return "reachable"
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "refused"
	}
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return "blocked"
	}

	// Unrecognized failure: signal a problem rather than silently
	// reporting reachable.
	return "blocked"
}

// Probe dials the guest address on its own, independent of any real client
// connection, so health-check can report state even when nothing is
// currently forwarding traffic through this port.
func (f *LoopbackForwarder) Probe(ctx context.Context, timeout time.Duration) string {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", fmt.Sprintf("%s:%d", f.guestHost, f.guestPort))
	if err == nil {
		conn.Close()
	}

	return classifyDialErr(err)
}

// Port returns the actual host port being listened on
func (f *LoopbackForwarder) Port() int {
	if addr, ok := f.listener.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}
	return 0
}

// Close stops the forwarder and waits for all connections to finish
func (f *LoopbackForwarder) Close() error {
	f.closeOnce.Do(func() {
		f.closeError = f.listener.Close()
		f.wg.Wait()
	})

	if f.closeError != nil && !errors.Is(f.closeError, net.ErrClosed) {
		return f.closeError
	}

	return nil
}

func (f *LoopbackForwarder) acceptLoop(ctx context.Context) {
	defer f.wg.Done()

	for {
		clientConn, err := f.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}

		f.wg.Add(1)
		go f.proxyConnection(ctx, clientConn)
	}
}

func (f *LoopbackForwarder) proxyConnection(ctx context.Context, clientConn net.Conn) {
	defer f.wg.Done()
	defer clientConn.Close()

	guestConn, err := (&net.Dialer{}).DialContext(
		ctx,
		"tcp",
		fmt.Sprintf("%s:%d", f.guestHost, f.guestPort),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] Warning: %s forwarder could not reach guest %s:%d (%s): %v\n",
			time.Now().Format("15:04:05"), f.name, f.guestHost, f.guestPort, classifyDialErr(err), err)
		return
	}
	defer guestConn.Close()

	var copyWG sync.WaitGroup
	copyWG.Add(2)

	go func() {
		defer copyWG.Done()
		io.Copy(guestConn, clientConn)
		guestConn.Close()
	}()

	go func() {
		defer copyWG.Done()
		io.Copy(clientConn, guestConn)
		clientConn.Close()
	}()

	copyWG.Wait()
}

func waitForSSHService(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}

		conn, err := net.DialTimeout("tcp", addr, shorterDuration(3*time.Second, remaining))
		if err != nil {
			lastErr = err
			sleepUntil(deadline, 1*time.Second)
			continue
		}

		readTimeout := shorterDuration(3*time.Second, time.Until(deadline))
		if readTimeout <= 0 {
			conn.Close()
			break
		}
		if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			conn.Close()
			return fmt.Errorf("failed to set SSH readiness deadline: %w", err)
		}

		buf := make([]byte, 256)
		n, err := conn.Read(buf)
		conn.Close()
		if err == nil && strings.HasPrefix(string(buf[:n]), "SSH-") {
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("unexpected SSH banner %q", string(buf[:n]))
		}
		sleepUntil(deadline, 1*time.Second)
	}

	if lastErr != nil {
		return fmt.Errorf("timed out waiting for SSH service at %s after %v: %w", addr, timeout, lastErr)
	}
	return fmt.Errorf("timed out waiting for SSH service at %s after %v", addr, timeout)
}

func shorterDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func sleepUntil(deadline time.Time, duration time.Duration) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return
	}
	time.Sleep(shorterDuration(duration, remaining))
}

// waitForVMIP waits for the VM to report its IP address in the console log
func waitForVMIP(consoleLogPath string, timeout time.Duration) (string, error) {
	initOutput, err := waitForInitOutput(consoleLogPath, timeout)
	if err != nil {
		return "", err
	}
	return initOutput.IP, nil
}

func waitForInitOutput(consoleLogPath string, timeout time.Duration) (*InitOutput, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		data, err := os.ReadFile(consoleLogPath)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// Look for "=== INIT OUTPUT START ===" and "=== INIT OUTPUT END ==="
		content := string(data)
		startMarker := "=== INIT OUTPUT START ==="
		endMarker := "=== INIT OUTPUT END ==="

		startIdx := strings.Index(content, startMarker)
		if startIdx == -1 {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		endIdx := strings.Index(content[startIdx:], endMarker)
		if endIdx == -1 {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// Extract JSON between markers
		jsonStart := startIdx + len(startMarker)
		jsonEnd := startIdx + endIdx
		jsonStr := strings.TrimSpace(content[jsonStart:jsonEnd])

		// Parse JSON
		var initOutput InitOutput
		if err := json.Unmarshal([]byte(jsonStr), &initOutput); err != nil {
			return nil, fmt.Errorf("failed to parse init output JSON: %w", err)
		}

		// Validate the output
		if initOutput.IP == "" {
			return nil, fmt.Errorf("init output missing IP address")
		}
		if net.ParseIP(initOutput.IP) == nil {
			return nil, fmt.Errorf("invalid IP address in init output: %s", initOutput.IP)
		}

		return &initOutput, nil
	}

	return nil, fmt.Errorf("timeout waiting for VM init output after %v", timeout)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: mac-runner <command> [options]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Commands:")
		fmt.Fprintln(os.Stderr, "  init [--ssh-key <private-key>]    Initialize VM")
		fmt.Fprintln(os.Stderr, "  start [--ports <svc>:<port>,...] <cpu> <ram> <data_size_gb>")
		fmt.Fprintln(os.Stderr, "                                    Start VM with CPU count, RAM size (MB),")
		fmt.Fprintln(os.Stderr, "                                    and data disk size in GB.")
		fmt.Fprintln(os.Stderr, "                                    --ports overrides which host port is bound")
		fmt.Fprintln(os.Stderr, "                                    for a named service (e.g. --ports db:9090,ssh:2222).")
		fmt.Fprintln(os.Stderr, "                                    Unspecified services use the same port as the VM,")
		fmt.Fprintln(os.Stderr, "                                    falling back to a random port if unavailable.")
		fmt.Fprintln(os.Stderr, "                                    The data disk will be:")
		fmt.Fprintln(os.Stderr, "                                      - created sparsely if it does not exist")
		fmt.Fprintln(os.Stderr, "                                      - reused as-is if its size matches")
		fmt.Fprintln(os.Stderr, "                                      - grown to the requested size if smaller")
		fmt.Fprintln(os.Stderr, "                                      - rejected (error) if larger; shrinking")
		fmt.Fprintln(os.Stderr, "                                        is not supported.")
		fmt.Fprintln(os.Stderr, "  stop                              Stop running VM")
		fmt.Fprintln(os.Stderr, "  status                            Print JSON {\"running\": bool}")
		fmt.Fprintln(os.Stderr, "  health-check                      Print JSON {\"ports\": {\"<name>\": {\"state\": ...}}}")
		fmt.Fprintln(os.Stderr, "                                    after freshly probing every forwarded port")
		fmt.Fprintln(os.Stderr, "  resize-data <size>                Resize data disk to SIZE GB (VM must be stopped)")
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "init":
		initFlags := flag.NewFlagSet("init", flag.ContinueOnError)
		initFlags.SetOutput(os.Stderr)
		sshKeyPath := initFlags.String("ssh-key", "", "Use an existing SSH private key instead of generating one")
		initFlags.Usage = func() {
			fmt.Fprintln(os.Stderr, "Usage: mac-runner init [--ssh-key <private-key>]")
			initFlags.PrintDefaults()
		}
		if parseErr := initFlags.Parse(os.Args[2:]); parseErr != nil {
			os.Exit(2)
		}
		if initFlags.NArg() != 0 {
			fmt.Fprintf(os.Stderr, "Unexpected init argument: %s\n", initFlags.Arg(0))
			initFlags.Usage()
			os.Exit(2)
		}
		err = initCmd(*sshKeyPath)
	case "start":
		startFlags := flag.NewFlagSet("start", flag.ContinueOnError)
		startFlags.SetOutput(os.Stderr)
		portsFlag := startFlags.String("ports", "", "Host port overrides: <service>:<port>[,<service>:<port>...]")
		versionCheckOptions := defaultVersionCheckOptions()
		startFlags.BoolVar(&versionCheckOptions.Enabled, "version-check-enabled", versionCheckOptions.Enabled, "Enable scheduled local database version checks")
		startFlags.IntVar(&versionCheckOptions.IntervalSeconds, "version-check-interval-seconds", versionCheckOptions.IntervalSeconds, "Interval in seconds for scheduled local database version checks")
		startFlags.StringVar(&versionCheckOptions.Identity, "version-check-identity", versionCheckOptions.Identity, "Identity string for scheduled local database version checks")
		startFlags.StringVar(&versionCheckOptions.URL, "version-check-url", versionCheckOptions.URL, "Version-check URL override for scheduled local database version checks")
		startFlags.Usage = func() {
			fmt.Fprintln(os.Stderr, "Usage: mac-runner start [--ports <service>:<port>,...] <cpu_count> <ram_size> <data_size_gb>")
			startFlags.PrintDefaults()
		}
		if parseErr := startFlags.Parse(os.Args[2:]); parseErr != nil {
			os.Exit(2)
		}
		if startFlags.NArg() != 3 {
			fmt.Fprintf(os.Stderr, "Error: expected 3 positional arguments, got %d\n", startFlags.NArg())
			startFlags.Usage()
			os.Exit(2)
		}
		dataSizeGB, parseErr := strconv.Atoi(startFlags.Arg(2))
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid data_size_gb: %v\n", parseErr)
			os.Exit(1)
		}
		if dataSizeGB <= 0 {
			fmt.Fprintln(os.Stderr, "Error: data_size_gb must be a positive integer")
			os.Exit(1)
		}
		err = startCmd(startFlags.Arg(0), startFlags.Arg(1), dataSizeGB, *portsFlag, versionCheckOptions)
	case "__daemon__":
		// Internal daemon mode - run VM in background
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Invalid daemon arguments")
			os.Exit(1)
		}
		daemonPorts := ""
		if len(os.Args) >= 5 {
			daemonPorts = os.Args[4]
		}
		err = runVMDaemon(os.Args[2], os.Args[3], daemonPorts)
	case "stop":
		err = stopCmd()
	case "status":
		err = statusCmd()
	case "health-check":
		err = healthCheckCmd()
	case "resize-data":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: mac-runner resize-data <new_size_gb>")
			os.Exit(1)
		}
		err = resizeDataDiskCmd(os.Args[2])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		fmt.Fprintln(os.Stderr, "Available commands: init, start, stop, status, resize-data")
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// extractTarXZ extracts a tar.xz archive to the specified output directory.
// pathTransform is an optional function to transform archive paths before extracting.
func extractTarXZ(data []byte, outputDir string, pathTransform func(string) string) error {
	xzReader, err := xz.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create xz reader: %w", err)
	}

	tarReader := tar.NewReader(xzReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar header: %w", err)
		}

		// Apply path transformation if provided
		outputPath := header.Name
		if pathTransform != nil {
			outputPath = pathTransform(header.Name)
			if outputPath == "" {
				continue // Skip this entry
			}
		}
		outputPath = filepath.Join(outputDir, outputPath)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(outputPath, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", outputPath, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
				return fmt.Errorf("failed to create parent directory for %s: %w", outputPath, err)
			}
			outFile, err := os.Create(outputPath)
			if err != nil {
				return fmt.Errorf("failed to create file %s: %w", outputPath, err)
			}
			if _, err := io.Copy(outFile, tarReader); err != nil {
				outFile.Close()
				return fmt.Errorf("failed to write file %s: %w", outputPath, err)
			}
			outFile.Close()
			if header.Mode&0111 != 0 {
				os.Chmod(outputPath, 0755)
			}
			fmt.Printf("Extracted: %s\n", outputPath)
		}
	}
	return nil
}

func initCmd(sshKeyPath string) error {
	fmt.Println("Initializing VM...")

	privateKeyPath := defaultSSHPrivateKeyPath
	var authorizedKey []byte
	if sshKeyPath != "" {
		var err error
		privateKeyPath, err = normalizeSSHPrivateKeyPath(sshKeyPath)
		if err != nil {
			return err
		}
		authorizedKey, err = authorizedKeyFromPrivateKey(privateKeyPath)
		if err != nil {
			return err
		}
	}

	fmt.Println("Extracting VM package...")

	// Create vm directory
	vmDir := "vm"
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return fmt.Errorf("failed to create vm directory: %w", err)
	}

	// Extract VM package, stripping the first directory component (mac-arm64 or mac-x86_64)
	if err := extractTarXZ(vmPackage, vmDir, func(path string) string {
		parts := strings.SplitN(path, "/", 2)
		if len(parts) < 2 {
			return "" // Skip the top-level directory entry
		}
		return parts[1]
	}); err != nil {
		return fmt.Errorf("failed to extract VM package: %w", err)
	}

	// Create shared directory for host-guest file sharing
	sharedDir := sharedDirName
	if err := os.MkdirAll(sharedDir, 0755); err != nil {
		return fmt.Errorf("failed to create shared directory: %w", err)
	}
	fmt.Printf("Created shared directory: %s/\n", sharedDir)

	// Extract init assets to vm-shared
	fmt.Println("Extracting init assets...")
	if err := extractTarXZ(initAssets, sharedDir, nil); err != nil {
		return fmt.Errorf("failed to extract init assets: %w", err)
	}

	publicKeyPath := filepath.Join(sharedDir, authorizedKeysName)
	if sshKeyPath == "" {
		// Generate SSH key pair for VM access
		fmt.Println("Generating SSH key pair...")
		if err := generateSSHKeyPair(privateKeyPath, publicKeyPath); err != nil {
			return fmt.Errorf("failed to generate SSH key pair: %w", err)
		}
	} else {
		fmt.Println("Using provided SSH private key...")
		if err := os.WriteFile(publicKeyPath, authorizedKey, 0644); err != nil {
			return fmt.Errorf("failed to write public key: %w", err)
		}
	}

	fmt.Printf("SSH private key: %s\n", privateKeyPath)
	fmt.Printf("SSH public key added to: %s\n", publicKeyPath)
	if err := writeRuntimeConfig(RuntimeConfig{SSHPrivateKey: privateKeyPath}); err != nil {
		return err
	}
	fmt.Printf("Runtime config written to: %s\n", runtimeConfigPath)

	fmt.Println("Successfully initialized VM")
	fmt.Printf("VM files extracted to: %s/\n", vmDir)
	fmt.Printf("Shared folder: %s/ -> /mnt/host (inside VM)\n", sharedDir)
	fmt.Println("Run 'mac-runner start <cpu_count> <ram_size> <data_size_gb>' to start the VM")
	return nil
}

// ensureDataDisk creates the data disk at requestedSizeGB if missing,
// leaves it untouched if it already matches that size, grows it if smaller,
// and returns an error if the existing disk is larger (shrinking unsupported).
func ensureDataDisk(path string, requestedSizeGB int) error {
	requestedBytes := int64(requestedSizeGB) * 1024 * 1024 * 1024

	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		fmt.Printf("Creating %dGB sparse data disk: %s\n", requestedSizeGB, path)
		if err := createSparseDataDisk(path, requestedSizeGB); err != nil {
			return fmt.Errorf("failed to create data disk: %w", err)
		}
		fmt.Println("Data disk created (sparse). It will be formatted as ext4 by VM on first boot.")
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to stat data disk: %w", err)
	}

	currentBytes := info.Size()
	switch {
	case currentBytes == requestedBytes:
		fmt.Printf("Data disk already at requested size (%dGB): %s\n", requestedSizeGB, path)
		return nil
	case currentBytes < requestedBytes:
		currentSizeGB := currentBytes / (1024 * 1024 * 1024)
		fmt.Printf("Growing data disk from %dGB to %dGB: %s\n", currentSizeGB, requestedSizeGB, path)
		f, err := os.OpenFile(path, os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("failed to open data disk: %w", err)
		}
		defer f.Close()
		if err := f.Truncate(requestedBytes); err != nil {
			return fmt.Errorf("failed to grow data disk: %w", err)
		}
		fmt.Println("Data disk grown. The VM will expand the filesystem on next boot.")
		return nil
	default:
		currentSizeGB := currentBytes / (1024 * 1024 * 1024)
		return fmt.Errorf("existing data disk is %dGB, larger than requested %dGB; shrinking data disks is not supported", currentSizeGB, requestedSizeGB)
	}
}

// parsePortOverrides parses a comma-separated list of "service:port" pairs into a map.
func parsePortOverrides(s string) (map[string]int, error) {
	overrides := make(map[string]int)
	if strings.TrimSpace(s) == "" {
		return overrides, nil
	}
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid port override %q: expected <service>:<port>", entry)
		}
		service := strings.TrimSpace(parts[0])
		portStr := strings.TrimSpace(parts[1])
		if service == "" {
			return nil, fmt.Errorf("empty service name in port override %q", entry)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid port in override %q: must be an integer 1-65535", entry)
		}
		overrides[service] = port
	}
	return overrides, nil
}

// shutdownVM asks the VM to stop and waits (briefly) for it to actually do
// so. The request itself is issued on a separate goroutine and bounded by
// requestStopTimeout: vz.RequestStop/Stop are known to sometimes never
// trigger ACPI shutdown at all (see the signal handler in runVMDaemon, which
// relies on SSH poweroff instead for exactly this reason) and have been
// observed to block the calling goroutine indefinitely when the guest can't
// acknowledge the request -- e.g. before a partially-booted guest has an
// ACPI handler running yet. Without this bound, a caller stuck here never
// reaches its own os.Exit, leaving an orphaned daemon holding the VM/disk.
func shutdownVM(vm *vz.VirtualMachine) {
	const requestStopTimeout = 5 * time.Second

	requested := make(chan struct{}, 1)
	go func() {
		if vm.CanRequestStop() {
			vm.RequestStop() //nolint:errcheck
		} else if vm.CanStop() {
			vm.Stop() //nolint:errcheck
		}
		requested <- struct{}{}
	}()

	select {
	case <-requested:
	case <-time.After(requestStopTimeout):
		fmt.Fprintln(os.Stderr,
			"Warning: VM stop request did not return in time; continuing to poll state anyway")
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if vm.State() == vz.VirtualMachineStateStopped {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// pollUntil is shared by startCmd's state-file and degraded-marker watchers,
// which otherwise duplicated the same poll loop.
func pollUntil(deadline time.Time, interval time.Duration, check func() bool) bool {
	for time.Now().Before(deadline) {
		if check() {
			return true
		}
		time.Sleep(interval)
	}

	return false
}

func startCmd(
	cpuCountStr string,
	ramSizeStr string,
	dataSizeGB int,
	portsOverride string,
	versionCheckOptions VersionCheckOptions,
) error {
	sharedDir := sharedDirName
	fmt.Printf("Starting VM with cpu_count=%s, ram_size=%s, data_size=%dGB, shared_dir=%s\n", cpuCountStr, ramSizeStr, dataSizeGB, sharedDir)

	// Check if VM has been initialized
	vmDir := "vm"
	if _, err := os.Stat(vmDir); os.IsNotExist(err) {
		return fmt.Errorf("VM not initialized. Run 'mac-runner init' first")
	}

	// Ensure the data disk exists at the requested size (create / grow / error).
	dataDiskPath := filepath.Join(vmDir, "data.img")
	if err := ensureDataDisk(dataDiskPath, dataSizeGB); err != nil {
		return err
	}

	writeVersionCheckRuntimeConfigFromOptions(sharedDir, versionCheckOptions)

	// Check if VM is already running by probing the status socket.
	if conn, err := net.DialTimeout("unix", vmSocketPath, 2*time.Second); err == nil {
		conn.SetDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
		var resp struct {
			Status string `json:"status"`
		}
		if err := json.NewEncoder(conn).Encode(map[string]string{"request": "status"}); err == nil {
			json.NewDecoder(conn).Decode(&resp) //nolint:errcheck
		}
		conn.Close()
		if resp.Status == "running" {
			return fmt.Errorf("VM is already running")
		}
	}

	// Get the current executable path
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Create log file for daemon output
	logFile, err := os.OpenFile("vm.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create log file: %w", err)
	}
	defer logFile.Close()

	// Start daemon process in background
	fmt.Println("Launching VM daemon process...")

	if err := os.Remove("vm-state.json"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove stale vm-state.json: %w", err)
	}
	if err := os.Remove(daemonDegradedMarkerFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove stale degraded-state marker: %w", err)
	}

	attr := &os.ProcAttr{
		Dir:   ".",
		Env:   os.Environ(),
		Files: []*os.File{nil, logFile, logFile}, // stdin=nil, stdout/stderr to log
		Sys: &syscall.SysProcAttr{
			Setsid: true, // Create new session to detach from terminal
		},
	}

	args := []string{executable, "__daemon__", cpuCountStr, ramSizeStr, portsOverride}

	process, err := os.StartProcess(executable, args, attr)
	if err != nil {
		return fmt.Errorf("failed to start VM daemon process: %w", err)
	}
	fmt.Printf("Daemon process started (PID: %d), waiting for VM initialization...\n", process.Pid)

	// Wait for either daemon to fail or vm-state.json to be written
	stateFile := "vm-state.json"
	timeout := 4 * time.Minute
	checkInterval := 200 * time.Millisecond
	deadline := time.Now().Add(timeout)

	// Channel to signal when state file appears
	stateCh := make(chan bool, 1)
	// Channel to signal when the daemon reports a degraded start (SSH gate
	// failed, but it is deliberately staying alive -- see
	// daemonDegradedMarkerFile)
	degradedCh := make(chan string, 1)
	// Channel to signal when daemon exits
	exitCh := make(chan error, 1)
	// Channel to signal when log tailer should stop
	stopTailCh := make(chan bool, 1)

	// Tail vm.log and forward to stderr for real-time visibility
	go func() {
		// Give the log file a moment to be written by the daemon
		time.Sleep(100 * time.Millisecond)

		var lastSize int64 = 0
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-stopTailCh:
				return
			case <-ticker.C:
				if fileInfo, err := os.Stat("vm.log"); err == nil {
					if fileInfo.Size() > lastSize {
						file, err := os.Open("vm.log")
						if err == nil {
							file.Seek(lastSize, 0)
							buf := make([]byte, fileInfo.Size()-lastSize)
							n, _ := file.Read(buf)
							if n > 0 {
								os.Stderr.Write(buf[:n])
							}
							lastSize = fileInfo.Size()
							file.Close()
						}
					}
				}
			}
		}
	}()

	// Monitor state file
	go func() {
		stateCh <- pollUntil(deadline, checkInterval, func() bool {
			_, err := os.Stat(stateFile)
			return err == nil
		})
	}()

	// Monitor degraded-state marker (SSH gate failed, daemon staying alive)
	go func() {
		pollUntil(deadline, checkInterval, func() bool {
			data, err := os.ReadFile(daemonDegradedMarkerFile)
			if err != nil {
				return false
			}
			var marker struct {
				Error string `json:"error"`
			}
			if jsonErr := json.Unmarshal(data, &marker); jsonErr != nil || marker.Error == "" {
				return false
			}
			degradedCh <- marker.Error
			return true
		})
	}()

	// Monitor daemon process
	go func() {
		state, err := process.Wait()
		if err != nil {
			exitCh <- fmt.Errorf("failed to wait for daemon: %w", err)
			return
		}
		if !state.Success() {
			// Read last lines of vm.log for context
			var logContext string
			if lines, err := readLastLines("vm.log", 20); err == nil && len(lines) > 0 {
				logContext = "\n\nLast 20 lines from vm.log:\n" + strings.Join(lines, "\n")
			}
			exitCh <- fmt.Errorf("daemon exited with error (exit code: %d)%s", state.ExitCode(), logContext)
			return
		}
		exitCh <- fmt.Errorf("daemon exited unexpectedly")
	}()

	// Wait for either condition
	select {
	case success := <-stateCh:
		stopTailCh <- true // Stop log tailer
		if !success {
			// Read last lines of vm.log for context
			var logContext string
			if lines, err := readLastLines("vm.log", 20); err == nil && len(lines) > 0 {
				logContext = "\n\nLast 20 lines from vm.log:\n" + strings.Join(lines, "\n")
			}
			return fmt.Errorf("timeout waiting for VM to start (%v)%s", timeout, logContext)
		}
		// State file exists, release the daemon process
		process.Release()

		fmt.Println("VM started successfully in background")
		fmt.Printf("Shared folder: %s/ -> /mnt/host (inside VM)\n", sharedDir)
		fmt.Println("Check vm.log for VM output")
		fmt.Println("Use 'mac-runner stop' to stop the VM")
		return nil

	case degradedErr := <-degradedCh:
		stopTailCh <- true // Stop log tailer
		// The daemon is deliberately staying alive (VM and forwarders intact)
		// so health-check/diag can still report real per-port reachability --
		// release it rather than leaving it parented to this exiting process.
		process.Release()
		return fmt.Errorf("VM failed to start: %s", degradedErr)

	case err := <-exitCh:
		stopTailCh <- true // Stop log tailer
		return fmt.Errorf("VM failed to start: %w", err)
	}
}

func runVMDaemon(cpuCountStr, ramSizeStr, portsOverride string) error {
	// This function runs as a background daemon
	sharedDir := sharedDirName
	runtimeConfig, err := loadRuntimeConfig()
	if err != nil {
		return err
	}
	sshPrivateKeyPath := runtimeConfig.SSHPrivateKey

	portOverrides, err := parsePortOverrides(portsOverride)
	if err != nil {
		return fmt.Errorf("invalid --ports argument: %w", err)
	}

	if err := startStatusListener(); err != nil {
		return fmt.Errorf("failed to start status listener: %w", err)
	}

	fmt.Printf("[%s] VM daemon started\n", time.Now().Format("15:04:05"))
	fmt.Printf("[%s] Parsing configuration: CPU=%s, RAM=%s MB\n", time.Now().Format("15:04:05"), cpuCountStr, ramSizeStr)
	fmt.Printf("[%s] Using SSH private key: %s\n", time.Now().Format("15:04:05"), sshPrivateKeyPath)

	cpuCount, err := strconv.Atoi(cpuCountStr)
	if err != nil {
		return fmt.Errorf("invalid cpu_count: %w", err)
	}

	ramSize, err := strconv.ParseUint(ramSizeStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid ram_size: %w", err)
	}

	fmt.Printf("[%s] Configuration parsed: %d CPUs, %d MB RAM\n", time.Now().Format("15:04:05"), cpuCount, ramSize)

	// Use files from vm/ directory
	vmDir := "vm"
	diskPath := filepath.Join(vmDir, "disk.img") // Use fat image with ESP for UEFI boot

	// Get absolute path for disk
	diskPath, err = filepath.Abs(diskPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for disk: %w", err)
	}

	// Check if disk exists
	fmt.Printf("[%s] Verifying VM disk...\n", time.Now().Format("15:04:05"))
	if _, err := os.Stat(diskPath); err != nil {
		return fmt.Errorf("disk image not found: %s (need fat image with ESP for UEFI boot)", diskPath)
	}
	fmt.Printf("[%s] VM disk verified\n", time.Now().Format("15:04:05"))

	fmt.Printf("[%s] Creating VM configuration...\n", time.Now().Format("15:04:05"))

	// Create EFI variable store for UEFI NVRAM
	// Modern ARM64 kernels have EFI stub and can't be used with NewLinuxBootLoader
	// Use UEFI boot instead - disk.img contains ESP with UKI
	efiVariableStorePath := filepath.Join(vmDir, "efi-nvram.bin")
	var variableStore *vz.EFIVariableStore

	// Check if variable store already exists
	if _, err := os.Stat(efiVariableStorePath); err == nil {
		// Load existing variable store
		fmt.Printf("[%s] Loading existing EFI variable store from %s...\n", time.Now().Format("15:04:05"), efiVariableStorePath)
		variableStore, err = vz.NewEFIVariableStore(efiVariableStorePath)
		if err != nil {
			return fmt.Errorf("failed to load EFI variable store: %w", err)
		}
	} else {
		// Create new variable store
		fmt.Printf("[%s] Creating new EFI variable store at %s...\n", time.Now().Format("15:04:05"), efiVariableStorePath)
		variableStore, err = vz.NewEFIVariableStore(efiVariableStorePath, vz.WithCreatingEFIVariableStore())
		if err != nil {
			return fmt.Errorf("failed to create EFI variable store: %w", err)
		}
	}

	// Create UEFI bootloader with variable store
	fmt.Printf("[%s] Creating EFI bootloader...\n", time.Now().Format("15:04:05"))
	bootLoader, err := vz.NewEFIBootLoader(vz.WithEFIVariableStore(variableStore))
	if err != nil {
		return fmt.Errorf("failed to create EFI bootloader: %w", err)
	}
	fmt.Printf("[%s] EFI bootloader configured with variable store\n", time.Now().Format("15:04:05"))

	// Create console logging attachment for hvc0 (VirtIO console)
	consoleLogPath := "vm-console.log"
	fmt.Printf("[%s] Configuring console logging to %s...\n", time.Now().Format("15:04:05"), consoleLogPath)

	// Clear console log from previous runs to ensure waitForInitOutput gets fresh output
	if err := os.Truncate(consoleLogPath, 0); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to clear console log: %w", err)
	}

	consoleAttachment, err := vz.NewFileSerialPortAttachment(consoleLogPath, true)
	if err != nil {
		return fmt.Errorf("failed to create console log attachment: %w", err)
	}

	// VirtIO console device - matches kernel console=hvc0
	// Captures kernel boot messages, OpenRC output, and init script output
	serialPort, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(consoleAttachment)
	if err != nil {
		return fmt.Errorf("failed to create serial port: %w", err)
	}
	fmt.Printf("[%s] Console logging configured\n", time.Now().Format("15:04:05"))

	// Create storage devices list
	storageDevices := []vz.StorageDeviceConfiguration{}

	// Check for separate data disk first (attach as first device if exists)
	dataDiskPath := filepath.Join(vmDir, "data.img")
	if absDataDiskPath, err := filepath.Abs(dataDiskPath); err == nil {
		if _, err := os.Stat(absDataDiskPath); err == nil {
			fmt.Printf("[%s] Attaching data disk: %s...\n", time.Now().Format("15:04:05"), dataDiskPath)

			dataDiskAttachment, err := vz.NewDiskImageStorageDeviceAttachment(absDataDiskPath, false)
			if err != nil {
				return fmt.Errorf("failed to create data disk attachment: %w", err)
			}

			dataStorageConfig, err := vz.NewVirtioBlockDeviceConfiguration(dataDiskAttachment)
			if err != nil {
				return fmt.Errorf("failed to create data storage device config: %w", err)
			}

			storageDevices = append(storageDevices, dataStorageConfig)
			fmt.Printf("[%s] Data disk attached as first device (for /var)\n", time.Now().Format("15:04:05"))
		}
	}

	// Attach boot disk
	fmt.Printf("[%s] Attaching boot disk: %s...\n", time.Now().Format("15:04:05"), diskPath)
	diskAttachment, err := vz.NewDiskImageStorageDeviceAttachment(diskPath, false)
	if err != nil {
		return fmt.Errorf("failed to create boot disk attachment: %w", err)
	}

	storageDeviceConfig, err := vz.NewVirtioBlockDeviceConfiguration(diskAttachment)
	if err != nil {
		return fmt.Errorf("failed to create storage device config: %w", err)
	}
	storageDevices = append(storageDevices, storageDeviceConfig)
	fmt.Printf("[%s] Boot disk attached\n", time.Now().Format("15:04:05"))

	// Create network device
	fmt.Printf("[%s] Configuring NAT networking...\n", time.Now().Format("15:04:05"))
	natAttachment, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return fmt.Errorf("failed to create NAT network attachment: %w", err)
	}

	networkConfig, err := vz.NewVirtioNetworkDeviceConfiguration(natAttachment)
	if err != nil {
		return fmt.Errorf("failed to create network device config: %w", err)
	}
	fmt.Printf("[%s] Network configured\n", time.Now().Format("15:04:05"))

	// Create entropy device (random number generator)
	fmt.Printf("[%s] Configuring entropy device...\n", time.Now().Format("15:04:05"))
	entropyConfig, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return fmt.Errorf("failed to create entropy device config: %w", err)
	}
	fmt.Printf("[%s] Entropy device configured\n", time.Now().Format("15:04:05"))

	// Create VirtioFS shared directory device
	fmt.Printf("[%s] Configuring VirtioFS shared directory: %s...\n", time.Now().Format("15:04:05"), sharedDir)
	// Ensure shared directory exists
	if _, err := os.Stat(sharedDir); os.IsNotExist(err) {
		fmt.Printf("[%s] Creating shared directory: %s\n", time.Now().Format("15:04:05"), sharedDir)
		if err := os.MkdirAll(sharedDir, 0755); err != nil {
			return fmt.Errorf("failed to create shared directory: %w", err)
		}
	}

	// Get absolute path
	absSharedDir, err := filepath.Abs(sharedDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for shared directory: %w", err)
	}

	// Create VirtioFS device with tag "hostshare" (matches cloud-init config)
	sharedDirObj, err := vz.NewSharedDirectory(absSharedDir, false)
	if err != nil {
		return fmt.Errorf("failed to create shared directory device: %w", err)
	}

	// Wrap in SingleDirectoryShare to implement DirectoryShare interface
	directoryShare, err := vz.NewSingleDirectoryShare(sharedDirObj)
	if err != nil {
		return fmt.Errorf("failed to create directory share: %w", err)
	}

	var sharedDirConfig *vz.VirtioFileSystemDeviceConfiguration
	sharedDirConfig, err = vz.NewVirtioFileSystemDeviceConfiguration("hostshare")
	if err != nil {
		return fmt.Errorf("failed to create VirtioFS config: %w", err)
	}
	sharedDirConfig.SetDirectoryShare(directoryShare)

	fmt.Printf("[%s] VirtioFS shared folder configured: %s -> /mnt/host\n", time.Now().Format("15:04:05"), absSharedDir)

	// Create VM configuration
	vzConfig, err := vz.NewVirtualMachineConfiguration(
		bootLoader,
		uint(cpuCount),
		ramSize*1024*1024, // Convert MB to bytes
	)
	if err != nil {
		return fmt.Errorf("failed to create VM configuration: %w", err)
	}

	// Set serial port for console logging
	vzConfig.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{
		serialPort,
	})

	vzConfig.SetStorageDevicesVirtualMachineConfiguration(storageDevices)

	vzConfig.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{
		networkConfig,
	})

	vzConfig.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{
		entropyConfig,
	})

	// Add shared directory
	vzConfig.SetDirectorySharingDevicesVirtualMachineConfiguration([]vz.DirectorySharingDeviceConfiguration{
		sharedDirConfig,
	})

	// Validate configuration
	fmt.Printf("[%s] Validating VM configuration...\n", time.Now().Format("15:04:05"))
	validated, err := vzConfig.Validate()
	if err != nil {
		return fmt.Errorf("failed to validate VM configuration: %w", err)
	}
	if !validated {
		return fmt.Errorf("VM configuration validation failed")
	}
	fmt.Printf("[%s] VM configuration validated\n", time.Now().Format("15:04:05"))

	fmt.Printf("[%s] Starting VM...\n", time.Now().Format("15:04:05"))

	// Create and start VM
	fmt.Printf("[%s] Creating virtual machine instance...\n", time.Now().Format("15:04:05"))
	vm, err := vz.NewVirtualMachine(vzConfig)
	if err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}
	fmt.Printf("[%s] Virtual machine instance created\n", time.Now().Format("15:04:05"))

	// Write PID file before starting
	pidFile := "vm.pid"
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to write PID file: %v\n", err)
	}
	defer os.Remove(pidFile)

	// Set up signal handling for graceful shutdown.
	// vz RequestStop is failing to trigger ACPI shutdown, so we must rely on ssh
	// We populate sshTarget once the VM has reported its IP.
	var sshTarget atomic.Pointer[string] // "ip:port"
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Println("Received stop signal; requesting guest poweroff via SSH...")

		sshOK := false
		if t := sshTarget.Load(); t != nil && *t != "" {
			parts := strings.SplitN(*t, ":", 2)
			host := parts[0]
			port := "22"
			if len(parts) == 2 {
				port = parts[1]
			}
			cmd := exec.Command("ssh",
				"-i", sshPrivateKeyPath,
				"-p", port,
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ConnectTimeout=5",
				"-o", "BatchMode=yes",
				"root@"+host,
				"poweroff",
			)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "ssh poweroff failed: %v; falling back to vz Stop()\n", err)
			} else {
				sshOK = true
				fmt.Println("ssh poweroff issued; waiting for VM to stop...")
			}
		} else {
			fmt.Fprintln(os.Stderr, "SSH target not yet known; falling back to vz Stop()")
		}

		if !sshOK {
			if vm.CanRequestStop() {
				if _, err := vm.RequestStop(); err != nil {
					fmt.Fprintf(os.Stderr, "RequestStop failed: %v\n", err)
					if vm.CanStop() {
						vm.Stop()
					}
				}
			} else if vm.CanStop() {
				vm.Stop()
			}
		}

		// Wait for the VM to actually reach Stopped so the guest can flush
		// dirty pages (ext4 journal, /var contents) to data.img before we exit.
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if vm.State() == vz.VirtualMachineStateStopped {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if vm.State() != vz.VirtualMachineStateStopped {
			fmt.Fprintln(os.Stderr, "Warning: VM did not stop within 60s; forcing exit (data loss possible)")
		} else {
			fmt.Println("VM stopped cleanly")
		}
		os.Exit(0)
	}()

	// Start the VM
	fmt.Printf("[%s] Starting virtual machine (this may take a few seconds)...\n", time.Now().Format("15:04:05"))
	if err := vm.Start(); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	fmt.Printf("[%s] VM started successfully!\n", time.Now().Format("15:04:05"))
	fmt.Println("VM is running with NAT networking")
	fmt.Printf("VM console output: vm-console.log\n")

	// Wait for VM to output init configuration
	fmt.Println("Waiting for VM initialization...")
	initOutput, err := waitForInitOutput("vm-console.log", 5*time.Minute)
	if err != nil {
		return fmt.Errorf("VM initialization failed: %w", err)
	}

	fmt.Printf("VM IP address: %s\n", initOutput.IP)
	fmt.Printf("VM ports: %+v\n", initOutput.Ports)

	vmIP := initOutput.IP

	// Make the SSH target visible to the shutdown signal handler. We use the
	// guest IP directly (the host can reach it) and the in-guest SSH port.
	guestSSHPort := initOutput.Ports["ssh"]
	if guestSSHPort == 0 {
		guestSSHPort = 22
	}
	target := fmt.Sprintf("%s:%d", vmIP, guestSSHPort)
	sshTarget.Store(&target)

	// Validate that all port overrides reference services reported by the VM.
	for serviceName := range portOverrides {
		if _, ok := initOutput.Ports[serviceName]; !ok {
			knownNames := make([]string, 0, len(initOutput.Ports))
			for n := range initOutput.Ports {
				knownNames = append(knownNames, n)
			}
			sort.Strings(knownNames)
			shutdownVM(vm)
			return fmt.Errorf("--ports references unknown service %q; known services: %s",
				serviceName, strings.Join(knownNames, ", "))
		}
	}

	// Start port forwarders dynamically for all ports in init output, before
	// waiting on SSH readiness below. This way a blocked host-to-VM network
	// path (e.g. macOS Local Network permission denied to the invoking app)
	// is still visible via `health-check` even when the SSH-readiness gate
	// never passes, instead of leaving no evidence behind at all.
	ctx := context.Background()
	forwarders := make(map[string]*LoopbackForwarder)
	hostPorts := make(map[string]int)

	for portName, guestPort := range initOutput.Ports {
		if guestPort == 0 {
			fmt.Fprintf(os.Stderr, "Warning: Skipping port forwarding for %s (port is 0)\n", portName)
			continue
		}

		var forwarder *LoopbackForwarder
		if overridePort, hasOverride := portOverrides[portName]; hasOverride {
			// User specified an exact host port — hard failure if it cannot be bound.
			forwarder, err = StartLoopbackForwarder(ctx, portName, overridePort, vmIP, guestPort)
			if err != nil {
				for _, f := range forwarders {
					f.Close()
				}
				shutdownVM(vm)
				return fmt.Errorf("cannot bind host port %d for service %q (requested via --ports): %w", overridePort, portName, err)
			}
		} else {
			// Default: try same port as the VM, fall back to OS-assigned.
			forwarder, err = StartLoopbackForwarder(ctx, portName, guestPort, vmIP, guestPort)
			if err != nil {
				forwarder, err = StartLoopbackForwarder(ctx, portName, 0, vmIP, guestPort)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Failed to start %s port forwarder: %v\n", portName, err)
				continue
			}
		}

		forwarders[portName] = forwarder
		registerForwarder(portName, forwarder)
		hostPort := forwarder.Port()
		hostPorts[portName] = hostPort
		fmt.Printf("%s forwarding: 127.0.0.1:%d -> %s:%d\n", portName, hostPort, vmIP, guestPort)
	}

	// Ensure forwarders are closed on exit
	defer func() {
		for _, forwarder := range forwarders {
			forwarder.Close()
		}
	}()

	fmt.Printf("Waiting for SSH service at %s...\n", target)
	if sshErr := waitForSSHService(target, 2*time.Minute); sshErr != nil {
		// Do not shut down the VM or forwarders here: they may be perfectly
		// healthy and only the host-to-VM network path is blocked. Keep the
		// daemon alive and queryable via health-check/diag so the launcher
		// can tell that apart from a genuine boot failure, instead of
		// destroying the only evidence of what actually happened. The parent
		// `start` invocation is still told about this failure below, via
		// daemonDegradedMarkerFile, so `exasol start` itself still reports
		// an error exactly as before.
		if markErr := writeDaemonDegradedMarker(sshErr); markErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to record degraded start state: %v\n", markErr)
		}
		fmt.Fprintf(os.Stderr,
			"Warning: SSH did not become ready (%v); VM and port forwarders are staying "+
				"up so health-check/diag can still report real per-port reachability.\n", sshErr)
	} else {
		fmt.Println("SSH service is ready")
		if err := writeHealthyStartArtifacts(
			vmIP, cpuCountStr, ramSizeStr, sharedDir, sshPrivateKeyPath, hostPorts,
		); err != nil {
			return err
		}
	}

	// Wait for VM to finish (or be interrupted). This runs whether or not
	// SSH became ready, so a degraded daemon stays alive (and stoppable via
	// the normal `stop` signal handler above) for diagnostics rather than
	// exiting and losing all forwarder/health state.
	for {
		if vm.State() == vz.VirtualMachineStateStopped {
			fmt.Println("VM stopped")
			break
		}
		time.Sleep(1 * time.Second)
	}
	return nil
}

// writeHealthyStartArtifacts writes vm-state.json and prints access
// information once SSH readiness has been confirmed.
func writeHealthyStartArtifacts(
	vmIP, cpuCountStr, ramSizeStr, sharedDir, sshPrivateKeyPath string,
	hostPorts map[string]int,
) error {
	vmState := map[string]interface{}{
		"vm_name":   "exasol-local-vm",
		"vm_ip":     vmIP,
		"cpu_count": cpuCountStr,
		"ram_size":  ramSizeStr,
		"pid":       fmt.Sprintf("%d", os.Getpid()),
		"ports":     hostPorts,
	}
	// Use relative path for shared directory
	vmState["shared_dir"] = "./" + filepath.Base(sharedDir)

	if _, err := os.Stat(sshPrivateKeyPath); err == nil {
		vmState["ssh_private_key"] = displayPath(sshPrivateKeyPath)
	} else {
		fmt.Fprintf(os.Stderr, "Warning: SSH private key not found: %s\n", sshPrivateKeyPath)
	}

	stateData, err := json.MarshalIndent(vmState, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal vm-state: %w", err)
	}

	if err := os.WriteFile("vm-state.json", stateData, 0644); err != nil {
		return fmt.Errorf("failed to write vm-state.json: %w", err)
	}

	fmt.Println("VM state written to vm-state.json")

	// Display access information
	fmt.Println("\n=== VM Access Information ===")
	if sshPort, ok := hostPorts["ssh"]; ok && sshPort > 0 {
		fmt.Printf("SSH:      ssh -i %s -p %d -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null root@127.0.0.1\n", displayPath(sshPrivateKeyPath), sshPort)
	}
	if dbPort, ok := hostPorts["db"]; ok && dbPort > 0 {
		fmt.Printf("Database: 127.0.0.1:%d\n", dbPort)
	}

	return nil
}

// daemonDegradedMarkerFile signals the parent `start` invocation that the
// daemon reached a "SSH readiness gate failed, but VM/forwarders are staying
// up for diagnostics" state, distinct from both success (vm-state.json) and
// a genuine daemon crash (process exit). Removed at the start of each fresh
// start attempt alongside vm-state.json.
const daemonDegradedMarkerFile = "vm-state-degraded.json"

func writeDaemonDegradedMarker(cause error) error {
	data, err := json.MarshalIndent(map[string]string{"error": cause.Error()}, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal degraded-state marker: %w", err)
	}

	if err := os.WriteFile(daemonDegradedMarkerFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write degraded-state marker: %w", err)
	}

	return nil
}

const (
	// healthCheckPerPortTimeout bounds a single forwarder's probe dial, so a
	// blocked/hanging guest connection cannot stall the whole health-check.
	healthCheckPerPortTimeout = 2 * time.Second
	// healthCheckConnDeadline bounds the whole health-check request/response,
	// generous enough for every forwarder's probe to run concurrently and
	// still finish comfortably inside it.
	healthCheckConnDeadline = 10 * time.Second
	// statusConnDeadline bounds the cheap, non-probing "status" request.
	statusConnDeadline = 5 * time.Second
)

var (
	forwarderRegistryMu sync.RWMutex
	forwarderRegistry   = map[string]*LoopbackForwarder{}
)

// registerForwarder makes a forwarder visible to health-check requests on
// the status socket, keyed by its service name (e.g. "ssh", "db", "ui").
func registerForwarder(name string, forwarder *LoopbackForwarder) {
	forwarderRegistryMu.Lock()
	defer forwarderRegistryMu.Unlock()
	forwarderRegistry[name] = forwarder
}

func forwarderSnapshot() map[string]*LoopbackForwarder {
	forwarderRegistryMu.RLock()
	defer forwarderRegistryMu.RUnlock()

	snapshot := make(map[string]*LoopbackForwarder, len(forwarderRegistry))
	for name, forwarder := range forwarderRegistry {
		snapshot[name] = forwarder
	}

	return snapshot
}

// portHealthResponse is the per-port shape returned by a health-check request.
type portHealthResponse struct {
	State string `json:"state"`
}

// probeForwarders always dials fresh rather than returning each forwarder's
// last-observed state, so a port nothing has recently connected through
// (e.g. SSH during a plain start/connect) still gets a current answer.
func probeForwarders(ctx context.Context) map[string]portHealthResponse {
	snapshot := forwarderSnapshot()
	result := make(map[string]portHealthResponse, len(snapshot))

	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, forwarder := range snapshot {
		wg.Add(1)
		go func(name string, forwarder *LoopbackForwarder) {
			defer wg.Done()
			state := forwarder.Probe(ctx, healthCheckPerPortTimeout)
			mu.Lock()
			result[name] = portHealthResponse{State: state}
			mu.Unlock()
		}(name, forwarder)
	}
	wg.Wait()

	return result
}

// startStatusListener serves {"request":"status"} -> {"status":"running"}
// and {"request":"health-check"} -> {"ports": {"<name>": {"state": "..."}}}
// on a Unix domain socket. health-check is kept separate from status, and
// only dials out when explicitly requested, so routine status polling never
// pays for (or triggers) a network probe. Stale sockets from a previous run
// are removed on entry.
func startStatusListener() error {
	os.Remove(vmSocketPath)
	ln, err := net.Listen("unix", vmSocketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", vmSocketPath, err)
	}
	go func() {
		defer ln.Close()
		defer os.Remove(vmSocketPath)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				c.SetDeadline(time.Now().Add(statusConnDeadline)) //nolint:errcheck
				var req struct {
					Request string `json:"request"`
				}
				if err := json.NewDecoder(c).Decode(&req); err != nil {
					return
				}

				switch req.Request {
				case "status":
					json.NewEncoder(c).Encode(map[string]string{"status": "running"}) //nolint:errcheck
				case "health-check":
					c.SetDeadline(time.Now().Add(healthCheckConnDeadline)) //nolint:errcheck
					ports := probeForwarders(context.Background())
					json.NewEncoder(c).Encode(map[string]any{"ports": ports}) //nolint:errcheck
				default:
					return
				}
			}(conn)
		}
	}()
	return nil
}

// isVMRunning checks VM liveness by querying the status Unix domain socket
// rather than relying on the PID file, which can be stale after an improper
// shutdown.
func isVMRunning() bool {
	conn, err := net.DialTimeout("unix", vmSocketPath, 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
	if err := json.NewEncoder(conn).Encode(map[string]string{"request": "status"}); err != nil {
		return false
	}
	var resp struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return false
	}
	return resp.Status == "running"
}

// queryHealthCheck asks the daemon to perform a fresh reachability probe of
// every forwarded port. The client deadline is kept above
// healthCheckConnDeadline so the daemon's own bound is what actually
// determines the outcome, not a client-side race against it.
func queryHealthCheck() (map[string]portHealthResponse, error) {
	const clientDeadline = healthCheckConnDeadline + 2*time.Second

	conn, err := net.DialTimeout("unix", vmSocketPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to VM socket: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(clientDeadline)) //nolint:errcheck

	if err := json.NewEncoder(conn).Encode(map[string]string{"request": "health-check"}); err != nil {
		return nil, fmt.Errorf("failed to send health-check request: %w", err)
	}

	var resp struct {
		Ports map[string]portHealthResponse `json:"ports"`
	}
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("failed to decode health-check response: %w", err)
	}

	return resp.Ports, nil
}

func healthCheckCmd() error {
	ports, err := queryHealthCheck()
	if err != nil {
		return err
	}

	out, err := json.Marshal(map[string]any{"ports": ports})
	if err != nil {
		return fmt.Errorf("failed to marshal health-check result: %w", err)
	}
	fmt.Println(string(out))

	return nil
}

func statusCmd() error {
	running := isVMRunning()

	out, err := json.Marshal(map[string]bool{"running": running})
	if err != nil {
		return fmt.Errorf("failed to marshal status: %w", err)
	}
	fmt.Println(string(out))
	return nil
}

func stopCmd() error {
	fmt.Println("Stopping VM...")

	// Read PID file
	pidFile := "vm.pid"
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		return fmt.Errorf("VM is not running (no PID file found)")
	}

	pidStr := strings.TrimSpace(string(pidData))
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return fmt.Errorf("invalid PID in file: %w", err)
	}

	// Send SIGTERM to the VM process
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find VM process: %w", err)
	}

	if err := process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("failed to send stop signal to VM: %w", err)
	}

	fmt.Println("Stop signal sent to VM; waiting for it to stop...")

	// The daemon's own signal handler tries an SSH poweroff and waits up to
	// 60s for the VM to reach Stopped before exiting, so give it enough room
	// to finish before giving up. Without this wait, a caller that starts a
	// new VM against the same disk images immediately after stop returns can
	// race the old daemon while it's still shutting down and corrupt guest
	// disk state.
	deadline := time.Now().Add(65 * time.Second)
	for time.Now().Before(deadline) {
		if process.Signal(syscall.Signal(0)) != nil {
			os.Remove(pidFile)
			fmt.Println("VM stopped")
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("VM (pid %d) did not stop within 65s of receiving the stop signal", pid)
}

func resizeDataDiskCmd(newSizeStr string) error {
	// Check if VM is running
	if isVMRunning() {
		return fmt.Errorf("VM is currently running. Stop the VM first with 'mac-runner stop'")
	}

	// Parse and validate new size
	newSizeGB, err := strconv.Atoi(newSizeStr)
	if err != nil {
		return fmt.Errorf("invalid size: %w", err)
	}

	// Check if data disk exists
	vmDir := "vm"
	dataDiskPath := filepath.Join(vmDir, "data.img")
	if _, err := os.Stat(dataDiskPath); os.IsNotExist(err) {
		return fmt.Errorf("data disk not found: %s. Initialize VM first with 'mac-runner init'", dataDiskPath)
	}

	// Get current size
	fileInfo, err := os.Stat(dataDiskPath)
	if err != nil {
		return fmt.Errorf("failed to stat data disk: %w", err)
	}
	currentSizeGB := fileInfo.Size() / (1024 * 1024 * 1024)

	// Check if new size is actually larger
	if int64(newSizeGB) <= currentSizeGB {
		return fmt.Errorf("new size (%dGB) must be larger than current size (%dGB). Shrinking is not supported", newSizeGB, currentSizeGB)
	}

	// Resize the sparse file
	fmt.Printf("Resizing data disk from %dGB to %dGB...\n", currentSizeGB, newSizeGB)
	f, err := os.OpenFile(dataDiskPath, os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open disk: %w", err)
	}
	defer f.Close()

	newSizeBytes := int64(newSizeGB) * 1024 * 1024 * 1024
	if err := f.Truncate(newSizeBytes); err != nil {
		return fmt.Errorf("failed to resize disk: %w", err)
	}

	fmt.Printf("Data disk successfully resized to %dGB\n", newSizeGB)
	fmt.Println("Restart the VM for changes to take effect:")
	fmt.Println("The VM will automatically expand the filesystem on next boot.")
	return nil
}
