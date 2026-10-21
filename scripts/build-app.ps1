# Builds Helium Syncer for Windows (64-bit Intel/AMD and ARM).
# Needs Go. Output in bin\:
#   Helium Syncer.exe          tray app (no console window)
#   helium-syncer.exe          command-line tool
#   arm64\...                  the same for Windows on ARM
$ErrorActionPreference = 'Stop'
# The same Go version as the macOS build (Go 1.26: Windows 10 and 11).
if (-not $env:GOTOOLCHAIN) { $env:GOTOOLCHAIN = 'go1.26.10' }
Set-Location (Split-Path -Parent $PSScriptRoot)

foreach ($arch in 'amd64', 'arm64') {
    $out = if ($arch -eq 'amd64') { 'bin' } else { 'bin\arm64' }
    New-Item -ItemType Directory -Force $out | Out-Null
    Write-Host "Building for $arch..."
    $env:GOOS = 'windows'; $env:GOARCH = $arch; $env:CGO_ENABLED = '0'
    go build -trimpath -ldflags '-H windowsgui -s -w' -o "$out\Helium Syncer.exe" .\cmd\helium-syncer-app
    if ($LASTEXITCODE) { throw "app build failed ($arch)" }
    go build -trimpath -ldflags '-s -w' -o "$out\helium-syncer.exe" .\cmd\helium-syncer
    if ($LASTEXITCODE) { throw "command-line build failed ($arch)" }
}
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
Write-Host 'Built bin\Helium Syncer.exe and bin\arm64\Helium Syncer.exe'
