#!/bin/sh
# Builds the Windows programs (from any OS) plus one for this machine. Needs Go 1.22+.
set -e
cd "$(dirname "$0")"
mkdir -p dist
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H=windowsgui -X main.guiBuild=1" -o dist/Ensemble.exe .
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o dist/ensemble-cli.exe .
go build -trimpath -ldflags "-s -w" -o dist/ensemble .
echo "Built dist/Ensemble.exe, dist/ensemble-cli.exe and dist/ensemble"
