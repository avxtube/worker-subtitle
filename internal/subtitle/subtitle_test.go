package subtitle

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOptionsRequireOneSource(t *testing.T) {
	o := Options{ChunkSeconds: 180, MaxTokens: 2048}
	if o.Validate() == nil {
		t.Fatal("missing input accepted")
	}
	o.AudioFile = "audio.m4a"
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	o.AudioURL = "https://example.com/audio"
	if o.Validate() == nil {
		t.Fatal("ambiguous inputs accepted")
	}
	o.AudioFile = ""
	o.AudioURL = "file:///etc/passwd"
	if o.Validate() == nil {
		t.Fatal("non HTTP URL accepted")
	}
}
func TestOffsetsAndExport(t *testing.T) {
	segments, err := offsetSegments([]Segment{{Start: 0.25, End: 1.5, Speaker: "S01", Text: "こんにちは <test>"}}, 180, 90)
	if err != nil {
		t.Fatal(err)
	}
	if segments[0].Start != 180.25 || segments[0].End != 181.5 {
		t.Fatalf("wrong offsets: %+v", segments)
	}
	srt, vtt := renderSubtitles(segments)
	if !strings.Contains(srt, "00:03:00,250 --> 00:03:01,500") || !strings.Contains(vtt, "WEBVTT\n\n00:03:00.250") {
		t.Fatalf("wrong output: %s / %s", srt, vtt)
	}
	if !strings.Contains(vtt, "こんにちは &lt;test&gt;") {
		t.Fatal("unicode/escaping corrupted")
	}
	if got := timestamp(3599.9996, "."); got != "01:00:00.000" {
		t.Fatalf("rounding rollover: %s", got)
	}
	for _, s := range []Segment{{Start: 2, End: 1}, {Start: -1, End: 1}, {Start: 0, End: math.NaN()}, {Start: 0, End: 999}} {
		if _, err := offsetSegments([]Segment{s}, 0, 180); err == nil {
			t.Fatalf("bad timestamp accepted: %+v", s)
		}
	}
}
func TestDownloadRetainsSourceAndPriorFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("audio bytes")) }))
	defer srv.Close()
	dir := t.TempDir()
	prior := filepath.Join(dir, "prior.srt")
	_ = os.WriteFile(prior, []byte("keep"), 0644)
	dest := filepath.Join(dir, "source.audio")
	if err := downloadAudio(context.Background(), srv.URL, dest); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(dest)
	if string(data) != "audio bytes" {
		t.Fatal("download changed")
	}
	if _, err := os.Stat(prior); err != nil {
		t.Fatal("prior output deleted")
	}
}
func TestPartialDownloadIsRetained(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("partial"))
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "audio")
	if err := downloadAudio(context.Background(), srv.URL, dest); err == nil {
		t.Fatal("truncated download accepted")
	}
	data, err := os.ReadFile(dest + ".part")
	if err != nil || string(data) != "partial" {
		t.Fatalf("partial not retained: %q %v", data, err)
	}
}

type fakeTranscriber struct {
	result Result
	err    error
}

func (f fakeTranscriber) Transcribe(context.Context, string, string, int) (Result, error) {
	return f.result, f.err
}
func TestTruncatedChunkRetryPreservesRawAndOffsets(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	runner := fakeTranscriber{result: Result{Text: "raw repeated text", GeneratedTokens: 2048}}
	retry := func(start, length float64, depth int) ([]Segment, error) {
		calls++
		if length != 90 || depth != 1 {
			t.Fatal("wrong retry bounds")
		}
		return []Segment{{Start: start, End: start + 1, Text: "ok"}}, nil
	}
	segments, err := transcribeChunk(context.Background(), runner, Options{MaxTokens: 2048}, "unused", dir, "chunk", 180, 180, 0, retry)
	if err != nil || calls != 2 || segments[1].Start != 270 {
		t.Fatalf("retry: %+v %v", segments, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "chunk.json"))
	if err != nil || !strings.Contains(string(raw), "raw repeated text") {
		t.Fatal("raw failed chunk missing")
	}
	_, err = transcribeChunk(context.Background(), runner, Options{MaxTokens: 2048}, "unused", dir, "limit", 0, 11.25, 4, retry)
	if err == nil {
		t.Fatal("unbounded retries")
	}
}
func TestCancellationDoesNotRetry(t *testing.T) {
	_, err := transcribeChunk(context.Background(), fakeTranscriber{err: context.Canceled}, Options{}, "", t.TempDir(), "cancel", 0, 180, 0, func(float64, float64, int) ([]Segment, error) { t.Fatal("cancel retried"); return nil, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestTokenLimitAndOOMRetryBelow45Seconds(t *testing.T) {
	for _, result := range []Result{
		{Text: "loop", GeneratedTokens: 2048},
		{Error: "CUDA allocation failed", Retryable: true},
	} {
		calls := 0
		retry := func(start, length float64, depth int) ([]Segment, error) {
			calls++
			if length != 22.5 || depth != 3 {
				t.Fatalf("wrong retry bounds: %v %v", length, depth)
			}
			return []Segment{{Start: start, End: start + 1, Text: "speech"}}, nil
		}
		_, err := transcribeChunk(context.Background(), fakeTranscriber{result: result}, Options{MaxTokens: 2048}, "unused", t.TempDir(), "short", 0, 45, 2, retry)
		if err != nil || calls != 2 {
			t.Fatalf("calls=%d err=%v", calls, err)
		}
		_, err = transcribeChunk(context.Background(), fakeTranscriber{result: result}, Options{MaxTokens: 2048}, "unused", t.TempDir(), "limit", 0, 11.25, 4, retry)
		if err == nil || calls != 2 {
			t.Fatal("retry limit not enforced")
		}
		want := "token limit reached"
		if result.Retryable {
			want = "GPU out of memory"
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatal(err)
		}
	}
}
func TestUnparseableOutputFailsAndIsRetained(t *testing.T) {
	dir := t.TempDir()
	_, err := transcribeChunk(context.Background(), fakeTranscriber{result: Result{Text: "bad format"}}, Options{MaxTokens: 2048}, "", dir, "bad", 0, 180, 0, nil)
	if err == nil {
		t.Fatal("bad transcript accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.json")); err != nil {
		t.Fatal("raw output missing")
	}
}

func TestMalformedOutputRetriesShorterWithoutInventingSubtitles(t *testing.T) {
	result := Result{Text: "malformed transcript", GeneratedTokens: 257}
	calls := 0
	retry := func(start, length float64, depth int) ([]Segment, error) {
		calls++
		if length != 31.3325 || depth != 2 {
			t.Fatalf("wrong split %v %v", length, depth)
		}
		return []Segment{{Start: start, End: start + 1, Text: "real text"}}, nil
	}
	dir := t.TempDir()
	_, err := transcribeChunk(context.Background(), fakeTranscriber{result: result}, Options{MaxTokens: 2048}, "", dir, "timestamp-only", 0, 62.665, 1, retry)
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
	calls = 0
	_, err = transcribeChunk(context.Background(), fakeTranscriber{result: result}, Options{MaxTokens: 2048}, "", dir, "bounded", 0, 31.3325, 2, retry)
	if err == nil || calls != 0 {
		t.Fatal("empty transcript accepted or retried without limit")
	}
}

func TestNoSpeechFinishesChunkWithoutRetry(t *testing.T) {
	for _, text := range []string{"", "  ", "[52.28][S01][52.78]", "[0.00][S01][1.20][3.00][S02][4.00]"} {
		segments, err := transcribeChunk(context.Background(), fakeTranscriber{result: Result{Text: text, GeneratedTokens: 100}}, Options{MaxTokens: 2048}, "", t.TempDir(), "silent", 0, 180, 0, func(float64, float64, int) ([]Segment, error) { t.Fatal("silence retried"); return nil, nil })
		if err != nil || len(segments) != 0 {
			t.Fatalf("%q: %v %v", text, segments, err)
		}
	}
	for _, text := range []string{"[0][S01]words[1]", "[0][S01][1", "bad format"} {
		if noSpeechTranscript(text) {
			t.Fatalf("accepted malformed/nonempty output: %q", text)
		}
	}
}
