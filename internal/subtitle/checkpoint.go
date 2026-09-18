package subtitle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type chunkCheckpoint struct {
	Start    float64   `json:"start"`
	Length   float64   `json:"length"`
	Status   string    `json:"status"`
	Segments []Segment `json:"segments"`
}

func prepareCheckpoints(ctx context.Context, root, source string, opts Options) (string, error) {
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	hash := sha256.New()
	buffer := make([]byte, 1024*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, e := input.Read(buffer)
		if n > 0 {
			hash.Write(buffer[:n])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", e
		}
	}
	// Bind reuse to actual audio bytes and inference settings, not just file ID.
	settings, _ := json.Marshal([]any{"checkpoint-v1", opts.Prompt, opts.MaxTokens, opts.ChunkSeconds, opts.ModelDir})
	hash.Write(settings)
	dir := filepath.Join(root, "checkpoints", hex.EncodeToString(hash.Sum(nil)))
	return dir, os.MkdirAll(dir, 0755)
}

func loadChunkCheckpoint(dir, name string, start, length float64) ([]Segment, bool) {
	if dir == "" {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if err != nil {
		return nil, false
	}
	var saved chunkCheckpoint
	if json.Unmarshal(data, &saved) != nil || saved.Status != "completed" || saved.Start != start || saved.Length != length || saved.Segments == nil {
		return nil, false
	}
	for _, s := range saved.Segments {
		if s.Start < start || s.End > s.Start+length || s.End > start+length || s.End <= s.Start {
			return nil, false
		}
	}
	return saved.Segments, true
}

func saveChunkCheckpoint(dir, name string, start, length float64, segments []Segment) error {
	if dir == "" {
		return nil
	}
	if segments == nil {
		segments = []Segment{}
	}
	data, err := json.MarshalIndent(chunkCheckpoint{start, length, "completed", segments}, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "checkpoint-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err = temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tempName, filepath.Join(dir, name+".json")); err != nil {
		return fmt.Errorf("save chunk checkpoint: %w", err)
	}
	return nil
}
