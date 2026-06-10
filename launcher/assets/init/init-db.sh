#!/bin/sh
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

# Load and run Exasol Nano DB container
# Based on container/load-shared-container.sh
set -eu

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

STATE_FILE="/var/lib/container-state.sha256"
LOG_DIR="$EXASOL_VM_HOST_SHARED_DIR/logs"

log_msg() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] [DB] $1"
  logger -t init-db "$1"
}

# Function to update init output file with container ports
update_output_ports() {
  jq --argjson db "$DB_PORT" '.ports.db = $db' "$INIT_OUTPUT_FILE" > "${INIT_OUTPUT_FILE}.tmp" && mv "${INIT_OUTPUT_FILE}.tmp" "$INIT_OUTPUT_FILE"
  log_msg "Updated init output file with database port"
}

# Create logs directory
mkdir -p "$LOG_DIR" 2>/dev/null || true

log_msg "Starting container initialization"

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
  update_output_ports
  exit 0
fi

# Check if container exists but isn't running - restart it if the image is current
if podman ps -a --format "{{.Names}}" | grep -q "^${DB_CONTAINER_NAME}$"; then
  if [ "$RELOAD_NEEDED" = "false" ]; then
    log_msg "Restarting existing stopped container"
    if podman start "$DB_CONTAINER_NAME" 2>&1; then
      log_msg "Container restarted successfully"
      update_output_ports
      exit 0
    else
      log_msg "Failed to restart container, will remove and recreate"
      podman rm "$DB_CONTAINER_NAME" 2>/dev/null || true
    fi
  else
    log_msg "Removing stopped container (image changed)"
    podman rm "$DB_CONTAINER_NAME" 2>/dev/null || true
  fi
fi

# Use the predictable tagged image name
IMAGE_NAME="localhost/${DB_CONTAINER_NAME}:latest"
log_msg "Using image: $IMAGE_NAME"

# Start the container
log_msg "Starting container: $DB_CONTAINER_NAME with shm-size=$DB_SHM_SIZE"
podman run -d \
  --name "$DB_CONTAINER_NAME" \
  --shm-size="$DB_SHM_SIZE" \
  -p "$DB_PORT:$DB_PORT" \
  "$IMAGE_NAME"

if [ $? -eq 0 ]; then
  log_msg "Container started successfully"
  update_output_ports
else
  log_msg "Error: Failed to start container"
  exit 1
fi

log_msg "Database initialization complete"