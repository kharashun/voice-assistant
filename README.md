# Voice Assistant

A low-latency voice assistant with speech-to-text (STT), LLM processing, and text-to-speech (TTS) in a single Docker container.

## Features

- **Speech-to-Text**: whisper.cpp (CPU, tiny.en model)
- **LLM Processing**: Calls local llama.cpp API
- **Text-to-Speech**: piper (high-quality, fast)
- **Total Latency**: 2-3 seconds
- **Audio Capture**: Uses `sox` (rec command)

## Architecture

```
Host (Ubuntu)
├── Microphone (ALSA)
├── Speaker (ALSA)
└── llama.cpp API (localhost:8080, EXTERNAL)
    └── Docker Container
        ├── orchestrator.go (main logic)
        ├── whisper-cli (STT, CPU)
        └── piper (TTS, CLI)
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
go build -o voice-assistant orchestrator.go
./voice-assistant
```

### Rebuild Docker

```bash
docker-compose build --no-cache
```

## Troubleshooting

### No audio device found

Ensure ALSA is configured on the host:
```bash
sudo apt install alsa-utils
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

## License

MIT
