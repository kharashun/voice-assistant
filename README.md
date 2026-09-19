# Voice Assistant

A low-latency voice assistant with speech-to-text (STT), LLM processing, and text-to-speech (TTS) in a single Docker container.

## Features

- **Speech-to-Text**: whisper.cpp (CPU, small.en-q5_1 model)
- **Voice-activated capture**: sox `silence` effect — the assistant waits for speech, records your utterance, and stops ~2s after you pause (no fixed window, no timing your speech)
- **Noise robustness**: whisper.cpp's built-in Silero VAD drops non-speech segments (keyboard/background noise) before transcription
- **Conversation memory**: recent turns are kept as context for the LLM; say the reset phrase (default: `new voice assistant session`), go silent for `SESSION_TIMEOUT_SEC`, or let the history trim itself
- **LLM Processing**: Calls local llama.cpp API
- **Text-to-Speech**: piper (C++ CLI, built from source)
- **Total Latency**: utterance + ~2s end-of-speech wait + 3-4s processing (target)
- **SoX**: Single command for 16kHz WAV capture
- **Privacy by default**: STT and TTS run locally; transcripts are sent only to the configured `LLM_ENDPOINT` (a local llama.cpp by default, but it can point at any API)

## Architecture

```
Host (Ubuntu)
├── Microphone (ALSA)
├── Speaker (ALSA)
└── llama.cpp API (host.docker.internal:8080, EXTERNAL)
    └── Docker Container
        ├── voice-assistant (Go orchestrator)
        ├── whisper-cli (STT, CPU)
        └── piper (TTS, CLI mode)
```

## Prerequisites

- Docker and Docker Compose
- Host with microphone and speaker
- llama.cpp server running on `host.docker.internal:8080` (listening on
  all interfaces, e.g. `--host 0.0.0.0`)

## Quick Start

```bash
# Build the image
docker compose build

# Download models (as root, since the script writes to the ./models volume;
# if ./models is owned by your user and this fails with Permission denied,
# use --user "$(id -u):$(id -g)" instead - see "Model not found" below)
docker compose run --rm --user 0 voice-assistant /app/install_models.sh

# Start the assistant
docker compose up -d

# View logs
docker compose logs -f

# Stop
docker compose down
```

## Manual Start Script

```bash
./start.sh
```

## Configuration

Environment variables (all optional, see AGENTS.md for the full table):

- `WHISPER_MODEL`: Path to whisper model (default: `/models/whisper/ggml-small.en-q5_1.bin`)
- `WHISPER_THREADS`: CPU threads for whisper-cli's `-t` flag (default: unset = whisper-cli's default of 4)
- `WHISPER_VAD_MODEL`: whisper.cpp Silero VAD model for `--vad` (default: `/models/whisper/ggml-silero-v5.1.2.bin`; a missing file disables the flag)
- `PIPER_MODEL`: Path to piper model (default: `/models/piper/en_US-ryan-high.onnx`)
- `LLM_ENDPOINT`: LLM API endpoint (default: `http://host.docker.internal:8080` - bridge network with host-gateway alias; the host server must listen on the bridge gateway, e.g. `--host 0.0.0.0`)
- `LLM_MODEL`: Model name sent with every request (required for llama.cpp router mode, e.g. `--models-dir`; default: empty)
- `LLM_SYSTEM_PROMPT`: System prompt for chat completions (default: short voice-assistant prompt)
- `LLM_TIMEOUT`: LLM request timeout (default: `30s`)
- `LLM_DISABLE_REASONING`: Disable thinking/reasoning mode via `chat_template_kwargs` `{"enable_thinking": false}` (honored by Qwen3-style templates; a thinking model otherwise burns the whole token budget and returns an empty answer) (default: on; set `false` to allow reasoning)
- `LLM_MAX_TOKENS`: Token budget per reply (default: `512`; lower, e.g. `100`, to cap latency once reasoning is disabled)
- `CAPTURE_MODE`: `vad` (voice-activated capture, default) or `fixed` (fixed window)
- `VAD_THRESHOLD`: sox amplitude threshold in percent that starts the recording (default: `10`; tune for your mic/room)
- `VAD_STOP_THRESHOLD`: sox amplitude threshold in percent that resets the end-of-speech counter (default: unset = same as `VAD_THRESHOLD`). In a noisy room set it above the noise peaks so blips cannot hold the recording open
- `VAD_START_MS`: sound duration that starts the recording (default: `100`)
- `VAD_SILENCE_SEC`: quiet duration that ends the utterance (default: `2.0`; lower = snappier but cuts off thinking pauses)
- `VAD_MAX_UTTERANCE_SEC`: hard cap on one utterance (default: `30`)
- `VAD_MIN_SPEECH_MS`: captures shorter than this are skipped before STT (default: `500`)
- `SESSION_TIMEOUT_SEC`: seconds of silence before the conversation history auto-resets, checked when the next capture ends; `0` disables (default: `60`)
- `SESSION_MAX_MESSAGES`: max messages kept as conversation context; must be even (default: `10`)
- `SESSION_RESET_PHRASE`: utterance that resets the conversation, matched case/punctuation-insensitively with room for a couple of padding words (default: `new voice assistant session`)
- `CAPTURE_SECONDS`: fixed capture window in seconds for `CAPTURE_MODE=fixed` (default: `5`)
- `DEBUG`: Enable debug logging (default: `false`)
- `AUDIO_GID`: Host audio group GID for `/dev/snd` access as the non-root container user (default: `29`)

### Changing the LLM endpoint

The llama.cpp endpoint can be overridden without editing any files, either
via a host environment variable:

```bash
LLM_ENDPOINT=http://192.168.1.50:8080 docker compose up
```

or via a `.env` file in the project directory (Docker Compose loads it
automatically):

```bash
cp .env.example .env
# edit LLM_ENDPOINT in .env, then:
docker compose up
```

## Models

| Model | Size | Description |
|-------|------|-------------|
| whisper small.en-q5_1 | 190MB | Quantized English STT model (far better accuracy than tiny.en) |
| whisper silero VAD | 0.9MB | Silero VAD in ggml format; whisper.cpp drops non-speech segments before transcription |
| piper en_US-ryan-high | 120MB | Highest-quality male English TTS voice (+ .onnx.json config) |

## Performance Targets

- **Total latency**: utterance length + end-of-speech wait (`VAD_SILENCE_SEC`, default ~2s) + 3-4s processing
- **Audio capture**: voice-activated; waiting for speech is free (sox blocks on the mic), no whisper inference burned on silence
- **Whisper STT**: <1500ms (4 threads by default; raise `WHISPER_THREADS` to cut this)
- **LLM inference**: <1500ms (via llama.cpp)
- **Piper TTS**: <1000ms

Every turn logs a per-stage breakdown (`STT`, `LLM first token/total`,
`TTS`, `Playback`) plus **`First audio: X after capture end`** — the
latency you actually perceive on top of the end-of-speech wait. If a
turn feels slow, that line shows which stage to tune.

## Development

### Build locally

```bash
sudo apt install sox libsox-fmt-alsa alsa-utils
go build -o voice-assistant orchestrator.go
WHISPER_BIN=/path/to/whisper-cli PIPER_BIN=/path/to/piper \
ESPEAK_DATA=/path/to/espeak-ng-data ./voice-assistant
```

### Rebuild Docker

```bash
docker compose build
```

Rebuilds use the build cache — unchanged steps (including the git
clones) are skipped, so a rebuild with no Dockerfile changes takes under
a second. To force a full rebuild that re-downloads everything (e.g.
when the cache is corrupt), use `docker compose build --no-cache`.

## Troubleshooting

### No audio device found

Check that ALSA devices are visible inside the container:
```bash
docker compose run --rm voice-assistant aplay -l
```

### Voice-activated capture misbehaves

Test the sox silence-effect chain directly — it should wait for speech,
stop ~2s after you stop talking, and print the recorded duration:
```bash
docker compose run --rm voice-assistant bash -c 'sox -d -r 16000 -c 1 -b 16 -t wav /tmp/vad.wav silence 1 0.1 3% 1 2.0 3% trim 0 10 && soxi -D /tmp/vad.wav'
```

If recording starts on background noise, or never starts on speech, tune
`VAD_THRESHOLD` in `.env` (higher = less sensitive). If recordings keep
running for seconds after you stop talking, background noise is resetting
the end-of-speech counter: measure the noise floor and set
`VAD_STOP_THRESHOLD` above its peaks:

```bash
docker compose run --rm voice-assistant bash -c 'sox -d -r 16000 -c 1 -b 16 /tmp/noise.wav trim 0 5 && sox /tmp/noise.wav -n stat'
```

The `Maximum amplitude` line (0-1 scale) times 100 is the noise peak in
percent; set `VAD_STOP_THRESHOLD` comfortably above it while
`VAD_THRESHOLD` stays low enough for quiet speech to start the recording.
`CAPTURE_MODE=fixed` restores the old fixed-window behavior while you
experiment.

### Model not found

Run the install script (as root, since it writes to the `./models` volume):
```bash
docker compose run --rm --user 0 voice-assistant /app/install_models.sh
```

The container drops all Linux capabilities (`cap_drop: ALL` in
docker-compose.yml), so even the root install can only write where uid 0
already owns the path. On a fresh host Docker creates `./models` root-owned
and this just works; if you created `./models` yourself and the run fails
with `Permission denied`, install as your own uid instead:
```bash
docker compose run --rm --user "$(id -u):$(id -g)" voice-assistant /app/install_models.sh
```

### LLM connection failed

Ensure llama.cpp is running on port 8080 and listening on all interfaces
(`--host 0.0.0.0`) so the bridge-network container can reach it — or
whatever `LLM_ENDPOINT` points to:

```bash
docker run -p 8080:8080 -v /path/to/model:/model ggerganov/llama.cpp:server -m /model/gguf
```

If the server runs in router mode (`--models-dir`/`--model-presets`) you'll
get `400: model name is missing from the request` — set `LLM_MODEL` in
`.env` to one of the names from `/v1/models`.

## Licenses

**This repository's own code** (orchestrator.go, scripts, Dockerfile, compose
files) is MIT — see [LICENSE](LICENSE). The built image bundles third-party
components that remain under their own licenses: texts are in
[LICENSES/](LICENSES), versions and pinned source URLs in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). Both are shipped inside the
image at `/usr/share/licenses/voice-assistant/`.

### Components built from source

| Component | License | Text |
|-----------|---------|------|
| whisper.cpp v1.9.4 (whisper-cli) | MIT | [LICENSE.MIT-whisper](LICENSES/LICENSE.MIT-whisper) |
| piper1-gpl v1.8.0 (piper, libpiper) | GPL-3.0 | [LICENSE.GPL-3.0](LICENSES/LICENSE.GPL-3.0) |
| espeak-ng (phonemization, linked into libpiper; espeak-ng-data) | GPL-3.0 | [LICENSE.GPL-3.0](LICENSES/LICENSE.GPL-3.0) |
| onnxruntime (libonnxruntime.so) | MIT | [LICENSE.MIT-onnxruntime](LICENSES/LICENSE.MIT-onnxruntime) |

### Debian bookworm packages (apt)

| Package | License | Text |
|---------|---------|------|
| sox, libsox-fmt-alsa | GPL-2.0-or-later (CLI); LGPL-2.1-or-later (libsox) | [LICENSE.GPL-2.0](LICENSES/LICENSE.GPL-2.0), [LICENSE.LGPL-2.1](LICENSES/LICENSE.LGPL-2.1) |
| alsa-utils (aplay, arecord) | GPL-2.0-or-later | [LICENSE.GPL-2.0](LICENSES/LICENSE.GPL-2.0) |
| libasound2, libpulse0 | LGPL-2.1-or-later | [LICENSE.LGPL-2.1](LICENSES/LICENSE.LGPL-2.1) |
| wget | GPL-3.0-or-later | [LICENSE.GPL-3.0](LICENSES/LICENSE.GPL-3.0) |
| curl | curl license (MIT-style) | in image: `/usr/share/doc/curl/copyright` |
| libgomp1, libstdc++6 | GPL-3.0+ with GCC runtime library exception | in image: `/usr/share/doc/*/copyright` |

Debian keeps the license texts for its packages inside the image
(`/usr/share/common-licenses/`, `/usr/share/doc/<pkg>/copyright`). The Go
toolchain (BSD-3-Clause) is used in the builder stage only and is not part of
the runtime image.

### Models (downloaded by install_models.sh, not redistributed here)

- **Whisper small.en-q5_1** (ggerganov/whisper.cpp): MIT — "Whisper's code and
  model weights are released under the MIT License" (openai/whisper)
- **Piper en_US-ryan-high** (rhasspy/piper-voices): MIT

### Distribution notes

The orchestrator interacts with the GPL binaries at arm's length (exec +
stdin/files), so the MIT license on this repository's code stands. If you
**distribute the built image**, GPLv3 requires the license text and
corresponding source (or a written offer) to accompany the GPL components —
the license texts ride along in the image, and the pinned source URLs in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) identify the exact
corresponding sources.
