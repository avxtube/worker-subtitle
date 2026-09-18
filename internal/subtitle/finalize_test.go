package subtitle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompactOnlyCompletedRun(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"source.audio", "subtitle.srt", "subtitle.vtt", "moss.log", "segments.partial.json"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "chunks"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(root, "segments.json"), []Segment{{Text: "This is an English sentence about the weather and the people walking outside in the beautiful sunshine."}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(root, "job.json"), map[string]any{"status": "failed"}); err != nil {
		t.Fatal(err)
	}
	if err := CompactCompletedRun(root); err == nil {
		t.Fatal("failed run cleaned")
	}
	if _, err := os.Stat(filepath.Join(root, "source.audio")); err != nil {
		t.Fatal("failed input deleted")
	}
	if err := writeJSON(filepath.Join(root, "job.json"), map[string]any{"status": "completed"}); err != nil {
		t.Fatal(err)
	}
	if err := CompactCompletedRun(root); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 3 {
		t.Fatalf("retained %v %v", entries, err)
	}
	if got := detectLanguage(nil); got.Code != "und" {
		t.Fatal(got)
	}
	if got := detectLanguage([]Segment{{Text: "I"}}); got.Code != "und" {
		t.Fatal(got)
	}
	if got := detectLanguage([]Segment{{Text: "This is an English sentence about the weather and the people walking outside in the beautiful sunshine."}}); got.Code != "en" {
		t.Fatal(got)
	}
}
