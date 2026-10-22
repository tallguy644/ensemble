# Builds Ensemble on Windows. Needs Go 1.22+ (winget install GoLang.Go).
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
New-Item -ItemType Directory -Force dist | Out-Null
go build -trimpath -ldflags "-s -w -H=windowsgui -X main.guiBuild=1" -o dist\Ensemble.exe .
go build -trimpath -ldflags "-s -w" -o dist\ensemble-cli.exe .
Write-Host "Built dist\Ensemble.exe (double-click app) and dist\ensemble-cli.exe (command line)."
