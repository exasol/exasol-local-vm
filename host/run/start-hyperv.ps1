#!/usr/bin/env pwsh
#Requires -RunAsAdministrator

param(
    [Parameter(Position=0)]
    [int]$ProcessorCount = 0,

    [Parameter(Position=1)]
    [int]$MemoryMB = 0,

    [string]$VMName = "Exasol-VM",
    [string]$VHDXPath = "exasol-vm.vhdx",
    [string]$SwitchName = "Default Switch"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$configPath = Join-Path $PSScriptRoot "vm-config.json"
$config = $null
if (Test-Path $configPath) {
    $config = Get-Content -Raw -Path $configPath | ConvertFrom-Json
}

if ($ProcessorCount -le 0) {
    $ProcessorCount = if ($config -and $config.cpus) { [int]$config.cpus } else { 2 }
}

if ($MemoryMB -le 0) {
    $MemoryMB = if ($config -and $config.memoryMB) { [int]$config.memoryMB } else { 2048 }
}

$memoryStartupBytes = $MemoryMB * 1MB

if (-not [System.IO.Path]::IsPathRooted($VHDXPath)) {
    $VHDXPath = Join-Path $PSScriptRoot $VHDXPath
}

if (-not (Test-Path $VHDXPath)) {
    throw "VHDX file not found: $VHDXPath"
}

try {
    $hyperV = Get-WindowsOptionalFeature -FeatureName Microsoft-Hyper-V-All -Online -ErrorAction Stop
    if ($hyperV.State -ne "Enabled") {
        throw "Hyper-V is not enabled."
    }
} catch {
    throw "Hyper-V is required to start this VM."
}

$switch = Get-VMSwitch -Name $SwitchName -ErrorAction SilentlyContinue
if (-not $switch) {
    $switch = Get-VMSwitch -Name "Default Switch" -ErrorAction SilentlyContinue
    if ($switch) {
        $SwitchName = "Default Switch"
    } else {
        $switch = Get-VMSwitch | Select-Object -First 1
        if (-not $switch) {
            throw "No Hyper-V virtual switch is available."
        }
        $SwitchName = $switch.Name
    }
}

$existingVM = Get-VM -Name $VMName -ErrorAction SilentlyContinue
if ($existingVM) {
    Set-VMProcessor -VMName $VMName -Count $ProcessorCount
    Set-VMMemory -VMName $VMName -StartupBytes $memoryStartupBytes -DynamicMemoryEnabled $true -MinimumBytes 512MB -MaximumBytes ($MemoryMB * 2MB)
    Set-VMFirmware -VMName $VMName -EnableSecureBoot Off

    if ($existingVM.State -ne "Running") {
        Start-VM -Name $VMName | Out-Null
    }
} else {
    New-VM -Name $VMName `
        -Generation 2 `
        -MemoryStartupBytes $memoryStartupBytes `
        -VHDPath $VHDXPath `
        -SwitchName $SwitchName | Out-Null

    Set-VMProcessor -VMName $VMName -Count $ProcessorCount
    Set-VMMemory -VMName $VMName -StartupBytes $memoryStartupBytes -DynamicMemoryEnabled $true -MinimumBytes 512MB -MaximumBytes ($MemoryMB * 2MB)
    Set-VMFirmware -VMName $VMName -EnableSecureBoot Off
    Set-VM -Name $VMName -AutomaticStartAction Nothing -AutomaticStopAction ShutDown

    Start-VM -Name $VMName | Out-Null
}

Start-Sleep -Seconds 2

$vm = Get-VM -Name $VMName
$vmIP = $null
$maxWaitSeconds = 300
$waitedSeconds = 0

while ($waitedSeconds -lt $maxWaitSeconds) {
    $adapter = Get-VMNetworkAdapter -VMName $VMName
    if ($adapter.IPAddresses) {
        $vmIP = $adapter.IPAddresses | Where-Object { $_ -match '^\d+\.\d+\.\d+\.\d+$' } | Select-Object -First 1
        if ($vmIP) {
            break
        }
    }
    Start-Sleep -Seconds 2
    $waitedSeconds += 2
}

$ipFilePath = Join-Path $PSScriptRoot "vm-ip.txt"
if ($vmIP) {
    $vmIP | Out-File -FilePath $ipFilePath -Encoding ASCII -NoNewline
} else {
    "IP not available yet" | Out-File -FilePath $ipFilePath -Encoding ASCII -NoNewline
}

Write-Host ""
Write-Host "========================================="
Write-Host "  Hyper-V VM Started"
Write-Host "========================================="
Write-Host ""
Write-Host "VM Information:"
Write-Host "  Name: $VMName"
Write-Host "  State: $($vm.State)"
Write-Host "  CPUs: $ProcessorCount"
Write-Host "  MemoryMB: $MemoryMB"
Write-Host "  VHDX: $VHDXPath"
Write-Host "  Switch: $SwitchName"
if ($vmIP) {
    Write-Host "  VM IP: $vmIP"
}
Write-Host "  IP file: $ipFilePath"

if ($config -and $config.ports -and $vmIP) {
    Write-Host ""
    Write-Host "Configured guest ports:"
    foreach ($portRule in $config.ports) {
        $proto = $portRule.protocol
        $vmPort = $portRule.vm
        Write-Host "  $proto://$vmIP:$vmPort"
    }
}
