FROM golang:1.23-bookworm AS builder

ENV DEBIAN_FRONTEND=noninteractive

WORKDIR /workspace

# Install build dependencies
RUN apt-get update && apt-get install -y \
    build-essential \
    cmake \
    ninja-build \
    git \
    wget \
    curl \
    pkg-config \
    python3 \
    python3-pip \
    && rm -rf /var/lib/apt/lists/*

# libpiper requires cmake >= 3.26; Debian bookworm ships 3.25.
# Install a newer cmake from PyPI into /usr/local/bin (shadows apt cmake).
RUN pip install --no-cache-dir --break-system-packages "cmake>=3.26"

# Clone and build whisper.cpp (CPU only, English models).
# SDL2 and FFmpeg are disabled: input is a plain 16kHz WAV produced by sox,
# so whisper-cli has no libav*/SDL runtime dependencies.
RUN git clone --depth 1 --branch v1.9.4 https://github.com/ggml-org/whisper.cpp.git && \
    cd whisper.cpp && \
    cmake -B build -DWHISPER_SDL2=OFF -DBUILD_SHARED_LIBS=OFF -DCMAKE_BUILD_TYPE=Release && \
    cmake --build build --config Release -j$(nproc) && \
    cp build/bin/whisper-cli /workspace/

# Clone and build piper (C++ CLI from libpiper, built from source).
# espeak-ng is built as a static dependency and onnxruntime is fetched
# as a prebuilt library by cmake. Text is read from stdin.
RUN git clone --depth 1 --branch v1.8.0 https://github.com/OHF-Voice/piper1-gpl.git piper && \
    cmake -S piper/libpiper -B piper/build -DCMAKE_BUILD_TYPE=Release && \
    cmake --build piper/build --config Release -j$(nproc) && \
    cp piper/build/src/main/piper_exe /workspace/piper_bin && \
    cp piper/build/libpiper.so /workspace/ && \
    cp piper/libpiper/lib/onnxruntime-linux-x64-*/lib/libonnxruntime.so /workspace/ && \
    cp -r piper/build/espeak_ng-install/share/espeak-ng-data /workspace/espeak-ng-data

# Build orchestrator
COPY go.mod /workspace/
COPY orchestrator.go /workspace/
RUN go build -o voice-assistant orchestrator.go

# Runtime stage
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y \
    alsa-utils \
    libasound2 \
    libpulse0 \
    libsox-fmt-alsa \
    libgomp1 \
    libstdc++6 \
    sox \
    wget \
    curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy binaries and libraries from builder
COPY --from=builder /workspace/whisper-cli /app/whisper-cli
COPY --from=builder /workspace/voice-assistant /app/voice-assistant
COPY --from=builder /workspace/piper_bin /app/piper
COPY --from=builder /workspace/libpiper.so /usr/local/lib/libpiper.so
COPY --from=builder /workspace/libonnxruntime.so /usr/local/lib/libonnxruntime.so
COPY --from=builder /workspace/espeak-ng-data /opt/espeak-ng-data
RUN ldconfig

COPY install_models.sh /app/install_models.sh
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh /app/install_models.sh

ENTRYPOINT ["/entrypoint.sh"]
CMD ["/app/voice-assistant"]
