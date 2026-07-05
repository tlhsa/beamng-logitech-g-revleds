# Packs the BeamNG mod (the contents of mod\) into dist\logitech_g_revleds.zip.
# Uses forward-slash entry names, which BeamNG's virtual filesystem requires.
#
# Usage:  powershell -ExecutionPolicy Bypass -File tools\pack-mod.ps1

$ErrorActionPreference = 'Stop'
$root    = Split-Path -Parent $PSScriptRoot
$srcDir  = Join-Path $root 'mod'
$distDir = Join-Path $root 'dist'
$zipPath = Join-Path $distDir 'logitech_g_revleds.zip'

if (-not (Test-Path $distDir)) { New-Item -ItemType Directory -Path $distDir | Out-Null }
if (Test-Path $zipPath) { Remove-Item $zipPath -Force }

Add-Type -AssemblyName System.IO.Compression.FileSystem
Add-Type -AssemblyName System.IO.Compression

$zip = [System.IO.Compression.ZipFile]::Open($zipPath, [System.IO.Compression.ZipArchiveMode]::Create)
try {
    $files = Get-ChildItem -LiteralPath $srcDir -Recurse -File
    foreach ($f in $files) {
        # relative path with forward slashes
        $rel = $f.FullName.Substring($srcDir.Length + 1).Replace('\', '/')
        [System.IO.Compression.ZipFileExtensions]::CreateEntryFromFile(
            $zip, $f.FullName, $rel,
            [System.IO.Compression.CompressionLevel]::Optimal) | Out-Null
        Write-Host "added  $rel"
    }
}
finally {
    $zip.Dispose()
}
Write-Host "`nwrote $zipPath ($((Get-Item $zipPath).Length) bytes)"
