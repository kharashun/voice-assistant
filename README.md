# Voice Assistant

A low-latency voice assistant with speech-to-text (STT), LLM processing, and text-to-speech (TTS) in a single Docker container.

## Features

- **Speech-to-Text**: whisper.cpp (CPU, tiny.en model)
- **LLM Processing**: Calls local llama.cpp API
- **Text-to-Speech**: piper (C++ CLI, built from source)
- **Total Latency**: fixed capture window (default 5s) + 2-3s processing (target)
- **SoX**: Single command for 16kHz WAV capture

## Architecture

```
Host (Ubuntu)
├── Microphone (ALSA)
├── Speaker (ALSA)
└── llama.cpp API (localhost:8080, EXTERNAL)
    └── Docker Container
        ├── voice-assistant (Go orchestrator)
        ├── whisper-cli (STT, CPU)
        └── piper (TTS, CLI mode)
```

## Prerequisites

- Docker and Docker Compose
- Host with microphone and speaker
- llama.cpp server running on `localhost:8080`

## Quick Start

```bash
# Build the image
docker compose build

# Download models
docker compose run --rm voice-assistant /app/install_models.sh

# Start the assistant
docker compose up -d

# View logs
docker compose logs -f

# Stop
docker compose down
```

## Manual Start Script

```bash
./start.sh
```

## Configuration

Environment variables (all optional, see AGENTS.md for the full table):

- `WHISPER_MODEL`: Path to whisper model (default: `/models/whisper/ggml-tiny.en.bin`)
- `PIPER_MODEL`: Path to piper model (default: `/models/piper/en_US-lessac-medium.onnx`)
- `LLM_ENDPOINT`: LLM API endpoint (default: `http://127.0.0.1:8080` - the container runs in host network mode)
- `LLM_MODEL`: Model name sent with every request (required for llama.cpp router mode, e.g. `--models-dir`; default: empty)
- `LLM_SYSTEM_PROMPT`: System prompt for chat completions (default: short voice-assistant prompt)
- `LLM_TIMEOUT`: LLM request timeout (default: `30s`)
- `CAPTURE_SECONDS`: Fixed capture window in seconds (default: `5`)
- `DEBUG`: Enable debug logging (default: `false`)

### Changing the LLM endpoint

The llama.cpp endpoint can be overridden without editing any files, either
via a host environment variable:

```bash
LLM_ENDPOINT=http://192.168.1.50:8080 docker compose up
```

or via a `.env` file in the project directory (Docker Compose loads it
automatically):

```bash
cp .env.example .env
# edit LLM_ENDPOINT in .env, then:
docker compose up
```

## Models

| Model | Size | Description |
|-------|------|-------------|
| whisper tiny.en | 75MB | Fastest English STT model |
| piper en_US-lessac-medium | 63MB | High-quality English TTS (+ .onnx.json config) |

## Performance Targets

- **Total latency**: fixed 5s capture window + 2-3s processing
- **Whisper STT**: <500ms
- **LLM inference**: <1500ms (via llama.cpp)
- **Piper TTS**: <500ms

## Development

### Build locally

```bash
sudo apt install sox libsox-fmt-alsa alsa-utils
go build -o voice-assistant orchestrator.go
WHISPER_BIN=/path/to/whisper-cli PIPER_BIN=/path/to/piper \
ESPEAK_DATA=/path/to/espeak-ng-data ./voice-assistant
```

### Rebuild Docker

```bash
docker compose build
```

Rebuilds use the build cache — unchanged steps (including the git
clones) are skipped, so a rebuild with no Dockerfile changes takes under
a second. To force a full rebuild that re-downloads everything (e.g.
when the cache is corrupt), use `docker compose build --no-cache`.

## Troubleshooting

### No audio device found

Check that ALSA devices are visible inside the container:
```bash
docker compose run --rm voice-assistant aplay -l
```

### Model not found

Run the install script:
```bash
docker compose run --rm voice-assistant /app/install_models.sh
```
### LLM connection failed

Ensure llama.cpp is running on `localhost:8080` (or whatever `LLM_ENDPOINT`
points to):

```bash
docker run -p 8080:8080 -v /path/to/model:/model ggerganov/llama.cpp:server -m /model/gguf
```

If the server runs in router mode (`--models-dir`/`--model-presets`) you'll
get `400: model name is missing from the request` — set `LLM_MODEL` in
`.env` to one of the names from `/v1/models`.

## Licenses

Voice Assistant: MIT

### Third-party Licenses

- **SoX**: GPL-2.0-or-later (see [LICENSES/LICENSE.GPL-2.0](LICENSES/LICENSE.GPL-2.0))
- **Piper**: GPL-3.0 (see [LICENSES/LICENSE.GPL-3.0](LICENSES/LICENSE.GPL-3.0))
- **Whisper.cpp**: MIT (see [LICENSES/LICENSE.MIT-whisper](LICENSES/LICENSE.MIT-whisper))
