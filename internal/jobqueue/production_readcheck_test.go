package jobqueue

import (
	"context"
	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
	"os"
	"testing"
	"time"
	"worker-subtitle/internal/subtitle"
)

// Explicitly opt-in, read-only connectivity check. It never calls Claim or Run.
func TestProductionReadOnly(t *testing.T) {
	if os.Getenv("SUBTITLE_READCHECK") != "1" {
		t.Skip("set SUBTITLE_READCHECK=1 for read-only production diagnostics")
	}
	env, err := godotenv.Read("../../.env")
	if err != nil {
		t.Fatal("cannot read local .env")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	q, err := Open(ctx, env["DATABASE_URL"])
	if err != nil {
		t.Fatal("cannot configure database connection")
	}
	defer q.Close()
	var setting struct {
		Value Settings `bson:"value"`
	}
	if err := q.db.Collection("settings").FindOne(ctx, bson.M{"name": "subtitle_config"}).Decode(&setting); err != nil {
		t.Log("subtitle_config unavailable")
	} else {
		t.Logf("subtitle_config: enabled=%v chunkSeconds=%d maxNewTokens=%d", setting.Value.Enabled, setting.Value.ChunkSeconds, setting.Value.MaxNewTokens)
	}
	for _, status := range []string{"pending", "processing", "completed", "failed"} {
		count, err := q.db.Collection("video_process").CountDocuments(ctx, bson.M{"processType": "subtitle", "status": status})
		if err != nil {
			t.Fatal("queue count unavailable; check database connectivity")
		}
		t.Logf("subtitle %s: %d", status, count)
	}
}

func TestProductionFailedSources(t *testing.T) {
	if os.Getenv("SUBTITLE_READCHECK") != "1" {
		t.Skip("opt-in read-only")
	}
	env, _ := godotenv.Read("../../.env")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	q, err := Open(ctx, env["DATABASE_URL"])
	if err != nil {
		t.Fatal("connection unavailable")
	}
	defer q.Close()
	cur, err := q.db.Collection("video_process").Find(ctx, bson.M{"processType": "subtitle", "status": "failed"}, options.Find().SetLimit(3).SetSort(bson.D{{Key: "updatedAt", Value: -1}}))
	if err != nil {
		t.Fatal(err)
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var j Job
		if err := cur.Decode(&j); err != nil {
			t.Fatal(err)
		}
		if j.FileID == nil {
			continue
		}
		var f bson.M
		err := q.db.Collection("files").FindOne(ctx, bson.M{"_id": *j.FileID}, options.FindOne().SetProjection(bson.M{"_id": 1, "status": 1, "metadata.deletedAt": 1, "metadata.trashedAt": 1})).Decode(&f)
		var exact bson.M
		exactErr := q.db.Collection("files").FindOne(ctx, bson.M{"_id": *j.FileID, "status": bson.M{"$in": bson.A{"ready", "ready_original"}}, "metadata.deletedAt": nil, "metadata.trashedAt": nil}).Decode(&exact)
		t.Logf("exact filter=%v", exactErr)
		resolved, resolveErr := q.resolve(ctx, &j, Settings{}, subtitle.Options{ChunkSeconds: 180, MaxTokens: 2048})
		t.Logf("source resolution: media=%s error=%v", resolved.MediaID, resolveErr)
		t.Logf("job=%s file=%s lookup=%v fields=%#v", j.ID, *j.FileID, err, f)
	}
}

func TestProductionSubtitleDestinationReadOnly(t *testing.T) {
	if os.Getenv("SUBTITLE_READCHECK") != "1" {
		t.Skip("opt-in read-only")
	}
	env, err := godotenv.Read("../../.env")
	if err != nil {
		t.Fatal("missing config")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	q, err := Open(ctx, env["DATABASE_URL"])
	if err != nil {
		t.Fatal("invalid connection")
	}
	defer q.Close()
	count, err := q.db.Collection("storages").CountDocuments(ctx, subtitleStorageFilter())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Eligible subtitle destinations: %d", count)
	var hello bson.M
	if err := q.client.Database("admin").RunCommand(ctx, bson.M{"hello": 1}).Decode(&hello); err != nil {
		t.Fatal(err)
	}
	t.Logf("Replica set: %v; mongos: %v", hello["setName"] != nil, hello["msg"] == "isdbgrid")
}
