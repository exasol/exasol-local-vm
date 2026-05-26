#!/sbin/openrc-run

name="format-data-disk"
description="Format data disk if unformatted"

depend() {
  need dev dev-mount sysfs
  before grow-var-fs localmount
}

start() {
  ebegin "Checking data disk"

  # Check if first virtio block device exists
  if [ ! -b /dev/vda ]; then
    ewarn "No separate data disk found (/dev/vda), using boot disk /var partition"
    eend 0
    return 0
  fi

  # Check if disk has a valid filesystem
  if blkid /dev/vda >/dev/null 2>&1; then
    # Already formatted
    einfo "Data disk already formatted"
    eend 0
    return 0
  fi

  # Disk exists but is not formatted - format it
  einfo "Formatting unformatted data disk as ext4..."
  if mkfs.ext4 -L exasol-data -F /dev/vda >/dev/null 2>&1; then
    einfo "Data disk formatted successfully"
    eend 0
  else
    eerror "Failed to format data disk"
    eend 1
  fi
}
