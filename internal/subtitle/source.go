package subtitle

import (
	"context"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"worker-subtitle/internal/config"
	"worker-subtitle/internal/db/database"
	"worker-subtitle/internal/db/models"
	"worker-subtitle/internal/uploader"
)

func acquireAudio(ctx context.Context, opts Options, dest string) error {
	if opts.AudioFile != "" {
		return copyAudio(ctx, opts.AudioFile, dest)
	}
	if opts.AudioURL != "" {
		return downloadAudio(ctx, opts.AudioURL, dest)
	}
	if err := database.Connect(); err != nil {
		return err
	}
	// No Save/Update/Create or queue operation is performed in test mode.
	media, err := models.MediaModel.FindOne(ctx, bson.M{"_id": opts.MediaID, "type": "audio", "deletedAt": nil})
	if err != nil {
		return fmt.Errorf("find audio media: %w", err)
	}
	storage, err := models.StorageModel.FindByID(ctx, media.StorageID)
	if err != nil {
		return err
	}
	if !storage.IsOnline() {
		return fmt.Errorf("audio storage is offline or disabled")
	}
	if storage.Provider == "s3" {
		if err := uploader.PrepareStorageCredentials(storage, config.AppConfig.StorageEncryptionKey); err != nil {
			return err
		}
		var lastProgress time.Time
		return uploader.DownloadFromS3(ctx, storage, media.Key, dest, func(downloaded, total int64) {
			if opts.Progress != nil && total > 0 && (downloaded >= total || time.Since(lastProgress) >= time.Second) {
				lastProgress = time.Now()
				opts.Progress("download_audio", 10*float64(downloaded)/float64(total))
			}
		})
	}
	sourceURL, err := audioStorageURL(storage, media.Key)
	if err != nil {
		return err
	}
	return downloadAudio(ctx, sourceURL, dest)
}
func audioStorageURL(storage *models.Storage, key string) (string, error) {
	if storage.PublicURL == nil || strings.TrimSpace(*storage.PublicURL) == "" {
		return "", fmt.Errorf("audio storage has no publicUrl")
	}
	base := strings.TrimSpace(strings.Split(*storage.PublicURL, ",")[0])
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	return url.JoinPath(base, strings.ReplaceAll(key, `\`, "/"))
}
func downloadAudio(ctx context.Context, source, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return fmt.Errorf("invalid audio request")
	}
	client := &http.Client{Timeout: 2 * time.Hour}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("audio download failed (URL omitted to protect credentials)")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("audio download HTTP %d", res.StatusCode)
	}
	// Keep partial downloads too: the test must not delete any artifacts.
	out, err := os.Create(dest + ".part")
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(out, res.Body)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if res.ContentLength >= 0 && n != res.ContentLength {
		return fmt.Errorf("incomplete audio download")
	}
	if n == 0 {
		return fmt.Errorf("empty audio download")
	}
	return os.Rename(dest+".part", dest)
}
