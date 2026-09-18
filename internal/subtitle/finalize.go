package subtitle

import (
	"encoding/json"
	"fmt"
	"github.com/abadojack/whatlanggo"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

type LanguageInfo struct {
	Code       string  `json:"code"`
	Name       string  `json:"name"`
	Candidate  string  `json:"candidate,omitempty"`
	Confidence float64 `json:"confidence"`
	Reliable   bool    `json:"reliable"`
	Method     string  `json:"method"`
}

func detectLanguage(segments []Segment) LanguageInfo {
	result := LanguageInfo{Code: "und", Name: "Unknown", Method: "subtitle_text_whatlanggo"}
	var text strings.Builder
	letters := 0
	for _, s := range segments {
		if annotation(s.Text) {
			continue
		}
		text.WriteString(s.Text)
		text.WriteByte(' ')
		for _, r := range s.Text {
			if unicode.IsLetter(r) {
				letters++
			}
		}
	}
	if letters < 20 {
		return result
	}
	info := whatlanggo.Detect(text.String())
	result.Candidate = info.Lang.Iso6391()
	if result.Candidate == "" {
		result.Candidate = info.Lang.Iso6393()
	}
	result.Confidence = info.Confidence
	result.Reliable = info.IsReliable()
	if result.Reliable {
		result.Code = result.Candidate
		result.Name = info.Lang.String()
	}
	return result
}

// CompactCompletedRun removes only known intermediate artifacts from a completed
// local run, after confirming the three retained results exist. Never follows
// a symlink/junction as the run or chunks directory; failed runs remain intact.
func CompactCompletedRun(dir string) error {
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid run directory")
	}
	for _, name := range []string{"job.json", "segments.json", "subtitle.vtt"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("invalid retained artifact %s", name)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "job.json"))
	if err != nil {
		return err
	}
	var manifest map[string]any
	if err = json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	if manifest["status"] != "completed" {
		return fmt.Errorf("only completed local runs may be compacted")
	}
	data, err = os.ReadFile(filepath.Join(root, "segments.json"))
	if err != nil {
		return err
	}
	var segments []Segment
	if err = json.Unmarshal(data, &segments); err != nil {
		return err
	}
	manifest["language"] = detectLanguage(segments)
	manifest["subtitleCues"] = len(MergeRepeatedCues(segments))
	manifest["mergedCues"] = len(segments) - len(MergeRepeatedCues(segments))
	manifest["deduplication"] = "overlapping_same_speaker_and_repeated_sound_cycles"
	manifest["noSpeech"] = len(segments) == 0
	if err = writeJSON(filepath.Join(root, "job.json"), manifest); err != nil {
		return err
	}
	for _, name := range []string{"source.audio", "source.audio.part", "subtitle.srt", "segments.partial.json", "moss.log", "chunks"} {
		target := filepath.Join(root, name)
		rel, err := filepath.Rel(root, target)
		if err != nil || !filepath.IsLocal(rel) {
			return fmt.Errorf("cleanup outside run directory")
		}
		info, err := os.Lstat(target)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing linked artifact %s", name)
		}
		if name == "chunks" {
			err = os.RemoveAll(target)
		} else if info.IsDir() {
			return fmt.Errorf("unexpected artifact directory %s", name)
		} else {
			err = os.Remove(target)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
