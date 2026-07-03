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
    "shm_size": "1gb",
    "pids_limit": 2048,
    "security_opt": "label=disable",
    "restart": "unless-stopped"
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
    local expected_url="https://metrics.example.test/v1/version-check"
    local expected_identity="exasol-personal;deployment;small;default"
    local expected_operating_system="MacOS"
    prepare_case "$case_dir"

    cat > "$case_dir/shared/version-check.json" <<VERSION_CHECK_JSON
{
  "enabled": true,
  "interval_seconds": 12,
  "identity": "$expected_identity",
  "url": "$expected_url",
  "operating_system": "$expected_operating_system"
}
VERSION_CHECK_JSON

    local run_line
    run_line="$(run_init_db_case "$case_dir")"

    assert_contains "$run_line" "localhost/exasol-local-db:latest init"
    assert_contains "$run_line" "VERSION_CHECK_ENABLED=1"
    assert_contains "$run_line" "VERSION_CHECK_ENDPOINT=$expected_url"
    assert_contains "$run_line" "-e VERSION_CHECK_IDENTITY=$expected_identity"
    assert_contains "$run_line" "VERSION_CHECK_INTERVAL_SEC=60"
    assert_contains "$run_line" "VERSION_CHECK_RETRY_INTERVAL_SEC=60"
    assert_contains "$run_line" "VERSION_CHECK_OPERATING_SYSTEM=$expected_operating_system"
    local init_args="${run_line#*localhost/exasol-local-db:latest init}"
    assert_not_contains "$init_args" "VERSION_CHECK_IDENTITY"
    assert_not_contains "$run_line" "version_check_architecture"
    assert_not_contains "$run_line" "EXANANO_VERSION_CHECK"
    assert_not_contains "$run_line" "--mount type=bind"
}

test_missing_runtime_config_disables_nano_checks() {
    local case_dir="$1/disabled"
    prepare_case "$case_dir"

    local run_line
    run_line="$(run_init_db_case "$case_dir")"

    assert_contains "$run_line" "localhost/exasol-local-db:latest init"
    assert_contains "$run_line" "VERSION_CHECK_ENABLED=0"
    assert_not_contains "$run_line" "VERSION_CHECK_OPERATING_SYSTEM"
    assert_not_contains "$run_line" "version_check_architecture"
    assert_not_contains "$run_line" "EXANANO_VERSION_CHECK"
    assert_not_contains "$run_line" "-e "
    assert_not_contains "$run_line" "--mount type=bind"
    assert_not_contains "$run_line" "version-check.json"
}

test_fresh_deployment_passes_params() {
    local case_dir="$1/fresh-params"
    prepare_case "$case_dir"
    # Add db.params to config.json for this case.
    jq '.db.params = ["maxConnectionsLicenseLimit=20"]' "$case_dir/init/config.json" > "$case_dir/init/config.json.tmp"
    mv "$case_dir/init/config.json.tmp" "$case_dir/init/config.json"

    local run_line
    run_line="$(run_init_db_case "$case_dir")"

    assert_contains "$run_line" "-v $case_dir/state/exa:/exa"
    assert_contains "$run_line" "params=maxConnectionsLicenseLimit=20"
}

test_existing_exa_data_skips_params() {
    local case_dir="$1/existing-exa"
    prepare_case "$case_dir"
    jq '.db.params = ["maxConnectionsLicenseLimit=20"]' "$case_dir/init/config.json" > "$case_dir/init/config.json.tmp"
    mv "$case_dir/init/config.json.tmp" "$case_dir/init/config.json"
    # Simulate a populated /exa runtime from a prior container's lifetime,
    # so this run must skip the first-deployment-only "params=" argument.
    mkdir -p "$case_dir/state/exa"
    touch "$case_dir/state/exa/exasol.conf"

    local run_line
    run_line="$(run_init_db_case "$case_dir")"

    assert_contains "$run_line" "-v $case_dir/state/exa:/exa"
    assert_not_contains "$run_line" "params="
}

main() {
    local tmp_dir
    tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/exasol-local-vm-init-db-test.XXXXXX")"
    trap "rm -rf '$tmp_dir'" EXIT

    test_enabled_runtime_config "$tmp_dir"
    test_missing_runtime_config_disables_nano_checks "$tmp_dir"
    test_fresh_deployment_passes_params "$tmp_dir"
    test_existing_exa_data_skips_params "$tmp_dir"

    echo "init-db version-check tests passed"
}

main "$@"
