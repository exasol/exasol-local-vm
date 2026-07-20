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

// TestMain forces all tests into the non-interactive branch of the
// Phase 14 prompt helpers by swapping promptIn from os.Stdin (which
// may be a real tty when running `go test` from a shell) to an empty
// bytes.Buffer (which isTerminal reports as false). Tests that need to
// exercise the interactive branch call ensurePodmanInstalledCtx
// directly with interactive=true instead of relying on the ambient
// promptIn.
func TestMain(m *testing.M) {
	promptIn = &bytes.Buffer{}
	os.Exit(m.Run())
}

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
	got := buildPodmanRunArgs(cfg, "docker.io/exasol/nano:2026.2.0-nano.2", map[string]int{"db": 8563}, VersionCheckOptions{})
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
		// Version-check disabled by the zero VersionCheckOptions: single
		// trailing arg matches the guest-side init-db.sh's
		// VERSION_CHECK_ENABLED=0 emit path.
		"VERSION_CHECK_ENABLED=0",
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
	got := buildPodmanRunArgs(cfg, "img:v1", map[string]int{"db": 9090}, VersionCheckOptions{})
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
	got := buildPodmanRunArgs(cfg, "img:v1", map[string]int{"db": 8563, "metrics": 9100}, VersionCheckOptions{})
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
// `params=` token and complain. With version-check disabled, `init`
// is immediately followed by `VERSION_CHECK_ENABLED=0`.
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
	got := buildPodmanRunArgs(cfg, "img:v1", map[string]int{"db": 8563}, VersionCheckOptions{})
	for _, tok := range got {
		if strings.HasPrefix(tok, "params=") {
			t.Errorf("did not expect a params= token when config has no params, got argv %v", got)
		}
	}
	// init must be immediately followed by VERSION_CHECK_ENABLED=0 (no
	// params= filler in between).
	var initIdx = -1
	for i, tok := range got {
		if tok == "init" {
			initIdx = i
			break
		}
	}
	if initIdx < 0 {
		t.Fatalf("init token missing from argv: %v", got)
	}
	if initIdx+1 >= len(got) || got[initIdx+1] != "VERSION_CHECK_ENABLED=0" {
		t.Errorf("expected 'init' to be immediately followed by 'VERSION_CHECK_ENABLED=0', got argv %v", got)
	}
}

// TestBuildPodmanRunArgsJoinsMultipleParamsWithSpace matches the guest
// shell's `jq '.db.params // [] | join(" ")'` behavior — Nano expects
// a single space-joined value on the right of `params=`. The joined
// params token appears between `init` and the trailing
// VERSION_CHECK_ENABLED= token.
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
	got := buildPodmanRunArgs(cfg, "img:v1", map[string]int{"db": 8563}, VersionCheckOptions{})
	var paramsTok string
	for _, tok := range got {
		if strings.HasPrefix(tok, "params=") {
			paramsTok = tok
			break
		}
	}
	if paramsTok != "params=a=1 b=2 c=3" {
		t.Errorf("expected joined params token, got %q", paramsTok)
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

func TestStopCmdTreatsMissingPodmanAsNothingToStop(t *testing.T) {
	// Phase 14: stop now no-ops when podman is missing rather than
	// erroring, because there is provably no container this launcher
	// could have started without podman. The prompt is skipped in
	// non-interactive contexts (no *os.File stdin), so this exercises
	// the soft-failure branch of ensurePodmanInstalled.
	t.Chdir(t.TempDir())
	writeFixtureResources(t)
	if err := os.WriteFile(vmStatePath, []byte(`{"ports":{"db":8563}}`), 0o644); err != nil {
		t.Fatalf("seed vm-state: %v", err)
	}
	// Empty PATH — Available() must fail. Prompt is non-interactive
	// (default promptIn = os.Stdin during tests is not a tty), so
	// stopCmd should take the soft path and clean up vm-state.json.
	t.Setenv("PATH", t.TempDir())

	if err := stopCmd(); err != nil {
		t.Fatalf("stopCmd should succeed as a no-op when podman is missing, got: %v", err)
	}
	// vm-state.json is expected to be removed — we know nothing could
	// have been running, so leaving a stale sidecar would confuse a
	// later `status` call.
	if _, err := os.Stat(vmStatePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected %s removed, stat error was %v", vmStatePath, err)
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

// --- Phase 9: --version-check-*, data-size sidecar, resize-data --------

func TestVersionCheckPodmanArgsDisabled(t *testing.T) {
	preImage, initArgs := versionCheckPodmanArgs(VersionCheckOptions{}, "Windows")
	if len(preImage) != 0 {
		t.Errorf("expected no pre-image args when disabled, got %v", preImage)
	}
	if !stringsEqual(initArgs, []string{"VERSION_CHECK_ENABLED=0"}) {
		t.Errorf("disabled init args: got %v", initArgs)
	}
}

func TestVersionCheckPodmanArgsEnabledDefaults(t *testing.T) {
	// Enabled=true with everything else zero — helpers should fill in
	// defaults matching the mac guest-side load_version_check_config.
	preImage, initArgs := versionCheckPodmanArgs(VersionCheckOptions{Enabled: true}, "Windows")
	wantPre := []string{"-e", "VERSION_CHECK_IDENTITY=NONE"}
	if !stringsEqual(preImage, wantPre) {
		t.Errorf("pre-image: want %v, got %v", wantPre, preImage)
	}
	wantInit := []string{
		"VERSION_CHECK_ENABLED=1",
		"VERSION_CHECK_ENDPOINT=https://metrics-test.exasol.com/v1/version-check",
		"VERSION_CHECK_INTERVAL_SEC=86400",
		"VERSION_CHECK_RETRY_INTERVAL_SEC=86400",
		"VERSION_CHECK_OPERATING_SYSTEM=Windows",
	}
	if !stringsEqual(initArgs, wantInit) {
		t.Errorf("init args:\n want %v\n got  %v", wantInit, initArgs)
	}
}

func TestVersionCheckPodmanArgsEnabledExplicitValues(t *testing.T) {
	preImage, initArgs := versionCheckPodmanArgs(VersionCheckOptions{
		Enabled:         true,
		Identity:        "exasol-personal;deployment;small;default",
		URL:             "https://metrics.example.test/v1/vc",
		IntervalSeconds: 3600,
	}, "Windows")
	// Identity survives the semicolon-heavy string verbatim; that's the
	// whole point of routing it via -e instead of the init argv (which
	// treats ';' as a separator).
	wantPre := []string{"-e", "VERSION_CHECK_IDENTITY=exasol-personal;deployment;small;default"}
	if !stringsEqual(preImage, wantPre) {
		t.Errorf("pre-image: want %v, got %v", wantPre, preImage)
	}
	wantInit := []string{
		"VERSION_CHECK_ENABLED=1",
		"VERSION_CHECK_ENDPOINT=https://metrics.example.test/v1/vc",
		"VERSION_CHECK_INTERVAL_SEC=3600",
		"VERSION_CHECK_RETRY_INTERVAL_SEC=3600",
		"VERSION_CHECK_OPERATING_SYSTEM=Windows",
	}
	if !stringsEqual(initArgs, wantInit) {
		t.Errorf("init args:\n want %v\n got  %v", wantInit, initArgs)
	}
}

func TestVersionCheckPodmanArgsClampsInterval(t *testing.T) {
	// Below the minimum (60s) — clamps up.
	_, initArgs := versionCheckPodmanArgs(VersionCheckOptions{Enabled: true, IntervalSeconds: 5}, "Windows")
	if !containsToken(initArgs, "VERSION_CHECK_INTERVAL_SEC=60") {
		t.Errorf("expected interval clamped to 60, got %v", initArgs)
	}

	// Above the maximum (604800s = one week) — clamps down.
	_, initArgs = versionCheckPodmanArgs(VersionCheckOptions{Enabled: true, IntervalSeconds: 999999999}, "Windows")
	if !containsToken(initArgs, "VERSION_CHECK_INTERVAL_SEC=604800") {
		t.Errorf("expected interval clamped to 604800, got %v", initArgs)
	}
	// Retry interval has a tighter cap (86400).
	if !containsToken(initArgs, "VERSION_CHECK_RETRY_INTERVAL_SEC=86400") {
		t.Errorf("expected retry interval clamped to 86400, got %v", initArgs)
	}
}

func TestVersionCheckPodmanArgsOmitsOperatingSystemWhenEmpty(t *testing.T) {
	// A hypothetical caller passing an empty osName should not produce a
	// VERSION_CHECK_OPERATING_SYSTEM=<empty> token — init-db.sh only
	// emits the OS arg when it has a non-empty value.
	_, initArgs := versionCheckPodmanArgs(VersionCheckOptions{Enabled: true}, "")
	for _, tok := range initArgs {
		if strings.HasPrefix(tok, "VERSION_CHECK_OPERATING_SYSTEM=") {
			t.Errorf("did not expect an OS token for empty osName, got %v", initArgs)
		}
	}
}

func TestBuildPodmanRunArgsInsertsVersionCheckIdentityBeforeImage(t *testing.T) {
	cfg := dbContainerConfig{
		ContainerName: "exasol-local-db",
		Ports:         map[string]int{"db": 8563},
		ShmSize:       "512mb",
		PidsLimit:     "-1",
		SecurityOpt:   "unmask=ALL",
		Restart:       "always",
	}
	got := buildPodmanRunArgs(cfg, "img:v1", map[string]int{"db": 8563},
		VersionCheckOptions{Enabled: true, Identity: "me"})
	// -e must appear before the image ref, and VERSION_CHECK_IDENTITY=me
	// must be the immediately-following argv token.
	var eIdx, imgIdx = -1, -1
	for i, tok := range got {
		if tok == "-e" && eIdx == -1 {
			eIdx = i
		}
		if tok == "img:v1" {
			imgIdx = i
		}
	}
	if eIdx == -1 {
		t.Fatalf("-e missing from argv: %v", got)
	}
	if imgIdx == -1 {
		t.Fatalf("image ref missing from argv: %v", got)
	}
	if eIdx >= imgIdx {
		t.Errorf("expected -e to precede image ref, got -e at %d, image at %d in %v", eIdx, imgIdx, got)
	}
	if eIdx+1 >= len(got) || got[eIdx+1] != "VERSION_CHECK_IDENTITY=me" {
		t.Errorf("expected -e to be followed by VERSION_CHECK_IDENTITY=me, got %v", got)
	}
}

// containsToken returns true if s contains exactly the token t.
func containsToken(s []string, t string) bool {
	for _, x := range s {
		if x == t {
			return true
		}
	}
	return false
}

// --- Phase 9: data-size sidecar helpers --------------------------------

func TestReadDataSizeReturnsZeroWhenAbsent(t *testing.T) {
	t.Chdir(t.TempDir())
	got, err := readDataSize()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 0 {
		t.Errorf("expected 0 for missing sidecar, got %d", got)
	}
}

func TestReadDataSizeRoundTrip(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(resourcesDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := writeDataSize(42); err != nil {
		t.Fatalf("writeDataSize: %v", err)
	}
	got, err := readDataSize()
	if err != nil {
		t.Fatalf("readDataSize: %v", err)
	}
	if got != 42 {
		t.Errorf("round-trip mismatch: wrote 42, read %d", got)
	}
}

func TestReadDataSizeRejectsGarbage(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(resourcesDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dataSizePath, []byte("notanumber"), 0o644); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}
	if _, err := readDataSize(); err == nil {
		t.Error("expected error on non-integer sidecar contents")
	}
}

func TestEnforceDataSizeGrowOnly(t *testing.T) {
	cases := []struct {
		name             string
		currentGB        int
		requestedGB      int
		expectShrinkErr  bool
	}{
		{"equal is fine", 10, 10, false},
		{"grow is fine", 10, 20, false},
		{"shrink rejected", 20, 10, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := enforceDataSizeGrowOnly(tc.currentGB, tc.requestedGB)
			if tc.expectShrinkErr {
				if err == nil {
					t.Fatal("expected shrink error, got nil")
				}
				// tests/data_persistence_test.go TestDataDiskShrinkRejected
				// asserts on a case-insensitive "shrink" substring.
				if !strings.Contains(strings.ToLower(err.Error()), "shrink") {
					t.Errorf("error should mention 'shrink', got: %v", err)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// --- Phase 9: resize-data ---------------------------------------------

func TestResizeDataDiskCmdRejectsInvalidSize(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFixtureResources(t)
	for _, arg := range []string{"abc", "0", "-1"} {
		if err := resizeDataDiskCmd(arg); err == nil {
			t.Errorf("expected error for size arg %q, got nil", arg)
		}
	}
}

func TestResizeDataDiskCmdRequiresInit(t *testing.T) {
	t.Chdir(t.TempDir())
	// No resources/config.json.
	err := resizeDataDiskCmd("20")
	if err == nil {
		t.Fatal("expected error when launcher not initialized")
	}
	if !strings.Contains(err.Error(), "not initialized") {
		t.Errorf("error should mention 'not initialized', got: %v", err)
	}
}

func TestResizeDataDiskCmdRequiresPriorStart(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFixtureResources(t)
	// No podman on PATH — we skip the running check gracefully.
	t.Setenv("PATH", t.TempDir())
	// No data-size sidecar yet.
	err := resizeDataDiskCmd("20")
	if err == nil {
		t.Fatal("expected error when data size not yet recorded")
	}
	if !strings.Contains(err.Error(), "not been recorded") {
		t.Errorf("error should hint at running start first, got: %v", err)
	}
}

func TestResizeDataDiskCmdRejectsShrink(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFixtureResources(t)
	t.Setenv("PATH", t.TempDir())
	if err := writeDataSize(20); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}
	err := resizeDataDiskCmd("10")
	if err == nil {
		t.Fatal("expected error for shrink")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "shrink") {
		t.Errorf("error should mention 'shrink', got: %v", err)
	}
}

func TestResizeDataDiskCmdRejectsEqualSize(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFixtureResources(t)
	t.Setenv("PATH", t.TempDir())
	if err := writeDataSize(20); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}
	err := resizeDataDiskCmd("20")
	if err == nil {
		t.Fatal("resize-data to equal size should error (only growth is allowed)")
	}
	// Message matches the mac semantics: "must be larger than".
	if !strings.Contains(err.Error(), "larger than current size") {
		t.Errorf("error should say 'larger than current size', got: %v", err)
	}
}

func TestResizeDataDiskCmdAcceptsGrow(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFixtureResources(t)
	t.Setenv("PATH", t.TempDir()) // no podman → skip running check
	if err := writeDataSize(10); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}
	if err := resizeDataDiskCmd("30"); err != nil {
		t.Fatalf("resize-data grow: %v", err)
	}
	got, err := readDataSize()
	if err != nil {
		t.Fatalf("readDataSize: %v", err)
	}
	if got != 30 {
		t.Errorf("sidecar not updated: want 30, got %d", got)
	}
}

func TestResizeDataDiskCmdBlocksWhenContainerRunning(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFixtureResources(t)
	if err := writeDataSize(10); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}
	// Fake podman: --version 0, container exists 0, container inspect → true.
	installFakePodman(t, `
case "$2" in
    exists) exit 0 ;;
    inspect) echo "true"; exit 0 ;;
esac
exit 0
`)
	err := resizeDataDiskCmd("20")
	if err == nil {
		t.Fatal("expected error when container is running")
	}
	if !strings.Contains(err.Error(), "currently running") {
		t.Errorf("error should mention 'currently running', got: %v", err)
	}
	// Sidecar must not have been updated.
	got, err := readDataSize()
	if err != nil {
		t.Fatalf("readDataSize: %v", err)
	}
	if got != 10 {
		t.Errorf("sidecar was updated despite refusal: got %d, want 10", got)
	}
}

// --- Phase 14: podman install prompt ------------------------------------

// installFakeWingetInEmptyPath drops a fake winget shim into a temp dir
// AND resets PATH to point only at that dir. This is what the Phase 14
// interactive-install tests need: Available() must fail on the first
// call (empty PATH → no podman anywhere) and succeed on the second
// call (after EnsurePodmanOnPath prepends the fake install dir).
func installFakeWingetInEmptyPath(t *testing.T, body string) (argvLogPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake winget shim uses a POSIX shell script; skipping on windows")
	}

	dir := t.TempDir()
	argvLogPath = filepath.Join(dir, "winget-argv.log")
	script := "#!/bin/sh\n" +
		"for arg in \"$@\"; do printf '%s\\n' \"$arg\" >> \"" + argvLogPath + "\"; done\n" +
		"printf -- '---\\n' >> \"" + argvLogPath + "\"\n" +
		body + "\n"
	binPath := filepath.Join(dir, "winget")
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake winget shim: %v", err)
	}
	t.Setenv("PATH", dir) // No podman anywhere on PATH.
	return argvLogPath
}

// stageFakePodmanInstallDir seeds a fresh temp directory with a fake
// podman shim and points WINDOWS_RUNNER_TEST_PODMAN_INSTALL_DIR at it.
// After winget.EnsurePodmanOnPath() prepends that directory to PATH,
// subsequent podman calls invoke this shim. The shim logs argv to a
// file so tests can assert on the install-flow's podman invocations
// (--version, machine init, machine start).
func stageFakePodmanInstallDir(t *testing.T, body string) (argvLogPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake podman shim uses a POSIX shell script; skipping on windows")
	}

	dir := t.TempDir()
	argvLogPath = filepath.Join(dir, "podman-argv.log")
	script := "#!/bin/sh\n" +
		"for arg in \"$@\"; do printf '%s\\n' \"$arg\" >> \"" + argvLogPath + "\"; done\n" +
		"printf -- '---\\n' >> \"" + argvLogPath + "\"\n" +
		body + "\n"
	binPath := filepath.Join(dir, "podman")
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake podman shim in install dir: %v", err)
	}
	t.Setenv("WINDOWS_RUNNER_TEST_PODMAN_INSTALL_DIR", dir)
	return argvLogPath
}

func TestEnsurePodmanInstalled_AlreadyAvailable(t *testing.T) {
	// Fake podman already on PATH — ensurePodmanInstalled should
	// short-circuit at the first Available() call, never touching
	// winget or the prompt.
	installFakePodman(t, `exit 0`)

	var out bytes.Buffer
	in := bytes.NewBufferString("this should never be read\n")

	installed, err := ensurePodmanInstalledCtx(in, &out, true, true)
	if err != nil {
		t.Fatalf("ensurePodmanInstalledCtx: %v", err)
	}
	if !installed {
		t.Error("expected installed=true when podman is already on PATH")
	}
	if out.Len() != 0 {
		t.Errorf("expected no output when podman is available, got %q", out.String())
	}
}

func TestEnsurePodmanInstalled_NonInteractiveRequired(t *testing.T) {
	// No podman on PATH, non-interactive, required=true → error.
	t.Setenv("PATH", t.TempDir())

	var out bytes.Buffer
	_, err := ensurePodmanInstalledCtx(&bytes.Buffer{}, &out, true, false)
	if err == nil {
		t.Fatal("expected error when podman missing + required + non-interactive")
	}
	if !strings.Contains(err.Error(), "Re-run this command interactively") {
		t.Errorf("error should suggest interactive re-run, got: %v", err)
	}
}

func TestEnsurePodmanInstalled_NonInteractiveOptional(t *testing.T) {
	// No podman on PATH, non-interactive, required=false → (false, nil)
	// with no prompt output.
	t.Setenv("PATH", t.TempDir())

	var out bytes.Buffer
	installed, err := ensurePodmanInstalledCtx(&bytes.Buffer{}, &out, false, false)
	if err != nil {
		t.Fatalf("ensurePodmanInstalledCtx: %v", err)
	}
	if installed {
		t.Error("expected installed=false")
	}
	if out.Len() != 0 {
		t.Errorf("expected silent behaviour in non-interactive optional path, got %q", out.String())
	}
}

func TestEnsurePodmanInstalled_InteractiveDeclineOptional(t *testing.T) {
	// User types "n\n" — required=false so we return (false, nil) with a
	// helpful "you will be prompted again" message.
	t.Setenv("PATH", t.TempDir())

	var out bytes.Buffer
	in := bytes.NewBufferString("n\n")
	installed, err := ensurePodmanInstalledCtx(in, &out, false, true)
	if err != nil {
		t.Fatalf("ensurePodmanInstalledCtx: %v", err)
	}
	if installed {
		t.Error("expected installed=false after decline")
	}
	if !strings.Contains(out.String(), "prompted again when you run 'windows-runner start'") {
		t.Errorf("expected re-prompt hint in output, got %q", out.String())
	}
}

func TestEnsurePodmanInstalled_InteractiveDeclineRequired(t *testing.T) {
	// User types "n\n" — required=true so we return an error.
	t.Setenv("PATH", t.TempDir())

	var out bytes.Buffer
	in := bytes.NewBufferString("n\n")
	_, err := ensurePodmanInstalledCtx(in, &out, true, true)
	if err == nil {
		t.Fatal("expected error when required + user declines")
	}
	if !strings.Contains(err.Error(), "cannot proceed without podman-for-windows") {
		t.Errorf("error should mention cannot-proceed, got: %v", err)
	}
}

func TestEnsurePodmanInstalled_InteractiveAcceptFullFlow(t *testing.T) {
	// End-to-end happy path: user says Y, winget install succeeds,
	// EnsurePodmanOnPath finds the staged install dir, subsequent
	// podman calls (--version, machine init, machine start) all
	// succeed against the fake shim in that dir.
	wingetLog := installFakeWingetInEmptyPath(t, `exit 0`)
	podmanLog := stageFakePodmanInstallDir(t, `exit 0`)

	var out bytes.Buffer
	in := bytes.NewBufferString("Y\n")

	installed, err := ensurePodmanInstalledCtx(in, &out, true, true)
	if err != nil {
		t.Fatalf("ensurePodmanInstalledCtx: %v", err)
	}
	if !installed {
		t.Error("expected installed=true after successful install flow")
	}

	// Verify winget was called exactly once with the expected argv.
	wingetCalls := readArgvCalls(t, wingetLog)
	if len(wingetCalls) != 1 {
		t.Fatalf("expected 1 winget call, got %d: %v", len(wingetCalls), wingetCalls)
	}
	wantWingetArgv := []string{
		"install",
		"--exact", "--id", "RedHat.Podman",
		"--source", "winget",
		"--scope", "user",
		"--accept-source-agreements",
		"--accept-package-agreements",
	}
	if !stringsEqual(wingetCalls[0], wantWingetArgv) {
		t.Errorf("winget argv:\n  want: %v\n  got:  %v", wantWingetArgv, wingetCalls[0])
	}

	// Verify podman was called three times in the expected order:
	//   1. --version           (post-install Available() sanity check)
	//   2. machine init --disk-size 40
	//   3. machine start
	podmanCalls := readArgvCalls(t, podmanLog)
	if len(podmanCalls) != 3 {
		t.Fatalf("expected 3 podman calls, got %d: %v", len(podmanCalls), podmanCalls)
	}
	if !stringsEqual(podmanCalls[0], []string{"--version"}) {
		t.Errorf("call 1 argv: want [--version], got %v", podmanCalls[0])
	}
	wantInit := []string{"machine", "init", "--disk-size", "40"}
	if !stringsEqual(podmanCalls[1], wantInit) {
		t.Errorf("call 2 argv: want %v, got %v", wantInit, podmanCalls[1])
	}
	if !stringsEqual(podmanCalls[2], []string{"machine", "start"}) {
		t.Errorf("call 3 argv: want [machine start], got %v", podmanCalls[2])
	}

	// User-visible progress messages should be present.
	for _, want := range []string{
		"podman-for-windows is not installed",
		"Installing podman-for-windows via winget",
		"Initializing podman WSL2 machine",
		"Starting podman machine",
		"Podman is ready",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("expected %q in output, got %q", want, out.String())
		}
	}
}

func TestEnsurePodmanInstalled_InteractiveAcceptDefaultOnEmptyInput(t *testing.T) {
	// Empty input line (just Enter) — defaultYes=true, so the install
	// flow runs. Same shims as the full-flow test, less-thorough argv
	// assertion since that's covered above.
	installFakeWingetInEmptyPath(t, `exit 0`)
	stageFakePodmanInstallDir(t, `exit 0`)

	var out bytes.Buffer
	in := bytes.NewBufferString("\n") // Just Enter → default = Yes.

	installed, err := ensurePodmanInstalledCtx(in, &out, true, true)
	if err != nil {
		t.Fatalf("ensurePodmanInstalledCtx: %v", err)
	}
	if !installed {
		t.Error("empty input should default to Yes and install")
	}
	if !strings.Contains(out.String(), "Podman is ready") {
		t.Errorf("expected 'Podman is ready' in output, got %q", out.String())
	}
}

func TestEnsurePodmanInstalled_InteractiveWingetFails(t *testing.T) {
	// User says Y but winget install exits non-zero — error surfaces
	// with winget's stderr streamed through the shared output writer.
	installFakeWingetInEmptyPath(t, `echo "package not found" >&2; exit 1`)
	// Also stage a podman install dir even though we won't reach it —
	// keeps the env consistent.
	stageFakePodmanInstallDir(t, `exit 0`)

	var out bytes.Buffer
	in := bytes.NewBufferString("y\n")
	_, err := ensurePodmanInstalledCtx(in, &out, true, true)
	if err == nil {
		t.Fatal("expected error when winget install fails")
	}
	if !strings.Contains(err.Error(), "winget install RedHat.Podman failed") {
		t.Errorf("error should mention winget failure, got: %v", err)
	}
	if !strings.Contains(out.String(), "package not found") {
		t.Errorf("expected winget stderr streamed to output, got %q", out.String())
	}
}

func TestPromptYesNo(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		defaultYes bool
		want       bool
	}{
		{"y accepts", "y\n", true, true},
		{"Y accepts", "Y\n", false, true},
		{"yes accepts", "yes\n", false, true},
		{"n rejects", "n\n", true, false},
		{"N rejects", "N\n", true, false},
		{"empty takes default yes", "\n", true, true},
		{"empty takes default no", "\n", false, false},
		{"unrecognized takes default yes", "maybe\n", true, true},
		{"unrecognized takes default no", "maybe\n", false, false},
		{"EOF takes default yes", "", true, true},
		{"EOF takes default no", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			got, err := promptYesNo(strings.NewReader(tc.input), &out, "Continue?", tc.defaultYes)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("want %v, got %v", tc.want, got)
			}
			// Prompt suffix must match defaultYes.
			suffix := " [y/N] "
			if tc.defaultYes {
				suffix = " [Y/n] "
			}
			if !strings.HasSuffix(out.String(), suffix) {
				t.Errorf("expected prompt to end with %q, got %q", suffix, out.String())
			}
		})
	}
}

func TestInitCmdSucceedsWhenPodmanCheckSoftFails(t *testing.T) {
	// Phase 14: init calls ensurePodmanInstalled with required=false.
	// Non-interactive + missing podman must not fail init — resources/
	// still has to be produced.
	assets := buildTestTarXZ(t, map[string][]byte{
		"init/config.json":           []byte(`{"db":{}}`),
		"init/exasol-nano-db.tar.gz": []byte("payload"),
	})
	t.Chdir(t.TempDir())
	t.Setenv("PATH", t.TempDir()) // No podman anywhere.

	if err := initCmdWithAssets("", assets); err != nil {
		t.Fatalf("initCmdWithAssets should succeed even without podman: %v", err)
	}
	// Resources must still exist.
	if _, err := os.Stat(filepath.Join(resourcesDir, "config.json")); err != nil {
		t.Errorf("resources/config.json should exist: %v", err)
	}
	if _, err := os.Stat(runtimeConfigPath); err != nil {
		t.Errorf("%s should exist: %v", runtimeConfigPath, err)
	}
}
