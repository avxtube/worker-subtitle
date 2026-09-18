package enums

// ─── Setting Keys ────────────────────────────────────────────────────

const (
	// subtitle_config = {enabled, slotRate, gpuEnabled} — shared with the
	// platform enqueuer; worker reads .enabled (kill switch) and
	// .gpuEnabled (อนุญาตใช้ GPU encoder — default true, auto-detect)
	SettingSubtitleConfig = "subtitle_config"
)
