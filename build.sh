#!/usr/bin/env bash
# Gera dist/Deixa.exe (Windows 10/11, 64 bits) a partir de Linux, macOS ou Windows (Git Bash).
# Requer Go 1.22+. Uso: ./build.sh
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p dist
go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --out rsrc --arch amd64
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H windowsgui" -o dist/Deixa.exe .
rm -f rsrc_windows_amd64.syso
ls -lh dist/Deixa.exe
