package main

import (
	"archive/tar"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Code-Hex/vz/v3"
	"github.com/ulikunitz/xz"
)

//go:embed vm-package.tar.xz
var vmPackage []byte

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: mac-runner <command>")
		fmt.Println("Available commands: init, start, stop")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "init":
		initCmd()
	case "start":
		if len(os.Args) < 4 {
			fmt.Println("Usage: mac-runner start <cpu_count> <ram_size> [shared_dir]")
			os.Exit(1)
		}
		sharedDir := ""
		if len(os.Args) >= 5 {
			sharedDir = os.Args[4]
		}
		startCmd(os.Args[2], os.Args[3], sharedDir)
	case "__daemon__":
		// Internal daemon mode - run VM in background
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "Invalid daemon arguments\n")
			os.Exit(1)
		}
		sharedDir := ""
		if len(os.Args) >= 5 {
			sharedDir = os.Args[4]
		}
		runVMDaemon(os.Args[2], os.Args[3], sharedDir)
	case "stop":
		stopCmd()
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		fmt.Println("Available commands: init, start, stop")
		os.Exit(1)
	}
}

func initCmd() {
	fmt.Println("Initializing VM...")
	fmt.Println("Extracting VM package...")

	// Create vm directory
	vmDir := "vm"
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create vm directory: %v\n", err)
		os.Exit(1)
	}

	// Create a reader from the embedded data
	bytesReader := bytes.NewReader(vmPackage)

	// Create xz decompressor
	xzReader, err := xz.NewReader(bytesReader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create xz reader: %v\n", err)
		os.Exit(1)
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
			fmt.Fprintf(os.Stderr, "Failed to read tar header: %v\n", err)
			os.Exit(1)
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
				fmt.Fprintf(os.Stderr, "Failed to create directory %s: %v\n", outputPath, err)
				os.Exit(1)
			}
		case tar.TypeReg:
			// Ensure parent directory exists
			parentDir := filepath.Dir(outputPath)
			if err := os.MkdirAll(parentDir, 0755); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to create parent directory: %v\n", err)
				os.Exit(1)
			}

			outFile, err := os.Create(outputPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to create output file %s: %v\n", outputPath, err)
				os.Exit(1)
			}

			if _, err := io.Copy(outFile, tarReader); err != nil {
				outFile.Close()
				fmt.Fprintf(os.Stderr, "Failed to extract file %s: %v\n", outputPath, err)
				os.Exit(1)
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
}

func startCmd(cpuCountStr, ramSizeStr, sharedDir string) {
	if sharedDir != "" {
		fmt.Printf("Starting VM with cpu_count=%s, ram_size=%s, shared_dir=%s\n", cpuCountStr, ramSizeStr, sharedDir)
	} else {
		fmt.Printf("Starting VM with cpu_count=%s, ram_size=%s\n", cpuCountStr, ramSizeStr)
	}

	// Check if VM has been initialized
	vmDir := "vm"
	if _, err := os.Stat(vmDir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "VM not initialized. Run 'mac-runner init' first.\n")
		os.Exit(1)
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
						fmt.Fprintf(os.Stderr, "VM is already running (PID: %d)\n", pid)
						os.Exit(1)
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
		fmt.Fprintf(os.Stderr, "Failed to get executable path: %v\n", err)
		os.Exit(1)
	}

	// Create log file for daemon output
	logFile, err := os.OpenFile("vm.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create log file: %v\n", err)
		os.Exit(1)
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
			fmt.Fprintf(os.Stderr, "Failed to get absolute path for shared directory: %v\n", err)
			os.Exit(1)
		}
		args = append(args, abs)
	}

	process, err := os.StartProcess(executable, args, attr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start VM daemon: %v\n", err)
		os.Exit(1)
	}

	// Release the process so it can run independently
	process.Release()

	// Wait a moment for the daemon to write the PID file
	time.Sleep(500 * time.Millisecond)

	// Verify the VM started successfully
	if _, err := os.Stat(pidFile); err != nil {
		fmt.Fprintf(os.Stderr, "VM may have failed to start - check vm.log for details\n")
		os.Exit(1)
	}

	fmt.Println("VM started successfully in background")
	if sharedDir != "" {
		fmt.Printf("Shared folder: %s -> /mnt/host (inside VM)\n", sharedDir)
	}
	fmt.Println("Check vm.log for VM output")
	fmt.Println("Use 'mac-runner stop' to stop the VM")
}

func runVMDaemon(cpuCountStr, ramSizeStr, sharedDir string) {
	// This function runs as a background daemon
	
	cpuCount, err := strconv.Atoi(cpuCountStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid cpu_count: %v\n", err)
		os.Exit(1)
	}

	ramSize, err := strconv.ParseUint(ramSizeStr, 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid ram_size: %v\n", err)
		os.Exit(1)
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
		fmt.Fprintf(os.Stderr, "Failed to get absolute path for kernel: %v\n", err)
		os.Exit(1)
	}

	initramfsPath, err = filepath.Abs(initramfsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get absolute path for initramfs: %v\n", err)
		os.Exit(1)
	}

	diskPath, err = filepath.Abs(diskPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get absolute path for disk: %v\n", err)
		os.Exit(1)
	}

	cmdlinePath, err = filepath.Abs(cmdlinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get absolute path for cmdline: %v\n", err)
		os.Exit(1)
	}

	// Check if all files exist
	if _, err := os.Stat(kernelPath); err != nil {
		fmt.Fprintf(os.Stderr, "Kernel not found: %s\n", kernelPath)
		os.Exit(1)
	}
	if _, err := os.Stat(initramfsPath); err != nil {
		fmt.Fprintf(os.Stderr, "Initramfs not found: %s\n", initramfsPath)
		os.Exit(1)
	}
	if _, err := os.Stat(diskPath); err != nil {
		fmt.Fprintf(os.Stderr, "Disk image not found: %s\n", diskPath)
		os.Exit(1)
	}
	if _, err := os.Stat(cmdlinePath); err != nil {
		fmt.Fprintf(os.Stderr, "Kernel cmdline not found: %s\n", cmdlinePath)
		os.Exit(1)
	}

	// Read kernel command line
	cmdlineData, err := os.ReadFile(cmdlinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read kernel cmdline: %v\n", err)
		os.Exit(1)
	}
	cmdline := strings.TrimSpace(string(cmdlineData))

	fmt.Println("Creating VM configuration...")

	// Create bootloader (Linux direct boot)
	bootLoader, err := vz.NewLinuxBootLoader(kernelPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create Linux bootloader: %v\n", err)
		os.Exit(1)
	}

	// Set initramfs
	if err := bootLoader.SetInitialRamdiskPath(initramfsPath); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to set initramfs: %v\n", err)
		os.Exit(1)
	}

	// Set kernel command line
	if cmdline != "" {
		if err := bootLoader.SetCommandLine(cmdline); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to set kernel cmdline: %v\n", err)
			os.Exit(1)
		}
	}

	// Create disk attachment
	diskAttachment, err := vz.NewDiskImageStorageDeviceAttachment(diskPath, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create disk attachment: %v\n", err)
		os.Exit(1)
	}

	storageDeviceConfig, err := vz.NewVirtioBlockDeviceConfiguration(diskAttachment)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create storage device config: %v\n", err)
		os.Exit(1)
	}

	// Create network device
	natAttachment, err := vz.NewNATNetworkDeviceAttachment()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create NAT network attachment: %v\n", err)
		os.Exit(1)
	}

	networkConfig, err := vz.NewVirtioNetworkDeviceConfiguration(natAttachment)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create network device config: %v\n", err)
		os.Exit(1)
	}

	// Create entropy device (random number generator)
	entropyConfig, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create entropy device config: %v\n", err)
		os.Exit(1)
	}

	// Create VirtioFS shared directory device if provided
	var sharedDirConfig *vz.VirtioFileSystemDeviceConfiguration
	if sharedDir != "" {
		// Ensure shared directory exists
		if _, err := os.Stat(sharedDir); os.IsNotExist(err) {
			fmt.Printf("Creating shared directory: %s\n", sharedDir)
			if err := os.MkdirAll(sharedDir, 0755); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to create shared directory: %v\n", err)
				os.Exit(1)
			}
		}

		// Get absolute path
		absSharedDir, err := filepath.Abs(sharedDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to get absolute path for shared directory: %v\n", err)
			os.Exit(1)
		}

		// Create VirtioFS device with tag "hostshare" (matches cloud-init config)
		sharedDirDevice, err := vz.NewSharedDirectory(absSharedDir, false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create shared directory device: %v\n", err)
			os.Exit(1)
		}

		sharedDirConfig, err = vz.NewVirtioFileSystemDeviceConfiguration("hostshare")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create VirtioFS config: %v\n", err)
			os.Exit(1)
		}
		sharedDirConfig.SetDirectoryShare(sharedDirDevice)

		fmt.Printf("VirtioFS shared folder configured: %s -> /mnt/host\n", absSharedDir)
	}

	// Create VM configuration
	vzConfig := vz.NewVirtualMachineConfiguration(
		bootLoader,
		uint(cpuCount),
		ramSize*1024*1024, // Convert MB to bytes
	)

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
		fmt.Fprintf(os.Stderr, "Failed to validate VM configuration: %v\n", err)
		os.Exit(1)
	}
	if !validated {
		fmt.Fprintf(os.Stderr, "VM configuration validation failed\n")
		os.Exit(1)
	}

	fmt.Println("Starting VM...")

	// Create and start VM
	vm := vz.NewVirtualMachine(vzConfig)

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
		fmt.Fprintf(os.Stderr, "Failed to start VM: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("VM started successfully!")
	fmt.Println("VM is running with NAT networking")

	// For now, we don't have easy access to guest IP from VZ NAT
	vmIP := "NAT (check inside VM)"

	// Write vm-state.json with runtime configuration
	vmState := map[string]string{
		"vm_name":   "exasol-nano-vm",
		"vm_ip":     vmIP,
		"cpu_count": cpuCountStr,
		"ram_size":  ramSizeStr,
		"pid":       fmt.Sprintf("%d", os.Getpid()),
	}
	if sharedDir != "" {
		abs, _ := filepath.Abs(sharedDir)
		vmState["shared_dir"] = abs
	}

	stateData, err := json.MarshalIndent(vmState, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal vm-state: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile("vm-state.json", stateData, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write vm-state.json: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("VM state written to vm-state.json")

	// Wait for VM to finish (or be interrupted)
	ctx := context.Background()
	for {
		if vm.State() == vz.VirtualMachineStateStopped {
			fmt.Println("VM stopped")
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
			// Continue checking
		}
	}
}

func stopCmd() {
	fmt.Println("Stopping VM...")

	// Read PID file
	pidFile := "vm.pid"
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "VM is not running (no PID file found)\n")
		os.Exit(1)
	}

	pidStr := strings.TrimSpace(string(pidData))
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid PID in file: %v\n", err)
		os.Exit(1)
	}

	// Send SIGTERM to the VM process
	process, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to find VM process: %v\n", err)
		os.Exit(1)
	}

	if err := process.Signal(syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to send stop signal to VM: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Stop signal sent to VM")
	fmt.Println("VM should stop gracefully")
}
