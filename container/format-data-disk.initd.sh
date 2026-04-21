#!/sbin/openrc-run

name="format-data-disk"
description="Format data disk if unformatted"

depend() {
  need dev dev-mount sysfs
  before fsck localmount grow-var-fs
}

find_data_disk() {
  # The data disk is the virtio block device that has NO child partitions.
  # The boot disk has a GPT partition table (vdaN/vdbN children).
  for sysdev in /sys/class/block/vd*; do
    [ -d "$sysdev" ] || continue
    name="${sysdev##*/}"
    # Skip partition entries themselves (e.g. vda1)
    [ -e "$sysdev/partition" ] && continue
    # Skip whole disks that have child partitions (boot disk)
    has_parts=0
    for child in "$sysdev"/"$name"*; do
      [ -e "$child" ] && has_parts=1 && break
    done
    [ "$has_parts" -eq 1 ] && continue
    echo "/dev/$name"
    return 0
  done
  return 1
}

start() {
  ebegin "Checking data disk"

  disk="$(find_data_disk)"
  if [ -z "$disk" ] || [ ! -b "$disk" ]; then
    eerror "No data disk found among /dev/vd* devices."
    eerror "Ensure the launcher creates and attaches data.img."
    eend 1
    return 1
  fi

  einfo "Detected data disk: $disk"
  # Persist for downstream services (grow-var-fs, etc.)
  mkdir -p /run
  echo "$disk" > /run/exasol-data-disk

  # Diagnostics: log everything we know about this device so a future wipe
  # incident is debuggable from vm-console.log.
  size_bytes="$(blockdev --getsize64 "$disk" 2>/dev/null || echo unknown)"
  size_gb="unknown"
  if [ "$size_bytes" != "unknown" ] && [ -n "$size_bytes" ]; then
    size_gb="$(( size_bytes / 1024 / 1024 / 1024 ))"
  fi
  einfo "Data disk size: ${size_bytes} bytes (~${size_gb} GiB)"
  einfo "blkid (cached):  $(blkid "$disk" 2>/dev/null || echo '<none>')"
  einfo "blkid (probed):  $(blkid -p "$disk" 2>/dev/null || echo '<none>')"

  # Probe the disk authoritatively. blkid alone has bitten us during early boot
  # (busybox blkid + stale/empty cache => empty TYPE/LABEL even on a valid fs,
  # which would cause us to wrongly reformat and wipe /var).
  #
  # Use dumpe2fs -h (reads the ext4 superblock directly) as the source of truth.
  # If that succeeds, we have a valid ext4 fs and MUST NOT format.
  einfo "Probing data disk with dumpe2fs..."
  if sb="$(dumpe2fs -h "$disk" 2>/dev/null)"; then
    fslabel="$(printf '%s\n' "$sb" | awk -F: '/^Filesystem volume name:/ {sub(/^[[:space:]]+/,"",$2); print $2; exit}')"
    fsblocks="$(printf '%s\n' "$sb" | awk -F: '/^Block count:/ {gsub(/[[:space:]]/,"",$2); print $2; exit}')"
    fsbsize="$(printf '%s\n' "$sb"  | awk -F: '/^Block size:/  {gsub(/[[:space:]]/,"",$2); print $2; exit}')"
    einfo "Data disk has valid ext4 superblock (label='$fslabel' blocks=$fsblocks blocksize=$fsbsize)"
    if [ "$fslabel" = "exasol-data" ]; then
      einfo "Data disk already formatted with expected label; skipping format"
      eend 0
      return 0
    fi
    # Valid ext4 but wrong label: relabel in place (preserves data).
    einfo "Relabeling existing ext4 fs to 'exasol-data' (preserving data)..."
    if e2label "$disk" exasol-data >/dev/null 2>&1; then
      einfo "Relabeled successfully"
      eend 0
      return 0
    else
      eerror "Failed to relabel; refusing to format to avoid data loss"
      eend 1
      return 1
    fi
  fi

  # No valid ext4 superblock. Cross-check with blkid -p (probe, bypasses cache)
  # before we destroy anything.
  fstype="$(blkid -p -s TYPE -o value "$disk" 2>/dev/null)"
  if [ -n "$fstype" ] && [ "$fstype" != "ext4" ]; then
    eerror "Data disk has unexpected filesystem type '$fstype'; refusing to format"
    eend 1
    return 1
  fi

  einfo "Formatting data disk as ext4 (no valid ext4 superblock found)..."
  if mkfs.ext4 -L exasol-data -F "$disk" >/dev/null 2>&1; then
    einfo "Data disk formatted successfully"
    /sbin/mdev -s
    sleep 1
    eend 0
  else
    eerror "Failed to format data disk"
    eend 1
  fi
}
