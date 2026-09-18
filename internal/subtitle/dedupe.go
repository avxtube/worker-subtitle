package subtitle

import (
	"regexp"
	"sort"
	"strings"
)

var speakerMarker = regexp.MustCompile(`\[S\d+\]`)

func cleanCueText(text string) string {
	return strings.Join(strings.Fields(speakerMarker.ReplaceAllString(text, "")), " ")
}

// Flag long, rapid runs for retry, not as evidence of silence. Ordinary short
// repetitions and identical phrases separated by pauses remain valid.
func repetitiveTranscript(segments []Segment) bool {
	count := 0
	var previous Segment
	for _, s := range segments {
		s.Text = cleanCueText(s.Text)
		if s.Text == "" || annotation(s.Text) || s.End <= s.Start || s.End-s.Start > 2 {
			count = 0
		} else if count > 0 && sameCue(previous, s) && s.Start >= previous.End-0.001 && s.Start-previous.End <= 0.25 {
			count++
		} else {
			count = 1
		}
		if count >= 8 {
			return true
		}
		previous = s
	}
	return false
}

func annotation(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]") && strings.Count(text, "[") == 1 && strings.Count(text, "]") == 1
}
func sameCue(a, b Segment) bool {
	return a.Speaker == b.Speaker && strings.Join(strings.Fields(a.Text), " ") == strings.Join(strings.Fields(b.Text), " ")
}

// MergeRepeatedCues retains raw segments separately. Spoken duplicates merge
// only when their timestamps overlap. Repeated sound annotation cycles must
// have at least three full repetitions with contiguous, same-speaker cues.
func MergeRepeatedCues(source []Segment) []Segment {
	ordered := append([]Segment(nil), source...)
	for i := range ordered {
		ordered[i].Text = cleanCueText(ordered[i].Text)
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })
	out := []Segment{}
	for i := 0; i < len(ordered); {
		bestEnd, bestWidth := i, 0
		if annotation(ordered[i].Text) {
			for width := 1; width <= 8 && i+3*width <= len(ordered); width++ {
				end := i
				for end < len(ordered) {
					s := ordered[end]
					if !annotation(s.Text) || !sameCue(s, ordered[i+(end-i)%width]) {
						break
					}
					if end > i && (s.Start < ordered[end-1].End-0.001 || s.Start-ordered[end-1].End > 0.25) {
						break
					}
					end++
				}
				end = i + (end-i)/width*width
				if end-i >= 3*width && end > bestEnd {
					bestEnd, bestWidth = end, width
				}
			}
		}
		if bestWidth > 0 {
			s := ordered[i]
			texts := []string{}
			seen := map[string]bool{}
			for j := i; j < i+bestWidth; j++ {
				t := strings.TrimSpace(ordered[j].Text)
				if !seen[t] {
					texts = append(texts, t)
					seen[t] = true
				}
			}
			s.Text = strings.Join(texts, " / ")
			s.End = ordered[bestEnd-1].End
			out = append(out, s)
			i = bestEnd
			continue
		}
		s := ordered[i]
		if len(out) > 0 {
			prev := &out[len(out)-1]
			if sameCue(*prev, s) && (s.Start < prev.End || (annotation(s.Text) && s.Start-prev.End <= 0.25)) {
				if s.End > prev.End {
					prev.End = s.End
				}
				i++
				continue
			}
		}
		out = append(out, s)
		i++
	}
	return out
}

func RewriteSubtitles(dir string, raw []Segment) error { return writeSubtitles(dir, raw) }
