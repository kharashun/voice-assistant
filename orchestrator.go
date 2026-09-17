package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type LLMRequest struct {
	Model              string          `json:"model,omitempty"`
	Messages           []ChatMessage   `json:"messages"`
	MaxTokens          int             `json:"max_tokens"`
	Temperature        float64         `json:"temperature"`
	ChatTemplateKwargs map[string]bool `json:"chat_template_kwargs,omitempty"`
}

type LLMResponse struct {
	Choices []struct {
		Message struct {
			Role             string `json:"role"`
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

type Config struct {
	WhisperBin          string
	WhisperModel        string
	WhisperThreads      int
	WhisperVadModel     string
	PiperBin            string
	PiperModel          string
	EspeakData          string
	LLMEndpoint         string
	LLMModel            string
	LLMSystemPrompt     string
	LLMTimeout          time.Duration
	LLMDisableReasoning bool
	LLMMaxTokens        int
	CaptureMode         string  // "vad" (silence-triggered) or "fixed" (fixed window)
	CaptureSeconds      int     // fixed mode: window length in seconds
	VadThreshold        float64 // sox amplitude threshold in percent
	VadStartMs          int     // sound duration that starts the recording
	VadSilenceSec       float64 // quiet duration that ends the recording
	VadMaxUtteranceSec  int     // hard cap on utterance length
	VadMinSpeechMs      int     // captures shorter than this are skipped before STT
	Debug               bool
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadConfig() *Config {
	llmTimeout, err := time.ParseDuration(getenv("LLM_TIMEOUT", "30s"))
	if err != nil || llmTimeout <= 0 {
		llmTimeout = 30 * time.Second
	}

	captureSeconds, err := strconv.Atoi(getenv("CAPTURE_SECONDS", "5"))
	if err != nil || captureSeconds <= 0 {
		captureSeconds = 5
	}

	// 0 means unset: whisper-cli then uses its own default of 4 threads.
	whisperThreads, err := strconv.Atoi(getenv("WHISPER_THREADS", "0"))
	if err != nil || whisperThreads < 0 {
		whisperThreads = 0
	}

	llmMaxTokens, err := strconv.Atoi(getenv("LLM_MAX_TOKENS", "512"))
	if err != nil || llmMaxTokens <= 0 {
		llmMaxTokens = 512
	}

	// On unless explicitly set to "false".
	llmDisableReasoning := os.Getenv("LLM_DISABLE_REASONING") != "false"

	captureMode := getenv("CAPTURE_MODE", "vad")
	if captureMode != "vad" && captureMode != "fixed" {
		captureMode = "vad"
	}

	vadThreshold, err := strconv.ParseFloat(getenv("VAD_THRESHOLD", "10"), 64)
	if err != nil || vadThreshold <= 0 || vadThreshold > 100 {
		vadThreshold = 10
	}

	vadStartMs, err := strconv.Atoi(getenv("VAD_START_MS", "100"))
	if err != nil || vadStartMs <= 0 {
		vadStartMs = 100
	}

	vadSilenceSec, err := strconv.ParseFloat(getenv("VAD_SILENCE_SEC", "2.0"), 64)
	if err != nil || vadSilenceSec <= 0 {
		vadSilenceSec = 2.0
	}

	vadMaxUtteranceSec, err := strconv.Atoi(getenv("VAD_MAX_UTTERANCE_SEC", "30"))
	if err != nil || vadMaxUtteranceSec <= 0 {
		vadMaxUtteranceSec = 30
	}

	vadMinSpeechMs, err := strconv.Atoi(getenv("VAD_MIN_SPEECH_MS", "500"))
	if err != nil || vadMinSpeechMs < 0 {
		vadMinSpeechMs = 500
	}

	return &Config{
		WhisperBin:          getenv("WHISPER_BIN", "/app/whisper-cli"),
		WhisperModel:        getenv("WHISPER_MODEL", "/models/whisper/ggml-small.en-q5_1.bin"),
		WhisperThreads:      whisperThreads,
		WhisperVadModel:     getenv("WHISPER_VAD_MODEL", "/models/whisper/ggml-silero-v5.1.2.bin"),
		PiperBin:            getenv("PIPER_BIN", "/app/piper"),
		PiperModel:          getenv("PIPER_MODEL", "/models/piper/en_US-ryan-high.onnx"),
		EspeakData:          getenv("ESPEAK_DATA", "/opt/espeak-ng-data"),
		LLMEndpoint:         strings.TrimRight(getenv("LLM_ENDPOINT", "http://127.0.0.1:8080"), "/"),
		LLMModel:            getenv("LLM_MODEL", ""),
		LLMSystemPrompt:     getenv("LLM_SYSTEM_PROMPT", "You are a voice assistant. Reply in one or two short sentences."),
		LLMTimeout:          llmTimeout,
		LLMDisableReasoning: llmDisableReasoning,
		LLMMaxTokens:        llmMaxTokens,
		CaptureMode:         captureMode,
		CaptureSeconds:      captureSeconds,
		VadThreshold:        vadThreshold,
		VadStartMs:          vadStartMs,
		VadSilenceSec:       vadSilenceSec,
		VadMaxUtteranceSec:  vadMaxUtteranceSec,
		VadMinSpeechMs:      vadMinSpeechMs,
		Debug:               os.Getenv("DEBUG") == "true",
	}
}

func debugLog(msg string) {
	if os.Getenv("DEBUG") == "true" {
		log.Println("[DEBUG]", msg)
	}
}

// soxDuration formats seconds for sox effect parameters. A bare integer
// like "2" is parsed as a sample count by sox, so always carry a decimal
// point ("2.0").
func soxDuration(seconds float64) string {
	s := strconv.FormatFloat(seconds, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func captureAudio(config *Config) ([]byte, error) {
	// Use sox to capture from the ALSA default device directly as 16kHz WAV
	tmpFile := fmt.Sprintf("/tmp/%d.wav", time.Now().UnixNano())
	defer os.Remove(tmpFile)

	args := []string{
		"-d",          // default audio device
		"-r", "16000", // 16kHz sample rate
		"-c", "1", // mono
		"-b", "16", // 16-bit
		"-t", "wav", // WAV format
		tmpFile, // output file
	}
	if config.CaptureMode == "vad" {
		// Voice-activated capture via the sox `silence` effect:
		// - discard audio until VadStartMs of sound above the threshold
		//   starts the recording (the wait for speech is unbounded and
		//   free - sox just blocks on the mic),
		// - stop the recording after VadSilenceSec of quiet below it.
		// `trim` after `silence` caps the utterance length so constant
		// noise cannot keep the recording running forever.
		threshold := fmt.Sprintf("%g%%", config.VadThreshold)
		args = append(args,
			"silence",
			"1", soxDuration(float64(config.VadStartMs)/1000), threshold,
			"1", soxDuration(config.VadSilenceSec), threshold,
			"trim", "0", soxDuration(float64(config.VadMaxUtteranceSec)),
		)
	} else {
		// Fixed window: record a fixed number of seconds.
		args = append(args, "trim", "0", soxDuration(float64(config.CaptureSeconds)))
	}

	cmd := exec.Command("sox", args...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to capture audio: %w, output: %s", err, string(output))
	}

	wavData, err := os.ReadFile(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read audio file: %w", err)
	}

	debugLog(fmt.Sprintf("Captured %d bytes of audio", len(wavData)))

	return wavData, nil
}

// wavDataSize returns the byte size of the data chunk of a RIFF/WAVE
// file. sox may write extra chunks (e.g. LIST) before it, so walk the
// chunk list instead of assuming the canonical 44-byte header.
func wavDataSize(wav []byte) (uint32, error) {
	if len(wav) < 12 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return 0, fmt.Errorf("not a RIFF/WAVE file")
	}
	off := uint32(12)
	for off+8 <= uint32(len(wav)) {
		id := string(wav[off : off+4])
		size := binary.LittleEndian.Uint32(wav[off+4 : off+8])
		if id == "data" {
			return size, nil
		}
		// Chunks are word-aligned: odd sizes carry one pad byte.
		off += 8 + size + (size & 1)
	}
	return 0, fmt.Errorf("no data chunk found")
}

// enoughSpeech reports whether the captured WAV holds at least
// VadMinSpeechMs of audio. In vad mode a shorter capture means a
// transient (cough, keyboard clack) triggered the recording but no
// speech followed - it is skipped before whisper runs.
func enoughSpeech(wav []byte, config *Config) bool {
	dataSize, err := wavDataSize(wav)
	if err != nil {
		// Unparseable header: let whisper decide instead of dropping
		// the turn outright.
		log.Printf("Could not parse WAV header: %v", err)
		return true
	}
	// 16000 samples/s * 2 bytes/sample / 1000 = bytes per millisecond.
	ms := dataSize / 32
	if ms < uint32(config.VadMinSpeechMs) {
		debugLog(fmt.Sprintf("Capture too short: %dms < %dms", ms, config.VadMinSpeechMs))
		return false
	}
	return true
}

// nonSpeechTagRe matches bracketed tags whisper emits instead of a
// transcript on non-speech input ([BLANK_AUDIO], [MUSIC], [SOUND],
// [typing], ...). They must never reach the LLM.
var nonSpeechTagRe = regexp.MustCompile(`(?i)\[[^\]]*\]`)

// stripNonSpeechTags removes bracketed non-speech tags and trims the
// remaining text.
func stripNonSpeechTags(text string) string {
	return strings.TrimSpace(nonSpeechTagRe.ReplaceAllString(text, ""))
}

func sttWithWhisper(wavData []byte, config *Config) (string, error) {
	// whisper-cli writes the transcript to <base>.txt when given
	// `-of <base> -otxt`; we read that file instead of parsing the
	// binary's log output (which contains banners, timings and colors).
	tmpBase := fmt.Sprintf("/tmp/%d", time.Now().UnixNano())
	wavFile := tmpBase + ".wav"
	txtFile := tmpBase + ".txt"

	err := os.WriteFile(wavFile, wavData, 0644)
	if err != nil {
		return "", fmt.Errorf("failed to write temp file: %w", err)
	}
	defer os.Remove(wavFile)
	defer os.Remove(txtFile)

	cmd := exec.Command(
		config.WhisperBin,
		"-m", config.WhisperModel,
		"-f", wavFile,
		"-of", tmpBase,
		"-otxt",
	)
	if config.WhisperThreads > 0 {
		cmd.Args = append(cmd.Args, "-t", strconv.Itoa(config.WhisperThreads))
	}
	// whisper.cpp ships a built-in Silero VAD: with --vad it drops
	// non-speech segments before whisper_full, which avoids hallucinated
	// non-speech tags on background noise and speeds up inference.
	// Only enabled when the model file actually exists.
	if config.WhisperVadModel != "" {
		if _, err := os.Stat(config.WhisperVadModel); err == nil {
			cmd.Args = append(cmd.Args, "--vad", "--vad-model", config.WhisperVadModel)
		} else {
			debugLog(fmt.Sprintf("Whisper VAD model not found at %s, running without --vad", config.WhisperVadModel))
		}
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("whisper failed: %w, output: %s", err, string(output))
	}

	raw, err := os.ReadFile(txtFile)
	if err != nil {
		return "", fmt.Errorf("failed to read transcript file: %w", err)
	}

	text := strings.TrimSpace(string(raw))
	debugLog(fmt.Sprintf("STT result: %s", text))

	return text, nil
}

func callLLM(prompt string, config *Config) (string, error) {
	messages := make([]ChatMessage, 0, 2)
	if config.LLMSystemPrompt != "" {
		messages = append(messages, ChatMessage{Role: "system", Content: config.LLMSystemPrompt})
	}
	messages = append(messages, ChatMessage{Role: "user", Content: prompt})

	req := LLMRequest{
		Model:       config.LLMModel,
		Messages:    messages,
		MaxTokens:   config.LLMMaxTokens,
		Temperature: 0.7,
	}
	// llama.cpp has no "reasoning" request field; Qwen3-style chat
	// templates disable thinking via the enable_thinking kwarg.
	if config.LLMDisableReasoning {
		req.ChatTemplateKwargs = map[string]bool{"enable_thinking": false}
	}

	jsonData, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	client := &http.Client{Timeout: config.LLMTimeout}
	resp, err := client.Post(
		config.LLMEndpoint+"/v1/chat/completions",
		"application/json",
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		return "", fmt.Errorf("failed to call LLM: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read LLM response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("LLM returned status %d: %s", resp.StatusCode, string(body))
	}

	var llmResp LLMResponse
	err = json.Unmarshal(body, &llmResp)
	if err != nil {
		return "", fmt.Errorf("failed to parse LLM response: %w, body: %s", err, string(body))
	}
	if len(llmResp.Choices) == 0 {
		return "", fmt.Errorf("LLM response contains no choices, body: %s", string(body))
	}

	content := llmResp.Choices[0].Message.Content
	if strings.TrimSpace(content) == "" {
		log.Printf("LLM returned empty content (finish_reason=%s, reasoning_content=%d chars); thinking may have consumed the token budget - raise LLM_MAX_TOKENS or keep reasoning disabled",
			llmResp.Choices[0].FinishReason, len(llmResp.Choices[0].Message.ReasoningContent))
	}
	debugLog(fmt.Sprintf("LLM response: %s", content))

	return content, nil
}

func ttsWithPiper(text string, config *Config) ([]byte, error) {
	// piper reads the text to synthesize from stdin and writes a WAV
	// file at the voice's native sample rate (no --input-text/--sample-rate
	// options exist in the piper CLI).
	tmpFile := fmt.Sprintf("/tmp/%d.wav", time.Now().UnixNano())
	defer os.Remove(tmpFile)

	args := []string{
		"--model", config.PiperModel,
		"--output-file", tmpFile,
	}
	if config.EspeakData != "" {
		args = append(args, "--espeak-data", config.EspeakData)
	}

	cmd := exec.Command(config.PiperBin, args...)
	cmd.Stdin = strings.NewReader(text + "\n")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("piper failed: %w, output: %s", err, string(output))
	}

	audioData, err := os.ReadFile(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read audio file: %w", err)
	}

	debugLog(fmt.Sprintf("Generated %d bytes of audio", len(audioData)))

	return audioData, nil
}

func playAudio(audioData []byte) error {
	// Write to temp file
	tmpFile := fmt.Sprintf("/tmp/%d.wav", time.Now().UnixNano())
	err := os.WriteFile(tmpFile, audioData, 0644)
	if err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	defer os.Remove(tmpFile)

	// Play using aplay (ALSA player)
	cmd := exec.Command("aplay", "-q", tmpFile)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to play audio: %w, output: %s", err, string(output))
	}

	return nil
}

func main() {
	config := loadConfig()

	log.Println("Voice Assistant starting...")
	log.Printf("Whisper model: %s", config.WhisperModel)
	if config.WhisperThreads > 0 {
		log.Printf("Whisper threads: %d", config.WhisperThreads)
	}
	if config.WhisperVadModel != "" {
		if _, err := os.Stat(config.WhisperVadModel); err == nil {
			log.Printf("Whisper VAD model: %s", config.WhisperVadModel)
		} else {
			log.Printf("Whisper VAD model missing (%s); STT runs without --vad", config.WhisperVadModel)
		}
	}
	log.Printf("Piper model: %s", config.PiperModel)
	log.Printf("LLM endpoint: %s", config.LLMEndpoint)
	if config.LLMModel != "" {
		log.Printf("LLM model: %s", config.LLMModel)
	}
	if config.LLMDisableReasoning {
		log.Println("LLM thinking disabled (enable_thinking=false)")
	}
	if config.CaptureMode == "vad" {
		log.Printf("Capture: voice-activated (threshold %g%%, stop after %ss of silence, max utterance %ds)",
			config.VadThreshold, soxDuration(config.VadSilenceSec), config.VadMaxUtteranceSec)
	} else {
		log.Printf("Capture: fixed %ds window", config.CaptureSeconds)
	}

	fmt.Println("Voice Assistant ready! Press Ctrl+C to exit.")
	fmt.Println()

	for {
		if config.CaptureMode == "vad" {
			fmt.Println("Listening... (speak, then pause)")
		} else {
			fmt.Printf("Listening... (%ds window)\n", config.CaptureSeconds)
		}

		// Measure end-to-end, including the capture window.
		startTime := time.Now()

		wavData, err := captureAudio(config)
		if err != nil {
			log.Printf("Failed to capture audio: %v", err)
			// Back off so a persistently unavailable mic (busy device,
			// hot-unplug) cannot spin this loop hot.
			time.Sleep(time.Second)
			continue
		}
		captureTime := time.Since(startTime)

		// In vad mode a too-short capture is a transient that started
		// the recording but contains no speech - skip STT entirely.
		if config.CaptureMode == "vad" && !enoughSpeech(wavData, config) {
			fmt.Println("No speech detected (capture too short). Listening again...")
			continue
		}

		fmt.Println("Processing...")

		text, err := sttWithWhisper(wavData, config)
		if err != nil {
			log.Printf("STT failed: %v", err)
			continue
		}

		fmt.Printf("You said: %s\n", text)

		// whisper.cpp writes "[BLANK_AUDIO]" when the capture window
		// contains only silence.
		if t := strings.TrimSpace(text); t == "" || t == "[BLANK_AUDIO]" {
			fmt.Println("No speech detected. Listening again...")
			continue
		}

		// whisper also emits bracketed non-speech tags ([typing],
		// [SOUND], ...) on background noise; strip them so they never
		// reach the LLM, and skip the turn if nothing real remains.
		if cleaned := stripNonSpeechTags(text); cleaned != text {
			debugLog(fmt.Sprintf("Stripped non-speech tags from: %s", text))
			text = cleaned
			if text == "" {
				fmt.Println("No speech detected (non-speech tags only). Listening again...")
				continue
			}
		}

		response, err := callLLM(text, config)
		if err != nil {
			log.Printf("LLM failed: %v", err)
			continue
		}

		fmt.Printf("Assistant: %s\n", response)

		if strings.TrimSpace(response) == "" {
			fmt.Println("No response. Listening again...")
			continue
		}

		audioData, err := ttsWithPiper(response, config)
		if err != nil {
			log.Printf("TTS failed: %v", err)
			continue
		}

		fmt.Println("Speaking...")
		err = playAudio(audioData)
		if err != nil {
			log.Printf("Failed to play audio: %v", err)
			continue
		}

		totalTime := time.Since(startTime)
		fmt.Printf("Capture: %v | STT+LLM+TTS+playback: %v | Total: %v\n",
			captureTime.Round(time.Millisecond),
			(totalTime - captureTime).Round(time.Millisecond),
			totalTime.Round(time.Millisecond))
		fmt.Println()

		if config.Debug {
			fmt.Printf("Debug: Audio bytes: %d\n", len(audioData))
		}
	}
}
