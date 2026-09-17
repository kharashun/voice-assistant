# Third-Party Notices

This project incorporates third-party software; each component remains under
its own license. The project's own code is MIT (see `LICENSE`). License texts
are in `LICENSES/` (repository root, or `/usr/share/licenses/voice-assistant/`
inside the built image, alongside this file).

## Components built from source (pinned versions)

| Component | Version | License | Source |
|-----------|---------|---------|--------|
| whisper.cpp (whisper-cli) | v1.9.4 | MIT - Copyright (c) 2023-2026 The ggml authors | https://github.com/ggml-org/whisper.cpp/tree/v1.9.4 |
| piper1-gpl (piper CLI, libpiper) | v1.8.0 | GPL-3.0 | https://github.com/OHF-Voice/piper1-gpl/tree/v1.8.0 |
| espeak-ng (phonemization, statically linked into libpiper; espeak-ng-data ships in the image) | as fetched by the piper1-gpl v1.8.0 build | GPL-3.0 | https://github.com/espeak-ng/espeak-ng |
| onnxruntime (libonnxruntime.so, piper inference) | prebuilt, as fetched by the piper1-gpl v1.8.0 build | MIT - Copyright (c) Microsoft Corporation | https://github.com/microsoft/onnxruntime |

## Debian bookworm packages (installed via apt in the runtime image)

| Package | License |
|---------|---------|
| sox, libsox-fmt-alsa | GPL-2.0-or-later (CLI); LGPL-2.1-or-later (libsox) |
| alsa-utils (aplay, arecord) | GPL-2.0-or-later |
| libasound2 (alsa-lib) | LGPL-2.1-or-later |
| libpulse0 | LGPL-2.1-or-later |
| wget | GPL-3.0-or-later |
| curl | curl license (MIT-style) |
| libgomp1, libstdc++6 | GPL-3.0-or-later with GCC runtime library exception |
| debian:bookworm-slim base image | various; per-package copyright files |

Debian keeps the license texts for these packages inside the image
(`/usr/share/common-licenses/` and `/usr/share/doc/<package>/copyright`).

## Build toolchain (builder stage only; not in the runtime image)

- Go (golang:1.23-bookworm): BSD-3-Clause - https://go.dev/LICENSE
- gcc, cmake, ninja, git, wget, curl, python3: GPL / Apache / MIT as
  distributed by Debian (builder stage only, not conveyed by the image)

## Models (downloaded by install_models.sh at runtime; not part of the image)

- Whisper small.en-q5_1 (ggerganov/whisper.cpp): MIT - "Whisper's code and
  model weights are released under the MIT License" (openai/whisper)
- Piper voice en_US-ryan-high (rhasspy/piper-voices): MIT
  (https://huggingface.co/rhasspy/piper-voices)

## Distribution

If you convey the built Docker image, you must comply with the GPL for the
GPL-licensed components (piper/libpiper, espeak-ng, sox, alsa-utils, wget):
the license text and the Corresponding Source (or a written offer, GPLv3
section 6) must accompany the distribution. The pinned versions and source
URLs above, together with the public Dockerfile, identify the exact
Corresponding Source for every component built from source.
