package jobqueue

import (
	"context"
	"go.mongodb.org/mongo-driver/bson"
	"math"
	"time"
)

type jobProgress struct {
	timeline bson.M
	active   string
}

func newJobProgress(now time.Time) *jobProgress {
	p := &jobProgress{timeline: bson.M{}, active: "prepare"}
	for _, step := range []string{"prepare", "download", "transcribe", "upload", "install"} {
		p.timeline[step] = bson.M{"status": "pending", "percent": 0.0}
	}
	p.timeline["prepare"].(bson.M)["status"] = "processing"
	p.timeline["prepare"].(bson.M)["startedAt"] = now
	return p
}
func (p *jobProgress) advance(stage string, percent float64, now time.Time) bson.M {
	percent = math.Max(0, math.Min(100, percent))
	step := stage
	value := percent
	overall := percent
	switch stage {
	case "download_audio":
		step = "download"
		value = percent * 10
	case "transcribe":
		value = math.Max(0, math.Min(100, (percent-10)/85*100))
		overall = 10 + value*.8
	case "export_local":
		step = "transcribe"
		value = 100
		overall = 90
	case "upload":
		overall = 90 + percent*.08
	case "install":
		overall = 98 + percent*.02
	}
	if _, ok := p.timeline[step]; !ok {
		return bson.M{}
	}
	if p.active != step {
		old := p.timeline[p.active].(bson.M)
		old["status"] = "completed"
		old["percent"] = 100.0
		if old["endedAt"] == nil {
			old["endedAt"] = now
		}
		p.active = step
	}
	current := p.timeline[step].(bson.M)
	if current["startedAt"] == nil {
		current["startedAt"] = now
	}
	current["status"] = "processing"
	current["percent"] = math.Min(value, 100)
	// Upload completes only after object verification, not just byte transfer.
	if value >= 100 {
		current["status"] = "completed"
		current["endedAt"] = now
	}
	return bson.M{"timeline": p.timeline, "overallPercent": overall, "testStage": stage, "updatedAt": now}
}
func (q *Queue) reportProgress(ctx context.Context, job *Job, worker, stage string, percent float64) error {
	if job.progress == nil {
		job.progress = newJobProgress(time.Now())
	}
	fields := job.progress.advance(stage, percent, time.Now())
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := q.db.Collection("video_process").UpdateOne(c, ownerFilter(job, worker), bson.M{"$set": fields})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrLeaseLost
	}
	return nil
}
func (p *jobProgress) skipUpload(now time.Time) {
	p.advance("upload", 100, now)
	p.timeline["upload"].(bson.M)["note"] = "No speech; upload not required"
}
func completeTimeline(update bson.M, job *Job, noSpeech bool) {
	if job.progress == nil {
		return
	}
	now := time.Now()
	if noSpeech && job.progress.active != "install" {
		job.progress.skipUpload(now)
	}
	job.progress.advance("install", 100, now)
	update["$set"].(bson.M)["timeline"] = job.progress.timeline
}
