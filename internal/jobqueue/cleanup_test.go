package jobqueue

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanSettledWork(t *testing.T) {
	for _, status := range []string{"completed", "failed", "running"} {
		t.Run(status, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "file")
			run := filepath.Join(target, "attempt-1")
			if err := os.MkdirAll(run, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "job.json"), []byte(`{"status":"failed"}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(run, "job.json"), []byte(`{"status":"`+status+`"}`), 0600); err != nil {
				t.Fatal(err)
			}
			sibling := filepath.Join(root, "other")
			for _, dir := range []string{target, run} {
				for _, name := range []string{"subtitle.vtt", "segments.json", "source.audio"} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte("comparison"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Mkdir(filepath.Join(dir, "chunks"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "chunks", "raw.json"), []byte(`{"text":"[S01]hello"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(sibling, 0700); err != nil {
				t.Fatal(err)
			}
			err := cleanSettledWork(root, "file", run)
			if status == "running" {
				if err == nil {
					t.Fatal("deleted active run")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				for _, dir := range []string{target, run} {
					if data, err := os.ReadFile(filepath.Join(dir, "moss-results.json")); err != nil || len(data) == 0 {
						t.Fatal("raw result lost", err)
					}
					for _, name := range []string{"source.audio", "chunks"} {
						if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
							t.Fatal("intermediate retained", name, err)
						}
					}
				}
			}
			for _, dir := range []string{target, run} {
				for _, name := range []string{"job.json", "segments.json", "subtitle.vtt"} {
					if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
						t.Fatal("comparison file removed", name, err)
					}
				}
			}
			if _, err := os.Stat(sibling); err != nil {
				t.Fatal("sibling affected", err)
			}
		})
	}
}

func TestCleanSettledWorkRejectsOutsidePaths(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"..", ".", "../outside"} {
		if err := cleanSettledWork(root, id, root); err == nil {
			t.Fatal("unsafe ID", id)
		}
	}
	if err := cleanSettledWork(root, "file", t.TempDir()); err == nil {
		t.Fatal("outside run accepted")
	}
}
