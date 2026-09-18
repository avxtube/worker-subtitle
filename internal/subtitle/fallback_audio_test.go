package subtitle

import (
	"context"
	"github.com/joho/godotenv"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"worker-subtitle/internal/config"
)

// Opt-in read-only source download for reproducing timestamp fallback failures.
// Does not claim jobs or write database/storage records.
func TestPrepareTimestampFallbackAudio(t *testing.T) {
	mediaID := os.Getenv("SUBTITLE_FALLBACK_TEST_MEDIA")
	if mediaID == "" {
		t.Skip("opt-in real audio fixture")
	}
	if err := godotenv.Load("../../.env"); err != nil {
		t.Fatal("config unavailable")
	}
	config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	root, err := filepath.Abs("../../.build/diagnostics/timestamp-fallback")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source.audio")
	if err = acquireAudio(ctx, Options{MediaID: mediaID}, source); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "180-225.wav")
	if out, err := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-y", "-ss", "180", "-i", source, "-t", "45", "-ac", "1", "-ar", "16000", output).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	t.Log("45-second fallback fixture ready")
}
