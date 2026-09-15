# Voice Assistant Project

## Project Overview

This is a **low-latency voice assistant** system for Ubuntu that provides:
- **Speech-to-Text (STT)**: whisper.cpp (CPU, English models)
- **LLM Processing**: Calls local llama.cpp API endpoint
- **Text-to-Speech (TTS)**: piper (C++ CLI, built from source)

**Processing latency target (after the fixed capture window): 2-3 seconds.**

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
sox -d → 16kHz mono 16-bit WAV (fixed capture window, default 5s)
    ↓
whisper-cli (STT) → transcript file (<base>.txt via -of/-otxt)
    ↓
HTTP POST to llama.cpp 127.0.0.1:8080/completion
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

# Download models (first time only)
docker compose run --rm voice-assistant /app/install_models.sh

# Start the service
docker compose up -d
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `WHISPER_MODEL` | `/models/whisper/ggml-tiny.en.bin` | Path to whisper model |
| `PIPER_MODEL` | `/models/piper/en_US-lessac-medium.onnx` | Path to piper model |
| `LLM_ENDPOINT` | `http://127.0.0.1:8080` | LLM API endpoint (host network mode); overridable via host env var or `.env` file |
| `LLM_TIMEOUT` | `30s` | LLM request timeout (Go duration) |
| `CAPTURE_SECONDS` | `5` | Fixed audio capture window in seconds |
| `WHISPER_BIN` | `/app/whisper-cli` | whisper-cli binary path |
| `PIPER_BIN` | `/app/piper` | piper binary path |
| `ESPEAK_DATA` | `/opt/espeak-ng-data` | espeak-ng data dir for piper |
| `DEBUG` | `false` | Enable debug logging |

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
| **Total (end-to-end)** | capture window + 2-3s | Includes the fixed 5s capture window |
| Whisper STT | <500ms | tiny.en model, Release build |
| LLM inference | <1500ms | via llama.cpp |
| Piper TTS | <500ms | CLI mode, model loaded per call |
| Audio capture | fixed 5s window | `CAPTURE_SECONDS`, no VAD yet |

The orchestrator measures latency from the start of capture and reports
capture time and processing time separately.

## Models

| Model | Size | Description |
|-------|------|-------------|
| whisper tiny.en | 75MB | Fastest English STT model |
| piper en_US-lessac-medium | 63MB | High-quality English TTS (+ .onnx.json config) |

Models are downloaded from HuggingFace by `install_models.sh` into the
`/models` volume (`./models` on the host).

## Prerequisites

- Docker and Docker Compose (v2 plugin or v1 binary)
- Ubuntu 22.04/24.04 host
- Host with microphone and speaker
- llama.cpp server running on `localhost:8080`

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
├── IMPLEMENTATION_SPEC.md     # Implementation specification
├── PERFORMANCE_IMPROVEMENTS.md# Optimization plan
├── LICENSES/                  # Third-party licenses
├── .dockerignore              # Docker ignore file
├── .env.example               # Template for .env overrides (LLM_ENDPOINT)
└── models/                    # Mount point for models (host ./models)
    ├── whisper/
    └── piper/
```

## Key Implementation Details

### Orchestrator (Go)
- Captures audio via `sox -d` directly as 16kHz mono WAV (single process)
- Calls whisper-cli, reads the transcript from the `-of <base> -otxt` file
  (never parses the binary's log output)
- Skips the LLM when whisper reports silence (`[BLANK_AUDIO]` or empty text)
- POSTs to llama.cpp `/completion` with a configurable timeout and
  status-code check
- Sends TTS text to piper via **stdin** (piper has no `--input-text` flag)
- Plays audio via `aplay`
- Measures and reports total latency including the capture window

### Whisper Integration
- Built from source (ggml-org/whisper.cpp, pinned v1.9.4)
- CPU only, no SDL2, no FFmpeg (input is plain WAV), statically linked
  (`BUILD_SHARED_LIBS=OFF`) - single self-contained binary
- tiny.en model for fastest inference, Release build

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
- Host network mode (`network_mode: host`) - the llama.cpp server on the
  host is reached via `http://127.0.0.1:8080` (`host.docker.internal` is NOT
  resolvable in host network mode on Linux)
- `LLM_ENDPOINT` is passed through in docker-compose.yml as
  `${LLM_ENDPOINT:-http://127.0.0.1:8080}`, so it can be overridden via a
  host env var or a `.env` file (see `.env.example`)
- /dev/snd passthrough for ALSA audio
- Volume mounts for models
- Entrypoint passes through any command given via
  `docker compose run --rm voice-assistant <cmd>`

## Troubleshooting

### Audio Issues
```bash
# Check ALSA devices
docker compose run --rm voice-assistant aplay -l

# Test microphone (5s recording)
docker compose run --rm voice-assistant bash -c 'sox -d -r 16000 -c 1 -b 16 /tmp/test.wav trim 0 5'
```

### Model Issues
```bash
# Re-download models
docker compose run --rm voice-assistant /app/install_models.sh
```

### LLM Connection
```bash
# Ensure llama.cpp is running (from inside the container, host network mode)
docker compose run --rm voice-assistant curl -s http://127.0.0.1:8080/health

# Start llama.cpp on the host
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
WHISPER_MODEL=./models/whisper/ggml-tiny.en.bin \
PIPER_MODEL=./models/piper/en_US-lessac-medium.onnx \
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

MIT
