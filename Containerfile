FROM alpine AS alpine_base

FROM alpine_base AS base

RUN apk upgrade -aU

RUN <<-'EOF'
set -eu
apk add alpine-base
# Handle poweroff signal
apk add acpid
apk add openssh
# podman needs iptables but it's not pulled in for some reason?
apk add podman fuse-overlayfs slirp4netns shadow-uidmap iptables

apk add linux-virt
EOF

RUN <<-'EOF'
set -eu
printf "ttyS0\nttyAMA0\nhvc0\n" >> /etc/securetty
for console in ttyS0 ttyAMA0 hvc0; do
    sed -Ei "s|^[# ]*(${console}:.*)|\\1|" /etc/inittab || true
done
EOF

### Enable services

COPY --link <<-'EOF' /etc/containers/storage.conf
[storage]
driver = "overlay"
runroot = "/run/containers/storage"
graphroot = "/var/lib/containers/storage"

[storage.options.overlay]
mount_program = "/usr/bin/fuse-overlayfs"
mountopt = "nodev"
EOF

RUN tee /etc/subuid /etc/subgid <<-'EOF'
containers:100000:65536
EOF

### Enable autologin

COPY --link <<-'EOF' /usr/sbin/autologin
#!/bin/sh
exec login -f root
EOF

RUN <<-'EOF'
set -eu
chmod +x /usr/sbin/autologin
sed -i 's@:respawn:/sbin/getty@:respawn:/sbin/getty -n -l /usr/sbin/autologin@g' /etc/inittab
EOF

### VM startup scripts

COPY --link container/init /init

# TODO grow /var partition when the packaged disk is enlarged.
# TODO resize /var filesystem after partition growth.
# TODO mount /mnt/host from virtiofs or a Hyper-V data disk.
# TODO decide the final SSH/user model; this image currently autologins root.
COPY --link <<-'EOF' /etc/fstab
LABEL=exasol-data  /var  ext4  defaults  0 2
EOF

COPY --link container/exasol-network /etc/init.d/exasol-network

RUN --mount=type=bind,source=container,target=/host/ <<-"EOF"
set -eu
/host/rc_add sysinit  devfs dmesg mdev hwdrivers
/host/rc_add boot     exasol-network
/host/rc_add boot     cgroups modules hwclock swap hostname sysctl bootmisc syslog seedrng localmount networking
/host/rc_add default  podman acpid sshd
/host/rc_add shutdown killprocs savecache mount-ro
EOF


### Clean up

RUN apk cache purge


###

FROM alpine_base AS initramfs_build

RUN apk add zstd

RUN --mount=type=bind,from=base,target=/image <<-'EOF'
set -eu
cd /image
find . -xdev -not -path './boot/*' -not -path './var/*' \
    | cpio --quiet -H newc -o \
    | zstdmt -9 \
    > /initramfs.img.zst
EOF

###

FROM scratch AS initramfs

COPY --link --from=initramfs_build /initramfs.img* /

###

FROM scratch AS kernel

COPY --link --from=base /boot/vmlinuz-virt /

###

FROM fedora AS vm_image_build

ARG DISK_PADDING_SIZE_MB=64

ARG KERNEL_CMDLINE="console=tty0 console=ttyS0,115200 console=ttyAMA0,115200 console=hvc0"

RUN dnf install -y \
    cpio \
    dosfstools \
    e2fsprogs \
    mtools \
    qemu-img \
    systemd \
    systemd-boot-unsigned \
    systemd-ukify \
    zstd && \
    dnf clean all

RUN --mount=type=bind,from=base,target=/base \
    --mount=type=bind,from=kernel,target=/kernel \
    --mount=type=bind,from=initramfs,target=/initramfs \
    --mount=type=bind,source=container,target=/host \
    mkdir -p /artifacts && \
    cp -a /base/var /artifacts/var && \
    cp /kernel/vmlinuz-virt /artifacts/vmlinuz-virt && \
    cp /initramfs/initramfs.img.zst /artifacts/initramfs.img.zst && \
    DISK_PADDING_SIZE_MB="$DISK_PADDING_SIZE_MB" \
    KERNEL_CMDLINE="$KERNEL_CMDLINE" \
    /host/build-vm-image.sh /artifacts

###

FROM scratch AS all

COPY --from=kernel /* /
COPY --from=initramfs /* /
COPY --from=vm_image_build /artifacts/arch.txt /artifacts/kernel-cmdline.txt /artifacts/disk.img /artifacts/disk.qcow2 /artifacts/disk.vhdx /
