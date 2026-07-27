// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	configSchemaVersion = 1
	hookAPIVersion      = 1
	stateSchemaVersion  = 1
	defaultRuntimeGiB   = 100
)

var resourceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// VMConfig is the versioned, workload-neutral input contract of local-vm.
type VMConfig struct {
	SchemaVersion int       `json:"schemaVersion"`
	Resources     Resources `json:"resources"`
	Shares        []Share   `json:"shares,omitempty"`
	Forwards      []Forward `json:"forwards,omitempty"`
	BootHook      *BootHook `json:"bootHook,omitempty"`
}

type Resources struct {
	CPUs      int `json:"cpus"`
	MemoryMiB int `json:"memoryMiB"`
}

type Share struct {
	Name      string `json:"name"`
	HostPath  string `json:"hostPath"`
	GuestPath string `json:"guestPath"`
	ReadOnly  bool   `json:"readOnly"`
}

type Forward struct {
	Name        string `json:"name"`
	Protocol    string `json:"protocol"`
	HostAddress string `json:"hostAddress"`
	HostPort    int    `json:"hostPort"`
	GuestPort   int    `json:"guestPort"`
}

type BootHook struct {
	APIVersion int    `json:"apiVersion"`
	Share      string `json:"share"`
	Path       string `json:"path"`
}

type VMPhase string

const (
	VMPhaseInitialized VMPhase = "initialized"
	VMPhaseStarting    VMPhase = "starting"
	VMPhaseRunning     VMPhase = "running"
	VMPhaseStopped     VMPhase = "stopped"
	VMPhaseDegraded    VMPhase = "degraded"
)

type HookPhase string

const (
	HookPhaseNone      HookPhase = "none"
	HookPhasePending   HookPhase = "pending"
	HookPhaseRunning   HookPhase = "running"
	HookPhaseSucceeded HookPhase = "succeeded"
	HookPhaseFailed    HookPhase = "failed"
)

type EndpointState struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type ForwardState struct {
	Name              string `json:"name"`
	Protocol          string `json:"protocol"`
	HostAddress       string `json:"hostAddress"`
	RequestedHostPort int    `json:"requestedHostPort"`
	HostPort          int    `json:"hostPort"`
	GuestPort         int    `json:"guestPort"`
	Health            string `json:"health,omitempty"`
}

type ShareState struct {
	Name      string `json:"name"`
	HostPath  string `json:"hostPath"`
	GuestPath string `json:"guestPath"`
	ReadOnly  bool   `json:"readOnly"`
	Mounted   bool   `json:"mounted"`
}

type HookState struct {
	Phase      HookPhase  `json:"phase"`
	Attempt    int        `json:"attempt"`
	ExitCode   *int       `json:"exitCode,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	LogPath    string     `json:"logPath,omitempty"`
	Message    string     `json:"message,omitempty"`
}

type ProviderState struct {
	SchemaVersion  int            `json:"schemaVersion"`
	Phase          VMPhase        `json:"phase"`
	Message        string         `json:"message,omitempty"`
	PID            int            `json:"pid,omitempty"`
	GuestIP        string         `json:"guestIP,omitempty"`
	SSH            *EndpointState `json:"ssh,omitempty"`
	PrivateKeyPath string         `json:"privateKeyPath,omitempty"`
	Forwards       []ForwardState `json:"forwards,omitempty"`
	Shares         []ShareState   `json:"shares,omitempty"`
	Hook           HookState      `json:"hook"`
	UpdatedAt      time.Time      `json:"updatedAt"`
}

func loadVMConfig(configPath string) (*VMConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read VM config %s: %w", configPath, err)
	}

	var config VMConfig
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("failed to parse VM config %s: %w", configPath, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("failed to parse VM config %s: trailing JSON data", configPath)
	}
	if err := validateVMConfig(&config); err != nil {
		return nil, fmt.Errorf("invalid VM config %s: %w", configPath, err)
	}
	return &config, nil
}

func validateVMConfig(config *VMConfig) error {
	if config == nil {
		return errors.New("config is missing")
	}
	if config.SchemaVersion != configSchemaVersion {
		return fmt.Errorf(
			"unsupported config schemaVersion %d (supported: %d)",
			config.SchemaVersion,
			configSchemaVersion,
		)
	}
	if config.Resources.CPUs <= 0 {
		return errors.New("resources.cpus must be greater than zero")
	}
	if config.Resources.MemoryMiB <= 0 {
		return errors.New("resources.memoryMiB must be greater than zero")
	}

	shares := make(map[string]Share, len(config.Shares))
	guestPaths := make(map[string]string, len(config.Shares))
	for index := range config.Shares {
		share := &config.Shares[index]
		if !resourceNamePattern.MatchString(share.Name) {
			return fmt.Errorf("shares[%d].name %q is invalid", index, share.Name)
		}
		if _, exists := shares[share.Name]; exists {
			return fmt.Errorf("duplicate share name %q", share.Name)
		}
		canonical, err := canonicalExistingPath(share.HostPath)
		if err != nil {
			return fmt.Errorf("shares[%d].hostPath: %w", index, err)
		}
		share.HostPath = canonical
		if !isCanonicalAbsoluteGuestPath(share.GuestPath) {
			return fmt.Errorf(
				"shares[%d].guestPath %q must be an absolute canonical path",
				index,
				share.GuestPath,
			)
		}
		if previous, exists := guestPaths[share.GuestPath]; exists {
			return fmt.Errorf(
				"shares %q and %q use duplicate guestPath %q",
				previous,
				share.Name,
				share.GuestPath,
			)
		}
		shares[share.Name] = *share
		guestPaths[share.GuestPath] = share.Name
	}

	if config.BootHook != nil {
		if config.BootHook.APIVersion != hookAPIVersion {
			return fmt.Errorf(
				"unsupported bootHook.apiVersion %d (supported: %d)",
				config.BootHook.APIVersion,
				hookAPIVersion,
			)
		}
		if _, exists := shares[config.BootHook.Share]; !exists {
			return fmt.Errorf("bootHook.share %q does not name a configured share", config.BootHook.Share)
		}
		if !isCanonicalRelativePath(config.BootHook.Path) {
			return fmt.Errorf(
				"bootHook.path %q must be a canonical relative path without traversal",
				config.BootHook.Path,
			)
		}
		hookShare := shares[config.BootHook.Share]
		hookHostPath := filepath.Join(hookShare.HostPath, filepath.FromSlash(config.BootHook.Path))
		if !pathWithin(hookShare.HostPath, hookHostPath) {
			return fmt.Errorf("bootHook.path %q escapes share %q", config.BootHook.Path, config.BootHook.Share)
		}
		canonicalHook, err := canonicalExistingPath(hookHostPath)
		if err != nil {
			return fmt.Errorf("bootHook.path: %w", err)
		}
		if !pathWithin(hookShare.HostPath, canonicalHook) {
			return fmt.Errorf("bootHook.path %q resolves outside share %q", config.BootHook.Path, config.BootHook.Share)
		}
		info, err := os.Stat(canonicalHook)
		if err != nil {
			return fmt.Errorf("bootHook.path: %w", err)
		}
		if !info.Mode().IsRegular() {
			return errors.New("bootHook.path must name a regular file")
		}
		if info.Mode().Perm()&0o111 == 0 {
			return errors.New("bootHook.path must be executable")
		}
	}

	forwardNames := make(map[string]struct{}, len(config.Forwards))
	hostEndpoints := make(map[string]string, len(config.Forwards))
	hasSSHForward := false
	for index := range config.Forwards {
		forward := &config.Forwards[index]
		if !resourceNamePattern.MatchString(forward.Name) {
			return fmt.Errorf("forwards[%d].name %q is invalid", index, forward.Name)
		}
		if _, exists := forwardNames[forward.Name]; exists {
			return fmt.Errorf("duplicate forward name %q", forward.Name)
		}
		forwardNames[forward.Name] = struct{}{}
		if strings.ToLower(forward.Protocol) != "tcp" {
			return fmt.Errorf("forwards[%d].protocol %q is unsupported; only tcp is supported", index, forward.Protocol)
		}
		forward.Protocol = "tcp"
		if forward.HostAddress == "" {
			forward.HostAddress = "127.0.0.1"
		}
		hostIP := net.ParseIP(forward.HostAddress)
		if hostIP == nil || !hostIP.IsLoopback() {
			return fmt.Errorf("forwards[%d].hostAddress must be a loopback IP address", index)
		}
		if forward.HostPort < 0 || forward.HostPort > 65535 {
			return fmt.Errorf("forwards[%d].hostPort must be between 0 and 65535", index)
		}
		if forward.GuestPort <= 0 || forward.GuestPort > 65535 {
			return fmt.Errorf("forwards[%d].guestPort must be between 1 and 65535", index)
		}
		if forward.Name == "ssh" {
			if forward.GuestPort != 22 {
				return errors.New("the ssh forward must target guest port 22")
			}
			hasSSHForward = true
		}
		if forward.HostPort != 0 {
			endpoint := net.JoinHostPort(forward.HostAddress, fmt.Sprint(forward.HostPort))
			if previous, exists := hostEndpoints[endpoint]; exists {
				return fmt.Errorf(
					"forwards %q and %q use duplicate host endpoint %s",
					previous,
					forward.Name,
					endpoint,
				)
			}
			hostEndpoints[endpoint] = forward.Name
		}
	}
	if !hasSSHForward {
		return errors.New(`a TCP forward named "ssh" targeting guest port 22 is required`)
	}

	return nil
}

func canonicalExistingPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%q is not absolute", path)
	}
	clean := filepath.Clean(path)
	if clean != path {
		return "", fmt.Errorf("%q is not canonical", path)
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("failed to resolve %q: %w", path, err)
	}
	if resolved != clean {
		return "", fmt.Errorf("%q resolves through a symlink to %q", path, resolved)
	}
	return clean, nil
}

func isCanonicalAbsoluteGuestPath(path string) bool {
	return strings.HasPrefix(path, "/") && path != "/" && filepath.Clean(path) == path
}

func isCanonicalRelativePath(path string) bool {
	if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	return path != "." && path != ".." && !strings.HasPrefix(path, "../")
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
