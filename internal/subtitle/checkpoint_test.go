package subtitle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointResumesWithoutAudioExtractionOrInference(t *testing.T) {
	dir := t.TempDir()
	segments := []Segment{{Start: 180.5, End: 181, Text: "hello", Speaker: "180:S01"}}
	if err := saveChunkCheckpoint(dir, "000000180000-000000180000", 180, 180, segments); err != nil {
		t.Fatal(err)
	}
	// Both the audio and transcriber would fail if the cached chunk were rerun.
	got, err := processChunk(context.Background(), fakeTranscriber{err: errors.New("must not run")}, Options{checkpointDir: dir}, "missing audio", t.TempDir(), 180, 180, 0)
	if err != nil || len(got) != 1 || got[0].Start != 180.5 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, ok := loadChunkCheckpoint(dir, "000000180000-000000180000", 180, 90); ok {
		t.Fatal("wrong chunk bounds reused")
	}
	if err := saveChunkCheckpoint(dir, "silent", 0, 45, nil); err != nil {
		t.Fatal(err)
	}
	if got, ok := loadChunkCheckpoint(dir, "silent", 0, 45); !ok || len(got) != 0 {
		t.Fatal("silent success not reusable")
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{"status":"completed"`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadChunkCheckpoint(dir, "broken", 0, 45); ok {
		t.Fatal("partial checkpoint accepted")
	}
}

func TestCheckpointIdentityIncludesAudioAndSettings(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "audio")
	if err := os.WriteFile(source, []byte("audio one"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{Prompt: "first", MaxTokens: 2048, ChunkSeconds: 180, ModelDir: "model"}
	first, err := prepareCheckpoints(context.Background(), root, source, opts)
	if err != nil {
		t.Fatal(err)
	}
	same, err := prepareCheckpoints(context.Background(), root, source, opts)
	if err != nil || same != first {
		t.Fatal("unstable identity")
	}
	opts.Prompt = "changed"
	changed, err := prepareCheckpoints(context.Background(), root, source, opts)
	if err != nil || changed == first {
		t.Fatal("prompt change ignored")
	}
	opts.Prompt = "first"
	if err := os.WriteFile(source, []byte("audio two"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err = prepareCheckpoints(context.Background(), root, source, opts)
	if err != nil || changed == first {
		t.Fatal("audio change ignored")
	}
}
