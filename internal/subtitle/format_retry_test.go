package subtitle

import (
	"context"
	"strings"
	"testing"
)

type formatRetryTranscriber struct {
	calls int
	t     *testing.T
}

func (f *formatRetryTranscriber) Transcribe(_ context.Context, _ string, prompt string, _ int) (Result, error) {
	f.calls++
	if f.calls == 1 {
		return Result{Text: "[S01]hello", GeneratedTokens: 10}, nil
	}
	if !strings.Contains(prompt, "[0.00][S01]spoken words[1.50]") {
		f.t.Fatal("format instructions missing")
	}
	return Result{Text: "[0.00][S01]hello[1.50]", Segments: []Segment{{Start: 0, End: 1.5, Speaker: "S01", Text: "hello"}}, GeneratedTokens: 20}, nil
}

func TestMissingTimestampsRetryWithExplicitFormat(t *testing.T) {
	runner := &formatRetryTranscriber{t: t}
	got, err := transcribeChunk(context.Background(), runner, Options{MaxTokens: 4096}, "audio", t.TempDir(), "chunk", 180, 45, 2, nil)
	if err != nil || runner.calls != 2 || len(got) != 1 || got[0].Start != 180 || got[0].End != 181.5 {
		t.Fatalf("%+v %v calls=%d", got, err, runner.calls)
	}
}

func TestTimestampFallbackUsesRealTimesAndFailsClosed(t *testing.T) {
	runner := fakeTranscriber{result: Result{Backend: "faster-whisper-small", MossText: "[S01]untimed", Segments: []Segment{{Start: 1, End: 2, Text: "timed", Speaker: "unknown", Backend: "faster-whisper-small"}}}}
	got, err := transcribeChunk(context.Background(), runner, Options{MaxTokens: 4096}, "", t.TempDir(), "fallback", 180, 45, 0, nil)
	if err != nil || len(got) != 1 || got[0].Start != 181 || got[0].Backend != "faster-whisper-small" {
		t.Fatalf("%+v %v", got, err)
	}
	runner.result = Result{Text: "[S01]untimed", Error: "Timestamp fallback failed: no timed speech", Backend: "faster-whisper-small"}
	_, err = transcribeChunk(context.Background(), runner, Options{MaxTokens: 4096}, "", t.TempDir(), "failed", 180, 45, 0, func(float64, float64, int) ([]Segment, error) {
		t.Fatal("fallback error retried as MOSS")
		return nil, nil
	})
	if err == nil {
		t.Fatal("fallback failure accepted as silence")
	}
}
