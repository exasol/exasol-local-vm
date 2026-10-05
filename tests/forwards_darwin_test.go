// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin

package integration

import (
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLabeledPortForwarding(t *testing.T) {
	// Given
	f := NewLauncherFixture(t)
	defer f.Cleanup()
	f.Init()
	exactPort := reserveFreePort(t)

	// When
	f.StartVMWithForwards(
		2, 4096, 10,
		fmt.Sprintf("exact:22:%d", exactPort),
		"dynamic:22:0",
	)
	state := f.VMState()

	// Then
	if got := state.Forwards["exact"]; got.GuestPort != 22 || got.HostPort != exactPort {
		t.Fatalf("exact forward = %+v, want guest 22 and host %d", got, exactPort)
	}
	dynamic := state.Forwards["dynamic"]
	if dynamic.GuestPort != 22 || dynamic.HostPort == 0 {
		t.Fatalf("dynamic forward = %+v, want guest 22 and assigned host port", dynamic)
	}
	assertSSHBanner(t, exactPort)
	assertSSHBanner(t, dynamic.HostPort)

	cmd := exec.Command(f.BinaryPath, "health-check")
	cmd.Dir = f.WorkDir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("health-check failed: %v", err)
	}
	var health struct {
		Ports map[string]struct {
			State string `json:"state"`
		} `json:"ports"`
	}
	if err := json.Unmarshal(output, &health); err != nil {
		t.Fatalf("failed to parse health-check output %q: %v", output, err)
	}
	for _, name := range []string{"exact", "dynamic"} {
		if got := health.Ports[name].State; got != "reachable" {
			t.Fatalf("health of %q = %q, want reachable", name, got)
		}
	}
}

func TestLabeledPortForwardingGivenOccupiedHostPort(t *testing.T) {
	// Given
	f := NewLauncherFixture(t)
	defer f.Cleanup()
	f.Init()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to occupy host port: %v", err)
	}
	defer listener.Close()
	occupiedPort := listener.Addr().(*net.TCPAddr).Port

	// When
	err = f.StartVMExpectError(
		2, 4096, 10,
		"--forward", fmt.Sprintf("occupied:22:%d", occupiedPort),
	)

	// Then
	if err == nil {
		t.Fatalf("start accepted occupied host port %d", occupiedPort)
	}
	if !strings.Contains(err.Error(), "cannot bind host port") {
		t.Fatalf("start error = %v, want host-port bind failure", err)
	}
}

func TestLiveForwardingPreservesRunningVM(t *testing.T) {
	// Given
	f := NewLauncherFixture(t)
	defer f.Cleanup()
	f.Init()
	f.StartVMWithForwards(2, 4096, 10, "existing:22:0")
	before := f.VMState()
	forward := func(args ...string) map[string]forwardState {
		t.Helper()
		cmd := exec.Command(f.BinaryPath, append([]string{"forward"}, args...)...)
		cmd.Dir = f.WorkDir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("forward %v: %v: %s", args, err, output)
		}
		var response struct {
			Forwards map[string]forwardState `json:"forwards"`
		}
		if err := json.Unmarshal(output, &response); err != nil {
			t.Fatal(err)
		}
		return response.Forwards
	}
	// When
	added := forward("add", "live:22:0")
	repeated := forward("add", "live:22:0")
	listed := forward("list")
	afterAdd := f.VMState()
	assertSSHBanner(t, added["live"].HostPort)
	removed := forward("remove", "live")
	forward("remove", "live")
	afterRemove := f.VMState()
	// Then
	if !reflect.DeepEqual(added, repeated) || !reflect.DeepEqual(added, listed) || !reflect.DeepEqual(added, afterAdd.Forwards) {
		t.Fatalf("inconsistent live mappings: added=%v repeated=%v listed=%v saved=%v", added, repeated, listed, afterAdd.Forwards)
	}
	if before.PID == "" || before.PID != afterAdd.PID || before.PID != afterRemove.PID {
		t.Fatalf("VM PID changed: %q, %q, %q", before.PID, afterAdd.PID, afterRemove.PID)
	}
	if !reflect.DeepEqual(removed, before.Forwards) || !reflect.DeepEqual(removed, afterRemove.Forwards) {
		t.Fatalf("unexpected mappings after removal: response=%v state=%v", removed, afterRemove.Forwards)
	}
	assertSSHBanner(t, before.Forwards["existing"].HostPort)
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", added["live"].HostPort), time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("removed forward still accepts connections")
	}
	cmd := exec.Command(f.BinaryPath, "health-check")
	cmd.Dir = f.WorkDir
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var health struct {
		Ports map[string]struct {
			State string `json:"state"`
		} `json:"ports"`
	}
	if err := json.Unmarshal(output, &health); err != nil {
		t.Fatal(err)
	}
	if len(health.Ports) != 1 || health.Ports["existing"].State != "reachable" {
		t.Fatalf("unexpected health after removal: %s", output)
	}
}

func reserveFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve host port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("failed to release reserved host port: %v", err)
	}
	return port
}

func assertSSHBanner(t *testing.T, port int) {
	t.Helper()
	connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 10*time.Second)
	if err != nil {
		t.Fatalf("failed to dial forwarded port %d: %v", port, err)
	}
	defer connection.Close()
	if err := connection.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("failed to set read deadline: %v", err)
	}
	banner := make([]byte, 256)
	read, err := connection.Read(banner)
	if err != nil {
		t.Fatalf("failed to read SSH banner from port %d: %v", port, err)
	}
	if !strings.HasPrefix(string(banner[:read]), "SSH-") {
		t.Fatalf("unexpected banner on port %d: %q", port, banner[:read])
	}
}
