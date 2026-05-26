
#!/bin/sh
# Run all initialization scripts in order
set -eu

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

log_msg "=== Starting VM initialization ==="

# Run SSH key initialization first
if [ -f "$DIR/init-ssh.sh" ]; then
  log_msg "Running SSH key initialization..."
  sh "$DIR/init-ssh.sh"
fi

# Run database/container initialization
if [ -f "$DIR/init-db.sh" ]; then
  log_msg "Running database initialization..."
  sh "$DIR/init-db.sh"
fi

# Run IP initialization
if [ -f "$DIR/init-ip.sh" ]; then
  log_msg "Running IP initialization..."
  sh "$DIR/init-ip.sh"
fi

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

log_msg "=== VM initialization complete ==="