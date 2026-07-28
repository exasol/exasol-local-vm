// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

// Package winget wraps the "winget" (Windows Package Manager) CLI to
// install podman-for-windows non-interactively on behalf of the launcher.
package winget

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

var binary = "winget"

// podmanPathAdditionsOverrideEnv is the environment variable a test can
// set to override the PATH-refresh source used by EnsurePodmanOnPath.
// Production callers do not set it; the default is to read the merged
// machine- and user-scope PATH from the Windows registry.
//
// Value semantics: an OS-native PATH string (';'-separated on Windows,
// ':'-separated elsewhere) whose entries EnsurePodmanOnPath prepends
// to the current process's PATH so a subsequent LookPath("podman")
// resolves the newly-installed binary. Setting it to a single
// directory (containing the fake podman shim) is the common test
// pattern.
const podmanPathAdditionsOverrideEnv = "WINDOWS_LAUNCHER_TEST_PODMAN_PATH_ADDITIONS"

// InstallPodman runs `winget install --exact --id RedHat.Podman` with the
// flags needed to complete unattended once the user has already consented
// via the launcher's own prompt: SLAs are auto-accepted, but winget's own
// output (download progress, etc.) is left visible by streaming to w.
// Blocks until winget exits.
//
// Scope is intentionally left unspecified. The RedHat.Podman entry in
// winget-pkgs currently declares only a machine-scope installer; passing
// `--scope user` would filter the manifest's installer list to zero
// candidates and winget would abort with
// APPINSTALLER_CLI_ERROR_NO_APPLICABLE_INSTALLER (exit 0x8A150010)
// before downloading anything. Without --scope, winget picks based on
// the current process's elevation: an elevated shell installs at
// machine scope silently, a non-elevated shell triggers a UAC prompt.
// End users on locked-down machines who cannot elevate must install
// podman-for-windows manually — the launcher's caller prints an
// advisory pointing them at the manual installer when this call fails.
//
// --source winget pins the package search to the official winget
// community source. Without this pin, winget silently falls back to
// the 'msstore' source when its winget-source refresh fails or is
// stale, and msstore then blocks on an interactive 'accept geographic
// region' prompt that even --accept-source-agreements does not
// silence. This was observed in CI on windows-latest but the same
// failure mode can bite end users whose winget source list is out of
// date. Pinning the source keeps the launcher's prompt-then-install
// flow deterministic.
//
// This intentionally does NOT pass --silent or --disable-interactivity:
// the launcher's prompt already asked the user, and hiding winget's
// output would strip useful download-progress feedback during the
// multi-minute download.
func InstallPodman(w io.Writer) error {
	cmd := exec.Command(binary, podmanInstallArgs()...)
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("winget install RedHat.Podman failed: %w", err)
	}
	return nil
}

// podmanInstallArgs returns the argv (without the leading `winget`
// executable name) that InstallPodman invokes. Kept in one place so
// PodmanInstallCommand can display exactly what the launcher will run
// before the user consents at the Y/n prompt.
func podmanInstallArgs() []string {
	return []string{
		"install",
		"--exact", "--id", "RedHat.Podman",
		"--source", "winget",
		"--accept-source-agreements",
		"--accept-package-agreements",
	}
}

// PodmanInstallCommand returns the exact `winget ...` command
// InstallPodman will execute, formatted as a single line. The launcher
// prints this to the user before asking whether to run it — showing the
// verb rather than a paraphrase lets a security-conscious user copy
// the command into their own shell if they'd rather run it themselves.
func PodmanInstallCommand() string {
	return binary + " " + strings.Join(podmanInstallArgs(), " ")
}

// EnsurePodmanOnPath refreshes the current process's PATH with the
// entries the Windows registry lists after winget's install of
// podman-for-windows, so a subsequent exec.LookPath("podman") in the
// same launcher process resolves the newly-installed binary without
// waiting for the user to open a new shell.
//
// Windows sets a process's PATH once at start-up and does not re-read
// the registry when installers add entries to it.
//
// Entries already on the current process's PATH are preserved and
// not duplicated; new entries are prepended so podman resolves before
// any conflicting shim earlier in PATH.
func EnsurePodmanOnPath() error {
	additions, err := podmanPathAdditions()
	if err != nil {
		return err
	}
	if strings.TrimSpace(additions) == "" {
		return nil
	}
	merged := mergePATH(os.Getenv("PATH"), additions)
	return os.Setenv("PATH", merged)
}

// podmanPathAdditions returns the PATH entries to add to the current
// process's PATH so podman becomes findable. On Windows this reads
// the merged machine- + user-scope PATH from the registry via
// PowerShell. On non-Windows (test-only) it consults the override env
// var; production non-Windows callers do not exist.
func podmanPathAdditions() (string, error) {
	if override, ok := os.LookupEnv(podmanPathAdditionsOverrideEnv); ok {
		return override, nil
	}
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf(
			"winget.EnsurePodmanOnPath: non-windows platform requires %s to be set (test-only)",
			podmanPathAdditionsOverrideEnv,
		)
	}
	return readWindowsRegistryPATH()
}

// readWindowsRegistryPATH concatenates the machine- and user-scope
// Path values from the Windows registry into a single OS-PATH string.
// If a scope is empty it is skipped; if both are empty the result is
// empty and EnsurePodmanOnPath no-ops.
func readWindowsRegistryPATH() (string, error) {
	// [Environment]::GetEnvironmentVariable("Path", "Machine" | "User")
	// reads the current registry value with REG_EXPAND_SZ expansion
	// already applied — no manual %VAR% substitution needed.
	const script = `$m = [Environment]::GetEnvironmentVariable("Path", "Machine")
$u = [Environment]::GetEnvironmentVariable("Path", "User")
if ($m -and $u) { "$m;$u" } elseif ($m) { $m } elseif ($u) { $u } else { "" }`
	cmd := exec.Command("powershell.exe", "-NoProfile",
		"-ExecutionPolicy", "Bypass", "-Command", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf(
			"powershell read of registry PATH failed (%w): %s",
			err, strings.TrimSpace(stderr.String()),
		)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// mergePATH returns currentPATH with any entries in additional that
// are not already present prepended, using the OS path separator.
// Comparison is case-insensitive so Windows entries that differ only
// in case (`C:\WINDOWS` vs `C:\Windows`) don't produce duplicates.
// Empty and whitespace-only entries are dropped.
func mergePATH(currentPATH, additional string) string {
	sep := string(os.PathListSeparator)
	have := map[string]struct{}{}
	for _, e := range strings.Split(currentPATH, sep) {
		if trimmed := strings.TrimSpace(e); trimmed != "" {
			have[strings.ToLower(trimmed)] = struct{}{}
		}
	}
	var toAdd []string
	for _, e := range strings.Split(additional, sep) {
		trimmed := strings.TrimSpace(e)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, dup := have[key]; dup {
			continue
		}
		have[key] = struct{}{}
		toAdd = append(toAdd, trimmed)
	}
	if len(toAdd) == 0 {
		return currentPATH
	}
	if currentPATH == "" {
		return strings.Join(toAdd, sep)
	}
	return strings.Join(toAdd, sep) + sep + currentPATH
}
