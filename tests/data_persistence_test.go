// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

//go:build darwin || windows

// Cross-platform data-persistence tests. The mac-only cases (that reach
// into vm/data.img, filesystem inodes, or the guest OS over SSH) live in
// data_persistence_darwin_test.go.
package integration

import (
	"strings"
	"testing"
	"time"
)

// TestDataDiskShrinkRejected verifies that resize-data to a size smaller
// than the recorded value fails with a message that mentions "shrink".
//
// On mac the launcher tracks the actual on-disk data.img size; on windows
// it tracks the requested size in resources/data-size.txt. Both surfaces
// enforce the same grow-only contract and produce the same substring in
// their error output.
func TestDataDiskShrinkRejected(t *testing.T) {
	requireIntegration(t)

	f := NewLauncherFixture(t)
	defer f.Cleanup()

	f.Init()
	f.StartVM(2, 4096, 20)
	f.StopVM()
	waitForVMStopped(t, f, 90*time.Second)

	err := f.ResizeData(10)
	if err == nil {
		t.Fatal("expected resize-data to a smaller size to fail, but it succeeded")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "shrink") {
		t.Fatalf("expected shrink-related error, got: %v", err)
	}
}
