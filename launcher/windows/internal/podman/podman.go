// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

// Package podman is the internal wrapper the windows-runner launcher uses
// to talk to the natively installed podman-for-windows. Every function is
// a thin shim around a single `podman` subprocess so that behavior can be
// exercised by unit tests using a fake `podman` shell script on PATH — no
// live podman-for-windows machine is required to test this package.
//
// Cross-platform note: this package is under launcher/windows/internal/
// because only the windows launcher uses it, but no `//go:build windows`
// tag is set so that the unit tests can run on a Linux CI host (using a
// shell-script shim). At runtime on Windows, exec.LookPath transparently
// resolves "podman" to podman.exe.
package podman

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// binary is the executable name the package invokes. Kept as a package
// variable rather than a hardcoded "podman" literal so that future test
// scenarios (for example, exercising a badly-named PATH entry) can swap
// it out. Production code never overrides this.
var binary = "podman"

// Available checks that a podman binary is on PATH and can report its
// version. It is intended as the very first pre-flight check the launcher
// performs on start-up.
func Available() error {
	cmd := exec.Command(binary, "--version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf(
			"podman-for-windows is required but %q was not found on PATH or failed to run; "+
				"install it from https://podman.io/ and ensure %q is on PATH: %w",
			binary, binary, err,
		)
	}
	return nil
}

// MachineRunning checks that at least one podman machine is in the
// "running" state. On windows/macOS podman ships its own WSL2/vfkit
// backing VM which must be started before any container can run.
//
// The command output has one State value per configured machine. As long
// as any of them reports "running" (case-insensitive) we consider the
// prerequisite met.
func MachineRunning() error {
	cmd := exec.Command(binary, "machine", "inspect", "--format", "{{.State}}")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf(
			"podman-for-windows machine state could not be inspected (%w): %s\n"+
				"Run 'podman machine init && podman machine start' first.",
			err, strings.TrimSpace(stderr.String()),
		)
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if strings.EqualFold(strings.TrimSpace(line), "running") {
			return nil
		}
	}
	return errors.New(
		"podman-for-windows is installed but no machine is running; " +
			"run 'podman machine start' first",
	)
}

// LoadImage loads a container image tarball into podman's local image
// store and returns the reference reported by podman (for example
// docker.io/exasol/nano:2026.2.0-nano.2). Handles both
//
//	Loaded image: <ref>
//	Loaded image(s): <ref1>, <ref2>
//
// output formats. If the tarball contains multiple images the first one
// is returned.
func LoadImage(tarballPath string) (string, error) {
	cmd := exec.Command(binary, "load", "-i", tarballPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf(
			"podman load -i %s failed (%w): %s",
			tarballPath, err, strings.TrimSpace(stderr.String()),
		)
	}
	ref, err := parseLoadedImage(stdout.String())
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stdout.String()))
	}
	return ref, nil
}

// parseLoadedImage extracts the first image reference from `podman load`
// stdout. Split out for direct unit testing so we do not need a fake
// podman for pure parsing coverage.
func parseLoadedImage(output string) (string, error) {
	prefixes := []string{"Loaded image: ", "Loaded image(s): "}
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		for _, prefix := range prefixes {
			if strings.HasPrefix(line, prefix) {
				rest := strings.TrimPrefix(line, prefix)
				// The "(s)" plural form may list several refs comma-separated;
				// take the first.
				if idx := strings.Index(rest, ","); idx >= 0 {
					rest = rest[:idx]
				}
				rest = strings.TrimSpace(rest)
				if rest == "" {
					continue
				}
				return rest, nil
			}
		}
	}
	return "", errors.New("could not find loaded image reference in podman load output")
}

// ContainerExists returns whether a container with the given name is
// registered with podman, regardless of its running state. Uses `podman
// container exists`, which exits 0/1 for present/absent with no output.
func ContainerExists(name string) (bool, error) {
	cmd := exec.Command(binary, "container", "exists", name)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("podman container exists %s: %w", name, err)
}

// ContainerRunning returns true if the named container exists AND its
// State.Running field is true. A missing container is reported as not
// running (false, nil) rather than an error, matching how the launcher
// treats "container gone" and "container stopped" identically.
func ContainerRunning(name string) (bool, error) {
	exists, err := ContainerExists(name)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	cmd := exec.Command(binary, "container", "inspect", "--format", "{{.State.Running}}", name)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf(
			"podman container inspect %s failed (%w): %s",
			name, err, strings.TrimSpace(stderr.String()),
		)
	}
	return strings.TrimSpace(stdout.String()) == "true", nil
}

// Stop gracefully stops the named container, allowing up to timeout for a
// clean shutdown before SIGKILL. Idempotent: uses `podman stop --ignore`
// which succeeds if the container is already stopped or does not exist.
//
// timeout is rounded down to whole seconds; a negative timeout is clamped
// to 0 (immediate SIGKILL).
func Stop(name string, timeout time.Duration) error {
	seconds := int(timeout.Seconds())
	if seconds < 0 {
		seconds = 0
	}
	cmd := exec.Command(binary, "stop", "--ignore", "--time", strconv.Itoa(seconds), name)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf(
			"podman stop %s failed (%w): %s",
			name, err, strings.TrimSpace(stderr.String()),
		)
	}
	return nil
}

// Rm removes the named container. Idempotent: uses `podman rm --ignore`
// so a missing container is not an error. Does NOT pass --force, so
// running containers are refused — callers must call Stop first.
func Rm(name string) error {
	cmd := exec.Command(binary, "rm", "--ignore", name)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf(
			"podman rm %s failed (%w): %s",
			name, err, strings.TrimSpace(stderr.String()),
		)
	}
	return nil
}
