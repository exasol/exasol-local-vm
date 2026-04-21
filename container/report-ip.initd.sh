#!/sbin/openrc-run

description="Report VM IP address to console"

depend()
{
        need networking
        after networking
}

start() {
        ebegin "Reporting VM IP address"
        
        # Wait briefly for interface to be fully configured
        sleep 1
        
        # Get IPv4 address from eth0
        IP=$(ip -4 addr show eth0 2>/dev/null | awk '/inet / {print $2}' | cut -d/ -f1 | head -n1)
        
        if [ -n "$IP" ]; then
                # Log to console in a parseable format
                echo "*** EXASOL_VM_IP=$IP ***" > /dev/console
                eend 0
        else
                ewarn "Could not determine VM IP address"
                eend 1
        fi
        
        return 0
}
