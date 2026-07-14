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

// TestLoadContainerConfigParsesFixture verifies loadContainerConfig round-
// trips the exact schema shipped in launcher/assets/windows/init/config.json.
// Fixture is duplicated inline (rather than reading the on-disk file) so
// the test is hermetic and does not depend on prior asset staging.
func TestLoadContainerConfigParsesFixture(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	fixture := []byte(`{
  "db": {
    "container_name": "exasol-local-db",
    "tarball_name": "exasol-nano-db.tar.gz",
    "ports": { "db": 8563 },
    "shm_size": "512mb",
    "pids_limit": "-1",
    "security_opt": "unmask=ALL",
    "restart": "always",
    "params": ["maxConnectionsLicenseLimit=20"]
  }
}`)
	if err := os.WriteFile(configPath, fixture, 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cfg, err := loadContainerConfig(configPath)
	if err != nil {
		t.Fatalf("loadContainerConfig: %v", err)
	}
	if cfg.ContainerName != "exasol-local-db" {
		t.Errorf("ContainerName: got %q", cfg.ContainerName)
	}
	if cfg.TarballName != "exasol-nano-db.tar.gz" {
		t.Errorf("TarballName: got %q", cfg.TarballName)
	}
	if cfg.Ports["db"] != 8563 {
		t.Errorf("Ports[\"db\"]: got %d", cfg.Ports["db"])
	}
	if cfg.ShmSize != "512mb" {
		t.Errorf("ShmSize: got %q", cfg.ShmSize)
	}
	if cfg.PidsLimit != "-1" {
		t.Errorf("PidsLimit: got %q", cfg.PidsLimit)
	}
	if cfg.SecurityOpt != "unmask=ALL" {
		t.Errorf("SecurityOpt: got %q", cfg.SecurityOpt)
	}
	if cfg.Restart != "always" {
		t.Errorf("Restart: got %q", cfg.Restart)
	}
	if len(cfg.Params) != 1 || cfg.Params[0] != "maxConnectionsLicenseLimit=20" {
		t.Errorf("Params: got %#v", cfg.Params)
	}
}

func TestLoadContainerConfigInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("not json"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := loadContainerConfig(path); err == nil {
		t.Fatal("expected parse error on invalid JSON")
	}
}

// TestBuildPodmanRunArgsExactArgv locks in the argv shape for the
// canonical Phase 1 fixture config: any drift here is a
// user-observable behavior change (podman flag order, added/removed
// options) and should be intentional.
func TestBuildPodmanRunArgsExactArgv(t *testing.T) {
	cfg := dbContainerConfig{
		ContainerName: "exasol-local-db",
		TarballName:   "exasol-nano-db.tar.gz",
		Ports:         map[string]int{"db": 8563},
		ShmSize:       "512mb",
		PidsLimit:     "-1",
		SecurityOpt:   "unmask=ALL",
		Restart:       "always",
		Params:        []string{"maxConnectionsLicenseLimit=20"},
	}
	got := buildPodmanRunArgs(cfg, "docker.io/exasol/nano:2026.2.0-nano.2", 8563)
	want := []string{
		"run", "-d",
		"--name", "exasol-local-db",
		"--shm-size=512mb",
		"--pids-limit=-1",
		"--security-opt", "unmask=ALL",
		"--restart", "always",
		"-p", "8563:8563",
		"-v", "exasol-nano-data:/exa",
		"docker.io/exasol/nano:2026.2.0-nano.2",
		"init",
		"params=maxConnectionsLicenseLimit=20",
	}
	if !stringsEqual(got, want) {
		t.Errorf("argv mismatch:\n want: %v\n got:  %v", want, got)
	}
}

// TestBuildPodmanRunArgsHostPortMayDifferFromContainerPort verifies the
// hostDBPort argument controls only the left-hand side of -p; the
// container-side port remains what config.json declared. Phase 7 will
// exercise this path with real overrides; Phase 6 already relies on it
// for the (host == container) default case.
func TestBuildPodmanRunArgsHostPortMayDifferFromContainerPort(t *testing.T) {
	cfg := dbContainerConfig{
		ContainerName: "c",
		Ports:         map[string]int{"db": 8563},
		ShmSize:       "1g",
		PidsLimit:     "-1",
		SecurityOpt:   "unmask=ALL",
		Restart:       "always",
	}
	got := buildPodmanRunArgs(cfg, "img:v1", 9090)
	// Find -p and verify the mapping.
	var mapping string
	for i, tok := range got {
		if tok == "-p" && i+1 < len(got) {
			mapping = got[i+1]
			break
		}
	}
	if mapping != "9090:8563" {
		t.Errorf("expected -p 9090:8563 (host:container), got %q", mapping)
	}
}

// TestBuildPodmanRunArgsOmitsParamsWhenEmpty verifies the params= argv
// element is dropped entirely when the config lists no params — the
// Nano container's `init` handler would otherwise see an empty
// `params=` token and complain.
func TestBuildPodmanRunArgsOmitsParamsWhenEmpty(t *testing.T) {
	cfg := dbContainerConfig{
		ContainerName: "c",
		Ports:         map[string]int{"db": 8563},
		ShmSize:       "1g",
		PidsLimit:     "-1",
		SecurityOpt:   "unmask=ALL",
		Restart:       "always",
		// no Params
	}
	got := buildPodmanRunArgs(cfg, "img:v1", 8563)
	for _, tok := range got {
		if strings.HasPrefix(tok, "params=") {
			t.Errorf("did not expect a params= token when config has no params, got argv %v", got)
		}
	}
	// Last token must still be `init`.
	if last := got[len(got)-1]; last != "init" {
		t.Errorf("expected last argv to be 'init', got %q", last)
	}
}

// TestBuildPodmanRunArgsJoinsMultipleParamsWithSpace matches the guest
// shell's `jq '.db.params // [] | join(" ")'` behavior — Nano expects
// a single space-joined value on the right of `params=`.
func TestBuildPodmanRunArgsJoinsMultipleParamsWithSpace(t *testing.T) {
	cfg := dbContainerConfig{
		ContainerName: "c",
		Ports:         map[string]int{"db": 8563},
		ShmSize:       "1g",
		PidsLimit:     "-1",
		SecurityOpt:   "unmask=ALL",
		Restart:       "always",
		Params:        []string{"a=1", "b=2", "c=3"},
	}
	got := buildPodmanRunArgs(cfg, "img:v1", 8563)
	last := got[len(got)-1]
	if last != "params=a=1 b=2 c=3" {
		t.Errorf("expected joined params token, got %q", last)
	}
}

func TestWriteVMStateProducesReadableJSON(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := writeVMState(map[string]int{"db": 9090}); err != nil {
		t.Fatalf("writeVMState: %v", err)
	}
	data, err := os.ReadFile(vmStatePath)
	if err != nil {
		t.Fatalf("read vm-state: %v", err)
	}
	var state VMState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("parse vm-state: %v", err)
	}
	if state.Ports["db"] != 9090 {
		t.Errorf("expected db port 9090, got %d", state.Ports["db"])
	}
}

func stringsEqual(a, b []string) bool {
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
