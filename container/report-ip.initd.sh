#!/sbin/openrc-run
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

description="Report VM IP address and announce it to network peers"

depend()
{
        need networking
        after networking
}

announce_ip() {
        arping -U -q -c 3 -I "$1" "$2"
}

report_ip() {
        echo "*** EXASOL_VM_IP=$1 ***" > /dev/console
}

start() {
        ebegin "Reporting VM IP address"
        
        # Wait briefly for interface to be fully configured
        sleep 1
        
        # Get IPv4 address from eth0
        IP=$(ip -4 addr show eth0 2>/dev/null | awk '/inet / {print $2}' | cut -d/ -f1 | head -n1)
        
        if [ -n "$IP" ]; then
                # vmnet can reuse an IP while the macOS host still caches the old MAC.
                if ! announce_ip eth0 "$IP"; then
                        ewarn "Could not announce VM IP address to network peers"
                fi

                report_ip "$IP"
                eend 0
        else
                ewarn "Could not determine VM IP address"
                eend 1
        fi
        
        return 0
}
