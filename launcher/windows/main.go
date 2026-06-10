// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:generate sh -c 'cp "$DISK_IMG" disk.tar.xz'

package main

import (
	"archive/tar"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ulikunitz/xz"
)

//go:embed disk.tar.xz
var disk_img []byte

//go:embed start-vm.ps1
var startVmScript string

//go:embed create-shared-vhd.ps1
var createSharedVhdScript string

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: windows-runner <command>")
		fmt.Println("Available commands: init, start, stop")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "init":
		initCmd()
	case "start":
		if len(os.Args) < 4 {
			fmt.Println("Usage: windows-runner start <cpu_count> <ram_size> [shared_dir]")
			os.Exit(1)
		}
		sharedDir := ""
		if len(os.Args) >= 5 {
			sharedDir = os.Args[4]
		}
		startCmd(os.Args[2], os.Args[3], sharedDir)
	case "stop":
		stopCmd()
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		fmt.Println("Available commands: init, start, stop")
		os.Exit(1)
	}
}

// tailFile continuously reads from a file and writes new content to the writer
// Similar to "tail -f" on Linux
func tailFile(filePath string, writer io.Writer, done <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()

	var lastSize int64 = 0
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			// Read any remaining content before returning
			if file, err := os.Open(filePath); err == nil {
				file.Seek(lastSize, 0)
				io.Copy(writer, file)
				file.Close()
			}
			return
		case <-ticker.C:
			// Check if file exists and has new content
			if stat, err := os.Stat(filePath); err == nil {
				if stat.Size() > lastSize {
					if file, err := os.Open(filePath); err == nil {
						file.Seek(lastSize, 0)
						io.Copy(writer, file)
						lastSize = stat.Size()
						file.Close()
					}
				}
			}
		}
	}
}

func createSharedVhd(sharedDir, vhdPath string) error {
	absSharedDir, err := filepath.Abs(sharedDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for shared dir: %w", err)
	}
	absVhdPath, err := filepath.Abs(vhdPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for VHD: %w", err)
	}

	// Write create-shared-vhd.ps1 to temp file
	tempCreateVhd := filepath.Join(os.TempDir(), "create-shared-vhd.ps1")
	if err := os.WriteFile(tempCreateVhd, []byte(createSharedVhdScript), 0644); err != nil {
		return fmt.Errorf("failed to write create-shared-vhd.ps1: %w", err)
	}
	defer os.Remove(tempCreateVhd)

	// Create temp files for output/error capture
	outFile := filepath.Join(os.TempDir(), fmt.Sprintf("vhd-output-%d.txt", os.Getpid()))
	errFile := filepath.Join(os.TempDir(), fmt.Sprintf("vhd-error-%d.txt", os.Getpid()))
	defer os.Remove(outFile)
	defer os.Remove(errFile)

	// Create empty files for tailing
	os.WriteFile(outFile, []byte{}, 0644)
	os.WriteFile(errFile, []byte{}, 0644)

	// Start tailing output files in background
	var wg sync.WaitGroup
	done := make(chan struct{})

	wg.Add(2)
	go tailFile(outFile, os.Stdout, done, &wg)
	go tailFile(errFile, os.Stderr, done, &wg)

	// Execute PowerShell script with UAC elevation
	psCommand := fmt.Sprintf(`Start-Process -FilePath "powershell" -ArgumentList "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "%s", "-sharedDir", "%s", "-vhdPath", "%s" -Verb RunAs -Wait -RedirectStandardOutput "%s" -RedirectStandardError "%s"`,
		tempCreateVhd, absSharedDir, absVhdPath, outFile, errFile)

	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", psCommand)

	cmdErr := cmd.Run()

	// Signal tailers to stop and wait for them to finish
	close(done)
	wg.Wait()

	if cmdErr != nil {
		return fmt.Errorf("failed to create VHD (UAC elevation may have been denied): %w", cmdErr)
	}

	// Check if there were errors in the error file
	if data, err := os.ReadFile(errFile); err == nil && len(data) > 0 {
		return fmt.Errorf("VHD creation failed")
	}

	return nil
}

func initCmd() {
	fmt.Println("Initializing VM...")
	fmt.Println("Extracting disk image...")

	// Create a reader from the embedded data
	bytesReader := bytes.NewReader(disk_img)

	// Create xz decompressor
	xzReader, err := xz.NewReader(bytesReader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create xz reader: %v\n", err)
		os.Exit(1)
	}

	// Create tar reader
	tarReader := tar.NewReader(xzReader)

	// Extract the first file from the tar archive (the disk image)
	header, err := tarReader.Next()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read tar header: %v\n", err)
		os.Exit(1)
	}

	// Create output file in the working directory
	outputPath := header.Name
	outFile, err := os.Create(outputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create output file: %v\n", err)
		os.Exit(1)
	}
	defer outFile.Close()

	// Copy the disk image data to the output file
	written, err := io.Copy(outFile, tarReader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to extract disk image: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully extracted %s (%d bytes)\n", outputPath, written)

	// Create vm-config.json with disk image path
	config := map[string]string{
		"disk_img": outputPath,
	}

	configData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal config: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile("vm-config.json", configData, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write vm-config.json: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Successfully initialized VM")
	fmt.Println("Run 'windows-runner start <cpu_count> <ram_size> [shared_dir]' to start the VM")
}

func startCmd(cpuCount, ramSize, sharedDir string) {
	fmt.Printf("Starting VM with cpu_count=%s, ram_size=%s", cpuCount, ramSize)
	if sharedDir != "" {
		fmt.Printf(", shared_dir=%s\n", sharedDir)
	} else {
		fmt.Println()
	}

	// Read vm-config.json
	configData, err := os.ReadFile("vm-config.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read vm-config.json: %v\n", err)
		os.Exit(1)
	}

	var config map[string]string
	if err := json.Unmarshal(configData, &config); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse vm-config.json: %v\n", err)
		os.Exit(1)
	}

	// Get absolute path for disk image
	diskPath, err := filepath.Abs(config["disk_img"])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get absolute path for disk: %v\n", err)
		os.Exit(1)
	}

	// Handle shared directory and VHD if provided
	var sharedPath, vhdPath string
	if sharedDir != "" {
		// Create shared_dir if it doesn't exist
		if _, err := os.Stat(sharedDir); os.IsNotExist(err) {
			fmt.Printf("Creating shared directory: %s\n", sharedDir)
			if err := os.MkdirAll(sharedDir, 0755); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to create shared directory: %v\n", err)
				os.Exit(1)
			}
		}

		// Get absolute path for shared directory
		sharedPath, err = filepath.Abs(sharedDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to get absolute path for shared dir: %v\n", err)
			os.Exit(1)
		}

		// Create or recreate VHD
		vhdPath = "shared.vhdx"
		if _, err := os.Stat(vhdPath); os.IsNotExist(err) {
			fmt.Println("Creating VHD for shared directory...")
			if err := createSharedVhd(sharedDir, vhdPath); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
		} else {
			fmt.Println("Using existing shared VHD...")
		}

		// Get absolute path for VHD
		vhdPath, err = filepath.Abs(vhdPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to get absolute path for VHD: %v\n", err)
			os.Exit(1)
		}
	}

	vmName := "exasol-local-vm"

	// Write embedded PowerShell script to temp file
	tempScript := filepath.Join(os.TempDir(), "start-vm.ps1")
	if err := os.WriteFile(tempScript, []byte(startVmScript), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write PowerShell script: %v\n", err)
		os.Exit(1)
	}
	defer os.Remove(tempScript)

	// Create temp files for output/error capture
	outFile := filepath.Join(os.TempDir(), fmt.Sprintf("vm-output-%d.txt", os.Getpid()))
	errFile := filepath.Join(os.TempDir(), fmt.Sprintf("vm-error-%d.txt", os.Getpid()))
	defer os.Remove(outFile)
	defer os.Remove(errFile)

	// Create empty files for tailing
	os.WriteFile(outFile, []byte{}, 0644)
	os.WriteFile(errFile, []byte{}, 0644)

	// Start tailing output files in background
	var wg sync.WaitGroup
	done := make(chan struct{})

	wg.Add(2)
	go tailFile(outFile, os.Stdout, done, &wg)
	go tailFile(errFile, os.Stderr, done, &wg)

	// Execute PowerShell script with UAC elevation
	var psCommand string
	if vhdPath != "" {
		psCommand = fmt.Sprintf(`Start-Process -FilePath "powershell" -ArgumentList "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "%s", "-vmName", "%s", "-cpuCount", "%s", "-ramSize", "%s", "-diskPath", "%s", "-sharedPath", "%s", "-vhdPath", "%s" -Verb RunAs -Wait -RedirectStandardOutput "%s" -RedirectStandardError "%s"`,
			tempScript, vmName, cpuCount, ramSize, diskPath, sharedPath, vhdPath, outFile, errFile)
	} else {
		psCommand = fmt.Sprintf(`Start-Process -FilePath "powershell" -ArgumentList "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", "%s", "-vmName", "%s", "-cpuCount", "%s", "-ramSize", "%s", "-diskPath", "%s" -Verb RunAs -Wait -RedirectStandardOutput "%s" -RedirectStandardError "%s"`,
			tempScript, vmName, cpuCount, ramSize, diskPath, outFile, errFile)
	}

	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", psCommand)

	cmdErr := cmd.Run()

	// Signal tailers to stop and wait for them to finish
	close(done)
	wg.Wait()

	if cmdErr != nil {
		fmt.Fprintf(os.Stderr, "Failed to start VM (UAC elevation may have been denied): %v\n", cmdErr)
		os.Exit(1)
	}

	// Read output to parse IP address
	outputData, err := os.ReadFile(outFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read VM output: %v\n", err)
		os.Exit(1)
	}
	output := string(outputData)

	// Check for errors
	if errData, err := os.ReadFile(errFile); err == nil && len(errData) > 0 {
		fmt.Fprintf(os.Stderr, "Failed to start VM\n")
		os.Exit(1)
	}

	// Parse IP address from output
	vmIP := "unknown"
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "VM_IP:") {
			vmIP = strings.TrimSpace(strings.TrimPrefix(line, "VM_IP:"))
			break
		}
	}

	// Write vm-state.json with runtime configuration
	vmState := map[string]string{
		"vm_name":   vmName,
		"vm_ip":     vmIP,
		"cpu_count": cpuCount,
		"ram_size":  ramSize,
	}
	if sharedDir != "" {
		// Use relative path for shared directory
		vmState["shared_dir"] = "./" + filepath.Base(sharedDir)
		vmState["vhd_path"] = vhdPath
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

	fmt.Println("VM started successfully")
	if vmIP != "unknown" {
		fmt.Printf("VM IP address: %s\n", vmIP)
	}
	fmt.Println("VM state written to vm-state.json")
}

func stopCmd() {
	fmt.Println("Stopping VM...")

	vmName := "exasol-local-vm"

	// Create temp files for output/error capture
	outFile := filepath.Join(os.TempDir(), fmt.Sprintf("stop-output-%d.txt", os.Getpid()))
	errFile := filepath.Join(os.TempDir(), fmt.Sprintf("stop-error-%d.txt", os.Getpid()))
	defer os.Remove(outFile)
	defer os.Remove(errFile)

	// Create empty files for tailing
	os.WriteFile(outFile, []byte{}, 0644)
	os.WriteFile(errFile, []byte{}, 0644)

	// Start tailing output files in background
	var wg sync.WaitGroup
	done := make(chan struct{})

	wg.Add(2)
	go tailFile(outFile, os.Stdout, done, &wg)
	go tailFile(errFile, os.Stderr, done, &wg)

	// Execute PowerShell command to stop the VM with UAC elevation
	psCommand := fmt.Sprintf(`Start-Process -FilePath "powershell" -ArgumentList "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "Stop-VM -Name '%s' -Force" -Verb RunAs -Wait -RedirectStandardOutput "%s" -RedirectStandardError "%s"`,
		vmName, outFile, errFile)

	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", psCommand)

	cmdErr := cmd.Run()

	// Signal tailers to stop and wait for them to finish
	close(done)
	wg.Wait()

	if cmdErr != nil {
		fmt.Fprintf(os.Stderr, "Failed to stop VM (UAC elevation may have been denied): %v\n", cmdErr)
		os.Exit(1)
	}

	// Check for errors
	if data, err := os.ReadFile(errFile); err == nil && len(data) > 0 {
		fmt.Fprintf(os.Stderr, "Failed to stop VM\n")
		os.Exit(1)
	}

	fmt.Println("VM stopped successfully")
}
