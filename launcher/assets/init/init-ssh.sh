
#!/bin/sh
# Import SSH keys from shared folder
# Based on container/import-shared-keys.sh
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

AUTHORIZED_KEYS="authorized_keys"
SHARED_KEYS="$EXASOL_VM_HOST_SHARED_DIR/$AUTHORIZED_KEYS"
USER_KEYS="$HOME/.ssh/authorized_keys"

log_msg() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] [SSH] $1"
  logger -t init-ssh "$1"
}

# Exit if shared folder not mounted or no keys file
if [ ! -f "$SHARED_KEYS" ]; then
  log_msg "No SSH keys found at $SHARED_KEYS, skipping"
  exit 0
fi

log_msg "Found SSH keys at $SHARED_KEYS"

# Create .ssh directory if it doesn't exist
mkdir -p "$HOME/.ssh"
chmod 700 "$HOME/.ssh"

# SECURITY: Clear existing keys - only keys in shared folder will have access
true > "$USER_KEYS"

# Import all keys from shared folder
while IFS= read -r key; do
  # Skip empty lines and comments
  [ -z "$key" ] && continue
  echo "$key" | grep -q "^#" && continue
  
  # Add key
  echo "$key" >> "$USER_KEYS"
  log_msg "Added SSH key: ${key%% *}..."
done < "$SHARED_KEYS"

# Set correct permissions
chmod 600 "$USER_KEYS"
chmod 700 "$HOME/.ssh"

# Update init output file with SSH port
if [ -n "${INIT_OUTPUT_FILE:-}" ]; then
  jq '.ports.ssh = 22' "$INIT_OUTPUT_FILE" > "${INIT_OUTPUT_FILE}.tmp" && mv "${INIT_OUTPUT_FILE}.tmp" "$INIT_OUTPUT_FILE"
  log_msg "Updated init output file with SSH port"
fi

log_msg "SSH keys imported successfully"