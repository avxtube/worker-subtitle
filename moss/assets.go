package moss

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

// Source travels inside the binary, so copying just windows.exe is sufficient.
//
//go:embed *.py requirements.txt LICENSE-MOSS moss_transcribe_diarize/*.py moss_transcribe_diarize/*.txt moss_transcribe_diarize/app/*.py
var assets embed.FS

//go:embed moss_transcribe_diarize/default_prompt.txt
var DefaultPrompt string

func Install(dir string) error {
	return fs.WalkDir(assets, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		data, err := assets.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
}
