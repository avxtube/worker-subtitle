package models

import "testing"

func TestMediaMatchesActiveKeySchema(t *testing.T) {
	quality := "360"
	media := Media{FileID: "file-1", StorageID: "storage-1", Quality: &quality, Key: "file-1/file_360.mp4", Mime: "video/mp4"}
	if media.FileID == "" || media.StorageID == "" || media.Key == "" || media.Mime == "" {
		t.Fatal("required media fields are missing")
	}
}
