#!/sbin/openrc-run

name="grow-var-vs"
description="Resize /var to fill image"

depend() {
  need dev dev-mount sysfs
  before localmount
}

start() {
  ebegin "Growing partition and filesystem"

  rc=1
  disk="$(readlink -f /dev/disk/by-label/exasol-data)"
  partition="$(cat "/sys/class/block/${disk##*/}/partition")"
  device="/dev/$(basename "$(realpath "/sys/class/block/${disk##*/}/..")")"

  # growpart returns non-zero when partition can't grow (already at max size)
  # Handle gracefully to allow script to continue
  if growpart "${device}" "${partition}" 2>&1 | grep -q "NOCHANGE"; then
    echo "Partition already at maximum size"
    rc=0
  else
    # growpart will make the disk disappear, trigger mdev to get it back
    # Would be nice if we didn't have to hard-code this, but alas...
    /sbin/mdev -s
    resize2fs "${disk}"
    echo "Partition and filesystem grown successfully"
    rc=0
  fi

  eend $rc
}
