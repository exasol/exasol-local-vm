#!/usr/bin/env bash
set -euo pipefail

if [ "$(uname -s)" != "Darwin" ]; then
    echo "Error: This test only works on macOS" >&2
    exit 1
fi

if [ "$#" -lt 1 ]; then
    echo "Error: pass image architecture as argument (aarch64)" >&2
    exit 1
fi
IMG_ARCH="${1}"
shift

if [ "$IMG_ARCH" != "aarch64" ]; then
    echo "Error: macOS launcher only supports aarch64" >&2
    exit 1
fi

TEST_DIR="$(mktemp -d)"
LAUNCHER="./release/mac-runner-${IMG_ARCH}"

if [ ! -f "$LAUNCHER" ]; then
    echo "Error: Mac launcher not found at $LAUNCHER" >&2
    echo "Run: task build-mac-launcher IMG_ARCH=${IMG_ARCH}" >&2
    exit 1
fi

TEST_KEY="${TEST_DIR}/test-key"
VM_DIR="$(pwd)/vm"
AUTHORIZED_KEYS="${VM_DIR}/shared/authorized_keys"

# Cleanup function to stop VM and clean up test files
cleanup() {
  local exit_code=$?
  echo ""
  echo "==> Cleaning up..."

  # If test failed, display vm.log for debugging
  if [ "$exit_code" -ne 0 ] && [ -f "vm.log" ]; then
    echo ""
    echo "==> VM Log (vm.log) for debugging:"
    echo "===================================="
    cat vm.log
    echo "===================================="
    echo ""
  fi

  # Stop the VM if it's running
  echo "==> Stopping VM..."
  "$LAUNCHER" stop || true
  
  # Clean up test files
  echo "==> Removing test files..."
  rm -rf "${TEST_DIR}"
  
  exit "$exit_code"
}

# Set trap to ensure cleanup happens on exit (success or failure)
trap cleanup EXIT INT TERM

echo "==> Testing SSH key import feature with Mac launcher"

# Initialize VM if needed
if [ ! -d "$VM_DIR" ]; then
    echo "==> Initializing VM..."
    "$LAUNCHER" init
fi

# Generate a new test key
echo "==> Generating test SSH key..."
ssh-keygen -t ed25519 -f "$TEST_KEY" -N "" -C "test-key-for-vm"

# Add the key to authorized_keys in shared folder
echo "==> Adding test key to vm/shared/authorized_keys..."
mkdir -p "${VM_DIR}/shared"
cat "$TEST_KEY.pub" >> "$AUTHORIZED_KEYS"

# Start the VM with 2 CPUs, 2GB RAM, and shared directory
echo "==> Starting VM..."
"$LAUNCHER" start 2 2048 "${VM_DIR}/shared"

# Read SSH port from vm-state.json
VM_STATE_FILE="vm-state.json"
if [ ! -f "$VM_STATE_FILE" ]; then
    echo "Error: vm-state.json not found" >&2
    exit 1
fi

# Extract SSH port using grep and sed (no jq dependency)
SSH_PORT=$(grep -o '"ssh":[[:space:]]*[0-9]*' "$VM_STATE_FILE" | grep -o '[0-9]*$')

if [ -z "$SSH_PORT" ]; then
    echo "Error: Could not read SSH port from vm-state.json" >&2
    exit 1
fi

echo "==> SSH port: $SSH_PORT"

# Try to connect with the test key (retry for 5 minutes)
echo "==> Testing SSH connection with test key (will retry for 5 minutes)..."
MAX_WAIT=300  # 5 minutes in seconds
ELAPSED=0
START_TIME=$(date +%s)

while [ $ELAPSED -lt $MAX_WAIT ]; do
    if ssh -i "$TEST_KEY" -p "$SSH_PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 exasol@127.0.0.1 "echo 'SSH key import successful!'" 2>/dev/null; then
        echo "==> ✓ Test passed: Successfully connected with imported key after ${ELAPSED} seconds"
        SUCCESS=true
        break
    fi

    echo "==> Connection failed, retrying... (${ELAPSED}s elapsed, ${MAX_WAIT}s timeout)"
    sleep 2

    # Update elapsed time
    CURRENT_TIME=$(date +%s)
    ELAPSED=$((CURRENT_TIME - START_TIME))   
done

# Report results
if [ "${SUCCESS:-false}" = "true" ]; then
    echo ""
    echo "==> ✓ SSH key import test PASSED"
else
    echo ""
    echo "==> ✗ SSH key import test FAILED"
    exit 1
fi
