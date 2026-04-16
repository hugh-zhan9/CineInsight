package services

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultTrashDirName = "trash"

type TrashService struct {
	TrashDirName string
	now          func() time.Time
}

func NewTrashService() *TrashService {
	return &TrashService{
		TrashDirName: DefaultTrashDirName,
		now:          time.Now,
	}
}

func (s *TrashService) MoveToTrash(srcPath string) (string, error) {
	srcPath = filepath.Clean(strings.TrimSpace(srcPath))
	if srcPath == "" {
		return "", fmt.Errorf("源文件路径为空")
	}

	info, err := os.Stat(srcPath)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("不支持移动目录到回收站: %s", srcPath)
	}
	if isTrashPath(srcPath) {
		return srcPath, nil
	}

	trashDir := filepath.Join(filepath.Dir(srcPath), s.TrashDirName)
	if err := os.MkdirAll(trashDir, 0755); err != nil {
		return "", err
	}

	targetPath, err := s.uniqueTrashTarget(trashDir, filepath.Base(srcPath))
	if err != nil {
		return "", err
	}

	if err := os.Rename(srcPath, targetPath); err == nil {
		return targetPath, nil
	} else if err := s.copyAndDelete(srcPath, targetPath, info.Mode()); err != nil {
		return "", fmt.Errorf("移动文件到回收站失败: %w", err)
	}

	return targetPath, nil
}

func (s *TrashService) uniqueTrashTarget(trashDir string, baseName string) (string, error) {
	targetPath := filepath.Join(trashDir, baseName)
	if _, err := os.Stat(targetPath); err == nil {
		ext := filepath.Ext(baseName)
		name := strings.TrimSuffix(baseName, ext)
		targetPath = filepath.Join(trashDir, fmt.Sprintf("%s_%s%s", name, s.now().Format("20060102150405"), ext))
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return targetPath, nil
}

func (s *TrashService) copyAndDelete(srcPath string, targetPath string, mode os.FileMode) error {
	source, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer source.Close()

	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	defer target.Close()

	if _, err := io.Copy(target, source); err != nil {
		return err
	}

	if err := target.Close(); err != nil {
		return err
	}
	return os.Remove(srcPath)
}
