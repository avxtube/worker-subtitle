#!/usr/bin/env bash
# Install a released subtitle worker. No Go compiler is required.
set -Eeuo pipefail
umask 077
REPO=avxtube/worker-subtitle
VERSION="${SUBTITLE_VERSION:-latest}"
APP_DIR="${SUBTITLE_DIR:-/opt/worker-subtitle}"
MODE=systemd
PORT="${SUBTITLE_DASHBOARD_PORT:-8887}"
START=true
DEPS=true
fail() { printf '[subtitle-install] ERROR: %s\n' "$*" >&2; exit 1; }
while (($#)); do
    case "$1" in
        --version|--dir|--mode|--port)
            (($# >= 2)) || fail "$1 requires a value"
            case "$1" in
                --version) VERSION="$2";; --dir) APP_DIR="$2";;
                --mode) MODE="$2";; --port) PORT="$2";;
            esac
            shift 2;;
        --no-start) START=false; shift;;
        --skip-deps) DEPS=false; shift;;
        -h|--help)
            cat <<'HELP'
Install worker-subtitle on Linux amd64 with an NVIDIA GPU.
Required for a new installation: DATABASE_URL and STORAGE_ENCRYPTION_KEY environment variables.
Options:
  --version TAG     Release tag, or latest (default)
  --dir PATH        Absolute installation directory (default /opt/worker-subtitle)
  --mode MODE       systemd (default) or runpod (foreground, no systemd)
  --port PORT       Loopback dashboard port (default 8887)
  --no-start        Install only; leave the worker stopped
  --skip-deps       Do not install OS packages; verify existing tools
Preserves .env, .venv, models, cache, work and log on upgrades.
HELP
            exit 0;;
        *) fail "unknown option: $1";;
    esac
done
[[ "$MODE" == systemd || "$MODE" == runpod ]] || fail 'invalid mode'
[[ "$VERSION" == latest || "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$ ]] || fail 'invalid release version'
[[ "$APP_DIR" =~ ^/[A-Za-z0-9._/-]+$ && "$APP_DIR" != / && "$APP_DIR" != *'/../'* && "$APP_DIR" != */.. ]] || fail 'use an absolute installation path without spaces or ..'
[[ "$PORT" =~ ^[0-9]{1,5}$ ]] || fail 'invalid port'
((10#$PORT > 0 && 10#$PORT <= 65535)) || fail 'invalid port'
[[ "$(uname -s)" == Linux && "$(uname -m)" == x86_64 ]] || fail 'Linux x86_64 is required'
if [[ "$MODE" == systemd ]]; then
    [[ "$(id -u)" == 0 && -d /run/systemd/system ]] || fail 'systemd mode requires root and running systemd; use --mode runpod inside containers'
fi
if [[ ! -f "$APP_DIR/.env" ]]; then
    [[ -n "${DATABASE_URL:-}" && -n "${STORAGE_ENCRYPTION_KEY:-${BETTER_AUTH_SECRET:-}}" ]] || fail 'DATABASE_URL and STORAGE_ENCRYPTION_KEY are required'
fi
if [[ "$DEPS" == true ]]; then
    [[ "$(id -u)" == 0 ]] || fail 'OS dependencies require root; use --skip-deps if already installed'
    command -v apt-get >/dev/null || fail 'automatic dependencies require Debian/Ubuntu; otherwise install dependencies and use --skip-deps'
    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ca-certificates curl ffmpeg python3 python3-venv python3-pip libsndfile1
fi
for tool in curl ffmpeg ffprobe python3 sha256sum tar; do
    command -v "$tool" >/dev/null || fail "missing dependency: $tool"
done
python3 -c 'import sys; assert (3,10) <= sys.version_info[:2] < (3,14), "Python 3.10-3.13 required"'
if ! command -v nvidia-smi >/dev/null || ! nvidia-smi >/dev/null; then
    fail 'NVIDIA GPU/driver unavailable; select a GPU Pod or install the host driver'
fi
mkdir -p "$APP_DIR"
staging="$(mktemp -d "$APP_DIR/.release.XXXXXX")"
trap 'rm -rf -- "$staging"' EXIT
curl_args=(--fail --location --silent --show-error --retry 3 --connect-timeout 20)
release_path=latest
[[ "$VERSION" == latest ]] || release_path="tags/$VERSION"
# Resolve once so the binary and checksums always come from the same release.
api_args=("${curl_args[@]}" --header 'Accept: application/vnd.github+json')
if [[ -n "${GITHUB_TOKEN:-}" ]]; then api_args+=(--header "Authorization: Bearer $GITHUB_TOKEN"); fi
curl "${api_args[@]}" "https://api.github.com/repos/$REPO/releases/$release_path" -o "$staging/release.json"
python3 - "$staging/release.json" "$staging" <<'PY'
import json, pathlib, sys
release = json.loads(pathlib.Path(sys.argv[1]).read_text())
assets = {a['name']: a for a in release['assets']}
for name in ('linux', 'SHA256SUMS'):
    if name not in assets:
        raise SystemExit(f'Missing release asset: {name}')
    pathlib.Path(sys.argv[2], name + '.url').write_text(assets[name]['url'])
pathlib.Path(sys.argv[2], 'version').write_text(release['tag_name'])
PY
asset_args=("${curl_args[@]}" --header 'Accept: application/octet-stream')
if [[ -n "${GITHUB_TOKEN:-}" ]]; then asset_args+=(--header "Authorization: Bearer $GITHUB_TOKEN"); fi
for asset in linux SHA256SUMS; do
    curl "${asset_args[@]}" "$(cat "$staging/$asset.url")" -o "$staging/$asset"
done
(cd "$staging"; awk '$2 == "linux" {print}' SHA256SUMS > binary.sha256; [[ -s binary.sha256 ]]; sha256sum -c binary.sha256)
# Verify the downloaded executable before changing an existing installation.
chmod 0755 "$staging/linux"
"$staging/linux" -h >/dev/null 2>&1
if [[ "$MODE" == systemd ]]; then systemctl stop worker-subtitle.service 2>/dev/null || true; fi
mv -f "$staging/linux" "$APP_DIR/linux"
mv -f "$staging/version" "$APP_DIR/VERSION"
if [[ ! -f "$APP_DIR/.env" ]]; then
    export SUBTITLE_INSTALL_ENV="$APP_DIR/.env"
    python3 - <<'PY'
import os, pathlib
values = {'DATABASE_URL': os.environ['DATABASE_URL'],
          'STORAGE_ENCRYPTION_KEY': os.environ.get('STORAGE_ENCRYPTION_KEY') or os.environ.get('BETTER_AUTH_SECRET', ''),
          'SUBTITLE_QUEUE_ENABLED': os.environ.get('SUBTITLE_QUEUE_ENABLED', 'true')}
def quote(value):
    if any(c in value for c in "\r\n'"):
        raise SystemExit('Configuration contains unsupported quote or newline')
    return "'" + value + "'"
pathlib.Path(os.environ['SUBTITLE_INSTALL_ENV']).write_text(''.join(k+'='+quote(v)+'\n' for k,v in values.items()))
PY
    chmod 0600 "$APP_DIR/.env"
fi
mkdir -p "$APP_DIR/work" "$APP_DIR/log" "$APP_DIR/models"
if [[ "$MODE" == systemd ]]; then
    cat > /etc/systemd/system/worker-subtitle.service <<UNIT
[Unit]
Description=AVXTube Subtitle Worker
Wants=network-online.target
After=network-online.target
[Service]
Type=simple
WorkingDirectory=$APP_DIR
ExecStart=$APP_DIR/linux -no-browser
Environment=DASHBOARD_ADDR=127.0.0.1:$PORT
Restart=on-failure
RestartSec=10
TimeoutStopSec=120
KillMode=control-group
UMask=0077
[Install]
WantedBy=multi-user.target
UNIT
    systemctl daemon-reload
    systemctl enable worker-subtitle.service
    if [[ "$START" == true ]]; then systemctl start worker-subtitle.service; fi
else
    if [[ "$START" == true ]]; then
        export DASHBOARD_ADDR="127.0.0.1:$PORT"
        export WORKER_ID="${WORKER_ID:-subtitle_${RUNPOD_POD_ID:-$(hostname)}@1}"
        rm -rf -- "$staging"
        trap - EXIT
        cd "$APP_DIR"
        exec "$APP_DIR/linux" -no-browser
    fi
fi
printf '[subtitle-install] Installed %s at %s. Dashboard: http://127.0.0.1:%s\n' "$(cat "$APP_DIR/VERSION")" "$APP_DIR" "$PORT"
