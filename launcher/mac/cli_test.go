// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHealthCheckConfigTracksProviderHealth(t *testing.T) {
	tests := []struct {
		name            string
		initialPhase    VMPhase
		failHealthQuery bool
		expectedPhase   VMPhase
		expectedMessage string
	}{
		{
			name:            "query failure degrades provider",
			initialPhase:    VMPhaseRunning,
			failHealthQuery: true,
			expectedPhase:   VMPhaseDegraded,
			expectedMessage: "failed to query provider health",
		},
		{
			name:          "successful query recovers provider",
			initialPhase:  VMPhaseDegraded,
			expectedPhase: VMPhaseRunning,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stateDir := t.TempDir()
			previous, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(stateDir); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chdir(previous) })

			if err := writeProviderState(&ProviderState{
				Phase:   test.initialPhase,
				Message: "previous provider health error",
				Hook:    HookState{Phase: HookPhaseSucceeded},
			}); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", vmSocketPath)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				for requestIndex := 0; requestIndex < 2; requestIndex++ {
					connection, acceptErr := listener.Accept()
					if acceptErr != nil {
						return
					}
					var request struct {
						Request string `json:"request"`
					}
					if decodeErr := json.NewDecoder(connection).Decode(&request); decodeErr != nil {
						_ = connection.Close()
						return
					}
					switch request.Request {
					case "status":
						_ = json.NewEncoder(connection).Encode(map[string]string{
							"status": "running",
						})
					case "health-check":
						if !test.failHealthQuery {
							_ = json.NewEncoder(connection).Encode(map[string]any{
								"ports": map[string]portHealthResponse{},
							})
						}
					}
					_ = connection.Close()
				}
			}()

			var output bytes.Buffer
			if err := healthCheckConfigCmd(&output); err != nil {
				t.Fatal(err)
			}
			<-done
			var state ProviderState
			if err := json.Unmarshal(output.Bytes(), &state); err != nil {
				t.Fatal(err)
			}
			messageMatches := state.Message == test.expectedMessage
			if test.expectedMessage != "" {
				messageMatches = strings.Contains(state.Message, test.expectedMessage)
			}
			if state.Phase != test.expectedPhase || !messageMatches {
				t.Fatalf("unexpected provider health state: %#v", state)
			}
		})
	}
}

//nolint:paralleltest // The provider contract uses the process working directory.
func TestInitConfigUpgradesPreContractStateWithoutReplacingProviderDisk(t *testing.T) {
	// Given
	stateDir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(stateDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	if err := os.Mkdir("vm", 0o750); err != nil {
		t.Fatal(err)
	}
	diskPath := filepath.Join("vm", "data.img")
	if err := os.WriteFile(diskPath, []byte("legacy-var"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := &VMConfig{SchemaVersion: configSchemaVersion}
	var preserveProviderDisk bool

	// When
	err = initConfigWith(config, func(preserve bool) error {
		preserveProviderDisk = preserve
		return nil
	})

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if !preserveProviderDisk {
		t.Fatal("pre-contract initialization did not request provider-disk preservation")
	}
	if data, readErr := os.ReadFile(diskPath); readErr != nil || string(data) != "legacy-var" {
		t.Fatalf("provider disk changed: data=%q err=%v", data, readErr)
	}
	if err := validateStateOwnership("test"); err != nil {
		t.Fatalf("provider ownership contract was not written: %v", err)
	}
}

func TestRunBootHookRecordsSuccessAndFailure(t *testing.T) {
	tests := []struct {
		name      string
		exitCode  int
		wantPhase HookPhase
		wantError bool
	}{
		{name: "success", exitCode: 0, wantPhase: HookPhaseSucceeded},
		{name: "failure", exitCode: 7, wantPhase: HookPhaseFailed, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stateDir := t.TempDir()
			binDir := filepath.Join(stateDir, "bin")
			if err := os.Mkdir(binDir, 0o750); err != nil {
				t.Fatal(err)
			}
			fakeSSH := filepath.Join(binDir, "ssh")
			script := fmt.Sprintf("#!/bin/sh\nprintf 'hook-output'\nexit %d\n", test.exitCode)
			if err := os.WriteFile(fakeSSH, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			previous, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(stateDir); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chdir(previous) })

			if err := writeProviderState(&ProviderState{
				Phase:          VMPhaseRunning,
				SSH:            &EndpointState{Address: "127.0.0.1", Port: 20022},
				PrivateKeyPath: filepath.Join(stateDir, "id_ed25519"),
				Hook:           HookState{Phase: HookPhaseNone},
			}); err != nil {
				t.Fatal(err)
			}
			config := &VMConfig{
				Shares: []Share{{
					Name:      "control",
					GuestPath: "/mnt/control",
				}},
				BootHook: &BootHook{Share: "control", Path: "hooks/start"},
			}

			err = runBootHook(config)
			if (err != nil) != test.wantError {
				t.Fatalf("runBootHook() error = %v, wantError %t", err, test.wantError)
			}
			state, readErr := readProviderState()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if state.Hook.Phase != test.wantPhase || state.Hook.Attempt != 1 {
				t.Fatalf("unexpected hook state: %#v", state.Hook)
			}
			if state.Hook.ExitCode == nil || *state.Hook.ExitCode != test.exitCode {
				t.Fatalf("hook exit code = %v, want %d", state.Hook.ExitCode, test.exitCode)
			}
			log, readErr := os.ReadFile(hookLogFileName)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(log) != "hook-output" {
				t.Fatalf("hook log = %q, want hook-output", log)
			}
		})
	}
}

func TestRunCLIVersionReportsIndependentContractVersions(t *testing.T) {
	// Given
	var stdout bytes.Buffer

	// When
	err := runCLI([]string{"version", "--json"}, &stdout, &bytes.Buffer{})

	// Then
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["configSchemaVersion"] != float64(configSchemaVersion) ||
		result["hookAPIVersion"] != float64(hookAPIVersion) ||
		result["stateSchemaVersion"] != float64(stateSchemaVersion) {
		t.Fatalf("unexpected version contract: %#v", result)
	}
}

func TestRunCLIStatusReportsStoppedForAbsentCanonicalState(t *testing.T) {
	// Given
	stateDir := filepath.Join(t.TempDir(), "absent")
	var stdout bytes.Buffer

	// When
	err := runCLI(
		[]string{"status", "--state-dir", stateDir, "--json"},
		&stdout,
		&bytes.Buffer{},
	)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	var state ProviderState
	if err := json.Unmarshal(stdout.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Phase != VMPhaseStopped || state.SchemaVersion != stateSchemaVersion {
		t.Fatalf("unexpected stopped state: %#v", state)
	}
}

func TestRunCLIStatusRejectsNonCanonicalStatePath(t *testing.T) {
	// When
	err := runCLI(
		[]string{"status", "--state-dir", "relative", "--json"},
		&bytes.Buffer{},
		&bytes.Buffer{},
	)

	// Then
	if err == nil || !strings.Contains(err.Error(), "absolute canonical") {
		t.Fatalf("expected path rejection, got %v", err)
	}
}

func TestRunCLIStopIsIdempotentForAbsentCanonicalState(t *testing.T) {
	// Given
	stateDir := filepath.Join(t.TempDir(), "absent")

	// When
	err := runCLI(
		[]string{"stop", "--state-dir", stateDir},
		&bytes.Buffer{},
		&bytes.Buffer{},
	)

	// Then
	if err != nil {
		t.Fatalf("expected absent provider stop to succeed, got %v", err)
	}
}

func TestCanonicalStateDirRejectsSymlinkAncestorBeforeCreating(t *testing.T) {
	// Given
	root := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(root, "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(link, "provider-state")

	// When
	_, err := canonicalStateDir(stateDir, true)

	// Then
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink ancestor rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(target, "provider-state")); !os.IsNotExist(statErr) {
		t.Fatalf("state directory was created through symlink before rejection: %v", statErr)
	}
}

func TestStopRefusesDirectoryWithoutProviderOwnershipMetadata(t *testing.T) {
	// Given
	stateDir := t.TempDir()

	// When
	err := runCLI(
		[]string{"stop", "--state-dir", stateDir},
		&bytes.Buffer{},
		&bytes.Buffer{},
	)

	// Then
	if err == nil || !strings.Contains(err.Error(), "refusing to stop") {
		t.Fatalf("expected ownership rejection, got %v", err)
	}
}

func TestDestroyRefusesDirectoryWithoutProviderOwnershipMetadata(t *testing.T) {
	// Given
	stateDir := t.TempDir()
	marker := filepath.Join(stateDir, "caller-owned")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(stateDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	// When
	err = destroyConfigCmd(stateDir)

	// Then
	if err == nil || !strings.Contains(err.Error(), "refusing to destroy") {
		t.Fatalf("expected ownership rejection, got %v", err)
	}
	if data, readErr := os.ReadFile(marker); readErr != nil || string(data) != "preserve" {
		t.Fatalf("caller directory was modified: data=%q err=%v", data, readErr)
	}
}
