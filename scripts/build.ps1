$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot
Push-Location $root
try {
    New-Item -ItemType Directory -Force output | Out-Null
    & go build -trimpath -ldflags '-s -w -H=windowsgui' -o output/IPv6Engine-0.8.0.exe .
    if ($LASTEXITCODE -ne 0) { throw 'Go build failed' }
    & (Join-Path $root 'native/build.ps1')
    Copy-Item output/EpicIPv6Native.exe output/IPv6-Research-Helper-0.8.0-windows-x64.exe
    Get-FileHash output/IPv6-Research-Helper-0.8.0-windows-x64.exe -Algorithm SHA256
} finally { Pop-Location }
