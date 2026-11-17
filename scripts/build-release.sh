#!/usr/bin/env bash
# Builds the release files Colima Dashboard's self-update expects, into dist/:
#   colima-dashboard_<version>_darwin_arm64.tar.gz
#   colima-dashboard_<version>_darwin_amd64.tar.gz
#   checksums.txt   (sha256, "<hex>  <file>" per line)
# It does not publish. Publishing is: gh release create v<version> dist/* (owner's call).
set -euo pipefail

VERSION="${1:-}"
if [[ ! "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "usage: $0 <version, e.g. 1.0.0>" >&2
    exit 1
fi
cd "$(dirname "$0")/.."
rm -rf dist
mkdir -p dist
bash scripts/build-menubar.sh
for arch in arm64 amd64; do
    work="$(mktemp -d)"
    CGO_ENABLED=0 GOOS=darwin GOARCH="${arch}" go build -trimpath -tags menubar \
        -ldflags "-s -w -X main.version=${VERSION}" -o "${work}/colima-dashboard" ./cmd/colima-dashboard
    cp LICENSE README.md "${work}/"
    tar -C "${work}" -czf "dist/colima-dashboard_${VERSION}_darwin_${arch}.tar.gz" colima-dashboard LICENSE README.md
    rm -rf "${work}"
done
(cd dist && shasum -a 256 ./*.tar.gz | sed 's#  \./#  #' >checksums.txt)
ls -la dist
cat dist/checksums.txt
