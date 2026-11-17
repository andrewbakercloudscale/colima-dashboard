#!/usr/bin/env bash
# Builds the menu bar helper (internal/menubar/MenuBar.swift) as one binary for Apple silicon
# and Intel Macs, into internal/menubar/menubar.bin, for `go build -tags menubar`.
set -euo pipefail
cd "$(dirname "$0")/../internal/menubar"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
swiftc -O -target arm64-apple-macos12 MenuBar.swift -o "${work}/arm64"
swiftc -O -target x86_64-apple-macos12 MenuBar.swift -o "${work}/x86_64"
lipo -create "${work}/arm64" "${work}/x86_64" -output menubar.bin
strip -x menubar.bin 2>/dev/null || true
ls -la menubar.bin
