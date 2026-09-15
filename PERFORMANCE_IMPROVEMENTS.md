# Voice Assistant - Performance Improvements

## Current State

- **Total latency target**: 2-3 seconds
- **Current bottleneck**: LLM inference (1-2 seconds)
- **CPU**: 4 cores (can leverage multi-threading)
- **User**: Single user (sequential processing)
- **Quality**: Preferred over speed

---

## ALSA Overview

**ALSA (Advanced Linux Sound Architecture)** is Linux's kernel-level audio framework:
- Direct access to audio hardware (sound cards, microphones, speakers)
- Low-level audio I/O via `arecord` (capture) and `aplay` (playback)
- Device passthrough via `/dev/snd` in Docker

Currently used via:
```bash
arecord -D default -f cd -t raw -d 5 -  # Capture
aplay -q /tmp/output.wav                 # Playback
```

---

## Optimization Opportunities

### 1. Audio Capture Optimization
**Current**: `arecord → ffmpeg` (two-process pipeline)
**Issue**: Process chain overhead

**Improvement**:
```bash
# Use sox single command
sox -d -r 16000 -c 1 -b 16 output.wav trim 0 5
```

**Benefits**:
- Reduce process chain from 2 → 1
- Estimated gain: **~50-100ms**

**Code Change** (`orchestrator.go`):
```go
// Replace arecord+ffmpeg pipeline with sox
cmd := exec.Command("sox", "-d", "-r", "16000", "-c", "1", "-b", "16",
    "-t", "wav", tmpFile, "trim", "0", "5")
```

---

### 2. Whisper Configuration
**Current**: Default settings (likely single-threaded)

**Improvement**:
```bash
./whisper-cli -m model.bin -f audio.wav -otxt -pc -t 4
#                                          ^^^^ add threads
```

**Benefits**:
- Use all 4 CPU cores
- Estimated gain: **~100-200ms**

**Configuration** (`orchestrator.go`):
```go
cmd := exec.Command(
    "/app/whisper-cli",
    "-m", config.WhisperModel,
    "-f", tmpFile,
    "-otxt",
    "-pc",
    "-t", "4",  // Add: use 4 threads
)
```

**Model Options**:
- `ggml-tiny.en.bin` (75MB) - current, fast but less accurate
- `ggml-base.en.bin` (146MB) - better accuracy, moderate speed
- `ggml-medium.en.bin` (388MB) - best accuracy, slower (~2x)
- `ggml-tiny.en-q5_0.bin` (75MB quantized) - same size, slightly faster

---

### 3. LLM Request Optimization
**Current**: Basic `http.Post` without timeout

**Improvement**:
```go
client := &http.Client{
    Timeout: 2 * time.Second,  // Prevent hangs
    Transport: &http.Transport{
        MaxIdleConns: 10,        // Connection pooling
    },
}
```

**Benefits**:
- Prevents stuck requests
- Better resource management
- Estimated gain: **stability improvement**

---

### 4. Piper TTS Optimization
**Current**: CLI mode with file I/O (write → read → delete)

**Improvement 1: HTTP Server Mode**
```bash
# Start Piper as HTTP server (already in entrypoint.sh)
/app/piper --model model.onnx --port 5000 --length-scale 1.0

# Use HTTP instead of CLI
curl -X POST http://localhost:5000/api/tts -d '{"text":"hello"}'
```

**Improvement 2: stdin/stdout streaming**
```bash
echo "hello" | /app/piper --model model.onnx --output-file -
```

**Benefits**:
- Avoid file I/O overhead
- Estimated gain: **~100-150ms**

---

### 5. In-Memory Streaming
**Current**: Write to `/tmp/` between stages

**Improvement**:
```go
// Stream WAV data via pipe
reader, writer := io.Pipe()
go func() {
    cmd1.Stdout = writer
    cmd1.Run()
    writer.Close()
}()

cmd2.Stdin = reader
```

**Benefits**:
- No disk I/O between stages
- Estimated gain: **~50-100ms**

---

### 6. Pipeline Overlap (Concurrent Processing)
**Current**: Strictly sequential
```
[T0] Capture ──► STT ──► LLM ──► TTS ──► Playback
[T1]                                    [same chain starts]
```

**Improvement**: Overlap stages
```
[T0] Capture ──► STT ──► LLM ──► TTS ──► Playback
[T1]          [Capture]──► [STT] ──► [LLM] ──► [TTS]
```

**Implementation**:
```go
// Buffer last response while capturing next input
var lastResponse []byte
var responseReady = make(chan bool)

go func() {
    for {
        wavData, _ := captureAudio()
        text, _ := sttWithWhisper(wavData)
        
        // Start capturing next while processing current
        go func() { 
            wavData2, _ := captureAudio()
            // ... pipeline continues
        }()
        
        // Process current
        response := callLLM(text)
        audio := ttsWithPiper(response)
        playAudio(audio)
    }
}()
```

**Benefits**:
- While user listens, system is already listening for next input
- Estimated gain: **~200-300ms perceived latency reduction**

---

## Priority Implementation Plan

### Phase 1: Quick Wins (0-1 day) - **HIGH PRIORITY**
| Task | Effort | Impact | Status |
|------|--------|--------|--------|
| Add `-t 4` to whisper | 2 min | +100ms | [ ] |
| Add HTTP timeout to LLM | 5 min | Stability | [ ] |
| Use sox instead of arecord+ffmpeg | 30 min | +50ms | [ ] |
| Add model existence check at startup | 10 min | UX | [ ] |

### Phase 2: Model Upgrade (1-2 days) - **MEDIUM PRIORITY**
| Task | Effort | Impact | Status |
|------|--------|--------|--------|
| Download medium.en model | 10 min | Quality | [ ] |
| Update whisper config | 5 min | +100-200ms latency | [ ] |
| Test accuracy vs tiny.en | 30 min | Validation | [ ] |

### Phase 3: Piper Streaming (2-3 days) - **MEDIUM PRIORITY**
| Task | Effort | Impact | Status |
|------|--------|--------|--------|
| Switch to Piper HTTP server mode | 1 hour | +100-150ms | [ ] |
| Stream text directly (no file I/O) | 2 hours | +50ms | [ ] |
| Test audio quality | 30 min | Validation | [ ] |

### Phase 4: Pipeline Overlap (3-5 days) - **MEDIUM PRIORITY**
| Task | Effort | Impact | Status |
|------|--------|--------|--------|
| Buffer last response while capturing next | 2 hours | -200ms perceived | [ ] |
| Async LLM calls | 4 hours | Better GPU utilization | [ ] |
| Test with real usage | 1 hour | Validation | [ ] |

---

## Testing Strategy

### Metrics to Track
1. **Total latency**: End-to-end from speech start to audio playback end
2. **Component latency**: STT, LLM, TTS, I/O separately
3. **CPU utilization**: Ensure all 4 cores are being used
4. **Memory usage**: Monitor for leaks

### Benchmark Script
```bash
#!/bin/bash
# benchmark.sh
echo "Starting benchmark..."
start=$(date +%s.%N)

# Run voice assistant test sequence
# - Speak phrase
# - Measure time until playback ends

end=$(date +%s.%N)
echo "Total time: $(echo "$end - $start" | bc)s"
```

### Quality Testing
- Use same audio clips for each model
- Compare transcription accuracy
- Evaluate TTS naturalness
- User testing with real scenarios

---

## Risk Assessment

| Optimization | Risk | Mitigation |
|--------------|------|------------|
| Multi-threaded whisper | Low | Already tested, stable |
| HTTP server mode | Medium | Test audio quality |
| In-memory streaming | High | Complex, test thoroughly |
| Pipeline overlap | High | Changes architecture, test edge cases |
| Model upgrade | Low | Larger disk space, better quality |

---

## Success Criteria

- **Target**: Reduce total latency from ~3s to <2s
- **Quality**: Maintain or improve transcript accuracy
- **Stability**: No hangs, memory leaks, or crashes
- **User experience**: Noticeably faster response

---

## References

- whisper.cpp options: https://github.com/ggml-org/whisper.cpp
- piper documentation: https://github.com/rhasspy/piper
- ALSA documentation: https://www.alsa-project.org/
- Docker audio passthrough: https://docs.docker.com/
