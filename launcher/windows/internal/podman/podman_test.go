// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package podman

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// installFakePodman drops a POSIX shell script named "podman" into a
// fresh temp directory, prepends that directory to PATH for the duration
// of the test, and returns the path of an argv-log file the shim writes
// to. The shim's behavior is entirely defined by the caller-supplied
// body — a case block on "$1" is the typical shape. The log file makes
// each podman invocation observable so tests can assert on the exact
// argv the helper produced.
//
// The tests use a POSIX shell script; Windows-native runs need a .bat
// variant which we do not need here because Phase 5 unit tests are
// designed to run on a Linux CI host.
func installFakePodman(t *testing.T, body string) (argvLogPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake podman shim uses a POSIX shell script; skipping on windows")
	}

	dir := t.TempDir()
	argvLogPath = filepath.Join(dir, "argv.log")
	script := "#!/bin/sh\n" +
		"# Log each argv token on its own line, followed by a '---' separator per call.\n" +
		"for arg in \"$@\"; do printf '%s\\n' \"$arg\" >> \"" + argvLogPath + "\"; done\n" +
		"printf -- '---\\n' >> \"" + argvLogPath + "\"\n" +
		body + "\n"

	binPath := filepath.Join(dir, "podman")
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake podman shim: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvLogPath
}

// readArgvCalls parses the log written by the fake shim into a slice of
// call slices, one entry per shim invocation.
func readArgvCalls(t *testing.T, logPath string) [][]string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read argv log: %v", err)
	}
	var calls [][]string
	var current []string
	for _, line := range strings.Split(string(data), "\n") {
		if line == "---" {
			calls = append(calls, current)
			current = nil
			continue
		}
		if line == "" {
			continue
		}
		current = append(current, line)
	}
	return calls
}

func TestAvailable_Success(t *testing.T) {
	installFakePodman(t, `exit 0`)
	if err := Available(); err != nil {
		t.Fatalf("Available() unexpected error: %v", err)
	}
}

func TestAvailable_NotOnPath(t *testing.T) {
	// Point PATH at an empty directory with no podman binary.
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	if err := Available(); err == nil {
		t.Fatal("expected error when podman is missing from PATH, got nil")
	} else if !strings.Contains(err.Error(), "podman-for-windows is required") {
		t.Errorf("error should mention prerequisite install: %v", err)
	}
}

func TestMachineRunning_Success(t *testing.T) {
	logPath := installFakePodman(t, `echo "Running"; exit 0`)
	if err := MachineRunning(); err != nil {
		t.Fatalf("MachineRunning() unexpected error: %v", err)
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(calls), calls)
	}
	wantArgv := []string{"machine", "inspect", "--format", "{{.State}}"}
	if got := calls[0]; !slicesEqual(got, wantArgv) {
		t.Errorf("argv mismatch: want %v, got %v", wantArgv, got)
	}
}

func TestMachineRunning_MultipleMachinesAnyRunning(t *testing.T) {
	// Two machines listed, only the second is running — should still succeed.
	installFakePodman(t, `printf 'Stopped\nRunning\n'; exit 0`)
	if err := MachineRunning(); err != nil {
		t.Fatalf("MachineRunning() unexpected error: %v", err)
	}
}

func TestMachineRunning_NotRunning(t *testing.T) {
	installFakePodman(t, `echo "Stopped"; exit 0`)
	err := MachineRunning()
	if err == nil {
		t.Fatal("expected error when no machine is running")
	}
	if !strings.Contains(err.Error(), "no machine is running") {
		t.Errorf("error should mention 'no machine is running': %v", err)
	}
}

func TestMachineRunning_InspectFails(t *testing.T) {
	installFakePodman(t, `echo "cannot connect to podman machine" >&2; exit 125`)
	err := MachineRunning()
	if err == nil {
		t.Fatal("expected error when podman machine inspect fails")
	}
	if !strings.Contains(err.Error(), "cannot connect to podman machine") {
		t.Errorf("error should propagate stderr: %v", err)
	}
}

func TestLoadImage_Success(t *testing.T) {
	logPath := installFakePodman(t, `echo "Loaded image: docker.io/exasol/nano:2026.2.0-nano.2"; exit 0`)
	got, err := LoadImage("/tmp/nano.tar.gz")
	if err != nil {
		t.Fatalf("LoadImage() unexpected error: %v", err)
	}
	want := "docker.io/exasol/nano:2026.2.0-nano.2"
	if got != want {
		t.Errorf("image ref: want %q, got %q", want, got)
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	wantArgv := []string{"load", "-i", "/tmp/nano.tar.gz"}
	if got := calls[0]; !slicesEqual(got, wantArgv) {
		t.Errorf("argv mismatch: want %v, got %v", wantArgv, got)
	}
}

func TestLoadImage_PluralOutput(t *testing.T) {
	// Some podman versions emit "Loaded image(s): ..., ..." — take the first.
	installFakePodman(t, `echo "Loaded image(s): quay.io/foo:v1, quay.io/foo:v2"; exit 0`)
	got, err := LoadImage("/tmp/multi.tar")
	if err != nil {
		t.Fatalf("LoadImage() unexpected error: %v", err)
	}
	if got != "quay.io/foo:v1" {
		t.Errorf("expected first ref 'quay.io/foo:v1', got %q", got)
	}
}

func TestLoadImage_LoadFails(t *testing.T) {
	installFakePodman(t, `echo "invalid tarball" >&2; exit 125`)
	_, err := LoadImage("/tmp/broken.tar")
	if err == nil {
		t.Fatal("expected LoadImage() to fail when podman load exits non-zero")
	}
	if !strings.Contains(err.Error(), "invalid tarball") {
		t.Errorf("error should include stderr: %v", err)
	}
}

func TestLoadImage_UnparseableOutput(t *testing.T) {
	installFakePodman(t, `echo "some unexpected output"; exit 0`)
	_, err := LoadImage("/tmp/mystery.tar")
	if err == nil {
		t.Fatal("expected LoadImage() to fail when output has no 'Loaded image:' line")
	}
	if !strings.Contains(err.Error(), "could not find loaded image reference") {
		t.Errorf("error should describe parse failure: %v", err)
	}
}

func TestParseLoadedImage(t *testing.T) {
	cases := []struct {
		name    string
		output  string
		want    string
		wantErr bool
	}{
		{"singular", "Loaded image: myimage:1\n", "myimage:1", false},
		{"singular with trailing newline", "Loaded image: myimage:2\n\n", "myimage:2", false},
		{"plural single", "Loaded image(s): only:1\n", "only:1", false},
		{"plural multiple", "Loaded image(s): a:1, b:2\n", "a:1", false},
		{"noise before", "Getting image source signatures\nLoaded image: x:1\n", "x:1", false},
		{"empty", "", "", true},
		{"no marker", "Foo bar\nBaz\n", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLoadedImage(tc.output)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestContainerExists_True(t *testing.T) {
	logPath := installFakePodman(t, `exit 0`)
	got, err := ContainerExists("my-container")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected exists=true")
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	wantArgv := []string{"container", "exists", "my-container"}
	if got := calls[0]; !slicesEqual(got, wantArgv) {
		t.Errorf("argv mismatch: want %v, got %v", wantArgv, got)
	}
}

func TestContainerExists_False(t *testing.T) {
	installFakePodman(t, `exit 1`)
	got, err := ContainerExists("missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected exists=false")
	}
}

func TestContainerExists_OtherFailure(t *testing.T) {
	// Non-1 exit code means something is actually wrong (e.g. podman missing);
	// that should surface as an error, not a false negative.
	installFakePodman(t, `echo "cannot reach podman socket" >&2; exit 125`)
	_, err := ContainerExists("whatever")
	if err == nil {
		t.Fatal("expected error for non-1 exit code")
	}
}

func TestContainerRunning_True(t *testing.T) {
	// The shim needs to answer two calls: `container exists` then
	// `container inspect ...`. Dispatch on $2.
	logPath := installFakePodman(t, `
case "$2" in
    exists) exit 0 ;;
    inspect) echo "true"; exit 0 ;;
    *) echo "unexpected argv: $*" >&2; exit 2 ;;
esac
`)
	got, err := ContainerRunning("my-container")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected running=true")
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 2 {
		t.Fatalf("expected 2 podman calls, got %d: %v", len(calls), calls)
	}
	if got := calls[1]; !slicesEqual(got, []string{"container", "inspect", "--format", "{{.State.Running}}", "my-container"}) {
		t.Errorf("second-call argv mismatch: %v", got)
	}
}

func TestContainerRunning_ExistsButStopped(t *testing.T) {
	installFakePodman(t, `
case "$2" in
    exists) exit 0 ;;
    inspect) echo "false"; exit 0 ;;
esac
`)
	got, err := ContainerRunning("my-container")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected running=false for stopped container")
	}
}

func TestContainerRunning_Missing(t *testing.T) {
	// container exists returns 1; inspect must NOT be called.
	logPath := installFakePodman(t, `
case "$2" in
    exists) exit 1 ;;
    inspect) echo "inspect should not be called" >&2; exit 99 ;;
esac
`)
	got, err := ContainerRunning("gone")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected running=false for missing container")
	}
	if calls := readArgvCalls(t, logPath); len(calls) != 1 {
		t.Errorf("expected exactly 1 podman call (the exists check), got %d: %v", len(calls), calls)
	}
}

func TestStop_PassesTimeout(t *testing.T) {
	logPath := installFakePodman(t, `exit 0`)
	if err := Stop("my-container", 30*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	wantArgv := []string{"stop", "--ignore", "--time", "30", "my-container"}
	if got := calls[0]; !slicesEqual(got, wantArgv) {
		t.Errorf("argv mismatch: want %v, got %v", wantArgv, got)
	}
}

func TestStop_NegativeTimeoutClamped(t *testing.T) {
	logPath := installFakePodman(t, `exit 0`)
	if err := Stop("c", -5*time.Second); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if got := calls[0][3]; got != "0" {
		t.Errorf("expected --time=0 for negative timeout, got %q", got)
	}
}

func TestStop_PodmanError(t *testing.T) {
	installFakePodman(t, `echo "not permitted" >&2; exit 125`)
	err := Stop("c", 30*time.Second)
	if err == nil {
		t.Fatal("expected error to propagate")
	}
	if !strings.Contains(err.Error(), "not permitted") {
		t.Errorf("error should include stderr: %v", err)
	}
}

func TestRm_Success(t *testing.T) {
	logPath := installFakePodman(t, `exit 0`)
	if err := Rm("my-container"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	wantArgv := []string{"rm", "--ignore", "my-container"}
	if got := calls[0]; !slicesEqual(got, wantArgv) {
		t.Errorf("argv mismatch: want %v, got %v", wantArgv, got)
	}
}

func TestRm_PodmanError(t *testing.T) {
	installFakePodman(t, `echo "container is running" >&2; exit 2`)
	err := Rm("running-c")
	if err == nil {
		t.Fatal("expected error to propagate")
	}
	if !strings.Contains(err.Error(), "container is running") {
		t.Errorf("error should include stderr: %v", err)
	}
}

func TestRun_PassesArgvVerbatim(t *testing.T) {
	logPath := installFakePodman(t, `exit 0`)
	argv := []string{"run", "-d", "--name", "foo", "-p", "8563:8563", "image:tag", "init"}
	if err := Run(argv); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if got := calls[0]; !slicesEqual(got, argv) {
		t.Errorf("argv mismatch: want %v, got %v", argv, got)
	}
}

func TestRun_Failure(t *testing.T) {
	installFakePodman(t, `echo "bad request" >&2; exit 125`)
	err := Run([]string{"foo", "bar"})
	if err == nil {
		t.Fatal("expected error from failing podman invocation")
	}
	// Error message should include the reconstructed command for debugging.
	if !strings.Contains(err.Error(), "podman foo bar failed") {
		t.Errorf("error should mention command: %v", err)
	}
}

func TestInitMachine_Success_Rootful(t *testing.T) {
	logPath := installFakePodman(t, `exit 0`)
	if err := InitMachine(true, 40); err != nil {
		t.Fatalf("InitMachine(true, 40) unexpected error: %v", err)
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 podman call, got %d: %v", len(calls), calls)
	}
	want := []string{"machine", "init", "--disk-size", "40", "--rootful"}
	if got := calls[0]; !slicesEqual(got, want) {
		t.Errorf("argv mismatch: want %v, got %v", want, got)
	}
}

func TestInitMachine_Success_Rootless(t *testing.T) {
	logPath := installFakePodman(t, `exit 0`)
	if err := InitMachine(false, 40); err != nil {
		t.Fatalf("InitMachine(false, 40) unexpected error: %v", err)
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 podman call, got %d: %v", len(calls), calls)
	}
	want := []string{"machine", "init", "--disk-size", "40"}
	if got := calls[0]; !slicesEqual(got, want) {
		t.Errorf("argv mismatch: want %v, got %v", want, got)
	}
}

func TestInitMachine_RejectsNonPositiveSize(t *testing.T) {
	// PATH deliberately empty so a bug that let this through to exec
	// would be visible (podman not found → different error).
	t.Setenv("PATH", t.TempDir())
	for _, size := range []int{0, -1, -100} {
		if err := InitMachine(true, size); err == nil {
			t.Errorf("InitMachine(true, %d): expected error, got nil", size)
		} else if !strings.Contains(err.Error(), "must be positive") {
			t.Errorf("InitMachine(true, %d): expected 'must be positive' error, got %v", size, err)
		}
	}
}

func TestInitMachine_PodmanFails(t *testing.T) {
	installFakePodman(t, `echo "machine already exists" >&2; exit 125`)
	err := InitMachine(true, 40)
	if err == nil {
		t.Fatal("expected error when podman machine init fails")
	}
	if !strings.Contains(err.Error(), "podman machine init failed") {
		t.Errorf("error should mention the command: %v", err)
	}
}

// --- Plan section 1: rootful default machine helpers -----------------

func TestMachineExists_Present(t *testing.T) {
	logPath := installFakePodman(t, `printf 'podman-machine-default\n'; exit 0`)
	exists, err := MachineExists()
	if err != nil {
		t.Fatalf("MachineExists() unexpected error: %v", err)
	}
	if !exists {
		t.Fatal("expected exists=true when default machine name appears in output")
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 podman call, got %d: %v", len(calls), calls)
	}
	want := []string{"machine", "list", "--format", "{{.Name}}"}
	if got := calls[0]; !slicesEqual(got, want) {
		t.Errorf("argv mismatch: want %v, got %v", want, got)
	}
}

func TestMachineExists_Absent(t *testing.T) {
	// Non-default machine present but not our target → exists=false, no error.
	installFakePodman(t, `printf 'some-other-machine\n'; exit 0`)
	exists, err := MachineExists()
	if err != nil {
		t.Fatalf("MachineExists() unexpected error: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false when default machine name is not listed")
	}
}

func TestMachineExists_EmptyOutput(t *testing.T) {
	installFakePodman(t, `exit 0`)
	exists, err := MachineExists()
	if err != nil {
		t.Fatalf("MachineExists() unexpected error: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false when podman list returns no output")
	}
}

func TestMachineExists_PodmanFails(t *testing.T) {
	installFakePodman(t, `echo "provider not installed" >&2; exit 125`)
	_, err := MachineExists()
	if err == nil {
		t.Fatal("expected error when podman machine list fails")
	}
	if !strings.Contains(err.Error(), "podman machine list failed") {
		t.Errorf("error should mention the command: %v", err)
	}
	if !strings.Contains(err.Error(), "provider not installed") {
		t.Errorf("error should surface stderr, got: %v", err)
	}
}

func TestMachineIsRootful_True(t *testing.T) {
	logPath := installFakePodman(t, `echo true; exit 0`)
	rootful, err := MachineIsRootful()
	if err != nil {
		t.Fatalf("MachineIsRootful() unexpected error: %v", err)
	}
	if !rootful {
		t.Fatal("expected rootful=true when podman prints 'true'")
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 podman call, got %d: %v", len(calls), calls)
	}
	want := []string{"machine", "inspect", "--format", "{{.Rootful}}", "podman-machine-default"}
	if got := calls[0]; !slicesEqual(got, want) {
		t.Errorf("argv mismatch: want %v, got %v", want, got)
	}
}

func TestMachineIsRootful_False(t *testing.T) {
	installFakePodman(t, `echo false; exit 0`)
	rootful, err := MachineIsRootful()
	if err != nil {
		t.Fatalf("MachineIsRootful() unexpected error: %v", err)
	}
	if rootful {
		t.Fatal("expected rootful=false when podman prints 'false'")
	}
}

func TestMachineIsRootful_UnexpectedOutput(t *testing.T) {
	installFakePodman(t, `echo maybe; exit 0`)
	_, err := MachineIsRootful()
	if err == nil {
		t.Fatal("expected error on unexpected .Rootful value")
	}
	if !strings.Contains(err.Error(), "unexpected podman machine .Rootful value") {
		t.Errorf("error should mention unexpected value, got: %v", err)
	}
}

func TestMachineIsRootful_PodmanFails(t *testing.T) {
	installFakePodman(t, `echo "machine does not exist" >&2; exit 125`)
	_, err := MachineIsRootful()
	if err == nil {
		t.Fatal("expected error when podman machine inspect fails")
	}
	if !strings.Contains(err.Error(), "podman machine inspect --format .Rootful failed") {
		t.Errorf("error should mention the command, got: %v", err)
	}
}

func TestMachineState_Success(t *testing.T) {
	logPath := installFakePodman(t, `echo Running; exit 0`)
	state, err := MachineState()
	if err != nil {
		t.Fatalf("MachineState() unexpected error: %v", err)
	}
	if state != "Running" {
		t.Errorf("expected state 'Running', got %q", state)
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 podman call, got %d: %v", len(calls), calls)
	}
	want := []string{"machine", "inspect", "--format", "{{.State}}", "podman-machine-default"}
	if got := calls[0]; !slicesEqual(got, want) {
		t.Errorf("argv mismatch: want %v, got %v", want, got)
	}
}

func TestMachineState_TrimsWhitespace(t *testing.T) {
	installFakePodman(t, `printf '  Stopped\n\n'; exit 0`)
	state, err := MachineState()
	if err != nil {
		t.Fatalf("MachineState() unexpected error: %v", err)
	}
	if state != "Stopped" {
		t.Errorf("expected trimmed state 'Stopped', got %q", state)
	}
}

func TestMachineState_PodmanFails(t *testing.T) {
	installFakePodman(t, `echo "machine does not exist" >&2; exit 125`)
	_, err := MachineState()
	if err == nil {
		t.Fatal("expected error when podman machine inspect fails")
	}
	if !strings.Contains(err.Error(), "podman machine inspect --format .State failed") {
		t.Errorf("error should mention the command, got: %v", err)
	}
}

func TestStopMachine_Success(t *testing.T) {
	logPath := installFakePodman(t, `exit 0`)
	if err := StopMachine(); err != nil {
		t.Fatalf("StopMachine() unexpected error: %v", err)
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 podman call, got %d: %v", len(calls), calls)
	}
	want := []string{"machine", "stop"}
	if got := calls[0]; !slicesEqual(got, want) {
		t.Errorf("argv mismatch: want %v, got %v", want, got)
	}
}

func TestStopMachine_PodmanFails(t *testing.T) {
	installFakePodman(t, `exit 125`)
	err := StopMachine()
	if err == nil {
		t.Fatal("expected error when podman machine stop fails")
	}
	if !strings.Contains(err.Error(), "podman machine stop failed") {
		t.Errorf("error should mention the command: %v", err)
	}
}

func TestSetMachineRootful_Success(t *testing.T) {
	logPath := installFakePodman(t, `exit 0`)
	if err := SetMachineRootful(); err != nil {
		t.Fatalf("SetMachineRootful() unexpected error: %v", err)
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 podman call, got %d: %v", len(calls), calls)
	}
	want := []string{"machine", "set", "--rootful"}
	if got := calls[0]; !slicesEqual(got, want) {
		t.Errorf("argv mismatch: want %v, got %v", want, got)
	}
}

func TestSetMachineRootful_PodmanFails(t *testing.T) {
	installFakePodman(t, `exit 125`)
	err := SetMachineRootful()
	if err == nil {
		t.Fatal("expected error when podman machine set --rootful fails")
	}
	if !strings.Contains(err.Error(), "podman machine set --rootful failed") {
		t.Errorf("error should mention the command: %v", err)
	}
}

func TestStartMachine_Success(t *testing.T) {
	logPath := installFakePodman(t, `exit 0`)
	if err := StartMachine(); err != nil {
		t.Fatalf("StartMachine() unexpected error: %v", err)
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 podman call, got %d: %v", len(calls), calls)
	}
	want := []string{"machine", "start"}
	if got := calls[0]; !slicesEqual(got, want) {
		t.Errorf("argv mismatch: want %v, got %v", want, got)
	}
}

func TestStartMachine_PodmanFails(t *testing.T) {
	installFakePodman(t, `echo "wsl not installed" >&2; exit 125`)
	err := StartMachine()
	if err == nil {
		t.Fatal("expected error when podman machine start fails")
	}
	if !strings.Contains(err.Error(), "podman machine start failed") {
		t.Errorf("error should mention the command: %v", err)
	}
}

// --- Phase 16: InspectState / Exec / LogsTail -------------------------

func TestInspectState_Success(t *testing.T) {
	logPath := installFakePodman(t, `echo "running 0"; exit 0`)
	got, err := InspectState("my-container")
	if err != nil {
		t.Fatalf("InspectState() unexpected error: %v", err)
	}
	if got.Status != "running" || got.ExitCode != 0 {
		t.Errorf("state mismatch: got %+v, want {running 0}", got)
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d: %v", len(calls), calls)
	}
	wantArgv := []string{
		"container", "inspect", "--format",
		"{{.State.Status}} {{.State.ExitCode}}", "my-container",
	}
	if got := calls[0]; !slicesEqual(got, wantArgv) {
		t.Errorf("argv mismatch: want %v, got %v", wantArgv, got)
	}
}

func TestInspectState_ExitedWithNonZeroExitCode(t *testing.T) {
	installFakePodman(t, `echo "exited 137"; exit 0`)
	got, err := InspectState("dead-container")
	if err != nil {
		t.Fatalf("InspectState() unexpected error: %v", err)
	}
	if got.Status != "exited" {
		t.Errorf("Status: want %q, got %q", "exited", got.Status)
	}
	if got.ExitCode != 137 {
		t.Errorf("ExitCode: want 137, got %d", got.ExitCode)
	}
}

func TestInspectState_PodmanFails(t *testing.T) {
	// Missing container is podman's own error path (exits non-zero).
	installFakePodman(t, `echo "no such container: gone" >&2; exit 125`)
	_, err := InspectState("gone")
	if err == nil {
		t.Fatal("expected error when podman container inspect fails")
	}
	if !strings.Contains(err.Error(), "no such container") {
		t.Errorf("error should include podman stderr: %v", err)
	}
}

func TestInspectState_UnexpectedFormat(t *testing.T) {
	installFakePodman(t, `echo "just one field"; exit 0`)
	_, err := InspectState("weird")
	if err == nil {
		t.Fatal("expected parse error for malformed output")
	}
	if !strings.Contains(err.Error(), "unexpected") {
		t.Errorf("error should mention 'unexpected': %v", err)
	}
}

func TestInspectState_UnparseableExitCode(t *testing.T) {
	installFakePodman(t, `echo "running abc"; exit 0`)
	_, err := InspectState("weird")
	if err == nil {
		t.Fatal("expected parse error for non-numeric exit code")
	}
	if !strings.Contains(err.Error(), "invalid exit code") {
		t.Errorf("error should mention 'invalid exit code': %v", err)
	}
}

func TestExec_SuccessCapturesStdoutStderr(t *testing.T) {
	logPath := installFakePodman(t,
		`printf 'to stdout\n'; printf 'to stderr\n' >&2; exit 0`)
	stdout, stderr, ec, err := Exec("my-container", []string{"true"})
	if err != nil {
		t.Fatalf("Exec() unexpected error: %v", err)
	}
	if ec != 0 {
		t.Errorf("exit code: want 0, got %d", ec)
	}
	if !strings.Contains(stdout, "to stdout") {
		t.Errorf("stdout: want 'to stdout', got %q", stdout)
	}
	if !strings.Contains(stderr, "to stderr") {
		t.Errorf("stderr: want 'to stderr', got %q", stderr)
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	wantArgv := []string{"exec", "my-container", "true"}
	if got := calls[0]; !slicesEqual(got, wantArgv) {
		t.Errorf("argv mismatch: want %v, got %v", wantArgv, got)
	}
}

func TestExec_NonZeroExitIsNotAGoError(t *testing.T) {
	// The Phase 16 poll loop's `test ! -e ...` probe relies on Exec
	// surfacing the exit code without treating it as an error, so a
	// still-present marker (exit 1) does not abort the wait.
	installFakePodman(t, `exit 1`)
	_, _, ec, err := Exec("my-container", []string{"test", "!", "-e", "/marker"})
	if err != nil {
		t.Fatalf("Exec() with non-zero child should not error, got %v", err)
	}
	if ec != 1 {
		t.Errorf("exit code: want 1, got %d", ec)
	}
}

func TestExec_PropagatesArbitraryExitCode(t *testing.T) {
	installFakePodman(t, `exit 42`)
	_, _, ec, err := Exec("c", []string{"cmd"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ec != 42 {
		t.Errorf("exit code: want 42, got %d", ec)
	}
}

func TestExec_SpawnFailureReportsError(t *testing.T) {
	// PATH deliberately empty so exec.Command cannot find "podman".
	// A "cannot find binary" failure is a genuine plumbing error and
	// must surface, unlike a merely-non-zero child.
	t.Setenv("PATH", t.TempDir())
	_, _, ec, err := Exec("c", []string{"cmd"})
	if err == nil {
		t.Fatal("expected error when podman is not on PATH")
	}
	if ec != -1 {
		t.Errorf("exit code on spawn failure: want -1, got %d", ec)
	}
	if !strings.Contains(err.Error(), "podman exec") {
		t.Errorf("error should mention the command: %v", err)
	}
}

func TestLogsTail_Success(t *testing.T) {
	logPath := installFakePodman(t, `printf 'line1\nline2\n'; exit 0`)
	out, err := LogsTail("my-container", 50)
	if err != nil {
		t.Fatalf("LogsTail() unexpected error: %v", err)
	}
	if !strings.Contains(out, "line1") || !strings.Contains(out, "line2") {
		t.Errorf("output missing expected lines: %q", out)
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	wantArgv := []string{"logs", "--tail", "50", "my-container"}
	if got := calls[0]; !slicesEqual(got, wantArgv) {
		t.Errorf("argv mismatch: want %v, got %v", wantArgv, got)
	}
}

func TestLogsTail_MergesStderrIntoOutput(t *testing.T) {
	// Container writes go to podman's stderr; the debug consumer wants
	// them merged with stdout so the diagnostic is not split across
	// two return values.
	installFakePodman(t, `printf 'stdout line\n'; printf 'stderr line\n' >&2; exit 0`)
	out, err := LogsTail("c", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "stdout line") || !strings.Contains(out, "stderr line") {
		t.Errorf("expected merged output, got: %q", out)
	}
}

func TestLogsTail_NormalisesNonPositiveLines(t *testing.T) {
	logPath := installFakePodman(t, `exit 0`)
	if _, err := LogsTail("c", 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := LogsTail("c", -5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	want := strconv.Itoa(logsTailDefaultLines)
	for i, c := range calls {
		if len(c) < 3 || c[2] != want {
			t.Errorf("call %d: want --tail %s, got %v", i, want, c)
		}
	}
}

func TestLogsTail_PodmanFails(t *testing.T) {
	installFakePodman(t, `echo "no logs" >&2; exit 125`)
	out, err := LogsTail("c", 10)
	if err == nil {
		t.Fatal("expected error when podman logs fails")
	}
	// Combined output is still returned so callers can surface it in
	// a diagnostic even on failure.
	if !strings.Contains(out, "no logs") {
		t.Errorf("expected combined output to include stderr, got: %q", out)
	}
}

func TestContainerFileExists_Present(t *testing.T) {
	// Success path: podman cp streams the file to stdout as tar and
	// exits 0. Body writes some bytes so the io.Discard sink is
	// exercised.
	logPath := installFakePodman(t, `printf 'fake tar bytes'; exit 0`)
	got, err := ContainerFileExists("my-container", "/exa/marker")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected exists=true")
	}
	calls := readArgvCalls(t, logPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	wantArgv := []string{"container", "cp", "my-container:/exa/marker", "-"}
	if got := calls[0]; !slicesEqual(got, wantArgv) {
		t.Errorf("argv mismatch: want %v, got %v", wantArgv, got)
	}
}

func TestContainerFileExists_MissingViaStderrNeedle(t *testing.T) {
	// Podman's canonical missing-file wording. Table-driven so a
	// future wording drift is a one-line fix. All stderr fixtures
	// are single-quote-safe (no apostrophes) so we can embed them
	// via sh single-quote escaping.
	cases := []struct {
		name   string
		stderr string
	}{
		{"classic no such file", `Error: statting "/exa/marker": stat /exa/marker: no such file or directory`},
		{"newer container-relative", `Error: "/exa/marker" does not exist in container "my-container"`},
		{"lowercase drift", `error: no such file or directory`},
		{"mixed-case with prefix", `Error: could not find the file /exa/marker in container`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`printf '%%s\n' '%s' >&2; exit 1`, tc.stderr)
			installFakePodman(t, body)
			got, err := ContainerFileExists("my-container", "/exa/marker")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got {
				t.Error("expected exists=false")
			}
		})
	}
}

func TestContainerFileExists_GenuineFailureSurfacesError(t *testing.T) {
	// Any non-zero exit whose stderr does NOT match a known
	// "missing file" needle must surface as an error, so a real
	// podman failure (container gone, permission denied, ...) is
	// not silently misread as "marker cleared".
	installFakePodman(t, `echo "Error: no container with name my-container" >&2; exit 125`)
	_, err := ContainerFileExists("my-container", "/exa/marker")
	if err == nil {
		t.Fatal("expected error for unknown failure mode")
	}
	if !strings.Contains(err.Error(), "no container with name my-container") {
		t.Errorf("error should propagate stderr: %v", err)
	}
	if !strings.Contains(err.Error(), "exit 125") {
		t.Errorf("error should mention exit code: %v", err)
	}
}

func TestContainerFileExists_SpawnFailure(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := ContainerFileExists("c", "/some/path")
	if err == nil {
		t.Fatal("expected error when podman is not on PATH")
	}
	if !strings.Contains(err.Error(), "podman container cp") {
		t.Errorf("error should mention the command: %v", err)
	}
}

func TestIsPodmanCpMissingPath(t *testing.T) {
	// Pure unit test on the stderr matcher — makes future wording
	// drift a one-line fix and prevents accidental broadening of
	// the pattern from swallowing genuine failures.
	cases := []struct {
		name   string
		stderr string
		want   bool
	}{
		{"empty", "", false},
		{"classic", `stat /foo: no such file or directory`, true},
		{"uppercase", `Error: No Such File Or Directory`, true},
		{"container-relative", `"/x" does not exist in container "c"`, true},
		{"could not find", `Could not find the file /x`, true},
		{"permission denied", `permission denied`, false},
		{"container gone", `no container with name "c"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPodmanCpMissingPath(tc.stderr); got != tc.want {
				t.Errorf("isPodmanCpMissingPath(%q)=%v, want %v", tc.stderr, got, tc.want)
			}
		})
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
