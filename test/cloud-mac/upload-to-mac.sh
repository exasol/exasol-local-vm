#!/usr/bin/env bash
# Upload the mac-arm64 release package to the cloud Mac and run setup-mac.sh.
# Requires: tofu applied in test/cloud-mac, release/mac-arm64.tar.xz built via `task package-mac`

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
ARCHIVE="$PROJECT_ROOT/release/mac-arm64.tar.xz"
ARCHIVE_NAME="$(basename "$ARCHIVE")"

if [ ! -f "$ARCHIVE" ]; then
    echo "Error: $ARCHIVE not found. Run 'task package-mac' first."
    exit 1
fi

echo "==> Getting cloud Mac connection details..."
SSH_KEY=$(cd "$SCRIPT_DIR" && tofu output -no-color -raw ssh_key_path)
PUBLIC_IP=$(cd "$SCRIPT_DIR" && tofu output -no-color -raw public_ip)
SSH_OPTS="-i $SSH_KEY -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"
echo "    Host: ec2-user@$PUBLIC_IP"
echo "    Key:  $SSH_KEY"

echo "==> Uploading $ARCHIVE_NAME to cloud Mac ($(du -sh "$ARCHIVE" | cut -f1))..."
scp $SSH_OPTS "$ARCHIVE" "ec2-user@$PUBLIC_IP:~/$ARCHIVE_NAME"
echo "    Upload complete"

echo "==> Uploading setup-mac.sh..."
scp $SSH_OPTS "$SCRIPT_DIR/setup-mac.sh" "ec2-user@$PUBLIC_IP:~/setup-mac.sh"

echo "==> Running setup-mac.sh on cloud Mac..."
# shellcheck disable=SC2029
ssh $SSH_OPTS "ec2-user@$PUBLIC_IP" "chmod +x ~/setup-mac.sh && ~/setup-mac.sh"

echo ""
echo "==> Done! To connect to the cloud Mac:"
echo "    ssh $SSH_OPTS ec2-user@$PUBLIC_IP"
