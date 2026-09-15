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

echo "Starting Piper TTS server on port 5000..."
/app/piper --model "$PIPER_MODEL" --port 5000 --length-scale 1.0 &
PIPER_PID=$!

sleep 2

echo "Starting voice assistant..."
exec /app/voice-assistant
