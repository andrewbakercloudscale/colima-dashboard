#!/usr/bin/env bash
# Installs Colima, the Docker tools and Colima Dashboard on macOS, and starts the dashboard.
#
#   curl -fsSL https://raw.githubusercontent.com/andrewbakercloudscale/colima-dashboard/main/install.sh | bash
#
# What it does, in order (each step is skipped when already done):
#   1. Checks for Homebrew (it does not install Homebrew: that needs your password).
#   2. brew install colima docker docker-compose docker-buildx docker-credential-helper
#   3. Tells the docker CLI where Homebrew keeps compose and buildx (~/.docker/config.json).
#   4. Downloads the latest Colima Dashboard release, checks its SHA-256, installs it into
#      Homebrew's bin folder (no sudo), starts it at login and opens it in your browser.
#   5. Adds "127.0.0.1 colima.localhost" to /etc/hosts so http://colima.localhost:7766 works in
#      every browser. This is the only step that uses sudo (it asks for your Mac password); say
#      no and the dashboard still works at http://127.0.0.1:7766.
# It does not start a Colima VM: press Start in the dashboard (the first start takes a while).
#
# Options: --dry-run (print what it would do), --no-service (do not start at login),
#          --kubectl (also install kubectl).
set -euo pipefail

REPO="andrewbakercloudscale/colima-dashboard"
DRY=0
SERVICE=1
KUBECTL=0
for arg in "$@"; do
    case "${arg}" in
    --dry-run) DRY=1 ;;
    --no-service) SERVICE=0 ;;
    --kubectl) KUBECTL=1 ;;
    -h | --help)
        echo "usage: install.sh [--dry-run] [--no-service] [--kubectl]"
        exit 0
        ;;
    *)
        echo "unknown option ${arg}" >&2
        exit 2
        ;;
    esac
done

say() { printf '\033[1m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33mwarning:\033[0m %s\n' "$*" >&2; }
die() {
    printf '\033[31merror:\033[0m %s\n' "$*" >&2
    exit 1
}
run() {
    if [ "${DRY}" -eq 1 ]; then
        printf '   would run: %s\n' "$*"
    else
        "$@"
    fi
}

[ "$(uname -s)" = "Darwin" ] || die "Colima Dashboard is for macOS"
case "$(uname -m)" in
arm64) ARCH=arm64 ;;
x86_64) ARCH=amd64 ;;
*) die "unsupported CPU $(uname -m)" ;;
esac

# 1. Homebrew
if ! command -v brew >/dev/null 2>&1; then
    for b in /opt/homebrew/bin/brew /usr/local/bin/brew; do
        [ -x "${b}" ] && eval "$("${b}" shellenv)" && break
    done
fi
command -v brew >/dev/null 2>&1 || die "Homebrew is needed first. Install it from https://brew.sh (paste its one line into Terminal), then run this again."
BREW_PREFIX="$(brew --prefix)"
say "Homebrew found at ${BREW_PREFIX}"

if pgrep -f "Rancher Desktop.app/Contents/MacOS" >/dev/null 2>&1; then
    warn "Rancher Desktop is running. Quit it before starting Colima: both want the docker socket."
fi

# 2. Colima and the Docker tools
pkgs=(colima docker docker-compose docker-buildx docker-credential-helper)
[ "${KUBECTL}" -eq 1 ] && pkgs+=(kubernetes-cli)
missing=()
for p in "${pkgs[@]}"; do
    brew list --formula "${p}" >/dev/null 2>&1 || missing+=("${p}")
done
if [ "${#missing[@]}" -gt 0 ]; then
    say "Installing ${missing[*]}"
    run brew install "${missing[@]}"
else
    say "Colima and the Docker tools are already installed"
fi

# 3. Let the docker CLI find compose and buildx from Homebrew
PLUGINS="${BREW_PREFIX}/lib/docker/cli-plugins"
CFG="${HOME}/.docker/config.json"
if [ -f "${CFG}" ] && grep -q "${PLUGINS}" "${CFG}"; then
    say "docker already looks in ${PLUGINS}"
elif [ "${DRY}" -eq 1 ]; then
    say "Would add ${PLUGINS} to cliPluginsExtraDirs in ${CFG}"
else
    say "Adding ${PLUGINS} to cliPluginsExtraDirs in ${CFG}"
    mkdir -p "${HOME}/.docker"
    [ -s "${CFG}" ] || echo '{}' >"${CFG}"
    cp "${CFG}" "${CFG}.bak-colima-dashboard"
    if grep -q '"cliPluginsExtraDirs"' "${CFG}"; then
        plutil -insert cliPluginsExtraDirs.0 -string "${PLUGINS}" "${CFG}"
    else
        plutil -insert cliPluginsExtraDirs -json "[\"${PLUGINS}\"]" "${CFG}"
    fi
fi

# 4. Colima Dashboard
TAG="$(curl -fsSI "https://github.com/${REPO}/releases/latest" | awk -F/ 'tolower($1) ~ /^location:/ {print $NF}' | tr -d '\r')"
[ -n "${TAG}" ] || die "could not find the latest Colima Dashboard release"
VERSION="${TAG#v}"
ASSET="colima-dashboard_${VERSION}_darwin_${ARCH}.tar.gz"
BASE="https://github.com/${REPO}/releases/download/${TAG}"
DEST="${BREW_PREFIX}/bin/colima-dashboard"
say "Installing Colima Dashboard ${VERSION} to ${DEST}"
if [ "${DRY}" -eq 0 ]; then
    work="$(mktemp -d)"
    trap 'rm -rf "${work}"' EXIT
    curl -fsSL -o "${work}/${ASSET}" "${BASE}/${ASSET}"
    curl -fsSL -o "${work}/checksums.txt" "${BASE}/checksums.txt"
    want="$(awk -v f="${ASSET}" '$2 == f {print $1}' "${work}/checksums.txt")"
    got="$(shasum -a 256 "${work}/${ASSET}" | awk '{print $1}')"
    [ -n "${want}" ] && [ "${want}" = "${got}" ] || die "the download does not match the release checksum; nothing was installed"
    tar -xzf "${work}/${ASSET}" -C "${work}" colima-dashboard
    install -m 0755 "${work}/colima-dashboard" "${DEST}"
else
    printf '   would download %s, check it against checksums.txt and install it\n' "${BASE}/${ASSET}"
fi

# 5. An easy address: colima.localhost. Names under .localhost never leave this Mac.
URL="http://127.0.0.1:7766"
if grep -Eq '^[[:space:]]*127\.0\.0\.1[[:space:]].*colima\.localhost' /etc/hosts 2>/dev/null; then
    URL="http://colima.localhost:7766"
else
    say "Adding colima.localhost to /etc/hosts (sudo asks for your Mac password)"
    if [ "${DRY}" -eq 1 ]; then
        printf '   would append: 127.0.0.1 colima.localhost\n'
    elif printf '127.0.0.1 colima.localhost\n' | sudo tee -a /etc/hosts >/dev/null; then
        sudo dscacheutil -flushcache 2>/dev/null || true
        sudo killall -HUP mDNSResponder 2>/dev/null || true
        URL="http://colima.localhost:7766"
    else
        warn "skipped; the dashboard is at http://127.0.0.1:7766 (its General page has a button to add the name later)"
    fi
fi

if [ "${SERVICE}" -eq 1 ]; then
    say "Starting Colima Dashboard now and at every login"
    run "${DEST}" install-service
    if [ "${DRY}" -eq 0 ]; then
        sleep 1
        "${DEST}" open || true
    fi
else
    say "Start it with: colima-dashboard"
fi

say "Done. Colima Dashboard is at ${URL} (it updates itself; untick 'Update automatically' on its General page to stop that)."
say "Press Start on the General page to create your first Colima VM."
