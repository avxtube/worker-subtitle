package enums

// ─── Storage Types ───────────────────────────────────────────────────

const (
	StorageTypeLocal = "local"
	StorageTypeS3    = "s3"
)

// ─── Storage Statuses ────────────────────────────────────────────────

const (
	StorageStatusOnline      = "online"
	StorageStatusOffline     = "offline"
	StorageStatusError       = "error"
	StorageStatusMaintenance = "maintenance"
)

// ─── Storage purposes and kinds ──────────────────────────────────────

const (
	StoragePurposeUpload  = "upload"
	StoragePurposeTemp    = "temp"
	StoragePurposeStorage = "storage"
	StorageKindStream     = "stream"
)
