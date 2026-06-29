// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBuildVersionCheckURLIncludesEncodedQueryParameters(t *testing.T) {
	config := validVersionCheckConfig("https://metrics.example.test/v1/version-check?existing=value")
	config.identity = "exasol-personal;deployment-id;small;default"

	requestURL, err := buildVersionCheckURL(config)
	if err != nil {
		t.Fatalf("expected URL to be built, got error: %v", err)
	}

	parsed, err := url.Parse(requestURL)
	if err != nil {
		t.Fatalf("failed to parse built URL: %v", err)
	}
	query := parsed.Query()

	assertQueryParameter(t, query, "existing", "value")
	assertQueryParameter(t, query, "category", "Exasol 8")
	assertQueryParameter(t, query, "operatingSystem", "MacOS")
	assertQueryParameter(t, query, "architecture", "arm64")
	assertQueryParameter(t, query, "version", "2026.2.0-nano.1")
	assertQueryParameter(t, query, "identity", "exasol-personal;deployment-id;small;default")
}

func TestLoadVersionCheckConfigDefaultsMissingIdentityToNone(t *testing.T) {
	withStaticVersionCheckMetadata(t, "Exasol 8", "2026.2.0-nano.1")
	configPath := writeRuntimeConfig(t, versionCheckRuntimeConfig{
		Enabled:         true,
		URL:             "https://metrics.example.test/v1/version-check",
		OperatingSystem: "Linux",
		Architecture:    "x86_64",
	})

	config, err := loadVersionCheckConfig(configPath)
	if err != nil {
		t.Fatalf("expected config to load, got error: %v", err)
	}

	if config.identity != defaultVersionCheckIdentity {
		t.Fatalf("expected default identity %q, got %q", defaultVersionCheckIdentity, config.identity)
	}
}

func TestLoadVersionCheckConfigMissingFileDisablesChecks(t *testing.T) {
	config, err := loadVersionCheckConfig(
		t.TempDir() + "/missing-version-check.json",
	)
	if err != nil {
		t.Fatalf("expected missing config to load as disabled, got error: %v", err)
	}

	if config.enabled {
		t.Fatal("expected version checks to be disabled")
	}
}

func TestLoadVersionCheckConfigDisabledDoesNotRequireRuntimeInputs(t *testing.T) {
	configPath := writeRuntimeConfig(t, versionCheckRuntimeConfig{
		Enabled: true,
	})

	config, err := loadVersionCheckConfig(configPath)
	if err == nil {
		t.Fatal("expected enabled config without required fields to fail")
	}
	if !strings.Contains(err.Error(), "url is required") {
		t.Fatalf("unexpected error: %v", err)
	}

	configPath = writeRuntimeConfig(t, versionCheckRuntimeConfig{
		Enabled: false,
	})
	config, err = loadVersionCheckConfig(configPath)
	if err != nil {
		t.Fatalf("expected disabled config to load without runtime inputs, got error: %v", err)
	}
	if config.enabled {
		t.Fatal("expected version checks to be disabled")
	}
}

func TestLoadVersionCheckConfigRequiresBuildMetadata(t *testing.T) {
	withStaticVersionCheckMetadata(t, "Exasol 8", "")
	configPath := writeRuntimeConfig(t, versionCheckRuntimeConfig{
		Enabled:         true,
		URL:             "https://metrics.example.test/v1/version-check",
		OperatingSystem: "Linux",
		Architecture:    "x86_64",
	})

	_, err := loadVersionCheckConfig(configPath)
	if err == nil {
		t.Fatal("expected enabled config without product version metadata to fail")
	}
	if !strings.Contains(err.Error(), "product version build constant") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadVersionCheckConfigUsesConfiguredIntervalAndTimeout(t *testing.T) {
	withStaticVersionCheckMetadata(t, "Exasol 8", "2026.2.0-nano.1")
	configPath := writeRuntimeConfig(t, versionCheckRuntimeConfig{
		Enabled:         true,
		IntervalSeconds: 7,
		URL:             "https://metrics.example.test/v1/version-check",
		OperatingSystem: "Windows",
		Architecture:    "x86_64",
	})

	config, err := loadVersionCheckConfig(configPath)
	if err != nil {
		t.Fatalf("expected config to load, got error: %v", err)
	}

	if config.interval != 7*time.Second {
		t.Fatalf("expected interval 7s, got %s", config.interval)
	}
	if config.timeout != defaultVersionCheckTimeout {
		t.Fatalf("expected default timeout %s, got %s", defaultVersionCheckTimeout, config.timeout)
	}
}

func TestPerformVersionCheckUsesLoadedRuntimeConfigValues(t *testing.T) {
	withStaticVersionCheckMetadata(t, "Exasol 8", "2026.2.0-nano.1")
	configPath := writeRuntimeConfig(t, versionCheckRuntimeConfig{
		Enabled:         true,
		IntervalSeconds: 5,
		URL:             "https://override.example.test/v1/version-check?source=runner",
		Identity:        "exasol-personal;deployment;small;default",
		OperatingSystem: "MacOS",
		Architecture:    "arm64",
	})
	config, err := loadVersionCheckConfig(configPath)
	if err != nil {
		t.Fatalf("expected config to load, got error: %v", err)
	}

	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "override.example.test" {
			t.Errorf("expected override host, got %s", request.URL.Host)
		}
		query := request.URL.Query()
		assertQueryParameter(t, query, "source", "runner")
		assertQueryParameter(t, query, "identity", "exasol-personal;deployment;small;default")
		assertQueryParameter(t, query, "operatingSystem", "MacOS")
		assertQueryParameter(t, query, "architecture", "arm64")
		assertQueryParameter(t, query, "category", "Exasol 8")
		assertQueryParameter(t, query, "version", "2026.2.0-nano.1")

		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})}

	if err := performVersionCheck(context.Background(), client, config); err != nil {
		t.Fatalf("expected version check to succeed, got error: %v", err)
	}
}

func TestRunRecurringVersionChecksRunsImmediatelyAndOnIntervalTicksAfterFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ticks := make(chan time.Time)
	calls := make(chan struct{}, 3)
	done := make(chan struct{})

	go func() {
		runRecurringVersionChecks(ctx, ticks, func(context.Context) error {
			calls <- struct{}{}
			return errors.New("simulated failure")
		})
		close(done)
	}()

	waitForCall(t, calls)
	sendTick(t, ticks)
	waitForCall(t, calls)
	sendTick(t, ticks)
	waitForCall(t, calls)

	cancel()
	waitForDone(t, done)
}

func TestPerformVersionCheckSendsExpectedRequest(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet {
			t.Errorf("expected GET request, got %s", request.Method)
		}
		if request.URL.Path != "/v1/version-check" {
			t.Errorf("expected /v1/version-check path, got %s", request.URL.Path)
		}

		query := request.URL.Query()
		assertQueryParameter(t, query, "category", "Exasol 8")
		assertQueryParameter(t, query, "operatingSystem", "Linux")
		assertQueryParameter(t, query, "architecture", "x86_64")
		assertQueryParameter(t, query, "version", "2026.2.0-nano.1")
		assertQueryParameter(t, query, "identity", "NONE")

		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})}

	config := validVersionCheckConfig("https://metrics.example.test/v1/version-check")
	config.operatingSystem = "Linux"
	config.architecture = "x86_64"
	config.identity = defaultVersionCheckIdentity

	if err := performVersionCheck(context.Background(), client, config); err != nil {
		t.Fatalf("expected version check to succeed, got error: %v", err)
	}
}

func TestPerformVersionCheckReturnsNonSuccessStatusAsError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(strings.NewReader("not ready")),
		}, nil
	})}

	err := performVersionCheck(context.Background(), client, validVersionCheckConfig("https://metrics.example.test"))
	if err == nil {
		t.Fatal("expected non-success status to return an error")
	}
	if !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("expected error to include status 503, got %q", err.Error())
	}
}

func TestPerformVersionCheckReturnsNetworkFailuresAsError(t *testing.T) {
	config := validVersionCheckConfig("https://metrics.example.test/v1/version-check")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	})}

	err := performVersionCheck(context.Background(), client, config)
	if err == nil {
		t.Fatal("expected network failure to return an error")
	}
	if !strings.Contains(err.Error(), "network unavailable") {
		t.Fatalf("expected network error to be preserved, got %q", err.Error())
	}
}

func TestPerformVersionCheckHonorsHTTPClientTimeout(t *testing.T) {
	config := validVersionCheckConfig("https://metrics.example.test/v1/version-check")
	config.timeout = 10 * time.Millisecond
	client := &http.Client{
		Timeout: config.timeout,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}),
	}

	err := performVersionCheck(context.Background(), client, config)
	if err == nil {
		t.Fatal("expected timeout to return an error")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func validVersionCheckConfig(requestURL string) versionCheckConfig {
	return versionCheckConfig{
		enabled:         true,
		interval:        defaultVersionCheckInterval,
		url:             requestURL,
		identity:        "NONE",
		operatingSystem: "MacOS",
		architecture:    "arm64",
		category:        "Exasol 8",
		productVersion:  "2026.2.0-nano.1",
		timeout:         defaultVersionCheckTimeout,
	}
}

func withStaticVersionCheckMetadata(t *testing.T, category string, productVersion string) {
	t.Helper()

	previousCategory := versionCheckCategory
	previousProductVersion := versionCheckProductVersion
	versionCheckCategory = category
	versionCheckProductVersion = productVersion
	t.Cleanup(func() {
		versionCheckCategory = previousCategory
		versionCheckProductVersion = previousProductVersion
	})
}

func writeRuntimeConfig(t *testing.T, config versionCheckRuntimeConfig) string {
	t.Helper()

	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("failed to marshal runtime config: %v", err)
	}
	path := t.TempDir() + "/version-check.json"
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("failed to write runtime config: %v", err)
	}
	return path
}

func assertQueryParameter(t *testing.T, query url.Values, name string, expected string) {
	t.Helper()
	if actual := query.Get(name); actual != expected {
		t.Fatalf("expected query parameter %s=%q, got %q", name, expected, actual)
	}
}

func sendTick(t *testing.T, ticks chan<- time.Time) {
	t.Helper()

	select {
	case ticks <- time.Now():
	case <-time.After(time.Second):
		t.Fatal("timed out sending interval tick")
	}
}

func waitForCall(t *testing.T, calls <-chan struct{}) {
	t.Helper()

	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for version-check call")
	}
}

func waitForDone(t *testing.T, done <-chan struct{}) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for recurring version-check loop to stop")
	}
}
