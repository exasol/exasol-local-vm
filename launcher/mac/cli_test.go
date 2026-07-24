// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
