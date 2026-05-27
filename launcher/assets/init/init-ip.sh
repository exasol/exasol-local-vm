#!/bin/sh
# Get VM IP address and update init output file
set -eu

log_msg() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] [IP] $1"
  logger -t init-ip "$1"
}

# Validate INIT_OUTPUT_FILE
if [ -z "${INIT_OUTPUT_FILE:-}" ]; then
  echo "Error: INIT_OUTPUT_FILE environment variable is not set" >&2
  exit 1
fi

log_msg "Getting VM IP address"

# Get the IP address of the default route interface
# Use awk instead of grep -P (Perl regex) for BusyBox compatibility
VM_IP=$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}' | head -n1 || true)

if [ -z "$VM_IP" ]; then
  log_msg "Warning: Could not determine VM IP address"
  VM_IP="unknown"
fi

log_msg "VM IP address: $VM_IP"

# Update init output file with IP address
jq --arg ip "$VM_IP" '.ip = $ip' "$INIT_OUTPUT_FILE" > "${INIT_OUTPUT_FILE}.tmp" && mv "${INIT_OUTPUT_FILE}.tmp" "$INIT_OUTPUT_FILE"

log_msg "Updated init output file with IP address"