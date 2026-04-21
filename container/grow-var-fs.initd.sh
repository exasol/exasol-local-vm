#!/sbin/openrc-run

name="grow-var-vs"
description="Resize /var to fill image"

depend() {
  need dev dev-mount sysfs
  after format-data-disk
  before localmount
}

start() {
  ebegin "Growing partition and filesystem"

  # format-data-disk identifies the actual data disk (vda or vdb depending on
  # virtio enumeration order) and writes it to /run/exasol-data-disk.
  if [ -r /run/exasol-data-disk ]; then
    disk="$(cat /run/exasol-data-disk)"
  fi

  if [ -z "$disk" ] || [ ! -b "$disk" ]; then
    eerror "Data disk path not found (expected in /run/exasol-data-disk)"
    eend 1
    return 1
  fi

  einfo "Growing data disk: $disk"
  rc=1
  
  # Check if this is a full disk or a partition
  if [ -e "/sys/class/block/${disk##*/}/partition" ]; then
    # It's a partition - grow the partition then resize the filesystem
    partition="$(cat "/sys/class/block/${disk##*/}/partition")"
    device="/dev/$(basename "$(realpath "/sys/class/block/${disk##*/}/..")")"

    # growpart returns non-zero when partition can't grow (already at max size)
    # Handle gracefully to allow script to continue
    if growpart "${device}" "${partition}" 2>&1 | grep -q "NOCHANGE"; then
      einfo "Partition already at maximum size"
      rc=0
    else
      # growpart will make the disk disappear, trigger mdev to get it back
      # Would be nice if we didn't have to hard-code this, but alas...
      /sbin/mdev -s
      resize2fs "${disk}"
      einfo "Partition and filesystem grown successfully"
      rc=0
    fi
  else
    # It's a full disk (like /dev/vda) - just resize the filesystem
    einfo "Data disk is not partitioned, resizing filesystem only"
    if resize2fs "${disk}" 2>&1; then
      einfo "Filesystem resized successfully"
      rc=0
    else
      eerror "Failed to resize filesystem"
      rc=1
    fi
  fi

  eend $rc
}
