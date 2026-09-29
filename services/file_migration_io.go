package services

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// migrationStagingMarker 是暂存源文件名里固定的标记：.<名>.cineinsight-migrating-<hex>。
// 清理入口据此拒绝删除不像暂存文件的路径。
const migrationStagingMarker = ".cineinsight-migrating-"

// linkFile 是迁移时切换文件用的硬链接；测试替换它来模拟跨文件系统（链接失败、回退到复制）。
var linkFile = os.Link

// moveFileNoReplace returns a non-empty retainedSource when a cross-filesystem
// copy was required. The caller must keep it until it can safely surface or
// resolve the retained source after committing its metadata changes.
func moveFileNoReplace(source, destination string) (retainedSource string, returnErr error) {
	staging, err := migrationStagingPath(source)
	if err != nil {
		return "", err
	}
	if err := os.Rename(source, staging); err != nil {
		return "", err
	}
	rollback := func(cause error) error {
		if exists, checkErr := pathExists(source); checkErr != nil {
			return errors.Join(cause, fmt.Errorf("检查源路径回滚目标失败: %w", checkErr))
		} else if exists {
			return errors.Join(cause, fmt.Errorf("源路径已被占用，原文件保留在: %s", staging))
		}
		if rollbackErr := os.Rename(staging, source); rollbackErr != nil {
			return errors.Join(cause, fmt.Errorf("回滚源文件失败，原文件保留在 %s: %w", staging, rollbackErr))
		}
		return cause
	}

	sourceInfo, err := os.Lstat(staging)
	if err != nil {
		return "", rollback(err)
	}
	if sourceInfo.IsDir() {
		return "", rollback(fmt.Errorf("源路径是文件夹: %s", source))
	}

	if sourceInfo.Mode().IsRegular() {
		if err := linkFile(staging, destination); err == nil {
			if err := os.Remove(staging); err != nil {
				cleanupErr := os.Remove(destination)
				if cleanupErr != nil {
					return "", errors.Join(rollback(err), fmt.Errorf("清理目标硬链接失败: %w", cleanupErr))
				}
				return "", rollback(err)
			}
			return "", nil
		} else if exists, checkErr := pathExists(destination); checkErr != nil {
			return "", rollback(checkErr)
		} else if exists {
			return "", rollback(fmt.Errorf("目标文件已存在: %s", destination))
		}
	}

	if err := copyFileNoReplace(staging, destination); err != nil {
		return "", rollback(err)
	}
	return staging, nil
}

func migrationStagingPath(source string) (string, error) {
	for attempts := 0; attempts < 10; attempts++ {
		randomBytes := make([]byte, 16)
		if _, err := rand.Read(randomBytes); err != nil {
			return "", err
		}
		candidate := filepath.Join(
			filepath.Dir(source),
			fmt.Sprintf(".%s%s%s", filepath.Base(source), migrationStagingMarker, hex.EncodeToString(randomBytes)),
		)
		if exists, err := pathExists(candidate); err != nil {
			return "", err
		} else if !exists {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("无法分配迁移暂存路径: %s", source)
}

func rollbackMovedFile(original, destination, retainedSource string) error {
	if retainedSource == "" {
		retainedRollback, err := moveFileNoReplace(destination, original)
		if err != nil {
			return err
		}
		if retainedRollback != "" {
			return fmt.Errorf("回滚副本已恢复，但仍保留暂存源文件: %s", retainedRollback)
		}
		return nil
	}
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("清理迁移目标失败: %w", err)
	}
	if exists, err := pathExists(original); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("原路径已被占用，源文件保留在: %s", retainedSource)
	}
	if err := os.Rename(retainedSource, original); err != nil {
		return fmt.Errorf("恢复源文件失败，源文件保留在 %s: %w", retainedSource, err)
	}
	return nil
}

func copyFileNoReplace(source, destination string) (returnErr error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()

	info, err := input.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("不支持迁移非普通文件: %s", source)
	}

	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("目标文件已存在: %s", destination)
		}
		return err
	}
	created := true
	defer func() {
		if output != nil {
			_ = output.Close()
		}
		if returnErr != nil && created {
			_ = os.Remove(destination)
		}
	}()

	written, err := io.Copy(output, input)
	if err != nil {
		return err
	}
	if written != info.Size() {
		return fmt.Errorf("复制文件大小不一致: copied=%d expected=%d", written, info.Size())
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	output = nil
	if err := os.Chmod(destination, info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Chtimes(destination, info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	latestInfo, err := os.Stat(source)
	if err != nil {
		return err
	}
	if latestInfo.Size() != info.Size() || !latestInfo.ModTime().Equal(info.ModTime()) {
		return fmt.Errorf("复制期间源文件发生变化: %s", source)
	}
	created = false
	return nil
}

func copyDirectoryNoReplace(source, destination string) (returnErr error) {
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !sourceInfo.IsDir() {
		return fmt.Errorf("源路径不是文件夹: %s", source)
	}
	if err := os.Mkdir(destination, sourceInfo.Mode().Perm()); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("目标文件夹已存在: %s", destination)
		}
		return err
	}
	defer func() {
		if returnErr != nil {
			_ = os.RemoveAll(destination)
		}
	}()

	type directoryTimestamp struct {
		path    string
		modTime time.Time
	}
	directories := []directoryTimestamp{{path: destination, modTime: sourceInfo.ModTime()}}
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if err := os.Mkdir(target, info.Mode().Perm()); err != nil {
				return err
			}
			directories = append(directories, directoryTimestamp{path: target, modTime: info.ModTime()})
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(linkTarget, target)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("不支持迁移特殊文件: %s", path)
		}
		if err := linkFile(path, target); err == nil {
			return nil
		} else if exists, checkErr := pathExists(target); checkErr != nil {
			return checkErr
		} else if exists {
			return fmt.Errorf("目标文件已存在: %s", target)
		}
		return copyFileNoReplace(path, target)
	})
	if err != nil {
		return err
	}

	if err := verifyDirectoryCopy(source, destination); err != nil {
		return err
	}
	sort.Slice(directories, func(i, j int) bool {
		return strings.Count(directories[i].path, string(os.PathSeparator)) > strings.Count(directories[j].path, string(os.PathSeparator))
	})
	for _, directory := range directories {
		if err := os.Chtimes(directory.path, directory.modTime, directory.modTime); err != nil {
			return err
		}
	}
	return nil
}

type copiedEntry struct {
	mode       fs.FileMode
	size       int64
	linkTarget string
}

func verifyDirectoryCopy(source, destination string) error {
	sourceEntries, err := directoryEntries(source)
	if err != nil {
		return err
	}
	destinationEntries, err := directoryEntries(destination)
	if err != nil {
		return err
	}
	if len(sourceEntries) != len(destinationEntries) {
		return fmt.Errorf("复制后的目录条目数量不一致: source=%d destination=%d", len(sourceEntries), len(destinationEntries))
	}
	for relative, sourceEntry := range sourceEntries {
		destinationEntry, exists := destinationEntries[relative]
		if !exists || sourceEntry != destinationEntry {
			return fmt.Errorf("复制后的目录条目不一致: %s", relative)
		}
		if !sourceEntry.mode.IsRegular() {
			continue
		}
		sourcePath := filepath.Join(source, relative)
		destinationPath := filepath.Join(destination, relative)
		sourceInfo, err := os.Stat(sourcePath)
		if err != nil {
			return err
		}
		destinationInfo, err := os.Stat(destinationPath)
		if err != nil {
			return err
		}
		if os.SameFile(sourceInfo, destinationInfo) {
			continue
		}
		sourceHash, err := fileSHA256(sourcePath)
		if err != nil {
			return err
		}
		destinationHash, err := fileSHA256(destinationPath)
		if err != nil {
			return err
		}
		if sourceHash != destinationHash {
			return fmt.Errorf("复制后的文件内容不一致: %s", relative)
		}
	}
	return nil
}

func directoryCopyUsesIndependentFiles(source, destination string) (bool, error) {
	independent := false
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if independent || !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		sourceInfo, err := os.Stat(path)
		if err != nil {
			return err
		}
		destinationInfo, err := os.Stat(filepath.Join(destination, relative))
		if err != nil {
			return err
		}
		independent = !os.SameFile(sourceInfo, destinationInfo)
		return nil
	})
	return independent, err
}

func fileSHA256(path string) ([sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return [sha256.Size]byte{}, err
	}
	var sum [sha256.Size]byte
	copy(sum[:], hash.Sum(nil))
	return sum, nil
}

func directoryEntries(root string) (map[string]copiedEntry, error) {
	entries := make(map[string]copiedEntry)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := copiedEntry{mode: info.Mode().Type()}
		if info.Mode().IsRegular() {
			item.size = info.Size()
		}
		if entry.Type()&os.ModeSymlink != 0 {
			item.linkTarget, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		entries[relative] = item
		return nil
	})
	return entries, err
}

// stagedSourceInput 是待登记的一个暂存源。Staged 为空表示这次没有产生暂存（同盘硬链接切换）。
type stagedSourceInput struct {
	Original string
	Staged   string
	Size     int64
}

// registerStagedSources 在调用方的事务里登记跨盘迁移留下的暂存源（D-PC05、LIB-11）。
// 与路径切换同一事务：登记与数据库切换要么一起成功，要么一起回滚。
func registerStagedSources(tx *gorm.DB, videoID *uint, inputs []stagedSourceInput) error {
	for _, input := range inputs {
		if input.Staged == "" {
			continue
		}
		size := input.Size
		if size == 0 {
			if info, err := os.Lstat(input.Staged); err == nil && info.Mode().IsRegular() {
				size = info.Size()
			}
		}
		row := models.MigrationStagedSource{
			VideoID:      videoID,
			OriginalPath: input.Original,
			StagedPath:   input.Staged,
			Size:         size,
			State:        models.MigrationStagedStatePending,
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("登记迁移残留失败: %w", err)
		}
	}
	return nil
}

// directoryFileBytes 累加目录下普通文件的字节数，用于登记暂存文件夹的占用；读不到的按 0 计。
func directoryFileBytes(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// ListStagedSources 列出仍待处理的迁移残留，并逐行对账：文件已经不存在的行置为 cleaned
// 且不再返回。状态转换用条件更新，并发时后到的一方是空操作。
func (s *VideoService) ListStagedSources() ([]models.MigrationStagedSource, error) {
	var rows []models.MigrationStagedSource
	if err := database.DB.Where("state = ?", models.MigrationStagedStatePending).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取迁移残留失败: %w", err)
	}
	pending := make([]models.MigrationStagedSource, 0, len(rows))
	for _, row := range rows {
		if _, err := os.Lstat(row.StagedPath); errors.Is(err, os.ErrNotExist) {
			if !filePathOnline(row.StagedPath) {
				// 卷离线（或所在目录读不到）不是「文件没了」：保持 pending，等卷回来再对账（I1）。
				pending = append(pending, row)
				continue
			}
			if markErr := markStagedSourceCleaned(row.ID); markErr != nil {
				return nil, markErr
			}
			continue
		}
		pending = append(pending, row)
	}
	return pending, nil
}

func markStagedSourceCleaned(id uint) error {
	now := time.Now()
	if err := database.DB.Model(&models.MigrationStagedSource{}).
		Where("id = ? AND state = ?", id, models.MigrationStagedStatePending).
		Updates(map[string]interface{}{"state": models.MigrationStagedStateCleaned, "cleaned_at": &now}).Error; err != nil {
		return fmt.Errorf("更新迁移残留状态失败: %w", err)
	}
	return nil
}

// StagedSourceFailure 是某条迁移残留清理失败的原因（中文，不含路径）。
type StagedSourceFailure struct {
	ID    uint   `json:"id"`
	Error string `json:"error"`
}

// StagedSourceDeleteResult 汇总一次 DeleteStagedSources。
type StagedSourceDeleteResult struct {
	Cleaned int                   `json:"cleaned"`
	Failed  []StagedSourceFailure `json:"failed"`
}

// DeleteStagedSources 永久删除指定的迁移残留：os.Remove（暂存文件夹用 RemoveAll）成功后置 cleaned。
// 文件已经不存在按已清理处理。不是 pending 的 ID 与不存在的 ID 被忽略。
// 名字不带暂存标记的路径一律拒绝——表里的路径来自数据库，删除前必须确认它确实是暂存文件。
func (s *VideoService) DeleteStagedSources(ids []uint) (*StagedSourceDeleteResult, error) {
	result := &StagedSourceDeleteResult{Failed: make([]StagedSourceFailure, 0)}
	if len(ids) == 0 {
		return result, nil
	}
	var rows []models.MigrationStagedSource
	if err := database.DB.Where("id IN ? AND state = ?", ids, models.MigrationStagedStatePending).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取迁移残留失败: %w", err)
	}
	for _, row := range rows {
		if !strings.Contains(filepath.Base(row.StagedPath), migrationStagingMarker) {
			result.Failed = append(result.Failed, StagedSourceFailure{ID: row.ID, Error: "记录的路径不是迁移暂存文件，已拒绝删除"})
			continue
		}
		info, err := os.Lstat(row.StagedPath)
		if err == nil {
			// 删除前复核体积与登记时一致：不一致说明这个路径上的内容已被换过，不能删（Minor 9）。
			actual := info.Size()
			if info.IsDir() {
				actual = directoryFileBytes(row.StagedPath)
			}
			if actual != row.Size {
				result.Failed = append(result.Failed, StagedSourceFailure{ID: row.ID, Error: "暂存文件的大小与登记时不一致，已拒绝删除"})
				continue
			}
		} else if errors.Is(err, os.ErrNotExist) && !filePathOnline(row.StagedPath) {
			// 卷离线不是「文件没了」：不置 cleaned（I1）。
			result.Failed = append(result.Failed, StagedSourceFailure{ID: row.ID, Error: "文件所在磁盘当前不可访问，未做任何改动"})
			continue
		}
		if err == nil {
			if info.IsDir() {
				err = os.RemoveAll(row.StagedPath)
			} else {
				err = os.Remove(row.StagedPath)
			}
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			result.Failed = append(result.Failed, StagedSourceFailure{ID: row.ID, Error: "删除失败，请检查文件权限或是否被占用"})
			log.Printf("删除迁移残留失败 id=%d err=%v", row.ID, err)
			continue
		}
		if err := markStagedSourceCleaned(row.ID); err != nil {
			return result, err
		}
		result.Cleaned++
	}
	return result, nil
}
