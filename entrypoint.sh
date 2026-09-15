#!/bin/bash

set -e

echo "=== Voice Assistant Startup ==="

export WHISPER_MODEL="${WHISPER_MODEL:-/models/whisper/ggml-tiny.en.bin}"
export PIPER_MODEL="${PIPER_MODEL:-/models/piper/en_US-lessac-medium.onnx}"
export LLM_ENDPOINT="${LLM_ENDPOINT:-http://host.docker.internal:8080}"

echo "Config:"
echo "  WHISPER_MODEL: $WHISPER_MODEL"
echo "  PIPER_MODEL: $PIPER_MODEL"
echo "  LLM_ENDPOINT: $LLM_ENDPOINT"

# Check prerequisites
echo ""
echo "Checking prerequisites..."

if ! aplay -l &>/dev/null; then
    echo "WARNING: No ALSA devices found. Audio may not work."
fi

if ! arecord -L &>/dev/null; then
    echo "WARNING: No microphone detected."
fi

if [ ! -f "$WHISPER_MODEL" ]; then
    echo "ERROR: Whisper model not found at $WHISPER_MODEL"
    echo "Please run: docker-compose run --rm voice-assistant ./install_models.sh"
    exit 1
fi

if [ ! -f "$PIPER_MODEL" ]; then
    echo "ERROR: Piper model not found at $PIPER_MODEL"
    echo "Please run: docker-compose run --rm voice-assistant ./install_models.sh"
    exit 1
fi

echo ""
echo "Starting voice assistant..."
exec /app/voice-assistant
