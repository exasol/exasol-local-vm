param(
    [string]$sharedDir,
    [string]$vhdPath
)

$ErrorActionPreference = "Stop"

# Create VHD (dynamic, 1GB max size)
Write-Host "Creating VHD for shared directory..."
New-VHD -Path $vhdPath -Dynamic -SizeBytes 1GB | Out-Null

# Mount the VHD
Write-Host "Mounting VHD..."
$mountedDisk = Mount-VHD -Path $vhdPath -Passthru

try {
    # Initialize the disk
    $diskNumber = $mountedDisk.Number
    Initialize-Disk -Number $diskNumber -PartitionStyle MBR | Out-Null
    
    # Create partition and format
    Write-Host "Creating partition and formatting..."
    $partition = New-Partition -DiskNumber $diskNumber -UseMaximumSize -AssignDriveLetter
    $driveLetter = $partition.DriveLetter
    Format-Volume -DriveLetter $driveLetter -FileSystem NTFS -NewFileSystemLabel "exasol-data" -Confirm:$false | Out-Null
    
    # Copy files from shared directory to VHD
    Write-Host "Copying files from $sharedDir to VHD..."
    $destination = "${driveLetter}:\"
    if (Test-Path $sharedDir) {
        Get-ChildItem -Path $sharedDir -Recurse | ForEach-Object {
            $targetPath = $_.FullName.Replace($sharedDir, $destination)
            if ($_.PSIsContainer) {
                New-Item -ItemType Directory -Path $targetPath -Force | Out-Null
            } else {
                Copy-Item -Path $_.FullName -Destination $targetPath -Force
            }
        }
    }
    
    Write-Host "VHD created and populated successfully"
} finally {
    # Dismount the VHD
    Write-Host "Dismounting VHD..."
    Dismount-VHD -Path $vhdPath
}
