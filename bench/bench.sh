#!/usr/bin/env bash

# shellcheck source=lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

DEPENDENCIES=(colima docker jq bc perl python3 footprint top pmset)
SCRIPT_NAME=$(basename "$0")
VERSION="1.0.0"

CPUS="${CPUS:-2}"
MEMORY_GB="${MEMORY_GB:-4}"
DISK_GB="${DISK_GB:-100}"
SAMPLE_S=5

function usage() {
    cat <<EOM

Measures Colima or Rancher Desktop on this Mac for the comparison post. Raw CSVs go to bench/results/.
Only one tool runs at a time: each command stops the other tool (and other Colima profiles) first.

usage: ${SCRIPT_NAME} <command> [options]

commands:
    env          Record versions, hardware and load for this run
    footprint    Disk used by the tool (app, data, VM disks, caches)
    coldstart    Stop, then time start to engine ready to hello-world, and stop; --runs times
    idle         Settle, then sample memory, CPU and energy every ${SAMPLE_S}s for --minutes
    build        Time a cold and a warm image build of bench/workload/build
    io           Time file-heavy work on a bind mount and inside the container
    battery      Log battery charge and power draw every 30s for --minutes (Mac must be unplugged)
    all          env, coldstart, idle, build, io in that order
    stop         Stop the tool

options:
    -t|--tool      <name>    colima | rancher | none (none: nothing running, the baseline)
    -c|--config    <name>    defaults | matched | matched-k8s (default: matched)
    -r|--runs      <n>       Repeats for timed tests (default: 5)
    -m|--minutes   <n>       Sampling window for idle and battery (default: 10, battery 60)
    -s|--settle    <n>       Minutes to wait after start before idle sampling (default: 5)
    -h|--help                Show this help
    --version                Show version

environment:
    CPUS=${CPUS} MEMORY_GB=${MEMORY_GB} DISK_GB=${DISK_GB}   the matched configuration
    COLIMA_PROFILE=${COLIMA_PROFILE}                          Colima profile used for measuring

dependencies: ${DEPENDENCIES[*]}
power: powermetrics runs too when 'sudo -n /usr/bin/powermetrics' is allowed; otherwise top's energy impact only

examples:
    ${SCRIPT_NAME} all --tool colima --config matched
    ${SCRIPT_NAME} idle --tool none --minutes 10
    ${SCRIPT_NAME} battery --tool rancher --config defaults --minutes 60

EOM
    exit 1
}

function main() {
    local command="" tool="" config="matched" runs=5 minutes="" settle=5

    [ "$#" -eq 0 ] && usage
    while [ "$1" != "" ]; do
        case $1 in
        -t | --tool)
            shift
            tool="$1"
            ;;
        -c | --config)
            shift
            config="$1"
            ;;
        -r | --runs)
            shift
            runs="$1"
            ;;
        -m | --minutes)
            shift
            minutes="$1"
            ;;
        -s | --settle)
            shift
            settle="$1"
            ;;
        --version)
            echo "${SCRIPT_NAME} version ${VERSION}"
            exit 0
            ;;
        -h | --help) usage ;;
        -*)
            log_error "unknown option '$1'"
            usage
            ;;
        *)
            [ -n "${command}" ] && {
                log_error "one command at a time"
                usage
            }
            command="$1"
            ;;
        esac
        shift
    done

    case "${tool}" in
    colima | rancher | none) ;;
    *)
        log_error "--tool must be colima, rancher or none"
        usage
        ;;
    esac
    case "${config}" in
    defaults | matched | matched-k8s) ;;
    *)
        log_error "--config must be defaults, matched or matched-k8s"
        usage
        ;;
    esac
    exit_on_missing_tools "${DEPENDENCIES[@]}"
    if [ "${tool}" = "rancher" ] && [ ! -x "${RDCTL}" ]; then
        log_error "rdctl not found; is Rancher Desktop installed?"
        exit 1
    fi
    mkdir -p "${RESULTS_DIR}" "${TMP_DIR}" || exit 1
    export CPUS MEMORY_GB DISK_GB

    case "${command}" in
    env) record_env "${tool}" "${config}" ;;
    footprint) measure_footprint "${tool}" ;;
    coldstart) measure_coldstart "${tool}" "${config}" "${runs}" ;;
    idle) measure_idle "${tool}" "${config}" "${minutes:-10}" "${settle}" ;;
    build) measure_build "${tool}" "${config}" "${runs}" ;;
    io) measure_io "${tool}" "${config}" "${runs}" ;;
    battery) measure_battery "${tool}" "${config}" "${minutes:-60}" "${settle}" ;;
    stop) tool_stop "${tool}" ;;
    all)
        record_env "${tool}" "${config}" &&
            measure_coldstart "${tool}" "${config}" "${runs}" &&
            measure_idle "${tool}" "${config}" "${minutes:-10}" "${settle}" &&
            measure_build "${tool}" "${config}" "${runs}" &&
            measure_io "${tool}" "${config}" "${runs}"
        ;;
    *)
        log_error "unknown command '${command}'"
        usage
        ;;
    esac
    local rc=$?
    [ "${rc}" -eq 0 ] && log_ok "${command} ${tool} ${config} done" || log_error "${command} ${tool} ${config} failed (${rc})"
    exit "${rc}"
}

# Stops the other tool and any Colima profile but the bench one, so only the measured tool runs.
function make_exclusive() {
    local tool="$1" profile
    if [ "${tool}" != "rancher" ] && [ -x "${RDCTL}" ] && pgrep -f "Rancher Desktop.app/Contents/MacOS" >/dev/null; then
        log_info "stopping Rancher Desktop"
        "${RDCTL}" shutdown >/dev/null 2>&1
    fi
    for profile in $(colima list --json 2>/dev/null | jq -r 'select(.status=="Running") | .name'); do
        [ "${tool}" = "colima" ] && [ "${profile}" = "${COLIMA_PROFILE}" ] && continue
        log_info "stopping Colima profile ${profile}"
        colima stop -p "${profile}" >/dev/null 2>&1
    done
    if [ "${tool}" = "none" ]; then
        wait_vm_gone 180 || {
            log_error "a VM is still running"
            return 1
        }
    fi
    return 0
}

function ensure_running() {
    local tool="$1" config="$2"
    [ "${tool}" = "none" ] && return 0
    if [ "${tool}" = "colima" ]; then
        colima_prepare "${config}"
        [ $? -eq 2 ] && wait_vm_gone 60
    fi
    if dk "${tool}" info >/dev/null 2>&1 && config_matches "${tool}" "${config}"; then
        return 0
    fi
    log_info "${tool}: starting (${config})"
    tool_start "${tool}" "${config}" || {
        log_error "${tool} did not start; see ${TMP_DIR}/${tool}-start.log"
        return 1
    }
    if [ "${config}" = "matched-k8s" ]; then
        wait_k8s "${tool}" 900 || {
            log_error "${tool}: Kubernetes not Ready"
            return 1
        }
    fi
    return 0
}

# Rancher keeps its settings between starts; compare the live ones to what the config asks for.
function config_matches() {
    local tool="$1" config="$2" want_k8s="false" settings
    [ "${tool}" = "colima" ] && return 0
    settings="$("${RDCTL}" list-settings 2>/dev/null)" || return 1
    [ "${config}" = "matched-k8s" ] && want_k8s="true"
    [ "$(echo "${settings}" | jq -r .containerEngine.name)" = "moby" ] || return 1
    [ "${config}" = "defaults" ] && return 0
    [ "$(echo "${settings}" | jq -r .kubernetes.enabled)" = "${want_k8s}" ] || return 1
    [ "$(echo "${settings}" | jq -r .virtualMachine.numberCPUs)" = "${CPUS}" ] || return 1
    [ "$(echo "${settings}" | jq -r .virtualMachine.memoryInGB)" = "${MEMORY_GB}" ] || return 1
}

function wait_k8s() {
    local tool="$1" timeout_s="$2" deadline
    deadline=$(($(date +%s) + timeout_s))
    while [ "$(date +%s)" -lt "${deadline}" ]; do
        k8s_ready "${tool}" && return 0
        sleep 1
    done
    return 1
}

function record_env() {
    local tool="$1" config="$2"
    csv_row "${RESULTS_DIR}/env.csv" \
        "ts,tool,version,config,cpus,memory_gb,disk_gb,mac,cores,ram_gb,macos,docker_cli,load1,power_source" \
        "$(iso_now),${tool},$(tool_version "${tool}"),${config},${CPUS},${MEMORY_GB},${DISK_GB},$(sysctl -n machdep.cpu.brand_string | tr ',' ' '),$(sysctl -n hw.ncpu),$(($(sysctl -n hw.memsize) / 1073741824)),$(sw_vers -productVersion),$(docker version --format '{{.Client.Version}}' 2>/dev/null),$(load_avg),$(on_battery && echo battery || echo ac)"
}

function measure_footprint() {
    local tool="$1" item path kb
    local -a items
    case "${tool}" in
    colima)
        items=(
            "colima binary|$(brew --prefix colima 2>/dev/null)/"
            "lima binaries|$(brew --prefix lima 2>/dev/null)/"
            "docker cli|$(brew --prefix docker 2>/dev/null)/"
            "vm disks and config|${HOME}/.colima"
            "image cache|${HOME}/Library/Caches/colima"
            "lima cache|${HOME}/Library/Caches/lima"
        )
        ;;
    rancher)
        items=(
            "app bundle|${RD_APP}"
            "app support (vm disks)|${HOME}/Library/Application Support/rancher-desktop"
            "caches|${HOME}/Library/Caches/rancher-desktop"
            "logs|${HOME}/Library/Logs/rancher-desktop"
            "preferences|${HOME}/Library/Preferences/rancher-desktop"
            "rd bin links|${HOME}/.rd"
        )
        ;;
    *)
        log_error "footprint needs --tool colima or rancher"
        return 1
        ;;
    esac
    for item in "${items[@]}"; do
        path="${item#*|}"
        kb=0
        # du -sk counts allocated blocks, so sparse VM disks report what they really use.
        [ -e "${path}" ] && kb="$(du -sk "${path}" 2>/dev/null | awk '{print $1}')"
        csv_row "${RESULTS_DIR}/footprint.csv" "ts,tool,item,kb" "$(iso_now),${tool},${item%%|*},${kb}"
        log_info "footprint ${tool} ${item%%|*}: $((kb / 1024)) MiB"
    done
}

function measure_coldstart() {
    local tool="$1" config="$2" runs="$3" run t0 t1 t2 t3 t4 k8s_ms
    [ "${tool}" = "none" ] && return 0
    make_exclusive "${tool}" || return 1
    ensure_running "${tool}" "${config}" || return 1
    log_info "warm-up: pulling hello-world (not timed)"
    dk "${tool}" pull -q hello-world >/dev/null || return 1
    for run in $(seq 1 "${runs}"); do
        tool_stop "${tool}" || {
            log_error "stop failed"
            return 1
        }
        sleep 10
        log_info "coldstart ${tool} ${config} run ${run}/${runs}"
        t0="$(now_ms)"
        tool_start "${tool}" "${config}" || return 1
        t1="$(now_ms)"
        dk "${tool}" run --rm hello-world >/dev/null || return 1
        t2="$(now_ms)"
        k8s_ms=""
        if [ "${config}" = "matched-k8s" ]; then
            wait_k8s "${tool}" 900 || return 1
            k8s_ms=$(($(now_ms) - t0))
        fi
        t3="$(now_ms)"
        tool_stop "${tool}" || return 1
        t4="$(now_ms)"
        csv_row "${RESULTS_DIR}/coldstart.csv" \
            "ts,tool,config,run,engine_ready_ms,hello_world_ms,total_ms,k8s_ready_ms,stop_ms,load1" \
            "$(iso_now),${tool},${config},${run},$((t1 - t0)),$((t2 - t1)),$((t2 - t0)),${k8s_ms},$((t4 - t3)),$(load_avg)"
        log_info "  ready $((t1 - t0)) ms, hello-world $((t2 - t1)) ms, stop $((t4 - t3)) ms${k8s_ms:+, k8s ${k8s_ms} ms}"
    done
}

function measure_idle() {
    local tool="$1" config="$2" minutes="$3" settle="$4"
    local samples tag raw_top raw_pm pids cpu0 cpu1 i fp top_pid pm_pid wall0 wall1
    make_exclusive "${tool}" || return 1
    ensure_running "${tool}" "${config}" || return 1
    log_info "idle ${tool} ${config}: settling ${settle} min"
    sleep $((settle * 60))

    samples=$((minutes * 60 / SAMPLE_S))
    tag="${tool}-${config}-$(date +%Y%m%d-%H%M%S)"
    raw_top="${RESULTS_DIR}/raw/top-${tag}.txt"
    raw_pm="${RESULTS_DIR}/raw/powermetrics-${tag}.plist"
    mkdir -p "${RESULTS_DIR}/raw" || return 1
    # shellcheck disable=SC2207
    pids=($(tool_pids "${tool}"))
    echo "${pids[*]}" >"${RESULTS_DIR}/raw/pids-${tag}.txt"
    ps -o pid=,rss=,command= -p "$(
        IFS=,
        echo "${pids[*]:-1}"
    )" >>"${RESULTS_DIR}/raw/pids-${tag}.txt" 2>/dev/null
    log_info "idle ${tool} ${config}: sampling ${#pids[@]} processes for ${minutes} min"

    top -l $((samples + 1)) -s "${SAMPLE_S}" -stats pid,cpu,power -n 5000 >"${raw_top}" 2>&1 &
    top_pid=$!
    pm_pid=""
    if powermetrics_allowed; then
        sudo -n /usr/bin/powermetrics -i $((SAMPLE_S * 1000)) -n "${samples}" \
            --samplers cpu_power,gpu_power,ane_power,tasks --show-process-energy \
            -f plist -o "${raw_pm}" >/dev/null 2>&1 &
        pm_pid=$!
    else
        log_warn "powermetrics not allowed without a password; energy impact from top only"
    fi

    cpu0="$(cpu_seconds "${pids[@]}")"
    wall0="$(now_ms)"
    for i in $(seq 1 "${samples}"); do
        sleep "${SAMPLE_S}"
        fp="$(footprint_bytes "${pids[@]}")"
        csv_row "${RESULTS_DIR}/raw/footprint-${tag}.csv" "sample,footprint_bytes" "${i},${fp}"
    done
    cpu1="$(cpu_seconds "${pids[@]}")"
    wall1="$(now_ms)"
    wait "${top_pid}"
    [ -n "${pm_pid}" ] && wait "${pm_pid}"

    python3 "${BENCH_DIR}/parse_top.py" "${raw_top}" "${pids[@]}" >"${RESULTS_DIR}/raw/topsum-${tag}.csv" || return 1
    if [ -s "${raw_pm}" ]; then
        python3 "${BENCH_DIR}/parse_power.py" "${raw_pm}" "${pids[@]}" >"${RESULTS_DIR}/raw/power-${tag}.csv" || return 1
    fi
    csv_row "${RESULTS_DIR}/idle.csv" \
        "ts,tool,config,tag,minutes,processes,cpu_seconds,wall_seconds,load1" \
        "$(iso_now),${tool},${config},${tag},${minutes},${#pids[@]},$(echo "${cpu1} - ${cpu0}" | bc),$(((wall1 - wall0) / 1000)),$(load_avg)"
    log_ok "idle samples in results/raw/*-${tag}.*"
}

function measure_build() {
    local tool="$1" config="$2" runs="$3" run t0 t1 t2 ctx
    [ "${tool}" = "none" ] && return 0
    ctx="${BENCH_DIR}/workload/build"
    make_exclusive "${tool}" || return 1
    ensure_running "${tool}" "${config}" || return 1
    log_info "build: pulling base images (not timed)"
    dk "${tool}" pull -q golang:1.25-alpine >/dev/null || return 1
    dk "${tool}" pull -q alpine:3.22 >/dev/null || return 1
    for run in $(seq 1 "${runs}"); do
        dk "${tool}" image rm -f bench-build:latest >/dev/null 2>&1
        dk "${tool}" builder prune -af >/dev/null 2>&1
        t0="$(now_ms)"
        dk "${tool}" build --no-cache -q -t bench-build:latest "${ctx}" >/dev/null 2>>"${TMP_DIR}/build.log" || return 1
        t1="$(now_ms)"
        dk "${tool}" build -q -t bench-build:latest "${ctx}" >/dev/null 2>>"${TMP_DIR}/build.log" || return 1
        t2="$(now_ms)"
        csv_row "${RESULTS_DIR}/build.csv" "ts,tool,config,run,cold_ms,warm_ms,load1" \
            "$(iso_now),${tool},${config},${run},$((t1 - t0)),$((t2 - t1)),$(load_avg)"
        log_info "build run ${run}/${runs}: cold $((t1 - t0)) ms, warm $((t2 - t1)) ms"
    done
}

function measure_io() {
    local tool="$1" config="$2" runs="$3" run mount name phase target t0
    local -a phases=(create read tar delete seqwrite seqread)
    [ "${tool}" = "none" ] && return 0
    make_exclusive "${tool}" || return 1
    ensure_running "${tool}" "${config}" || return 1
    dk "${tool}" pull -q alpine:3.22 >/dev/null || return 1
    mount="${TMP_DIR}/io-mount"
    name="bench-io"
    for run in $(seq 1 "${runs}"); do
        rm -rf "${mount}" && mkdir -p "${mount}" || return 1
        dk "${tool}" rm -f "${name}" >/dev/null 2>&1
        dk "${tool}" run -d --name "${name}" -v "${mount}:/mnt/host" -v "${BENCH_DIR}/workload/io.sh:/io.sh:ro" \
            alpine:3.22 sleep 3600 >/dev/null || return 1
        for target in /mnt/host /root/work; do
            for phase in "${phases[@]}"; do
                t0="$(now_ms)"
                dk "${tool}" exec "${name}" sh /io.sh "${phase}" "${target}" >/dev/null 2>>"${TMP_DIR}/io.log" || {
                    log_error "io ${phase} on ${target} failed; see ${TMP_DIR}/io.log"
                    dk "${tool}" rm -f "${name}" >/dev/null 2>&1
                    return 1
                }
                csv_row "${RESULTS_DIR}/io.csv" "ts,tool,config,run,target,phase,ms" \
                    "$(iso_now),${tool},${config},${run},$([ "${target}" = /mnt/host ] && echo bind-mount || echo container-fs),${phase},$(($(now_ms) - t0))"
            done
        done
        dk "${tool}" rm -f "${name}" >/dev/null 2>&1
        log_info "io run ${run}/${runs} done"
    done
    rm -rf "${mount}"
}

function measure_battery() {
    local tool="$1" config="$2" minutes="$3" settle="$4" end batt
    if ! on_battery; then
        log_error "the Mac is on AC power; unplug it first"
        return 1
    fi
    make_exclusive "${tool}" || return 1
    ensure_running "${tool}" "${config}" || return 1
    log_info "battery ${tool} ${config}: settling ${settle} min, then ${minutes} min; keep the lid open"
    # Display and system kept awake so every run has the same screen-on load.
    caffeinate -d -i -t $(((settle + minutes) * 60 + 60)) &
    sleep $((settle * 60))
    end=$(($(date +%s) + minutes * 60))
    while [ "$(date +%s)" -le "${end}" ]; do
        if ! on_battery; then
            log_error "power was connected; run invalid"
            return 1
        fi
        batt="$(python3 "${BENCH_DIR}/battery_sample.py")" || return 1
        csv_row "${RESULTS_DIR}/battery.csv" "ts,tool,config,percent,mah,mv,ma,draw_mw,load1" \
            "$(iso_now),${tool},${config},${batt},$(load_avg)"
        sleep 30
    done
}

function exit_on_missing_tools() {
    for cmd in "$@"; do
        if command -v "$cmd" &>/dev/null; then
            continue
        fi
        log_error "required tool '$cmd' is not installed or not in PATH"
        exit 1
    done
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
    exit 0
fi
