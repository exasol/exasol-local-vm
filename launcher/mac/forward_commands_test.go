// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestParseForwardCommand(t *testing.T) {
	// Given
	cases := []struct {
		name  string
		args  []string
		valid bool
	}{
		{"add", []string{"add", "service:80:0"}, true},
		{"IPv6 loopback", []string{"add", "--host-ip", "::1", "service:80:8000"}, true},
		{"remove", []string{"remove", "service"}, true},
		{"list", []string{"list"}, true},
		{"missing command", nil, false},
		{"unknown command", []string{"replace"}, false},
		{"missing mapping", []string{"add"}, false},
		{"invalid port", []string{"add", "service:0:8000"}, false},
		{"public binding", []string{"add", "--host-ip", "0.0.0.0", "service:80:0"}, false},
		{"missing name", []string{"remove"}, false},
		{"invalid name", []string{"remove", "a/b"}, false},
		{"extra argument", []string{"list", "service"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When
			_, err := parseForwardCommand(tc.args)
			// Then
			if (err == nil) != tc.valid {
				t.Fatalf("parse %v: %v", tc.args, err)
			}
		})
	}
}

func setupLiveForwards(t *testing.T) int {
	t.Helper()
	t.Chdir(t.TempDir())
	if err := os.WriteFile("vm-state.json", []byte(`{"pid":"123","forwards":{},"shared_dir":"./vm-shared"}`), 0644); err != nil {
		t.Fatal(err)
	}
	forwarderRegistry = make(map[string]*LoopbackForwarder)
	forwardGuestHost = "127.0.0.1"
	forwardsReady = true
	forwardsStopping = false
	t.Cleanup(closeForwarders)
	guest, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { guest.Close() })
	go func() {
		for {
			conn, err := guest.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); io.Copy(conn, conn) }()
		}
	}()
	return guest.Addr().(*net.TCPAddr).Port
}

func addTestForward(t *testing.T, name string, guestPort int) ForwardState {
	t.Helper()
	response := handleForwardRequest(forwardRequest{Request: "forward-add", Name: name, GuestPort: guestPort})
	if response.Error != "" {
		t.Fatal(response.Error)
	}
	return response.Forwards[name]
}

func TestLiveForwardsAreIdempotentAndPublishState(t *testing.T) {
	// Given
	port := setupLiveForwards(t)
	first := addTestForward(t, "service", port)
	// When
	second := addTestForward(t, "service", port)
	listed := handleForwardRequest(forwardRequest{Request: "forward-list"})
	data, err := os.ReadFile("vm-state.json")
	// Then
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		PID       string                  `json:"pid"`
		SharedDir string                  `json:"shared_dir"`
		Forwards  map[string]ForwardState `json:"forwards"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if first != second || first.HostPort == 0 || first.HostIP != "127.0.0.1" {
		t.Fatalf("mappings changed: %+v, %+v", first, second)
	}
	if state.PID != "123" || state.SharedDir != "./vm-shared" || !reflect.DeepEqual(state.Forwards, listed.Forwards) {
		t.Fatalf("inconsistent state: %s, response %+v", data, listed)
	}
}

func TestLiveForwardRejectsConflictsAndUnsafeSocketRequests(t *testing.T) {
	// Given
	port := setupLiveForwards(t)
	initial := addTestForward(t, "service", port)
	cases := []forwardRequest{
		{Request: "forward-add", Name: "service", GuestPort: port, HostPort: initial.HostPort},
		{Request: "forward-add", Name: "public", GuestPort: port, HostIP: "0.0.0.0"},
		{Request: "forward-add", Name: "bad", GuestPort: 65536},
		{Request: "forward-add", Name: "bad/name", GuestPort: port},
		{Request: "forward-add", Name: "occupied", GuestPort: port, HostPort: initial.HostPort},
	}
	for _, request := range cases {
		// When
		response := handleForwardRequest(request)
		// Then
		if response.Error == "" {
			t.Fatalf("accepted %+v", request)
		}
	}
	if got := handleForwardRequest(forwardRequest{Request: "forward-list"}).Forwards; len(got) != 1 || got["service"] != initial {
		t.Fatalf("failed requests changed mappings: %+v", got)
	}
}

func TestRemoveClosesActiveConnectionsAndPreservesOtherForwards(t *testing.T) {
	// Given
	port := setupLiveForwards(t)
	removed := addTestForward(t, "removed", port)
	kept := addTestForward(t, "kept", port)
	dial := func(state ForwardState) net.Conn {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", state.HostPort), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		return conn
	}
	echo := func(conn net.Conn) {
		if _, err := conn.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		var result [1]byte
		if _, err := io.ReadFull(conn, result[:]); err != nil {
			t.Fatal(err)
		}
		if result[0] != 'x' {
			t.Fatalf("echo = %q", result)
		}
	}
	removedConn, keptConn := dial(removed), dial(kept)
	echo(removedConn)
	echo(keptConn)
	// When
	for range 2 {
		response := handleForwardRequest(forwardRequest{Request: "forward-remove", Name: "removed"})
		if response.Error != "" {
			t.Fatal(response.Error)
		}
	}
	// Then
	var data [1]byte
	if _, err := removedConn.Read(data[:]); err == nil {
		t.Fatal("removed connection remains open")
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("removal did not close the active connection")
	}
	echo(keptConn)
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", removed.HostPort), time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("removed listener remains open")
	}
	if got := forwarderSnapshot(); len(got) != 1 || got["kept"] == nil {
		t.Fatalf("registry: %+v", got)
	}
	if health := probeForwarders(context.Background()); len(health) != 1 || health["kept"].State != "reachable" {
		t.Fatalf("health after removal: %+v", health)
	}
}

func TestStateWriteFailureRollsBackLiveMutations(t *testing.T) {
	// Given
	port := setupLiveForwards(t)
	initial := addTestForward(t, "service", port)
	if err := os.Rename("vm-state.json", "saved-state.json"); err != nil {
		t.Fatal(err)
	}
	// When
	add := handleForwardRequest(forwardRequest{Request: "forward-add", Name: "new", GuestPort: port})
	remove := handleForwardRequest(forwardRequest{Request: "forward-remove", Name: "service"})
	listed := handleForwardRequest(forwardRequest{Request: "forward-list"})
	// Then
	if add.Error == "" || remove.Error == "" || len(listed.Forwards) != 1 || listed.Forwards["service"] != initial {
		t.Fatalf("failed rollback: add=%+v remove=%+v list=%+v", add, remove, listed)
	}
}

func TestForwardRequestsRequireReadyVM(t *testing.T) {
	// Given
	port := setupLiveForwards(t)
	forwardsReady = false
	// When
	for _, request := range []forwardRequest{
		{Request: "forward-add", Name: "service", GuestPort: port},
		{Request: "forward-remove", Name: "service"},
		{Request: "forward-list"},
	} {
		response := handleForwardRequest(request)
		// Then
		if response.Error == "" {
			t.Fatalf("accepted %+v before readiness", request)
		}
	}
}

func TestConcurrentForwardMutations(t *testing.T) {
	// Given
	port := setupLiveForwards(t)
	var wg sync.WaitGroup
	// When
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("service-%d", i)
			for _, request := range []forwardRequest{
				{Request: "forward-add", Name: name, GuestPort: port},
				{Request: "forward-list"},
				{Request: "forward-remove", Name: name},
			} {
				if response := handleForwardRequest(request); response.Error != "" {
					t.Error(response.Error)
				}
			}
		}()
	}
	wg.Wait()
	// Then
	if got := handleForwardRequest(forwardRequest{Request: "forward-list"}); len(got.Forwards) != 0 || got.Error != "" {
		t.Fatalf("final state: %+v", got)
	}
}

func TestForwardCommandUsesControlSocket(t *testing.T) {
	// Given
	port := setupLiveForwards(t)
	listener, err := net.Listen("unix", vmSocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	served := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(time.Second))
		var request forwardRequest
		if err := json.NewDecoder(conn).Decode(&request); err != nil {
			served <- err
			return
		}
		served <- json.NewEncoder(conn).Encode(handleForwardRequest(request))
	}()
	var output bytes.Buffer
	// When
	err = forwardCmd([]string{"add", fmt.Sprintf("service:%d:0", port)}, &output)
	// Then
	if err != nil {
		t.Fatal(err)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	var response forwardResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != "" || response.Forwards["service"].HostPort == 0 {
		t.Fatalf("command output: %s", &output)
	}
}

func TestForwardCommandRequiresRunningDaemon(t *testing.T) {
	// Given
	t.Chdir(t.TempDir())
	// When
	err := forwardCmd([]string{"list"}, io.Discard)
	// Then
	if err == nil {
		t.Fatal("command succeeded without a daemon")
	}
	if _, err := os.Stat("vm-state.json"); !os.IsNotExist(err) {
		t.Fatalf("unexpected VM state: %v", err)
	}
}
