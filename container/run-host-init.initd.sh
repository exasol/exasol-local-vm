#!/sbin/openrc-run
# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

description="Run host-provided initialization script"

depend() {
    need localmount
    after networking
}

start() {
    ebegin "Running host initialization script"
    
    if [ ! -d "/mnt/host" ]; then
        eerror "/mnt/host not mounted"
        eend 1
        return 1
    fi
    
    INIT_SCRIPT="/mnt/host/init/init.sh"
    
    if [ ! -f "$INIT_SCRIPT" ]; then
        ewarn "No init script found at $INIT_SCRIPT"
        eend 0
        return 0
    fi
    
    if [ ! -x "$INIT_SCRIPT" ]; then
        chmod +x "$INIT_SCRIPT"
    fi
    
    # Set environment variables for the init script
    export EXASOL_VM_INIT_DIR="/mnt/host/init"
    export EXASOL_VM_HOST_SHARED_DIR="/mnt/host"
    
    # Run init script and capture output to both console and log file
    # This allows the launcher to see the output markers in console log
    # while also keeping a persistent log for debugging
    INIT_LOG="/mnt/host/init.log"
    # Use POSIX-compliant approach: write to log first, then cat to console
    # This properly captures the init script exit code
    sh "$INIT_SCRIPT" 2>&1 > "$INIT_LOG"
    result=$?
    cat "$INIT_LOG"
    
    if [ $result -eq 0 ]; then
        eend 0
    else
        eerror "Init script failed with exit code $result"
        eerror "Check $INIT_LOG for details"
        eend $result
    fi
}
