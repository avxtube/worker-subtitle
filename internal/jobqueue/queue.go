// Package jobqueue uploads subtitles and atomically publishes enabled media, file status and queue completion.
package jobqueue

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"net/url"
	"strings"
	"time"
	"worker-subtitle/internal/db/models"
	"worker-subtitle/internal/subtitle"
)

var ErrLeaseLost = errors.New("queue lease lost or job cancelled")

type Queue struct {
	client *mongo.Client
	db     *mongo.Database
}
type Job struct {
	progress            *jobProgress `bson:"-"`
	models.VideoProcess `bson:",inline"`
	LeaseToken          string `bson:"leaseToken"`
}
type Settings struct {
	Enabled      bool   `bson:"enabled"`
	ChunkSeconds int    `bson:"chunkSeconds"`
	MaxNewTokens int    `bson:"maxNewTokens"`
	Prompt       string `bson:"prompt"`
}

func Open(ctx context.Context, uri string) (*Queue, error) {
	u, err := url.Parse(uri)
	if err != nil || strings.Trim(u.Path, "/") == "" {
		return nil, fmt.Errorf("queue DATABASE_URL must include database name")
	}
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri).SetServerSelectionTimeout(5*time.Second))
	if err != nil {
		return nil, err
	}
	return &Queue{client: client, db: client.Database(strings.Trim(u.Path, "/"))}, nil
}
func (q *Queue) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = q.client.Disconnect(ctx)
}
func claimFilter(now time.Time) bson.M {
	return bson.M{"processType": "subtitle", "$or": bson.A{
		bson.M{"status": "pending", "$or": bson.A{bson.M{"nextRetryAt": nil}, bson.M{"nextRetryAt": bson.M{"$lte": now}}}},
		bson.M{"status": "processing", "leaseExpiresAt": bson.M{"$lte": now}},
	}}
}
func ownerFilter(job *Job, worker string) bson.M {
	return bson.M{"_id": job.ID, "processType": "subtitle", "status": "processing", "workerId": worker, "leaseToken": job.LeaseToken}
}
func (q *Queue) Claim(ctx context.Context, worker string) (*Job, Settings, string, error) {
	var setting struct {
		Value Settings `bson:"value"`
	}
	err := q.db.Collection("settings").FindOne(ctx, bson.M{"name": "subtitle_config"}).Decode(&setting)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, Settings{}, "Waiting for subtitle_config.enabled", nil
	}
	if err != nil {
		return nil, Settings{}, "", err
	}
	if !setting.Value.Enabled {
		return nil, setting.Value, "Subtitle queue disabled in admin", nil
	}
	var w struct {
		Enable bool `bson:"enable"`
	}
	if err := q.db.Collection("workers").FindOne(ctx, bson.M{"workerId": worker, "type": "subtitle"}).Decode(&w); err != nil {
		return nil, setting.Value, "", err
	}
	if !w.Enable {
		return nil, setting.Value, "Worker disabled in admin", nil
	}
	now := time.Now()
	token := uuid.NewString()
	update := bson.M{"$set": bson.M{"status": "processing", "workerId": worker, "leaseToken": token, "claimedAt": now, "startedAt": now, "heartbeatAt": now, "leaseExpiresAt": now.Add(2 * time.Minute), "updatedAt": now, "overallPercent": 0, "executionMode": "production"}, "$unset": bson.M{"finishedAt": "", "nextRetryAt": "", "error": "", "testResult": ""}}
	var job Job
	update["$set"].(bson.M)["timeline"] = newJobProgress(now).timeline
	err = q.db.Collection("video_process").FindOneAndUpdate(ctx, claimFilter(now), update, options.FindOneAndUpdate().SetSort(bson.D{{Key: "priority", Value: -1}, {Key: "createdAt", Value: 1}}).SetReturnDocument(options.After)).Decode(&job)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, setting.Value, "Waiting for pending subtitle jobs", nil
	}
	if err != nil {
		return nil, setting.Value, "", err
	}
	return &job, setting.Value, "Claimed production job", nil
}
func sourceFilter(job *Job) bson.M {
	fileID := ""
	if job.FileID != nil {
		fileID = *job.FileID
	}
	filter := bson.M{"fileId": fileID, "type": "audio", "deletedAt": nil}
	if len(job.SourceMediaIDs) > 0 {
		filter["_id"] = bson.M{"$in": job.SourceMediaIDs}
	}
	if job.SourceStorageID != nil && *job.SourceStorageID != "" {
		filter["storageId"] = *job.SourceStorageID
	}
	return filter
}
func (q *Queue) resolve(ctx context.Context, job *Job, settings Settings, base subtitle.Options) (subtitle.Options, error) {
	if job.FileID == nil || strings.TrimSpace(*job.FileID) == "" {
		return base, fmt.Errorf("subtitle job has no fileId")
	}
	var file models.File
	if err := q.db.Collection("files").FindOne(ctx, bson.M{"_id": *job.FileID}).Decode(&file); err != nil {
		return base, fmt.Errorf("source file %s unavailable: %w", *job.FileID, err)
	}
	if file.Metadata != nil && (file.Metadata.DeletedAt != nil || file.Metadata.TrashedAt != nil) {
		return base, fmt.Errorf("source file %s is deleted or trashed", *job.FileID)
	}
	if file.Status != "ready" && file.Status != "ready_original" {
		return base, fmt.Errorf("source file %s is not ready (status=%q); retry after it becomes ready", *job.FileID, file.Status)
	}
	var media models.Media
	if err := q.db.Collection("medias").FindOne(ctx, sourceFilter(job), options.FindOne().SetSort(bson.D{{Key: "key", Value: 1}, {Key: "_id", Value: 1}})).Decode(&media); err != nil {
		return base, fmt.Errorf("separated audio not found: %w", err)
	}
	base.AudioURL = ""
	base.AudioFile = ""
	base.MediaID = media.ID
	base.FileID = *job.FileID
	base.QueueJobID = job.ID
	if settings.ChunkSeconds != 0 {
		base.ChunkSeconds = settings.ChunkSeconds
	}
	if settings.MaxNewTokens != 0 {
		base.MaxTokens = settings.MaxNewTokens
	}
	base.Prompt = settings.Prompt
	return base, base.Validate()
}

// LogName resolves the same file slug used by the transcode worker, including
// before source validation so failed jobs still get a file-specific log.
func (q *Queue) LogName(ctx context.Context, job *Job) string {
	if job.FileID == nil || *job.FileID == "" {
		return job.ID
	}
	var file models.File
	if err := q.db.Collection("files").FindOne(ctx, bson.M{"_id": *job.FileID}, options.FindOne().SetProjection(bson.M{"slug": 1})).Decode(&file); err == nil && file.Slug != "" {
		return file.Slug
	}
	return *job.FileID
}
func (q *Queue) renew(ctx context.Context, job *Job, worker string) error {
	now := time.Now()
	res, err := q.db.Collection("video_process").UpdateOne(ctx, ownerFilter(job, worker), bson.M{"$set": bson.M{"heartbeatAt": now, "leaseExpiresAt": now.Add(2 * time.Minute), "updatedAt": now}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrLeaseLost
	}
	return nil
}

// Run renews ownership while downloading/inferencing and cancels I/O on lease
// loss or admin cancellation. Queue writes always carry the unique claim token.
func (q *Queue) Run(parent context.Context, job *Job, worker string, settings Settings, base subtitle.Options, handler func(context.Context, subtitle.Options) (string, error)) (string, error) {
	job.progress = newJobProgress(time.Now())
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c, stop := context.WithTimeout(ctx, 5*time.Second)
				err := q.renew(c, job, worker)
				stop()
				if err != nil {
					cancel(fmt.Errorf("%w: renewal unavailable", ErrLeaseLost))
					return
				}
			}
		}
	}()
	opts, err := q.resolve(ctx, job, settings, base)
	dir := ""
	noSpeech := false
	var published *publication
	if err == nil {
		opts.OnComplete = func(empty bool) { noSpeech = empty }
		opts.Progress = func(stage string, percent float64) {
			if e := q.reportProgress(ctx, job, worker, stage, percent); e != nil {
				cancel(ErrLeaseLost)
			}
		}
		dir, err = handler(ctx, opts)
		if err == nil && ctx.Err() == nil && !noSpeech {
			err = q.reportProgress(ctx, job, worker, "upload", 0)
			if err == nil {
				published, err = q.uploadSubtitle(ctx, job, worker, dir)
			}
			if err == nil {
				err = q.reportProgress(ctx, job, worker, "upload", 100)
			}
		}
		if err == nil && ctx.Err() == nil {
			if noSpeech {
				job.progress.skipUpload(time.Now())
			}
			err = q.reportProgress(ctx, job, worker, "install", 0)
		}
	}
	cause := context.Cause(ctx)
	cancel(nil)
	<-watcherDone
	if errors.Is(cause, ErrLeaseLost) {
		return dir, cause
	}
	settle, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	update := settleUpdate(dir, err, parent.Err() != nil, time.Now())
	if err != nil && job.progress != nil {
		step := job.progress.timeline[job.progress.active].(bson.M)
		step["status"] = "failed"
		step["endedAt"] = time.Now()
		update["$set"].(bson.M)["timeline"] = job.progress.timeline
	}
	if err == nil && parent.Err() == nil && published != nil {
		if e := q.settlePublished(settle, job, worker, dir, published); e != nil {
			return dir, fmt.Errorf("VTT uploaded but database publication failed; upload attempt retained on queue: %w", e)
		}
		cleanupAfterSettlement(job, base.OutputDir, dir)
		return dir, nil
	}
	if err == nil && parent.Err() == nil && noSpeech {
		if e := q.settleNoSpeech(settle, job, worker, update); e != nil {
			return dir, fmt.Errorf("no-speech settlement failed: %w", e)
		}
		cleanupAfterSettlement(job, base.OutputDir, dir)
		return dir, nil
	}
	res, settleErr := q.db.Collection("video_process").UpdateOne(settle, ownerFilter(job, worker), update)
	if settleErr != nil {
		return dir, fmt.Errorf("result retained locally; queue settlement failed: %w", settleErr)
	}
	if res.MatchedCount == 0 {
		return dir, ErrLeaseLost
	}
	if parent.Err() == nil {
		cleanupAfterSettlement(job, base.OutputDir, dir)
	}
	return dir, err
}

// Commit both records together so a cancelled/stale worker cannot mark a file
// as no_speech. A transaction failure leaves both records unchanged.
func (q *Queue) settleNoSpeech(ctx context.Context, job *Job, worker string, update bson.M) error {
	if job.FileID == nil || *job.FileID == "" {
		return fmt.Errorf("no file ID for no_speech result")
	}
	session, err := q.client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)
	set := update["$set"].(bson.M)
	set["testStage"] = "no_speech"
	completeTimeline(update, job, true)
	_, err = session.WithTransaction(ctx, func(sc mongo.SessionContext) (interface{}, error) {
		res, e := q.db.Collection("video_process").UpdateOne(sc, ownerFilter(job, worker), update)
		if e != nil {
			return nil, e
		}
		if res.MatchedCount == 0 {
			return nil, ErrLeaseLost
		}
		res, e = q.db.Collection("files").UpdateOne(sc, bson.M{"_id": *job.FileID, "metadata.deletedAt": nil, "metadata.trashedAt": nil}, bson.M{"$set": bson.M{"metadata.subtitleProcessingStatus": "no_speech"}})
		if e != nil {
			return nil, e
		}
		if res.MatchedCount == 0 {
			return nil, fmt.Errorf("source file unavailable for no_speech update")
		}
		return nil, nil
	})
	return err
}
func settleUpdate(dir string, runErr error, shutdown bool, now time.Time) bson.M {
	unset := bson.M{"leaseExpiresAt": "", "leaseToken": ""}
	set := bson.M{"updatedAt": now, "testResult": bson.M{"outputDir": dir, "uploaded": false, "mode": "production", "finishedAt": now}}
	if shutdown {
		set["status"] = "pending"
		for _, key := range []string{"workerId", "claimedAt", "heartbeatAt", "startedAt"} {
			unset[key] = ""
		}
	} else {
		set["finishedAt"] = now
		if runErr == nil {
			set["status"] = "completed"
			set["overallPercent"] = 100
			set["testStage"] = "completed"
			unset["error"] = ""
			unset["errorCategory"] = ""
		} else {
			set["status"] = "failed"
			set["error"] = runErr.Error()
			set["errorCategory"] = "subtitle"
		}
	}
	return bson.M{"$set": set, "$unset": unset}
}
