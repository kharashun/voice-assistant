#!/bin/bash

set -e

echo "=== Voice Assistant Startup ==="

export WHISPER_MODEL="${WHISPER_MODEL:-/models/whisper/ggml-small.en-q5_1.bin}"
export PIPER_MODEL="${PIPER_MODEL:-/models/piper/en_US-ryan-high.onnx}"
# The container runs with network_mode: host, so the llama.cpp server on the
# host is reachable via the loopback interface (host.docker.internal is only
# resolvable on Docker's bridge networks, not in host mode on Linux).
export LLM_ENDPOINT="${LLM_ENDPOINT:-http://127.0.0.1:8080}"

echo "Config:"
echo "  WHISPER_MODEL: $WHISPER_MODEL"
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
    echo "Please run: docker compose run --rm voice-assistant /app/install_models.sh"
    exit 1
fi

if [ ! -f "$PIPER_MODEL" ] || [ ! -f "$PIPER_MODEL.json" ]; then
    echo "ERROR: Piper model not found at $PIPER_MODEL (expected $PIPER_MODEL and $PIPER_MODEL.json)"
    echo "Please run: docker compose run --rm voice-assistant /app/install_models.sh"
    exit 1
fi

echo ""
echo "Starting voice assistant..."
exec /app/voice-assistant
