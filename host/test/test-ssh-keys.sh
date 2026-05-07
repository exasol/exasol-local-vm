#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 1 ]; then
    echo "Error: pass image architecture as argument (x86_64 or aarch64)" >&2
    exit 1
fi
IMG_ARCH="${1}"
shift

TEST_DIR="$(mktemp -d)"
export VM_CONTAINER_NAME="exasol-nano-test-vm-$$"
export VM_SHARED_DIR="${TEST_DIR}/shared"
mkdir -p "${VM_SHARED_DIR}"

TEST_KEY="${TEST_DIR}/test-key"
AUTHORIZED_KEYS="${TEST_DIR}/shared/authorized_keys"

# Cleanup function to stop VM and clean up test files
cleanup() {
  local exit_code=$?
  echo ""
  echo "==> Cleaning up..."

  # Stop the VM if it's running
  echo "==> Stopping VM..."
  ./host/run/stop-qemu-container.sh || true
  
  # Clean up test files
  echo "==> Removing test files..."
  rm -rf "${TEST_DIR}"
  
  exit "$exit_code"
}

# Set trap to ensure cleanup happens on exit (success or failure)
trap cleanup EXIT INT TERM

echo "==> Testing SSH key import feature"

# Generate a new test key
echo "==> Generating test SSH key..."
ssh-keygen -t ed25519 -f "$TEST_KEY" -N "" -C "test-key-for-vm"

# Add the key to authorized_keys in shared folder
echo "==> Adding test key to shared/authorized_keys..."
mkdir -p shared
cat "$TEST_KEY.pub" >> "$AUTHORIZED_KEYS"

# Start the VM
echo "==> Starting VM..."
./host/run/start-qemu-container.sh "${IMG_ARCH}"

# Try to connect with the test key (retry for 5 minutes)
echo "==> Testing SSH connection with test key (will retry for 5 minutes)..."
MAX_WAIT=600  # 10 minutes in seconds
ELAPSED=0
START_TIME=$(date +%s)

while [ $ELAPSED -lt $MAX_WAIT ] && podman container exists "${VM_CONTAINER_NAME}"; do
    if ssh -i "$TEST_KEY" -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 exasol@127.0.0.1 "echo 'SSH key import successful!'" 2>/dev/null; then
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
