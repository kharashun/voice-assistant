package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
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
	Stream             bool            `json:"stream,omitempty"`
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

type Session struct {
	messages    []ChatMessage
	lastSpeech  time.Time
	maxMessages int
}

func newSession(maxMessages int) *Session {
	return &Session{
		messages:    nil,
		lastSpeech:  time.Now(),
		maxMessages: maxMessages,
	}
}

func (s *Session) reset() {
	s.messages = nil
	s.lastSpeech = time.Now()
}

// trim drops the oldest user/assistant pairs so the history stays within
// maxMessages. Turns are appended as pairs, so removing two at a time
// keeps the user/assistant alternation intact.
func (s *Session) trim() {
	for len(s.messages) > s.maxMessages {
		s.messages = s.messages[2:]
	}
}

// appendTurn records a completed exchange. Only successful turns are
// stored, so a failed LLM call never leaves an unanswered user message
// in the history.
func (s *Session) appendTurn(userMsg, assistantMsg string) {
	s.messages = append(s.messages, ChatMessage{Role: "user", Content: userMsg})
	s.messages = append(s.messages, ChatMessage{Role: "assistant", Content: assistantMsg})
	s.trim()
}

// normalizeUtterance lowercases text and re-joins the words it contains
// with single spaces, dropping everything else (punctuation, hyphens).
// This absorbs whisper's transcription quirks ("New voice-assistant
// session.") before phrase matching.
func normalizeUtterance(text string) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return strings.Join(words, " ")
}

// isResetCommand reports whether an utterance is the session-reset
// command: the phrase must appear as whole words, and the utterance may
// carry at most two padding words ("um", "please") around it, so a longer
// sentence that merely mentions the phrase does not wipe the conversation.
func isResetCommand(text, phrase string) bool {
	normalized := normalizeUtterance(text)
	phraseWords := normalizeUtterance(phrase)
	if phraseWords == "" {
		return false
	}
	if !strings.Contains(" "+normalized+" ", " "+phraseWords+" ") {
		return false
	}
	return len(strings.Fields(normalized)) <= len(strings.Fields(phraseWords))+2
}

// checkSessionTimeout clears the session when the user has been silent
// longer than the configured timeout. It runs right after each capture,
// before the audio is processed: in vad mode the capture itself blocks
// for an unbounded time waiting for speech, so a check between loop
// iterations would never fire for a returning user, whose utterance would
// then be answered with a stale conversation. A timeout <= 0 disables the
// check; an already-empty session is left alone so an idle assistant
// does not log spurious resets.
func checkSessionTimeout(session *Session, config *Config) {
	if config.SessionTimeoutSec <= 0 || len(session.messages) == 0 {
		return
	}
	if time.Since(session.lastSpeech) > time.Duration(config.SessionTimeoutSec)*time.Second {
		log.Println("Session timeout - resetting conversation")
		session.reset()
		fmt.Println("Session timed out - conversation cleared.")
	}
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
	LLMStream           bool    // sentence-chunked streaming TTS via SSE
	CaptureMode         string  // "vad" (silence-triggered) or "fixed" (fixed window)
	CaptureSeconds      int     // fixed mode: window length in seconds
	VadThreshold        float64 // sox amplitude threshold in percent
	VadStartMs          int     // sound duration that starts the recording
	VadSilenceSec       float64 // quiet duration that ends the recording
	VadMaxUtteranceSec  int     // hard cap on utterance length
	VadMinSpeechMs      int     // captures shorter than this are skipped before STT
	SessionTimeoutSec   int     // seconds of silence before auto-reset (0 = disabled)
	SessionMaxMessages  int     // max messages kept in session history (even)
	SessionResetPhrase  string  // utterance that resets the session
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

	// 0 disables the idle timeout; negatives and garbage fall back to 60s.
	sessionTimeoutSec, err := strconv.Atoi(getenv("SESSION_TIMEOUT_SEC", "60"))
	if err != nil || sessionTimeoutSec < 0 {
		sessionTimeoutSec = 60
	}

	sessionMaxMessages, err := strconv.Atoi(getenv("SESSION_MAX_MESSAGES", "10"))
	if err != nil || sessionMaxMessages <= 0 || sessionMaxMessages%2 != 0 {
		sessionMaxMessages = 10
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
		LLMStream:           getenv("LLM_STREAM", "false") == "true",
		CaptureMode:         captureMode,
		CaptureSeconds:      captureSeconds,
		VadThreshold:        vadThreshold,
		VadStartMs:          vadStartMs,
		VadSilenceSec:       vadSilenceSec,
		VadMaxUtteranceSec:  vadMaxUtteranceSec,
		VadMinSpeechMs:      vadMinSpeechMs,
		SessionTimeoutSec:   sessionTimeoutSec,
		SessionMaxMessages:  sessionMaxMessages,
		SessionResetPhrase:  getenv("SESSION_RESET_PHRASE", "new voice assistant session"),
		Debug:               os.Getenv("DEBUG") == "true",
	}
}

func debugLog(c *Config, msg string) {
	if c.Debug {
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

	debugLog(config, fmt.Sprintf("Captured %d bytes of audio", len(wavData)))
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
		debugLog(config, fmt.Sprintf("Capture too short: %dms < %dms", ms, config.VadMinSpeechMs))
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
			debugLog(config, fmt.Sprintf("Whisper VAD model not found at %s, running without --vad", config.WhisperVadModel))
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
	debugLog(config, fmt.Sprintf("STT result: %s", text))
	return text, nil
}

// buildLLMRequest assembles the chat-completions request body: system
// prompt + trimmed history + the current utterance. stream asks
// llama.cpp for an SSE response (used by the sentence-chunked TTS path).
func buildLLMRequest(prompt string, config *Config, session *Session, stream bool) (*bytes.Buffer, error) {
	// System prompt + trimmed history + the current utterance.
	messages := make([]ChatMessage, 0, len(session.messages)+2)
	if config.LLMSystemPrompt != "" {
		messages = append(messages, ChatMessage{Role: "system", Content: config.LLMSystemPrompt})
	}
	for _, m := range session.messages {
		messages = append(messages, m)
	}
	messages = append(messages, ChatMessage{Role: "user", Content: prompt})

	req := LLMRequest{
		Model:       config.LLMModel,
		Messages:    messages,
		MaxTokens:   config.LLMMaxTokens,
		Temperature: 0.7,
		Stream:      stream,
	}
	if config.LLMDisableReasoning {
		req.ChatTemplateKwargs = map[string]bool{"enable_thinking": false}
	}

	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	return bytes.NewBuffer(jsonData), nil
}

func callLLM(prompt string, config *Config, session *Session) (string, error) {
	reqBody, err := buildLLMRequest(prompt, config, session, false)
	if err != nil {
		return "", err
	}

	client := &http.Client{Timeout: config.LLMTimeout}
	resp, err := client.Post(
		config.LLMEndpoint+"/v1/chat/completions",
		"application/json",
		reqBody,
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

	content, err := parseLLMResponse(body)
	if err != nil {
		return "", err
	}
	debugLog(config, fmt.Sprintf("LLM response: %s", content))
	return content, nil
}

func parseLLMResponse(body []byte) (string, error) {
	var llmResp LLMResponse
	err := json.Unmarshal(body, &llmResp)
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

	return content, nil
}

// piperOutDir is the directory the persistent piper process writes its
// per-sentence WAVs into (--output-dir). /tmp is writable by the
// non-root container user.
const piperOutDir = "/tmp/piper-tts"

// piperSynthTimeout bounds one synthesis round trip (text line written,
// WAV path ack received, file read). A var so tests can shorten it.
var piperSynthTimeout = 30 * time.Second

// errPiperDown marks failures that mean the piper process is unusable
// (it exited, its pipes broke, or it stopped answering). synthesize
// restarts the process and retries once for these; other errors (e.g. a
// WAV that failed validation) leave a healthy process alone.
var errPiperDown = errors.New("piper process is down")

// cappedBuffer keeps only the last limit bytes written to it. piper's
// stderr is captured into one so a chatty or crashing process cannot
// grow an unbounded buffer; the tail is attached to error messages.
type cappedBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func newCappedBuffer(limit int) *cappedBuffer {
	return &cappedBuffer{limit: limit}
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf = append(c.buf, p...)
	if len(c.buf) > c.limit {
		c.buf = c.buf[len(c.buf)-c.limit:]
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.buf)
}

// piperProc is one running piper process with its request/ack plumbing.
type piperProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *cappedBuffer
	ack    chan string   // valid WAV paths printed by piper
	dead   chan struct{} // closed once the process is reaped
	done   chan struct{} // closed to abandon the process
}

// piperClient keeps a single piper process alive for the whole run.
//
// piper1-gpl's CLI (v1.8.0) synthesizes stdin line by line when given
// --output-dir: the voice model is loaded once at startup, each input
// line becomes a timestamped WAV in the directory, and the finished
// file's path is printed to stdout (libpiper/src/main/utils/process.cpp,
// OUTPUT_DIRECTORY branch). That is a ready-made request/ack protocol
// with no piper source changes: write one sentence per line, read the
// ack path, read the WAV. The model-load cost is paid once at startup
// instead of once per utterance, which is what makes TTS fast.
//
// Empty input lines are skipped by piper without printing an ack, so
// synthesize must never send them (it would hang the protocol). Acks
// are flushed promptly: C++ ties cin to cout, so piper's getline flushes
// the path before blocking on the next line, and the process is also
// run under `stdbuf -oL` as belt and braces. Anything piper prints to
// stdout that is not a WAV path under our output dir is skipped by the
// reader, so stray log lines cannot desync the protocol.
type piperClient struct {
	bin    string
	model  string
	espeak string
	dir    string
	debug  bool

	mu   sync.Mutex
	proc *piperProc
}

// newPiperClient starts the persistent piper process. The caller should
// validate it with a short warm-up synthesis and fall back to per-call
// mode (ttsWithPiper) if that fails.
func newPiperClient(bin, model, espeak, dir string, debug bool) (*piperClient, error) {
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create piper output dir: %w", err)
	}

	pc := &piperClient{
		bin:    bin,
		model:  model,
		espeak: espeak,
		dir:    dir,
		debug:  debug,
	}
	if err := pc.spawn(); err != nil {
		return nil, err
	}
	log.Println("piper started (persistent TTS process, voice model loaded once)")
	return pc, nil
}

func (pc *piperClient) debugf(format string, args ...any) {
	if pc.debug {
		log.Printf("[DEBUG] piper: "+format, args...)
	}
}

// spawn starts a fresh piper process and its stdout reader goroutine.
// Called by newPiperClient and by synthesize's restart path; always
// with pc.mu held.
func (pc *piperClient) spawn() error {
	args := []string{"--model", pc.model}
	if pc.espeak != "" {
		args = append(args, "--espeak-data", pc.espeak)
	}
	args = append(args, "--output-dir", pc.dir)

	// stdbuf -oL forces line-buffered stdout so the ack path is flushed
	// as soon as it is written (coreutils; skipped if absent - the
	// cin/cout tie already flushes before each blocking read).
	argv := append([]string{pc.bin}, args...)
	if _, err := exec.LookPath("stdbuf"); err == nil {
		argv = append([]string{"stdbuf", "-oL"}, argv...)
	}
	cmd := exec.Command(argv[0], argv[1:]...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create piper stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create piper stdout pipe: %w", err)
	}
	stderr := newCappedBuffer(4096)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start piper: %w", err)
	}

	p := &piperProc{
		cmd:    cmd,
		stdin:  stdin,
		stderr: stderr,
		ack:    make(chan string, 8),
		dead:   make(chan struct{}),
		done:   make(chan struct{}),
	}

	go func() {
		sc := bufio.NewScanner(stdout)
	readLoop:
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !isAckPath(line, pc.dir) {
				pc.debugf("ignoring piper stdout line: %q", line)
				continue
			}
			select {
			case p.ack <- line:
			case <-p.done:
				break readLoop
			}
		}
		// Reap the process (exited or killed) and announce the death.
		_ = cmd.Wait()
		close(p.dead)
	}()

	pc.proc = p
	return nil
}

// isAckPath reports whether line is a WAV path piper printed under the
// output directory - its per-line completion ack.
func isAckPath(line, dir string) bool {
	if !strings.HasPrefix(line, dir+string(filepath.Separator)) {
		return false
	}
	if !strings.HasSuffix(line, ".wav") {
		return false
	}
	return !strings.Contains(line, "..")
}

// synthesize speaks one chunk of text through the persistent piper
// process and returns its WAV bytes. Failures marked errPiperDown
// trigger a single restart + retry; other errors are returned as-is.
func (pc *piperClient) synthesize(text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("piper synthesize: empty text")
	}

	pc.mu.Lock()
	defer pc.mu.Unlock()

	data, err := pc.trySynth(text)
	if err == nil || !errors.Is(err, errPiperDown) {
		return data, err
	}

	// Process-level failure: restart once and retry the text.
	pc.debugf("restarting piper after: %v", err)
	pc.shutdown()
	if serr := pc.spawn(); serr != nil {
		return nil, fmt.Errorf("piper restart failed: %w", serr)
	}
	return pc.trySynth(text)
}

// trySynth runs one request/ack round against the current process.
// Called with pc.mu held.
func (pc *piperClient) trySynth(text string) ([]byte, error) {
	p := pc.proc
	if p == nil {
		return nil, errPiperDown
	}
	if _, err := io.WriteString(p.stdin, text+"\n"); err != nil {
		return nil, fmt.Errorf("%w: stdin write failed: %v", errPiperDown, err)
	}
	select {
	case path, ok := <-p.ack:
		if !ok {
			return nil, fmt.Errorf("%w: ack channel closed", errPiperDown)
		}
		data, err := readAckWav(path)
		if err != nil {
			return nil, err
		}
		pc.debugf("generated %d bytes for %q", len(data), text)
		return data, nil
	case <-time.After(piperSynthTimeout):
		return nil, fmt.Errorf("%w: no ack within %v", errPiperDown, piperSynthTimeout)
	case <-p.dead:
		return nil, fmt.Errorf("%w: process exited (last stderr: %s)", errPiperDown, p.stderr.String())
	}
}

// readAckWav reads and removes one synthesized WAV, validating that it
// is a complete RIFF file: a synthesis that produced no samples (e.g.
// text with no speakable content) writes an empty file.
func readAckWav(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	os.Remove(path)
	if err != nil {
		return nil, fmt.Errorf("piper output read failed: %w", err)
	}
	if _, err := wavDataSize(data); err != nil {
		return nil, fmt.Errorf("piper produced no audio (%s): %w", path, err)
	}
	return data, nil
}

// shutdown abandons and kills the current process. done is closed first
// so a reader blocked on a full ack channel wakes up; the kill turns a
// blocked stdin read into EOF so the reader reaps the process and
// closes dead. Called with pc.mu held.
func (pc *piperClient) shutdown() {
	p := pc.proc
	if p == nil {
		return
	}
	close(p.done)
	_ = p.cmd.Process.Kill()
	select {
	case <-p.dead:
	case <-time.After(5 * time.Second):
		log.Println("piper process did not exit after kill")
	}
	pc.proc = nil
}

// stop shuts the persistent process down. main never calls it (the
// loop runs until the orchestrator exits), but tests and local runs do.
func (pc *piperClient) stop() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.shutdown()
}

func ttsWithPiper(text string, config *Config) ([]byte, error) {
	// Fallback when the persistent process cannot start: piper reads
	// the text to synthesize from stdin and writes a WAV file at the
	// voice's native sample rate (no --input-text/--sample-rate options
	// exist in the piper CLI). The voice model is loaded per call.
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

	debugLog(config, fmt.Sprintf("Generated %d bytes of audio", len(audioData)))
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

// speaker pipelines TTS behind playback for one assistant response. A
// TTS worker synthesizes sentences in order while a single player
// goroutine plays them one at a time: synthesis of the next sentence
// overlaps playback of the current one, two aplay processes never fight
// over the ALSA device, and both channels being FIFO keeps the audio in
// text order.
type speaker struct {
	synth func(string) ([]byte, error)
	texts chan string
	wavs  chan []byte
	wg    sync.WaitGroup
	spoke sync.Once
}

// newSpeaker starts the pipeline. synth is piperClient.synthesize for
// the persistent process or a ttsWithPiper wrapper for the fallback.
func newSpeaker(synth func(string) ([]byte, error)) *speaker {
	s := &speaker{
		synth: synth,
		texts: make(chan string, 8),
		wavs:  make(chan []byte, 2),
	}
	// TTS worker: sentences in, WAVs out.
	go func() {
		defer close(s.wavs)
		for text := range s.texts {
			data, err := s.synth(text)
			if err != nil {
				// One failed sentence is skipped, not fatal: the rest
				// of the response still speaks.
				log.Printf("TTS failed: %v", err)
				continue
			}
			s.wavs <- data
		}
	}()
	// Player: one aplay at a time, in order.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for data := range s.wavs {
			if err := playAudio(data); err != nil {
				log.Printf("Failed to play audio: %v", err)
			}
		}
	}()
	return s
}

// submit queues one chunk of text for synthesis and playback. It blocks
// once the pipeline is a whole response ahead of the speaker, which is
// the backpressure that keeps the LLM stream from outrunning playback.
func (s *speaker) submit(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	// Skip chunks with nothing speakable ("...", "???"): piper would
	// produce an empty WAV for them.
	if !strings.ContainsFunc(text, func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	}) {
		return
	}
	s.spoke.Do(func() { fmt.Println("Speaking...") })
	s.texts <- text
}

// closeAndWait drains the pipeline: no more text will be submitted, and
// playback of everything already queued finishes before it returns.
func (s *speaker) closeAndWait() {
	close(s.texts)
	s.wg.Wait()
}

// speakResponse synthesizes and plays a complete response through the
// pipeline (the non-streaming path).
func speakResponse(text string, synth func(string) ([]byte, error)) {
	s := newSpeaker(synth)
	s.submit(cleanForTTS(text))
	s.closeAndWait()
}

// mdLeadRe matches leading markdown headings, block quotes and list
// bullets at the start of a (single-line) sentence.
var mdLeadRe = regexp.MustCompile(`^(?:[#>*-]+\s+)+`)

// cleanForTTS strips markdown decoration the TTS would otherwise read
// aloud ("**bold**", "`code`", "# heading", "- bullet").
func cleanForTTS(text string) string {
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "*", "")
	text = strings.ReplaceAll(text, "`", "")
	text = mdLeadRe.ReplaceAllString(strings.TrimSpace(text), "")
	return strings.TrimSpace(text)
}

// sentenceAbbrevs lists words whose trailing period does not end a
// sentence ("Mr. Smith arrived."). Matched case-insensitively on the
// word stripped of its punctuation.
var sentenceAbbrevs = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true,
	"prof": true, "sr": true, "jr": true,
	"st": true, "ave": true, "blvd": true,
	"jan": true, "feb": true, "mar": true, "apr": true,
	"may": true, "jun": true, "jul": true,
	"aug": true, "sep": true, "sept": true, "oct": true,
	"nov": true, "dec": true,
	"eg": true, "ie": true, "vs": true, "etc": true,
	"vol": true, "no": true, "fig": true,
}

// extractSentences splits buffered text into completed sentences plus
// the unspoken remainder. A sentence ends at a run of '.', '!' or '?'
// (optionally followed by closing quotes) that is itself followed by
// whitespace, or at a newline; a period directly followed by another
// character does not end a sentence (decimals like 3.5, URLs, "Mr."),
// and an ellipsis (three or more dots) is a pause, not a sentence end.
// A punctuation run at the very end of the buffer is left in the
// remainder because more text may still arrive ("3." could become
// "3.5"); the caller flushes the remainder when the response ends.
func extractSentences(buf string) (sentences []string, remainder string) {
	start, i := 0, 0
	for i < len(buf) {
		r, size := utf8.DecodeRuneInString(buf[i:])
		switch {
		case r == '\n':
			// Paragraph break ends the sentence even without punctuation.
			if s := strings.TrimSpace(buf[start:i]); s != "" {
				sentences = append(sentences, s)
			}
			i += size
			start = i
		case r == '.' || r == '!' || r == '?':
			// Extend over a punctuation run and any closing quotes.
			j := i + size
			for j < len(buf) && strings.IndexByte(".!?\"'", buf[j]) >= 0 {
				j++
			}
			if j >= len(buf) {
				// Could continue ("3." -> "3.5"): keep for later.
				return sentences, strings.TrimLeft(buf[start:], " \t\v\f\r\n")
			}
			if next, _ := utf8.DecodeRuneInString(buf[j:]); unicode.IsSpace(next) {
				// An ellipsis ("Well... okay.") is a pause inside the
				// sentence, not its end.
				ellipsis := strings.Count(buf[i:j], ".") >= 3
				if !ellipsis && !endsAbbreviation(buf[start:j]) {
					if s := strings.TrimSpace(buf[start:j]); s != "" {
						sentences = append(sentences, s)
					}
					start = j
				}
			}
			i = j
		default:
			i += size
		}
	}
	return sentences, strings.TrimLeft(buf[start:], " \t\v\f\r\n")
}

// endsAbbreviation reports whether the word ending at the punctuation
// run is a known abbreviation, so "Mr." must not become a sentence.
func endsAbbreviation(text string) bool {
	end := len(text)
	for end > 0 && !unicode.IsSpace(rune(text[end-1])) {
		end--
	}
	word := strings.ToLower(strings.Trim(text[end:], ".,!?\"'"))
	return word != "" && sentenceAbbrevs[word]
}

// callLLMStream requests an SSE stream from llama.cpp and speaks the
// response sentence by sentence while the rest is still generating:
// each completed sentence is submitted to the speaker as soon as it
// arrives, so its synthesis overlaps generation of the rest. It returns
// only after playback of the whole response has finished, so the caller
// never starts capturing while the assistant is still speaking (the
// mic would pick up the assistant's own voice).
func callLLMStream(prompt string, config *Config, session *Session, piper *piperClient) (string, error) {
	reqBody, err := buildLLMRequest(prompt, config, session, true)
	if err != nil {
		return "", err
	}

	// The timeout is an overall deadline: http.Client.Timeout covers
	// headers through the last SSE event, so it bounds total generation
	// time for the stream.
	client := &http.Client{Timeout: config.LLMTimeout}
	resp, err := client.Post(
		config.LLMEndpoint+"/v1/chat/completions",
		"application/json",
		reqBody,
	)
	if err != nil {
		return "", fmt.Errorf("failed to call LLM: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("LLM returned status %d: %s", resp.StatusCode, string(body))
	}

	sp := newSpeaker(piper.synthesize)
	var full strings.Builder
	var partial string
	reader := bufio.NewReader(resp.Body)

	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			// A broken stream after audio has already been spoken:
			// keep what was generated so the recorded turn matches the
			// audio, and let the flush below speak the remainder.
			if full.Len() > 0 {
				log.Printf("SSE stream failed mid-response (%v); continuing with partial text", err)
				break
			}
			sp.closeAndWait()
			return "", fmt.Errorf("failed to read SSE: %w", err)
		}

		// Each SSE event is one "data: {...}" line; llama.cpp ends the
		// stream with "data: [DONE]".
		if line = strings.TrimRight(line, "\r\n"); strings.HasPrefix(line, "data: ") {
			dataStr := strings.TrimPrefix(line, "data: ")
			if dataStr == "[DONE]" {
				break
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					FinishReason string `json:"finish_reason"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(dataStr), &chunk) == nil && len(chunk.Choices) > 0 {
				if content := chunk.Choices[0].Delta.Content; content != "" {
					full.WriteString(content)
					partial += content
					var sentences []string
					sentences, partial = extractSentences(partial)
					for _, s := range sentences {
						sp.submit(cleanForTTS(s))
					}
				}
				if chunk.Choices[0].FinishReason != "" {
					break
				}
			}
		}

		if err == io.EOF {
			break
		}
	}

	// Flush whatever never reached a sentence boundary.
	if rest := cleanForTTS(partial); rest != "" {
		sp.submit(rest)
	}
	sp.closeAndWait()

	content := strings.TrimSpace(full.String())
	debugLog(config, fmt.Sprintf("LLM streaming response: %s", content))
	return content, nil
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
	if config.LLMStream {
		log.Println("LLM streaming enabled (sentence-chunked TTS)")
	}
	if config.CaptureMode == "vad" {
		log.Printf("Capture: voice-activated (threshold %g%%, stop after %ss of silence, max utterance %ds)",
			config.VadThreshold, soxDuration(config.VadSilenceSec), config.VadMaxUtteranceSec)
	} else {
		log.Printf("Capture: fixed %ds window", config.CaptureSeconds)
	}

	session := newSession(config.SessionMaxMessages)
	if config.SessionTimeoutSec > 0 {
		log.Printf("Session: %d-message history, %ds idle timeout, reset command: '%s'",
			config.SessionMaxMessages, config.SessionTimeoutSec, config.SessionResetPhrase)
	} else {
		log.Printf("Session: %d-message history, idle timeout disabled, reset command: '%s'",
			config.SessionMaxMessages, config.SessionResetPhrase)
	}
	fmt.Printf("Voice Assistant ready! Say '%s' to reset the conversation.\n", config.SessionResetPhrase)
	fmt.Println()

	// Start the persistent piper process (voice model loaded once for
	// the whole run) and validate it with a short warm-up synthesis,
	// which also pre-warms the model. On failure, fall back to
	// spawning piper per utterance (ttsWithPiper).
	piper, err := newPiperClient(config.PiperBin, config.PiperModel, config.EspeakData, piperOutDir, config.Debug)
	if err != nil {
		log.Printf("Failed to start persistent piper (%v); falling back to per-call mode", err)
		piper = nil
	} else if _, err := piper.synthesize("Voice assistant ready."); err != nil {
		log.Printf("Persistent piper failed validation (%v); falling back to per-call mode", err)
		piper.stop()
		piper = nil
	}

	// All speech goes through the same pipeline; only the synthesis
	// function differs between persistent and per-call piper.
	synthesize := func(text string) ([]byte, error) {
		return ttsWithPiper(text, config)
	}
	if piper != nil {
		synthesize = piper.synthesize
	}

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
			time.Sleep(time.Second)
			continue
		}
		captureTime := time.Since(startTime)

		// The capture can block for a long time waiting for speech, so
		// the idle timeout is re-checked here, before the new utterance
		// is answered with a possibly stale conversation.
		checkSessionTimeout(session, config)

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
			debugLog(config, fmt.Sprintf("Stripped non-speech tags from: %s", text))
			text = cleaned
			if text == "" {
				fmt.Println("No speech detected (non-speech tags only). Listening again...")
				continue
			}
		}

		// Check for the session reset command.
		if isResetCommand(text, config.SessionResetPhrase) {
			session.reset()
			log.Println("Session reset by user")
			response := "Session reset."
			fmt.Printf("Assistant: %s\n", response)
			speakResponse(response, synthesize)
			continue
		}

		session.lastSpeech = time.Now()

		// With streaming enabled, callLLMStream speaks the response
		// sentence by sentence and returns only after playback has
		// finished. Otherwise the full reply is spoken below. Either
		// way the capture loop resumes only once the assistant has
		// gone quiet.
		var response string
		if config.LLMStream && piper != nil {
			response, err = callLLMStream(text, config, session, piper)
		} else {
			response, err = callLLM(text, config, session)
		}
		if err != nil {
			log.Printf("LLM failed: %v", err)
			continue
		}

		fmt.Printf("Assistant: %s\n", response)

		if strings.TrimSpace(response) == "" {
			fmt.Println("No response. Listening again...")
			continue
		}

		// callLLM appends the current utterance to the request itself;
		// the turn is recorded in the history only after a reply.
		session.appendTurn(text, response)

		// For non-streaming mode, the whole reply is one submission to
		// the same pipeline (streaming already spoke it above).
		if !config.LLMStream || piper == nil {
			speakResponse(response, synthesize)
		}

		totalTime := time.Since(startTime)
		fmt.Printf("Capture: %v | STT+LLM+TTS+playback: %v | Total: %v\n",
			captureTime.Round(time.Millisecond),
			(totalTime - captureTime).Round(time.Millisecond),
			totalTime.Round(time.Millisecond))
		fmt.Println()
	}
	// piper.stop() is never reached (infinite loop); the process ends
	// when the orchestrator exits.
}
