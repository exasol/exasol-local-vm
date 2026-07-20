// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

// Package winget wraps the "winget" (Windows Package Manager) CLI to
// install podman-for-windows non-interactively on behalf of the launcher.
package winget

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

var binary = "winget"

// podmanInstallDirOverrideEnv is the environment variable a test can set
// to override the assumed post-install location of podman.exe. Production
// callers do not set it; the default is %LOCALAPPDATA%\Programs\Podman
// (matches the user-scope MSI install layout).
const podmanInstallDirOverrideEnv = "WINDOWS_RUNNER_TEST_PODMAN_INSTALL_DIR"

// InstallPodman runs `winget install --exact --id RedHat.Podman` with the
// flags needed to complete unattended once the user has already consented
// via the launcher's own prompt: SLAs are auto-accepted, but winget's own
// output (download progress, etc.) is left visible by streaming to w.
// Blocks until winget exits.
//
// --scope user is passed explicitly so the install lands in
// %LOCALAPPDATA%\Programs\Podman rather than %PROGRAMFILES%\Podman.
// User-scope install does not require administrator privileges (no UAC
// prompt) which is the whole point of Phase 14: the launcher should be
// usable by non-admin developers on locked-down enterprise machines.
// See https://github.com/containers/podman/blob/main/docs/tutorials/podman-for-windows.md#installing-podman
// for Podman's own documentation of the scope semantics.
//
// This intentionally does NOT pass --silent or --disable-interactivity:
// the launcher's prompt already asked the user, and hiding winget's
// output would strip useful download-progress feedback during the
// multi-minute download.
func InstallPodman(w io.Writer) error {
	cmd := exec.Command(
		binary, "install",
		"--exact", "--id", "RedHat.Podman",
		"--scope", "user",
		"--accept-source-agreements",
		"--accept-package-agreements",
	)
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("winget install RedHat.Podman failed: %w", err)
	}
	return nil
}

// EnsurePodmanOnPath prepends podman's default install directory to the
// current process's PATH so subsequent exec.LookPath("podman") calls
// resolve within the same launcher invocation, without waiting for the
// user to open a new shell. The PATH mutation is process-local and does
// not persist across launcher invocations — a fresh `windows-runner`
// process on a new shell will already have the updated PATH from
// winget's own environment mutation.
func EnsurePodmanOnPath() error {
	installDir, err := podmanInstallDir()
	if err != nil {
		return err
	}
	if _, err := os.Stat(installDir); err != nil {
		return fmt.Errorf(
			"winget install reported success but podman install directory not found at %s: %w",
			installDir, err,
		)
	}
	current := os.Getenv("PATH")
	if current == "" {
		return os.Setenv("PATH", installDir)
	}
	return os.Setenv("PATH", installDir+string(os.PathListSeparator)+current)
}

func podmanInstallDir() (string, error) {
	if override := os.Getenv(podmanInstallDirOverrideEnv); override != "" {
		return override, nil
	}
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf(
			"winget.EnsurePodmanOnPath: non-windows platform requires %s to be set (test-only)",
			podmanInstallDirOverrideEnv,
		)
	}
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return "", fmt.Errorf("winget.EnsurePodmanOnPath: LOCALAPPDATA is not set")
	}
	return filepath.Join(localAppData, "Programs", "Podman"), nil
}
