#!/bin/bash

set -e

echo "=== Voice Assistant Startup ==="

export WHISPER_MODEL="${WHISPER_MODEL:-/models/whisper/ggml-small.en-q5_1.bin}"
export WHISPER_VAD_MODEL="${WHISPER_VAD_MODEL:-/models/whisper/ggml-silero-v5.1.2.bin}"
export PIPER_MODEL="${PIPER_MODEL:-/models/piper/en_US-ryan-high.onnx}"
# The container runs on a bridge network with the host-gateway alias, so the
# llama.cpp server on the host is reachable via host.docker.internal (it must
# listen on the Docker bridge gateway, e.g. --host 0.0.0.0, not only on
# loopback).
export LLM_ENDPOINT="${LLM_ENDPOINT:-http://host.docker.internal:8080}"

echo "Config:"
echo "  WHISPER_MODEL: $WHISPER_MODEL"
echo "  WHISPER_VAD_MODEL: $WHISPER_VAD_MODEL"
echo "  PIPER_MODEL: $PIPER_MODEL"
echo "  LLM_ENDPOINT: $LLM_ENDPOINT"
echo "  LLM_MODEL: ${LLM_MODEL:-(none)}"

# If a command was passed (e.g. `docker compose run --rm voice-assistant <cmd>`
# for /app/install_models.sh, aplay -l, debugging shells, ...), run it directly.
if [ "$#" -gt 0 ]; then
    exec "$@"
fi

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
    echo "Please run: docker compose run --rm --user 0 voice-assistant /app/install_models.sh"
    exit 1
fi

if [ ! -f "$PIPER_MODEL" ] || [ ! -f "$PIPER_MODEL.json" ]; then
    echo "ERROR: Piper model not found at $PIPER_MODEL (expected $PIPER_MODEL and $PIPER_MODEL.json)"
    echo "Please run: docker compose run --rm --user 0 voice-assistant /app/install_models.sh"
    exit 1
fi

# Optional: whisper.cpp's built-in Silero VAD. A missing model only disables
# the --vad flag (the orchestrator degrades gracefully), so warn, don't fail.
if [ ! -f "$WHISPER_VAD_MODEL" ]; then
    echo "WARNING: Whisper VAD model not found at $WHISPER_VAD_MODEL"
    echo "STT will run without built-in VAD. Run install_models.sh to download it."
fi

echo ""
echo "Starting voice assistant..."
exec /app/voice-assistant
