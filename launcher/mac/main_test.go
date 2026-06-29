// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAuthorizedKeyFromPrivateKeyMatchesGeneratedPublicKey(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	privateKeyPath := filepath.Join(tempDir, "id_ed25519")
	generatedAuthorizedKeysPath := filepath.Join(tempDir, "generated_authorized_keys")
	importedAuthorizedKeysPath := filepath.Join(tempDir, "imported_authorized_keys")

	if err := generateSSHKeyPair(privateKeyPath, generatedAuthorizedKeysPath); err != nil {
		t.Fatalf("generateSSHKeyPair() error = %v", err)
	}

	authorizedKey, err := authorizedKeyFromPrivateKey(privateKeyPath)
	if err != nil {
		t.Fatalf("authorizedKeyFromPrivateKey() error = %v", err)
	}
	if err := os.WriteFile(importedAuthorizedKeysPath, authorizedKey, 0644); err != nil {
		t.Fatalf("failed to write imported authorized key: %v", err)
	}

	generatedAuthorizedKey, err := os.ReadFile(generatedAuthorizedKeysPath)
	if err != nil {
		t.Fatalf("failed to read generated authorized key: %v", err)
	}
	importedAuthorizedKey, err := os.ReadFile(importedAuthorizedKeysPath)
	if err != nil {
		t.Fatalf("failed to read imported authorized key: %v", err)
	}
	if string(importedAuthorizedKey) != string(generatedAuthorizedKey) {
		t.Fatalf("imported authorized key does not match generated public key")
	}
}

func TestVersionCheckRuntimeConfigFromOptionsUsesRunnerContract(t *testing.T) {
	config := versionCheckRuntimeConfigFromOptions(VersionCheckOptions{
		Enabled:         false,
		IntervalSeconds: 42,
		Identity:        "exasol-personal;deployment;small;default",
		URL:             "https://metrics.example.test/v1/version-check",
	}, versionCheckHostOSMacOS, "amd64")

	if config.Enabled {
		t.Fatal("expected version checks to be disabled")
	}
	if config.IntervalSeconds != 42 {
		t.Fatalf("expected interval 42, got %d", config.IntervalSeconds)
	}
	if config.Identity != "exasol-personal;deployment;small;default" {
		t.Fatalf("unexpected identity: %q", config.Identity)
	}
	if config.URL != "https://metrics.example.test/v1/version-check" {
		t.Fatalf("unexpected URL: %q", config.URL)
	}
	if config.OperatingSystem != "MacOS" {
		t.Fatalf("unexpected operating system: %q", config.OperatingSystem)
	}
	if config.Architecture != "x86_64" {
		t.Fatalf("unexpected architecture: %q", config.Architecture)
	}
}

func TestVersionCheckRuntimeConfigFromOptionsDefaults(t *testing.T) {
	config := versionCheckRuntimeConfigFromOptions(VersionCheckOptions{
		Enabled: true,
	}, versionCheckHostOSMacOS, "aarch64")

	if !config.Enabled {
		t.Fatal("expected version checks to be enabled")
	}
	if config.IntervalSeconds != defaultVersionCheckIntervalSeconds {
		t.Fatalf("expected default interval %d, got %d", defaultVersionCheckIntervalSeconds, config.IntervalSeconds)
	}
	if config.Identity != defaultVersionCheckIdentity {
		t.Fatalf("expected default identity %q, got %q", defaultVersionCheckIdentity, config.Identity)
	}
	if config.URL != defaultVersionCheckURL {
		t.Fatalf("expected default URL %q, got %q", defaultVersionCheckURL, config.URL)
	}
	if config.Architecture != "arm64" {
		t.Fatalf("unexpected architecture: %q", config.Architecture)
	}
}

func TestWriteVersionCheckRuntimeConfig(t *testing.T) {
	tempDir := t.TempDir()
	config := VersionCheckRuntimeConfig{
		Enabled:         true,
		IntervalSeconds: 7,
		Identity:        "NONE",
		URL:             "https://metrics.example.test/v1/version-check",
		OperatingSystem: "MacOS",
		Architecture:    "arm64",
	}

	if err := writeVersionCheckRuntimeConfig(tempDir, config); err != nil {
		t.Fatalf("writeVersionCheckRuntimeConfig() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tempDir, versionCheckRuntimeConfigName))
	if err != nil {
		t.Fatalf("failed to read version-check runtime config: %v", err)
	}

	var decoded VersionCheckRuntimeConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to parse version-check runtime config: %v", err)
	}
	if decoded != config {
		t.Fatalf("decoded config mismatch: got %+v, want %+v", decoded, config)
	}
}
