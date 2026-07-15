#!/usr/bin/env bash
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

set -euo pipefail

# Script to trigger Build Packages workflow and download the Windows launcher.
# Usage: ./ci-build-windows-launcher.sh [--skip-linux-build] [--use-run-id RUN_ID]
#
# Options:
#   --skip-linux-build    Skip building Linux packages (reuse from previous run)
#   --use-run-id RUN_ID   Use Linux packages from specific run ID
#
# Mirrors ci-build-mac-launcher.sh; the only functional differences are the
# artifact name (windows-launcher) and the expected zip filename
# (windows-runner-x86_64.zip). Test failure logs are downloaded from the
# windows-specific artifact name (test-failure-logs-windows) so the two
# helpers do not clobber each other's downloads.

SKIP_LINUX_BUILD="false"
PREVIOUS_RUN_ID=""

while [[ $# -gt 0 ]]; do
  case $1 in
    --skip-linux-build)
      SKIP_LINUX_BUILD="true"
      shift
      ;;
    --use-run-id)
      PREVIOUS_RUN_ID="$2"
      SKIP_LINUX_BUILD="true"
      shift 2
      ;;
    *)
      echo "Unknown option: $1"
      echo "Usage: $0 [--skip-linux-build] [--use-run-id RUN_ID]"
      exit 1
      ;;
  esac
done

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

if [ "$SKIP_LINUX_BUILD" = "true" ] && [ -z "$PREVIOUS_RUN_ID" ]; then
  echo "Validating that a previous run with Linux packages exists..."

  RUN_IDS=$(gh run list \
    --workflow=build-packages.yml \
    --branch="$BRANCH" \
    --status=success \
    --limit=20 \
    --json databaseId \
    --jq '.[].databaseId' 2>/dev/null || echo "")

  FOUND_VALID_RUN=""
  for candidate_run_id in $RUN_IDS; do
    BUILD_JOB_STATUS=$(gh run view "$candidate_run_id" \
      --json jobs \
      --jq '.jobs[] | select(.name == "build-disk-images") | .conclusion' 2>/dev/null || echo "")

    if [ "$BUILD_JOB_STATUS" = "success" ]; then
      FOUND_VALID_RUN="$candidate_run_id"
      echo "✓ Found valid previous run: $FOUND_VALID_RUN"
      break
    fi
  done

  if [ -z "$FOUND_VALID_RUN" ]; then
    echo "Error: No previous successful run with Linux packages found on branch: $BRANCH"
    echo "You need to run the workflow without --skip-linux-build at least once first"
    exit 1
  fi
fi

echo "Triggering 'Build Packages' workflow on branch: $BRANCH"
if [ "$SKIP_LINUX_BUILD" = "true" ]; then
  echo "  - Skipping Linux build (using previous artifacts)"
  if [ -n "$PREVIOUS_RUN_ID" ]; then
    echo "  - Using run ID: $PREVIOUS_RUN_ID"
    gh workflow run build-packages.yml \
      --ref "$BRANCH" \
      --field skip-linux-build=true \
      --field previous-run-id="$PREVIOUS_RUN_ID"
  else
    echo "  - Will use latest run with Linux packages (workflow will auto-detect)"
    gh workflow run build-packages.yml \
      --ref "$BRANCH" \
      --field skip-linux-build=true
  fi
else
  gh workflow run build-packages.yml --ref "$BRANCH"
fi

echo "Waiting for workflow to start..."
sleep 5

RUN_ID=""
for i in {1..30}; do
  RUN_ID=$(gh run list --workflow=build-packages.yml --branch="$BRANCH" --limit=1 --json databaseId --jq '.[0].databaseId' 2>/dev/null || echo "")
  if [ -n "$RUN_ID" ]; then
    break
  fi
  sleep 2
done

if [ -z "$RUN_ID" ]; then
  echo "Error: Could not find workflow run"
  echo "Check manually: gh run list --workflow=build-packages.yml"
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

# Windows test failure logs live under a windows-specific artifact name so
# they do not collide with the mac helper's downloads in the same run.
echo ""
echo "Downloading Windows test failure logs (if any)..."
if gh run download "$RUN_ID" --name test-failure-logs-windows --dir ci-downloads/test-failure-logs 2>/dev/null; then
  echo "✓ Test failure logs downloaded to: ci-downloads/test-failure-logs/"
else
  echo "  (no test-failure-logs-windows artifact found for this run)"
fi

if [ "$WORKFLOW_FAILED" = "true" ]; then
  exit 1
fi

echo ""
echo "✓ Workflow completed successfully"
echo ""

echo "Downloading Windows launcher artifact..."
cd ci-downloads

gh run download "$RUN_ID" --name windows-launcher || {
  echo ""
  echo "❌ Failed to download windows-launcher artifact"
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

if [ -f ci-downloads/windows-runner-x86_64.zip ]; then
  echo "✓ Launcher zip: ci-downloads/windows-runner-x86_64.zip"
fi

if [ "$SKIP_LINUX_BUILD" != "true" ]; then
  echo ""
  echo "💡 Tip: To skip rebuilding Linux packages next time (faster):"
  echo "   $0 --skip-linux-build"
  echo "   This will reuse Linux packages from run $RUN_ID"
fi
