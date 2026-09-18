# Optional compatibility launcher. Installation is now owned by the binary.
$ErrorActionPreference = 'Stop'
$binary = Join-Path $PSScriptRoot 'windows.exe'
if (-not (Test-Path $binary)) {
    & (Join-Path $PSScriptRoot 'build.ps1')
    $binary = Join-Path $PSScriptRoot '.build/windows.exe'
}
& $binary @args
exit $LASTEXITCODE
