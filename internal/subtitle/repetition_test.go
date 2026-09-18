package subtitle

import (
	"context"
	"strings"
	"testing"
)

func TestRepetitionGuard(t *testing.T) {
	var segments []Segment
	for i := 0; i < 88; i++ {
		segments = append(segments, Segment{Start: float64(i) * .5, End: float64(i+1) * .5, Text: "你[S01]的声音。", Speaker: "S01"})
	}
	segments[0].Text = "你[S01]的[S01]声音。"
	if !repetitiveTranscript(segments) {
		t.Fatal("loop not detected")
	}
	if repetitiveTranscript(segments[:3]) {
		t.Fatal("ordinary repetition rejected")
	}
	spaced := append([]Segment(nil), segments...)
	for i := range spaced {
		spaced[i].Start = float64(i) * 5
		spaced[i].End = spaced[i].Start + .5
	}
	if repetitiveTranscript(spaced) {
		t.Fatal("separate utterances rejected")
	}
	calls := 0
	retry := func(start, length float64, depth int) ([]Segment, error) {
		calls++
		return []Segment{{Start: start, End: start + 1, Text: "ok"}}, nil
	}
	result := Result{Text: "loop", Segments: segments, GeneratedTokens: 1900}
	_, err := transcribeChunk(context.Background(), fakeTranscriber{result: result}, Options{MaxTokens: 2048}, "", t.TempDir(), "loop", 0, 45, 2, retry)
	if err != nil || calls != 2 {
		t.Fatalf("retry %d %v", calls, err)
	}
	_, err = transcribeChunk(context.Background(), fakeTranscriber{result: result}, Options{MaxTokens: 2048}, "", t.TempDir(), "limit", 0, 11.25, 4, retry)
	if err == nil || !strings.Contains(err.Error(), "suspicious repeated transcript") {
		t.Fatal("loop accepted", err)
	}
	cleaned := MergeRepeatedCues(segments[:1])
	if cleaned[0].Text != "你的声音。" || segments[0].Text == cleaned[0].Text {
		t.Fatal("marker cleanup mutated raw output")
	}
}
