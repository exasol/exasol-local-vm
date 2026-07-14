// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"
)

// buildTestTarXZ produces an in-memory tar.xz archive matching the layout
// build-windows-launcher.sh writes (all entries rooted at init/).
func buildTestTarXZ(t *testing.T, files map[string][]byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	xzWriter, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatalf("xz.NewWriter: %v", err)
	}
	tw := tar.NewWriter(xzWriter)
	// Write the top-level directory entry, which extractTarXZ must skip.
	if err := tw.WriteHeader(&tar.Header{
		Name:     "init/",
		Mode:     0755,
		Typeflag: tar.TypeDir,
	}); err != nil {
		t.Fatalf("write dir header: %v", err)
	}
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(data)),
		}); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("write body %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := xzWriter.Close(); err != nil {
		t.Fatalf("xz close: %v", err)
	}
	return buf.Bytes()
}

// TestInitCmdRejectsSSHKey verifies the --ssh-key flag is rejected with
// exit code 2 and the documented message, before any filesystem work is
// attempted (so the test does not need an isolated working directory).
func TestInitCmdRejectsSSHKey(t *testing.T) {
	t.Parallel()

	err := initCmdWithAssets("/path/to/some/key", nil)
	if err == nil {
		t.Fatal("expected error rejecting --ssh-key, got nil")
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *exitError, got %T (%v)", err, err)
	}
	if ee.code != 2 {
		t.Errorf("expected exit code 2, got %d", ee.code)
	}
	want := "--ssh-key is not supported on windows: there is no guest VM to SSH into"
	if ee.Error() != want {
		t.Errorf("expected message %q, got %q", want, ee.Error())
	}
}

// TestInitCmdExtractsAssetsAndWritesConfig verifies that a successful init
// produces resources/config.json, resources/exasol-nano-db.tar.gz, and
// vm-config.json — the on-disk contract the later phases rely on.
func TestInitCmdExtractsAssetsAndWritesConfig(t *testing.T) {
	wantConfig := []byte(`{"db":{"container_name":"test-container","ports":{"db":8563}}}`)
	wantTarball := []byte("stand-in bytes for exasol-nano-db.tar.gz")

	assets := buildTestTarXZ(t, map[string][]byte{
		"init/config.json":           wantConfig,
		"init/exasol-nano-db.tar.gz": wantTarball,
	})

	t.Chdir(t.TempDir())

	if err := initCmdWithAssets("", assets); err != nil {
		t.Fatalf("initCmdWithAssets: %v", err)
	}

	// Extracted assets land under resources/ with the init/ prefix stripped.
	gotConfig, err := os.ReadFile(filepath.Join(resourcesDir, "config.json"))
	if err != nil {
		t.Fatalf("read extracted config.json: %v", err)
	}
	if !bytes.Equal(gotConfig, wantConfig) {
		t.Errorf("extracted config.json mismatch:\n want: %s\n got:  %s", wantConfig, gotConfig)
	}
	gotTarball, err := os.ReadFile(filepath.Join(resourcesDir, "exasol-nano-db.tar.gz"))
	if err != nil {
		t.Fatalf("read extracted tarball: %v", err)
	}
	if !bytes.Equal(gotTarball, wantTarball) {
		t.Errorf("extracted tarball mismatch")
	}

	// vm-config.json must exist and contain an empty ssh_private_key field
	// (windows has no guest VM to SSH into).
	rawRuntimeConfig, err := os.ReadFile(runtimeConfigPath)
	if err != nil {
		t.Fatalf("read %s: %v", runtimeConfigPath, err)
	}
	var runtimeConfig RuntimeConfig
	if err := json.Unmarshal(rawRuntimeConfig, &runtimeConfig); err != nil {
		t.Fatalf("parse %s: %v", runtimeConfigPath, err)
	}
	if runtimeConfig.SSHPrivateKey != "" {
		t.Errorf("expected empty SSHPrivateKey, got %q", runtimeConfig.SSHPrivateKey)
	}
	// The JSON must also carry the explicit key so downstream consumers can
	// distinguish "unset" from "missing field".
	if !strings.Contains(string(rawRuntimeConfig), `"ssh_private_key"`) {
		t.Errorf("%s missing ssh_private_key field: %s", runtimeConfigPath, rawRuntimeConfig)
	}
}

// TestInitCmdIsIdempotent verifies a second init call on top of a populated
// working directory succeeds and leaves the assets in a good state
// (needed so users can safely re-run init to refresh embedded resources).
func TestInitCmdIsIdempotent(t *testing.T) {
	assets := buildTestTarXZ(t, map[string][]byte{
		"init/config.json":           []byte(`{"first":true}`),
		"init/exasol-nano-db.tar.gz": []byte("first payload"),
	})

	t.Chdir(t.TempDir())

	if err := initCmdWithAssets("", assets); err != nil {
		t.Fatalf("first init: %v", err)
	}
	if err := initCmdWithAssets("", assets); err != nil {
		t.Fatalf("second init: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(resourcesDir, "config.json"))
	if err != nil {
		t.Fatalf("read config.json after second init: %v", err)
	}
	if string(got) != `{"first":true}` {
		t.Errorf("config.json content diverged after second init: %s", got)
	}
}
