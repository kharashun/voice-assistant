package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type LLMRequest struct {
	Model       string        `json:"model,omitempty"`
	Messages    []ChatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature"`
}

type LLMResponse struct {
	Choices []struct {
		Message ChatMessage `json:"message"`
	} `json:"choices"`
}

type Config struct {
	WhisperBin      string
	WhisperModel    string
	PiperBin        string
	PiperModel      string
	EspeakData      string
	LLMEndpoint     string
	LLMModel        string
	LLMSystemPrompt string
	LLMTimeout      time.Duration
	CaptureSeconds  int
	Debug           bool
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

	return &Config{
		WhisperBin:      getenv("WHISPER_BIN", "/app/whisper-cli"),
		WhisperModel:    getenv("WHISPER_MODEL", "/models/whisper/ggml-tiny.en.bin"),
		PiperBin:        getenv("PIPER_BIN", "/app/piper"),
		PiperModel:      getenv("PIPER_MODEL", "/models/piper/en_US-lessac-medium.onnx"),
		EspeakData:      getenv("ESPEAK_DATA", "/opt/espeak-ng-data"),
		LLMEndpoint:     strings.TrimRight(getenv("LLM_ENDPOINT", "http://127.0.0.1:8080"), "/"),
		LLMModel:        getenv("LLM_MODEL", ""),
		LLMSystemPrompt: getenv("LLM_SYSTEM_PROMPT", "You are a voice assistant. Reply in one or two short sentences."),
		LLMTimeout:      llmTimeout,
		CaptureSeconds:  captureSeconds,
		Debug:           os.Getenv("DEBUG") == "true",
	}
}

func debugLog(msg string) {
	if os.Getenv("DEBUG") == "true" {
		log.Println("[DEBUG]", msg)
	}
}

func captureAudio(config *Config) ([]byte, error) {
	// Use sox to capture from the ALSA default device directly as 16kHz WAV
	tmpFile := fmt.Sprintf("/tmp/%d.wav", time.Now().UnixNano())
	defer os.Remove(tmpFile)

	cmd := exec.Command(
		"sox",
		"-d",          // default audio device
		"-r", "16000", // 16kHz sample rate
		"-c", "1", // mono
		"-b", "16", // 16-bit
		"-t", "wav", // WAV format
		tmpFile,                                          // output file
		"trim", "0", strconv.Itoa(config.CaptureSeconds), // record a fixed window
	)

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
		MaxTokens:   100,
		Temperature: 0.7,
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
	log.Printf("Piper model: %s", config.PiperModel)
	log.Printf("LLM endpoint: %s", config.LLMEndpoint)
	if config.LLMModel != "" {
		log.Printf("LLM model: %s", config.LLMModel)
	}

	fmt.Println("Voice Assistant ready! Press Ctrl+C to exit.")
	fmt.Println()

	for {
		fmt.Printf("Listening... (%ds window)\n", config.CaptureSeconds)

		// Measure end-to-end, including the capture window.
		startTime := time.Now()

		wavData, err := captureAudio(config)
		if err != nil {
			log.Printf("Failed to capture audio: %v", err)
			continue
		}
		captureTime := time.Since(startTime)

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
