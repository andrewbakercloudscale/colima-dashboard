#!/usr/bin/env bash
# Starts a throwaway dashboard built from this checkout and clicks through every page with
# scripts/ui-crawl.js, at desktop width (light) and phone width (dark). Fails on any finding.
#
#   PLAYWRIGHT=/path/to/node_modules/playwright-core scripts/ci-crawl.sh [label]
#
# Uses whatever this Mac has (no colima, colima with no VM, a running VM), so CI runs it on a
# fresh runner before and after install.sh. Terminal never opens: COLIMA_DASHBOARD_NO_TERMINAL=1.
set -euo pipefail

main() {
    local label="${1:-crawl}" port="${PORT:-7766}"
    [ -n "${PLAYWRIGHT:-}" ] || { echo "set PLAYWRIGHT to playwright-core's folder" >&2; exit 2; }
    go build -o "${TMPDIR:-/tmp}/cd-crawl" ./cmd/colima-dashboard
    COLIMA_DASHBOARD_NO_TERMINAL=1 "${TMPDIR:-/tmp}/cd-crawl" --no-browser --port "${port}" >"${TMPDIR:-/tmp}/cd-crawl.log" 2>&1 &
    CRAWL_PID=$! # global: the EXIT trap runs after main's locals are gone
    trap 'kill "${CRAWL_PID}" 2>/dev/null || true' EXIT
    local token="" i
    for i in $(seq 1 30); do
        token="$(cat "${HOME}/Library/Application Support/colima-dashboard/session" 2>/dev/null || true)"
        curl -fsS -o /dev/null "http://127.0.0.1:${port}/" 2>/dev/null && [ -n "${token}" ] && break
        sleep 1
    done
    [ -n "${token}" ] || { cat "${TMPDIR:-/tmp}/cd-crawl.log"; echo "dashboard did not start" >&2; exit 1; }
    local rc=0
    echo "== ${label}: 1280 light"
    BASE="http://127.0.0.1:${port}" TOKEN="${token}" node scripts/ui-crawl.js 1280 || rc=1
    echo "== ${label}: 390 dark"
    BASE="http://127.0.0.1:${port}" TOKEN="${token}" node scripts/ui-crawl.js 390 dark || rc=1
    return "${rc}"
}

main "$@"
