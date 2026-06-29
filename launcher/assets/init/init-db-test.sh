#!/usr/bin/env bash
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
INIT_DB_SCRIPT="$ROOT_DIR/launcher/assets/init/init-db.sh"

assert_contains() {
    local haystack="$1"
    local needle="$2"
    if [[ "$haystack" != *"$needle"* ]]; then
        echo "Expected to find '$needle' in:" >&2
        echo "$haystack" >&2
        exit 1
    fi
}

assert_not_contains() {
    local haystack="$1"
    local needle="$2"
    if [[ "$haystack" == *"$needle"* ]]; then
        echo "Did not expect to find '$needle' in:" >&2
        echo "$haystack" >&2
        exit 1
    fi
}

create_mock_commands() {
    local mock_dir="$1"

    cat > "$mock_dir/podman" <<'MOCK_PODMAN'
#!/usr/bin/env bash
set -euo pipefail

command="${1:-}"
printf '%s' "$command" >> "$PODMAN_CALLS"
shift || true
for arg in "$@"; do
    printf ' %s' "$arg" >> "$PODMAN_CALLS"
done
printf '\n' >> "$PODMAN_CALLS"

case "$command" in
    load)
        cat >/dev/null
        echo "Loaded image: docker.io/library/exasol-local-db:test"
        ;;
    *)
        ;;
esac
MOCK_PODMAN
    chmod +x "$mock_dir/podman"

    cat > "$mock_dir/logger" <<'MOCK_LOGGER'
#!/usr/bin/env bash
exit 0
MOCK_LOGGER
    chmod +x "$mock_dir/logger"
}

prepare_case() {
    local case_dir="$1"

    mkdir -p "$case_dir/init" "$case_dir/shared" "$case_dir/state" "$case_dir/mock-bin"
    create_mock_commands "$case_dir/mock-bin"

    cat > "$case_dir/init/config.json" <<'CONFIG_JSON'
{
  "db": {
    "container_name": "exasol-local-db",
    "tarball_name": "exasol-nano-db.tar.gz",
    "ports": {
      "db": 8563
    },
    "shm_size": "1gb"
  }
}
CONFIG_JSON
    printf 'fake container image' > "$case_dir/init/exasol-nano-db.tar.gz"
    printf '{"ports":{}}\n' > "$case_dir/init/init-output.json"
}

run_init_db_case() {
    local case_dir="$1"
    local calls_file="$case_dir/podman-calls.log"

    PATH="$case_dir/mock-bin:$PATH" \
        PODMAN_CALLS="$calls_file" \
        EXASOL_VM_INIT_DIR="$case_dir/init" \
        EXASOL_VM_HOST_SHARED_DIR="$case_dir/shared" \
        EXASOL_VM_CONTAINER_STATE_DIR="$case_dir/state" \
        INIT_OUTPUT_FILE="$case_dir/init/init-output.json" \
        sh "$INIT_DB_SCRIPT" >/dev/null

    grep '^run ' "$calls_file"
}

test_enabled_runtime_config() {
    local case_dir="$1/enabled"
    prepare_case "$case_dir"

    cat > "$case_dir/shared/version-check.json" <<'VERSION_CHECK_JSON'
{
  "enabled": true,
  "interval_seconds": 12,
  "identity": "exasol-personal;deployment;small;default",
  "url": "https://metrics.example.test/v1/version-check",
  "operating_system": "MacOS",
  "architecture": "arm64"
}
VERSION_CHECK_JSON

    local run_line
    run_line="$(run_init_db_case "$case_dir")"

    assert_contains "$run_line" "--mount type=bind,src=$case_dir/shared/version-check.json,target=/run/exasol-local-vm-version-check.json,readonly"
    assert_not_contains "$run_line" "EXASOL_VERSION_CHECK_"
}

test_missing_runtime_config_disables_checks() {
    local case_dir="$1/disabled"
    prepare_case "$case_dir"

    local run_line
    run_line="$(run_init_db_case "$case_dir")"

    assert_not_contains "$run_line" "--mount type=bind"
    assert_not_contains "$run_line" "version-check.json"
    assert_not_contains "$run_line" "EXASOL_VERSION_CHECK_"
}

main() {
    local tmp_dir
    tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/exasol-local-vm-init-db-test.XXXXXX")"
    trap "rm -rf '$tmp_dir'" EXIT

    test_enabled_runtime_config "$tmp_dir"
    test_missing_runtime_config_disables_checks "$tmp_dir"

    echo "init-db version-check tests passed"
}

main "$@"
