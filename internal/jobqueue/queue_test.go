package jobqueue

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
	"testing"
	"time"
	"worker-subtitle/internal/db/models"
	"worker-subtitle/internal/subtitle"
)

func TestLeaseOwnerIncludesToken(t *testing.T) {
	job := &Job{VideoProcess: models.VideoProcess{ID: "job-1"}, LeaseToken: "new-claim"}
	filter := ownerFilter(job, "subtitle_host@1")
	if filter["leaseToken"] != "new-claim" || filter["status"] != "processing" || filter["processType"] != "subtitle" {
		t.Fatalf("unsafe ownership: %v", filter)
	}
	raw, _ := bson.Marshal(bson.M{"_id": "job-1", "fileId": "file-1", "leaseToken": "new-claim"})
	var decoded Job
	if err := bson.Unmarshal(raw, &decoded); err != nil || decoded.ID != "job-1" || decoded.FileID == nil {
		t.Fatalf("queue job decode failed: %+v %v", decoded, err)
	}
}
func TestLocalOnlySettlement(t *testing.T) {
	for _, tt := range []struct {
		err      error
		shutdown bool
		status   string
	}{{nil, false, "completed"}, {errors.New("inference failed"), false, "failed"}, {context.Canceled, true, "pending"}} {
		update := settleUpdate("D:/runs/retained", tt.err, tt.shutdown, time.Now())
		set := update["$set"].(bson.M)
		if set["status"] != tt.status || set["testResult"].(bson.M)["uploaded"] != false {
			t.Fatalf("wrong outcome: %v", set)
		}
	}
}
func TestAudioSourceRestrictions(t *testing.T) {
	file := "file"
	storage := "storage"
	job := &Job{VideoProcess: models.VideoProcess{FileID: &file, SourceStorageID: &storage, SourceMediaIDs: []string{"audio"}}}
	filter := sourceFilter(job)
	if filter["fileId"] != file || filter["type"] != "audio" || filter["storageId"] != storage || filter["_id"] == nil {
		t.Fatal(filter)
	}
}

func TestMongoQueueLifecycle(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("atomic claim reads returned ownership", func(mt *mtest.T) {
		q := &Queue{client: mt.Client, db: mt.DB}
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, mt.DB.Name()+".settings", mtest.FirstBatch, bson.D{{Key: "value", Value: bson.M{"enabled": true}}}),
			mtest.CreateCursorResponse(0, mt.DB.Name()+".workers", mtest.FirstBatch, bson.D{{Key: "enable", Value: true}}),
			mtest.CreateSuccessResponse(bson.E{Key: "value", Value: bson.M{"_id": "job-1", "fileId": "file-1", "processType": "subtitle", "status": "processing", "leaseToken": "claimed-token"}}),
		)
		job, _, _, err := q.Claim(context.Background(), "subtitle_test@1")
		if err != nil || job == nil || job.ID != "job-1" || job.LeaseToken != "claimed-token" {
			mt.Fatalf("claim decode: %+v %v", job, err)
		}
		events := mt.GetAllStartedEvents()
		last := events[len(events)-1]
		if last.CommandName != "findAndModify" || last.Command.Lookup("query").Document().Lookup("processType").StringValue() != "subtitle" {
			mt.Fatal("claim is not atomic or wrong queue")
		}
	})
	mt.Run("disabled setting never claims", func(mt *mtest.T) {
		q := &Queue{client: mt.Client, db: mt.DB}
		mt.AddMockResponses(mtest.CreateCursorResponse(0, mt.DB.Name()+".settings", mtest.FirstBatch, bson.D{{Key: "value", Value: bson.M{"enabled": false}}}))
		job, _, _, err := q.Claim(context.Background(), "subtitle_test@1")
		if err != nil || job != nil {
			mt.Fatal("disabled queue claimed")
		}
		if len(mt.GetAllStartedEvents()) != 1 {
			mt.Fatal("disabled queue performed writes")
		}
	})
	mt.Run("inference error settles only queue record", func(mt *mtest.T) {
		q := &Queue{client: mt.Client, db: mt.DB}
		file := "file-1"
		job := &Job{VideoProcess: models.VideoProcess{ID: "job-1", FileID: &file}, LeaseToken: "token"}
		mt.AddMockResponses(
			mtest.CreateCursorResponse(0, mt.DB.Name()+".files", mtest.FirstBatch, bson.D{{Key: "_id", Value: file}, {Key: "status", Value: "ready"}}),
			mtest.CreateCursorResponse(0, mt.DB.Name()+".medias", mtest.FirstBatch, bson.D{{Key: "_id", Value: "audio-1"}, {Key: "type", Value: "audio"}}),
			mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}, bson.E{Key: "nModified", Value: 1}),
		)
		dir, err := q.Run(context.Background(), job, "subtitle_test@1", Settings{ChunkSeconds: 90, MaxNewTokens: 2048, Prompt: "Japanese"}, subtitle.Options{}, func(ctx context.Context, o subtitle.Options) (string, error) {
			if o.FileID != "file-1" || o.MediaID != "audio-1" || o.QueueJobID != "job-1" || o.ChunkSeconds != 90 {
				mt.Fatal("source/settings not applied")
			}
			return "D:/runs/retained", errors.New("inference failed")
		})
		if err == nil || dir != "D:/runs/retained" {
			mt.Fatalf("run: %s %v", dir, err)
		}
		for _, event := range mt.GetAllStartedEvents() {
			if event.CommandName == "update" && event.Command.Lookup("update").StringValue() != "video_process" {
				mt.Fatal("wrote content collection")
			}
		}
	})
	mt.Run("lease loss cancels stale ownership", func(mt *mtest.T) {
		q := &Queue{client: mt.Client, db: mt.DB}
		mt.AddMockResponses(mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 0}, bson.E{Key: "nModified", Value: 0}))
		if !errors.Is(q.renew(context.Background(), &Job{LeaseToken: "old"}, "worker"), ErrLeaseLost) {
			mt.Fatal("lost lease accepted")
		}
	})
}

func TestSuccessfulSettlementCompletesQueue(t *testing.T) {
	update := settleUpdate("work/file-1", nil, false, time.Now())
	set := update["$set"].(bson.M)
	if set["status"] != "completed" || set["overallPercent"] != 100 || set["testResult"].(bson.M)["uploaded"] != false {
		t.Fatal(set)
	}
	if _, ok := update["$unset"].(bson.M)["error"]; !ok {
		t.Fatal("review reason removed")
	}
}

func TestNoSpeechTransaction(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("empty result marks file and settles queue together", func(mt *mtest.T) {
		q := &Queue{client: mt.Client, db: mt.DB}
		file := "file-1"
		job := &Job{VideoProcess: models.VideoProcess{ID: "job", FileID: &file}, LeaseToken: "token"}
		mt.AddMockResponses(mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}), mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}), mtest.CreateSuccessResponse())
		if err := q.settleNoSpeech(context.Background(), job, "worker", settleUpdate("work/file", nil, false, time.Now())); err != nil {
			mt.Fatal(err)
		}
		events := mt.GetAllStartedEvents()
		fileWrites := 0
		commits := 0
		for _, e := range events {
			if e.CommandName == "commitTransaction" {
				commits++
			}
			if e.CommandName == "update" && e.Command.Lookup("update").StringValue() == "files" {
				fileWrites++
				updates := e.Command.Lookup("updates").Array()
				values, _ := updates.Values()
				set := values[0].Document().Lookup("u").Document().Lookup("$set").Document()
				if set.Lookup("metadata.subtitleProcessingStatus").StringValue() != "no_speech" {
					mt.Fatal(set)
				}
				elements, err := set.Elements()
				if err != nil || len(elements) != 1 {
					mt.Fatal("file update must change only subtitleProcessingStatus, preserving updatedAt", set)
				}
			}
		}
		if fileWrites != 1 || commits != 1 {
			mt.Fatalf("writes=%d commits=%d", fileWrites, commits)
		}
	})
	mt.Run("lost ownership never updates file", func(mt *mtest.T) {
		q := &Queue{client: mt.Client, db: mt.DB}
		file := "file-1"
		job := &Job{VideoProcess: models.VideoProcess{ID: "job", FileID: &file}, LeaseToken: "old"}
		mt.AddMockResponses(mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 0}), mtest.CreateSuccessResponse())
		if !errors.Is(q.settleNoSpeech(context.Background(), job, "worker", settleUpdate("work/file", nil, false, time.Now())), ErrLeaseLost) {
			mt.Fatal("stale owner accepted")
		}
		for _, e := range mt.GetAllStartedEvents() {
			if e.CommandName == "update" && e.Command.Lookup("update").StringValue() == "files" {
				mt.Fatal("stale owner wrote file")
			}
		}
	})
}
