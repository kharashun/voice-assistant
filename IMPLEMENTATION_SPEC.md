# Voice Assistant - Implementation Specification

## Overview

A low-latency voice assistant system for Ubuntu with speech-to-text (STT), LLM processing, and text-to-speech (TTS) in a single Docker container.

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                     HOST MACHINE (Ubuntu)                    │
│                                                              │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐  │
│  │  Microphone  │    │  Speaker     │    │  llama.cpp   │  │
│  │  (ALSA)      │    │  (ALSA)      │    │  Port 8080   │  │
│  └──────┬───────┘    └──────┬───────┘    └──────┬───────┘  │
│         │                   │                   │           │
│         ▼                   ▼                   ▼           │
│  ┌─────────────────────────────────────────────────────┐   │
│  │        Docker Container (Combined, bridge mode)     │   │
│  │                                                     │   │
│  │  ┌─────────────────────────────────────────────┐   │   │
│  │  │  voice-assistant (Go Orchestrator)          │   │   │
│  │  │  - Audio capture (sox, single process)      │   │   │
│  │  │  - Whisper STT integration                  │   │   │
│  │  │  - LLM API calls (host.docker.internal:8080)│   │   │
│  │  │  - Piper TTS integration (text via stdin)   │   │   │
│  │  │  - Audio playback (aplay)                   │   │   │
│  │  └─────────────────────────────────────────────┘   │   │
│  │                  │           │                       │   │
│  │                  ▼           ▼                       │   │
│  │        ┌─────────────┐ ┌───────┐                     │   │
│  │        │ whisper-cli │ │ piper │  (CLI mode only)   │   │
│  │        │ (STT)       │ │ (TTS) │                    │   │
│  │        └─────────────┘ └───────┘                     │   │
│  └─────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────┘
```

## Component Specifications

### 1. Go Orchestrator (`orchestrator.go`)

**Responsibilities:**
- Capture audio from the host microphone via ALSA (sox, single process)
- Pass audio to whisper-cli for STT
- Parse STT output (read the `-of <base> -otxt` file, never parse log output)
  and send it to the LLM API
- Parse LLM response and send it to piper via stdin
- Play audio to the host speaker
- Measure and report total latency (including the capture window)

**Audio Pipeline:**
```go
captureAudio() → sttWithWhisper() → callLLM() → ttsWithPiper() → playAudio()
```

**API Integration:**
- LLM endpoint: `http://host.docker.internal:8080/v1/chat/completions`
  (bridge network with the host-gateway alias; the llama.cpp server on the
  host must listen on the Docker bridge gateway, e.g. `--host 0.0.0.0`,
  not only on loopback)
- Piper: `/app/piper` (CLI mode; text is written to its stdin, output WAV is
  written via `--output-file`)

**Configuration:**
```go
type Config struct {
    WhisperBin     string        // WHISPER_BIN env var
    WhisperModel   string        // WHISPER_MODEL env var
    PiperBin       string        // PIPER_BIN env var
    PiperModel     string        // PIPER_MODEL env var
    EspeakData     string        // ESPEAK_DATA env var
    LLMEndpoint    string        // LLM_ENDPOINT env var
    LLMModel       string        // LLM_MODEL env var (required for llama.cpp router mode)
    LLMSystemPrompt string       // LLM_SYSTEM_PROMPT env var
    LLMTimeout     time.Duration // LLM_TIMEOUT env var
    CaptureSeconds int           // CAPTURE_SECONDS env var
    Debug          bool          // DEBUG env var
}
```

### 2. whisper.cpp (STT)

**Source:** ggml-org/whisper.cpp (pinned v1.9.4)
**Build:** Compiled from source, statically linked (`BUILD_SHARED_LIBS=OFF`),
no SDL2, no FFmpeg (input is a plain 16kHz WAV produced by sox)
**Model:** ggml-small.en-q5_1.bin (190MB, quantized)
**Mode:** CLI mode for lowest overhead
**CPU Only:** No GPU acceleration

**Command:**
```bash
./whisper-cli -m /models/whisper/ggml-small.en-q5_1.bin -f /tmp/input.wav \
    -of /tmp/input -otxt
# optional: -t $WHISPER_THREADS to use more than the default 4 CPU threads
# transcript is then read from /tmp/input.txt
```

### 3. piper (TTS)

**Source:** OHF-Voice/piper1-gpl (pinned v1.8.0)
**Build:** The C++ CLI (`libpiper/src/main/piper_exe`) built from source with
cmake; espeak-ng is built as a static dependency, onnxruntime is fetched as a
prebuilt shared library
**Model:** en_US-ryan-high.onnx (120MB) + .onnx.json config
**Mode:** CLI mode only (no HTTP server)

**Command:**
```bash
echo "Hello world" | /app/piper \
    --model /models/piper/en_US-ryan-high.onnx \
    --espeak-data /opt/espeak-ng-data \
    --output-file /tmp/output.wav
```

Note: piper has no `--input-text` or `--sample-rate` options; text is read
from stdin and the WAV is written at the voice's native sample rate.

## Docker Configuration

### Docker Compose (`docker-compose.yml`)

```yaml
services:
  voice-assistant:
    build:
      context: .
      dockerfile: Dockerfile
    container_name: voice-assistant
    extra_hosts:                  # Reach host llama.cpp via host-gateway alias
      - "host.docker.internal:host-gateway"
    volumes:
      - ./models:/models          # Model mount
    environment:
      - WHISPER_MODEL=/models/whisper/ggml-small.en-q5_1.bin
      - PIPER_MODEL=/models/piper/en_US-ryan-high.onnx
      - WHISPER_THREADS=${WHISPER_THREADS:-}
      - LLM_ENDPOINT=http://host.docker.internal:8080
      - DEBUG=false
    devices:
      - /dev/snd:/dev/snd         # Audio device passthrough
    group_add:                    # Host audio group for non-root /dev/snd access
      - "${AUDIO_GID:-29}"
    restart: unless-stopped
```

### Dockerfile (`Dockerfile`)

**Multi-stage build:**

1. **Builder stage (golang:1.23-bookworm):**
   - Install build dependencies (cmake ≥3.26 via PyPI, ninja, build-essential)
   - Clone and build whisper.cpp (static, no SDL2/FFmpeg)
   - Clone and build piper's C++ CLI from libpiper
   - Build Go orchestrator

2. **Runtime stage (debian:bookworm-slim):**
   - Install runtime dependencies (alsa-utils, sox + ALSA format plugin,
     libgomp1, libstdc++6)
   - Copy binaries (whisper-cli, voice-assistant, piper), shared libraries
     (libpiper.so, libonnxruntime.so → /usr/local/lib + ldconfig) and
     espeak-ng data (/opt/espeak-ng-data)
   - Copy entrypoint + model install scripts
   - ENTRYPOINT + CMD (the entrypoint passes through any command)

## Audio I/O Implementation

### Capture (`captureAudio`)
```bash
# Single process: record the default ALSA device as 16kHz mono 16-bit WAV
sox -d -r 16000 -c 1 -b 16 -t wav /tmp/input.wav trim 0 5
```

### Playback (`playAudio`)
```bash
aplay -q /tmp/output.wav
```

## Performance Targets

| Component | Target | Actual (estimated) | Notes |
|-----------|--------|-------------------|-------|
| Audio capture | fixed 5s window | 5s | `CAPTURE_SECONDS`, no VAD |
| Whisper STT | <1500ms | 700-1500ms | small.en-q5_1 model, Release build |
| LLM inference | <1500ms | 1-2s | depends on model/hardware |
| Piper TTS | <1000ms | 600-900ms | ryan-high model, CLI mode, model loaded per call |
| Playback | <200ms | 100-200ms | aplay |
| **Total** | 5s + <3s | 6.5-8s | includes the fixed capture window |

## Model Download (`install_models.sh`)

Downloads models at runtime into the `/models` volume (mounted from `./models`):
- Whisper: `ggml-small.en-q5_1.bin` (190MB) from HuggingFace (ggerganov/whisper.cpp)
- Piper: `en_US-ryan-high.onnx` (120MB) + `.onnx.json` (4KB) from
  HuggingFace (rhasspy/piper-voices)

```bash
docker compose run --rm voice-assistant /app/install_models.sh
```

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `WHISPER_MODEL` | `/models/whisper/ggml-small.en-q5_1.bin` | Whisper model path |
| `WHISPER_THREADS` | _(unset)_ | CPU threads for whisper-cli (`-t`); unset = whisper-cli default (4) |
| `PIPER_MODEL` | `/models/piper/en_US-ryan-high.onnx` | Piper model path |
| `LLM_ENDPOINT` | `http://host.docker.internal:8080` | LLM API URL (bridge network, host-gateway alias) |
| `LLM_TIMEOUT` | `30s` | LLM request timeout (Go duration) |
| `CAPTURE_SECONDS` | `5` | Fixed capture window |
| `WHISPER_BIN` | `/app/whisper-cli` | whisper-cli binary path |
| `PIPER_BIN` | `/app/piper` | piper binary path |
| `ESPEAK_DATA` | `/opt/espeak-ng-data` | espeak-ng data dir |
| `DEBUG` | `false` | Enable debug logging |

### Log Output

```bash
# Normal operation
Voice Assistant ready! Press Ctrl+C to exit.
Listening... (5s window)
Processing...
You said: Hello how are you
Assistant: I'm doing great, thank you!
Speaking...
Capture: 5.1s | STT+LLM+TTS+playback: 2.3s | Total: 7.4s

# Silence (whisper writes "[BLANK_AUDIO]")
You said: [BLANK_AUDIO]
No speech detected. Listening again...

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
docker compose build
docker compose run --rm voice-assistant /app/install_models.sh
docker compose up -d
```

### Stopping

```bash
docker compose down
```

### Logs

```bash
docker compose logs -f voice-assistant
```

## Development

### Local Build (without Docker)

```bash
# Install dependencies (sox needs the ALSA format plugin)
sudo apt install sox libsox-fmt-alsa alsa-utils

# Build orchestrator
go build -o voice-assistant orchestrator.go

# Run with local paths/models
WHISPER_BIN=/path/to/whisper-cli \
PIPER_BIN=/path/to/piper \
ESPEAK_DATA=/path/to/espeak-ng-data \
MODELS_DIR=./models ./install_models.sh   # download models first
./voice-assistant
```

### Rebuild Container

```bash
docker compose build --no-cache
```

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
docker compose run --rm --user 0 voice-assistant /app/install_models.sh
```

### LLM Connection

```bash
# Check llama.cpp is running (via the host-gateway alias)
docker compose run --rm voice-assistant curl -s http://host.docker.internal:8080/health

# Start llama.cpp
docker run -p 8080:8080 \
  -v /path/to/model:/models \
  ggerganov/llama.cpp:server \
  -m /models/ggml-model.bin
```

## File Structure

```
voice-assistant/
├── AGENTS.md                  # Project context and architecture
├── docker-compose.yml         # Container orchestration
├── Dockerfile                 # Container build instructions
├── orchestrator.go            # Go orchestrator code
├── go.mod                     # Go module definition
├── entrypoint.sh              # Container startup script
├── install_models.sh          # Model download script
├── start.sh                   # Quick start script
├── README.md                  # User documentation
├── IMPLEMENTATION_SPEC.md     # This file
├── PERFORMANCE_IMPROVEMENTS.md# Optimization plan
├── LICENSES/                  # Third-party licenses
└── .dockerignore              # Docker build ignore rules
```

## License

MIT
