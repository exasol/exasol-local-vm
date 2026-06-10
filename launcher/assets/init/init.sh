# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

#!/bin/sh
# Run all initialization scripts in order
set -u  # Only exit on undefined variables, not on command failures

# Validate required environment variables
if [ -z "${EXASOL_VM_INIT_DIR:-}" ]; then
  echo "Error: EXASOL_VM_INIT_DIR environment variable is not set" >&2
  exit 1
fi

if [ -z "${EXASOL_VM_HOST_SHARED_DIR:-}" ]; then
  echo "Error: EXASOL_VM_HOST_SHARED_DIR environment variable is not set" >&2
  exit 1
fi

if [ ! -d "$EXASOL_VM_INIT_DIR" ]; then
  echo "Error: EXASOL_VM_INIT_DIR directory does not exist: $EXASOL_VM_INIT_DIR" >&2
  exit 1
fi

if [ ! -d "$EXASOL_VM_HOST_SHARED_DIR" ]; then
  echo "Error: EXASOL_VM_HOST_SHARED_DIR directory does not exist: $EXASOL_VM_HOST_SHARED_DIR" >&2
  exit 1
fi

INIT_OUTPUT_FILE_TEMPLATE="$EXASOL_VM_INIT_DIR/init-output.json.template"
INIT_OUTPUT_FILE="$EXASOL_VM_INIT_DIR/init-output.json"

cp "$INIT_OUTPUT_FILE_TEMPLATE" "$INIT_OUTPUT_FILE"

# Export for child scripts
export INIT_OUTPUT_FILE
export EXASOL_VM_INIT_DIR
export EXASOL_VM_HOST_SHARED_DIR

DIR="$EXASOL_VM_INIT_DIR"

log_msg() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1"
  logger -t init "$1"
}

# Function to run an init script with error handling
run_init_script() {
  local script_name="$1"
  local description="$2"
  local script_path="$DIR/$script_name"
  
  if [ -f "$script_path" ]; then
    log_msg "Running $description..."
    if sh "$script_path"; then
      log_msg "$description completed successfully"
    else
      log_msg "WARNING: $description failed with exit code $?"
      FAILED_SCRIPTS="${FAILED_SCRIPTS}${script_name} "
      OVERALL_SUCCESS=false
    fi
  else
    log_msg "$description script not found, skipping"
  fi
}

log_msg "=== Starting VM initialization ==="

# Track failures
FAILED_SCRIPTS=""
OVERALL_SUCCESS=true

# Run initialization scripts in order
run_init_script "init-ssh.sh" "SSH key initialization"
run_init_script "init-db.sh" "database initialization"
run_init_script "init-ip.sh" "IP initialization"

# Print the init output file for the launcher to read
if [ -f "$INIT_OUTPUT_FILE" ]; then
  log_msg "Init output:"
  echo "=== INIT OUTPUT START ==="
  jq '.' "$INIT_OUTPUT_FILE"
  echo "=== INIT OUTPUT END ==="
  
  # Copy to shared directory for verification
  cp "$INIT_OUTPUT_FILE" "$EXASOL_VM_HOST_SHARED_DIR/vm-init-output.json"
  log_msg "Init output saved to $EXASOL_VM_HOST_SHARED_DIR/vm-init-output.json"
else
  log_msg "Warning: Init output file not found at $INIT_OUTPUT_FILE"
fi

# Report on initialization results
if [ "$OVERALL_SUCCESS" = "true" ]; then
  log_msg "=== VM initialization complete - ALL SCRIPTS SUCCEEDED ==="
  exit 0
else
  log_msg "=== VM initialization complete - SOME SCRIPTS FAILED ==="
  log_msg "Failed scripts: $FAILED_SCRIPTS"
  log_msg "Partial initialization data may be available in vm-init-output.json"
  # Exit with success anyway to allow VM to continue booting
  # The launcher will detect missing data in the init output
  exit 0
fi