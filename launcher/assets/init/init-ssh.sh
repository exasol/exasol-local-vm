
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

# Function to set up SSH keys for a user
setup_user_keys() {
  local user_home="$1"
  local user_name=$(basename "$user_home")
  local user_ssh_dir="$user_home/.ssh"
  local user_keys="$user_ssh_dir/authorized_keys"
  
  log_msg "Setting up SSH keys for user: $user_name"
  
  # Create .ssh directory if it doesn't exist
  mkdir -p "$user_ssh_dir"
  chmod 700 "$user_ssh_dir"
  
  # SECURITY: Clear existing keys - only keys in shared folder will have access
  true > "$user_keys"
  
  # Import all keys from shared folder
  local key_count=0
  while IFS= read -r key; do
    # Skip empty lines and comments
    [ -z "$key" ] && continue
    echo "$key" | grep -q "^#" && continue
    
    # Add key
    echo "$key" >> "$user_keys"
    key_count=$((key_count + 1))
  done < "$SHARED_KEYS"
  
  # Set correct permissions
  chmod 600 "$user_keys"
  chmod 700 "$user_ssh_dir"
  
  log_msg "Added $key_count SSH key(s) for $user_name"
}

# Set up SSH keys for all users in /home (excluding root)
for user_home in /home/*; do
  # Skip if no directories found or if it's not a directory
  [ ! -d "$user_home" ] && continue
  
  setup_user_keys "$user_home"
done

# Update init output file with SSH port
if [ -n "${INIT_OUTPUT_FILE:-}" ]; then
  jq '.ports.ssh = 22' "$INIT_OUTPUT_FILE" > "${INIT_OUTPUT_FILE}.tmp" && mv "${INIT_OUTPUT_FILE}.tmp" "$INIT_OUTPUT_FILE"
  log_msg "Updated init output file with SSH port"
fi

log_msg "SSH keys imported successfully"