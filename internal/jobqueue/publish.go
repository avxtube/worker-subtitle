package jobqueue

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"log"
	"os"
	"path/filepath"
	"time"
	"worker-subtitle/internal/config"
	"worker-subtitle/internal/db/models"
	"worker-subtitle/internal/uploader"
)

type publication struct {
	MediaID, StorageID, Key, Slug, Language, LanguageName string
	Size                                                  int64
}

func subtitleStorageFilter() bson.M {
	return bson.M{"provider": "s3", "enabled": true, "status": "online", "deletedAt": nil, "purposes": bson.M{"$all": bson.A{"storage", "upload"}}, "kinds": "subtitle"}
}
func newPublication(now time.Time) (*publication, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	slug := base64.RawURLEncoding.EncodeToString(b)
	return &publication{MediaID: uuid.NewString(), Slug: slug, Key: "subtitle/" + now.Format("2006-01-02") + "/" + slug + ".vtt"}, nil
}
func (q *Queue) uploadSubtitle(ctx context.Context, job *Job, worker, dir string) (*publication, error) {
	var storage models.Storage
	if err := q.db.Collection("storages").FindOne(ctx, subtitleStorageFilter(), options.FindOne().SetSort(bson.D{{Key: "priority", Value: 1}, {Key: "_id", Value: 1}})).Decode(&storage); err != nil {
		return nil, fmt.Errorf("no available S3 subtitle storage with storage/upload purposes: %w", err)
	}
	if err := uploader.PrepareStorageCredentials(&storage, config.AppConfig.StorageEncryptionKey); err != nil {
		return nil, err
	}
	p, err := newPublication(time.Now())
	if err != nil {
		return nil, err
	}
	p.StorageID = storage.ID
	p.Language = "und"
	p.LanguageName = "Unknown"
	path := filepath.Join(dir, "subtitle.vtt")
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, fmt.Errorf("invalid subtitle artifact")
	}
	p.Size = info.Size()
	data, err := os.ReadFile(filepath.Join(dir, "job.json"))
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Language struct {
			Code string `json:"code"`
			Name string `json:"name"`
		} `json:"language"`
	}
	if err = json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	if manifest.Language.Code != "" {
		p.Language = manifest.Language.Code
		if manifest.Language.Name != "" {
			p.LanguageName = manifest.Language.Name
		}
	}
	// Keep an audit record before uploading. If ownership is lost, the object can
	// be reconciled using this record; no media is published by the stale owner.
	res, err := q.db.Collection("video_process").UpdateOne(ctx, ownerFilter(job, worker), bson.M{"$push": bson.M{"subtitleUploadAttempts": bson.M{"mediaId": p.MediaID, "storageId": p.StorageID, "key": p.Key, "leaseToken": job.LeaseToken, "createdAt": time.Now()}}, "$set": bson.M{"testStage": "upload", "updatedAt": time.Now()}})
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, ErrLeaseLost
	}
	if err = uploader.UploadToS3(ctx, &storage, path, p.Key, nil); err != nil {
		return nil, err
	}
	return p, nil
}
func mediaDocument(job *Job, p *publication, now time.Time) bson.M {
	return bson.M{"_id": p.MediaID, "type": "subtitle", "enabled": true, "fileId": *job.FileID, "storageId": p.StorageID, "key": p.Key, "slug": p.Slug, "mime": "text/vtt", "size": p.Size, "metadata": bson.M{"language": p.Language, "name": p.LanguageName, "type": "transcribe", "source": "ai"}, "createdAt": now, "updatedAt": now}
}
func (q *Queue) settlePublished(ctx context.Context, job *Job, worker, dir string, p *publication) error {
	if job.FileID == nil {
		return fmt.Errorf("publication missing fileId")
	}
	session, err := q.client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)
	now := time.Now()
	_, err = session.WithTransaction(ctx, func(sc mongo.SessionContext) (interface{}, error) {
		update := settleUpdate(dir, nil, false, now)
		completeTimeline(update, job, false)
		set := update["$set"].(bson.M)
		set["testStage"] = "completed"
		set["result"] = bson.M{"mediaId": p.MediaID, "storageId": p.StorageID, "key": p.Key, "uploaded": true, "enabled": true}
		set["testResult"].(bson.M)["uploaded"] = true
		res, e := q.db.Collection("video_process").UpdateOne(sc, ownerFilter(job, worker), update)
		if e != nil {
			return nil, e
		}
		if res.MatchedCount == 0 {
			return nil, ErrLeaseLost
		}
		if _, e = q.db.Collection("medias").InsertOne(sc, mediaDocument(job, p, now)); e != nil {
			return nil, e
		}
		res, e = q.db.Collection("files").UpdateOne(sc, bson.M{"_id": *job.FileID, "metadata.deletedAt": nil, "metadata.trashedAt": nil}, bson.M{"$set": bson.M{"metadata.subtitleProcessingStatus": "completed"}})
		if e != nil {
			return nil, e
		}
		if res.MatchedCount == 0 {
			return nil, fmt.Errorf("file unavailable at publication")
		}
		return nil, nil
	})
	if err == nil {
		path := filepath.Join(dir, "job.json")
		data, e := os.ReadFile(path)
		var manifest map[string]any
		if e == nil {
			e = json.Unmarshal(data, &manifest)
		}
		if e == nil {
			manifest["uploaded"] = true
			manifest["mediaId"] = p.MediaID
			manifest["storageId"] = p.StorageID
			manifest["key"] = p.Key
			manifest["enabled"] = true
			data, e = json.MarshalIndent(manifest, "", "  ")
		}
		if e == nil {
			e = os.WriteFile(path, append(data, '\n'), 0644)
		}
		if e != nil {
			log.Printf("[subtitle] Published successfully; local upload receipt could not be saved: %v", e)
		}
	}
	return err
}
