# Voice Assistant Project

## Project Overview

This is a **low-latency voice assistant** system for Ubuntu that provides:
- **Speech-to-Text (STT)**: whisper.cpp (CPU, English models)
- **LLM Processing**: Calls local llama.cpp API endpoint
- **Text-to-Speech (TTS)**: piper (C++ CLI, built from source)

**Processing latency target (after capture, which ends ~2s after speech
stops): 3-4 seconds.**

## Architecture

```
Host (Ubuntu)
├── Microphone (ALSA)
├── Speaker (ALSA)
└── llama.cpp API (localhost:8080, EXTERNAL)
    └── Docker Container (COMBINED - single container)
        ├── voice-assistant (Go orchestrator)
        ├── whisper-cli (STT, CPU, CLI mode, statically linked)
        └── piper (TTS, C++ CLI built from piper1-gpl source)
```

### Container Design Decision

All services run in **a single combined container** for minimal overhead:
- **No network latency** between whisper→piper (file-based transfer)
- **Simpler deployment** - one container to manage
- **Lower resource footprint** - single base image
- **Sequential processing** - whisper runs, then piper runs

### Audio Pipeline

```
Host Mic (ALSA default device)
    ↓
sox -d → 16kHz mono 16-bit WAV (voice-activated capture via the sox
`silence` effect: waits for speech, records until ~2s of quiet, 30s cap;
CAPTURE_MODE=fixed restores the old fixed window)
    ↓
whisper-cli (STT with built-in Silero VAD; non-speech segments are
dropped before whisper_full) → transcript file (<base>.txt via -of/-otxt)
    ↓
HTTP POST to llama.cpp host.docker.internal:8080/v1/chat/completions
    ↓
Response text
    ↓
piper CLI (text via stdin) → WAV at the voice's native sample rate
    ↓
aplay → Host Speaker
```

## Build Instructions

```bash
# Build the Docker image
docker compose build

# Download models (first time only; --user 0 because the script writes to
# the ./models volume and the container otherwise runs non-root)
docker compose run --rm --user 0 voice-assistant /app/install_models.sh

# Start the service
docker compose up -d
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `WHISPER_MODEL` | `/models/whisper/ggml-small.en-q5_1.bin` | Path to whisper model |
| `WHISPER_THREADS` | _(unset)_ | CPU threads for whisper-cli (`-t`); unset = whisper-cli default (4); overridable via host env var or `.env` file |
| `PIPER_MODEL` | `/models/piper/en_US-ryan-high.onnx` | Path to piper model |
| `LLM_ENDPOINT` | `http://host.docker.internal:8080` | LLM API endpoint (bridge network, host-gateway alias); overridable via host env var or `.env` file |
| `LLM_MODEL` | _(unset)_ | Model name sent in every request; required for llama.cpp router mode (`--models-dir`/`--model-presets`), ignored by single-model servers |
| `LLM_SYSTEM_PROMPT` | `You are a voice assistant. Reply in one or two short sentences.` | System prompt for chat completions |
| `LLM_TIMEOUT` | `30s` | LLM request timeout (Go duration) |
| `LLM_DISABLE_REASONING` | _(on)_ | Sends `chat_template_kwargs {"enable_thinking": false}` to disable thinking; honored by Qwen3-style chat templates. Set `false` to allow reasoning (then raise `LLM_MAX_TOKENS`) |
| `LLM_MAX_TOKENS` | `512` | Reply token budget; a thinking model needs ~400+ (reasoning + answer), lower (e.g. 100) to cap latency once reasoning is off |
| `CAPTURE_MODE` | `vad` | `vad` = silence-triggered capture via the sox `silence` effect; `fixed` = fixed window via `CAPTURE_SECONDS` |
| `CAPTURE_SECONDS` | `5` | Fixed audio capture window in seconds (used by `CAPTURE_MODE=fixed`) |
| `VAD_THRESHOLD` | `10` | sox amplitude threshold (%) for speech start/stop; tune per mic/room (overridable via host env var or `.env`) |
| `VAD_START_MS` | `100` | Sound duration (ms) above the threshold that starts the recording |
| `VAD_SILENCE_SEC` | `2.0` | Quiet duration (s) below the threshold that ends the utterance |
| `VAD_MAX_UTTERANCE_SEC` | `30` | Hard cap on one utterance (sox `trim`), so constant noise cannot record forever |
| `VAD_MIN_SPEECH_MS` | `500` | Captures shorter than this are skipped before STT (transient noise, not speech) |
| `SESSION_TIMEOUT_SEC` | `60` | Seconds of silence before the conversation history auto-resets; checked when the next capture ends (sox blocks in vad mode, so a between-iteration check would never fire). `0` disables the timeout |
| `SESSION_MAX_MESSAGES` | `10` | Max messages kept in session history; must be even (oldest user/assistant pairs are trimmed) |
| `SESSION_RESET_PHRASE` | `new voice assistant session` | Utterance that resets the session; matched case/punctuation-insensitively with room for ~2 padding words |
| `WHISPER_VAD_MODEL` | `/models/whisper/ggml-silero-v5.1.2.bin` | whisper.cpp Silero VAD model (ggml); a missing file disables `--vad` |
| `WHISPER_BIN` | `/app/whisper-cli` | whisper-cli binary path |
| `PIPER_BIN` | `/app/piper` | piper binary path |
| `ESPEAK_DATA` | `/opt/espeak-ng-data` | espeak-ng data dir for piper |
| `DEBUG` | `false` | Enable debug logging |
| `AUDIO_GID` | `29` | Host audio group GID, added via compose `group_add` so the non-root container user can open `/dev/snd` |

## Run Commands

```bash
# Start in foreground with logs
docker compose up

# Start in background
docker compose up -d

# Stop
docker compose down

# View logs
docker compose logs -f

# Rebuild (uses layer cache; add --no-cache only for a full reset)
docker compose build

# Run a command inside the container (entrypoint passes it through)
docker compose run --rm voice-assistant aplay -l
```

## Performance Targets

| Component | Target Latency | Notes |
|-----------|----------------|-------|
| **Total (end-to-end)** | utterance + 2s tail + 3-4s | Capture ends ~2s after speech stops (VAD_SILENCE_SEC) |
| Whisper STT | <1500ms | small.en-q5_1 model, Release build (4 threads by default; raise `WHISPER_THREADS` to cut this) |
| LLM inference | <1500ms | via llama.cpp |
| Piper TTS | <1000ms | ryan-high model, CLI mode, model loaded per call |
| Audio capture | utterance + 2s tail | Voice-activated (sox `silence` effect, 30s cap); the wait for speech is free - sox blocks on the mic, no whisper inference is burned on silence |

The orchestrator measures latency from the start of capture and reports
capture time and processing time separately.

## Models

| Model | Size | Description |
|-------|------|-------------|
| whisper small.en-q5_1 | 190MB | Quantized English STT model; far better accuracy than tiny.en |
| whisper silero VAD | 0.9MB | Silero VAD in ggml format; whisper.cpp drops non-speech segments before whisper_full |
| piper en_US-ryan-high | 120MB | Highest-quality male English TTS voice (+ .onnx.json config) |

Models are downloaded from HuggingFace by `install_models.sh` into the
`/models` volume (`./models` on the host).

## Prerequisites

- Docker and Docker Compose (v2 plugin or v1 binary)
- Ubuntu 22.04/24.04 host
- Host with microphone and speaker
- llama.cpp server running on port 8080, listening on the Docker bridge
  gateway (e.g. `--host 0.0.0.0`), not only loopback

## Directory Structure

```
voice-assistant/
├── AGENTS.md                  # This file - project context
├── docker-compose.yml         # Docker Compose configuration
├── Dockerfile                 # Combined container build
├── orchestrator.go            # Go orchestrator code
├── go.mod                     # Go module file
├── entrypoint.sh              # Container entrypoint script
├── install_models.sh          # Model download script
├── start.sh                   # Quick start script
├── README.md                  # User documentation
├── LICENSES/                  # Third-party license texts (whisper MIT, piper/espeak-ng GPL-3.0, onnxruntime MIT, GPL-2.0, LGPL-2.1)
├── THIRD_PARTY_NOTICES.md     # Third-party components, licenses, and pinned source URLs
├── .dockerignore              # Docker ignore file
├── .env.example               # Template for .env overrides (LLM_*, WHISPER_THREADS, VAD_* capture tuning, AUDIO_GID)
└── models/                    # Mount point for models (host ./models)
    ├── whisper/
    └── piper/
```

## Key Implementation Details

### Orchestrator (Go)
- Captures audio via `sox -d` directly as 16kHz mono WAV (single process).
  In `vad` mode (default) the sox `silence` effect endpointing waits for
  speech (unbounded, free - sox blocks on the mic), records until
  `VAD_SILENCE_SEC` of quiet, and caps the utterance at
  `VAD_MAX_UTTERANCE_SEC` via sox `trim`; `CAPTURE_MODE=fixed` restores the
  old fixed window (`CAPTURE_SECONDS`). sox durations are formatted with a
  decimal point - bare integers are parsed as sample counts
- Skips STT entirely when a vad-mode capture is shorter than
  `VAD_MIN_SPEECH_MS` (transient noise started the recording, no speech
  followed)
- Calls whisper-cli, reads the transcript from the `-of <base> -otxt` file
  (never parses the binary's log output)
- Passes `-t $WHISPER_THREADS` to whisper-cli when `WHISPER_THREADS` is set
  (whisper-cli otherwise defaults to 4 threads)
- Passes `--vad --vad-model $WHISPER_VAD_MODEL` to whisper-cli when the
  Silero VAD model file exists, so non-speech segments are dropped before
  whisper_full (missing file = flag omitted, graceful degradation)
- Skips the LLM when whisper reports silence (`[BLANK_AUDIO]` or empty text)
- Strips bracketed non-speech tags (`[typing]`, `[SOUND]`, ...) whisper
  hallucinates on noise before `callLLM`; skips the turn when nothing real
  remains
- POSTs to llama.cpp `/v1/chat/completions` (system prompt + session
  history + current utterance) with a configurable timeout and
  status-code check
- Keeps conversation history in a `Session`: only complete user/assistant
  pairs are recorded (a failed LLM call records nothing, so the history
  alternation never breaks), the oldest pairs are trimmed to
  `SESSION_MAX_MESSAGES`, and the session is cleared by the
  `SESSION_RESET_PHRASE` utterance (word-boundary match, ~2 padding words
  allowed) or after `SESSION_TIMEOUT_SEC` without speech - checked after
  each capture ends, because sox blocks in vad mode and a between-iteration
  check would never fire for a returning user
- Sends `LLM_MODEL` in the request body: required when the llama.cpp
  server runs in router mode (`--models-dir`/`--model-presets`), which
  rejects requests without a model name; single-model servers ignore it
- Sends `chat_template_kwargs {"enable_thinking": false}` when
  `LLM_DISABLE_REASONING` is on (default): llama.cpp has no per-request
  "reasoning" switch (a `reasoning` field is silently ignored), and
  Qwen3-style templates honor this kwarg. A thinking model otherwise
  exhausts `LLM_MAX_TOKENS` on `reasoning_content` and returns an empty
  `content` (perceived as silence); the orchestrator logs `finish_reason`
  and reasoning length when that happens
- Sends TTS text to piper via **stdin** (piper has no `--input-text` flag)
- Plays audio via `aplay`
- Measures and reports total latency including the capture window

### Whisper Integration
- Built from source (ggml-org/whisper.cpp, pinned v1.9.4)
- CPU only, no SDL2, no FFmpeg (input is plain WAV), statically linked
  (`BUILD_SHARED_LIBS=OFF`) - single self-contained binary
- Built-in Silero VAD (`--vad`) is passed when the ggml VAD model exists;
  non-speech segments are dropped before whisper_full, which avoids
  hallucinated non-speech tags on noise and speeds up inference. The VAD
  model is downloaded by `install_models.sh` (see Models)
- small.en-q5_1 model (quantized) for far better accuracy at moderate
  inference speed, Release build

### Piper Integration
- Built from source (OHF-Voice/piper1-gpl, pinned v1.8.0): the C++ CLI
  (`libpiper/src/main/piper_exe`) with espeak-ng statically linked and
  prebuilt onnxruntime as a shared library
- CLI mode only (no HTTP server): reads text from stdin, writes a WAV file
  at the voice's native sample rate (no `--sample-rate` flag exists)
- Runtime needs `libpiper.so` + `libonnxruntime.so` (installed to
  `/usr/local/lib`, ldconfig) and espeak-ng data at `/opt/espeak-ng-data`

### Docker Configuration
- Single container approach
- Base images pinned by digest (`golang:1.23-bookworm@sha256:...`,
  `debian:bookworm-slim@sha256:...`) for reproducible builds
- git clone and cmake build are separate RUN layers with build trees
  removed after artifact copy — cached layer diffs stay small, and cmake
  flag changes don't re-download sources
- Bridge networking with the host-gateway alias
  (`extra_hosts: host.docker.internal:host-gateway`) - the llama.cpp server
  on the host is reached via `http://host.docker.internal:8080` and must
  listen on the Docker bridge gateway (e.g. `--host 0.0.0.0`), not only
  loopback
- The runtime container runs as a non-root user (uid 1000, matches the
  typical host user so the `./models` bind mount stays readable); the host
  audio group is added via `group_add: ${AUDIO_GID:-29}` for `/dev/snd`
  access, and model installs use `--user 0` (see Build Instructions)
- `LLM_ENDPOINT`, `LLM_MODEL`, `LLM_SYSTEM_PROMPT`, `LLM_DISABLE_REASONING`,
  `LLM_MAX_TOKENS`, `WHISPER_THREADS`, `WHISPER_VAD_MODEL`, the
  `CAPTURE_MODE`/`VAD_*` capture variables and the `SESSION_*` session
  variables are passed through in
  docker-compose.yml (e.g. `${LLM_ENDPOINT:-http://host.docker.internal:8080}`),
  so they can be overridden via host env vars or a `.env` file (see
  `.env.example`)
- /dev/snd passthrough for ALSA audio
- Volume mounts for models
- LICENSE, THIRD_PARTY_NOTICES.md, and LICENSES/ are copied into the image at
  /usr/share/licenses/voice-assistant/ (GPLv3 notice compliance when the
  image is distributed)
- Entrypoint passes through any command given via
  `docker compose run --rm voice-assistant <cmd>`

## Troubleshooting

### Audio Issues
```bash
# Check ALSA devices
docker compose run --rm voice-assistant aplay -l

# Test microphone (5s fixed recording)
docker compose run --rm voice-assistant bash -c 'sox -d -r 16000 -c 1 -b 16 /tmp/test.wav trim 0 5'

# Test voice-activated capture (waits for speech, stops ~2s after quiet;
# prints the recorded duration)
docker compose run --rm voice-assistant bash -c 'sox -d -r 16000 -c 1 -b 16 -t wav /tmp/vad.wav silence 1 0.1 3% 1 2.0 3% trim 0 10 && soxi -D /tmp/vad.wav'
```

### Model Issues
```bash
# Re-download models
docker compose run --rm --user 0 voice-assistant /app/install_models.sh
```

### LLM Connection
```bash
# Ensure llama.cpp is running and reachable (from inside the container,
# via the host-gateway alias)
docker compose run --rm voice-assistant curl -s http://host.docker.internal:8080/health

# Start llama.cpp on the host (-p 8080:8080 publishes on all interfaces,
# which the bridge-network container can reach via host.docker.internal)
docker run -p 8080:8080 ggerganov/llama.cpp:server -m /model.gguf
```

## Development

### Build Locally (without Docker)
The orchestrator's binary paths are configurable, so it can run against
locally installed whisper-cli/piper:

```bash
# Install dependencies (sox needs the ALSA format plugin)
sudo apt install sox libsox-fmt-alsa alsa-utils

# Build orchestrator
go build -o voice-assistant orchestrator.go

# Run with local paths/models
WHISPER_BIN=/path/to/whisper-cli \
PIPER_BIN=/path/to/piper \
ESPEAK_DATA=/path/to/espeak-ng-data \
WHISPER_MODEL=./models/whisper/ggml-small.en-q5_1.bin \
PIPER_MODEL=./models/piper/en_US-ryan-high.onnx \
MODELS_DIR=./models ./install_models.sh   # download models first
./voice-assistant
```

### Rebuild Container
```bash
docker compose build
```

Builds go through the dedicated `voice` buildx builder (docker-container
driver, selected as default). Its cache lives inside the builder container
and survives rebuilds reliably; the daemon's embedded builder had broken
cache persistence after a docker restart (records were never written, so
every rebuild re-downloaded the sources from GitHub). If the builder is
missing (e.g. after `docker buildx rm voice` or a fresh host), recreate it:

```bash
docker buildx create --name voice --driver docker-container --use
```

Unchanged steps (including the git clones) are skipped, so a rebuild with
no Dockerfile changes takes under a second. `--no-cache` forces a full
rebuild including re-downloading sources — use it only to reset a corrupt
cache.

### Build Cache Issues
If rebuilds unexpectedly re-download the sources from GitHub every time,
first check `docker buildx ls` — builds must use the `voice` builder, not
the daemon's embedded `default` (see Rebuild Container above). Also check
disk usage (`df -h /`): BuildKit's garbage collector evicts build-cache
records when the disk is close to full (~80%+), so no layer cache survives
between builds. The Dockerfile also keeps cached layer diffs small by
cloning and building in separate layers and removing build trees after
copying artifacts.

## License

MIT for this repository's own code (orchestrator.go, scripts, Dockerfile,
compose files). The built image bundles third-party components under their
own licenses: whisper.cpp (MIT), piper1-gpl + espeak-ng (GPL-3.0), onnxruntime
(MIT), sox (GPL-2.0+/LGPL-2.1+), alsa-utils (GPL-2.0+), libasound2/libpulse0
(LGPL-2.1+), wget (GPL-3.0+), curl (curl license). Full list with pinned
sources: THIRD_PARTY_NOTICES.md; texts: LICENSES/ (also copied into the image
at /usr/share/licenses/voice-assistant/ so notices accompany a distributed
image, GPLv3 sections 4/6). Models are MIT: whisper weights per
openai/whisper ("code and model weights are released under the MIT License"),
piper voices per rhasspy/piper-voices. The Go toolchain (BSD-3-Clause) is
builder-stage only. Keep .dockerignore's `!THIRD_PARTY_NOTICES.md` negation
rule (against the `*.md` exclusion) — the Dockerfile COPYs it and LICENSES/.
