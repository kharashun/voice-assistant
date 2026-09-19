package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// testWav returns a minimal valid 44-byte PCM WAV with 4 data bytes.
func testWav(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := func(s string) { buf.WriteString(s) }
	u32 := func(v uint32) { binary.Write(&buf, binary.LittleEndian, v) }
	u16 := func(v uint16) { binary.Write(&buf, binary.LittleEndian, v) }

	w("RIFF")
	u32(36 + 4)
	w("WAVE")
	w("fmt ")
	u32(16)
	u16(1) // PCM
	u16(1) // mono
	u32(22050)
	u32(44100)
	u16(2)
	u16(16)
	w("data")
	u32(4)
	buf.Write([]byte{0, 0, 0, 0})
	return buf.Bytes()
}

// writeFakePiper creates an executable that mimics the piper CLI's
// --output-dir line protocol: one WAV per stdin line, its path printed
// to stdout.
func writeFakePiper(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-piper")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPiperClientRoundTrip(t *testing.T) {
	base := t.TempDir()
	outDir := filepath.Join(base, "out")
	template := filepath.Join(base, "template.wav")
	wav := testWav(t)
	if err := os.WriteFile(template, wav, 0o644); err != nil {
		t.Fatal(err)
	}
	bin := writeFakePiper(t, base, `
n=0
while IFS= read -r line; do
  [ -z "$line" ] && continue
  n=$((n+1))
  f="$FAKE_OUT/$$-$n.wav"
  cp "$FAKE_TEMPLATE" "$f"
  echo "$f"
done
`)
	t.Setenv("FAKE_OUT", outDir)
	t.Setenv("FAKE_TEMPLATE", template)

	pc, err := newPiperClient(bin, "model.onnx", "", outDir, false)
	if err != nil {
		t.Fatalf("newPiperClient: %v", err)
	}
	defer pc.stop()

	for _, text := range []string{"Hello there.", "Second sentence."} {
		data, err := pc.synthesize(text)
		if err != nil {
			t.Fatalf("synthesize(%q): %v", text, err)
		}
		if !bytes.Equal(data, wav) {
			t.Errorf("synthesize(%q) returned %d bytes, want the template WAV", text, len(data))
		}
	}

	// The ack WAVs are consumed (read + removed).
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("output dir has %d leftover files", len(entries))
	}

	// Empty text is refused without touching the process.
	if _, err := pc.synthesize("   "); err == nil {
		t.Error("synthesize(blank) should fail")
	}
}

func TestPiperClientRestartOnTimeout(t *testing.T) {
	base := t.TempDir()
	outDir := filepath.Join(base, "out")
	// Reads lines but never prints an ack.
	bin := writeFakePiper(t, base, "while IFS= read -r line; do :; done\n")

	old := piperSynthTimeout
	piperSynthTimeout = 200 * time.Millisecond
	t.Cleanup(func() { piperSynthTimeout = old })

	pc, err := newPiperClient(bin, "model.onnx", "", outDir, false)
	if err != nil {
		t.Fatalf("newPiperClient: %v", err)
	}
	defer pc.stop()

	start := time.Now()
	if _, err := pc.synthesize("hello"); err == nil {
		t.Fatal("expected a timeout error")
	}
	// One failed try + restart + one failed retry should stay well
	// under the 5s shutdown fallback.
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("restart took too long: %v", elapsed)
	}

	// The client must still be in a working state afterwards: the
	// restart left a fresh process behind (it will time out again, but
	// without deadlock or panic).
	if _, err := pc.synthesize("again"); err == nil {
		t.Fatal("expected a timeout error on the restarted process")
	}
}

func TestIsAckPath(t *testing.T) {
	const dir = "/tmp/piper-tts"
	tests := []struct {
		line string
		want bool
	}{
		{dir + "/123456789.wav", true},
		{dir + "/a-b.wav", true},
		{"/tmp/other/1.wav", false},
		{dir + "/../etc/passwd.wav", false},
		{dir + "/1.txt", false},
		{"Loaded voice in 0.3 seconds", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isAckPath(tt.line, dir); got != tt.want {
			t.Errorf("isAckPath(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}

func TestExtractSentences(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
		rem  string
	}{
		{"punctuation at end of buffer is held", "Hello there.", nil, "Hello there."},
		{"two sentences", "Hello there. How are you?", []string{"Hello there."}, "How are you?"},
		{"trailing space completes", "Done. ", []string{"Done."}, ""},
		{"newline completes without punctuation", "Hello there\nHow are you", []string{"Hello there"}, "How are you"},
		{"blank lines skipped", "One.\n\nTwo.", []string{"One."}, "Two."},
		{"abbreviation keeps going", "Mr. Smith arrived today.", nil, "Mr. Smith arrived today."},
		{"abbrev then real end", "Mr. Smith arrived. He sat down.", []string{"Mr. Smith arrived."}, "He sat down."},
		{"decimal is not a boundary", "It costs 3.5 dollars.", nil, "It costs 3.5 dollars."},
		{"number can end a sentence", "I have 3. Yes.", []string{"I have 3."}, "Yes."},
		{"exclamation and question", "Wow! Really? Yes.", []string{"Wow!", "Really?"}, "Yes."},
		{"closing quote after period", `He said "stop." Then left.`, []string{`He said "stop."`}, "Then left."},
		{"punctuation run", "Well... okay. Fine.", []string{"Well... okay."}, "Fine."},
		{"mid-stream split across chunks", "Hello the", nil, "Hello the"},
		{"empty", "", nil, ""},
	}
	for _, tt := range tests {
		got, rem := extractSentences(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("%s: got %q (rem %q), want %q (rem %q)", tt.name, got, rem, tt.want, tt.rem)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("%s: sentence %d = %q, want %q", tt.name, i, got[i], tt.want[i])
			}
		}
		if rem != tt.rem {
			t.Errorf("%s: remainder = %q, want %q", tt.name, rem, tt.rem)
		}
	}
}

func TestCleanForTTS(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"**bold** text", "bold text"},
		{"some *emphasis* here", "some emphasis here"},
		{"run `cargo build` now", "run cargo build now"},
		{"## A heading", "A heading"},
		{"- bullet item", "bullet item"},
		{"-5 degrees is cold", "-5 degrees is cold"},
		{"a #hashtag stays", "a #hashtag stays"},
		{"plain sentence.", "plain sentence."},
	}
	for _, tt := range tests {
		if got := cleanForTTS(tt.in); got != tt.want {
			t.Errorf("cleanForTTS(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBuildLLMRequestStream(t *testing.T) {
	config := &Config{LLMModel: "test-model", LLMMaxTokens: 64}
	session := newSession(4)

	body, err := buildLLMRequest("hi", config, session, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body.String(), `"stream":true`) {
		t.Error("non-streaming request must not set stream:true")
	}

	body, err = buildLLMRequest("hi", config, session, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.String(), `"stream":true`) {
		t.Error("streaming request must set stream:true")
	}
}

func TestCaptureArgs(t *testing.T) {
	base := []string{"-d", "-r", "16000", "-c", "1", "-b", "16", "-t", "wav", "/tmp/x.wav"}

	cfg := &Config{CaptureMode: "vad", VadThreshold: 10, VadStartMs: 100, VadSilenceSec: 2, VadMaxUtteranceSec: 30}
	if got := captureArgs(cfg, "/tmp/x.wav"); !reflect.DeepEqual(got, append(append([]string{}, base...),
		"silence", "1", "0.1", "10%", "1", "2.0", "10%", "trim", "0", "30.0")) {
		t.Errorf("symmetric vad args = %v", got)
	}

	cfg.VadStopThreshold = 30
	if got := captureArgs(cfg, "/tmp/x.wav"); !reflect.DeepEqual(got, append(append([]string{}, base...),
		"silence", "1", "0.1", "10%", "1", "2.0", "30%", "trim", "0", "30.0")) {
		t.Errorf("asymmetric stop threshold args = %v", got)
	}

	cfg = &Config{CaptureMode: "fixed", CaptureSeconds: 5}
	if got := captureArgs(cfg, "/tmp/x.wav"); !reflect.DeepEqual(got, append(append([]string{}, base...),
		"trim", "0", "5.0")) {
		t.Errorf("fixed window args = %v", got)
	}
}

func TestSilenceWav(t *testing.T) {
	for _, d := range []time.Duration{0, 500 * time.Millisecond, 1500 * time.Millisecond} {
		wav := silenceWav(d)
		size, err := wavDataSize(wav)
		if err != nil {
			t.Fatalf("silenceWav(%v): %v", d, err)
		}
		if want := uint32(int(d*16000/time.Second) * 2); size != want {
			t.Errorf("silenceWav(%v) data size = %d, want %d", d, size, want)
		}
		for _, b := range wav[44:] {
			if b != 0 {
				t.Fatalf("silenceWav(%v) has non-zero sample data", d)
			}
		}
	}
}

func TestTurnStats(t *testing.T) {
	var nilStats *turnStats
	nilStats.setSTT(time.Second)
	nilStats.startLLM()
	nilStats.firstToken()
	nilStats.doneLLM()
	nilStats.sawReasoning()
	nilStats.noteSynth(time.Second)
	nilStats.notePlaybackStart()
	nilStats.addPlayback(time.Second)
	if n := nilStats.reasoningCount(); n != 0 {
		t.Errorf("nil stats reasoningCount = %d, want 0", n)
	}

	s := newTurnStats()
	s.startLLM()
	s.sawReasoning()
	s.sawReasoning()
	s.firstToken()
	s.firstToken() // second call must not overwrite the first-token time
	s.doneLLM()
	s.setSTT(800 * time.Millisecond)
	s.noteSynth(300 * time.Millisecond)
	s.noteSynth(500 * time.Millisecond)
	s.notePlaybackStart()
	s.addPlayback(2 * time.Second)
	if got := s.reasoningCount(); got != 2 {
		t.Errorf("reasoningCount = %d, want 2", got)
	}
	if s.llmTotal == 0 || s.llmTTFT == 0 || s.llmTotal < s.llmTTFT {
		t.Errorf("LLM timings inconsistent: ttft=%v total=%v", s.llmTTFT, s.llmTotal)
	}
	if s.ttsFirst != 300*time.Millisecond {
		t.Errorf("ttsFirst = %v, want 300ms", s.ttsFirst)
	}

	line := s.summary(9 * time.Second, 12 * time.Second)
	for _, want := range []string{
		"Capture: 9s", "STT: 800ms", "LLM: first token", "total ",
		"TTS: first 300ms", "2 sentences", "First audio:", "Playback: 2s", "Total: 12s",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("summary %q missing %q", line, want)
		}
	}

	empty := newTurnStats().summary(time.Second, time.Second)
	if strings.Contains(empty, "STT:") || strings.Contains(empty, "LLM:") || strings.Contains(empty, "Playback:") {
		t.Errorf("empty summary should omit unmeasured stages, got %q", empty)
	}
}
