// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const (
	controllerPath = "/controller"
	checkLoopMode  = "__version-check-loop"

	versionCheckConfigPath = "/run/exasol-local-vm-version-check.json"

	defaultVersionCheckIdentity = "NONE"
	defaultVersionCheckInterval = 24 * time.Hour
	defaultVersionCheckTimeout  = 3 * time.Second
)

var (
	versionCheckCategory       = "Exasol 8"
	versionCheckProductVersion = ""
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == checkLoopMode {
		runCheckLoop()
		return
	}

	if versionChecksEnabled() {
		startCheckLoop()
	}

	execController(os.Args[1:])
}

func versionChecksEnabled() bool {
	config, err := loadVersionCheckConfig(versionCheckConfigPath)
	if err != nil {
		logDiagnostic("version-check loop disabled due to invalid configuration: %v", err)
		return false
	}
	return config.enabled
}

func startCheckLoop() {
	cmd := exec.Command(os.Args[0], checkLoopMode)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "exasol-local-vm-wrapper: failed to start version-check loop: %v\n", err)
	}
}

func runCheckLoop() {
	config, err := loadVersionCheckConfig(versionCheckConfigPath)
	if err != nil {
		logDiagnostic("version-check loop disabled due to invalid configuration: %v", err)
		return
	}
	if !config.enabled {
		return
	}

	client := &http.Client{Timeout: config.timeout}
	ticker := time.NewTicker(config.interval)
	defer ticker.Stop()

	runRecurringVersionChecks(context.Background(), ticker.C, func(ctx context.Context) error {
		return performVersionCheck(ctx, client, config)
	})
}

func execController(args []string) {
	controllerArgs := append([]string{controllerPath}, args...)
	if err := syscall.Exec(controllerPath, controllerArgs, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "exasol-local-vm-wrapper: failed to exec %s: %v\n", controllerPath, err)
		os.Exit(127)
	}
}

type versionCheckConfig struct {
	enabled         bool
	interval        time.Duration
	url             string
	identity        string
	operatingSystem string
	architecture    string
	category        string
	productVersion  string
	timeout         time.Duration
}

type versionCheckRuntimeConfig struct {
	Enabled         bool   `json:"enabled"`
	IntervalSeconds int    `json:"interval_seconds"`
	URL             string `json:"url"`
	Identity        string `json:"identity"`
	OperatingSystem string `json:"operating_system"`
	Architecture    string `json:"architecture"`
}

func loadVersionCheckConfig(configPath string) (versionCheckConfig, error) {
	config := versionCheckConfig{
		interval: defaultVersionCheckInterval,
		identity: defaultVersionCheckIdentity,
		category: strings.TrimSpace(versionCheckCategory),
		timeout:  defaultVersionCheckTimeout,
	}

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return config, nil
	}
	if err != nil {
		return config, fmt.Errorf("failed to read version-check config: %w", err)
	}

	var runtimeConfig versionCheckRuntimeConfig
	if err := json.Unmarshal(data, &runtimeConfig); err != nil {
		return config, fmt.Errorf("failed to parse version-check config: %w", err)
	}

	config.enabled = runtimeConfig.Enabled
	if !config.enabled {
		return config, nil
	}
	if runtimeConfig.IntervalSeconds > 0 {
		config.interval = time.Duration(runtimeConfig.IntervalSeconds) * time.Second
	}
	if identity := strings.TrimSpace(runtimeConfig.Identity); identity != "" {
		config.identity = identity
	}

	config.url = strings.TrimSpace(runtimeConfig.URL)
	config.operatingSystem = strings.TrimSpace(runtimeConfig.OperatingSystem)
	config.architecture = strings.TrimSpace(runtimeConfig.Architecture)
	config.productVersion = strings.TrimSpace(versionCheckProductVersion)

	if config.url == "" {
		return config, fmt.Errorf("url is required when version checks are enabled")
	}
	if config.operatingSystem == "" {
		return config, fmt.Errorf("operating_system is required when version checks are enabled")
	}
	if config.architecture == "" {
		return config, fmt.Errorf("architecture is required when version checks are enabled")
	}
	if config.category == "" {
		return config, fmt.Errorf("category build constant is required when version checks are enabled")
	}
	if config.productVersion == "" {
		return config, fmt.Errorf("product version build constant is required when version checks are enabled")
	}

	return config, nil
}

func buildVersionCheckURL(config versionCheckConfig) (string, error) {
	parsed, err := url.Parse(config.url)
	if err != nil {
		return "", fmt.Errorf("invalid version-check URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid version-check URL: absolute URL with scheme and host required")
	}

	query := parsed.Query()
	query.Set("category", config.category)
	query.Set("operatingSystem", config.operatingSystem)
	query.Set("architecture", config.architecture)
	query.Set("version", config.productVersion)
	query.Set("identity", config.identity)
	parsed.RawQuery = query.Encode()

	return parsed.String(), nil
}

func performVersionCheck(ctx context.Context, client *http.Client, config versionCheckConfig) error {
	requestURL, err := buildVersionCheckURL(config)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create version-check request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("version-check request failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("version-check request returned status %d", resp.StatusCode)
	}

	return nil
}

func runRecurringVersionChecks(
	ctx context.Context,
	ticks <-chan time.Time,
	check func(context.Context) error,
) {
	runBestEffortVersionCheck(ctx, check)

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			runBestEffortVersionCheck(ctx, check)
		}
	}
}

func runBestEffortVersionCheck(ctx context.Context, check func(context.Context) error) {
	if err := check(ctx); err != nil {
		logDiagnostic("version-check request failed: %v", err)
	}
}

func logDiagnostic(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "exasol-local-vm-wrapper: "+format+"\n", args...)
}
