package config

import (
	"github.com/joho/godotenv"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

type Config struct {
	QueueEnabled                                                                   bool
	RuntimeDir, DashboardAddr, BasePython, TorchIndex, ModelRevision, HeartbeatURI string
	MongoURI, StorageId, StorageEncryptionKey, WorkerVersion                       string
	WorkDir, LogDir, Python, MossScript, ModelDir                                  string
	S3UploadConcurrency                                                            int
}

var AppConfig Config

func Load() {
	exe, _ := os.Executable()
	root := filepath.Dir(exe)
	_ = godotenv.Load(filepath.Join(root, ".env"))
	python := filepath.Join(root, ".venv", "bin", "python")
	if runtime.GOOS == "windows" {
		python = filepath.Join(root, ".venv", "Scripts", "python.exe")
	}
	AppConfig = Config{
		QueueEnabled:         getBoolEnv("SUBTITLE_QUEUE_ENABLED", true),
		RuntimeDir:           root,
		DashboardAddr:        env("DASHBOARD_ADDR", "127.0.0.1:8887"),
		BasePython:           env("MOSS_BASE_PYTHON", ""),
		TorchIndex:           env("MOSS_TORCH_INDEX", "https://download.pytorch.org/whl/cu130"),
		ModelRevision:        env("MOSS_MODEL_REVISION", "main"),
		HeartbeatURI:         os.Getenv("DATABASE_URL"),
		MongoURI:             env("DATABASE_URL", "mongodb://localhost:27017/avxtube"),
		StorageEncryptionKey: env("STORAGE_ENCRYPTION_KEY", os.Getenv("BETTER_AUTH_SECRET")),
		WorkerVersion:        "dev", S3UploadConcurrency: 2,
		WorkDir:    env("WORK_DIR", filepath.Join(root, "work")),
		LogDir:     filepath.Join(root, "log"),
		Python:     python,
		MossScript: env("MOSS_SCRIPT", filepath.Join(root, "moss", "server.py")),
		ModelDir:   filepath.Join(root, "models", "MOSS-Transcribe-Diarize"),
	}
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func getBoolEnv(key string, fallback bool) bool {
	value, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return value
}
