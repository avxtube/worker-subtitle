package jobqueue

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Remove intermediate artifacts while retaining comparison files in every attempt.
// Refuse linked paths and any attempt whose manifest still says running.
func cleanSettledWork(root, fileID, runDir string) error {
	if root == "" || runDir == "" {
		return nil
	}
	if !filepath.IsLocal(fileID) || filepath.Base(fileID) != fileID || fileID == "." {
		return fmt.Errorf("invalid cleanup file ID")
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	target := filepath.Join(base, fileID)
	run, err := filepath.Abs(runDir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(target, run)
	if err != nil || (rel != "." && !filepath.IsLocal(rel)) {
		return fmt.Errorf("run outside file work directory")
	}
	for _, path := range []string{base, target} {
		info, e := os.Lstat(path)
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("linked or invalid work directory")
		}
	}
	var attempts []string
	err = filepath.WalkDir(target, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("linked artifact: %s", path)
		}
		if !entry.IsDir() && strings.EqualFold(entry.Name(), "job.json") {
			data, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			var manifest struct {
				Status string `json:"status"`
			}
			if e = json.Unmarshal(data, &manifest); e != nil {
				return e
			}
			if manifest.Status != "completed" && manifest.Status != "failed" {
				return fmt.Errorf("unfinished attempt: %s", path)
			}
			attempts = append(attempts, filepath.Dir(path))
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, attempt := range attempts {
		// Preserve model responses before deleting audio/chunks, including failed
		// runs that never reached subtitle export.
		chunkFiles, err := filepath.Glob(filepath.Join(attempt, "chunks", "*.json"))
		if err != nil {
			return err
		}
		if len(chunkFiles) > 0 {
			responses := map[string]json.RawMessage{}
			for _, path := range chunkFiles {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if !json.Valid(data) {
					return fmt.Errorf("invalid raw result: %s", path)
				}
				responses[filepath.Base(path)] = data
			}
			data, err := json.MarshalIndent(responses, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(attempt, "moss-results.json"), data, 0644); err != nil {
				return err
			}
		}
		for _, name := range []string{"source.audio", "source.audio.part", "subtitle.srt", "moss.log", "chunks"} {
			artifact := filepath.Join(attempt, name)
			rel, err := filepath.Rel(target, artifact)
			if err != nil || !filepath.IsLocal(rel) {
				return fmt.Errorf("artifact outside work directory")
			}
			if err := os.RemoveAll(artifact); err != nil {
				return err
			}
		}
	}
	return nil
}

func cleanupAfterSettlement(job *Job, root, dir string) {
	if job.FileID == nil || dir == "" {
		return
	}
	if err := cleanSettledWork(root, *job.FileID, dir); err != nil {
		log.Printf("[subtitle] Queue settled; work cleanup failed: %v", err)
	} else {
		log.Printf("[subtitle] Queue settled; cleaned intermediates for file %s; retained job.json, segments.json and subtitle.vtt where available", *job.FileID)
	}
}
