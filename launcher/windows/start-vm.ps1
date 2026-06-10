# Copyright 2026 Exasol AG
# SPDX-License-Identifier: MIT

param(
    [string]$vmName,
    [int]$cpuCount,
    [int]$ramSize,
    [string]$diskPath,
    [string]$sharedPath = "",
    [string]$vhdPath = ""
)

# Check if VM already exists
$existingVM = Get-VM -Name $vmName -ErrorAction SilentlyContinue
if ($existingVM) {
    Write-Host "VM $vmName already exists, starting it..."
    Start-VM -Name $vmName
} else {
    Write-Host "Creating new VM: $vmName"
    
    # Create new VM
    New-VM -Name $vmName -MemoryStartupBytes ($ramSize * 1MB) -Generation 2 -NoVHD
    
    # Set processor count
    Set-VMProcessor -VMName $vmName -Count $cpuCount
    
    # Add existing disk
    Add-VMHardDiskDrive -VMName $vmName -Path $diskPath
    
    # Add shared directory VHD as second disk (if provided)
    if ($vhdPath -and (Test-Path $vhdPath)) {
        Write-Host "Attaching shared directory VHD..."
        Add-VMHardDiskDrive -VMName $vmName -Path $vhdPath
    }
    
    # Add network adapter connected to Default Switch
    Add-VMNetworkAdapter -VMName $vmName -SwitchName "Default Switch"
    
    # Enable guest services for file sharing
    Enable-VMIntegrationService -VMName $vmName -Name "Guest Service Interface"
    
    # Start the VM
    Start-VM -Name $vmName
}

Write-Host "VM $vmName is running"

# Wait for VM to get an IP address (timeout after 30 seconds)
Write-Host "Waiting for VM to get IP address..."
$timeout = 30
$elapsed = 0
$ipAddress = $null

while ($elapsed -lt $timeout) {
    Start-Sleep -Seconds 2
    $elapsed += 2
    
    $adapter = Get-VMNetworkAdapter -VMName $vmName
    $ipAddresses = $adapter.IPAddresses
    
    # Look for IPv4 address (not link-local)
    foreach ($ip in $ipAddresses) {
        if ($ip -match '^\d+\.\d+\.\d+\.\d+$' -and -not $ip.StartsWith("169.254.")) {
            $ipAddress = $ip
            break
        }
    }
    
    if ($ipAddress) {
        break
    }
}

if ($ipAddress) {
    Write-Host "VM_IP:$ipAddress"
} else {
    Write-Host "Warning: VM did not receive an IP address within $timeout seconds"
    Write-Host "VM_IP:unknown"
}
