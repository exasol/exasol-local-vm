#!/usr/bin/env bash
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

set -euo pipefail

# Script to trigger the Build Windows Launcher workflow and download the
# resulting artifact.
# Usage: ./ci-build-windows-launcher.sh
#
# Since Phase 15 the windows launcher lives in its own workflow file
# (.github/workflows/build-windows.yml) that is fully independent of the
# mac build path: it installs podman-for-windows on the windows launcher
# and pulls the container tarball inline. There is nothing to reuse
# between runs, so this helper takes no flags.
#
# Test failure logs are downloaded from the windows-specific artifact
# name (test-failure-logs-windows) so the two helpers do not clobber
# each other's downloads.

WORKFLOW_FILE="build-windows.yml"
LAUNCHER_ARTIFACT="windows-launcher"
LAUNCHER_ZIP="windows-launcher-x86_64.zip"
FAILURE_LOGS_ARTIFACT="test-failure-logs-windows"

if [ $# -gt 0 ]; then
  echo "Error: unexpected arguments: $*" >&2
  echo "Usage: $0" >&2
  exit 1
fi

if [ -d "ci-downloads" ] && [ "$(ls -A ci-downloads)" ]; then
  echo "Error: ci-downloads directory already exists and is not empty"
  exit 1
fi

if ! command -v gh &> /dev/null; then
  echo "Error: GitHub CLI (gh) is not installed"
  echo "Install from: https://cli.github.com/"
  exit 1
fi

if ! gh auth status &> /dev/null; then
  echo "Error: Not authenticated with GitHub CLI"
  echo "Run: gh auth login"
  exit 1
fi

BRANCH=$(git branch --show-current)
if [ -z "$BRANCH" ]; then
  echo "Error: Not on a branch (detached HEAD?)"
  exit 1
fi

echo "Current branch: $BRANCH"

git fetch origin "$BRANCH" 2>/dev/null || true
LOCAL=$(git rev-parse HEAD)
REMOTE=$(git rev-parse "origin/$BRANCH" 2>/dev/null || echo "")

if [ -z "$REMOTE" ]; then
  echo "Error: Branch '$BRANCH' does not exist on remote"
  echo "Push the branch first: git push -u origin $BRANCH"
  exit 1
fi

if [ "$LOCAL" != "$REMOTE" ]; then
  echo "Error: Local commits not pushed to remote"
  echo "Local:  $LOCAL"
  echo "Remote: $REMOTE"
  echo ""
  echo "Push your changes first: git push"
  exit 1
fi

echo "✓ All commits are pushed"
echo ""

echo "Triggering '$WORKFLOW_FILE' workflow on branch: $BRANCH"
gh workflow run "$WORKFLOW_FILE" --ref "$BRANCH"

echo "Waiting for workflow to start..."
sleep 5

RUN_ID=""
for i in {1..30}; do
  RUN_ID=$(gh run list --workflow="$WORKFLOW_FILE" --branch="$BRANCH" --limit=1 --json databaseId --jq '.[0].databaseId' 2>/dev/null || echo "")
  if [ -n "$RUN_ID" ]; then
    break
  fi
  sleep 2
done

if [ -z "$RUN_ID" ]; then
  echo "Error: Could not find workflow run"
  echo "Check manually: gh run list --workflow=$WORKFLOW_FILE"
  exit 1
fi

echo "Workflow run ID: $RUN_ID"
echo "Watching: https://github.com/$(gh repo view --json nameWithOwner -q .nameWithOwner)/actions/runs/$RUN_ID"
echo ""

echo "Waiting for workflow to complete..."
WORKFLOW_FAILED="false"
gh run watch "$RUN_ID" --exit-status || WORKFLOW_FAILED="true"

if [ "$WORKFLOW_FAILED" = "true" ]; then
  echo ""
  echo "❌ Workflow failed!"
  echo "View logs: gh run view $RUN_ID"
fi

mkdir -p ci-downloads

echo ""
echo "Downloading Windows test failure logs (if any)..."
if gh run download "$RUN_ID" --name "$FAILURE_LOGS_ARTIFACT" --dir ci-downloads/test-failure-logs 2>/dev/null; then
  echo "✓ Test failure logs downloaded to: ci-downloads/test-failure-logs/"
else
  echo "  (no $FAILURE_LOGS_ARTIFACT artifact found for this run)"
fi

if [ "$WORKFLOW_FAILED" = "true" ]; then
  exit 1
fi

echo ""
echo "✓ Workflow completed successfully"
echo ""

echo "Downloading Windows launcher artifact..."
cd ci-downloads

gh run download "$RUN_ID" --name "$LAUNCHER_ARTIFACT" || {
  echo ""
  echo "❌ Failed to download $LAUNCHER_ARTIFACT artifact"
  echo "Available artifacts:"
  gh run view "$RUN_ID" --log-failed
  exit 1
}

cd ..

echo ""
echo "✓ Windows launcher downloaded to: ci-downloads/"
echo ""
ls -lh ci-downloads/
echo ""

if [ -f "ci-downloads/$LAUNCHER_ZIP" ]; then
  echo "✓ Launcher zip: ci-downloads/$LAUNCHER_ZIP"
fi
