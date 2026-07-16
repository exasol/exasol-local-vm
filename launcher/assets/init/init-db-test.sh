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

marker_dir="${PODMAN_STATE_DIR:-}"
marker_for() { printf '%s/%s.exists' "$marker_dir" "$1"; }
image_marker() { printf '%s/img-%s.exists' "$marker_dir" "$(printf '%s' "$1" | tr '/:.' '___')"; }

case "$command" in
    load)
        cat >/dev/null
        echo "Loaded image: docker.io/library/exasol-local-db:test"
        ;;
    container)
        # Support: podman container exists <name> (0 = exists, 1 = absent).
        if [ "${1:-}" = "exists" ]; then
            if [ -n "$marker_dir" ] && [ -f "$(marker_for "${2:-}")" ]; then
                exit 0
            fi
            exit 1
        fi
        ;;
    rm)
        # Drop the existence marker for any named container (ignore flags like -f).
        for arg in "$@"; do
            case "$arg" in
                -*) ;;
                *) [ -n "$marker_dir" ] && rm -f "$(marker_for "$arg")" 2>/dev/null || true ;;
            esac
        done
        ;;
    run)
        # Record the --name container as now existing.
        prev=""
        for arg in "$@"; do
            if [ "$prev" = "--name" ] && [ -n "$marker_dir" ]; then
                : > "$(marker_for "$arg")"
            fi
            prev="$arg"
        done
        ;;
    image)
        # podman image exists <ref> (0 = present, 1 = absent)
        if [ "${1:-}" = "exists" ]; then
            if [ -n "$marker_dir" ] && [ -f "$(image_marker "${2:-}")" ]; then
                exit 0
            fi
            exit 1
        fi
        ;;
    pull)
        # Record the pulled image as now present, storing its full ref so `images` can
        # report it back.
        if [ -n "$marker_dir" ] && [ -n "${1:-}" ]; then
            printf '%s\n' "$1" > "$(image_marker "$1")"
        fi
        ;;
    images)
        # podman images --format '{{.Repository}}:{{.Tag}}': list stored image refs. Each
        # image marker holds its full reference (written on pull or pre-seeded by a test).
        if [ -n "$marker_dir" ]; then
            for f in "$marker_dir"/img-*.exists; do
                [ -e "$f" ] || continue
                cat "$f"
            done
        fi
        ;;
    rmi)
        # Drop the marker(s) for the removed image ref(s); ignore flags.
        for arg in "$@"; do
            case "$arg" in
                -*) ;;
                *) [ -n "$marker_dir" ] && rm -f "$(image_marker "$arg")" 2>/dev/null || true ;;
            esac
        done
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

    mkdir -p "$case_dir/init" "$case_dir/shared" "$case_dir/state" "$case_dir/mock-bin" "$case_dir/podman-state"
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
        PODMAN_STATE_DIR="$case_dir/podman-state" \
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

test_removes_existing_tls_certificates() {
    local case_dir="$1/existing-certs"
    prepare_case "$case_dir"
    # Simulate certs left in /exa by a prior (possibly interrupted) init. Their
    # presence would otherwise make the Nano entrypoint block on an interactive
    # overwrite prompt; init-db.sh must delete them before starting the container.
    mkdir -p "$case_dir/state/exa/certificates"
    touch "$case_dir/state/exa/certificates/fullchain.pem"
    touch "$case_dir/state/exa/certificates/privkey.pem"

    run_init_db_case "$case_dir" >/dev/null

    if [ -e "$case_dir/state/exa/certificates/fullchain.pem" ]; then
        echo "Expected fullchain.pem to be removed before container start" >&2
        exit 1
    fi
    if [ -e "$case_dir/state/exa/certificates/privkey.pem" ]; then
        echo "Expected privkey.pem to be removed before container start" >&2
        exit 1
    fi
}

test_preserves_existing_runtime_tls_certificates() {
    local case_dir="$1/existing-runtime-certs"
    prepare_case "$case_dir"
    # A completed /exa runtime needs these certificate files. The Nano restart
    # path does not regenerate them when exasol.conf already exists.
    mkdir -p "$case_dir/state/exa/certificates"
    touch "$case_dir/state/exa/exasol.conf"
    printf 'fullchain' > "$case_dir/state/exa/certificates/fullchain.pem"
    printf 'privkey' > "$case_dir/state/exa/certificates/privkey.pem"

    local run_line
    run_line="$(run_init_db_case "$case_dir")"

    assert_contains "$run_line" "-v $case_dir/state/exa:/exa"
    assert_not_contains "$run_line" "params="
    if [ "$(cat "$case_dir/state/exa/certificates/fullchain.pem")" != "fullchain" ]; then
        echo "Expected existing runtime fullchain.pem to be preserved" >&2
        exit 1
    fi
    if [ "$(cat "$case_dir/state/exa/certificates/privkey.pem")" != "privkey" ]; then
        echo "Expected existing runtime privkey.pem to be preserved" >&2
        exit 1
    fi
}

test_quarantines_incomplete_initial_create() {
    local case_dir="$1/incomplete-create"
    prepare_case "$case_dir"
    jq '.db.params = ["maxConnectionsLicenseLimit=20"]' "$case_dir/init/config.json" > "$case_dir/init/config.json.tmp"
    mv "$case_dir/init/config.json.tmp" "$case_dir/init/config.json"
    mkdir -p "$case_dir/state/exa"
    touch "$case_dir/state/exa/exasol.conf"
    touch "$case_dir/state/exa/.exanano-initial-create-in-progress"
    printf 'keep diagnostics' > "$case_dir/state/exa/sentinel.txt"

    local run_line
    run_line="$(run_init_db_case "$case_dir")"

    local quarantined_dir
    quarantined_dir="$(find "$case_dir/state" -maxdepth 1 -type d -name 'exa.failed-*' -print -quit)"
    if [ -z "$quarantined_dir" ]; then
        echo "Expected incomplete /exa runtime to be moved to exa.failed-*" >&2
        exit 1
    fi
    if [ ! -f "$quarantined_dir/.exanano-initial-create-in-progress" ]; then
        echo "Expected incomplete-create marker to be preserved in quarantined /exa runtime" >&2
        exit 1
    fi
    if [ ! -f "$quarantined_dir/sentinel.txt" ]; then
        echo "Expected quarantined /exa runtime to preserve existing files" >&2
        exit 1
    fi
    if [ -e "$case_dir/state/exa/.exanano-initial-create-in-progress" ]; then
        echo "Expected replacement /exa runtime to be clean" >&2
        exit 1
    fi
    assert_contains "$run_line" "-v $case_dir/state/exa:/exa"
    assert_contains "$run_line" "params=maxConnectionsLicenseLimit=20"
}

test_slc_mounts_present() {
    local case_dir="$1/slc-present"
    prepare_case "$case_dir"
    cat > "$case_dir/shared/slc.json" <<'SLC_JSON'
{
  "slc": [
    { "image": "docker.io/exasol/script-language-container:standard-EXASOL-all-python-3.12-release_arm64_HASHPY", "target": "/exa/slc/python312" },
    { "image": "docker.io/exasol/script-language-container:standard-EXASOL-all-java-17-release_arm64_HASHJAVA", "target": "/exa/slc/java17" }
  ]
}
SLC_JSON

    local run_line
    run_line="$(run_init_db_case "$case_dir")"

    assert_contains "$run_line" "--mount type=image,source=docker.io/exasol/script-language-container:standard-EXASOL-all-python-3.12-release_arm64_HASHPY,destination=/exa/slc/python312"
    assert_contains "$run_line" "--mount type=image,source=docker.io/exasol/script-language-container:standard-EXASOL-all-java-17-release_arm64_HASHJAVA,destination=/exa/slc/java17"
    assert_contains "$run_line" "localhost/exasol-local-db:latest init"

    # Missing images must be pulled before the mount (podman does not pull image mounts).
    local calls
    calls="$(cat "$case_dir/podman-calls.log")"
    assert_contains "$calls" "pull docker.io/exasol/script-language-container:standard-EXASOL-all-python-3.12-release_arm64_HASHPY"
    assert_contains "$calls" "pull docker.io/exasol/script-language-container:standard-EXASOL-all-java-17-release_arm64_HASHJAVA"
}

test_slc_skips_pull_when_image_present() {
    local case_dir="$1/slc-cached"
    prepare_case "$case_dir"
    cat > "$case_dir/shared/slc.json" <<'SLC_JSON'
{ "slc": [ { "image": "docker.io/exasol/script-language-container:py", "target": "/exa/slc/python312" } ] }
SLC_JSON
    # Pre-mark the image as already present (matches image_marker's tr '/:.' '___').
    : > "$case_dir/podman-state/img-docker_io_exasol_script-language-container_py.exists"

    run_init_db_case "$case_dir" >/dev/null

    if grep -q '^pull ' "$case_dir/podman-calls.log"; then
        echo "Expected no 'podman pull' when the SLC image is already present" >&2
        exit 1
    fi
}

test_no_slc_config_has_no_image_mounts() {
    local case_dir="$1/slc-absent"
    prepare_case "$case_dir"
    # No slc.json is written: behavior must be identical to before this feature.

    local run_line
    run_line="$(run_init_db_case "$case_dir")"

    assert_contains "$run_line" "localhost/exasol-local-db:latest init"
    assert_not_contains "$run_line" "--mount type=image"
}

test_invalid_slc_config_fails() {
    local case_dir="$1/slc-invalid"
    prepare_case "$case_dir"
    # Missing "target": init-db.sh must fail fast at load time rather than start a
    # database with a broken mount.
    printf '{"slc":[{"image":"docker.io/x:tag"}]}' > "$case_dir/shared/slc.json"

    set +e
    PATH="$case_dir/mock-bin:$PATH" \
        PODMAN_CALLS="$case_dir/podman-calls.log" \
        PODMAN_STATE_DIR="$case_dir/podman-state" \
        EXASOL_VM_INIT_DIR="$case_dir/init" \
        EXASOL_VM_HOST_SHARED_DIR="$case_dir/shared" \
        EXASOL_VM_CONTAINER_STATE_DIR="$case_dir/state" \
        INIT_OUTPUT_FILE="$case_dir/init/init-output.json" \
        sh "$INIT_DB_SCRIPT" >/dev/null 2>&1
    local rc=$?
    set -e

    if [ "$rc" -eq 0 ]; then
        echo "Expected init-db.sh to fail on invalid slc.json, but it succeeded" >&2
        exit 1
    fi
}

test_removes_stale_container_before_recreate() {
    local case_dir="$1/stale-container"
    prepare_case "$case_dir"
    # Simulate a container left behind by an unclean shutdown: pre-create its marker so
    # "podman container exists" reports it, forcing a force-remove before recreation.
    : > "$case_dir/podman-state/exasol-local-db.exists"

    local run_line
    run_line="$(run_init_db_case "$case_dir")"

    assert_contains "$(cat "$case_dir/podman-calls.log")" "rm -f exasol-local-db"
    assert_contains "$run_line" "localhost/exasol-local-db:latest init"
}

test_prunes_unreferenced_slc_images() {
    local case_dir="$1/slc-prune"
    prepare_case "$case_dir"
    # Only python is desired now (e.g. java was uninstalled, or python replaced it).
    cat > "$case_dir/shared/slc.json" <<'SLC_JSON'
{ "slc": [ { "image": "docker.io/exasol/script-language-container:py-keep", "target": "/exa/slc/python312" } ] }
SLC_JSON
    # Both images are already in the store: the desired python one, and a stale java one
    # that slc.json no longer references. Marker content is the full ref so `podman images`
    # can report it; the filename matches image_marker's tr '/:.' '___' sanitization.
    printf '%s\n' "docker.io/exasol/script-language-container:py-keep" \
        > "$case_dir/podman-state/img-docker_io_exasol_script-language-container_py-keep.exists"
    printf '%s\n' "docker.io/exasol/script-language-container:java-stale" \
        > "$case_dir/podman-state/img-docker_io_exasol_script-language-container_java-stale.exists"

    local run_line
    run_line="$(run_init_db_case "$case_dir")"

    local calls
    calls="$(cat "$case_dir/podman-calls.log")"
    # The unreferenced java image is removed; the desired python image is kept.
    assert_contains "$calls" "rmi docker.io/exasol/script-language-container:java-stale"
    assert_not_contains "$calls" "rmi docker.io/exasol/script-language-container:py-keep"
    # And the desired image is still mounted into the recreated container.
    assert_contains "$run_line" "--mount type=image,source=docker.io/exasol/script-language-container:py-keep,destination=/exa/slc/python312"
}

test_no_slc_config_leaves_images_untouched() {
    local case_dir="$1/slc-prune-absent"
    prepare_case "$case_dir"
    # No slc.json: the store is left untouched (SLC-unaware behavior, no rmi at all).
    printf '%s\n' "docker.io/exasol/script-language-container:java-stale" \
        > "$case_dir/podman-state/img-docker_io_exasol_script-language-container_java-stale.exists"

    run_init_db_case "$case_dir" >/dev/null

    assert_not_contains "$(cat "$case_dir/podman-calls.log")" "rmi "
}

test_empty_slc_config_mounts_nothing_and_prunes_all() {
    local case_dir="$1/slc-empty"
    prepare_case "$case_dir"
    # The common path once the launcher is SLC-aware: it writes slc.json on every start,
    # with an empty array when no SLC is installed (or after the last one is removed).
    printf '{"slc":[]}' > "$case_dir/shared/slc.json"
    # A leftover SLC image is now unreferenced by the (empty) desired set.
    printf '%s\n' "docker.io/exasol/script-language-container:java-stale" \
        > "$case_dir/podman-state/img-docker_io_exasol_script-language-container_java-stale.exists"

    local run_line
    run_line="$(run_init_db_case "$case_dir")"

    # No SLC is mounted, but pruning still runs and reclaims the unreferenced image.
    assert_not_contains "$run_line" "--mount type=image"
    assert_contains "$(cat "$case_dir/podman-calls.log")" "rmi docker.io/exasol/script-language-container:java-stale"
}

main() {
    local tmp_dir
    tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/exasol-local-vm-init-db-test.XXXXXX")"
    trap "rm -rf '$tmp_dir'" EXIT

    test_enabled_runtime_config "$tmp_dir"
    test_missing_runtime_config_disables_nano_checks "$tmp_dir"
    test_fresh_deployment_passes_params "$tmp_dir"
    test_existing_exa_data_skips_params "$tmp_dir"
    test_removes_existing_tls_certificates "$tmp_dir"
    test_preserves_existing_runtime_tls_certificates "$tmp_dir"
    test_quarantines_incomplete_initial_create "$tmp_dir"
    test_slc_mounts_present "$tmp_dir"
    test_slc_skips_pull_when_image_present "$tmp_dir"
    test_no_slc_config_has_no_image_mounts "$tmp_dir"
    test_invalid_slc_config_fails "$tmp_dir"
    test_removes_stale_container_before_recreate "$tmp_dir"
    test_prunes_unreferenced_slc_images "$tmp_dir"
    test_no_slc_config_leaves_images_untouched "$tmp_dir"
    test_empty_slc_config_mounts_nothing_and_prunes_all "$tmp_dir"

    echo "init-db tests passed"
}

main "$@"
