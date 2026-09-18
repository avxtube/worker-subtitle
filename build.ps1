param([ValidateSet('windows', 'linux')][string]$Target = 'windows')
$ErrorActionPreference = 'Stop'
# Keep both entry points on the same build/copy implementation.
& (Join-Path $PSScriptRoot 'build.bat') $Target
if ($LASTEXITCODE -ne 0) { throw "Build failed (exit $LASTEXITCODE)" }
