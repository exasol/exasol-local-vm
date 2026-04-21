#!/sbin/openrc-run

name="setup-var-structure"
description="Create required directory structure in /var"

depend() {
  need localmount
  before sshd run-host-init
}

start() {
  ebegin "Setting up /var directory structure"

  # /var lives on the data disk (ext4) and is empty on first boot.
  # Create the standard skeleton expected by Alpine services and by podman.
  # mkdir -p is idempotent so this is safe on every boot.
  mkdir -p \
    /var/cache \
    /var/empty \
    /var/lib \
    /var/lib/containers \
    /var/lib/misc \
    /var/local \
    /var/lock \
    /var/log \
    /var/opt \
    /var/run \
    /var/spool \
    /var/spool/cron \
    /var/spool/cron/crontabs \
    /var/spool/mail \
    /var/tmp

  # /var/tmp must be world-writable + sticky (podman writes temp files here).
  chmod 1777 /var/tmp

  # /var/empty: owned by root, not group/world writable (sshd requirement).
  chmod 755 /var/empty
  chown root:root /var/empty

  # /var/log writable for syslog etc.
  chmod 755 /var/log

  einfo "/var skeleton ready"
  eend 0
}
