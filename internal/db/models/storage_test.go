package models

import "testing"

func TestStorageIsOnlineMatchesActiveSchema(t *testing.T) {
	storage := Storage{Provider: "s3", Enabled: true, Status: "online"}
	if !storage.IsOnline() {
		t.Fatal("enabled online storage should be usable")
	}
	storage.Enabled = false
	if storage.IsOnline() {
		t.Fatal("disabled storage should not be usable")
	}
}
