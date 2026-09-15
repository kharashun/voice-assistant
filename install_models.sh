#!/bin/bash

set -e

echo "=== Installing Voice Assistant Models ==="

mkdir -p models/whisper models/piper

echo "Downloading Whisper tiny.en model..."
if [ ! -f models/whisper/ggml-tiny.en.bin ]; then
    wget -q --show-progress -O models/whisper/ggml-tiny.en.bin \
        https://raw.githubusercontent.com/ggerganov/whisper.cpp/master/models/ggml-tiny.en.bin
else
    echo "Whisper model already exists, skipping"
fi

echo "Downloading Piper English model..."
if [ ! -f models/piper/en_US-lessac-medium.onnx ]; then
    mkdir -p models/piper
    wget -q --show-progress -O models/piper/en_US-lessac-medium.onnx \
        https://github.com/rhasspy/piper/releases/download/v1.2.0/en_US-lessac-medium.onnx
    
    wget -q --show-progress -O models/piper/en_US-lessac-medium.onnx.json \
        https://github.com/rhasspy/piper/releases/download/v1.2.0/en_US-lessac-medium.onnx.json
else
    echo "Piper model already exists, skipping"
fi

echo ""
echo "=== Models Installed ==="
echo "Whisper: models/whisper/ggml-tiny.en.bin"
echo "Piper:   models/piper/en_US-lessac-medium.onnx"
echo ""
echo "You can now run: docker-compose up -d"
