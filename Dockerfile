FROM golang:1.22-bookworm AS builder

ENV DEBIAN_FRONTEND=noninteractive

WORKDIR /workspace

# Install build dependencies
RUN apt-get update && apt-get install -y \
    build-essential \
    cmake \
    git \
    wget \
    curl \
    pkg-config \
    libsdl2-dev \
    libasound2-dev \
    libpulse-dev \
    && rm -rf /var/lib/apt/lists/*

# Clone and build whisper.cpp (CPU only, English models)
RUN git clone --depth 1 --branch master https://github.com/ggml-org/whisper.cpp.git && \
    cd whisper.cpp && \
    cmake -B build -DWHISPER_SDL2=OFF -DWHISPER_COMMON_FFMPEG=ON && \
    cmake --build build --config Release -j$(nproc) && \
    cp build/bin/whisper-cli /workspace/

# Clone and build piper
RUN git clone --depth 1 --branch main https://github.com/OHF-Voice/piper1-gpl.git piper && \
    cd piper && \
    pip install --no-cache-dir piper-tts && \
    cp -r /root/.local/bin /usr/local/bin/ 2>/dev/null || true

# Build orchestrator
WORKDIR /workspace
COPY go.mod /workspace/
RUN go mod download

COPY orchestrator.go /workspace/
RUN go build -o voice-assistant orchestrator.go

# Runtime stage
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y \
    libasound2 \
    libpulse0 \
    sox \
    espeak-ng \
    python3 \
    wget \
    curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy binaries from builder
COPY --from=builder /workspace/whisper-cli /app/whisper-cli
COPY --from=builder /workspace/voice-assistant /app/voice-assistant
COPY --from=builder /usr/local/bin/piper /app/piper

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
