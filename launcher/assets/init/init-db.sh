#!/bin/sh
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

# Load and run Exasol Nano DB container
# Based on container/load-shared-container.sh
set -eu
set -x
trap 'rc=$?; echo "[$(date +%Y-%m-%dT%H:%M:%S)] [DB] EXIT trap: code=$rc"' EXIT

# Validate required environment variables
if [ -z "${EXASOL_VM_HOST_SHARED_DIR:-}" ]; then
  echo "Error: EXASOL_VM_HOST_SHARED_DIR environment variable is not set" >&2
  exit 1
fi

if [ ! -d "$EXASOL_VM_HOST_SHARED_DIR" ]; then
  echo "Error: EXASOL_VM_HOST_SHARED_DIR directory does not exist: $EXASOL_VM_HOST_SHARED_DIR" >&2
  exit 1
fi

# Validate INIT_OUTPUT_FILE
if [ -z "${INIT_OUTPUT_FILE:-}" ]; then
  echo "Error: INIT_OUTPUT_FILE environment variable is not set" >&2
  exit 1
fi

# Ensure INIT_OUTPUT_FILE directory exists
mkdir -p "$(dirname "$INIT_OUTPUT_FILE")" 2>/dev/null || true

# Test if we can write to INIT_OUTPUT_FILE
if ! touch "$INIT_OUTPUT_FILE" 2>/dev/null; then
  echo "Error: Cannot write to INIT_OUTPUT_FILE: $INIT_OUTPUT_FILE" >&2
  exit 1
fi

# Load configuration from config.json (required)
CONFIG_FILE="$EXASOL_VM_INIT_DIR/config.json"
if [ ! -f "$CONFIG_FILE" ]; then
  echo "Error: config.json not found at $CONFIG_FILE" >&2
  exit 1
fi

DB_CONTAINER_TARBALL_NAME=$(jq -r '.db.tarball_name' "$CONFIG_FILE")
DB_CONTAINER_NAME=$(jq -r '.db.container_name' "$CONFIG_FILE")
DB_PORT=$(jq -r '.db.ports.db' "$CONFIG_FILE")
DB_SHM_SIZE=$(jq -r '.db.shm_size' "$CONFIG_FILE")
DB_PIDS_LIMIT=$(jq -r '.db.pids_limit' "$CONFIG_FILE")
DB_SECURITY_OPT=$(jq -r '.db.security_opt' "$CONFIG_FILE")
DB_RESTART=$(jq -r '.db.restart' "$CONFIG_FILE")

# Optional DB parameters applied on first start. The Nano container accepts
# these via its documented "init params='k=v ...'" interface; the values are
# persisted to /exa/exasol.conf on the initial bootstrap.
DB_PARAMS=$(jq -r '.db.params // [] | join(" ")' "$CONFIG_FILE")
VERSION_CHECK_RUNTIME_CONFIG_FILE="$EXASOL_VM_HOST_SHARED_DIR/version-check.json"
NANO_VERSION_CHECK_DEFAULT_INTERVAL_SEC=86400
NANO_VERSION_CHECK_MIN_INTERVAL_SEC=60
NANO_VERSION_CHECK_MAX_INTERVAL_SEC=604800
NANO_VERSION_CHECK_MAX_RETRY_INTERVAL_SEC=86400
NANO_VERSION_CHECK_ENABLED=0
NANO_VERSION_CHECK_ENDPOINT=
NANO_VERSION_CHECK_IDENTITY=
NANO_VERSION_CHECK_OPERATING_SYSTEM=
NANO_VERSION_CHECK_INTERVAL_SEC=$NANO_VERSION_CHECK_DEFAULT_INTERVAL_SEC
NANO_VERSION_CHECK_RETRY_INTERVAL_SEC=$NANO_VERSION_CHECK_DEFAULT_INTERVAL_SEC

# Validate that required fields were present in config
if [ -z "$DB_CONTAINER_TARBALL_NAME" ] || [ "$DB_CONTAINER_TARBALL_NAME" = "null" ]; then
  echo "Error: db.tarball_name not found in $CONFIG_FILE" >&2
  exit 1
fi
if [ -z "$DB_CONTAINER_NAME" ] || [ "$DB_CONTAINER_NAME" = "null" ]; then
  echo "Error: db.container_name not found in $CONFIG_FILE" >&2
  exit 1
fi

# Construct full path to tarball
DB_CONTAINER_TARBALL="$EXASOL_VM_INIT_DIR/$DB_CONTAINER_TARBALL_NAME"
if [ -z "$DB_PORT" ] || [ "$DB_PORT" = "null" ]; then
  echo "Error: db.ports.db not found in $CONFIG_FILE" >&2
  exit 1
fi
if [ -z "$DB_SHM_SIZE" ] || [ "$DB_SHM_SIZE" = "null" ]; then
  echo "Error: db.shm_size not found in $CONFIG_FILE" >&2
  exit 1
fi
if [ -z "$DB_PIDS_LIMIT" ] || [ "$DB_PIDS_LIMIT" = "null" ]; then
  echo "Error: db.pids_limit not found in $CONFIG_FILE" >&2
  exit 1
fi
if [ -z "$DB_SECURITY_OPT" ] || [ "$DB_SECURITY_OPT" = "null" ]; then
  echo "Error: db.security_opt not found in $CONFIG_FILE" >&2
  exit 1
fi
if [ -z "$DB_RESTART" ] || [ "$DB_RESTART" = "null" ]; then
  echo "Error: db.restart not found in $CONFIG_FILE" >&2
  exit 1
fi

STATE_DIR="${EXASOL_VM_CONTAINER_STATE_DIR:-/var/lib}"
STATE_FILE="$STATE_DIR/container-state.sha256"
CONTAINER_RUNTIME_STATE_FILE="$STATE_DIR/container-runtime-state.sha256"
LOG_DIR="$EXASOL_VM_HOST_SHARED_DIR/logs"

log_msg() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] [DB] $1"
  logger -t init-db "$1" 2>/dev/null || true
}

version_check_state_payload() {
  if [ -f "$VERSION_CHECK_RUNTIME_CONFIG_FILE" ]; then
    sha256sum "$VERSION_CHECK_RUNTIME_CONFIG_FILE"
  else
    printf 'absent\n'
  fi
}

clamp_integer() {
  CLAMP_VALUE="$1"
  CLAMP_MIN="$2"
  CLAMP_MAX="$3"
  CLAMP_DEFAULT="$4"

  case "$CLAMP_VALUE" in
    ''|*[!0-9]*)
      CLAMP_VALUE="$CLAMP_DEFAULT"
      ;;
  esac
  if [ "$CLAMP_VALUE" -lt "$CLAMP_MIN" ]; then
    CLAMP_VALUE="$CLAMP_MIN"
  fi
  if [ "$CLAMP_VALUE" -gt "$CLAMP_MAX" ]; then
    CLAMP_VALUE="$CLAMP_MAX"
  fi
  printf '%s' "$CLAMP_VALUE"
}

load_version_check_config() {
  NANO_VERSION_CHECK_ENABLED=0
  NANO_VERSION_CHECK_ENDPOINT=
  NANO_VERSION_CHECK_IDENTITY=
  NANO_VERSION_CHECK_OPERATING_SYSTEM=
  NANO_VERSION_CHECK_INTERVAL_SEC=$NANO_VERSION_CHECK_DEFAULT_INTERVAL_SEC
  NANO_VERSION_CHECK_RETRY_INTERVAL_SEC=$NANO_VERSION_CHECK_DEFAULT_INTERVAL_SEC

  if [ ! -f "$VERSION_CHECK_RUNTIME_CONFIG_FILE" ]; then
    log_msg "No version-check runtime config found; Nano version checks disabled in exasol.conf"
    return
  fi

  VERSION_CHECK_ENABLED_VALUE=$(jq -r '.enabled // false' "$VERSION_CHECK_RUNTIME_CONFIG_FILE" 2>/dev/null) || {
    log_msg "Invalid version-check runtime config; Nano version checks disabled in exasol.conf"
    return
  }
  if [ "$VERSION_CHECK_ENABLED_VALUE" != "true" ]; then
    log_msg "Version-check runtime config disables Nano version checks in exasol.conf"
    return
  fi

  VERSION_CHECK_ENDPOINT_VALUE=$(jq -r '.url // empty' "$VERSION_CHECK_RUNTIME_CONFIG_FILE" 2>/dev/null) || {
    log_msg "Invalid version-check runtime config; Nano version checks disabled in exasol.conf"
    return
  }
  if [ -z "$VERSION_CHECK_ENDPOINT_VALUE" ] || [ "$VERSION_CHECK_ENDPOINT_VALUE" = "null" ]; then
    log_msg "Version-check runtime config has no URL; Nano version checks disabled in exasol.conf"
    return
  fi

  VERSION_CHECK_IDENTITY_VALUE=$(jq -r '.identity // "NONE"' "$VERSION_CHECK_RUNTIME_CONFIG_FILE" 2>/dev/null) || {
    VERSION_CHECK_IDENTITY_VALUE=NONE
  }
  if [ -z "$VERSION_CHECK_IDENTITY_VALUE" ] || [ "$VERSION_CHECK_IDENTITY_VALUE" = "null" ]; then
    VERSION_CHECK_IDENTITY_VALUE=NONE
  fi

  VERSION_CHECK_OPERATING_SYSTEM_VALUE=$(jq -r '.operating_system // .version_check_operating_system // empty' "$VERSION_CHECK_RUNTIME_CONFIG_FILE" 2>/dev/null) || {
    VERSION_CHECK_OPERATING_SYSTEM_VALUE=
  }

  VERSION_CHECK_INTERVAL_VALUE=$(jq -r ".interval_seconds // $NANO_VERSION_CHECK_DEFAULT_INTERVAL_SEC" "$VERSION_CHECK_RUNTIME_CONFIG_FILE" 2>/dev/null) || {
    VERSION_CHECK_INTERVAL_VALUE=$NANO_VERSION_CHECK_DEFAULT_INTERVAL_SEC
  }

  NANO_VERSION_CHECK_ENABLED=1
  NANO_VERSION_CHECK_ENDPOINT="$VERSION_CHECK_ENDPOINT_VALUE"
  NANO_VERSION_CHECK_IDENTITY="$VERSION_CHECK_IDENTITY_VALUE"
  NANO_VERSION_CHECK_OPERATING_SYSTEM="$VERSION_CHECK_OPERATING_SYSTEM_VALUE"
  NANO_VERSION_CHECK_INTERVAL_SEC=$(clamp_integer "$VERSION_CHECK_INTERVAL_VALUE" "$NANO_VERSION_CHECK_MIN_INTERVAL_SEC" "$NANO_VERSION_CHECK_MAX_INTERVAL_SEC" "$NANO_VERSION_CHECK_DEFAULT_INTERVAL_SEC")
  NANO_VERSION_CHECK_RETRY_INTERVAL_SEC=$(clamp_integer "$NANO_VERSION_CHECK_INTERVAL_SEC" "$NANO_VERSION_CHECK_MIN_INTERVAL_SEC" "$NANO_VERSION_CHECK_MAX_RETRY_INTERVAL_SEC" "$NANO_VERSION_CHECK_DEFAULT_INTERVAL_SEC")
  log_msg "Configured Nano version checks: endpoint=$NANO_VERSION_CHECK_ENDPOINT operating_system=$NANO_VERSION_CHECK_OPERATING_SYSTEM interval=${NANO_VERSION_CHECK_INTERVAL_SEC}s retry_interval=${NANO_VERSION_CHECK_RETRY_INTERVAL_SEC}s"
}

log_diagnostics() {
  log_msg "Diagnostic dump start"
  log_msg "dmesg (last 40 lines)"
  dmesg 2>/dev/null | tail -40 | while IFS= read -r line; do log_msg "$line"; done || true
  log_msg "podman info"
  podman info 2>&1 | while IFS= read -r line; do log_msg "$line"; done || true
  log_msg "podman ps -a"
  podman ps -a 2>&1 | while IFS= read -r line; do log_msg "$line"; done || true
  if podman ps -a --format "{{.Names}}" 2>/dev/null | grep -q "^${DB_CONTAINER_NAME}$"; then
    log_msg "podman inspect $DB_CONTAINER_NAME"
    podman inspect "$DB_CONTAINER_NAME" 2>&1 | while IFS= read -r line; do log_msg "$line"; done || true
    log_msg "podman logs $DB_CONTAINER_NAME"
    podman logs "$DB_CONTAINER_NAME" 2>&1 | while IFS= read -r line; do log_msg "$line"; done || true
  fi
  log_msg "Diagnostic dump end"
}

# Function to update init output file with container ports
update_output_ports() {
  jq --argjson db "$DB_PORT" '.ports.db = $db' "$INIT_OUTPUT_FILE" > "${INIT_OUTPUT_FILE}.tmp" && mv "${INIT_OUTPUT_FILE}.tmp" "$INIT_OUTPUT_FILE"
  log_msg "Updated init output file with database port"
}

# Create logs directory
mkdir -p "$LOG_DIR" 2>/dev/null || true
mkdir -p "$STATE_DIR" 2>/dev/null || true

log_msg "Starting container initialization"
log_msg "initial container state (podman ps -a)"
podman ps -a 2>&1 | while IFS= read -r line; do log_msg "$line"; done || true

load_version_check_config
CURRENT_RUNTIME_SHA=$(version_check_state_payload | sha256sum | cut -d' ' -f1)

CONTAINER_RUNTIME_CHANGED=true
if [ -f "$CONTAINER_RUNTIME_STATE_FILE" ]; then
  PREVIOUS_RUNTIME_SHA=$(cat "$CONTAINER_RUNTIME_STATE_FILE")
  if [ "$CURRENT_RUNTIME_SHA" = "$PREVIOUS_RUNTIME_SHA" ]; then
    CONTAINER_RUNTIME_CHANGED=false
  else
    log_msg "Container runtime configuration has changed"
  fi
else
  log_msg "No previous container runtime configuration state found"
fi

# Check if tarball exists
if [ ! -f "$DB_CONTAINER_TARBALL" ]; then
  log_msg "Error: Container tarball not found: $DB_CONTAINER_TARBALL"
  exit 1
fi

log_msg "Found container tarball: $DB_CONTAINER_TARBALL_NAME"

# Calculate checksum
CURRENT_SHA=$(sha256sum "$DB_CONTAINER_TARBALL" | cut -d' ' -f1)
log_msg "Tarball checksum: $CURRENT_SHA"

# Check if any images exist in podman storage
IMAGE_COUNT=$(podman images --format "{{.Repository}}" 2>/dev/null | wc -l)

# Check if we need to load/reload the container
RELOAD_NEEDED=true
if [ "$IMAGE_COUNT" -gt 0 ] && [ -f "$STATE_FILE" ]; then
  PREVIOUS_SHA=$(cat "$STATE_FILE")
  if [ "$CURRENT_SHA" = "$PREVIOUS_SHA" ]; then
    log_msg "Container unchanged since last load"
    RELOAD_NEEDED=false
  else
    log_msg "Container has changed, will reload"
  fi
elif [ "$IMAGE_COUNT" -eq 0 ]; then
  log_msg "No container images found in podman storage, will load"
else
  log_msg "No previous state found, will load container"
fi

if [ "$RELOAD_NEEDED" = "true" ]; then
  log_msg "Cleaning up old DB container and images..."
  
  # Stop and remove the specific DB container if it exists
  if podman ps -a --format "{{.Names}}" | grep -q "^${DB_CONTAINER_NAME}$"; then
    log_msg "Stopping and removing existing DB container: $DB_CONTAINER_NAME"
    podman stop "$DB_CONTAINER_NAME" 2>/dev/null || true
    podman rm "$DB_CONTAINER_NAME" 2>/dev/null || true
  fi
  
  # Remove only exasol-nano images (not all images)
  EXASOL_IMAGES=$(podman images --format "{{.Repository}}:{{.Tag}}" | grep "^exasol-nano" || true)
  if [ -n "$EXASOL_IMAGES" ]; then
    for image in $EXASOL_IMAGES; do
      log_msg "Removing Exasol Nano image: $image"
      podman rmi -f "$image" 2>/dev/null || true
    done
  fi
  
  # Load the new container
  log_msg "Loading container image..."
  if LOAD_OUTPUT=$(podman load < "$DB_CONTAINER_TARBALL" 2>&1); then
    log_msg "Container loaded successfully"
    
    # Extract the loaded image name from podman load output
    # Output format: "Loaded image: docker.io/library/exasol-nano:tag" or similar
    # Use sed instead of grep -P for BusyBox compatibility
    LOADED_IMAGE=$(echo "$LOAD_OUTPUT" | sed -n 's/.*Loaded image[^:]*:[[:space:]]*//p' | tr -d '[:space:]')
    if [ -z "$LOADED_IMAGE" ]; then
      log_msg "Error: Could not determine loaded image name from output"
      exit 1
    fi
    
    # Tag it with a predictable name for easy reference
    IMAGE_TAG="localhost/${DB_CONTAINER_NAME}:latest"
    log_msg "Tagging loaded image $LOADED_IMAGE as $IMAGE_TAG"
    if ! podman tag "$LOADED_IMAGE" "$IMAGE_TAG" 2>&1; then
      log_msg "Error: Failed to tag image"
      exit 1
    fi
    
    echo "$CURRENT_SHA" > "$STATE_FILE"
    sync
    log_msg "Image and state file flushed to disk"
  else
    LOAD_RC=$?
    log_msg "Error: podman load failed (exit $LOAD_RC) for $DB_CONTAINER_TARBALL"
    log_msg "podman output: $LOAD_OUTPUT"
    exit 1
  fi
fi

# Check if container is already running
if podman ps --format "{{.Names}}" | grep -q "^${DB_CONTAINER_NAME}$"; then
  log_msg "Container already running"
  if [ "$CONTAINER_RUNTIME_CHANGED" = "true" ]; then
    log_msg "Container runtime configuration changed; running container will keep its current version-check configuration until restart"
  fi
  update_output_ports
  exit 0
fi

# Check if container exists but isn't running - restart it if the image is current
if podman ps -a --format "{{.Names}}" | grep -q "^${DB_CONTAINER_NAME}$"; then
  if [ "$RELOAD_NEEDED" = "false" ] && [ "$CONTAINER_RUNTIME_CHANGED" = "false" ]; then
    log_msg "Restarting existing stopped container"
    if podman start "$DB_CONTAINER_NAME" 2>&1; then
      log_msg "Container restarted successfully"
      update_output_ports
      exit 0
    else
      log_msg "Failed to restart container, will remove and recreate"
      log_diagnostics
      podman rm "$DB_CONTAINER_NAME" 2>/dev/null || true
    fi
  else
    log_msg "Removing stopped container (image or runtime configuration changed)"
    podman rm "$DB_CONTAINER_NAME" 2>/dev/null || true
  fi
fi

# Use the predictable tagged image name
IMAGE_NAME="localhost/${DB_CONTAINER_NAME}:latest"
log_msg "Using image: $IMAGE_NAME"

run_db_container() {
  if [ "$NANO_VERSION_CHECK_ENABLED" = "1" ]; then
    log_msg "Starting DB container with Nano version checks enabled in exasol.conf"
  else
    log_msg "Starting DB container with Nano version checks disabled in exasol.conf"
  fi
  podman run -d \
    --name "$DB_CONTAINER_NAME" \
    --shm-size="$DB_SHM_SIZE" \
    --pids-limit="$DB_PIDS_LIMIT" \
    --security-opt "$DB_SECURITY_OPT" \
    --restart "$DB_RESTART" \
    -p "$DB_PORT:$DB_PORT" \
    "$IMAGE_NAME" "$@"
}
# Start the container
# Append Nano's documented "init" config arguments so version-check settings
# are persisted to exasol.conf instead of being one-run environment overrides.
set -- init
if [ -n "$DB_PARAMS" ]; then
  set -- "$@" "params=$DB_PARAMS"
fi
set -- "$@" "VERSION_CHECK_ENABLED=$NANO_VERSION_CHECK_ENABLED"
if [ "$NANO_VERSION_CHECK_ENABLED" = "1" ]; then
  set -- "$@" \
    "VERSION_CHECK_ENDPOINT=$NANO_VERSION_CHECK_ENDPOINT" \
    "VERSION_CHECK_IDENTITY=$NANO_VERSION_CHECK_IDENTITY" \
    "VERSION_CHECK_INTERVAL_SEC=$NANO_VERSION_CHECK_INTERVAL_SEC" \
    "VERSION_CHECK_RETRY_INTERVAL_SEC=$NANO_VERSION_CHECK_RETRY_INTERVAL_SEC"
  if [ -n "$NANO_VERSION_CHECK_OPERATING_SYSTEM" ]; then
    set -- "$@" "VERSION_CHECK_OPERATING_SYSTEM=$NANO_VERSION_CHECK_OPERATING_SYSTEM"
  fi
fi

log_msg "Starting container: $DB_CONTAINER_NAME with shm-size=$DB_SHM_SIZE pids-limit=$DB_PIDS_LIMIT security-opt=$DB_SECURITY_OPT restart=$DB_RESTART db-params=[$DB_PARAMS]"
PODMAN_RUN_RC=0
run_db_container "$@" || PODMAN_RUN_RC=$?

if [ "$PODMAN_RUN_RC" -ne 0 ]; then
  log_msg "Error: podman run failed with exit code $PODMAN_RUN_RC"
  log_diagnostics
  exit "$PODMAN_RUN_RC"
fi
log_msg "Container started successfully"
echo "$CURRENT_RUNTIME_SHA" > "$CONTAINER_RUNTIME_STATE_FILE"
sync
log_msg "Container state flushed to disk"
update_output_ports

log_msg "Database initialization complete"
