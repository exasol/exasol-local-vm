// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package podman

import (
	"os"
	"path/filepath"
	"runtime"
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
