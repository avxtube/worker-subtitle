# Worker Subtitle

Run `.build/windows.exe` (Windows) or `.build/linux` to start the worker. Open `http://127.0.0.1:8887` manually to view the local dashboard; the browser does not open automatically. Pass `-no-browser=false` to open it automatically if desired.
The worker installs MOSS dependencies when missing, downloads the model, loads it
on CUDA, publishes heartbeat, and claims production subtitle jobs.

## Production flow

1. Download separated audio and transcribe in chunks (180 seconds by default).
   Send one chunk at a time and save each completed result immediately under
   `work/<file.id>/checkpoints/<fingerprint>/`. Retry reuses matching completed
   chunks (including successful split retries); timestamps already include the
   chunk offset. Audio bytes, prompt, token limit, chunk size and model path must
   match. Only merge/export/upload once every chunk succeeds. Checkpoints and
   `segments.partial.json` survive cleanup; older jobs without checkpoints cannot
   automatically resume. Source audio is downloaded again to verify its contents.
2. Save the subtitle and detect its language from text (unknown is `und`).
3. If no speech is returned across the entire successful run: update
   `files.metadata.subtitleProcessingStatus = no_speech`, complete the queue,
   and do not create/upload an empty subtitle media.
4. Otherwise select an enabled, online, non-deleted S3 storage with both
   `purposes: storage, upload` and `kinds: subtitle`, ordered by priority then ID.
5. Upload and verify `subtitle/YYYY-MM-DD/<random-11>.vtt`. The database key has
   no leading slash; the storage S3 prefix is applied by the uploader.
6. In one MongoDB transaction: complete the owned queue job, insert media with
   `type: subtitle`, `enabled: true`, `fileId`, `storageId`, `key`, `mime: text/vtt`,
   and `metadata: {language, name, type: transcribe, source: ai}`; update the file's
   `metadata.subtitleProcessingStatus` to `completed`.

Neither file status update changes `files.updatedAt`. Replica set/sharded MongoDB
transactions are required. Lease ownership is checked during work and at commit.
Upload failures create no media record. An upload that succeeds before a database
failure may remain as an unreferenced object; its destination is recorded in the
queue's `subtitleUploadAttempts` for reconciliation. It is not deleted automatically.

This replaces the earlier forced-failure/local-only test queue policy. No existing
failed/completed queue job is automatically reset or backfilled. Admin must enable
the subtitle worker and `subtitle_config` before new queue claims.

## Local files

MOSS now uses constrained decoding in a single pass. `lm-format-enforcer` filters
token choices so each segment has numeric start/end timestamps and a speaker ID.
Immediate empty output is accepted only when EOS is the model's top token BEFORE
filtering; rejecting an untimed speaker token cannot accidentally select silence.
Complete empty timestamp/speaker segments remain valid for non-speech. Malformed
nonempty results fail instead of becoming `no_speech`. Times are predicted by MOSS
and validated against chunk bounds; the grammar does not guarantee alignment accuracy. Results record
`backend: moss-constrained`. The previous Faster-Whisper fallback is no longer
called by the service. Existing completed checkpoints remain reusable.

All managed paths are beside the executable:

- `python/`: managed Windows Python 3.13 (requires Python Install Manager).
- `.venv/`: Python dependencies; reused on later starts/builds.
- `moss/`: bundled source; `models/MOSS-Transcribe-Diarize/`: model weights.
- `.cache/huggingface/`: isolated model code cache.
- `work/<file.id>/`: successful transcription is compacted to `job.json`,
  `segments.json`, `subtitle.vtt` before upload. After the production queue is
  settled as completed or failed (including exhausted inference retries), the
  downloaded audio and intermediate files are deleted; `job.json`, `segments.json`
  and `subtitle.vtt` are retained where available, including older attempts, for comparison.
  Shutdown, lost ownership,
  unsettled database writes, or an active attempt retain local files.
- `log/<file.slug>.log`, with file ID fallback; setup writes `log/setup.log`.

`segments.json` keeps raw parsed segments. VTT merges overlapping identical cues
from the same speaker and continuous repeated sound-annotation cycles. Spoken
repetitions at distinct times are preserved. Default MOSS prompt requests only
intelligible speech and skips non-speech noise/descriptions without guessing.

`job.json.language` is an offline estimate from subtitle text with confidence and
reliability, not a guarantee of the original audio language. Short/unreliable text
uses `und`. Successfully published jobs also record the upload receipt locally.
Media metadata keeps the language code in `language` and its display name in
`name` (for example `zh` / `Mandarin`, or `und` / `Unknown`).

## Configuration and dashboard

Copy `.env.example` to `.env` and configure `DATABASE_URL` and storage encryption
key. `build.bat` copies `.env` into `.build`. Old `MOSS_PYTHON` and `MOSS_MODEL_DIR`
environment overrides are ignored; explicit CLI development flags still exist.
FFmpeg/FFprobe and NVIDIA driver must be installed. Windows Python Install Manager
can download Python automatically; Linux needs Python 3.10–3.13.

Dashboard defaults to `http://127.0.0.1:8887`. MOSS Requests displays the last 100
JSON-pipe exchanges, request/response previews, elapsed time and errors. It is not
HTTP traffic. Ping entries can be shown optionally; history resets on restart.

Standalone `--audio-file`, `--audio-url`, or `--media-id` CLI input remains local
conversion only and does not publish media. Queue publishing is the daemon flow.

## Build and verification

`build.bat` builds Windows and copies assets; `build.bat linux` builds Linux.
`go test ./...` and `go vet ./...` verify the Go code. Set `MOSS_TEST_PYTHON` to a
Python executable for the fake-model/real-FFmpeg integration test. Production
read-only diagnostics require explicit `SUBTITLE_READCHECK=1`.


## Linux service installation

Requires Linux x86_64, an NVIDIA GPU/driver and Python 3.10–3.13. The installer
installs OS packages on Debian/Ubuntu; it does not install or replace GPU drivers.
Set `DATABASE_URL` and `STORAGE_ENCRYPTION_KEY` in the environment, then:

```bash
curl -fsSL https://raw.githubusercontent.com/avxtube/worker-subtitle/main/install.sh -o /tmp/install-subtitle.sh
sudo -E bash /tmp/install-subtitle.sh --version v0.1.0
```

Use `--version latest` to upgrade to the latest release. The installer verifies
SHA256SUMS before replacing the binary. Existing `.env`, `.venv`, models, cache,
work and logs remain in `/opt/worker-subtitle`. It starts one GPU worker service:

```bash
systemctl status worker-subtitle
journalctl -u worker-subtitle -f
systemctl stop worker-subtitle
```

The binary installs CUDA PyTorch and MOSS dependencies at first startup. Subsequent
starts reuse installed packages/model files. Setup output is in `log/setup.log`.
`--no-start` installs without starting; `--skip-deps` uses existing OS packages.
The installer preserves an existing `.env`; edit it and restart the service to
change saved credentials. Environment values override `.env` values.

## RunPod (no systemd)

Use an Ubuntu GPU Pod with Python 3.10–3.13 and a persistent `/workspace` volume.
Set `DATABASE_URL` and `STORAGE_ENCRYPTION_KEY` in the Pod environment. Then run:

```bash
curl -fsSL https://raw.githubusercontent.com/avxtube/worker-subtitle/main/scripts/worker-runpod.sh -o /tmp/subtitle-runpod.sh
bash /tmp/subtitle-runpod.sh
```

The launcher installs and runs subtitle in the foreground. Configure the Pod to run
it on startup. `SUBTITLE_VERSION` pins a release; default `latest` downloads the
current release on each launch. `SUBTITLE_DIR` defaults to
`/workspace/avxtube-workers/subtitle`; the binary, Python venv, models, cache, work
and logs share that directory. Do not run an installer over an active foreground
worker; stop the worker before upgrading. One instance per directory/GPU is intended.

Dashboard port is **8888** on RunPod to avoid spritesheet's 8887. It remains bound
to loopback; access it using an SSH tunnel, e.g. `ssh -L 8888:127.0.0.1:8888 ...`,
then open `http://127.0.0.1:8888`. It is not exposed through a public Pod HTTP port.
Set `SUBTITLE_DASHBOARD_PORT` to change it. Stopping the foreground process sends
shutdown to MOSS; persistent artifacts survive Pod restarts when the volume persists.
The platform's existing transcode/spritesheet launcher is unchanged; run this
subtitle launcher separately with sufficient GPU memory.

## Release

Push a `vX.Y.Z` tag to run Go tests/vet, CPU Python tests, installer checks and
Windows/Linux amd64 cross-builds. A release is published only after those checks
pass. Assets include standalone `linux` / `windows.exe` (embedded MOSS source),
platform archives, install scripts and `SHA256SUMS`. CUDA inference still requires
validation on the deployment GPU; GitHub's CPU runner does not perform it.

### RunPod background mode

Download the launcher from `main` for these options (the v0.1.0 launcher predates
background support). Run `bash worker-runpod.sh --background` to detach setup and
the worker from the terminal. Use `--status`, `--logs` and `--stop` to manage it.
The combined output is saved to `log/worker.log`. Status reports the process,
not GPU/model readiness. Starting twice will not launch a second managed process.
`--stop` sends SIGTERM to its process group, including Python. Background mode does
not restart after a crash or Pod restart; use the foreground launcher as the Pod
startup command for that. Thai instructions: `scripts/worker-runpod.txt`.
