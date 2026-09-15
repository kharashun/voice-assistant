# Voice Assistant Project

## Project Overview

This is a **low-latency voice assistant** system for Ubuntu that provides:
- **Speech-to-Text (STT)**: whisper.cpp (CPU, English models)
- **LLM Processing**: Calls local llama.cpp API endpoint
- **Text-to-Speech (TTS)**: piper (high-quality, fast neural TTS)

**Total latency target: 2-3 seconds**

## Architecture

```
Host (Ubuntu)
├── Microphone (ALSA)
├── Speaker (ALSA)
└── llama.cpp API (localhost:8080, EXTERNAL)
    └── Docker Container (COMBINED - single container)
        ├── voice-assistant (Go orchestrator)
        ├── whisper-cli (STT, CPU, CLI mode)
        └── piper (TTS, CLI mode)
```

### Container Design Decision

All services run in **a single combined container** for minimal overhead:
- **No network latency** between whisper→piper (file-based transfer)
- **Simpler deployment** - one container to manage
- **Lower resource footprint** - single base image
- **Sequential processing** - whisper runs, then piper runs

### Audio Pipeline

```
Host Mic (48kHz ALSA)
    ↓
ffmpeg/arecord → 16kHz WAV
    ↓
whisper-cli (STT) → text
    ↓
HTTP POST to llama.cpp:8080
    ↓
Response text
    ↓
piper CLI (TTS) → WAV
    ↓
aplay → Host Speaker
```

## Build Instructions

```bash
# Build the Docker image
docker-compose build

# Download models (first time only)
docker-compose run --rm voice-assistant ./install_models.sh

# Start the service
docker-compose up -d
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `WHISPER_MODEL` | `/models/whisper/ggml-tiny.en.bin` | Path to whisper model |
| `PIPER_MODEL` | `/models/piper/en_US-lessac-medium.onnx` | Path to piper model |
| `LLM_ENDPOINT` | `http://host.docker.internal:8080` | LLM API endpoint |
| `DEBUG` | `false` | Enable debug logging |

## Run Commands

```bash
# Start in foreground with logs
docker-compose up

# Start in background
docker-compose up -d

# Stop
docker-compose down

# View logs
docker-compose logs -f

# Rebuild
docker-compose build --no-cache
```

## Performance Targets

| Component | Target Latency | Notes |
|-----------|----------------|-------|
| **Total** | <3 seconds | End-to-end |
| Whisper STT | <500ms | tiny.en model |
| LLM inference | <1500ms | via llama.cpp |
| Piper TTS | <500ms | CLI mode |
| Audio I/O | <200ms | Capture + playback |

## Models

| Model | Size | Description |
|-------|------|-------------|
| whisper tiny.en | 75MB | Fastest English STT model |
| piper en_US-lessac-medium | 150MB | High-quality English TTS |

## Prerequisites

- Docker and Docker Compose
- Ubuntu 22.04/24.04 host
- Host with microphone and speaker
- llama.cpp server running on `localhost:8080`

## Directory Structure

```
voice-assistant/
├── AGENTS.md              # This file - project context
├── docker-compose.yml     # Docker Compose configuration
├── Dockerfile             # Combined container build
├── orchestrator.go        # Go orchestrator code
├── go.mod                 # Go module file
├── entrypoint.sh          # Container entrypoint script
├── install_models.sh      # Model download script
├── start.sh               # Quick start script
├── README.md              # User documentation
├── .dockerignore          # Docker ignore file
├── models/                # Mount point for models
│   ├── whisper/
│   └── piper/
└── voices/                # Alternative voice directory
```

## Key Implementation Details

### Orchestrator (Go)
- Captures audio from host via ALSA (ffmpeg/arecord)
- Converts to 16kHz WAV format
- Calls whisper-cli for STT
- POSTs to llama.cpp API for LLM inference
- Calls piper CLI for TTS
- Plays audio via aplay
- Measures and reports total latency

### Whisper Integration
- Built from source (ggml-org/whisper.cpp)
- CPU only (no GPU acceleration)
- tiny.en model for fastest inference
- CLI mode (lowest overhead)

### Piper Integration
- Built from source (OHF-Voice/piper1-gpl)
- CPU only
- HTTP server mode for API calls
- CLI mode for direct file processing
- espeak-ng for phonemization

### Docker Configuration
- Single container approach
- Host network mode for llama.cpp access
- /dev/snd passthrough for ALSA audio
- Volume mounts for models

## Troubleshooting

### Audio Issues
```bash
# Check ALSA devices
docker-compose run --rm voice-assistant aplay -l

# Test microphone
docker-compose run --rm voice-assistant arecord -D default -f cd test.wav
```

### Model Issues
```bash
# Re-download models
docker-compose run --rm voice-assistant ./install_models.sh
```

### LLM Connection
```bash
# Ensure llama.cpp is running
docker run -p 8080:8080 ggerganov/llama.cpp:server -m /model.gguf
```

## Development

### Build Locally (without Docker)
```bash
# Install dependencies
sudo apt install sox libsox-fmt-all alsa-utils

# Build orchestrator
go build -o voice-assistant orchestrator.go

# Run
./voice-assistant
```

### Rebuild Container
```bash
docker-compose build --no-cache
```

## License

MIT
