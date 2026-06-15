// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
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
