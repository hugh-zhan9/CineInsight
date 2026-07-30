package services

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultTrashDirName = "trash"

var ErrTrashTargetExists = errors.New("回收站目标已存在")

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
	if srcPath == "." {
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

	for attempt := 0; attempt < 10000; attempt++ {
		targetPath := s.TrashTargetPath(srcPath, attempt)
		if err := s.MoveToTrashAt(srcPath, targetPath); err == nil {
			return targetPath, nil
		} else if !errors.Is(err, ErrTrashTargetExists) {
			return "", fmt.Errorf("移动文件到回收站失败: %w", err)
		}
	}
	return "", fmt.Errorf("无法为文件生成唯一回收站路径: %s", srcPath)
}

// TrashTargetPath 返回指定尝试次数对应的确定性回收站路径。
func (s *TrashService) TrashTargetPath(srcPath string, attempt int) string {
	srcPath = filepath.Clean(strings.TrimSpace(srcPath))
	trashDir := filepath.Join(filepath.Dir(srcPath), s.TrashDirName)
	baseName := filepath.Base(srcPath)
	if attempt <= 0 {
		return filepath.Join(trashDir, baseName)
	}
	ext := filepath.Ext(baseName)
	name := strings.TrimSuffix(baseName, ext)
	suffix := s.now().Format("20060102150405")
	if attempt > 1 {
		suffix = fmt.Sprintf("%s_%d", suffix, attempt)
	}
	return filepath.Join(trashDir, fmt.Sprintf("%s_%s%s", name, suffix, ext))
}

// MoveToTrashAt 将文件移动到指定路径，目标已存在时绝不覆盖。
func (s *TrashService) MoveToTrashAt(srcPath string, targetPath string) error {
	srcPath = filepath.Clean(strings.TrimSpace(srcPath))
	targetPath = filepath.Clean(strings.TrimSpace(targetPath))
	if srcPath == "." || targetPath == "." {
		return fmt.Errorf("回收站路径为空")
	}
	if srcPath == targetPath {
		return fmt.Errorf("源路径与回收站路径相同: %s", srcPath)
	}
	info, err := os.Stat(srcPath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("不支持移动目录到回收站: %s", srcPath)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return err
	}

	if err := os.Link(srcPath, targetPath); err == nil {
		if err := os.Remove(srcPath); err != nil {
			_ = os.Remove(targetPath)
			return err
		}
		return nil
	} else if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%w: %s", ErrTrashTargetExists, targetPath)
	}

	if err := s.copyToExclusiveAndDelete(srcPath, targetPath, info); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: %s", ErrTrashTargetExists, targetPath)
		}
		return err
	}
	return nil
}

// RestoreFromTrash 在不覆盖现有目标的前提下恢复回收站文件。
func (s *TrashService) RestoreFromTrash(trashPath string, targetPath string) error {
	trashPath = filepath.Clean(strings.TrimSpace(trashPath))
	targetPath = filepath.Clean(strings.TrimSpace(targetPath))
	if trashPath == "." || targetPath == "." {
		return fmt.Errorf("恢复路径为空")
	}
	if trashPath == targetPath {
		return fmt.Errorf("回收站路径与恢复路径相同: %s", trashPath)
	}

	info, err := os.Stat(trashPath)
	if err != nil {
		return fmt.Errorf("检查回收站文件失败: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("不支持恢复目录: %s", trashPath)
	}
	if _, err := os.Stat(targetPath); err == nil {
		return fmt.Errorf("恢复目标已存在: %s", targetPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查恢复目标失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return fmt.Errorf("创建恢复目录失败: %w", err)
	}

	if err := os.Link(trashPath, targetPath); err == nil {
		if err := os.Remove(trashPath); err != nil {
			_ = os.Remove(targetPath)
			return fmt.Errorf("清理回收站源文件失败: %w", err)
		}
		return nil
	}
	if err := s.copyToExclusiveAndDelete(trashPath, targetPath, info); err != nil {
		return fmt.Errorf("恢复回收站文件失败: %w", err)
	}
	return nil
}

func (s *TrashService) copyToExclusiveAndDelete(srcPath string, targetPath string, info os.FileInfo) error {
	source, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer source.Close()

	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	removeTarget := true
	defer func() {
		_ = target.Close()
		if removeTarget {
			_ = os.Remove(targetPath)
		}
	}()

	copiedHash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(target, copiedHash), source); err != nil {
		return err
	}
	if err := target.Sync(); err != nil {
		return err
	}
	if err := target.Close(); err != nil {
		return err
	}
	if err := os.Chtimes(targetPath, info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	currentInfo, err := source.Stat()
	if err != nil {
		return err
	}
	if currentInfo.Size() != info.Size() || !currentInfo.ModTime().Equal(info.ModTime()) {
		return fmt.Errorf("复制期间源文件发生变化")
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return err
	}
	currentHash := sha256.New()
	if _, err := io.Copy(currentHash, source); err != nil {
		return err
	}
	verifiedInfo, err := source.Stat()
	if err != nil {
		return err
	}
	if verifiedInfo.Size() != info.Size() || !verifiedInfo.ModTime().Equal(info.ModTime()) || !bytes.Equal(currentHash.Sum(nil), copiedHash.Sum(nil)) {
		return fmt.Errorf("复制校验失败，源文件发生变化")
	}
	if err := source.Close(); err != nil {
		return err
	}
	if err := os.Remove(srcPath); err != nil {
		return err
	}
	removeTarget = false
	return nil
}
