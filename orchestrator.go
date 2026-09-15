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
	"strings"
	"time"
)

type LLMRequest struct {
	Prompt      string  `json:"prompt"`
	MaxTokens   int     `json:"max_tokens"`
	Temperature float64 `json:"temperature"`
}

type LLMResponse struct {
	Content string `json:"content"`
}

type Config struct {
	WhisperModel string
	PiperModel   string
	LLMEndpoint  string
	Debug        bool
}

func loadConfig() *Config {
	return &Config{
		WhisperModel: os.Getenv("WHISPER_MODEL"),
		PiperModel:   os.Getenv("PIPER_MODEL"),
		LLMEndpoint:  os.Getenv("LLM_ENDPOINT"),
		Debug:        os.Getenv("DEBUG") == "true",
	}
}

func debugLog(msg string) {
	if os.Getenv("DEBUG") == "true" {
		log.Println("[DEBUG]", msg)
	}
}

func captureAudio(config *Config) ([]byte, error) {
	// Use sox to capture from ALSA device directly to 16kHz WAV
	tmpFile := fmt.Sprintf("/tmp/%d.wav", time.Now().UnixNano())
	defer os.Remove(tmpFile)

	cmd := exec.Command(
		"sox",
		"-d",           // default audio device
		"-r", "16000",  // 16kHz sample rate
		"-c", "1",      // mono
		"-b", "16",     // 16-bit
		"-t", "wav",    // WAV format
		tmpFile,        // output file
		"trim", "0", "5", // trim: start at 0, duration 5 seconds
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
	tmpFile := fmt.Sprintf("/tmp/%d.wav", time.Now().UnixNano())
	err := os.WriteFile(tmpFile, wavData, 0644)
	if err != nil {
		return "", fmt.Errorf("failed to write temp file: %w", err)
	}
	defer os.Remove(tmpFile)

	cmd := exec.Command(
		"/app/whisper-cli",
		"-m", config.WhisperModel,
		"-f", tmpFile,
		"-otxt",
		"-pc",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("whisper failed: %w, output: %s", err, string(output))
	}

	text := strings.TrimSpace(string(output))
	debugLog(fmt.Sprintf("STT result: %s", text))

	return text, nil
}

func callLLM(prompt string, config *Config) (string, error) {
	req := LLMRequest{
		Prompt:      prompt,
		MaxTokens:   100,
		Temperature: 0.7,
	}

	jsonData, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := http.Post(
		config.LLMEndpoint+"/completion",
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

	var llmResp LLMResponse
	err = json.Unmarshal(body, &llmResp)
	if err != nil {
		return "", fmt.Errorf("failed to parse LLM response: %w, body: %s", err, string(body))
	}

	debugLog(fmt.Sprintf("LLM response: %s", llmResp.Content))

	return llmResp.Content, nil
}

func ttsWithPiper(text string, config *Config) ([]byte, error) {
	tmpFile := fmt.Sprintf("/tmp/%d.wav", time.Now().UnixNano())

	cmd := exec.Command(
		"/app/piper",
		"--model", config.PiperModel,
		"--input-text", text,
		"--output-file", tmpFile,
		"--sample-rate", "22050",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("piper failed: %w, output: %s", err, string(output))
	}

	audioData, err := os.ReadFile(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read audio file: %w", err)
	}

	os.Remove(tmpFile)

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

	fmt.Println("Voice Assistant ready! Press Ctrl+C to exit.")
	fmt.Println()

	for {
		fmt.Print("Listening... ")

		wavData, err := captureAudio(config)
		if err != nil {
			log.Printf("Failed to capture audio: %v", err)
			continue
		}

		fmt.Println("Processing...")

		startTime := time.Now()

		text, err := sttWithWhisper(wavData, config)
		if err != nil {
			log.Printf("STT failed: %v", err)
			continue
		}

		fmt.Printf("You said: %s\n", text)

		if strings.TrimSpace(text) == "" {
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
		fmt.Printf("Total latency: %v\n", totalTime)
		fmt.Println()

		if config.Debug {
			fmt.Printf("Debug: Audio bytes: %d\n", len(audioData))
		}
	}
}
