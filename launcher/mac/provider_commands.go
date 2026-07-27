// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	providerStateFileName    = "vm-state.json"
	providerContractFileName = "vm-contract.json"
	hookLogFileName          = "hook.log"
)

type vmContractIdentity struct {
	SchemaVersion int     `json:"schemaVersion"`
	Shares        []Share `json:"shares"`
}

func initConfigCmd(config *VMConfig) error {
	return initConfigWith(config, initCmd)
}

func initConfigWith(config *VMConfig, initialize func(bool) error) error {
	if _, err := os.Stat("vm"); err == nil {
		if _, contractErr := os.Stat(providerContractFileName); contractErr == nil {
			return validateExistingVMContract(config)
		} else if !errors.Is(contractErr, os.ErrNotExist) {
			return fmt.Errorf("failed to inspect VM ownership contract: %w", contractErr)
		}
		if isVMRunning() {
			return errors.New("cannot upgrade pre-contract VM state while its VM is running")
		}
		if err := initialize(true); err != nil {
			return fmt.Errorf("failed to refresh pre-contract VM assets: %w", err)
		}
		return writeVMContract(config)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to inspect VM state: %w", err)
	}
	if err := initialize(false); err != nil {
		return err
	}
	return writeVMContract(config)
}

func validateExistingVMContract(config *VMConfig) error {
	data, err := os.ReadFile(providerContractFileName)
	if errors.Is(err, os.ErrNotExist) {
		return writeVMContract(config)
	}
	if err != nil {
		return fmt.Errorf("failed to read existing VM contract: %w", err)
	}
	var existing vmContractIdentity
	if err := json.Unmarshal(data, &existing); err != nil {
		return fmt.Errorf("failed to parse existing VM contract: %w", err)
	}
	candidate := vmContractIdentity{
		SchemaVersion: config.SchemaVersion,
		Shares:        config.Shares,
	}
	existingJSON, _ := json.Marshal(existing)
	candidateJSON, _ := json.Marshal(candidate)
	if !bytes.Equal(existingJSON, candidateJSON) {
		return errors.New(
			"configured shares are incompatible with the initialized VM; " +
				"destroy and recreate VM-owned state without deleting caller data",
		)
	}
	return nil
}

func writeVMContract(config *VMConfig) error {
	identity := vmContractIdentity{
		SchemaVersion: config.SchemaVersion,
		Shares:        config.Shares,
	}
	data, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(providerContractFileName, data, 0o600)
}

func prepareProviderDisk() error {
	return ensureDataDisk(filepath.Join("vm", "data.img"), defaultRuntimeGiB)
}

func runConfigVMDaemon(configPath string) error {
	config, err := loadVMConfig(configPath)
	if err != nil {
		return err
	}
	return runVMDaemonConfig(config)
}

func statusConfigCmd(output io.Writer) error {
	state, err := readProviderState()
	if errors.Is(err, os.ErrNotExist) {
		return writeStoppedState(output)
	}
	if err != nil {
		return err
	}
	if !isVMRunning() {
		state.Phase = VMPhaseStopped
		state.PID = 0
	}
	state.UpdatedAt = time.Now().UTC()
	return json.NewEncoder(output).Encode(state)
}

func healthCheckConfigCmd(output io.Writer) error {
	state, err := readProviderState()
	if errors.Is(err, os.ErrNotExist) {
		return writeStoppedState(output)
	}
	if err != nil {
		return err
	}
	if !isVMRunning() {
		state.Phase = VMPhaseStopped
		state.PID = 0
		state.UpdatedAt = time.Now().UTC()
		return json.NewEncoder(output).Encode(state)
	}
	health, healthErr := queryHealthCheck()
	for index := range state.Forwards {
		if port, exists := health[state.Forwards[index].Name]; exists {
			state.Forwards[index].Health = port.State
		}
	}
	if healthErr != nil {
		state.Phase = VMPhaseDegraded
		state.Message = fmt.Sprintf("failed to query provider health: %v", healthErr)
	} else if state.Hook.Phase == HookPhaseNone ||
		state.Hook.Phase == HookPhaseSucceeded {
		state.Phase = VMPhaseRunning
		state.Message = ""
	}
	state.UpdatedAt = time.Now().UTC()
	return json.NewEncoder(output).Encode(state)
}

func readProviderState() (*ProviderState, error) {
	data, err := os.ReadFile(providerStateFileName)
	if err != nil {
		return nil, err
	}
	var state ProviderState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse provider state: %w", err)
	}
	if state.SchemaVersion != stateSchemaVersion {
		return nil, fmt.Errorf("unsupported provider state schema %d", state.SchemaVersion)
	}
	return &state, nil
}

func writeProviderState(state *ProviderState) error {
	state.SchemaVersion = stateSchemaVersion
	state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(providerStateFileName, data, 0o600)
}

func writeConfiguredProviderState(
	config *VMConfig,
	guestIP, privateKey string,
	forwards []ForwardState,
	sharesMounted bool,
	cause error,
) error {
	keyPath, err := filepath.Abs(privateKey)
	if err != nil {
		return fmt.Errorf("failed to resolve SSH private key path: %w", err)
	}
	phase := VMPhaseRunning
	if cause != nil {
		phase = VMPhaseDegraded
	}
	hook := HookState{Phase: HookPhaseNone}
	if config.BootHook != nil {
		hook.Phase = HookPhasePending
	}
	state := &ProviderState{
		SchemaVersion:  stateSchemaVersion,
		Phase:          phase,
		PID:            os.Getpid(),
		GuestIP:        guestIP,
		PrivateKeyPath: keyPath,
		Forwards:       forwards,
		Hook:           hook,
	}
	for _, forward := range forwards {
		if forward.Name == "ssh" {
			state.SSH = &EndpointState{
				Address: forward.HostAddress,
				Port:    forward.HostPort,
			}
		}
	}
	for _, share := range config.Shares {
		state.Shares = append(state.Shares, ShareState{
			Name:      share.Name,
			HostPath:  share.HostPath,
			GuestPath: share.GuestPath,
			ReadOnly:  share.ReadOnly,
			Mounted:   sharesMounted,
		})
	}
	if cause != nil {
		state.Message = cause.Error()
	}
	return writeProviderState(state)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func runBootHook(config *VMConfig) error {
	if config.BootHook == nil {
		return nil
	}
	state, err := readProviderState()
	if err != nil {
		return err
	}
	var hookShare *Share
	for index := range config.Shares {
		if config.Shares[index].Name == config.BootHook.Share {
			hookShare = &config.Shares[index]
			break
		}
	}
	if hookShare == nil {
		return errors.New("boot hook share is missing after validation")
	}
	hookGuestPath := filepath.ToSlash(filepath.Join(hookShare.GuestPath, config.BootHook.Path))
	logPath, err := filepath.Abs(hookLogFileName)
	if err != nil {
		return err
	}
	attempt := state.Hook.Attempt + 1
	started := time.Now().UTC()
	state.Hook = HookState{
		Phase:     HookPhaseRunning,
		Attempt:   attempt,
		StartedAt: &started,
		LogPath:   logPath,
	}
	if err := writeProviderState(state); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	args, err := providerSSHArgs(state)
	if err != nil {
		return finishHook(state, HookPhaseFailed, nil, err)
	}
	args = append(args, hookGuestPath)
	command := exec.Command("ssh", args...)
	command.Stdout = io.MultiWriter(os.Stdout, logFile)
	command.Stderr = io.MultiWriter(os.Stderr, logFile)
	runErr := command.Run()
	if runErr != nil {
		return finishHook(state, HookPhaseFailed, exitCode(runErr), runErr)
	}
	code := 0
	return finishHook(state, HookPhaseSucceeded, &code, nil)
}

func finishHook(
	state *ProviderState,
	phase HookPhase,
	code *int,
	runErr error,
) error {
	finished := time.Now().UTC()
	state.Hook.Phase = phase
	state.Hook.ExitCode = code
	state.Hook.FinishedAt = &finished
	state.Hook.Message = ""
	if runErr != nil {
		state.Hook.Message = runErr.Error()
		state.Phase = VMPhaseDegraded
	} else {
		state.Message = ""
		state.Phase = VMPhaseRunning
	}
	if err := writeProviderState(state); err != nil {
		return err
	}
	if runErr != nil {
		return fmt.Errorf("boot hook failed; VM remains running for diagnostics: %w", runErr)
	}
	return nil
}

func exitCode(err error) *int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		return &code
	}
	return nil
}

func providerSSHArgs(state *ProviderState) ([]string, error) {
	if state.SSH == nil || state.SSH.Port <= 0 {
		return nil, errors.New("provider state has no SSH endpoint")
	}
	if state.PrivateKeyPath == "" {
		return nil, errors.New("provider state has no SSH private key")
	}
	return []string{
		"-i", state.PrivateKeyPath,
		"-p", strconv.Itoa(state.SSH.Port),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes",
		"root@" + state.SSH.Address,
	}, nil
}

func mountConfiguredShares(config *VMConfig, vmIP, privateKey string) error {
	for _, share := range config.Shares {
		options := "rw"
		if share.ReadOnly {
			options = "ro"
		}
		command := "mkdir -p " + shellQuote(share.GuestPath) +
			" && mountpoint -q " + shellQuote(share.GuestPath) +
			" || mount -t virtiofs -o " + options + " " +
			shellQuote(shareTag(share.Name)) + " " + shellQuote(share.GuestPath)
		args := directSSHArgs(vmIP, privateKey)
		args = append(args, command)
		output, err := exec.Command("ssh", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf(
				"failed to mount share %q at %s: %w: %s",
				share.Name,
				share.GuestPath,
				err,
				strings.TrimSpace(string(output)),
			)
		}
	}
	return nil
}

func directSSHArgs(vmIP, privateKey string) []string {
	return []string{
		"-i", privateKey,
		"-p", "22",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"root@" + vmIP,
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func shareTag(name string) string {
	return "share-" + name
}
