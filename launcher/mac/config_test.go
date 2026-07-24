// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateVMConfigAcceptsGenericContract(t *testing.T) {
	// Given
	root := t.TempDir()
	control := filepath.Join(root, "control")
	data := filepath.Join(root, "data")
	for _, path := range []string{control, data} {
		if err := os.Mkdir(path, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	hook := filepath.Join(control, "start")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := validVMConfig(control, data)

	// When
	err := validateVMConfig(config)

	// Then
	if err != nil {
		t.Fatalf("expected config to be accepted, got %v", err)
	}
}

func TestValidateVMConfigRejectsSchemaAndHookVersionsIndependently(t *testing.T) {
	// Given
	root := t.TempDir()
	hook := filepath.Join(root, "start")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		mutate      func(*VMConfig)
		wantMessage string
	}{
		{
			name:        "config schema",
			mutate:      func(config *VMConfig) { config.SchemaVersion = 2 },
			wantMessage: "config schemaVersion",
		},
		{
			name:        "hook api",
			mutate:      func(config *VMConfig) { config.BootHook.APIVersion = 2 },
			wantMessage: "bootHook.apiVersion",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validVMConfig(root, root)
			test.mutate(config)

			// When
			err := validateVMConfig(config)

			// Then
			if err == nil || !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("expected %q error, got %v", test.wantMessage, err)
			}
		})
	}
}

func TestValidateVMConfigRejectsSymlinkEscapeAndTraversal(t *testing.T) {
	// Given
	root := t.TempDir()
	realShare := filepath.Join(root, "real")
	if err := os.Mkdir(realShare, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realShare, "start"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkShare := filepath.Join(root, "link")
	if err := os.Symlink(realShare, symlinkShare); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		config *VMConfig
	}{
		{name: "symlink host path", config: validVMConfig(symlinkShare, realShare)},
		{
			name: "hook traversal",
			config: func() *VMConfig {
				config := validVMConfig(realShare, realShare)
				config.BootHook.Path = "../start"
				return config
			}(),
		},
		{
			name: "guest traversal",
			config: func() *VMConfig {
				config := validVMConfig(realShare, realShare)
				config.Shares[0].GuestPath = "/mnt/../escape"
				return config
			}(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// When
			err := validateVMConfig(test.config)

			// Then
			if err == nil {
				t.Fatal("expected invalid path to be rejected")
			}
		})
	}
}

func TestValidateVMConfigRejectsDuplicateNamesAndExplicitPorts(t *testing.T) {
	// Given
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "start"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*VMConfig)
	}{
		{
			name: "share name",
			mutate: func(config *VMConfig) {
				config.Shares[1].Name = config.Shares[0].Name
			},
		},
		{
			name: "forward name",
			mutate: func(config *VMConfig) {
				config.Forwards[1].Name = config.Forwards[0].Name
			},
		},
		{
			name: "explicit endpoint",
			mutate: func(config *VMConfig) {
				config.Forwards[1].HostPort = config.Forwards[0].HostPort
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validVMConfig(root, root)
			test.mutate(config)

			// When
			err := validateVMConfig(config)

			// Then
			if err == nil {
				t.Fatal("expected duplicate to be rejected")
			}
		})
	}
}

func TestLoadVMConfigRejectsUnknownAndTrailingJSON(t *testing.T) {
	t.Parallel()

	// Given
	root := t.TempDir()
	tests := []string{
		`{"schemaVersion":1,"resources":{"cpus":2,"memoryMiB":8192},"unknown":true}`,
		`{"schemaVersion":1,"resources":{"cpus":2,"memoryMiB":8192}} {}`,
	}
	for index, content := range tests {
		path := filepath.Join(root, fmt.Sprintf("config-%d.json", index))
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}

		// When
		_, err := loadVMConfig(path)

		// Then
		if err == nil {
			t.Fatalf("expected config %q to be rejected", content)
		}
	}
}

func TestValidateVMConfigRejectsNonExecutableHook(t *testing.T) {
	t.Parallel()

	// Given
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "start"), []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := validVMConfig(root, root)

	// When
	err := validateVMConfig(config)

	// Then
	if err == nil || !strings.Contains(err.Error(), "executable") {
		t.Fatalf("expected non-executable hook error, got %v", err)
	}
}

func validVMConfig(control, data string) *VMConfig {
	return &VMConfig{
		SchemaVersion: configSchemaVersion,
		Resources:     Resources{CPUs: 2, MemoryMiB: 8192},
		Shares: []Share{
			{Name: "control", HostPath: control, GuestPath: "/mnt/control"},
			{Name: "data", HostPath: data, GuestPath: "/mnt/exa"},
		},
		Forwards: []Forward{
			{
				Name:        "ssh",
				Protocol:    "tcp",
				HostAddress: "127.0.0.1",
				HostPort:    20022,
				GuestPort:   22,
			},
			{
				Name:        "echo",
				Protocol:    "tcp",
				HostAddress: "127.0.0.1",
				HostPort:    8563,
				GuestPort:   9000,
			},
		},
		BootHook: &BootHook{APIVersion: hookAPIVersion, Share: "control", Path: "start"},
		RuntimeDisk: &RuntimeDisk{
			HostPath:       filepath.Join(filepath.Dir(control), "runtime.img"),
			InitialSizeGiB: defaultRuntimeGiB,
		},
	}
}
