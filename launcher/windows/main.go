// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

// Package main is the windows-runner launcher: a CLI that delegates
// container lifecycle to a natively installed podman-for-windows so the
// same on-disk contract and integration tests used by the mac launcher can
// be reused on windows.
//
// Phase 6 status: init and start (core path) are implemented. stop /
// status / resize-data remain stubs. --ports overrides and
// --version-check-* flags are not yet propagated to podman — later
// phases wire them in.
package main

import (
	"archive/tar"
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ulikunitz/xz"

	"windows-runner/internal/podman"
)

// initAssets is the embedded launcher/assets/windows/init/ directory,
// packaged by host/package/build-windows-launcher.sh into
// launcher/windows/init-assets.tar.xz. It is extracted into ./resources/
// by initCmd.
//
//go:embed init-assets.tar.xz
var initAssets []byte

// InitOutput is the JSON shape written by the guest-side init scripts on
// mac. It is mirrored here so windows can produce compatible state files
// once start is implemented; kept small and pure-data so the skeleton has
// no external dependencies.
type InitOutput struct {
	IP    string         `json:"ip"`
	Ports map[string]int `json:"ports"`
}

// RuntimeConfig matches the mac vm-config.json schema; ssh_private_key is
// left empty on windows because there is no guest VM to SSH into.
type RuntimeConfig struct {
	SSHPrivateKey string `json:"ssh_private_key"`
}

// VersionCheckRuntimeConfig mirrors the mac schema so any shared tooling
// that reads the file finds the same shape. Windows may or may not emit
// this file — that is decided in a later phase.
type VersionCheckRuntimeConfig struct {
	Enabled         bool   `json:"enabled"`
	IntervalSeconds int    `json:"interval_seconds"`
	Identity        string `json:"identity"`
	URL             string `json:"url"`
	OperatingSystem string `json:"operating_system"`
}

// VersionCheckOptions carries the parsed --version-check-* flag values.
type VersionCheckOptions struct {
	Enabled         bool
	IntervalSeconds int
	Identity        string
	URL             string
}

const (
	defaultVersionCheckIntervalSeconds = 86400
	defaultVersionCheckIdentity        = "NONE"

	resourcesDir      = "resources"
	runtimeConfigPath = "vm-config.json"
	vmStatePath       = "vm-state.json"

	// dataVolumeName is the podman named volume that persists /exa
	// across container restarts. Matches the mac guest-side
	// -v "$EXA_DATA_DIR:/exa" bind mount in launcher/assets/init/init-db.sh.
	dataVolumeName = "exasol-nano-data"
)

var defaultVersionCheckURL = "https://metrics-test.exasol.com/v1/version-check"

func defaultVersionCheckOptions() VersionCheckOptions {
	return VersionCheckOptions{
		Enabled:         true,
		IntervalSeconds: defaultVersionCheckIntervalSeconds,
		Identity:        defaultVersionCheckIdentity,
		URL:             defaultVersionCheckURL,
	}
}

// errNotImplemented is returned by every subcommand until later phases
// wire in real behavior. Kept as a sentinel so tests can assert on it.
func errNotImplemented(subcommand string) error {
	return fmt.Errorf("%s: not implemented on windows yet", subcommand)
}

// exitError carries an explicit process exit code out of a subcommand.
// main() type-asserts on it to distinguish flag-style errors (exit 2)
// from ordinary runtime errors (exit 1).
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func newExitError(code int, format string, args ...any) *exitError {
	return &exitError{code: code, msg: fmt.Sprintf(format, args...)}
}

// writeRuntimeConfig serialises RuntimeConfig to runtimeConfigPath in cwd
// as pretty-printed JSON. Mirrors the mac vm-config.json shape.
func writeRuntimeConfig(config RuntimeConfig) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal runtime config: %w", err)
	}
	if err := os.WriteFile(runtimeConfigPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write runtime config: %w", err)
	}
	return nil
}

// extractTarXZ extracts a tar.xz archive to the specified output directory.
// pathTransform is an optional function to transform archive paths before
// extracting; returning "" from pathTransform skips the entry. Copied from
// launcher/mac/main.go per Phase 4 plan ("recommend copying it verbatim").
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

		outputPath := header.Name
		if pathTransform != nil {
			outputPath = pathTransform(header.Name)
			if outputPath == "" {
				continue
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
		}
	}
	return nil
}

// initCmd is the public entry point invoked by main(). It uses the
// //go:embed'd initAssets blob so production builds always have the
// canonical assets available.
func initCmd(sshKeyPath string) error {
	return initCmdWithAssets(sshKeyPath, initAssets)
}

// initCmdWithAssets is the implementation split out so unit tests can
// supply their own tarball bytes without touching the embedded blob.
//
// The tarball is expected to root at init/ (matching the layout produced
// by build-windows-launcher.sh: 'tar -C launcher/assets/windows -cf - init').
// The init/ prefix is stripped during extraction, so init/config.json ends
// up at resources/config.json.
func initCmdWithAssets(sshKeyPath string, assetsData []byte) error {
	if sshKeyPath != "" {
		return newExitError(2, "--ssh-key is not supported on windows: there is no guest VM to SSH into")
	}

	fmt.Println("Initializing windows launcher...")

	if err := os.MkdirAll(resourcesDir, 0755); err != nil {
		return fmt.Errorf("failed to create resources directory: %w", err)
	}

	fmt.Println("Extracting init assets...")
	if err := extractTarXZ(assetsData, resourcesDir, func(path string) string {
		parts := strings.SplitN(path, "/", 2)
		if len(parts) < 2 {
			// Skip the top-level directory entry itself (e.g. "init/").
			return ""
		}
		return parts[1]
	}); err != nil {
		return fmt.Errorf("failed to extract init assets: %w", err)
	}

	// Windows has no guest VM to SSH into, so the ssh_private_key field is
	// intentionally left empty. The file still exists so 'stop' and 'status'
	// can share the same on-disk contract as the mac launcher.
	if err := writeRuntimeConfig(RuntimeConfig{}); err != nil {
		return err
	}
	fmt.Printf("Runtime config written to: %s\n", runtimeConfigPath)

	fmt.Printf("Resources extracted to: %s/\n", resourcesDir)
	fmt.Println("Initialized. Run 'windows-runner start <cpu> <ram_mb> <data_size_gb>' to start.")
	return nil
}

func startCmd(
	cpuCountStr string,
	ramSizeStr string,
	dataSizeGB int,
	portsOverride string,
	versionCheckOptions VersionCheckOptions,
) error {
	// Positional args (cpu, ram, data_size) are accepted for CLI parity
	// with the mac launcher but ignored in Phase 6: podman-for-windows
	// sizes its backing VM globally, and Phase 9 wires data_size_gb into
	// the resources/data-size.txt sidecar. Silently accept for now.
	_ = cpuCountStr
	_ = ramSizeStr
	_ = dataSizeGB

	// --ports overrides and --version-check-* flags are Phase 7 / Phase 9
	// respectively. Reject an explicit --ports value so users are not
	// misled into thinking overrides took effect; --version-check-* is
	// benign to ignore because our defaults match mac defaults.
	if portsOverride != "" {
		return fmt.Errorf("--ports override is not yet implemented on windows (Phase 7)")
	}
	_ = versionCheckOptions

	if err := podman.Available(); err != nil {
		return err
	}
	if err := podman.MachineRunning(); err != nil {
		return err
	}

	configPath := filepath.Join(resourcesDir, "config.json")
	cfg, err := loadContainerConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load %s: %w", configPath, err)
	}
	dbPort, ok := cfg.Ports["db"]
	if !ok || dbPort <= 0 {
		return fmt.Errorf("invalid config %s: db.ports.db missing or not positive", configPath)
	}

	// Idempotency: if the container is already running, refresh vm-state.json
	// and return without touching the podman state.
	running, err := podman.ContainerRunning(cfg.ContainerName)
	if err != nil {
		return fmt.Errorf("failed to check container state: %w", err)
	}
	if running {
		fmt.Printf("Container %q is already running; nothing to do.\n", cfg.ContainerName)
		return writeVMState(map[string]int{"db": dbPort})
	}

	// A stopped container with the same name would make `podman run` fail
	// with a name-collision error; remove it. The data volume is
	// preserved by name so DB state survives.
	exists, err := podman.ContainerExists(cfg.ContainerName)
	if err != nil {
		return err
	}
	if exists {
		fmt.Printf("Removing stale stopped container %q...\n", cfg.ContainerName)
		if err := podman.Rm(cfg.ContainerName); err != nil {
			return fmt.Errorf("failed to remove stale container: %w", err)
		}
	}

	fmt.Println("Loading DB container image...")
	tarballPath := filepath.Join(resourcesDir, cfg.TarballName)
	imageRef, err := podman.LoadImage(tarballPath)
	if err != nil {
		return err
	}
	fmt.Printf("Loaded image: %s\n", imageRef)

	args := buildPodmanRunArgs(cfg, imageRef, dbPort)
	fmt.Printf("Starting container %q with db port %d...\n", cfg.ContainerName, dbPort)
	if err := podman.Run(args); err != nil {
		return err
	}

	if err := writeVMState(map[string]int{"db": dbPort}); err != nil {
		return err
	}
	fmt.Printf("Started. VM state written to %s\n", vmStatePath)
	return nil
}

// dbContainerConfig is the subset of resources/config.json (the `.db`
// object) that windows-runner needs to build a `podman run` argv.
// Mirrors the schema the mac guest-side init-db.sh consumes.
type dbContainerConfig struct {
	ContainerName string         `json:"container_name"`
	TarballName   string         `json:"tarball_name"`
	Ports         map[string]int `json:"ports"`
	ShmSize       string         `json:"shm_size"`
	PidsLimit     string         `json:"pids_limit"`
	SecurityOpt   string         `json:"security_opt"`
	Restart       string         `json:"restart"`
	Params        []string       `json:"params"`
}

// launcherConfig is the top-level shape of resources/config.json.
type launcherConfig struct {
	DB dbContainerConfig `json:"db"`
}

// loadContainerConfig reads resources/config.json and returns its .db
// object. Fails fast on missing/invalid JSON with a wrapped error.
func loadContainerConfig(path string) (dbContainerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return dbContainerConfig{}, fmt.Errorf("read config: %w", err)
	}
	var lc launcherConfig
	if err := json.Unmarshal(data, &lc); err != nil {
		return dbContainerConfig{}, fmt.Errorf("parse config: %w", err)
	}
	return lc.DB, nil
}

// buildPodmanRunArgs constructs the `podman run` argv for the DB
// container. The shape mirrors the guest-side
// launcher/assets/init/init-db.sh run_db_container function (see
// init-db.sh#L396) so future divergence between the two paths is
// intentional and reviewable.
//
// Phase 6 scope: no --ports overrides, no --version-check-* args, no
// per-run params gating on "fresh deployment". Later phases extend this
// function; keep the signature and return-slice ordering stable so
// tests stay tight.
func buildPodmanRunArgs(cfg dbContainerConfig, imageRef string, hostDBPort int) []string {
	containerDBPort := cfg.Ports["db"]
	args := []string{
		"run", "-d",
		"--name", cfg.ContainerName,
		"--shm-size=" + cfg.ShmSize,
		"--pids-limit=" + cfg.PidsLimit,
		"--security-opt", cfg.SecurityOpt,
		"--restart", cfg.Restart,
		"-p", fmt.Sprintf("%d:%d", hostDBPort, containerDBPort),
		"-v", dataVolumeName + ":/exa",
		imageRef,
		"init",
	}
	if len(cfg.Params) > 0 {
		// Nano's `init params='k=v ...'` interface: single argv token,
		// space-joined values. See init-db.sh's DB_PARAMS assignment.
		args = append(args, "params="+strings.Join(cfg.Params, " "))
	}
	return args
}

// VMState is the on-disk shape of vm-state.json, the file the shared
// integration tests parse to discover which host port the DB is bound
// to. Only `ports` is populated on windows; the mac launcher also
// includes vm_ip, cpu_count, ram_size, pid, shared_dir, and
// ssh_private_key.
type VMState struct {
	Ports map[string]int `json:"ports"`
}

// writeVMState serialises VMState to vmStatePath as pretty-printed
// JSON. Overwrites any previous file.
func writeVMState(chosenPorts map[string]int) error {
	state := VMState{Ports: chosenPorts}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal vm-state: %w", err)
	}
	if err := os.WriteFile(vmStatePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write vm-state: %w", err)
	}
	return nil
}

func stopCmd() error {
	return errNotImplemented("stop")
}

func statusCmd() error {
	return errNotImplemented("status")
}

func resizeDataDiskCmd(sizeArg string) error {
	_ = sizeArg
	return errNotImplemented("resize-data")
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: windows-runner <command> [options]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Commands:")
		fmt.Fprintln(os.Stderr, "  init [--ssh-key <private-key>]    Initialize launcher working directory")
		fmt.Fprintln(os.Stderr, "  start [--ports <svc>:<port>,...] <cpu> <ram> <data_size_gb>")
		fmt.Fprintln(os.Stderr, "                                    Start the DB container via podman-for-windows.")
		fmt.Fprintln(os.Stderr, "                                    CPU count, RAM size (MB), and data disk size in")
		fmt.Fprintln(os.Stderr, "                                    GB are accepted for CLI parity with the mac")
		fmt.Fprintln(os.Stderr, "                                    launcher; podman sizes its own backing VM.")
		fmt.Fprintln(os.Stderr, "                                    --ports overrides which host port is bound")
		fmt.Fprintln(os.Stderr, "                                    for a named service (e.g. --ports db:9090).")
		fmt.Fprintln(os.Stderr, "                                    Unspecified services use the same port as the")
		fmt.Fprintln(os.Stderr, "                                    container, falling back to a random port if")
		fmt.Fprintln(os.Stderr, "                                    unavailable.")
		fmt.Fprintln(os.Stderr, "  stop                              Stop the running DB container")
		fmt.Fprintln(os.Stderr, "  status                            Print JSON {\"running\": bool}")
		fmt.Fprintln(os.Stderr, "  resize-data <size>                Record a new data size in GB (container must be stopped)")
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "init":
		initFlags := flag.NewFlagSet("init", flag.ContinueOnError)
		initFlags.SetOutput(os.Stderr)
		sshKeyPath := initFlags.String("ssh-key", "", "Use an existing SSH private key instead of generating one")
		initFlags.Usage = func() {
			fmt.Fprintln(os.Stderr, "Usage: windows-runner init [--ssh-key <private-key>]")
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
			fmt.Fprintln(os.Stderr, "Usage: windows-runner start [--ports <service>:<port>,...] <cpu_count> <ram_size> <data_size_gb>")
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
	case "stop":
		err = stopCmd()
	case "status":
		err = statusCmd()
	case "resize-data":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: windows-runner resize-data <new_size_gb>")
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
		var ee *exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		os.Exit(1)
	}
}
