package subtitle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkDirectoryRetainsPreviousAttempt(t *testing.T) {
	root := t.TempDir()
	first, err := createWorkDir(root, "file-123")
	if err != nil || first != filepath.Join(root, "file-123") {
		t.Fatalf("%s %v", first, err)
	}
	artifact := filepath.Join(first, "subtitle.srt")
	if err := os.WriteFile(artifact, []byte("review me"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := createWorkDir(root, "file-123")
	if err != nil || filepath.Dir(second) != first {
		t.Fatalf("%s %v", second, err)
	}
	data, err := os.ReadFile(artifact)
	if err != nil || string(data) != "review me" {
		t.Fatal("previous result changed")
	}
	for _, id := range []string{"../outside", `a\b`, "a/b", "CON"} {
		if _, err := createWorkDir(root, id); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
}
