// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
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
	got := buildPodmanRunArgs(cfg, "docker.io/exasol/nano:2026.2.0-nano.2", map[string]int{"db": 8563})
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
// per-service host port controls only the left-hand side of -p; the
// container-side port remains what config.json declared.
func TestBuildPodmanRunArgsHostPortMayDifferFromContainerPort(t *testing.T) {
	cfg := dbContainerConfig{
		ContainerName: "c",
		Ports:         map[string]int{"db": 8563},
		ShmSize:       "1g",
		PidsLimit:     "-1",
		SecurityOpt:   "unmask=ALL",
		Restart:       "always",
	}
	got := buildPodmanRunArgs(cfg, "img:v1", map[string]int{"db": 9090})
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

// TestBuildPodmanRunArgsMultiPortsAreDeterministic covers the case a
// future config schema adds a second service (e.g. metrics). Service
// ordering must be alphabetical so the argv — and the tests that
// assert on it — stay reproducible.
func TestBuildPodmanRunArgsMultiPortsAreDeterministic(t *testing.T) {
	cfg := dbContainerConfig{
		ContainerName: "c",
		Ports:         map[string]int{"db": 8563, "metrics": 9100},
		ShmSize:       "1g",
		PidsLimit:     "-1",
		SecurityOpt:   "unmask=ALL",
		Restart:       "always",
	}
	got := buildPodmanRunArgs(cfg, "img:v1", map[string]int{"db": 8563, "metrics": 9100})
	// Collect all -p mappings in the order they appear.
	var mappings []string
	for i := 0; i < len(got); i++ {
		if got[i] == "-p" && i+1 < len(got) {
			mappings = append(mappings, got[i+1])
		}
	}
	if !stringsEqual(mappings, []string{"8563:8563", "9100:9100"}) {
		t.Errorf("expected mappings [8563:8563 9100:9100] in alphabetical order, got %v", mappings)
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
	got := buildPodmanRunArgs(cfg, "img:v1", map[string]int{"db": 8563})
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
	got := buildPodmanRunArgs(cfg, "img:v1", map[string]int{"db": 8563})
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

// --- Phase 7: --ports parsing, validation, and selection ---------------

func TestParsePortOverridesHappyPaths(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want map[string]int
	}{
		{"empty", "", map[string]int{}},
		{"whitespace only", "   ", map[string]int{}},
		{"single", "db:9090", map[string]int{"db": 9090}},
		{"multi", "db:9090,ssh:2222", map[string]int{"db": 9090, "ssh": 2222}},
		{"tolerates spaces", " db : 9090 , ssh : 2222 ", map[string]int{"db": 9090, "ssh": 2222}},
		{"skips trailing comma", "db:9090,,", map[string]int{"db": 9090}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePortOverrides(tc.in)
			if err != nil {
				t.Fatalf("parsePortOverrides(%q) error = %v", tc.in, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("size mismatch: want %v, got %v", tc.want, got)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("key %q: want %d, got %d", k, v, got[k])
				}
			}
		})
	}
}

func TestParsePortOverridesErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // substring the error must contain
	}{
		{"missing colon", "db9090", "expected <service>:<port>"},
		{"empty service", ":9090", "empty service name"},
		{"non-numeric port", "db:abc", "invalid port"},
		{"port zero", "db:0", "invalid port"},
		{"port too high", "db:70000", "invalid port"},
		{"negative port", "db:-1", "invalid port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parsePortOverrides(tc.in)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should contain %q, got %v", tc.want, err)
			}
		})
	}
}

func TestValidatePortOverridesUnknownServiceIncludesName(t *testing.T) {
	cfg := dbContainerConfig{Ports: map[string]int{"db": 8563}}
	err := validatePortOverrides(map[string]int{"nonexistent": 9090}, cfg)
	if err == nil {
		t.Fatal("expected error for unknown service")
	}
	// tests/ports_test.go TestPortOverrideFailsForUnknownService asserts
	// the error message contains the offending service name.
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should name the unknown service, got %v", err)
	}
	if !strings.Contains(err.Error(), "db") {
		t.Errorf("error should list known services, got %v", err)
	}
}

func TestValidatePortOverridesAcceptsKnownService(t *testing.T) {
	cfg := dbContainerConfig{Ports: map[string]int{"db": 8563}}
	if err := validatePortOverrides(map[string]int{"db": 9090}, cfg); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := validatePortOverrides(map[string]int{}, cfg); err != nil {
		t.Errorf("empty overrides should validate: %v", err)
	}
}

// freePort returns a currently-unbound TCP port on 127.0.0.1 by asking
// the OS to allocate one and immediately releasing it. There is a
// small race window but the ephemeral port range makes collisions
// extremely unlikely in practice.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// holdPort binds a TCP listener on 127.0.0.1 and registers a cleanup
// that closes it. The port is guaranteed unbindable by anyone else
// for the duration of the test.
func holdPort(t *testing.T) (port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold port: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// TestSelectHostPortExplicitOverrideFree covers Phase 7 verification
// case (1): explicit override to a free port returns exactly that port.
func TestSelectHostPortExplicitOverrideFree(t *testing.T) {
	requested := freePort(t)
	got, err := selectHostPort("db", 8563, map[string]int{"db": requested})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != requested {
		t.Errorf("expected requested port %d, got %d", requested, got)
	}
}

// TestSelectHostPortExplicitOverrideBusy covers Phase 7 verification
// case (2): explicit override to a busy port must fail hard with an
// error naming the service and the fact that --ports supplied it.
func TestSelectHostPortExplicitOverrideBusy(t *testing.T) {
	busy := holdPort(t)
	_, err := selectHostPort("db", 8563, map[string]int{"db": busy})
	if err == nil {
		t.Fatalf("expected error binding busy port %d", busy)
	}
	if !strings.Contains(err.Error(), "db") {
		t.Errorf("error should name the service, got %v", err)
	}
	if !strings.Contains(err.Error(), "--ports") {
		t.Errorf("error should mention --ports, got %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", busy)) {
		t.Errorf("error should name the requested port %d, got %v", busy, err)
	}
}

// TestSelectHostPortDefaultFree covers Phase 7 verification case (3):
// no override, container port free, chosen host port == container port.
func TestSelectHostPortDefaultFree(t *testing.T) {
	containerPort := freePort(t)
	got, err := selectHostPort("db", containerPort, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != containerPort {
		t.Errorf("expected default (container) port %d, got %d", containerPort, got)
	}
}

// TestSelectHostPortDefaultBusyFallsBack covers Phase 7 verification
// case (4): no override, container port busy, chosen host port is a
// different (random) port > 0.
func TestSelectHostPortDefaultBusyFallsBack(t *testing.T) {
	busy := holdPort(t)
	got, err := selectHostPort("db", busy, nil)
	if err != nil {
		t.Fatalf("unexpected fallback error: %v", err)
	}
	if got == busy {
		t.Errorf("expected a fallback different from busy port %d, got %d", busy, got)
	}
	if got <= 0 || got > 65535 {
		t.Errorf("fallback port %d out of range", got)
	}
}

// TestSelectAllHostPortsPopulatesEveryServiceExactlyOnce sanity-checks
// the wrapper that startCmd actually calls.
func TestSelectAllHostPortsPopulatesEveryServiceExactlyOnce(t *testing.T) {
	cfg := dbContainerConfig{Ports: map[string]int{"db": freePort(t), "metrics": freePort(t)}}
	chosen, err := selectAllHostPorts(cfg, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chosen) != 2 {
		t.Fatalf("expected 2 chosen ports, got %v", chosen)
	}
	if chosen["db"] == 0 || chosen["metrics"] == 0 {
		t.Errorf("missing port assignment: %v", chosen)
	}
}

// TestSelectAllHostPortsExplicitBusyAborts ensures a single-service
// failure surfaces immediately rather than being masked by successful
// selections for other services.
func TestSelectAllHostPortsExplicitBusyAborts(t *testing.T) {
	busy := holdPort(t)
	cfg := dbContainerConfig{Ports: map[string]int{"db": freePort(t), "metrics": freePort(t)}}
	_, err := selectAllHostPorts(cfg, map[string]int{"db": busy})
	if err == nil {
		t.Fatal("expected error when explicit db override is busy")
	}
	if !strings.Contains(err.Error(), "db") {
		t.Errorf("error should mention db: %v", err)
	}
}

// --- Phase 8: stop and status ------------------------------------------

// installFakePodman drops a POSIX shell shim named "podman" into a fresh
// t.TempDir() and prepends it to PATH. Mirrors the helper of the same
// name in internal/podman/podman_test.go so main-package tests can drive
// the full stopCmd / statusCmd flow without pulling in the podman
// package's test-only symbols.
func installFakePodman(t *testing.T, body string) (argvLogPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake podman shim uses a POSIX shell script; skipping on windows")
	}

	dir := t.TempDir()
	argvLogPath = filepath.Join(dir, "argv.log")
	script := "#!/bin/sh\n" +
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

// readArgvCalls parses the shim's log into one slice per podman invocation.
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

// writeFixtureResources creates ./resources/config.json in the current
// working directory with the canonical shipping schema. Used by
// stop/status tests that need loadContainerConfig to succeed.
func writeFixtureResources(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(resourcesDir, 0o755); err != nil {
		t.Fatalf("mkdir resources: %v", err)
	}
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
	if err := os.WriteFile(filepath.Join(resourcesDir, "config.json"), fixture, 0o644); err != nil {
		t.Fatalf("write fixture config: %v", err)
	}
}

func TestStopCmdHappyPath(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFixtureResources(t)
	if err := os.WriteFile(vmStatePath, []byte(`{"ports":{"db":8563}}`), 0o644); err != nil {
		t.Fatalf("seed vm-state: %v", err)
	}
	// Fake podman: --version (Available) succeeds, stop/rm succeed.
	logPath := installFakePodman(t, `exit 0`)

	if err := stopCmd(); err != nil {
		t.Fatalf("stopCmd: %v", err)
	}

	// vm-state.json must be gone.
	if _, err := os.Stat(vmStatePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected %s removed, stat error was %v", vmStatePath, err)
	}

	calls := readArgvCalls(t, logPath)
	// Expect: --version (Available), stop --ignore --time 30 <name>, rm --ignore <name>.
	if len(calls) != 3 {
		t.Fatalf("expected 3 podman calls, got %d: %v", len(calls), calls)
	}
	wantStop := []string{"stop", "--ignore", "--time", "30", "exasol-local-db"}
	if !stringsEqual(calls[1], wantStop) {
		t.Errorf("stop argv: want %v, got %v", wantStop, calls[1])
	}
	wantRm := []string{"rm", "--ignore", "exasol-local-db"}
	if !stringsEqual(calls[2], wantRm) {
		t.Errorf("rm argv: want %v, got %v", wantRm, calls[2])
	}
}

func TestStopCmdIsIdempotentWhenVMStateAbsent(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFixtureResources(t)
	// No vm-state.json to start with.
	installFakePodman(t, `exit 0`)
	if err := stopCmd(); err != nil {
		t.Fatalf("stopCmd (no vm-state): %v", err)
	}
	// A second stop must also succeed cleanly.
	if err := stopCmd(); err != nil {
		t.Fatalf("second stopCmd: %v", err)
	}
}

func TestStopCmdWithoutConfigCleansStateFile(t *testing.T) {
	t.Chdir(t.TempDir())
	// No resources/config.json — init was never run in this dir.
	if err := os.WriteFile(vmStatePath, []byte(`{"ports":{"db":8563}}`), 0o644); err != nil {
		t.Fatalf("seed vm-state: %v", err)
	}
	// Even though podman would be invoked with a bogus binary, we should
	// short-circuit before ever calling it. Point PATH at an empty dir to
	// prove that.
	t.Setenv("PATH", t.TempDir())

	if err := stopCmd(); err != nil {
		t.Fatalf("stopCmd without config: %v", err)
	}
	if _, err := os.Stat(vmStatePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected %s removed, stat error was %v", vmStatePath, err)
	}
}

func TestStopCmdErrorsWhenPodmanUnavailable(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFixtureResources(t)
	if err := os.WriteFile(vmStatePath, []byte(`{"ports":{"db":8563}}`), 0o644); err != nil {
		t.Fatalf("seed vm-state: %v", err)
	}
	// Empty PATH — Available() must fail.
	t.Setenv("PATH", t.TempDir())

	err := stopCmd()
	if err == nil {
		t.Fatal("expected error when podman is missing")
	}
	if !strings.Contains(err.Error(), "podman-for-windows is required") {
		t.Errorf("error should mention prerequisite install: %v", err)
	}
	// vm-state.json is expected to remain — the user's environment is
	// broken and we should not silently pretend we cleaned it up.
	if _, err := os.Stat(vmStatePath); err != nil {
		t.Errorf("expected %s preserved on error, stat error was %v", vmStatePath, err)
	}
}

func TestStatusCmdReportsRunningTrue(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFixtureResources(t)
	// container exists → true, inspect → "true".
	installFakePodman(t, `
case "$2" in
    exists) exit 0 ;;
    inspect) echo "true"; exit 0 ;;
esac
exit 0
`)
	var buf bytes.Buffer
	if err := statusCmdTo(&buf); err != nil {
		t.Fatalf("statusCmdTo: %v", err)
	}
	got := strings.TrimSpace(buf.String())
	if got != `{"running":true}` {
		t.Errorf("stdout: want {\"running\":true}, got %q", got)
	}
}

func TestStatusCmdReportsRunningFalseWhenContainerMissing(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFixtureResources(t)
	// --version 0; container exists 1 (absent) — inspect never called.
	installFakePodman(t, `
case "$2" in
    exists) exit 1 ;;
esac
exit 0
`)
	var buf bytes.Buffer
	if err := statusCmdTo(&buf); err != nil {
		t.Fatalf("statusCmdTo: %v", err)
	}
	got := strings.TrimSpace(buf.String())
	if got != `{"running":false}` {
		t.Errorf("stdout: want {\"running\":false}, got %q", got)
	}
}

func TestStatusCmdReportsFalseWhenPodmanUnavailable(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFixtureResources(t)
	t.Setenv("PATH", t.TempDir()) // no podman on PATH
	var buf bytes.Buffer
	if err := statusCmdTo(&buf); err != nil {
		t.Fatalf("statusCmdTo: %v (must not error even when podman is missing)", err)
	}
	got := strings.TrimSpace(buf.String())
	if got != `{"running":false}` {
		t.Errorf("stdout: want {\"running\":false}, got %q", got)
	}
}

func TestStatusCmdReportsFalseWithoutConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	// No resources/config.json.
	var buf bytes.Buffer
	if err := statusCmdTo(&buf); err != nil {
		t.Fatalf("statusCmdTo without config: %v (must not error)", err)
	}
	got := strings.TrimSpace(buf.String())
	if got != `{"running":false}` {
		t.Errorf("stdout: want {\"running\":false}, got %q", got)
	}
}
