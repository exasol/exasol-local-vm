#!/usr/bin/env bash
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

set -euo pipefail

if [[ $# -ne 0 ]]; then
  echo "Usage: $0" >&2
  exit 1
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "Error: GitHub CLI (gh) is not installed" >&2
  exit 1
fi
if ! gh auth status >/dev/null 2>&1; then
  echo "Error: GitHub CLI is not authenticated; run 'gh auth login'" >&2
  exit 1
fi
if [[ -d ci-downloads ]] && [[ -n "$(ls -A ci-downloads)" ]]; then
  echo "Error: ci-downloads already exists and is not empty" >&2
  exit 1
fi

branch="$(git branch --show-current)"
if [[ -z "$branch" ]]; then
  echo "Error: detached HEAD; push a branch before starting the workflow" >&2
  exit 1
fi
git fetch origin "$branch"
local_revision="$(git rev-parse HEAD)"
remote_revision="$(git rev-parse "origin/$branch")"
if [[ "$local_revision" != "$remote_revision" ]]; then
  echo "Error: local branch is not pushed to origin/$branch" >&2
  exit 1
fi

started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
gh workflow run build-mac.yml --ref "$branch"

run_id=""
for _ in {1..30}; do
  run_id="$(
    gh run list \
      --workflow=build-mac.yml \
      --branch="$branch" \
      --created=">=$started_at" \
      --limit=1 \
      --json databaseId \
      --jq '.[0].databaseId // empty'
  )"
  [[ -n "$run_id" ]] && break
  sleep 2
done
if [[ -z "$run_id" ]]; then
  echo "Error: could not locate the newly started workflow run" >&2
  exit 1
fi

gh run watch "$run_id" --exit-status
mkdir -p ci-downloads
gh run download "$run_id" --name local-vm-provider --dir ci-downloads
echo "Development provider downloaded to ci-downloads/local-vm-darwin-arm64.zip"
