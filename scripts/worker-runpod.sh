#!/usr/bin/env bash
# RunPod foreground launcher; persists Python, model and work beside the binary.
set -Eeuo pipefail
umask 077
export SUBTITLE_DIR="${SUBTITLE_DIR:-${WORKER_ROOT:-/workspace/avxtube-workers}/subtitle}"
# 8887 is used by spritesheet in the shared platform launcher.
export SUBTITLE_DASHBOARD_PORT="${SUBTITLE_DASHBOARD_PORT:-8888}"
if [[ "${1:-}" == --help || "${1:-}" == -h ]]; then
    echo 'Set DATABASE_URL and STORAGE_ENCRYPTION_KEY in RunPod, then run this script.'
    echo 'Optional: SUBTITLE_VERSION=vX.Y.Z, SUBTITLE_DIR, SUBTITLE_DASHBOARD_PORT (8888).'
    echo 'Additional arguments are passed to install.sh (e.g. --skip-deps, --no-start).'
    exit 0
fi
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
