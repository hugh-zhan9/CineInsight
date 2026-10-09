package services

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

func consolidationOwnerScope(dataDir string) (string, string, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return "", "", err
	}
	directory, err := snapshotMigrationDirectory(dataDir)
	if err != nil {
		return "", "", err
	}
	machine, err := consolidationMachineID()
	if err != nil {
		return "", "", err
	}
	if machine == "" {
		return "", "", fmt.Errorf("无法取得稳定本机身份，不能启动集中整理")
	}
	return directory.RealPath, fmt.Sprintf("%x", sha256.Sum256([]byte(machine+"\x00"+directory.RealPath+"\x00"+directory.Identity))), nil
}

func consolidationLockPath(dataDir, previewID string) string {
	return filepath.Join(dataDir, fmt.Sprintf(".cleanup-consolidation-%x.lock", sha256.Sum256([]byte(previewID))))
}
