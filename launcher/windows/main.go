// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

// Package main is the windows-runner launcher: a CLI that delegates
// container lifecycle to a natively installed podman-for-windows so the
// same on-disk contract and integration tests used by the mac launcher can
// be reused on windows.
//
// Phase 9 status: all five subcommands are implemented. --version-check-*
// flags are translated directly into the `podman run … init` argv
// (skipping the mac's intermediate vm-shared/version-check.json step),
// and data_size_gb is tracked via a resources/data-size.txt sidecar
// enforced by both start and resize-data.
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
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

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

	// versionCheckMinIntervalSeconds and companions mirror the clamping
	// applied by launcher/assets/init/init-db.sh so the effective values
	// the container sees are identical whether the mac guest script or
	// the windows launcher constructed the argv.
	versionCheckMinIntervalSeconds      = 60
	versionCheckMaxIntervalSeconds      = 604800
	versionCheckMaxRetryIntervalSeconds = 86400

	// versionCheckOperatingSystemWindows is the value the Nano container
	// records in exasol.conf when this launcher is the host. Matches the
	// versionCheckOperatingSystem("windows") branch in launcher/mac/main.go.
	versionCheckOperatingSystemWindows = "Windows"

	// dataSizePath is the sidecar file the windows launcher uses to fake
	// the mac raw-disk-image size contract. See the "Differences with the
	// mac version" section in windows-runner-plan.md.
	dataSizePath = "resources/data-size.txt"
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
	// with the mac launcher but ignored in Phase 7: podman-for-windows
	// sizes its backing VM globally, and Phase 9 wires data_size_gb into
	// the resources/data-size.txt sidecar. Silently accept for now.
	_ = cpuCountStr
	_ = ramSizeStr
	_ = dataSizeGB

	// --version-check-* flags are Phase 9. Silently accepted because our
	// defaults (Enabled=true, mac endpoint) match the mac launcher's
	// default behavior.
	_ = versionCheckOptions

	// Parse --ports first so syntax errors surface before the podman
	// prerequisite checks; users then get a clean diagnostic without
	// having to fix podman just to see the parse failure.
	overrides, err := parsePortOverrides(portsOverride)
	if err != nil {
		return err
	}

	configPath := filepath.Join(resourcesDir, "config.json")
	cfg, err := loadContainerConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load %s: %w", configPath, err)
	}

	// Every override must name a service the container actually exposes.
	// TestPortOverrideFailsForUnknownService requires the error text to
	// include the offending service name. Kept before the podman prereq
	// checks so a typo in --ports is surfaced without needing a working
	// podman-for-windows machine.
	if err := validatePortOverrides(overrides, cfg); err != nil {
		return err
	}

	// Enforce the grow-only data-size contract before touching podman.
	// current == 0 means the sidecar does not exist yet (first start in
	// this working directory), in which case any positive dataSizeGB is
	// acceptable and gets recorded below.
	current, err := readDataSize()
	if err != nil {
		return err
	}
	if current > 0 {
		if err := enforceDataSizeGrowOnly(current, dataSizeGB); err != nil {
			return err
		}
	}

	if err := podman.Available(); err != nil {
		return err
	}
	if err := podman.MachineRunning(); err != nil {
		return err
	}

	// Idempotency: if the container is already running, leave vm-state.json
	// alone (rewriting with newly-selected ports would clobber the real
	// bindings from the previous start).
	running, err := podman.ContainerRunning(cfg.ContainerName)
	if err != nil {
		return fmt.Errorf("failed to check container state: %w", err)
	}
	if running {
		fmt.Printf("Container %q is already running; leaving vm-state.json untouched.\n", cfg.ContainerName)
		return nil
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

	// Reserve host ports before invoking podman so we hard-fail on an
	// explicit --ports collision, and pick a random fallback if the
	// default is taken. There is an unavoidable TOCTOU window between
	// closing our probe listener and podman binding the port; matches
	// the mac launcher's behavior.
	hostPorts, err := selectAllHostPorts(cfg, overrides)
	if err != nil {
		return err
	}

	fmt.Println("Loading DB container image...")
	tarballPath := filepath.Join(resourcesDir, cfg.TarballName)
	imageRef, err := podman.LoadImage(tarballPath)
	if err != nil {
		return err
	}
	fmt.Printf("Loaded image: %s\n", imageRef)

	args := buildPodmanRunArgs(cfg, imageRef, hostPorts, versionCheckOptions)
	for _, svc := range sortedKeys(hostPorts) {
		fmt.Printf("Publishing %s: 127.0.0.1:%d -> container:%d\n", svc, hostPorts[svc], cfg.Ports[svc])
	}
	if err := podman.Run(args); err != nil {
		return err
	}

	// Persist the newly-agreed data size so subsequent starts and
	// resize-data invocations can enforce the grow-only rule.
	if current != dataSizeGB {
		if err := writeDataSize(dataSizeGB); err != nil {
			return err
		}
	}

	if err := writeVMState(hostPorts); err != nil {
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
// hostPorts maps each service name (`db`, and any future siblings) to
// the host-side port chosen by selectAllHostPorts. Container-side ports
// come from cfg.Ports. Services are emitted in alphabetical order so
// the argv is deterministic and test-assertable.
//
// versionCheck controls whether the container is launched with Nano's
// periodic version-check plumbing enabled and what parameters are
// baked into exasol.conf on first boot. See versionCheckPodmanArgs.
func buildPodmanRunArgs(cfg dbContainerConfig, imageRef string, hostPorts map[string]int, versionCheck VersionCheckOptions) []string {
	args := []string{
		"run", "-d",
		"--name", cfg.ContainerName,
		"--shm-size=" + cfg.ShmSize,
		"--pids-limit=" + cfg.PidsLimit,
		"--security-opt", cfg.SecurityOpt,
		"--restart", cfg.Restart,
	}
	for _, svc := range sortedKeys(cfg.Ports) {
		containerPort := cfg.Ports[svc]
		hostPort, ok := hostPorts[svc]
		if !ok {
			// Defensive: caller should always populate hostPorts for every
			// service in cfg.Ports. Fall through to the container port.
			hostPort = containerPort
		}
		args = append(args, "-p", fmt.Sprintf("%d:%d", hostPort, containerPort))
	}
	args = append(args, "-v", dataVolumeName+":/exa")

	// Version-check emits BOTH pre-image -e VERSION_CHECK_IDENTITY=...
	// (only when enabled) and post-init VERSION_CHECK_*= key=value args.
	// Matches the split in init-db.sh's run_db_container() where the
	// identity has to travel via env because Nano's init parser treats
	// ';' as a separator and personal-tier identities contain semicolons.
	preImage, initExtras := versionCheckPodmanArgs(versionCheck, versionCheckOperatingSystemWindows)
	args = append(args, preImage...)
	args = append(args, imageRef, "init")
	if len(cfg.Params) > 0 {
		// Nano's `init params='k=v ...'` interface: single argv token,
		// space-joined values. See init-db.sh's DB_PARAMS assignment.
		args = append(args, "params="+strings.Join(cfg.Params, " "))
	}
	args = append(args, initExtras...)
	return args
}

// versionCheckPodmanArgs translates VersionCheckOptions into the two
// argv fragments the guest-side init-db.sh would produce:
//
//   - preImage: `-e VERSION_CHECK_IDENTITY=<identity>` inserted right
//     before the container image ref. Empty when checks are disabled.
//   - initArgs: the trailing `VERSION_CHECK_*=<value>` tokens appended
//     after `init` (and after any `params=...`). Always contains at
//     least `VERSION_CHECK_ENABLED=<0|1>`; when enabled, adds
//     ENDPOINT, INTERVAL_SEC, RETRY_INTERVAL_SEC, and
//     OPERATING_SYSTEM.
//
// Defaulting and clamping match load_version_check_config() in
// init-db.sh: empty URL falls back to defaultVersionCheckURL, empty
// identity to defaultVersionCheckIdentity, non-positive interval to
// defaultVersionCheckIntervalSeconds; interval is clamped into
// [versionCheckMinIntervalSeconds, versionCheckMaxIntervalSeconds] and
// the retry interval into
// [versionCheckMinIntervalSeconds, versionCheckMaxRetryIntervalSeconds].
//
// osName is threaded in as a parameter (rather than reading runtime.GOOS
// directly) so tests can pin the value; production callers pass
// versionCheckOperatingSystemWindows.
func versionCheckPodmanArgs(opts VersionCheckOptions, osName string) (preImage []string, initArgs []string) {
	url := strings.TrimSpace(opts.URL)
	if url == "" {
		url = defaultVersionCheckURL
	}
	identity := strings.TrimSpace(opts.Identity)
	if identity == "" {
		identity = defaultVersionCheckIdentity
	}
	intervalSec := opts.IntervalSeconds
	if intervalSec <= 0 {
		intervalSec = defaultVersionCheckIntervalSeconds
	}

	// URL is always non-empty after defaulting, so "enabled iff flag AND
	// URL" collapses to the flag. Kept as an AND for symmetry with
	// init-db.sh's `if VERSION_CHECK_ENDPOINT is empty then disabled`.
	enabled := opts.Enabled && url != ""
	if !enabled {
		return nil, []string{"VERSION_CHECK_ENABLED=0"}
	}

	if intervalSec < versionCheckMinIntervalSeconds {
		intervalSec = versionCheckMinIntervalSeconds
	}
	if intervalSec > versionCheckMaxIntervalSeconds {
		intervalSec = versionCheckMaxIntervalSeconds
	}
	retryInterval := intervalSec
	if retryInterval > versionCheckMaxRetryIntervalSeconds {
		retryInterval = versionCheckMaxRetryIntervalSeconds
	}

	preImage = []string{"-e", "VERSION_CHECK_IDENTITY=" + identity}
	initArgs = []string{
		"VERSION_CHECK_ENABLED=1",
		"VERSION_CHECK_ENDPOINT=" + url,
		fmt.Sprintf("VERSION_CHECK_INTERVAL_SEC=%d", intervalSec),
		fmt.Sprintf("VERSION_CHECK_RETRY_INTERVAL_SEC=%d", retryInterval),
	}
	if osName != "" {
		initArgs = append(initArgs, "VERSION_CHECK_OPERATING_SYSTEM="+osName)
	}
	return preImage, initArgs
}

// parsePortOverrides parses a comma-separated list of "service:port"
// pairs into a map. Copied from launcher/mac/main.go so the two
// launchers accept identical --ports syntax; TestPortOverride* in
// tests/ports_test.go exercises this shape on both platforms.
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

// validatePortOverrides errors if any override refers to a service that
// is not present in cfg.Ports. Required to satisfy the mac test
// TestPortOverrideFailsForUnknownService which asserts the error text
// contains the offending service name so users can spot typos.
func validatePortOverrides(overrides map[string]int, cfg dbContainerConfig) error {
	if len(overrides) == 0 {
		return nil
	}
	known := sortedKeys(cfg.Ports)
	knownSet := make(map[string]struct{}, len(known))
	for _, k := range known {
		knownSet[k] = struct{}{}
	}
	for _, svc := range sortedKeys(overrides) {
		if _, ok := knownSet[svc]; !ok {
			return fmt.Errorf("unknown service %q in --ports override; known services: %v", svc, known)
		}
	}
	return nil
}

// selectHostPort chooses the host-side port for a single named service.
//
// Behavior matches the mac launcher's port-selection semantics:
//   - If overrides[serviceName] is set, we probe-bind that exact port
//     and hard-fail on collision. The error text mentions the service
//     name and that --ports supplied the value.
//   - Otherwise we probe-bind containerPort. If that fails (port in
//     use), we fall back to a random OS-assigned port via net.Listen
//     on ":0". If even that fails (very unusual — no free port on the
//     loopback), we return a wrapped error.
//
// The probe listener is closed before the function returns so podman
// can bind the port. There is an unavoidable TOCTOU race window
// between our close and podman's bind; the mac path has the same race
// and it has not been observed to matter in practice.
func selectHostPort(serviceName string, containerPort int, overrides map[string]int) (int, error) {
	if requested, ok := overrides[serviceName]; ok {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", requested))
		if err != nil {
			return 0, fmt.Errorf(
				"cannot bind host port %d for service %q (requested via --ports): %w",
				requested, serviceName, err,
			)
		}
		_ = ln.Close()
		return requested, nil
	}
	// Default: try the container port first.
	if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", containerPort)); err == nil {
		_ = ln.Close()
		return containerPort, nil
	}
	// Fall back to an OS-assigned free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("failed to allocate a fallback host port for service %q: %w", serviceName, err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

// selectAllHostPorts computes the host-side port for every service in
// cfg.Ports. Aborts on the first selection failure so an explicit
// --ports collision is not masked by the remaining services being
// processed.
func selectAllHostPorts(cfg dbContainerConfig, overrides map[string]int) (map[string]int, error) {
	chosen := make(map[string]int, len(cfg.Ports))
	for _, svc := range sortedKeys(cfg.Ports) {
		hostPort, err := selectHostPort(svc, cfg.Ports[svc], overrides)
		if err != nil {
			return nil, err
		}
		chosen[svc] = hostPort
	}
	return chosen, nil
}

// sortedKeys returns the map's string keys in ascending lexical order.
// Used everywhere we need deterministic iteration for testability.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
	// Container name lives in resources/config.json. If init has never run
	// in this working directory, there is nothing this launcher could have
	// started — report success and only clean up vm-state.json if present.
	configPath := filepath.Join(resourcesDir, "config.json")
	cfg, err := loadContainerConfig(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("No resources/config.json; nothing to stop.")
			return removeVMState()
		}
		return fmt.Errorf("failed to load %s: %w", configPath, err)
	}

	if err := podman.Available(); err != nil {
		return err
	}

	// podman.Stop uses --ignore, so an absent or already-stopped container
	// is a no-op with a clean exit. 30s matches the mac graceful window.
	fmt.Printf("Stopping container %q...\n", cfg.ContainerName)
	if err := podman.Stop(cfg.ContainerName, 30*time.Second); err != nil {
		return err
	}
	// podman.Rm also uses --ignore, so removing a nonexistent container is
	// a no-op. Removal keeps a subsequent start able to recreate the
	// container with a fresh podman-run argv (e.g. updated port bindings).
	// The named data volume is preserved so restart persistence survives.
	if err := podman.Rm(cfg.ContainerName); err != nil {
		return err
	}

	if err := removeVMState(); err != nil {
		return err
	}
	fmt.Println("Stopped.")
	return nil
}

// removeVMState deletes vm-state.json if present. A missing file is not an
// error — stopCmd must be idempotent.
func removeVMState() error {
	if err := os.Remove(vmStatePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove %s: %w", vmStatePath, err)
	}
	return nil
}

func statusCmd() error {
	return statusCmdTo(os.Stdout)
}

// statusCmdTo writes {"running":bool} to w. Always exits 0 (returns nil)
// unless w itself is broken — the shared integration tests rely on
// `status` never erroring so they can poll it during teardown and
// forceful-kill scenarios (TestStatusAfterForcefulKill).
//
// Any errors reaching podman are absorbed and treated as "not running":
// if we cannot inspect the container, we conservatively report absence
// rather than propagating an ambiguous signal to the test harness.
func statusCmdTo(w io.Writer) error {
	running := checkContainerRunning()
	out, err := json.Marshal(map[string]bool{"running": running})
	if err != nil {
		// json.Marshal of a bool map cannot realistically fail; treat as
		// a fatal internal error.
		return fmt.Errorf("failed to marshal status: %w", err)
	}
	_, err = fmt.Fprintln(w, string(out))
	return err
}

// checkContainerRunning returns whether the DB container is currently
// running, absorbing every error mode (missing config, missing podman,
// podman inspect failure) as "not running". Split out so statusCmdTo
// stays focused on I/O.
func checkContainerRunning() bool {
	cfg, err := loadContainerConfig(filepath.Join(resourcesDir, "config.json"))
	if err != nil {
		return false
	}
	if err := podman.Available(); err != nil {
		return false
	}
	running, err := podman.ContainerRunning(cfg.ContainerName)
	if err != nil {
		return false
	}
	return running
}

func resizeDataDiskCmd(sizeArg string) error {
	newSizeGB, err := strconv.Atoi(sizeArg)
	if err != nil {
		return fmt.Errorf("invalid size: %w", err)
	}
	if newSizeGB <= 0 {
		return fmt.Errorf("new size (%dGB) must be a positive integer", newSizeGB)
	}

	configPath := filepath.Join(resourcesDir, "config.json")
	cfg, err := loadContainerConfig(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("launcher not initialized: run 'windows-runner init' first")
		}
		return fmt.Errorf("failed to load %s: %w", configPath, err)
	}

	// Container must be stopped. If podman is unavailable, treat the
	// state as "not running" (nothing we could ask, so we cannot claim
	// it is running). Only a positive "container is running" answer
	// blocks the resize — matches the plan text ("call
	// podman.ContainerRunning and error if true").
	if err := podman.Available(); err == nil {
		running, err := podman.ContainerRunning(cfg.ContainerName)
		if err == nil && running {
			return fmt.Errorf("container %q is currently running. Stop it first with 'windows-runner stop'", cfg.ContainerName)
		}
	}

	current, err := readDataSize()
	if err != nil {
		return err
	}
	if current == 0 {
		return fmt.Errorf("data size has not been recorded yet: run 'windows-runner start <cpu> <ram> <size>' first")
	}
	if newSizeGB <= current {
		return fmt.Errorf("new size (%dGB) must be larger than current size (%dGB). Shrinking is not supported", newSizeGB, current)
	}

	if err := writeDataSize(newSizeGB); err != nil {
		return err
	}
	fmt.Printf("Data size updated: %dGB -> %dGB\n", current, newSizeGB)
	return nil
}

// readDataSize returns the size in GB recorded in resources/data-size.txt,
// or (0, nil) if the file does not exist yet (first-start case). Any
// parse or read error other than "not found" is surfaced.
func readDataSize() (int, error) {
	data, err := os.ReadFile(dataSizePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to read %s: %w", dataSizePath, err)
	}
	sizeStr := strings.TrimSpace(string(data))
	size, err := strconv.Atoi(sizeStr)
	if err != nil {
		return 0, fmt.Errorf("invalid size in %s: %q", dataSizePath, sizeStr)
	}
	if size <= 0 {
		return 0, fmt.Errorf("invalid size in %s: %d (must be positive)", dataSizePath, size)
	}
	return size, nil
}

// writeDataSize replaces resources/data-size.txt with the given size
// in GB. resources/ is created by init and expected to exist; if it
// does not, that surfaces here as an error.
func writeDataSize(sizeGB int) error {
	if err := os.MkdirAll(resourcesDir, 0o755); err != nil {
		return fmt.Errorf("failed to create %s: %w", resourcesDir, err)
	}
	if err := os.WriteFile(dataSizePath, fmt.Appendf(nil, "%d\n", sizeGB), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", dataSizePath, err)
	}
	return nil
}

// enforceDataSizeGrowOnly rejects a requested size that is strictly
// smaller than what was previously recorded. Message intentionally
// contains the string "shrink" so tests/data_persistence_test.go
// TestDataDiskShrinkRejected (which does a case-insensitive substring
// match on "shrink") passes unchanged.
func enforceDataSizeGrowOnly(currentGB, requestedGB int) error {
	if requestedGB < currentGB {
		return fmt.Errorf("existing data size is %dGB, larger than requested %dGB; shrinking data volumes is not supported", currentGB, requestedGB)
	}
	return nil
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
