// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin

package integration

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

type providerState struct {
	SchemaVersion int    `json:"schemaVersion"`
	Phase         string `json:"phase"`
	Forwards      []struct {
		Name     string `json:"name"`
		HostPort int    `json:"hostPort"`
		Health   string `json:"health"`
	} `json:"forwards"`
	Hook struct {
		Phase string `json:"phase"`
	} `json:"hook"`
}

func TestGenericHookAndTCPEchoService(t *testing.T) {
	// Given
	fixture := newProviderFixture(t)
	defer fixture.cleanup()

	// When
	fixture.run("init", "--state-dir", fixture.stateDir, "--config", fixture.configPath)
	fixture.run("start", "--state-dir", fixture.stateDir, "--config", fixture.configPath)

	// Then
	state := fixture.status("health-check")
	if state.SchemaVersion != 1 || state.Phase != "running" {
		t.Fatalf("unexpected provider state: %#v", state)
	}
	if state.Hook.Phase != "succeeded" {
		t.Fatalf("expected successful generic hook, got %#v", state.Hook)
	}
	echoPort := forwardPort(t, state, "echo")
	connection, err := net.DialTimeout(
		"tcp",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(echoPort)),
		5*time.Second,
	)
	if err != nil {
		t.Fatalf("failed to reach generic TCP echo service: %v", err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 6)
	if _, err := io.ReadFull(connection, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "hello\n" {
		t.Fatalf("unexpected echo response %q", response)
	}
}

func TestDestroyPreservesCallerOwnedPaths(t *testing.T) {
	// Given
	fixture := newProviderFixture(t)
	defer fixture.cleanup()
	fixture.run("init", "--state-dir", fixture.stateDir, "--config", fixture.configPath)
	fixture.run("start", "--state-dir", fixture.stateDir, "--config", fixture.configPath)
	callerMarker := filepath.Join(fixture.controlDir, "caller-owned")
	if err := os.WriteFile(callerMarker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}

	// When
	fixture.run("destroy", "--state-dir", fixture.stateDir)

	// Then
	if _, err := os.Stat(fixture.stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected provider state to be removed, got %v", err)
	}
	if data, err := os.ReadFile(callerMarker); err != nil || string(data) != "preserve" {
		t.Fatalf("caller share was not preserved: %q, %v", data, err)
	}
	if _, err := os.Stat(fixture.runtimeDisk); err != nil {
		t.Fatalf("caller runtime disk was not preserved: %v", err)
	}
}

type providerFixture struct {
	t           *testing.T
	root        string
	binary      string
	stateDir    string
	controlDir  string
	configPath  string
	runtimeDisk string
	running     bool
}

func newProviderFixture(t *testing.T) *providerFixture {
	t.Helper()
	zipPath := os.Getenv("PROVIDER_ZIP")
	if zipPath == "" {
		zipPath = "../dist/local-vm-darwin-arm64.zip"
	}
	if _, err := os.Stat(zipPath); err != nil {
		t.Skipf("provider artifact not found at %s", zipPath)
	}
	root := t.TempDir()
	if err := unzip(zipPath, root); err != nil {
		t.Fatal(err)
	}
	control := filepath.Join(root, "control")
	if err := os.Mkdir(control, 0o750); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(control, "hooks")
	if err := os.Mkdir(hooks, 0o750); err != nil {
		t.Fatal(err)
	}
	hook := []byte(`#!/bin/sh
set -eu
touch /mnt/control/hook-ran
while true; do nc -l -p 9000 -e /bin/cat; done >/mnt/control/echo.log 2>&1 &
`)
	if err := os.WriteFile(filepath.Join(hooks, "start"), hook, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := &providerFixture{
		t:           t,
		root:        root,
		binary:      filepath.Join(root, "local-vm"),
		stateDir:    filepath.Join(root, "provider-state"),
		controlDir:  control,
		configPath:  filepath.Join(root, "config.json"),
		runtimeDisk: filepath.Join(root, "caller-runtime.img"),
	}
	config := map[string]any{
		"schemaVersion": 1,
		"resources":     map[string]any{"cpus": 2, "memoryMiB": 4096},
		"shares": []map[string]any{{
			"name": "control", "hostPath": control, "guestPath": "/mnt/control", "readOnly": false,
		}},
		"forwards": []map[string]any{
			{
				"name": "ssh", "protocol": "tcp", "hostAddress": "127.0.0.1",
				"hostPort": 0, "guestPort": 22,
			},
			{
				"name": "echo", "protocol": "tcp", "hostAddress": "127.0.0.1",
				"hostPort": 0, "guestPort": 9000,
			},
		},
		"bootHook":    map[string]any{"apiVersion": 1, "share": "control", "path": "hooks/start"},
		"runtimeDisk": map[string]any{"hostPath": fixture.runtimeDisk, "initialSizeGiB": 4},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture *providerFixture) run(args ...string) {
	fixture.t.Helper()
	command := exec.Command(fixture.binary, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		fixture.t.Fatalf("provider %v failed: %v", args, err)
	}
	if len(args) != 0 && args[0] == "start" {
		fixture.running = true
	}
}

func (fixture *providerFixture) status(command string) providerState {
	fixture.t.Helper()
	output, err := exec.Command(
		fixture.binary,
		command,
		"--state-dir",
		fixture.stateDir,
		"--json",
	).Output()
	if err != nil {
		fixture.t.Fatal(err)
	}
	var state providerState
	if err := json.Unmarshal(output, &state); err != nil {
		fixture.t.Fatal(err)
	}
	return state
}

func (fixture *providerFixture) cleanup() {
	if fixture.running {
		_ = exec.Command(fixture.binary, "stop", "--state-dir", fixture.stateDir).Run()
	}
}

func forwardPort(t *testing.T, state providerState, name string) int {
	t.Helper()
	for _, forward := range state.Forwards {
		if forward.Name == name {
			return forward.HostPort
		}
	}
	t.Fatalf("forward %q not found in %#v", name, state.Forwards)
	return 0
}

func unzip(source, target string) error {
	reader, err := zip.OpenReader(source)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, entry := range reader.File {
		path := filepath.Join(target, entry.Name)
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(path, entry.Mode()); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		input, err := entry.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, entry.Mode())
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
