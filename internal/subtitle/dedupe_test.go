package subtitle

import "testing"

func TestMergeSoundCyclesPreservesRawAndSpeech(t *testing.T) {
	raw := []Segment{}
	for i := 0; i < 33; i++ {
		raw = append(raw, Segment{Start: 23 + float64(i*2), End: 25 + float64(i*2), Speaker: "S1", Text: []string{"[starting]", "[running]", "[closing]"}[i%3]})
	}
	raw = append(raw, Segment{Start: 100, End: 101, Speaker: "S1", Text: "yes"}, Segment{Start: 101, End: 102, Speaker: "S1", Text: "yes"})
	got := MergeRepeatedCues(raw)
	if len(got) != 3 || got[0].Start != 23 || got[0].End != 89 || got[0].Text != "[starting] / [running] / [closing]" {
		t.Fatal(got)
	}
	if raw[0].End != 25 || len(raw) != 35 {
		t.Fatal("raw mutated")
	}
	raw[3].Start += 2
	if len(MergeRepeatedCues(raw)) <= 3 {
		t.Fatal("gap merged")
	}
}
func TestOverlappingDuplicateSpeaker(t *testing.T) {
	raw := []Segment{{Start: 0, End: 2, Speaker: "S1", Text: "hello"}, {Start: 1, End: 3, Speaker: "S1", Text: "hello"}, {Start: 2, End: 4, Speaker: "S2", Text: "hello"}}
	got := MergeRepeatedCues(raw)
	if len(got) != 2 || got[0].End != 3 {
		t.Fatal(got)
	}
}
