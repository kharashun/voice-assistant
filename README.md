# Voice Assistant

A low-latency voice assistant with speech-to-text (STT), LLM processing, and text-to-speech (TTS) in a single Docker container.

## Features

- **Speech-to-Text**: whisper.cpp (CPU, tiny.en model)
- **LLM Processing**: Calls local llama.cpp API
- **Text-to-Speech**: piper (high-quality, fast, CLI mode)
- **Total Latency**: 2-3 seconds (target)
- **SoX**: Single command for 16kHz WAV capture (50-100ms faster than arecord+ffmpeg)
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
docker-compose build

# Download models
docker-compose run --rm voice-assistant ./install_models.sh

# Start the assistant
docker-compose up -d

# View logs
docker-compose logs -f

# Stop
docker-compose down
```

## Manual Start Script

```bash
./start.sh
```

## Configuration

Environment variables:

- `WHISPER_MODEL`: Path to whisper model (default: `/models/whisper/ggml-tiny.en.bin`)
- `PIPER_MODEL`: Path to piper model (default: `/models/piper/en_US-lessac-medium.onnx`)
- `LLM_ENDPOINT`: LLM API endpoint (default: `http://host.docker.internal:8080`)
- `DEBUG`: Enable debug logging (default: `false`)

## Models

| Model | Size | Description |
|-------|------|-------------|
| whisper tiny.en | 75MB | Fastest English STT model |
| piper en_US-lessac-medium | 150MB | High-quality English TTS |

## Performance Targets

- **Total latency**: 2-3 seconds
- **Whisper STT**: <500ms
- **LLM inference**: <1500ms (via llama.cpp)
- **Piper TTS**: <500ms

## Development

### Build locally

```bash
sudo apt install sox alsa-utils
go build -o voice-assistant orchestrator.go
./voice-assistant
```

### Rebuild Docker

```bash
docker-compose build --no-cache
```

## Troubleshooting

### No audio device found

Ensure ALSA and SoX are configured on the host:
```bash
sudo apt install alsa-utils sox
```

### Model not found

Run the install script:
```bash
docker-compose run --rm voice-assistant ./install_models.sh
```

### LLM connection failed

Ensure llama.cpp is running on `localhost:8080`:
```bash
docker run -p 8080:8080 -v /path/to/model:/model ggerganov/llama.cpp:server -m /model/ggml-model.bin
```

## Licenses

Voice Assistant: MIT

### Third-party Licenses

- **SoX**: GPL-2.0-or-later (see [LICENSES/LICENSE.GPL-2.0](LICENSES/LICENSE.GPL-2.0))
- **Piper**: GPL-3.0 (see [LICENSES/LICENSE.GPL-3.0](LICENSES/LICENSE.GPL-3.0))
- **Whisper.cpp**: MIT (see [LICENSES/LICENSE.MIT-whisper](LICENSES/LICENSE.MIT-whisper))
