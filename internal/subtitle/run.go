package subtitle

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"worker-subtitle/moss"
)

type Options struct {
	checkpointDir                                   string
	OnComplete                                      func(noSpeech bool)
	Observe                                         func(RequestEvent)
	FileID                                          string
	QueueJobID                                      string
	Progress                                        func(string, float64)
	AudioURL, AudioFile, MediaID, OutputDir, Prompt string
	Python, MossScript, ModelDir                    string
	ChunkSeconds, MaxTokens                         int
}

func (o Options) Validate() error {
	count := 0
	for _, v := range []string{o.AudioURL, o.AudioFile, o.MediaID} {
		if v != "" {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("specify exactly one of --audio-url, --audio-file or --media-id")
	}
	if o.ChunkSeconds < 30 || o.ChunkSeconds > 180 {
		return fmt.Errorf("chunk-seconds must be 30-180")
	}
	if o.MaxTokens < 128 || o.MaxTokens > 8192 {
		return fmt.Errorf("max-new-tokens must be 128-8192")
	}
	if o.AudioURL != "" {
		u, err := url.Parse(o.AudioURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("audio-url must be an HTTP(S) URL")
		}
	}
	return nil
}

// Run is deliberately test-only: it never claims queue jobs, uploads results,
// updates database records or removes input, chunks, logs or outputs.
func Run(ctx context.Context, opts Options) (dir string, runErr error) {
	return run(ctx, opts, nil)
}

func RunWithRuntime(ctx context.Context, opts Options, runtime Runtime) (string, error) {
	return run(ctx, opts, runtime)
}

func run(ctx context.Context, opts Options, runtime Runtime) (dir string, runErr error) {
	opts.Prompt = effectivePrompt(opts.Prompt)
	if err := opts.Validate(); err != nil {
		return "", err
	}
	for _, tool := range []string{"ffmpeg", "ffprobe", opts.Python} {
		if _, err := exec.LookPath(tool); err != nil {
			return "", fmt.Errorf("required executable %s: %w", tool, err)
		}
	}
	for _, p := range []string{opts.ModelDir, opts.MossScript} {
		if _, err := os.Stat(p); err != nil {
			return "", err
		}
	}
	root, err := filepath.Abs(opts.OutputDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return "", err
	}
	dir, err = createWorkDir(root, opts.FileID)
	if err != nil {
		return "", err
	}
	log.Printf("[subtitle] Working directory: %s; intermediate files will be cleaned after successful transcription", dir)
	manifest := map[string]any{"mode": "test", "status": "running", "startedAt": time.Now().UTC(), "chunkSeconds": opts.ChunkSeconds, "maxNewTokens": opts.MaxTokens, "prompt": opts.Prompt, "modelDir": opts.ModelDir, "mediaId": opts.MediaID}
	manifest["queueJobId"] = opts.QueueJobID
	manifest["fileId"] = opts.FileID
	progress := func(stage string, percent float64) {
		if opts.Progress != nil {
			opts.Progress(stage, percent)
		}
	}
	// Do not persist signed URL query strings or credentials in manifests.
	defer func() {
		manifest["finishedAt"] = time.Now().UTC()
		manifest["status"] = "completed"
		if runErr != nil {
			manifest["status"] = "failed"
			manifest["error"] = runErr.Error()
		}
		if err := writeJSON(filepath.Join(dir, "job.json"), manifest); err != nil {
			runErr = fmt.Errorf("%v; save job manifest: %w", runErr, err)
		}
		if runErr == nil {
			if err := CompactCompletedRun(dir); err != nil {
				runErr = fmt.Errorf("subtitles saved; cleanup failed: %w", err)
			} else {
				log.Printf("[subtitle] Cleanup completed at %s; retained job.json, segments.json and subtitle.vtt", dir)
			}
		}
		if runErr == nil && opts.OnComplete != nil {
			opts.OnComplete(manifest["noSpeech"] == true)
		}
	}()
	if err := writeJSON(filepath.Join(dir, "job.json"), manifest); err != nil {
		return dir, err
	}
	source := filepath.Join(dir, "source.audio")
	progress("download_audio", 0)
	if err := acquireAudio(ctx, opts, source); err != nil {
		return dir, err
	}
	duration, err := probeDuration(ctx, source)
	if err != nil {
		return dir, err
	}
	manifest["durationSeconds"] = duration
	checkpointRoot := dir
	if opts.FileID != "" {
		checkpointRoot = filepath.Join(root, opts.FileID)
	}
	opts.checkpointDir, err = prepareCheckpoints(ctx, checkpointRoot, source, opts)
	if err != nil {
		return dir, err
	}
	manifest["checkpointDir"] = opts.checkpointDir
	progress("transcribe", 10)
	mossLog, err := os.Create(filepath.Join(dir, "moss.log"))
	if err != nil {
		return dir, err
	}
	defer mossLog.Close()
	var runner transcriber = runtime
	if runtime == nil {
		log.Print("Loading MOSS once; model startup details go to moss.log")
		owned, err := startService(ctx, opts, mossLog)
		if err != nil {
			return dir, err
		}
		defer owned.Close()
		runner = owned
	}
	chunksDir := filepath.Join(dir, "chunks")
	if err := os.MkdirAll(chunksDir, 0755); err != nil {
		return dir, err
	}
	all := []Segment{}
	for start := 0.0; start < duration; start += float64(opts.ChunkSeconds) {
		length := math.Min(float64(opts.ChunkSeconds), duration-start)
		log.Printf("Transcribing %.1f–%.1fs / %.1fs", start, start+length, duration)
		segments, err := processChunk(ctx, runner, opts, source, chunksDir, start, length, 0)
		if err != nil {
			return dir, err
		}
		all = append(all, segments...)
		progress("transcribe", 10+85*(start+length)/duration)
		// Checkpoints remain readable if a later chunk fails or the worker stops.
		if err := writeJSON(filepath.Join(dir, "segments.partial.json"), all); err != nil {
			return dir, err
		}
	}
	manifest["noSpeech"] = len(all) == 0
	if len(all) == 0 {
		log.Print("No speech returned by MOSS — finished with zero subtitle cues")
	}
	if err := writeJSON(filepath.Join(dir, "segments.json"), all); err != nil {
		return dir, err
	}
	if err := writeSubtitles(dir, all); err != nil {
		return dir, err
	}
	manifest["segments"] = len(all)
	progress("export_local", 100)
	return dir, nil
}

func effectivePrompt(prompt string) string {
	if strings.TrimSpace(prompt) == "" {
		return strings.TrimSpace(moss.DefaultPrompt)
	}
	return strings.TrimSpace(prompt)
}

func processChunk(ctx context.Context, runner transcriber, opts Options, source, dir string, start, length float64, depth int) ([]Segment, error) {
	name := fmt.Sprintf("%012d-%012d", int64(math.Round(start*1000)), int64(math.Round(length*1000)))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if saved, ok := loadChunkCheckpoint(opts.checkpointDir, name, start, length); ok {
		log.Printf("[subtitle] Reusing completed chunk %.1f–%.1fs", start, start+length)
		return saved, nil
	}
	audio := filepath.Join(dir, name+".wav")
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-ss", fmt.Sprintf("%.6f", start), "-i", source, "-t", fmt.Sprintf("%.6f", length), "-map", "0:a:0", "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", audio)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg chunk: %w: %s", err, out)
	}
	segments, err := transcribeChunk(ctx, runner, opts, audio, dir, name, start, length, depth, func(s, l float64, d int) ([]Segment, error) {
		return processChunk(ctx, runner, opts, source, dir, s, l, d)
	})
	if err != nil {
		return nil, err
	}
	if err := saveChunkCheckpoint(opts.checkpointDir, name, start, length, segments); err != nil {
		return nil, err
	}
	log.Printf("[subtitle] Saved completed chunk %.1f–%.1fs (%d segments)", start, start+length, len(segments))
	return segments, nil
}

func transcribeChunk(ctx context.Context, runner transcriber, opts Options, audio, dir, name string, start, length float64, depth int, retry func(float64, float64, int) ([]Segment, error)) ([]Segment, error) {
	result, err := runner.Transcribe(ctx, audio, opts.Prompt, opts.MaxTokens)
	if err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(dir, name+".json"), result); err != nil {
		return nil, err
	}
	if result.Backend == "faster-whisper-small" {
		log.Printf("[subtitle] Timestamp fallback for %.1f–%.1fs: %d timed segments", start, start+length, len(result.Segments))
	}
	if result.Backend == "moss-constrained" {
		log.Printf("[subtitle] MOSS constrained decoding for %.1f–%.1fs: %d timed segments", start, start+length, len(result.Segments))
	}
	if result.Error == "" && !result.Retryable && result.GeneratedTokens < opts.MaxTokens && len(result.Segments) == 0 && !noSpeechTranscript(result.Text) {
		log.Printf("Retry chunk %.1f–%.1fs with explicit numeric timestamp format", start, start+length)
		formatPrompt := effectivePrompt(opts.Prompt) + "\n\nEvery spoken segment MUST include BOTH numeric start and end timestamps in seconds relative to this audio chunk. Required syntax: [0.00][S01]spoken words[1.50]. Replace the example words and times with the actual speech and timing. Never output speaker-only lines or omit timestamps. Do not copy this example into the transcript."
		result, err = runner.Transcribe(ctx, audio, formatPrompt, opts.MaxTokens)
		if err != nil {
			return nil, err
		}
		if err := writeJSON(filepath.Join(dir, name+"-format-retry.json"), result); err != nil {
			return nil, err
		}
	}
	truncated := result.GeneratedTokens >= opts.MaxTokens
	if !truncated && result.Error == "" && !result.Retryable && len(result.Segments) == 0 && noSpeechTranscript(result.Text) {
		log.Printf("No speech returned in chunk %.1f–%.1fs; continuing", start, start+length)
		return []Segment{}, nil
	}
	missingTranscript := result.Error == "" && strings.TrimSpace(result.Text) != "" && len(result.Segments) == 0
	repetitive := repetitiveTranscript(result.Segments)
	if truncated || result.Retryable || missingTranscript || repetitive {
		maxDepth, minSplitLength := 2, 60.0
		reason := "unparseable transcript"
		if truncated || result.Retryable || repetitive {
			// Give looping generation/OOM a smaller audio window, while keeping
			// retries bounded and never accepting an incomplete transcript.
			maxDepth, minSplitLength = 4, 20
			reason = fmt.Sprintf("token limit reached (%d/%d)", result.GeneratedTokens, opts.MaxTokens)
			if repetitive && !truncated {
				reason = "suspicious repeated transcript"
			}
			if result.Retryable {
				reason = "GPU out of memory: " + result.Error
			}
		}
		if depth >= maxDepth || length < minSplitLength {
			if missingTranscript && !truncated {
				return nil, fmt.Errorf("MOSS transcript has no parseable timed segments at %.1fs (%.1fs chunk); timestamps may be missing or malformed; raw result retained", start, length)
			}
			return nil, fmt.Errorf("chunk at %.1fs (%.2fs audio): %s after %d split retries; raw result retained", start, length, reason, depth)
		}
		if retry == nil {
			return nil, fmt.Errorf("unparseable MOSS transcript at %.1fs; raw result retained", start)
		}
		log.Printf("Retry chunk %.1f–%.1fs: %s; splitting into two %.2fs chunks (depth %d/%d)", start, start+length, reason, length/2, depth+1, maxDepth)
		first, err := retry(start, length/2, depth+1)
		if err != nil {
			return nil, err
		}
		second, err := retry(start+length/2, length/2, depth+1)
		if err != nil {
			return nil, err
		}
		return append(first, second...), nil
	}
	if result.Error != "" {
		return nil, fmt.Errorf("MOSS: %s", result.Error)
	}
	if strings.TrimSpace(result.Text) != "" && len(result.Segments) == 0 {
		return nil, fmt.Errorf("unparseable MOSS transcript at %.1fs; raw result retained", start)
	}
	return offsetSegments(result.Segments, start, length)
}
func probeDuration(ctx context.Context, source string) (float64, error) {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", source).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe: %w", err)
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 {
		return 0, fmt.Errorf("invalid audio duration")
	}
	return duration, nil
}
func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}
func copyAudio(ctx context.Context, source, dest string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	buf := make([]byte, 1024*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := in.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	return out.Close()
}

// Accept empty output or complete timestamp/speaker-only segments. Arbitrary
// malformed text, truncation and model errors must not be mistaken for silence.
var emptySpeechSegments = regexp.MustCompile(`^(\s*\[[0-9]+(\.[0-9]+)?\]\s*\[S[0-9]+\]\s*\[[0-9]+(\.[0-9]+)?\]\s*)+$`)

func noSpeechTranscript(text string) bool {
	return strings.TrimSpace(text) == "" || emptySpeechSegments.MatchString(text)
}
