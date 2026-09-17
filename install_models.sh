#!/bin/bash

set -e

echo "=== Installing Voice Assistant Models ==="

# Models live in the /models volume inside the container (./models on the
# host). MODELS_DIR can be overridden for local (non-Docker) use.
MODELS_DIR="${MODELS_DIR:-/models}"
WHISPER_MODEL_NAME="ggml-small.en-q5_1.bin"
VAD_MODEL_NAME="ggml-silero-v5.1.2.bin"
PIPER_MODEL_NAME="en_US-ryan-high.onnx"
PIPER_VOICE_URL="https://huggingface.co/rhasspy/piper-voices/resolve/v1.0.0/en/en_US/ryan/high"

mkdir -p "$MODELS_DIR/whisper" "$MODELS_DIR/piper"

echo "Downloading Whisper small.en-q5_1 model..."
if [ ! -f "$MODELS_DIR/whisper/$WHISPER_MODEL_NAME" ]; then
    wget -q --show-progress -O "$MODELS_DIR/whisper/$WHISPER_MODEL_NAME" \
        "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/$WHISPER_MODEL_NAME"
else
    echo "Whisper model already exists, skipping"
fi

echo "Downloading Piper English voice (ryan, high)..."
if [ ! -f "$MODELS_DIR/piper/$PIPER_MODEL_NAME" ]; then
    wget -q --show-progress -O "$MODELS_DIR/piper/$PIPER_MODEL_NAME" \
        "$PIPER_VOICE_URL/$PIPER_MODEL_NAME"
else
    echo "Piper model already exists, skipping"
fi

# The .onnx.json voice config is small; always (re)download it so a partial
# earlier download heals itself.
wget -q --show-progress -O "$MODELS_DIR/piper/$PIPER_MODEL_NAME.json" \
    "$PIPER_VOICE_URL/$PIPER_MODEL_NAME.json"

echo "Downloading Silero VAD model for whisper.cpp..."
if [ ! -f "$MODELS_DIR/whisper/$VAD_MODEL_NAME" ]; then
    wget -q --show-progress -O "$MODELS_DIR/whisper/$VAD_MODEL_NAME" \
        "https://huggingface.co/ggml-org/whisper-vad/resolve/main/$VAD_MODEL_NAME"
else
    echo "Whisper VAD model already exists, skipping"
fi

echo ""
echo "=== Models Installed ==="
echo "Whisper:     $MODELS_DIR/whisper/$WHISPER_MODEL_NAME"
echo "Whisper VAD: $MODELS_DIR/whisper/$VAD_MODEL_NAME"
echo "Piper:       $MODELS_DIR/piper/$PIPER_MODEL_NAME (+ .json)"
echo ""
echo "You can now run: docker compose up -d"
