// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

// Package podman is the internal wrapper the windows-launcher uses
// to talk to the natively installed podman-for-windows. Every function is
// a thin shim around a single `podman` subprocess so that behavior can be
// exercised by unit tests using a fake `podman` shell script on PATH — no
// live podman-for-windows machine is required to test this package.

package podman

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

var binary = "podman"

// DefaultMachineName is the podman-for-windows default machine name that
// the launcher targets exclusively. See plan.md § "Ensure --rootful
// podman machine" for why we do not enumerate or select between multiple
// machines.
const DefaultMachineName = "podman-machine-default"

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
// "running" state.
//
// The command output has one State value per configured machine. As long
// as any of them reports "running" we consider the
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
// stdout.
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

// Run invokes `podman <args...>` with its stdout and stderr streamed
// straight to the launcher process. Intended for long-form commands
// such as `run -d ...` where the user should see any diagnostics
// podman emits (image layer progress, container-startup errors, etc).
// For commands whose output the launcher needs to parse, use a purpose-
// built helper (LoadImage, ContainerRunning, ...) instead.
func Run(args []string) error {
	cmd := exec.Command(binary, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("podman %s failed: %w", strings.Join(args, " "), err)
	}
	return nil
}

// InitMachine initializes a new podman machine with the given disk
// size in GB. Intended for use immediately after a fresh winget install
// of podman-for-windows, where no machine exists yet. Streams podman's
// own output so the user can watch progress — the first-time WSL2
// image download is a multi-minute step.
//
// rootful=true adds --rootful so the machine is created in rootful mode
// from the outset. See the ensureRootfulPodmanMachine doc comment in
// main.go for the plan-level rationale.
//
// diskSizeGB is passed through directly. Zero or negative values
// return an argument error before invoking podman.
//
// The provider (WSL vs Hyper-V) is intentionally left to podman's own
// default rather than being forced via a flag. Podman ≥ 6.0 accepts
// --provider {wsl,hyperv} but podman 5.x (still the current stable at
// the time of writing) does not recognise the flag and exits with
// "unknown flag: --provider". WSL is podman-for-windows's default on
// every current release, so relying on the default keeps us compatible
// across both 5.x and 6.x. Users who want Hyper-V can set
// CONTAINERS_MACHINE_PROVIDER=hyperv in their environment.
func InitMachine(rootful bool, diskSizeGB int) error {
	if diskSizeGB <= 0 {
		return fmt.Errorf("podman machine init: disk size must be positive, got %d", diskSizeGB)
	}
	args := []string{
		"machine", "init",
		"--disk-size", strconv.Itoa(diskSizeGB),
	}
	if rootful {
		args = append(args, "--rootful")
	}
	cmd := exec.Command(binary, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("podman machine init failed: %w", err)
	}
	return nil
}

// MachineExists reports whether the default podman machine
// (DefaultMachineName) exists — regardless of whether it is currently
// running or stopped.
//
// Uses `podman machine list --format '{{.Name}}'` and matches by name
// so that transient errors (socket unreachable, provider not installed,
// etc.) surface as an error return rather than as a spurious
// "does not exist" false.
func MachineExists() (bool, error) {
	cmd := exec.Command(binary, "machine", "list", "--format", "{{.Name}}")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf(
			"podman machine list failed (%w): %s",
			err, strings.TrimSpace(stderr.String()),
		)
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		if strings.TrimSpace(line) == DefaultMachineName {
			return true, nil
		}
	}
	return false, nil
}

// MachineIsRootful reports whether the default podman machine is
// configured as rootful. Only meaningful when MachineExists() has
// already returned true; a "does not exist" error from podman is
// propagated as an error.
func MachineIsRootful() (bool, error) {
	cmd := exec.Command(binary, "machine", "inspect",
		"--format", "{{.Rootful}}", DefaultMachineName)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf(
			"podman machine inspect --format .Rootful failed (%w): %s",
			err, strings.TrimSpace(stderr.String()),
		)
	}
	switch strings.ToLower(strings.TrimSpace(stdout.String())) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf(
			"unexpected podman machine .Rootful value: %q",
			strings.TrimSpace(stdout.String()),
		)
	}
}

// StopMachine stops the default podman machine. Blocks until the stop
// completes or podman reports an error. Required before SetMachineRootful
// because podman rejects the mode change on a running machine.
func StopMachine() error {
	cmd := exec.Command(binary, "machine", "stop")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("podman machine stop failed: %w", err)
	}
	return nil
}

// SetMachineRootful flips the default machine to rootful. Podman
// requires the machine to be stopped first — callers must call
// StopMachine ahead of this.
func SetMachineRootful() error {
	cmd := exec.Command(binary, "machine", "set", "--rootful")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("podman machine set --rootful failed: %w", err)
	}
	return nil
}

// StartMachine starts the default podman machine. Blocks until podman
// reports the machine is running (or exits with an error). Intended as
// the second half of the winget-install → machine-init → machine-start
// bootstrap flow.
func StartMachine() error {
	cmd := exec.Command(binary, "machine", "start")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("podman machine start failed: %w", err)
	}
	return nil
}

// ContainerState reflects the subset of `podman container inspect
// --format ...` output that Phase 16's waitForDBReady needs.
//
// Status is one of podman's lifecycle strings — typically "created",
// "running", "paused", "exited", "removing", or "dead". ExitCode is
// only meaningful once Status has reached a terminal value ("exited",
// "removing", "stopped", "dead").
type ContainerState struct {
	Status   string
	ExitCode int
}

// InspectState returns the current lifecycle status and exit code of
// the named container. Used by the Phase 16 wait loop to detect an
// early exit (so a launcher-side timeout does not sit polling a dead
// container until the deadline).
//
// The output format is deliberately a single line of two
// space-separated fields, mirroring the shape stopCmd's inspect uses
// so the fake-shim tests can dispatch on $2 alone. Callers must not
// treat "unexpected format" as "container missing" — a missing
// container makes podman itself exit non-zero, which surfaces here as
// a wrapped error before parsing runs.
func InspectState(name string) (ContainerState, error) {
	cmd := exec.Command(binary, "container", "inspect",
		"--format", "{{.State.Status}} {{.State.ExitCode}}", name)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return ContainerState{}, fmt.Errorf(
			"podman container inspect %s failed (%w): %s",
			name, err, strings.TrimSpace(stderr.String()),
		)
	}
	fields := strings.Fields(strings.TrimSpace(stdout.String()))
	if len(fields) != 2 {
		return ContainerState{}, fmt.Errorf(
			"unexpected podman container inspect output for %s: %q",
			name, strings.TrimSpace(stdout.String()),
		)
	}
	ec, err := strconv.Atoi(fields[1])
	if err != nil {
		return ContainerState{}, fmt.Errorf(
			"invalid exit code %q from podman container inspect for %s: %w",
			fields[1], name, err,
		)
	}
	return ContainerState{Status: fields[0], ExitCode: ec}, nil
}

// Exec runs `podman exec <name> <argv...>` and returns the captured
// stdout, stderr, and the child's exit code.
//
// A non-zero exit code is NOT treated as a Go error, because
// polling probes (see ContainerFileExists) need to distinguish "the
// command inside the container said no" from "podman itself failed
// to run the command". Genuine podman failures (binary not on PATH,
// container gone mid-wait, ...) surface as a non-nil error with
// exitCode == -1.
//
// Caveat: `podman exec` requires the requested command to actually
// exist inside the container. The exasol/nano image is minimal and
// does not ship /bin/test, /bin/sh, /bin/ls, or coreutils — attempts
// to exec any of those return exit 127 (command not found) with a
// message on stderr. For file-existence probes against Nano use
// ContainerFileExists instead, which is podman-only and works even
// against a distroless base.
func Exec(name string, argv []string) (stdout, stderr string, exitCode int, err error) {
	full := append([]string{"exec", name}, argv...)
	cmd := exec.Command(binary, full...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	stdout = out.String()
	stderr = errBuf.String()
	if runErr == nil {
		return stdout, stderr, 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return stdout, stderr, exitErr.ExitCode(), nil
	}
	return stdout, stderr, -1, fmt.Errorf(
		"podman exec %s %s failed to spawn: %w",
		name, strings.Join(argv, " "), runErr,
	)
}

// ContainerFileExists returns whether `path` exists inside the named
// container's filesystem.
//
// Implemented via `podman container cp <name>:<path> -` (with stdout
// discarded) rather than `podman exec test -e <path>` because the
// probe must work against distroless / scratch base images that ship
// no /bin/test, /bin/sh, or coreutils. `podman cp` reads the file
// via podman's own storage driver, so no in-container binary is
// required.
//
// Semantics:
//   - podman cp exits 0 → file exists → returns (true, nil).
//   - podman cp exits non-zero with a "no such file" / "does not
//     exist in container" style stderr → returns (false, nil).
//   - Any other non-zero exit (permission denied, container gone,
//     podman socket broken, ...) surfaces as an error so a genuine
//     plumbing failure is not silently misread as "file absent".
//
// stderr from every failing invocation is included in returned
// errors so callers can spot silent regressions.
func ContainerFileExists(name, path string) (bool, error) {
	cmd := exec.Command(binary, "container", "cp", name+":"+path, "-")
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		return false, fmt.Errorf(
			"podman container cp %s:%s failed to spawn: %w",
			name, path, runErr,
		)
	}
	errText := strings.TrimSpace(stderr.String())
	if isPodmanCpMissingPath(errText) {
		return false, nil
	}
	return false, fmt.Errorf(
		"podman container cp %s:%s failed (exit %d): %s",
		name, path, exitErr.ExitCode(), errText,
	)
}

// isPodmanCpMissingPath returns whether stderr from a failed
// `podman container cp` invocation indicates that the source path
// simply does not exist inside the container (as opposed to a
// genuine plumbing failure).
//
// Podman's exact wording has drifted across releases; both current
// and historical variants are covered here so a podman upgrade in
// CI does not silently turn "marker gone" into a hard error.
func isPodmanCpMissingPath(stderr string) bool {
	needles := []string{
		"no such file or directory",
		"does not exist in container",
		"could not find the file",
	}
	lower := strings.ToLower(stderr)
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

// LogsTail returns the last `lines` lines of the named container's
// podman-managed log stream. Passes --tail to keep the output bounded
// when a container has produced megabytes of logs. A non-positive
// `lines` value is normalised to logsTailDefaultLines so callers can
// pass through a config value without a pre-check.
//
// Both stdout and stderr from `podman logs` are returned in the
// combined output (podman writes container stderr onto its own
// stderr; the launcher-facing consumer is a debug tail, so we merge
// both streams). A non-zero exit from `podman logs` (e.g. container
// gone) is returned as an error with whatever stderr podman emitted
// so the caller can still surface it in a diagnostic message.
func LogsTail(name string, lines int) (string, error) {
	if lines <= 0 {
		lines = logsTailDefaultLines
	}
	cmd := exec.Command(binary, "logs", "--tail", strconv.Itoa(lines), name)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	if err := cmd.Run(); err != nil {
		return combined.String(), fmt.Errorf(
			"podman logs --tail %d %s failed: %w", lines, name, err,
		)
	}
	return combined.String(), nil
}

// logsTailDefaultLines is the fallback bound applied when a caller
// passes a non-positive `lines` argument to LogsTail. Kept as a
// package constant so both LogsTail and its tests reference the same
// value.
const logsTailDefaultLines = 200
