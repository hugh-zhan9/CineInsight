package services

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// publishBackup 是两个后端共用的后半段（4.1 第 3–6 步）：查空间 → 写文件包 → 写清单 →
// 依次发布快照、文件包、清单。清单最后发布：中途失败时已发布的成员会被删掉，
// 删不掉的半成品（有文件包没有清单）由下次轮转清理。
func (s *BackupService) publishBackup(ctx context.Context, directory, snapshotTemp, name string, now time.Time, manifest BackupManifest) (*BackupFile, error) {
	info, err := os.Stat(snapshotTemp)
	if err != nil || info.Size() == 0 {
		if err == nil {
			err = errors.New("备份产物为空")
		}
		return nil, err
	}
	if err := os.Chmod(snapshotTemp, 0600); err != nil {
		return nil, err
	}
	// 先算指纹再发布：发布之后不再有可失败的读取步骤。
	snapshotHash, err := hashFile(snapshotTemp)
	if err != nil {
		return nil, fmt.Errorf("读取备份产物失败: %w", err)
	}
	if err := s.ensureBackupSpace(directory, info.Size()); err != nil {
		return nil, err
	}
	filesTemp, err := createBackupTempFile(directory)
	if err != nil {
		return nil, err
	}
	defer os.Remove(filesTemp)
	files, unavailable, err := s.writeFilesPackTo(ctx, filesTemp)
	if err != nil {
		return nil, err
	}
	manifest.UnavailableRoots = unavailable
	files.Name = backupFilesName(name)
	if manifest.Models, err = currentModelSchema(); err != nil {
		return nil, err
	}
	manifest.Format = backupManifestFormat
	manifest.CreatedAt = now
	manifest.Snapshot = BackupManifestFile{Name: name, Size: info.Size(), SHA256: snapshotHash}
	manifest.Files = files
	if manifest.Schema.Tables == nil {
		manifest.Schema.Tables = map[string][]string{}
	}
	manifestTemp, err := writeBackupManifestTemp(directory, manifest)
	if err != nil {
		return nil, err
	}
	defer os.Remove(manifestTemp)

	published := []string{}
	for _, member := range []struct{ temp, name string }{
		{snapshotTemp, name}, {filesTemp, files.Name}, {manifestTemp, backupManifestName(name)},
	} {
		if err := publishBackupFile(directory, member.temp, member.name); err != nil {
			for _, done := range published {
				_ = os.Remove(filepath.Join(directory, done))
			}
			return nil, err
		}
		published = append(published, member.name)
	}
	s.setFilesWarning(unavailable)
	return &BackupFile{
		Name: name, Size: info.Size(), CreatedAt: now, Fingerprint: snapshotHash,
		FilesSize: files.Size, FileCount: len(files.Entries), UnavailableRoots: unavailable,
	}, nil
}

// ensureBackupSpace 检查备份目录所在卷还放得下「文件包预计大小 + 快照大小」（4.1 第 3 步）。
// 海报副本多时文件包可能远大于数据库，不够就这次备份失败，原因码 backup_insufficient_space。
func (s *BackupService) ensureBackupSpace(directory string, snapshotSize int64) error {
	estimate, err := estimateBackupFilesPack(s.dataDir)
	if err != nil {
		return fmt.Errorf("统计待备份文件失败: %w", err)
	}
	diskFree := s.diskFree
	if diskFree == nil {
		diskFree = enhancementDiskFree
	}
	free, err := diskFree(directory)
	if err != nil {
		return fmt.Errorf("无法检查备份目录所在磁盘的剩余空间: %w", pathlessError(err))
	}
	if need := uint64(estimate + snapshotSize); free < need {
		return fmt.Errorf("%s: 备份目录所在磁盘剩余空间不足（需要约 %s，可用 %s）",
			BackupReasonInsufficientSpace, formatEnhancementBytes(need), formatEnhancementBytes(free))
	}
	return nil
}

// writeFilesPackTo 把文件包写进临时文件，边写边算整个文件的哈希与大小。
func (s *BackupService) writeFilesPackTo(ctx context.Context, path string) (BackupManifestFiles, []string, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return BackupManifestFiles{}, nil, fmt.Errorf("创建文件包失败: %w", pathlessError(err))
	}
	hash := sha256.New()
	counter := &countingWriter{}
	entries, unavailable, err := writeBackupFilesPack(ctx, s.dataDir, io.MultiWriter(file, hash, counter))
	closeErr := file.Close()
	if err != nil {
		return BackupManifestFiles{}, nil, fmt.Errorf("写出文件包失败: %w", err)
	}
	if closeErr != nil {
		return BackupManifestFiles{}, nil, fmt.Errorf("写出文件包失败: %w", pathlessError(closeErr))
	}
	return BackupManifestFiles{Size: counter.n, SHA256: fmt.Sprintf("%x", hash.Sum(nil)), Entries: entries}, unavailable, nil
}

type countingWriter struct{ n int64 }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

func writeBackupManifestTemp(directory string, manifest BackupManifest) (string, error) {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("生成备份清单失败: %w", err)
	}
	path, err := createBackupTempFile(directory)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("写出备份清单失败: %w", pathlessError(err))
	}
	return path, nil
}

// publishBackupFile 用「独占创建占位 + rename」把临时文件发布成正式文件：不覆盖同名文件，
// 也不依赖用户所选磁盘的硬链接支持。
func publishBackupFile(directory, tempPath, name string) error {
	finalPath := filepath.Join(directory, name)
	if err := os.Chmod(tempPath, 0600); err != nil {
		return fmt.Errorf("发布备份文件失败: %w", pathlessError(err))
	}
	placeholder, err := os.OpenFile(finalPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("发布备份文件失败: %w", pathlessError(err))
	}
	if err := placeholder.Close(); err != nil {
		_ = os.Remove(finalPath)
		return fmt.Errorf("发布备份文件失败: %w", pathlessError(err))
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(finalPath)
		return fmt.Errorf("发布备份文件失败: %w", pathlessFileError(err))
	}
	return nil
}

// pathlessFileError 在 pathlessError 之外再去掉改名错误（*os.LinkError）里的两个路径（G-3）：
// 这些错误会进 backup_last_error 与界面文案。
func pathlessFileError(err error) error {
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return linkErr.Err
	}
	return pathlessError(err)
}

// unavailableRootsWarning 是「这份备份少了哪些整理文件」的提示文案（相对数据目录的路径，不含绝对路径）。
func unavailableRootsWarning(roots []string) string {
	if len(roots) == 0 {
		return ""
	}
	return "备份时 " + strings.Join(roots, "、") + " 不可用（链接失效或磁盘没挂载），这部分整理过的文件没有备份进去"
}

// setFilesWarning 记下最近一次备份的整理文件提示，设置页的备份状态显示它（D-DH-01，审阅 R2）。
func (s *BackupService) setFilesWarning(roots []string) {
	s.registryMu.Lock()
	s.filesWarning = unavailableRootsWarning(roots)
	s.registryMu.Unlock()
}
