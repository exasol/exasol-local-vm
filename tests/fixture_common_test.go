// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin || windows

// Package integration contains end-to-end tests for the launcher binaries.
// Tests require the notarized artifact zip produced by the build-packages CI
// workflow. By default the fixture looks for the current platform's zip under
// ../dist/; override with the LAUNCHER_ZIP env var. Platform-specific bits
// (default zip path, binary name, SSH/data-disk helpers) live in
// fixture_darwin_test.go and fixture_windows_test.go.
package integration

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// LauncherFixture manages a temporary working directory with the launcher binary.
// Call NewLauncherFixture to create one; call Cleanup when the test is done.
type LauncherFixture struct {
	// WorkDir is the temporary directory in which the launcher is run.
	WorkDir string
	// BinaryPath is the path to the launcher binary inside WorkDir.
	BinaryPath string

	vmRunning bool
	t         *testing.T
}

// vmState is the on-disk shape of vm-state.json (the subset the tests care
// about). Only .ports is guaranteed populated on all platforms; on mac it
// also carries an "ssh" key which is absent on windows.
type vmState struct {
	Ports map[string]int `json:"ports"`
}

// NewLauncherFixture locates the platform-appropriate launcher zip
// (launcherZipDefault + LAUNCHER_ZIP env var), unzips it into a fresh
// temporary directory, and returns a ready-to-use fixture. If the zip
// cannot be found the test is skipped.
func NewLauncherFixture(t *testing.T) *LauncherFixture {
	t.Helper()

	if err := os.MkdirAll("failures", 0755); err != nil {
		t.Logf("warning: could not create failures dir: %v", err)
	}

	zipPath := findLauncherZip(t)

	workDir, err := os.MkdirTemp("", "launcher-test-*")
	if err != nil {
		t.Fatalf("failed to create temp work dir: %v", err)
	}

	if err := unzipTo(zipPath, workDir); err != nil {
		os.RemoveAll(workDir)
		t.Fatalf("failed to unzip launcher artifact %s: %v", zipPath, err)
	}

	binaryPath := filepath.Join(workDir, launcherBinaryName)
	// os.Chmod on windows only affects the read-only attribute (which we
	// do not need to touch on a fresh extract), but the call is harmless.
	if err := os.Chmod(binaryPath, 0755); err != nil {
		os.RemoveAll(workDir)
		t.Fatalf("failed to chmod launcher binary %s: %v", binaryPath, err)
	}

	return &LauncherFixture{
		WorkDir:    workDir,
		BinaryPath: binaryPath,
		t:          t,
	}
}

// Init runs `launcher init` in WorkDir.
func (f *LauncherFixture) Init() {
	f.t.Helper()
	f.run("init")
}

// StartVM runs `launcher start <cpu> <ramMB> <dataSizeGB>` and waits for it to
// return. Marks the launcher as running so Cleanup will call Stop.
func (f *LauncherFixture) StartVM(cpu, ramMB, dataSizeGB int) {
	f.t.Helper()
	f.run("start",
		fmt.Sprintf("%d", cpu),
		fmt.Sprintf("%d", ramMB),
		fmt.Sprintf("%d", dataSizeGB),
	)
	f.vmRunning = true
}

// StartVMWithPorts is like StartVM but also passes --ports to override which
// host port is bound for each named service (e.g. "db:9090,ssh:2222").
func (f *LauncherFixture) StartVMWithPorts(cpu, ramMB, dataSizeGB int, ports string) {
	f.t.Helper()
	f.run("start",
		"--ports", ports,
		fmt.Sprintf("%d", cpu),
		fmt.Sprintf("%d", ramMB),
		fmt.Sprintf("%d", dataSizeGB),
	)
	f.vmRunning = true
}

// StartVMExpectError runs `launcher start` with the given extra flags/args and
// returns any error rather than fataling, so callers can assert on failure cases.
// The launcher is not marked as running regardless of outcome.
func (f *LauncherFixture) StartVMExpectError(cpu, ramMB, dataSizeGB int, extraArgs ...string) error {
	f.t.Helper()
	args := append([]string{"start"}, extraArgs...)
	args = append(args, fmt.Sprintf("%d", cpu), fmt.Sprintf("%d", ramMB), fmt.Sprintf("%d", dataSizeGB))
	cmd := exec.Command(f.BinaryPath, args...)
	cmd.Dir = f.WorkDir
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		f.t.Logf("start output:\n%s", strings.TrimSpace(string(out)))
	}
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// StopVM runs `launcher stop`.
func (f *LauncherFixture) StopVM() {
	f.t.Helper()
	f.run("stop")
	f.vmRunning = false
}

// Status runs `launcher status` and returns whether the launcher reports the
// container as running.
func (f *LauncherFixture) Status() bool {
	f.t.Helper()
	cmd := exec.Command(f.BinaryPath, "status")
	cmd.Dir = f.WorkDir
	out, err := cmd.Output()
	if err != nil {
		f.t.Fatalf("launcher status: %v", err)
	}
	var result struct {
		Running bool `json:"running"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		f.t.Fatalf("failed to parse status output %q: %v", strings.TrimSpace(string(out)), err)
	}
	return result.Running
}

// VMState reads and parses vm-state.json from WorkDir.
func (f *LauncherFixture) VMState() vmState {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.WorkDir, "vm-state.json"))
	if err != nil {
		f.t.Fatalf("failed to read vm-state.json: %v", err)
	}
	var state vmState
	if err := json.Unmarshal(data, &state); err != nil {
		f.t.Fatalf("failed to parse vm-state.json: %v", err)
	}
	return state
}

// ResizeData runs `launcher resize-data <newSizeGB>` and returns any error.
// The launcher's stderr is included in the returned error so callers can
// inspect the human-readable rejection message (e.g. "shrinking is not supported").
func (f *LauncherFixture) ResizeData(newSizeGB int) error {
	f.t.Helper()
	cmd := exec.Command(f.BinaryPath, "resize-data", fmt.Sprintf("%d", newSizeGB))
	cmd.Dir = f.WorkDir
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		f.t.Logf("resize-data output:\n%s", strings.TrimSpace(string(out)))
	}
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CopyLogsToFailuresDir copies launcher log files and any host-shared
// directory contents from WorkDir into failures/<testName>/ so they survive
// fixture cleanup and can be inspected after a test failure. Files that do
// not exist on this platform are silently skipped, so the same helper works
// unchanged on mac (vm.log, vm-console.log, vm-shared/) and windows
// (vm-state.json only).
func (f *LauncherFixture) CopyLogsToFailuresDir(testName string) {
	f.t.Helper()
	dest := filepath.Join("failures", testName)
	if err := os.MkdirAll(dest, 0755); err != nil {
		f.t.Logf("could not create failures dir %s: %v", dest, err)
		return
	}
	for _, name := range []string{"vm.log", "vm-console.log", "vm-state.json"} {
		f.copyFileToDir(filepath.Join(f.WorkDir, name), filepath.Join(dest, name))
	}

	// vm-shared is the mac VirtioFS folder shared with the guest. Skipped
	// entirely on windows because the directory does not exist.
	sharedDir := filepath.Join(f.WorkDir, "vm-shared")
	sharedDest := filepath.Join(dest, "vm-shared")
	entries, err := os.ReadDir(sharedDir)
	if err != nil {
		if !os.IsNotExist(err) {
			f.t.Logf("could not read shared dir %s for failures dir: %v", sharedDir, err)
		}
		return
	}
	for _, entry := range entries {
		if entry.Name() == "init" {
			continue
		}
		f.copyPathToDir(filepath.Join(sharedDir, entry.Name()), filepath.Join(sharedDest, entry.Name()))
	}
}

// copyFileToDir copies a single file, logging (not fataling) on error. Missing
// source files are silently skipped since they're expected in many failure modes.
func (f *LauncherFixture) copyFileToDir(src, dst string) {
	f.t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		if !os.IsNotExist(err) {
			f.t.Logf("could not read %s for failures dir: %v", src, err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		f.t.Logf("could not create %s for failures dir: %v", filepath.Dir(dst), err)
		return
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		f.t.Logf("could not write %s to failures dir: %v", dst, err)
	}
}

// copyPathToDir copies a file, or a directory tree recursively, from src to dst.
func (f *LauncherFixture) copyPathToDir(src, dst string) {
	f.t.Helper()
	info, err := os.Stat(src)
	if err != nil {
		if !os.IsNotExist(err) {
			f.t.Logf("could not stat %s for failures dir: %v", src, err)
		}
		return
	}
	if !info.IsDir() {
		f.copyFileToDir(src, dst)
		return
	}
	_ = filepath.Walk(src, func(path string, walkInfo os.FileInfo, walkErr error) error {
		if walkErr != nil {
			f.t.Logf("could not walk %s for failures dir: %v", path, walkErr)
			return nil
		}
		if walkInfo.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			f.t.Logf("could not compute relative path for %s: %v", path, err)
			return nil
		}
		f.copyFileToDir(path, filepath.Join(dst, rel))
		return nil
	})
}

// Cleanup captures diagnostics for a failed test (while the launcher / VM can
// still be reached), then stops the launcher if it is running and removes
// WorkDir. SSHCaptureDiagnostics is a no-op on windows.
func (f *LauncherFixture) Cleanup() {
	if f.t.Failed() {
		f.SSHCaptureDiagnostics(f.t.Name())
		f.CopyLogsToFailuresDir(f.t.Name())
	}
	if f.vmRunning {
		// cmd.Dir MUST be f.WorkDir so the launcher's stopCmd can find
		// resources/config.json (relative path). Without it, stopCmd
		// prints "No resources/config.json; nothing to stop." and
		// no-ops — which on windows leaks the globally-named
		// exasol-local-db container into the next test and cascades
		// into "Container is already running" failures for
		// TestPortOverride*, TestStatusLifecycle, and TestDBConnection.
		// StopVM/run() sets cmd.Dir; this fallback path must too.
		cmd := exec.Command(f.BinaryPath, "stop")
		cmd.Dir = f.WorkDir
		_ = cmd.Run()
	}
	os.RemoveAll(f.WorkDir)
}

// requireIntegration is kept as a no-op helper for compatibility with existing
// tests; integration tests now always run when this package is executed.
func requireIntegration(t *testing.T) {
	t.Helper()
}

// run executes the launcher binary with the given arguments inside WorkDir and
// fails the test if the command exits non-zero.
func (f *LauncherFixture) run(args ...string) {
	f.t.Helper()
	cmd := exec.Command(f.BinaryPath, args...)
	cmd.Dir = f.WorkDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		f.t.Fatalf("launcher %v: %v", args, err)
	}
}

// findLauncherZip returns the path to the platform-appropriate launcher zip.
// Honors the LAUNCHER_ZIP env var override; falls back to launcherZipDefault
// (defined per platform in fixture_{darwin,windows}_test.go).
func findLauncherZip(t *testing.T) string {
	t.Helper()

	if p := os.Getenv("LAUNCHER_ZIP"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		t.Skipf("LAUNCHER_ZIP=%q not found; skipping", p)
	}

	// go test sets the working directory to the package directory (tests/),
	// so ../dist/ resolves to the repo root's dist/ directory.
	if _, err := os.Stat(launcherZipDefault); err != nil {
		t.Skipf(
			"launcher zip not found at %s; download the launcher artifact from CI or set LAUNCHER_ZIP",
			launcherZipDefault,
		)
	}
	return launcherZipDefault
}

// unzipTo extracts all files from zipPath into destDir, preserving permissions.
func unzipTo(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		destPath := filepath.Join(destDir, filepath.Clean(f.Name))

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(destPath, f.Mode()); err != nil {
				return fmt.Errorf("mkdir %s: %w", destPath, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return fmt.Errorf("mkdir parent of %s: %w", destPath, err)
		}

		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			return fmt.Errorf("create %s: %w", destPath, err)
		}

		rc, err := f.Open()
		if err != nil {
			out.Close()
			return fmt.Errorf("open zip entry %s: %w", f.Name, err)
		}

		_, copyErr := io.Copy(out, rc)
		rc.Close()
		out.Close()
		if copyErr != nil {
			return fmt.Errorf("extract %s: %w", f.Name, copyErr)
		}
	}
	return nil
}
