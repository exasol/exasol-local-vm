// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func runCLI(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(
			"usage: local-vm {init|start|stop|status|health-check|destroy|version}",
		)
	}
	switch args[0] {
	case "init", "start":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		flags.SetOutput(stderr)
		stateDir := flags.String("state-dir", "", "VM-owned state directory")
		configPath := flags.String("config", "", "versioned VM configuration")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("%s does not accept positional arguments", args[0])
		}
		resolvedStateDir, err := canonicalStateDir(*stateDir, true)
		if err != nil {
			return err
		}
		resolvedConfig, err := canonicalConfigPath(*configPath)
		if err != nil {
			return err
		}
		config, err := loadVMConfig(resolvedConfig)
		if err != nil {
			return err
		}
		if err := os.Chdir(resolvedStateDir); err != nil {
			return fmt.Errorf("failed to enter state directory %s: %w", resolvedStateDir, err)
		}
		if args[0] == "init" {
			return initConfigCmd(config)
		}
		return startConfigCmd(config, resolvedConfig)
	case "stop", "destroy":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		flags.SetOutput(stderr)
		stateDir := flags.String("state-dir", "", "VM-owned state directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("%s does not accept positional arguments", args[0])
		}
		resolvedStateDir, err := canonicalStateDir(*stateDir, false)
		if err != nil {
			if (args[0] == "stop" || args[0] == "destroy") &&
				errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if err := os.Chdir(resolvedStateDir); err != nil {
			return fmt.Errorf("failed to enter state directory %s: %w", resolvedStateDir, err)
		}
		if args[0] == "stop" {
			if err := validateStateOwnership("stop"); err != nil {
				return err
			}
			return stopCmd()
		}
		return destroyConfigCmd(resolvedStateDir)
	case "status", "health-check":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		flags.SetOutput(stderr)
		stateDir := flags.String("state-dir", "", "VM-owned state directory")
		jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("%s does not accept positional arguments", args[0])
		}
		if !*jsonOutput {
			return fmt.Errorf("%s requires --json", args[0])
		}
		resolvedStateDir, err := canonicalStateDir(*stateDir, false)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return writeStoppedState(stdout)
			}
			return err
		}
		if err := os.Chdir(resolvedStateDir); err != nil {
			return fmt.Errorf("failed to enter state directory %s: %w", resolvedStateDir, err)
		}
		if args[0] == "health-check" {
			return healthCheckConfigCmd(stdout)
		}
		return statusConfigCmd(stdout)
	case "version":
		flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
		flags.SetOutput(stderr)
		jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || !*jsonOutput {
			return errors.New("version requires --json")
		}
		return json.NewEncoder(stdout).Encode(map[string]any{
			"version":             providerVersion,
			"configSchemaVersion": configSchemaVersion,
			"hookAPIVersion":      hookAPIVersion,
			"stateSchemaVersion":  stateSchemaVersion,
		})
	case "__daemon__":
		if len(args) != 3 {
			return errors.New("invalid internal daemon arguments")
		}
		if err := os.Chdir(args[1]); err != nil {
			return err
		}
		return runConfigVMDaemon(args[2])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func canonicalStateDir(path string, create bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("--state-dir is required")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("--state-dir must be an absolute canonical path")
	}
	if create {
		if err := validateCreatableDirectory(path); err != nil {
			return "", err
		}
		if err := os.MkdirAll(path, 0o750); err != nil {
			return "", fmt.Errorf("failed to create state directory: %w", err)
		}
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve state directory %s: %w", path, err)
	}
	if resolved != path {
		return "", fmt.Errorf("state directory %s resolves through a symlink to %s", path, resolved)
	}
	return path, nil
}

func validateCreatableDirectory(path string) error {
	ancestor := path
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to inspect state directory ancestor %s: %w", ancestor, err)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return fmt.Errorf("failed to locate an existing state directory ancestor for %s", path)
		}
		ancestor = parent
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return fmt.Errorf("failed to resolve state directory ancestor %s: %w", ancestor, err)
	}
	if resolved != ancestor {
		return fmt.Errorf(
			"state directory ancestor %s resolves through a symlink to %s",
			ancestor,
			resolved,
		)
	}
	return nil
}

func canonicalConfigPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("--config is required")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("--config must be an absolute canonical path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve config path %s: %w", path, err)
	}
	if resolved != path {
		return "", fmt.Errorf("config path %s resolves through a symlink to %s", path, resolved)
	}
	return path, nil
}

func startConfigCmd(config *VMConfig, configPath string) error {
	activeStartConfigPath = configPath
	if isVMRunning() {
		// A failed or cancelled hook intentionally leaves the VM running.
		// Re-running start reconciles that hook without replacing the VM.
		return runBootHook(config)
	}
	if err := prepareRuntimeDisk(config); err != nil {
		return err
	}
	if err := startCmd(
		strconv.Itoa(config.Resources.CPUs),
		strconv.Itoa(config.Resources.MemoryMiB),
	); err != nil {
		return err
	}
	return runBootHook(config)
}

func destroyConfigCmd(stateDir string) error {
	if err := validateDestroyTarget(); err != nil {
		return err
	}
	stopErr := stopCmd()
	if stopErr != nil && isVMRunning() {
		return stopErr
	}
	parent := filepath.Dir(stateDir)
	if err := os.Chdir(parent); err != nil {
		return fmt.Errorf("failed to leave state directory before destroy: %w", err)
	}
	if err := os.RemoveAll(stateDir); err != nil {
		return fmt.Errorf("failed to remove VM-owned state directory %s: %w", stateDir, err)
	}
	return syncDirectory(parent)
}

func validateDestroyTarget() error {
	return validateStateOwnership("destroy")
}

func validateStateOwnership(operation string) error {
	data, err := os.ReadFile(providerContractFileName)
	if err != nil {
		return fmt.Errorf(
			"refusing to %s state without local-vm ownership metadata: %w",
			operation,
			err,
		)
	}
	var contract vmContractIdentity
	if err := json.Unmarshal(data, &contract); err != nil {
		return fmt.Errorf(
			"refusing to %s state with invalid ownership metadata: %w",
			operation,
			err,
		)
	}
	if contract.SchemaVersion != configSchemaVersion {
		return fmt.Errorf(
			"refusing to %s state with unsupported schema version %d",
			operation,
			contract.SchemaVersion,
		)
	}
	return nil
}

func writeStoppedState(output io.Writer) error {
	return json.NewEncoder(output).Encode(ProviderState{
		SchemaVersion: stateSchemaVersion,
		Phase:         VMPhaseStopped,
		Hook:          HookState{Phase: HookPhaseNone},
	})
}
