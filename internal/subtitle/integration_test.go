package subtitle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Enable with MOSS_TEST_PYTHON pointing at Python. This uses real FFmpeg and
// subprocess pipes, but a fake model; it needs no weights, GPU or database.
func TestPipelineWithFakeModel(t *testing.T) {
	python := os.Getenv("MOSS_TEST_PYTHON")
	if python == "" {
		t.Skip("set MOSS_TEST_PYTHON for process integration tests")
	}
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " unavailable")
		}
	}
	root := t.TempDir()
	source := filepath.Join(root, "input.wav")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-y", source).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %s %v", out, err)
	}
	script := filepath.Join(root, "fake.py")
	body := `import sys,json
print(json.dumps({"ready":True}),flush=True)
for line in sys.stdin:
    req=json.loads(line)
    assert req['audio'].endswith('.wav')
    assert req['prompt'] == '\u65e5\u672c\u8a9e'
    print(json.dumps({"text":"[0.1][S01]test[1.0]","generated_tokens":10,"segments":[{"start":0.1,"end":1.0,"speaker":"S01","text":"test"}]}),flush=True)
`
	if err := os.WriteFile(script, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	opts := Options{AudioFile: source, OutputDir: filepath.Join(root, "runs"), Python: python, MossScript: script, ModelDir: root, ChunkSeconds: 180, MaxTokens: 2048, Prompt: "日本語"}
	dir, err := Run(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"subtitle.vtt", "segments.json", "job.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("artifact %s missing: %v", name, err)
		}
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("original audio removed")
	}
	second, err := Run(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if second == dir {
		t.Fatal("new run overwrote prior run")
	}
	if _, err := os.Stat(filepath.Join(dir, "subtitle.vtt")); err != nil {
		t.Fatal("prior run removed")
	}
}

func TestServiceCancellation(t *testing.T) {
	python := os.Getenv("MOSS_TEST_PYTHON")
	if python == "" {
		t.Skip("set MOSS_TEST_PYTHON")
	}
	root := t.TempDir()
	script := filepath.Join(root, "slow.py")
	body := "import json,sys,time\nprint(json.dumps({'ready':True}),flush=True)\nfor line in sys.stdin:\n time.sleep(120)\n"
	if err := os.WriteFile(script, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(root, "moss.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	service, err := startService(context.Background(), Options{Python: python, MossScript: script, ModelDir: root}, logFile)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := service.Transcribe(ctx, "unused", "", 2048); err == nil {
		t.Fatal("cancelled request succeeded")
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("child process did not stop promptly")
	}
}
