// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

// Package main is the windows-runner launcher: a CLI that delegates
// container lifecycle to a natively installed podman-for-windows so the
// same on-disk contract and integration tests used by the mac launcher can
// be reused on windows.
//
// Phase 2 skeleton: this file locks in the subcommand surface (matching
// launcher/mac/main.go byte-for-byte where the semantics carry over) but
// every subcommand body is a stub that returns "not implemented on windows
// yet". Later phases fill each stub in.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
)

// InitOutput is the JSON shape written by the guest-side init scripts on
// mac. It is mirrored here so windows can produce compatible state files
// once start is implemented; kept small and pure-data so the skeleton has
// no external dependencies.
type InitOutput struct {
	IP    string         `json:"ip"`
	Ports map[string]int `json:"ports"`
}

// RuntimeConfig matches the mac vm-config.json schema; ssh_private_key is
// left empty on windows because there is no guest VM to SSH into.
type RuntimeConfig struct {
	SSHPrivateKey string `json:"ssh_private_key"`
}

// VersionCheckRuntimeConfig mirrors the mac schema so any shared tooling
// that reads the file finds the same shape. Windows may or may not emit
// this file — that is decided in a later phase.
type VersionCheckRuntimeConfig struct {
	Enabled         bool   `json:"enabled"`
	IntervalSeconds int    `json:"interval_seconds"`
	Identity        string `json:"identity"`
	URL             string `json:"url"`
	OperatingSystem string `json:"operating_system"`
}

// VersionCheckOptions carries the parsed --version-check-* flag values.
type VersionCheckOptions struct {
	Enabled         bool
	IntervalSeconds int
	Identity        string
	URL             string
}

const (
	defaultVersionCheckIntervalSeconds = 86400
	defaultVersionCheckIdentity        = "NONE"
)

var defaultVersionCheckURL = "https://metrics-test.exasol.com/v1/version-check"

func defaultVersionCheckOptions() VersionCheckOptions {
	return VersionCheckOptions{
		Enabled:         true,
		IntervalSeconds: defaultVersionCheckIntervalSeconds,
		Identity:        defaultVersionCheckIdentity,
		URL:             defaultVersionCheckURL,
	}
}

// errNotImplemented is returned by every subcommand until later phases
// wire in real behavior. Kept as a sentinel so tests can assert on it.
func errNotImplemented(subcommand string) error {
	return fmt.Errorf("%s: not implemented on windows yet", subcommand)
}

func initCmd(sshKeyPath string) error {
	_ = sshKeyPath
	return errNotImplemented("init")
}

func startCmd(
	cpuCountStr string,
	ramSizeStr string,
	dataSizeGB int,
	portsOverride string,
	versionCheckOptions VersionCheckOptions,
) error {
	_ = cpuCountStr
	_ = ramSizeStr
	_ = dataSizeGB
	_ = portsOverride
	_ = versionCheckOptions
	return errNotImplemented("start")
}

func stopCmd() error {
	return errNotImplemented("stop")
}

func statusCmd() error {
	return errNotImplemented("status")
}

func resizeDataDiskCmd(sizeArg string) error {
	_ = sizeArg
	return errNotImplemented("resize-data")
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: windows-runner <command> [options]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Commands:")
		fmt.Fprintln(os.Stderr, "  init [--ssh-key <private-key>]    Initialize launcher working directory")
		fmt.Fprintln(os.Stderr, "  start [--ports <svc>:<port>,...] <cpu> <ram> <data_size_gb>")
		fmt.Fprintln(os.Stderr, "                                    Start the DB container via podman-for-windows.")
		fmt.Fprintln(os.Stderr, "                                    CPU count, RAM size (MB), and data disk size in")
		fmt.Fprintln(os.Stderr, "                                    GB are accepted for CLI parity with the mac")
		fmt.Fprintln(os.Stderr, "                                    launcher; podman sizes its own backing VM.")
		fmt.Fprintln(os.Stderr, "                                    --ports overrides which host port is bound")
		fmt.Fprintln(os.Stderr, "                                    for a named service (e.g. --ports db:9090).")
		fmt.Fprintln(os.Stderr, "                                    Unspecified services use the same port as the")
		fmt.Fprintln(os.Stderr, "                                    container, falling back to a random port if")
		fmt.Fprintln(os.Stderr, "                                    unavailable.")
		fmt.Fprintln(os.Stderr, "  stop                              Stop the running DB container")
		fmt.Fprintln(os.Stderr, "  status                            Print JSON {\"running\": bool}")
		fmt.Fprintln(os.Stderr, "  resize-data <size>                Record a new data size in GB (container must be stopped)")
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "init":
		initFlags := flag.NewFlagSet("init", flag.ContinueOnError)
		initFlags.SetOutput(os.Stderr)
		sshKeyPath := initFlags.String("ssh-key", "", "Use an existing SSH private key instead of generating one")
		initFlags.Usage = func() {
			fmt.Fprintln(os.Stderr, "Usage: windows-runner init [--ssh-key <private-key>]")
			initFlags.PrintDefaults()
		}
		if parseErr := initFlags.Parse(os.Args[2:]); parseErr != nil {
			os.Exit(2)
		}
		if initFlags.NArg() != 0 {
			fmt.Fprintf(os.Stderr, "Unexpected init argument: %s\n", initFlags.Arg(0))
			initFlags.Usage()
			os.Exit(2)
		}
		err = initCmd(*sshKeyPath)
	case "start":
		startFlags := flag.NewFlagSet("start", flag.ContinueOnError)
		startFlags.SetOutput(os.Stderr)
		portsFlag := startFlags.String("ports", "", "Host port overrides: <service>:<port>[,<service>:<port>...]")
		versionCheckOptions := defaultVersionCheckOptions()
		startFlags.BoolVar(&versionCheckOptions.Enabled, "version-check-enabled", versionCheckOptions.Enabled, "Enable scheduled local database version checks")
		startFlags.IntVar(&versionCheckOptions.IntervalSeconds, "version-check-interval-seconds", versionCheckOptions.IntervalSeconds, "Interval in seconds for scheduled local database version checks")
		startFlags.StringVar(&versionCheckOptions.Identity, "version-check-identity", versionCheckOptions.Identity, "Identity string for scheduled local database version checks")
		startFlags.StringVar(&versionCheckOptions.URL, "version-check-url", versionCheckOptions.URL, "Version-check URL override for scheduled local database version checks")
		startFlags.Usage = func() {
			fmt.Fprintln(os.Stderr, "Usage: windows-runner start [--ports <service>:<port>,...] <cpu_count> <ram_size> <data_size_gb>")
			startFlags.PrintDefaults()
		}
		if parseErr := startFlags.Parse(os.Args[2:]); parseErr != nil {
			os.Exit(2)
		}
		if startFlags.NArg() != 3 {
			fmt.Fprintf(os.Stderr, "Error: expected 3 positional arguments, got %d\n", startFlags.NArg())
			startFlags.Usage()
			os.Exit(2)
		}
		dataSizeGB, parseErr := strconv.Atoi(startFlags.Arg(2))
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid data_size_gb: %v\n", parseErr)
			os.Exit(1)
		}
		if dataSizeGB <= 0 {
			fmt.Fprintln(os.Stderr, "Error: data_size_gb must be a positive integer")
			os.Exit(1)
		}
		err = startCmd(startFlags.Arg(0), startFlags.Arg(1), dataSizeGB, *portsFlag, versionCheckOptions)
	case "stop":
		err = stopCmd()
	case "status":
		err = statusCmd()
	case "resize-data":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Usage: windows-runner resize-data <new_size_gb>")
			os.Exit(1)
		}
		err = resizeDataDiskCmd(os.Args[2])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		fmt.Fprintln(os.Stderr, "Available commands: init, start, stop, status, resize-data")
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
