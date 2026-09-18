package subtitle

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var safeFileID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Keep persistent work directories portable between Linux and Windows.
var reservedFileID = regexp.MustCompile(`(?i)^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])$`)

func createWorkDir(root, fileID string) (string, error) {
	if fileID == "" {
		return os.MkdirTemp(root, "manual-"+time.Now().Format("20060102-150405")+"-")
	}
	if !safeFileID.MatchString(fileID) || !filepath.IsLocal(fileID) || reservedFileID.MatchString(fileID) {
		return "", fmt.Errorf("invalid file ID for work directory")
	}
	dir := filepath.Join(root, fileID)
	if err := os.Mkdir(dir, 0755); err == nil {
		return dir, nil
	} else if !os.IsExist(err) {
		return "", err
	}
	// Preserve earlier output on retry instead of overwriting reviewed artifacts.
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("work path is not a regular directory")
	}
	return os.MkdirTemp(dir, "attempt-"+time.Now().Format("20060102-150405")+"-")
}
