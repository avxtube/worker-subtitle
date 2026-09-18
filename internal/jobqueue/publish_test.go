package jobqueue

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
	"regexp"
	"testing"
	"time"
	"worker-subtitle/internal/db/models"
)

func TestPublicationShape(t *testing.T) {
	p, err := newPublication(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^subtitle/2026-09-18/[A-Za-z0-9_-]{11}\.vtt$`).MatchString(p.Key) {
		t.Fatal(p.Key)
	}
	file := "file"
	p.Language = "ja"
	p.LanguageName = "Japanese"
	doc := mediaDocument(&Job{VideoProcess: models.VideoProcess{FileID: &file}}, p, time.Now())
	if metadata := doc["metadata"].(bson.M); metadata["language"] != "ja" || metadata["name"] != "Japanese" {
		t.Fatal(metadata)
	}
	if doc["type"] != "subtitle" || doc["enabled"] != true || doc["metadata"].(bson.M)["source"] != "ai" || doc["mime"] != "text/vtt" {
		t.Fatal(doc)
	}
	filter := subtitleStorageFilter()
	if filter["enabled"] != true || filter["status"] != "online" || filter["kinds"] != "subtitle" || len(filter["purposes"].(bson.M)["$all"].(bson.A)) != 2 {
		t.Fatal(filter)
	}
}
func TestPublishedTransaction(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))
	mt.Run("commits enabled media and file without touching file date", func(mt *mtest.T) {
		q := &Queue{client: mt.Client, db: mt.DB}
		file := "file"
		job := &Job{VideoProcess: models.VideoProcess{ID: "job", FileID: &file}, LeaseToken: "token"}
		mt.AddMockResponses(mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}), mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}), mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 1}), mtest.CreateSuccessResponse())
		if err := q.settlePublished(context.Background(), job, "worker", "work/file", &publication{MediaID: "media", Key: "subtitle/test.vtt", Language: "ja"}); err != nil {
			mt.Fatal(err)
		}
		files, media, commits := 0, 0, 0
		for _, e := range mt.GetAllStartedEvents() {
			switch e.CommandName {
			case "commitTransaction":
				commits++
			case "insert":
				media++
				docs, _ := e.Command.Lookup("documents").Array().Values()
				if docs[0].Document().Lookup("enabled").Boolean() != true {
					mt.Fatal("media must be enabled")
				}
			case "update":
				if e.Command.Lookup("update").StringValue() == "files" {
					files++
					updates, _ := e.Command.Lookup("updates").Array().Values()
					set := updates[0].Document().Lookup("u").Document().Lookup("$set").Document()
					elems, _ := set.Elements()
					if len(elems) != 1 || set.Lookup("metadata.subtitleProcessingStatus").StringValue() != "completed" {
						mt.Fatal("file timestamp changed", set)
					}
				}
			}
		}
		if files != 1 || media != 1 || commits != 1 {
			mt.Fatal(files, media, commits)
		}
	})
	mt.Run("stale owner cannot publish media", func(mt *mtest.T) {
		q := &Queue{client: mt.Client, db: mt.DB}
		file := "file"
		job := &Job{VideoProcess: models.VideoProcess{ID: "job", FileID: &file}, LeaseToken: "old"}
		mt.AddMockResponses(mtest.CreateSuccessResponse(bson.E{Key: "n", Value: 0}), mtest.CreateSuccessResponse())
		if !errors.Is(q.settlePublished(context.Background(), job, "worker", "dir", &publication{}), ErrLeaseLost) {
			mt.Fatal("stale publication accepted")
		}
		for _, e := range mt.GetAllStartedEvents() {
			if e.CommandName == "insert" {
				mt.Fatal("stale owner inserted media")
			}
		}
	})
}
