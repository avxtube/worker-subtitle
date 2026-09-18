package subtitle

import (
	"fmt"
	"html"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func offsetSegments(segments []Segment, offset, duration float64) ([]Segment, error) {
	out := make([]Segment, 0, len(segments))
	for _, s := range segments {
		if math.IsNaN(s.Start) || math.IsNaN(s.End) || math.IsInf(s.Start, 0) || math.IsInf(s.End, 0) || s.Start < 0 || s.End <= s.Start || s.Start >= duration || s.End > duration+1 {
			return nil, fmt.Errorf("invalid MOSS timestamp %.3f–%.3f in %.3fs chunk", s.Start, s.End, duration)
		}
		s.Text = strings.TrimSpace(s.Text)
		if s.Text == "" {
			continue
		}
		s.End = math.Min(s.End, duration) + offset
		s.Start += offset
		// Speaker IDs are local to a chunk, not guaranteed identities across chunks.
		s.Speaker = fmt.Sprintf("%.3f:%s", offset, s.Speaker)
		out = append(out, s)
	}
	return out, nil
}
func timestamp(seconds float64, separator string) string {
	ms := int64(math.Round(seconds * 1000))
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", ms/3600000, (ms/60000)%60, (ms/1000)%60, separator, ms%1000)
}
func renderSubtitles(segments []Segment) (string, string) {
	ordered := append([]Segment(nil), segments...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })
	var srt, vtt strings.Builder
	vtt.WriteString("WEBVTT\n\n")
	for i, s := range ordered {
		// Escape markup and flatten embedded newlines so model output cannot break cues.
		text := html.EscapeString(strings.Join(strings.Fields(s.Text), " "))
		fmt.Fprintf(&srt, "%d\n%s --> %s\n%s\n\n", i+1, timestamp(s.Start, ","), timestamp(s.End, ","), text)
		fmt.Fprintf(&vtt, "%s --> %s\n%s\n\n", timestamp(s.Start, "."), timestamp(s.End, "."), text)
	}
	return srt.String(), vtt.String()
}
func writeSubtitles(dir string, segments []Segment) error {
	srt, vtt := renderSubtitles(MergeRepeatedCues(segments))
	if err := os.WriteFile(filepath.Join(dir, "subtitle.srt"), []byte(srt), 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "subtitle.vtt"), []byte(vtt), 0644)
}
