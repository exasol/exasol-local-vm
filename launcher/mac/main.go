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
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

const (
	// guestIPv4 is the expected IP for the first VM in Virtualization.framework NAT
	// The actual IP is detected at runtime by parsing console output
	guestIPv4       = "192.168.64.2"
	guestSSHPort    = 22
	guestDBPort     = 8563  // Exasol database SQL port
	guestUIPort     = 8443  // Exasol web UI port (HTTPS)
)

// InitOutput represents the JSON output from the VM init scripts
type InitOutput struct {
	IP    string         `json:"ip"`
	Ports map[string]int `json:"ports"`
}

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
	listener   net.Listener
	guestHost  string
	guestPort  int
	closeOnce  sync.Once
	closeError error
	wg         sync.WaitGroup
}

// StartLoopbackForwarder starts a TCP proxy from hostPort to guestHost:guestPort
// If hostPort is 0, the OS will allocate a free port dynamically
func StartLoopbackForwarder(ctx context.Context, hostPort int, guestHost string, guestPort int) (*LoopbackForwarder, error) {
	listener, err := (&net.ListenConfig{}).Listen(
		ctx,
		"tcp",
		fmt.Sprintf("127.0.0.1:%d", hostPort),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on 127.0.0.1:%d: %w", hostPort, err)
	}

	forwarder := &LoopbackForwarder{
		listener:  listener,
		guestHost: guestHost,
		guestPort: guestPort,
	}
	forwarder.wg.Add(1)
	go forwarder.acceptLoop(ctx)

	return forwarder, nil
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
		fmt.Fprintln(os.Stderr, "  init [--data-size SIZE]  Initialize VM (SIZE in GB, default: 50)")
		fmt.Fprintln(os.Stderr, "  start <cpu> <ram>        Start VM with CPU count and RAM size (MB)")
		fmt.Fprintln(os.Stderr, "  stop                     Stop running VM")
		fmt.Fprintln(os.Stderr, "  resize-data <size>       Resize data disk to SIZE GB (VM must be stopped)")
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "init":
		// Parse optional --data-size flag
		dataSizeGB := 50 // default
		for i := 2; i < len(os.Args); i++ {
			if os.Args[i] == "--data-size" {
				if i+1 >= len(os.Args) {
					fmt.Fprintln(os.Stderr, "Error: --data-size requires a value")
					os.Exit(1)
				}
				size, parseErr := strconv.Atoi(os.Args[i+1])
				if parseErr != nil {
					fmt.Fprintf(os.Stderr, "Error: invalid data size: %v\n", parseErr)
					os.Exit(1)
				}
				dataSizeGB = size
				break
			}
		}
		err = initCmd(dataSizeGB)
	case "start":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: mac-runner start <cpu_count> <ram_size>")
			os.Exit(1)
		}
		err = startCmd(os.Args[2], os.Args[3])
	case "__daemon__":
		// Internal daemon mode - run VM in background
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Invalid daemon arguments")
			os.Exit(1)
		}
		err = runVMDaemon(os.Args[2], os.Args[3])
	case "stop":
		err = stopCmd()
	case "resize-data":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: mac-runner resize-data <new_size_gb>")
			os.Exit(1)
		}
		err = resizeDataDiskCmd(os.Args[2])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		fmt.Fprintln(os.Stderr, "Available commands: init, start, stop")
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

func initCmd(dataSizeGB int) error {
	fmt.Println("Initializing VM...")
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
	sharedDir := "vm-shared"
	if err := os.MkdirAll(sharedDir, 0755); err != nil {
		return fmt.Errorf("failed to create shared directory: %w", err)
	}
	fmt.Printf("Created shared directory: %s/\n", sharedDir)

	// Extract init assets to vm-shared
	fmt.Println("Extracting init assets...")
	if err := extractTarXZ(initAssets, sharedDir, nil); err != nil {
		return fmt.Errorf("failed to extract init assets: %w", err)
	}

	// Generate SSH key pair for VM access
	fmt.Println("Generating SSH key pair...")
	privateKeyPath := "vm-ssh-key"
	publicKeyPath := filepath.Join(sharedDir, "authorized_keys")
	
	if err := generateSSHKeyPair(privateKeyPath, publicKeyPath); err != nil {
		return fmt.Errorf("failed to generate SSH key pair: %w", err)
	}
	
	fmt.Printf("SSH private key: %s\n", privateKeyPath)
	fmt.Printf("SSH public key added to: %s\n", publicKeyPath)

	// Create sparse data disk for /var
	dataDiskPath := filepath.Join(vmDir, "data.img")
	fmt.Printf("Creating %dGB sparse data disk...\n", dataSizeGB)
	if err := createSparseDataDisk(dataDiskPath, dataSizeGB); err != nil {
		return fmt.Errorf("failed to create data disk: %w", err)
	}
	fmt.Printf("Data disk created: %s (sparse, grows on demand)\n", dataDiskPath)
	fmt.Println("Note: Disk will be formatted as ext4 by VM on first boot")

	fmt.Println("Successfully initialized VM")
	fmt.Printf("VM files extracted to: %s/\n", vmDir)
	fmt.Printf("Shared folder: %s/ -> /mnt/host (inside VM)\n", sharedDir)
	fmt.Println("Run 'mac-runner start <cpu_count> <ram_size>' to start the VM")
	return nil
}

func startCmd(cpuCountStr, ramSizeStr string) error {
	sharedDir := "vm-shared"
	fmt.Printf("Starting VM with cpu_count=%s, ram_size=%s, shared_dir=%s\n", cpuCountStr, ramSizeStr, sharedDir)

	// Check if VM has been initialized
	vmDir := "vm"
	if _, err := os.Stat(vmDir); os.IsNotExist(err) {
		return fmt.Errorf("VM not initialized. Run 'mac-runner init' first")
	}

	// Check if VM is already running
	pidFile := "vm.pid"
	if _, err := os.Stat(pidFile); err == nil {
		pidData, _ := os.ReadFile(pidFile)
		if len(pidData) > 0 {
			pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
			if err == nil {
				// Check if process exists
				if process, err := os.FindProcess(pid); err == nil {
					if err := process.Signal(syscall.Signal(0)); err == nil {
						return fmt.Errorf("VM is already running (PID: %d)", pid)
					}
				}
			}
		}
		// Stale PID file, remove it
		os.Remove(pidFile)
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
	attr := &os.ProcAttr{
		Dir: ".",
		Env: os.Environ(),
		Files: []*os.File{nil, logFile, logFile}, // stdin=nil, stdout/stderr to log
		Sys: &syscall.SysProcAttr{
			Setsid: true, // Create new session to detach from terminal
		},
	}

	args := []string{executable, "__daemon__", cpuCountStr, ramSizeStr}

	process, err := os.StartProcess(executable, args, attr)
	if err != nil {
		return fmt.Errorf("failed to start VM daemon process: %w", err)
	}
	fmt.Printf("Daemon process started (PID: %d), waiting for VM initialization...\n", process.Pid)

	// Wait for either daemon to fail or vm-state.json to be written
	stateFile := "vm-state.json"
	timeout := 60 * time.Second
	checkInterval := 200 * time.Millisecond
	deadline := time.Now().Add(timeout)

	// Channel to signal when state file appears
	stateCh := make(chan bool, 1)
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
		for time.Now().Before(deadline) {
			if _, err := os.Stat(stateFile); err == nil {
				stateCh <- true
				return
			}
			time.Sleep(checkInterval)
		}
		stateCh <- false
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
			return fmt.Errorf("timeout waiting for VM to start (60s)%s", logContext)
		}
		// State file exists, release the daemon process
		process.Release()
		
		fmt.Println("VM started successfully in background")
		fmt.Printf("Shared folder: %s/ -> /mnt/host (inside VM)\n", sharedDir)
		fmt.Println("Check vm.log for VM output")
		fmt.Println("Use 'mac-runner stop' to stop the VM")
		return nil

	case err := <-exitCh:
		stopTailCh <- true // Stop log tailer
		return fmt.Errorf("VM failed to start: %w", err)
	}
}

func runVMDaemon(cpuCountStr, ramSizeStr string) error {
	// This function runs as a background daemon
	sharedDir := "vm-shared"
	
	fmt.Printf("[%s] VM daemon started\n", time.Now().Format("15:04:05"))
	fmt.Printf("[%s] Parsing configuration: CPU=%s, RAM=%s MB\n", time.Now().Format("15:04:05"), cpuCountStr, ramSizeStr)
	
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

	// Set up signal handling for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Println("Received stop signal, shutting down VM...")
		if vm.CanStop() {
			vm.Stop()
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

	// Start port forwarders dynamically for all ports in init output
	ctx := context.Background()
	forwarders := make(map[string]*LoopbackForwarder)
	hostPorts := make(map[string]int)
	
	for portName, guestPort := range initOutput.Ports {
		if guestPort == 0 {
			fmt.Fprintf(os.Stderr, "Warning: Skipping port forwarding for %s (port is 0)\n", portName)
			continue
		}
		
		forwarder, err := StartLoopbackForwarder(ctx, 0, vmIP, guestPort)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to start %s port forwarder: %v\n", portName, err)
			continue
		}
		
		forwarders[portName] = forwarder
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

	// Write vm-state.json with runtime configuration
	vmState := map[string]interface{}{
		"vm_name":   "exasol-nano-vm",
		"vm_ip":     vmIP,
		"cpu_count": cpuCountStr,
		"ram_size":  ramSizeStr,
		"pid":       fmt.Sprintf("%d", os.Getpid()),
		"ports":     hostPorts,
	}
	// Use relative path for shared directory
	vmState["shared_dir"] = "./" + filepath.Base(sharedDir)
	
	// Add SSH private key path if it exists (relative path)
	privateKeyPath := "vm-ssh-key"
	if _, err := os.Stat(privateKeyPath); err == nil {
		vmState["ssh_private_key"] = "./" + privateKeyPath
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
		fmt.Printf("SSH:      ssh -p %d exasol@127.0.0.1\n", sshPort)
	}
	if dbPort, ok := hostPorts["db"]; ok && dbPort > 0 {
		fmt.Printf("Database: 127.0.0.1:%d\n", dbPort)
	}
	if uiPort, ok := hostPorts["ui"]; ok && uiPort > 0 {
		fmt.Printf("Web UI:   https://127.0.0.1:%d\n", uiPort)
	}

	// Wait for VM to finish (or be interrupted)
	for { // TODO is there a better way to wait for VM to exit, instead of polling state?
		if vm.State() == vz.VirtualMachineStateStopped {
			fmt.Println("VM stopped")
			break
		}
		time.Sleep(1 * time.Second)
	}
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

	fmt.Println("Stop signal sent to VM")
	fmt.Println("VM should stop gracefully")
	return nil
}

func resizeDataDiskCmd(newSizeStr string) error {
	// Check if VM is running
	pidFile := "vm.pid"
	if _, err := os.Stat(pidFile); err == nil {
		pidData, _ := os.ReadFile(pidFile)
		if len(pidData) > 0 {
			pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
			if err == nil {
				if process, err := os.FindProcess(pid); err == nil {
					if err := process.Signal(syscall.Signal(0)); err == nil {
						return fmt.Errorf("VM is currently running (PID: %d). Stop the VM first with 'mac-runner stop'", pid)
					}
				}
			}
		}
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
