package enums

// ─── File Types ──────────────────────────────────────────────────────
// Must match FILE_TYPES in platform/packages/core/src/enums/file.enum.ts.

const (
	FileTypeFolder = "folder"
	FileTypeVideo  = "video"
	FileTypeImage  = "image"
	FileTypeOther  = "other"
)

// ─── File Statuses ───────────────────────────────────────────────────
// Must match FILE_STATUSES in platform/packages/core/src/enums/file.enum.ts.

const (
	FileStatusPending       = "pending"
	FileStatusProcessing    = "processing"
	FileStatusReady         = "ready"
	FileStatusReadyOriginal = "ready_original"
	FileStatusError         = "error"
	FileStatusQueue         = "queue"
)
