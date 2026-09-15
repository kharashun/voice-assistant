# Voice Assistant - Implementation Specification

## Overview

A low-latency voice assistant system for Ubuntu with speech-to-text (STT), LLM processing, and text-to-speech (TTS) in a single Docker container.

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                     HOST MACHINE (Ubuntu)                    │
│                                                              │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐  │
│  │  Microphone  │    │  Speaker   │    │  llama.cpp   │  │
│  │  (ALSA)      │    │  (ALSA)    │    │  Port 8080   │  │
│  └──────┬───────┘    └──────┬───────┘    └──────┬───────┘  │
│         │                   │                   │           │
│         │                   │                   │           │
│         ▼                   ▼                   │           │
│  ┌─────────────────────────────────────────────▼───────────┐ │
│  │              Docker Container (Combined)                │ │
│  │                                                         │ │
│  │  ┌─────────────────────────────────────────────────┐   │ │
│  │  │  voice-assistant (Go Orchestrator)              │   │ │
│  │  │  - Audio capture (ffmpeg/arecord)               │   │ │
│  │  │  - Whisper STT integration                      │   │ │
│  │  │  - LLM API calls (port 8080)                    │   │ │
│  │  │  - Piper TTS integration                        │   │ │
│  │  │  - Audio playback (aplay)                       │   │ │
│  │  └─────────────────────────────────────────────────┘   │ │
│  │                  │           │           │              │ │
│  │                  ▼           ▼           ▼              │ │
│  │        ┌─────────────┐ ┌───────┐ ┌───────────┐         │ │
│  │        │ whisper-cli │ │piper  │ │   piper   │         │ │
│  │        │ (STT)       │ │server │ │  (TTS)    │         │ │
│  │        └─────────────┘ │port   │ └───────────┘         │ │
│  │                        │5000   │                       │ │
│  │                        └───────┘                       │ │
│  └─────────────────────────────────────────────────────────┘ │
└───────────────────────────────────────────────────────────────┘
```

## Component Specifications

### 1. Go Orchestrator (`orchestrator.go`)

**Responsibilities:**
- Capture audio from host microphone via ALSA
- Convert audio to 16kHz WAV format
- Pass audio to whisper-cli for STT
- Parse STT output and send to LLM API
- Parse LLM response and send to piper
- Play audio to host speaker
- Measure and report total latency

**Audio Pipeline:**
```go
captureAudio() → sttWithWhisper() → callLLM() → ttsWithPiper() → playAudio()
```

**API Integration:**
- LLM endpoint: `http://host.docker.internal:8080/completion`
- Piper endpoint: `/app/piper` (CLI mode for low latency)

**Configuration:**
```go
type Config struct {
    WhisperModel string   // WHISPER_MODEL env var
    PiperModel   string   // PIPER_MODEL env var
    LLMEndpoint  string   // LLM_ENDPOINT env var
    Debug        bool     // DEBUG env var
}
```

### 2. whisper.cpp (STT)

**Source:** ggml-org/whisper.cpp
**Build:** Compiled from source with FFmpeg support
**Model:** ggml-tiny.en.bin (75MB)
**Mode:** CLI mode for lowest overhead
**CPU Only:** No GPU acceleration

**Command:**
```bash
./whisper-cli -m /models/whisper/ggml-tiny.en.bin -f /tmp/input.wav -otxt -pc
```

### 3. piper (TTS)

**Source:** OHF-Voice/piper1-gpl
**Build:** Installed via pip, compiled from source
**Model:** en_US-lessac-medium.onnx (150MB)
**Mode:** CLI mode (no HTTP server in combined container)

**Command:**
```bash
/app/piper --model /models/piper/en_US-lessac-medium.onnx \
           --input-text "Hello world" \
           --output-file /tmp/output.wav \
           --sample-rate 22050
```

## Docker Configuration

### Docker Compose (`docker-compose.yml`)

```yaml
version: '3.8'

services:
  voice-assistant:
    build:
      context: .
      dockerfile: Dockerfile
    container_name: voice-assistant
    network_mode: host  # Access host llama.cpp API
    volumes:
      - ./models:/models:ro        # Read-only model mount
      - /dev/snd:/dev/snd          # Audio device passthrough
    environment:
      - WHISPER_MODEL=/models/whisper/ggml-tiny.en.bin
      - PIPER_MODEL=/models/piper/en_US-lessac-medium.onnx
      - LLM_ENDPOINT=http://host.docker.internal:8080
      - DEBUG=false
    devices:
      - /dev/snd:/dev/snd
    cap_add:
      - SYS_NICE
    restart: unless-stopped
```

### Dockerfile (`Dockerfile`)

**Multi-stage build:**

1. **Builder stage:**
   - Install build dependencies (cmake, sox, alsa, etc.)
   - Clone and build whisper.cpp
   - Clone and build piper
   - Build Go orchestrator

2. **Runtime stage:**
   - Install runtime dependencies
   - Copy binaries from builder
   - Copy entrypoint script
   - Set entrypoint

**Base image:** `golang:1.22-bookworm` (builder), `debian:bookworm-slim` (runtime)

## Audio I/O Implementation

### Capture (`captureAudio`)
```bash
# Method 1: Using arecord + ffmpeg
arecord -D default -f cd -t raw -d 5 - | \
    ffmpeg -y -f s16le -ar 44100 -ac 1 -i - \
             -ar 16000 -ac 1 -f wav -
```

### Playback (`playAudio`)
```bash
aplay -q /tmp/output.wav
```

## Performance Targets

| Component | Target | Actual (estimated) | Notes |
|-----------|--------|-------------------|-------|
| Whisper STT | <500ms | 300-600ms | tiny.en model, 4 threads |
| LLM inference | <1500ms | 1-2s | 3B model on GPU |
| Piper TTS | <500ms | 200-400ms | CLI mode, no HTTP |
| Audio I/O | <200ms | 100-200ms | Capture + playback |
| **Total** | <3s | 2.5-3.5s | Dependent on models |

## Model Download (`install_models.sh`)

Downloads models at runtime:
- Whisper: `ggml-tiny.en.bin` (75MB)
- Piper: `en_US-lessac-medium.onnx` + `.json` (150MB)

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `WHISPER_MODEL` | `/models/whisper/ggml-tiny.en.bin` | Whisper model path |
| `PIPER_MODEL` | `/models/piper/en_US-lessac-medium.onnx` | Piper model path |
| `LLM_ENDPOINT` | `http://host.docker.internal:8080` | LLM API URL |
| `DEBUG` | `false` | Enable debug logging |

### Log Output

```bash
# Normal operation
Voice Assistant ready! Press Ctrl+C to exit.
Listening... Processing...
You said: Hello how are you
Assistant: I'm doing great, thank you!
Speaking...
Total latency: 2.3s

# Debug mode (DEBUG=true)
[DEBUG] Captured 160000 bytes of audio
[DEBUG] STT result: Hello how are you
[DEBUG] LLM response: I'm doing great, thank you!
[DEBUG] Generated 320000 bytes of audio
```

## Deployment

### Prerequisites

1. Docker installed and running
2. Docker Compose installed
3. Host microphone and speaker configured
4. llama.cpp server running on port 8080

### Quick Start

```bash
# Build and start
./start.sh

# Or manual start
docker-compose build
docker-compose run --rm voice-assistant ./install_models.sh
docker-compose up -d
```

### Stopping

```bash
docker-compose down
```

### Logs

```bash
docker-compose logs -f voice-assistant
```

## Development

### Local Build (without Docker)

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

## Troubleshooting

### Audio Issues

```bash
# Check ALSA devices
docker-compose run --rm voice-assistant aplay -l

# Test microphone
docker-compose run --rm voice-assistant arecord -D default -f cd test.wav

# Check audio permissions
usermod -aG audio $USER
```

### Model Issues

```bash
# Re-download models
docker-compose run --rm voice-assistant ./install_models.sh
```

### LLM Connection

```bash
# Check llama.cpp is running
curl http://localhost:8080/completion

# Start llama.cpp
docker run -p 8080:8080 \
  -v /path/to/model:/models \
  ggerganov/llama.cpp:server \
  -m /models/ggml-model.bin
```

## File Structure

```
voice-assistant/
├── AGENTS.md              # Project context and architecture
├── docker-compose.yml     # Container orchestration
├── Dockerfile             # Container build instructions
├── orchestrator.go        # Go orchestrator code
├── go.mod                 # Go module definition
├── entrypoint.sh          # Container startup script
├── install_models.sh      # Model download script
├── start.sh               # Quick start script
├── README.md              # User documentation
└── .dockerignore          # Docker build ignore rules
```

## License

MIT