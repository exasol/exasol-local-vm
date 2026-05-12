package main

import (
	"archive/tar"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
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
)

//go:embed vm-package.tar.xz
var vmPackage []byte

const (
	// guestIPv4 is the expected IP for the first VM in Virtualization.framework NAT
	// The actual IP is detected at runtime by parsing console output
	guestIPv4    = "192.168.64.2"
	guestSSHPort = 22
	hostSSHPort  = 2222
)

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
	deadline := time.Now().Add(timeout)
	
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(consoleLogPath)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		
		// Look for "*** EXASOL_VM_IP=192.168.64.2 ***"
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			if strings.Contains(line, "EXASOL_VM_IP=") {
				// Extract IP address
				parts := strings.Split(line, "EXASOL_VM_IP=")
				if len(parts) >= 2 {
					ip := strings.TrimSpace(strings.Trim(parts[1], "*"))
					if net.ParseIP(ip) != nil {
						return ip, nil
					}
				}
			}
		}
		
		time.Sleep(500 * time.Millisecond)
	}
	
	return "", fmt.Errorf("timeout waiting for VM IP address after %v", timeout)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: mac-runner <command>")
		fmt.Fprintln(os.Stderr, "Available commands: init, start, stop")
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "init":
		err = initCmd()
	case "start":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: mac-runner start <cpu_count> <ram_size> [shared_dir]")
			os.Exit(1)
		}
		sharedDir := ""
		if len(os.Args) >= 5 {
			sharedDir = os.Args[4]
		}
		err = startCmd(os.Args[2], os.Args[3], sharedDir)
	case "__daemon__":
		// Internal daemon mode - run VM in background
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Invalid daemon arguments")
			os.Exit(1)
		}
		sharedDir := ""
		if len(os.Args) >= 5 {
			sharedDir = os.Args[4]
		}
		err = runVMDaemon(os.Args[2], os.Args[3], sharedDir)
	case "stop":
		err = stopCmd()
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

func initCmd() error {
	fmt.Println("Initializing VM...")
	fmt.Println("Extracting VM package...")

	// Create vm directory
	vmDir := "vm"
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return fmt.Errorf("failed to create vm directory: %w", err)
	}

	// Create a reader from the embedded data
	bytesReader := bytes.NewReader(vmPackage)

	// Create xz decompressor
	xzReader, err := xz.NewReader(bytesReader)
	if err != nil {
		return fmt.Errorf("failed to create xz reader: %w", err)
	}

	// Create tar reader
	tarReader := tar.NewReader(xzReader)

	// Extract all files from the archive
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar header: %w", err)
		}

		// Construct output path in vm directory
		// The archive contains mac-arm64/files, we want to extract to vm/files
		relPath := header.Name
		// Strip the first directory component (mac-arm64 or mac-x86_64)
		parts := strings.SplitN(relPath, "/", 2)
		if len(parts) < 2 {
			continue // Skip the top-level directory entry
		}
		outputPath := filepath.Join(vmDir, parts[1])

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(outputPath, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", outputPath, err)
			}
		case tar.TypeReg:
			// Ensure parent directory exists
			parentDir := filepath.Dir(outputPath)
			if err := os.MkdirAll(parentDir, 0755); err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}

			outFile, err := os.Create(outputPath)
			if err != nil {
				return fmt.Errorf("failed to create output file %s: %w", outputPath, err)
			}

			if _, err := io.Copy(outFile, tarReader); err != nil {
				outFile.Close()
				return fmt.Errorf("failed to extract file %s: %w", outputPath, err)
			}
			outFile.Close()

			// Preserve executable permissions if set
			if header.Mode&0111 != 0 {
				os.Chmod(outputPath, 0755)
			}

			fmt.Printf("Extracted: %s\n", outputPath)
		}
	}

	fmt.Println("Successfully initialized VM")
	fmt.Printf("VM files extracted to: %s/\n", vmDir)
	fmt.Println("Run 'mac-runner start <cpu_count> <ram_size> [shared_dir]' to start the VM")
	return nil
}

func startCmd(cpuCountStr, ramSizeStr, sharedDir string) error {
	if sharedDir != "" {
		fmt.Printf("Starting VM with cpu_count=%s, ram_size=%s, shared_dir=%s\n", cpuCountStr, ramSizeStr, sharedDir)
	} else {
		fmt.Printf("Starting VM with cpu_count=%s, ram_size=%s\n", cpuCountStr, ramSizeStr)
	}

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
	attr := &os.ProcAttr{
		Dir: ".",
		Env: os.Environ(),
		Files: []*os.File{nil, logFile, logFile}, // stdin=nil, stdout/stderr to log
		Sys: &syscall.SysProcAttr{
			Setsid: true, // Create new session to detach from terminal
		},
	}

	args := []string{executable, "__daemon__", cpuCountStr, ramSizeStr}
	if sharedDir != "" {
		abs, err := filepath.Abs(sharedDir)
		if err != nil {
			return fmt.Errorf("failed to get absolute path for shared directory: %w", err)
		}
		args = append(args, abs)
	}

	process, err := os.StartProcess(executable, args, attr)
	if err != nil {
		return fmt.Errorf("failed to start VM daemon: %w", err)
	}

	// Release the process so it can run independently
	process.Release()

	// Wait a moment for the daemon to write the PID file
	time.Sleep(500 * time.Millisecond)

	// Verify the VM started successfully
	if _, err := os.Stat(pidFile); err != nil {
		return fmt.Errorf("VM may have failed to start - check vm.log for details")
	}

	fmt.Println("VM started successfully in background")
	if sharedDir != "" {
		fmt.Printf("Shared folder: %s -> /mnt/host (inside VM)\n", sharedDir)
	}
	fmt.Println("Check vm.log for VM output")
	fmt.Println("Use 'mac-runner stop' to stop the VM")
	return nil
}

func runVMDaemon(cpuCountStr, ramSizeStr, sharedDir string) error {
	// This function runs as a background daemon
	
	cpuCount, err := strconv.Atoi(cpuCountStr)
	if err != nil {
		return fmt.Errorf("invalid cpu_count: %w", err)
	}

	ramSize, err := strconv.ParseUint(ramSizeStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid ram_size: %w", err)
	}

	// Use files from vm/ directory
	vmDir := "vm"
	kernelPath := filepath.Join(vmDir, "vmlinuz-virt")
	initramfsPath := filepath.Join(vmDir, "initramfs.img")
	diskPath := filepath.Join(vmDir, "disk_thin.img")
	cmdlinePath := filepath.Join(vmDir, "kernel-cmdline.txt")

	// Get absolute paths for all VM files
	kernelPath, err = filepath.Abs(kernelPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for kernel: %w", err)
	}

	initramfsPath, err = filepath.Abs(initramfsPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for initramfs: %w", err)
	}

	diskPath, err = filepath.Abs(diskPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for disk: %w", err)
	}

	cmdlinePath, err = filepath.Abs(cmdlinePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for cmdline: %w", err)
	}

	// Check if all files exist
	if _, err := os.Stat(kernelPath); err != nil {
		return fmt.Errorf("kernel not found: %s", kernelPath)
	}
	if _, err := os.Stat(initramfsPath); err != nil {
		return fmt.Errorf("initramfs not found: %s", initramfsPath)
	}
	if _, err := os.Stat(diskPath); err != nil {
		return fmt.Errorf("disk image not found: %s", diskPath)
	}
	if _, err := os.Stat(cmdlinePath); err != nil {
		return fmt.Errorf("kernel cmdline not found: %s", cmdlinePath)
	}

	// Read kernel command line
	cmdlineData, err := os.ReadFile(cmdlinePath)
	if err != nil {
		return fmt.Errorf("failed to read kernel cmdline: %w", err)
	}
	cmdline := strings.TrimSpace(string(cmdlineData))

	fmt.Println("Creating VM configuration...")

	// Create bootloader (Linux direct boot)
	bootLoader, err := vz.NewLinuxBootLoader(
		kernelPath,
		vz.WithInitrd(initramfsPath),
		vz.WithCommandLine(cmdline),
	)
	if err != nil {
		return fmt.Errorf("failed to create Linux bootloader: %w", err)
	}

	// Create console logging attachment
	consoleLogPath := "vm-console.log"
	consoleAttachment, err := vz.NewFileSerialPortAttachment(consoleLogPath, true)
	if err != nil {
		return fmt.Errorf("failed to create console log attachment: %w", err)
	}

	serialPort, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(consoleAttachment)
	if err != nil {
		return fmt.Errorf("failed to create console device serial port: %w", err)
	}

	// Create disk attachment
	diskAttachment, err := vz.NewDiskImageStorageDeviceAttachment(diskPath, false)
	if err != nil {
		return fmt.Errorf("failed to create disk attachment: %w", err)
	}

	storageDeviceConfig, err := vz.NewVirtioBlockDeviceConfiguration(diskAttachment)
	if err != nil {
		return fmt.Errorf("failed to create storage device config: %w", err)
	}

	// Create network device
	natAttachment, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		return fmt.Errorf("failed to create NAT network attachment: %w", err)
	}

	networkConfig, err := vz.NewVirtioNetworkDeviceConfiguration(natAttachment)
	if err != nil {
		return fmt.Errorf("failed to create network device config: %w", err)
	}

	// Create entropy device (random number generator)
	entropyConfig, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return fmt.Errorf("failed to create entropy device config: %w", err)
	}

	// Create VirtioFS shared directory device if provided
	var sharedDirConfig *vz.VirtioFileSystemDeviceConfiguration
	if sharedDir != "" {
		// Ensure shared directory exists
		if _, err := os.Stat(sharedDir); os.IsNotExist(err) {
			fmt.Printf("Creating shared directory: %s\n", sharedDir)
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

		sharedDirConfig, err = vz.NewVirtioFileSystemDeviceConfiguration("hostshare")
		if err != nil {
			return fmt.Errorf("failed to create VirtioFS config: %w", err)
		}
		err = sharedDirConfig.SetDirectoryShare(directoryShare)
		if err != nil {
			return fmt.Errorf("failed to set directory share: %w", err)
		}

		fmt.Printf("VirtioFS shared folder configured: %s -> /mnt/host\n", absSharedDir)
	}

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

	vzConfig.SetStorageDevicesVirtualMachineConfiguration([]vz.StorageDeviceConfiguration{
		storageDeviceConfig,
	})

	vzConfig.SetNetworkDevicesVirtualMachineConfiguration([]*vz.VirtioNetworkDeviceConfiguration{
		networkConfig,
	})

	vzConfig.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{
		entropyConfig,
	})

	// Add shared directory if configured
	if sharedDirConfig != nil {
		vzConfig.SetDirectorySharingDevicesVirtualMachineConfiguration([]vz.DirectorySharingDeviceConfiguration{
			sharedDirConfig,
		})
	}

	// Validate configuration
	validated, err := vzConfig.Validate()
	if err != nil {
		return fmt.Errorf("failed to validate VM configuration: %w", err)
	}
	if !validated {
		return fmt.Errorf("VM configuration validation failed")
	}

	fmt.Println("Starting VM...")

	// Create and start VM
	vm, err := vz.NewVirtualMachine(vzConfig)
	if err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}

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
	if err := vm.Start(); err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	fmt.Println("VM started successfully!")
	fmt.Println("VM is running with NAT networking")
	fmt.Printf("VM console output: vm-console.log\n")

	// Wait for VM to report its IP address
	fmt.Println("Waiting for VM to report IP address...")
	vmIP, err := waitForVMIP("vm-console.log", 30*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		fmt.Fprintf(os.Stderr, "Falling back to default IP: %s\n", guestIPv4)
		vmIP = guestIPv4
	} else {
		fmt.Printf("VM IP address: %s\n", vmIP)
	}

	// Start SSH port forwarder with discovered IP
	ctx := context.Background()
	sshForwarder, err := StartLoopbackForwarder(ctx, hostSSHPort, vmIP, guestSSHPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to start SSH port forwarder: %v\n", err)
		fmt.Fprintf(os.Stderr, "SSH will not be accessible on port %d\n", hostSSHPort)
	} else {
		fmt.Printf("SSH forwarding: 127.0.0.1:%d -> %s:%d\n", hostSSHPort, vmIP, guestSSHPort)
		defer sshForwarder.Close()
	}

	// Write vm-state.json with runtime configuration
	vmState := map[string]string{
		"vm_name":   "exasol-nano-vm",
		"vm_ip":     vmIP,
		"cpu_count": cpuCountStr,
		"ram_size":  ramSizeStr,
		"pid":       fmt.Sprintf("%d", os.Getpid()),
		"ssh_port":  fmt.Sprintf("%d", hostSSHPort),
	}
	if sharedDir != "" {
		abs, _ := filepath.Abs(sharedDir)
		vmState["shared_dir"] = abs
	}

	stateData, err := json.MarshalIndent(vmState, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal vm-state: %w", err)
	}

	if err := os.WriteFile("vm-state.json", stateData, 0644); err != nil {
		return fmt.Errorf("failed to write vm-state.json: %w", err)
	}

	fmt.Println("VM state written to vm-state.json")
	fmt.Printf("\nSSH access: ssh -p %d exasol@127.0.0.1\n", hostSSHPort)

	// Wait for VM to finish (or be interrupted)
	for {
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
