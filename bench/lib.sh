#!/usr/bin/env bash
# Shared functions for bench.sh: tool control, process sets, timing and sampling.
# Sourced, never run. Every function returns non-zero on failure; nothing here exits.

export LC_ALL=C

BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESULTS_DIR="${RESULTS_DIR:-${BENCH_DIR}/results}"
TMP_DIR="${BENCH_DIR}/tmp"

COLIMA_PROFILE="${COLIMA_PROFILE:-bench}"
RD_APP="/Applications/Rancher Desktop.app"
RDCTL="${HOME}/.rd/bin/rdctl"
[ -x "${RDCTL}" ] || RDCTL="${RD_APP}/Contents/Resources/resources/darwin/bin/rdctl"

VM_XPC="com.apple.Virtualization.VirtualMachine"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'
if [[ -n "${NO_COLOR:-}" ]] || [[ "${TERM:-}" == "dumb" ]] || [[ ! -t 1 ]]; then
    RED="" GREEN="" YELLOW="" BLUE="" NC=""
fi

function log_info() { echo -e "${BLUE}[$(date +%H:%M:%S)]${NC} $*"; }
function log_ok() { echo -e "${GREEN}[$(date +%H:%M:%S)] ok${NC} $*"; }
function log_warn() { echo -e "${YELLOW}[$(date +%H:%M:%S)] warning${NC} $*" >&2; }
function log_error() { echo -e "${RED}[$(date +%H:%M:%S)] error${NC} $*" >&2; }

# Wall clock in milliseconds (macOS date has no %N).
function now_ms() { perl -MTime::HiRes=time -e 'printf "%d\n", time*1000'; }

function iso_now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

function docker_socket() {
    local tool="$1"
    case "${tool}" in
    colima) echo "${HOME}/.colima/${COLIMA_PROFILE}/docker.sock" ;;
    rancher) echo "${HOME}/.rd/docker.sock" ;;
    *) return 1 ;;
    esac
}

# Docker CLI pinned to one tool's socket, whatever the current context is.
function dk() {
    local tool="$1"
    shift
    local sock
    sock="$(docker_socket "${tool}")" || return 1
    # A private, empty Docker config: the same for both tools, and immune to credential helpers
    # that one tool installs and the other does not (osxkeychain from ~/.rd/bin, for one).
    mkdir -p "${TMP_DIR}/docker-config" || return 1
    [ -s "${TMP_DIR}/docker-config/config.json" ] || echo '{}' >"${TMP_DIR}/docker-config/config.json"
    DOCKER_CONFIG="${TMP_DIR}/docker-config" DOCKER_HOST="unix://${sock}" docker "$@"
}

# Appends one CSV row; writes the header first when the file is new.
function csv_row() {
    local file="$1" header="$2" row="$3"
    mkdir -p "$(dirname "${file}")" || return 1
    [ -s "${file}" ] || echo "${header}" >"${file}"
    echo "${row}" >>"${file}"
}

# PIDs belonging to a tool: its own processes plus the VZ virtual machine.
# Only one tool runs at a time, so any VZ VM process is attributed to the running one.
function tool_pids() {
    local tool="$1" pattern
    case "${tool}" in
    colima) pattern='^ *[0-9]+ ([^ ]*/colima( |$)|[^ ]*/colima-dashboard( |$)|[^ ]*limactl .*\.colima/|ssh: .*\.colima/)' ;;
    rancher) pattern='^ *[0-9]+ (/Applications/Rancher Desktop\.app/|[^ ]*limactl .*rancher-desktop/lima|ssh: .*rancher-desktop/)' ;;
    none) return 0 ;;
    *) return 1 ;;
    esac
    ps -axo pid=,command= | grep -E "${pattern}|^ *[0-9]+ [^ ]*${VM_XPC}" | grep -v -E 'grep|bench\.sh|footprint' | awk '{print $1}'
}

function vm_running() { pgrep -f "${VM_XPC}" >/dev/null; }

function wait_vm_gone() {
    local timeout_s="${1:-120}" waited=0
    while vm_running; do
        sleep 0.5
        waited=$((waited + 1))
        [ "${waited}" -ge $((timeout_s * 2)) ] && return 1
    done
    return 0
}

# Polls until the tool's Docker engine answers. Returns 1 on timeout.
function wait_docker() {
    local tool="$1" timeout_s="${2:-600}" deadline
    deadline=$(($(date +%s) + timeout_s))
    while [ "$(date +%s)" -lt "${deadline}" ]; do
        dk "${tool}" info >/dev/null 2>&1 && return 0
        sleep 0.5
    done
    return 1
}

# Config name to start flags. CPUS, MEMORY_GB, DISK_GB come from the environment (matched values).
function colima_flags() {
    local config="$1"
    case "${config}" in
    defaults) echo "" ;;
    matched) echo "--cpus ${CPUS} --memory ${MEMORY_GB} --disk ${DISK_GB} --vm-type vz --mount-type virtiofs" ;;
    matched-k8s) echo "--cpus ${CPUS} --memory ${MEMORY_GB} --disk ${DISK_GB} --vm-type vz --mount-type virtiofs --kubernetes" ;;
    *) return 1 ;;
    esac
}

function rancher_flags() {
    local config="$1"
    local common="--container-engine.name=moby --application.admin-access=false --application.path-management-strategy=manual"
    case "${config}" in
    defaults) echo "${common}" ;;
    matched) echo "${common} --virtual-machine.number-cpus=${CPUS} --virtual-machine.memory-in-gb=${MEMORY_GB} --kubernetes.enabled=false" ;;
    matched-k8s) echo "${common} --virtual-machine.number-cpus=${CPUS} --virtual-machine.memory-in-gb=${MEMORY_GB} --kubernetes.enabled=true" ;;
    *) return 1 ;;
    esac
}

# Recreates the bench Colima profile when the config changes, so flags never leak between configs.
function colima_prepare() {
    local config="$1" marker="${TMP_DIR}/colima-config"
    mkdir -p "${TMP_DIR}" || return 1
    if [ "$(cat "${marker}" 2>/dev/null)" != "${config}" ]; then
        log_info "colima: recreating profile ${COLIMA_PROFILE} for ${config}"
        colima delete -f -p "${COLIMA_PROFILE}" >/dev/null 2>&1
        echo "${config}" >"${marker}"
        return 2
    fi
    return 0
}

function tool_start() {
    local tool="$1" config="$2" flags
    case "${tool}" in
    colima)
        colima_prepare "${config}"
        flags="$(colima_flags "${config}")" || return 1
        # shellcheck disable=SC2086 # flags are a word list on purpose
        colima start -p "${COLIMA_PROFILE}" ${flags} >>"${TMP_DIR}/colima-start.log" 2>&1 || return 1
        ;;
    rancher)
        flags="$(rancher_flags "${config}")" || return 1
        # shellcheck disable=SC2086
        "${RDCTL}" start --no-modal-dialogs ${flags} >>"${TMP_DIR}/rancher-start.log" 2>&1 || return 1
        ;;
    *) return 1 ;;
    esac
    wait_docker "${tool}" 900
}

function tool_stop() {
    local tool="$1"
    case "${tool}" in
    colima) colima stop -p "${COLIMA_PROFILE}" >/dev/null 2>&1 ;;
    rancher) "${RDCTL}" shutdown >/dev/null 2>&1 ;;
    none) return 0 ;;
    *) return 1 ;;
    esac
    wait_vm_gone 180
}

function tool_version() {
    local tool="$1"
    case "${tool}" in
    colima) colima version 2>/dev/null | awk '/colima version/{print $3}' ;;
    rancher) defaults read "${RD_APP}/Contents/Info.plist" CFBundleShortVersionString 2>/dev/null ;;
    none) echo "-" ;;
    esac
}

# Kubernetes node readiness, checked inside the VM so no host kubectl is needed.
function k8s_ready() {
    local tool="$1" out
    case "${tool}" in
    colima) out="$(colima ssh -p "${COLIMA_PROFILE}" -- sudo k3s kubectl get nodes --no-headers 2>/dev/null)" ;;
    rancher) out="$("${RDCTL}" shell sudo k3s kubectl get nodes --no-headers 2>/dev/null)" ;;
    *) return 1 ;;
    esac
    echo "${out}" | grep -qw Ready
}

# Sum of physical footprint (what Activity Monitor calls Memory) of the given PIDs, in bytes.
function footprint_bytes() {
    [ "$#" -eq 0 ] && { echo 0; return 0; }
    local args=() pid
    for pid in "$@"; do args+=(-p "${pid}"); done
    footprint -f bytes --noCategories "${args[@]}" 2>/dev/null |
        awk '/phys_footprint:/{s+=$2} END{printf "%d\n", s}'
}

# Accumulated CPU seconds of the given PIDs (ps TIME column, [dd-]hh:mm:ss.cc or mm:ss.cc).
function cpu_seconds() {
    [ "$#" -eq 0 ] && { echo 0; return 0; }
    ps -o time= -p "$(
        IFS=,
        echo "$*"
    )" 2>/dev/null | awk '{
        t=$1; d=0
        if (index(t,"-")) { split(t,a,"-"); d=a[1]; t=a[2] }
        n=split(t,p,":"); s=0
        for (i=1;i<=n;i++) s=s*60+p[i]
        tot+=s+d*86400
    } END {printf "%.2f\n", tot}'
}

function load_avg() { sysctl -n vm.loadavg | awk '{print $2}'; }

function on_battery() { pmset -g batt | grep -q "Battery Power"; }

function powermetrics_allowed() { sudo -n /usr/bin/powermetrics -h >/dev/null 2>&1; }
