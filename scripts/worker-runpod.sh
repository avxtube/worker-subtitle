#!/usr/bin/env bash
# RunPod launcher; persists Python, model and work beside the binary.
set -Eeuo pipefail
umask 077
export SUBTITLE_DIR="${SUBTITLE_DIR:-${WORKER_ROOT:-/workspace/avxtube-workers}/subtitle}"
export SUBTITLE_DASHBOARD_PORT="${SUBTITLE_DASHBOARD_PORT:-8889}"
state="$SUBTITLE_DIR/worker.pid"
logfile="$SUBTITLE_DIR/log/worker.log"
start_time() {
    local stat
    local fields=()
    [[ -r "/proc/$1/stat" ]] || return 1
    stat=$(cat "/proc/$1/stat") || return 1
    read -r -a fields <<< "${stat##*) }"
    [[ "${fields[0]}" != Z ]] || return 1
    printf '%s' "${fields[19]}"
}
running() {
    local saved
    [[ -f "$state" ]] || return 1
    read -r pid saved < "$state" || return 1
    [[ "$pid" =~ ^[0-9]+$ && "$pid" -gt 1 ]] || return 1
    [[ "$(start_time "$pid")" == "$saved" ]] && kill -0 "$pid" 2>/dev/null
}
case "${1:-}" in
    --help|-h)
        echo 'RunPod subtitle: --background | --status | --stop | --logs'
        echo 'Without these options, install and run in the foreground.'
        echo 'Set DATABASE_URL and STORAGE_ENCRYPTION_KEY for a new install.'
        echo 'Optional: SUBTITLE_VERSION, SUBTITLE_DIR, SUBTITLE_DASHBOARD_PORT (8889).'
        echo 'Installer options follow --background (e.g. --skip-deps).'
        exit 0;;
    --status)
        if running; then echo "Worker/installer running (PID $pid); log: $logfile"; else echo 'Worker stopped'; fi
        exit 0;;
    --logs) exec tail -n 100 -F "$logfile";;
    --stop)
        if running; then
            # The PID and Linux start time identify our setsid process group.
            kill -TERM -- "-$pid"
            for ((i=0; i<120; i++)); do
                if ! running; then echo 'Worker stopped'; exit 0; fi
                sleep 1
            done
            echo 'Shutdown still in progress; inspect --logs' >&2
            exit 1
        fi
        echo 'Worker already stopped'
        exit 0;;
    --background)
        shift
        if running; then echo "Already running (PID $pid)"; exit 0; fi
        for tool in nohup setsid flock; do command -v "$tool" >/dev/null || { echo "Missing $tool (install util-linux/coreutils)" >&2; exit 1; }; done
        mkdir -p "$SUBTITLE_DIR/log"
        self="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/$(basename -- "${BASH_SOURCE[0]}")"
        nohup setsid bash "$self" --managed "$@" </dev/null >>"$logfile" 2>&1 &
        for ((i=0; i<50; i++)); do
            if running; then echo "Started in background (PID $pid); log: $logfile"; exit 0; fi
            sleep 0.1
        done
        echo "Startup failed; inspect $logfile" >&2
        exit 1;;
    --managed)
        shift
        mkdir -p "$SUBTITLE_DIR"
        exec 9>"$SUBTITLE_DIR/worker.lock"
        flock -n 9 || { echo 'Another managed worker is running'; exit 1; }
        printf '%s %s\n' "$$" "$(start_time "$$")" > "$state.next"
        mv -f "$state.next" "$state"
        ;;
esac
local_installer="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)/install.sh"
if [[ -f "$local_installer" ]]; then
    exec bash "$local_installer" --mode runpod --dir "$SUBTITLE_DIR" "$@"
fi
mkdir -p "$SUBTITLE_DIR"
curl --fail --location --silent --show-error --retry 3 \
    https://raw.githubusercontent.com/avxtube/worker-subtitle/main/install.sh \
    -o "$SUBTITLE_DIR/install.sh.next"
mv -f "$SUBTITLE_DIR/install.sh.next" "$SUBTITLE_DIR/install.sh"
exec bash "$SUBTITLE_DIR/install.sh" --mode runpod --dir "$SUBTITLE_DIR" "$@"
