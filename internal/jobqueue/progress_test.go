package jobqueue

import (
	"go.mongodb.org/mongo-driver/bson"
	"testing"
	"time"
)

func TestQueueProgressTimeline(t *testing.T) {
	start := time.Now()
	p := newJobProgress(start)
	for i, tt := range []struct {
		stage, step             string
		input, percent, overall float64
	}{
		{"download_audio", "download", 5, 50, 5},
		{"transcribe", "transcribe", 10, 0, 10},
		{"transcribe", "transcribe", 52.5, 50, 50},
		{"export_local", "transcribe", 100, 100, 90},
		{"upload", "upload", 0, 0, 90},
		{"upload", "upload", 100, 100, 98},
		{"install", "install", 0, 0, 98},
	} {
		fields := p.advance(tt.stage, tt.input, start.Add(time.Duration(i+1)*time.Second))
		step := p.timeline[tt.step].(bson.M)
		if fields["overallPercent"] != tt.overall || step["percent"] != tt.percent || step["startedAt"] == nil {
			t.Fatalf("%s: %v", tt.stage, fields)
		}
	}
	update := settleUpdate("work", nil, false, time.Now())
	completeTimeline(update, &Job{progress: p}, false)
	for name, raw := range p.timeline {
		step := raw.(bson.M)
		if step["status"] != "completed" || step["percent"] != 100.0 || step["startedAt"] == nil || step["endedAt"] == nil {
			t.Fatalf("unfinished %s: %v", name, step)
		}
	}
}

func TestNoSpeechTimelinePreservesInstallStart(t *testing.T) {
	now := time.Now()
	p := newJobProgress(now)
	p.advance("download_audio", 0, now)
	p.advance("transcribe", 10, now)
	p.advance("export_local", 100, now)
	p.skipUpload(now)
	p.advance("install", 0, now)
	update := settleUpdate("work", nil, false, now)
	completeTimeline(update, &Job{progress: p}, true)
	if p.timeline["upload"].(bson.M)["note"] == nil || p.timeline["install"].(bson.M)["startedAt"] != now {
		t.Fatal(p.timeline)
	}
}
