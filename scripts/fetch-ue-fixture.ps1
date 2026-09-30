$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot
$target = Join-Path $root 'testdata/ue-reference.chunk'
New-Item -ItemType Directory -Force (Split-Path $target) | Out-Null
$url = 'https://egs-cloudfront-chunks.epicgamescdn.com/Builds/UE5/Releases/CloudDir/ChunksV4/32/A5B3747FCABAF49B_5EA5DD4B4EEDE55022795F86E3C7EF06.chunk'
Invoke-WebRequest -UseBasicParsing $url -OutFile $target
$expected = '62b08120c9ea6edccfdc02a20ea1bee202529590b628abb34cee017bd71c8d7c'
if ((Get-FileHash $target -Algorithm SHA256).Hash.ToLowerInvariant() -ne $expected) {
    throw 'Official UE fixture checksum mismatch; do not use the downloaded fixture.'
}
Write-Host 'Verified official UE fixture. This fetch checks integrity, not IPv6 routing.'
