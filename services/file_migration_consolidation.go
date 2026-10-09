package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"

	"gorm.io/gorm"
)

func verifyConsolidationSource(expected FileMigrationSource) error {
	current, err := snapshotMigrationSource(expected.Path)
	if err != nil {
		return err
	}
	if current != expected {
		return fmt.Errorf("来源版本或路径已变化，请重新预览: %s", expected.Path)
	}
	return nil
}

func validateConsolidationDirectory(expected FileMigrationDirectory) error {
	current, err := snapshotMigrationDirectory(expected.Path)
	if err != nil {
		return err
	}
	if current != expected {
		return fmt.Errorf("目标目录已变化，请重新预览: %s", expected.Path)
	}
	return nil
}

func (s *VideoService) validateConsolidationFiles(ctx context.Context, preview CleanupConsolidationPreview) error {
	if err := validateConsolidationDirectory(preview.DestinationInfo); err != nil {
		return err
	}
	inventory, err := newFileMigrationInventory(ctx)
	if err != nil {
		return err
	}
	if err := inventory.loadActivePaths(ctx); err != nil {
		return err
	}
	for _, item := range preview.Items {
		if err := validateConsolidationItem(ctx, inventory, item, preview.DestinationInfo); err != nil {
			return err
		}
	}
	return nil
}

func validateConsolidationItem(ctx context.Context, inventory *fileMigrationInventory, item FileMigrationItem, target FileMigrationDirectory) error {
	if len(item.Files) == 0 {
		return fmt.Errorf("迁移清单为空")
	}
	if err := validateConsolidationDirectory(target); err != nil {
		return err
	}
	var video models.Video
	if err := database.DB.WithContext(ctx).First(&video, item.VideoID).Error; err != nil {
		return err
	}
	if video.Path != item.SourcePath || video.IsStale {
		return fmt.Errorf("视频路径已变化，请重新预览")
	}
	for _, file := range item.Files {
		if err := verifyConsolidationSource(file.Source); err != nil {
			return err
		}
		if !item.Stay && !file.CopyOnly {
			if err := inventory.checkSourceOwner(file.Source, item.VideoID); err != nil {
				return err
			}
		}
	}
	attachments, _, err := inventory.attachments(ctx, item.Files[0].Source)
	if err != nil {
		return err
	}
	files := migrationDestinationFiles(item.Files[0].Source, attachments, target.RealPath, filepath.Base(item.DestinationPath))
	for i := range files {
		files[i].CrossVolume = !sameMigrationVolume(files[i].Source.Identity, target.Identity)
	}
	if !reflect.DeepEqual(files, item.Files) {
		return fmt.Errorf("附件或共享归属已变化，请重新预览")
	}
	if !item.Stay {
		// 新 inventory 重读库引用及目录，不使用预览期索引为后出现的共享来源授权。
		reserved := make(map[string]bool)
		entries, err := os.ReadDir(target.RealPath)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			key, err := inventory.pathKey(filepath.Join(target.RealPath, entry.Name()))
			if err != nil {
				return err
			}
			reserved[key] = true
		}
		occupied, err := inventory.filesOccupied(ctx, item.Files, reserved)
		if err != nil {
			return err
		}
		if occupied {
			return fmt.Errorf("确认的目标已占用，请重新预览")
		}
	}
	return nil
}

func consolidationSubtitlePaths(item FileMigrationItem) []string {
	paths := []string{subtitleparser.SRTPathForVideo(item.SourcePath), subtitleparser.SRTPathForVideo(item.Files[0].Source.RealPath), subtitleparser.SRTPathForVideo(item.DestinationPath)}
	for _, file := range item.Files {
		if file.Kind == "subtitle" {
			paths = append(paths, file.Source.Path, file.Source.RealPath, file.Destination)
		}
	}
	return paths
}

func (s *VideoService) executeConsolidationItem(ctx context.Context, e *consolidationExecution, index int) error {
	item := e.plan.Preview.Items[index]
	log := &e.journal.Items[index]
	if err := e.checkpoint("planned", item.VideoID, -1); err != nil {
		return err
	}
	if err := e.persistItem(index); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if item.Stay {
		if err := verifyConsolidationSource(item.Files[0].Source); err != nil {
			return err
		}
		log.Phase = "committed"
		for i := range log.Files {
			log.Files[i].Phase = "committed"
		}
		e.task.Completed++
		return e.persistItem(index)
	}
	// 大文件阶段不持路径锁或数据库事务。字幕可以在此期间完成，发布时再核对。
	for i, file := range item.Files {
		if err := s.stageConsolidationFile(ctx, e, index, i, file); err != nil {
			return err
		}
	}
	snapshot, err := prepareConsolidationPublishSnapshot(ctx, e, index)
	if err != nil {
		return err
	}
	releaseSubtitles, err := tryLockConsolidationSubtitles(consolidationSubtitlePaths(item))
	if err != nil {
		return err
	}
	defer releaseSubtitles()
	releasePaths, err := lockLibraryPaths()
	if err != nil {
		return err
	}
	defer releasePaths()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := snapshot.recheck(ctx, item); err != nil {
		return err
	}
	for i, file := range item.Files {
		record := &log.Files[i]
		if err := checkConsolidationArtifact(record.TemporaryPath, record.TargetSnapshot, record.TemporaryIdentity); err != nil {
			return err
		}
		// 意图中预先写发布身份：rename 成功到下一次日志写入之间崩溃也能识别自己的文件。
		record.Phase = "publishing"
		record.PublishedIdentity = record.TemporaryIdentity
		log.Phase = "publishing"
		if err := e.persistItem(index); err != nil {
			return err
		}
		if err := e.checkpoint("publishing", item.VideoID, i); err != nil {
			return err
		}
		if err := validateConsolidationDirectory(e.plan.Preview.DestinationInfo); err != nil {
			return err
		}
		if err := verifyConsolidationSource(file.Source); err != nil {
			return err
		}
		if err := checkConsolidationArtifact(record.TemporaryPath, record.TargetSnapshot, record.TemporaryIdentity); err != nil {
			return err
		}
		if err := publishConsolidationNoReplace(record.TemporaryPath, file.Destination); err != nil {
			return fmt.Errorf("排他发布 %s: %w", file.Destination, err)
		}
		if err := syncSubtitleParentDirectory(filepath.Dir(file.Destination)); err != nil {
			return err
		}
		if err := e.checkpoint("published", item.VideoID, i); err != nil {
			return err
		}
		if !file.CopyOnly {
			if err := verifyConsolidationSource(file.Source); err != nil {
				return err
			}
			if err := publishConsolidationNoReplace(file.Source.RealPath, record.StagedSourcePath); err != nil {
				return err
			}
			if err := syncSubtitleParentDirectory(filepath.Dir(file.Source.RealPath)); err != nil {
				return err
			}
		}
		if err := e.checkpoint("source_staged", item.VideoID, i); err != nil {
			return err
		}
		if err := e.persistItem(index); err != nil {
			return err
		}
	}
	if err := e.checkpoint("before_commit", item.VideoID, -1); err != nil {
		return err
	}
	if err := validateConsolidationDirectory(e.plan.Preview.DestinationInfo); err != nil {
		return err
	}
	for i, file := range item.Files {
		record := log.Files[i]
		if err := checkConsolidationArtifact(file.Destination, record.TargetSnapshot, record.PublishedIdentity); err != nil {
			return err
		}
		if file.CopyOnly {
			if err := verifyConsolidationSource(file.Source); err != nil {
				return err
			}
		} else {
			resolved, err := resolveMigrationPath(file.Source.Path)
			if err != nil {
				return err
			}
			if resolved != file.Source.RealPath {
				return fmt.Errorf("来源目录别名已变化，保留所有位置: %s", file.Source.Path)
			}
			if _, err := os.Lstat(file.Source.RealPath); !os.IsNotExist(err) {
				return fmt.Errorf("来源位置在切换期间重新出现或不可读，保留文件: %s", file.Source.RealPath)
			}
			if err := checkConsolidationArtifact(record.StagedSourcePath, file.Source, record.SourceIdentity); err != nil {
				return err
			}
		}
	}
	previousCompleted := e.task.Completed
	commitAt := time.Now()
	log.Phase = "committed"
	for i := range log.Files {
		log.Files[i].Phase = "committed"
	}
	e.task.Completed++
	err = database.Transaction(func(tx *gorm.DB) error {
		changed := tx.Model(&models.Video{}).Where("id = ? AND path = ? AND is_stale = ?", item.VideoID, item.SourcePath, false).Updates(map[string]interface{}{"path": item.DestinationPath, "directory": filepath.Dir(item.DestinationPath), "name": filepath.Base(item.DestinationPath), "is_stale": false, "stale_reason": ""})
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return fmt.Errorf("视频路径条件更新失败，请重新审阅")
		}
		var staged []stagedSourceInput
		for i, file := range item.Files {
			if (file.CrossVolume || log.Files[i].Copied) && !file.CopyOnly {
				staged = append(staged, stagedSourceInput{Original: file.Source.Path, Staged: log.Files[i].StagedSourcePath, Size: file.Source.Size})
			}
		}
		if err := registerStagedSources(tx, &item.VideoID, staged); err != nil {
			return err
		}
		if err := syncShortVideoTagForVideo(tx, item.VideoID); err != nil {
			return err
		}
		return e.save(tx, index, commitAt)
	})
	if err != nil {
		e.task.Completed = previousCompleted
		log.Phase = "publishing"
		for i := range log.Files {
			log.Files[i].Phase = "publishing"
		}
		return err
	}
	e.acceptSave(commitAt)
	if err := e.checkpoint("committed", item.VideoID, -1); err != nil {
		return err
	}
	// 同盘硬链接提交后删本任务暂存名字；跨盘源登记残留，绝不在此删除。
	for i, file := range item.Files {
		if file.CopyOnly || file.CrossVolume || log.Files[i].Copied {
			continue
		}
		record := &log.Files[i]
		if err := checkConsolidationArtifact(record.StagedSourcePath, file.Source, record.SourceIdentity); err != nil {
			return err
		}
		if err := os.Remove(record.StagedSourcePath); err != nil {
			return err
		}
		record.StagedSourcePath = ""
	}
	// 索引是可重建派生数据，但失败仍如实报告，不带着旧路径成功返回。
	if err := refreshConsolidationSubtitleIndex(item); err != nil {
		return err
	}
	return e.persistItem(index)
}

func refreshConsolidationSubtitleIndex(item FileMigrationItem) error {
	targetSRT := subtitleparser.SRTPathForVideo(item.DestinationPath)
	if _, err := os.Stat(targetSRT); err == nil {
		err = indexSubtitleFileForVideoID(item.VideoID, targetSRT)
		if err != nil {
			_ = deleteSubtitleIndex(item.VideoID)
			return err
		}
	} else if os.IsNotExist(err) {
		if err := deleteSubtitleIndex(item.VideoID); err != nil {
			return err
		}
	} else {
		return err
	}
	return nil
}

func checkConsolidationArtifact(path string, source FileMigrationSource, identity string) error {
	if path == "" || identity == "" {
		return fmt.Errorf("文件归属证据不足: %s", path)
	}
	current, err := snapshotMigrationSource(path)
	if err != nil {
		return err
	}
	if current.RealPath != path || current.Identity != identity || current.Size != source.Size || current.ModTimeNS != source.ModTimeNS || current.Mode != source.Mode {
		return fmt.Errorf("文件身份或版本不符，已保留: %s", path)
	}
	return nil
}

func (s *VideoService) stageConsolidationFile(ctx context.Context, e *consolidationExecution, itemIndex, fileIndex int, file FileMigrationFile) error {
	item := &e.journal.Items[itemIndex]
	record := &item.Files[fileIndex]
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifyConsolidationSource(file.Source); err != nil {
		return err
	}
	if err := validateConsolidationDirectory(e.plan.Preview.DestinationInfo); err != nil {
		return err
	}
	record.Phase = "staging"
	item.Phase = "staging"
	if err := e.persistItem(itemIndex); err != nil {
		return err
	}
	if err := e.checkpoint("staging", item.VideoID, fileIndex); err != nil {
		return err
	}
	copyFile := file.CrossVolume || file.CopyOnly || (e.hooks != nil && e.hooks.forceCopy)
	if copyFile {
		record.Copied = true
		freeFn := enhancementDiskFree
		if e.hooks != nil && e.hooks.freeBytes != nil {
			freeFn = e.hooks.freeBytes
		}
		free, err := freeFn(filepath.Dir(file.Destination))
		if err != nil {
			return err
		}
		if uint64(file.Source.Size) > free {
			return fmt.Errorf("目标目录可用空间不足")
		}
		if err := copyConsolidationFile(ctx, e, itemIndex, fileIndex, file, record); err != nil {
			return err
		}
	} else {
		if err := os.Link(file.Source.RealPath, record.TemporaryPath); err != nil {
			return err
		}
		info, err := os.Lstat(record.TemporaryPath)
		if err != nil {
			return err
		}
		record.TemporaryIdentity = stableFileIdentity(info)
		if err := e.persistItem(itemIndex); err != nil {
			return err
		}
		digest, err := hashConsolidationFile(ctx, file.Source.RealPath, func(n int64) error { return e.progress(ctx, n) })
		if err != nil {
			return err
		}
		record.SHA256 = digest
	}
	if err := verifyConsolidationSource(file.Source); err != nil {
		return err
	}
	target, err := snapshotMigrationSource(record.TemporaryPath)
	if err != nil {
		return err
	}
	if target.Identity != record.TemporaryIdentity || target.Size != file.Source.Size {
		return fmt.Errorf("临时目标身份或长度已变化")
	}
	if copyFile && target != record.TargetSnapshot {
		return fmt.Errorf("摘要验证后目标已变化")
	}
	record.TargetSnapshot = target
	record.Phase = "verified"
	item.Phase = "verified"
	if err := e.persistItem(itemIndex); err != nil {
		return err
	}
	return e.checkpoint("verified", item.VideoID, fileIndex)
}

func copyConsolidationFile(ctx context.Context, e *consolidationExecution, itemIndex, fileIndex int, file FileMigrationFile, record *cleanupConsolidationFileJournal) (returnErr error) {
	source, err := os.Open(file.Source.RealPath)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if stableFileIdentity(info) != file.Source.Identity {
		return fmt.Errorf("打开期间来源已变化")
	}
	target, err := os.OpenFile(record.TemporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(file.Source.Mode))
	if err != nil {
		return err
	}
	defer func() {
		if err := target.Close(); returnErr == nil {
			returnErr = err
		}
	}()
	info, err = target.Stat()
	if err != nil {
		return err
	}
	record.TemporaryIdentity = stableFileIdentity(info)
	if err := e.persistItem(itemIndex); err != nil {
		return err
	}
	hash := sha256.New()
	buffer := make([]byte, 1024*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			if _, err := target.Write(buffer[:n]); err != nil {
				return err
			}
			_, _ = hash.Write(buffer[:n])
			if err := e.progress(ctx, int64(n)); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := target.Chmod(os.FileMode(file.Source.Mode)); err != nil {
		return err
	}
	if err := os.Chtimes(record.TemporaryPath, time.Unix(0, file.Source.ModTimeNS), time.Unix(0, file.Source.ModTimeNS)); err != nil {
		return err
	}
	if err := target.Sync(); err != nil {
		return err
	}
	if e.hooks != nil && e.hooks.targetMetadata != nil {
		if err := e.hooks.targetMetadata(record.TemporaryPath); err != nil {
			return err
		}
	}
	// 目标卷可能量化时间或按卷表达权限。保存实际目标版本，源版本仍严格独立核对。
	verifiedTarget, err := snapshotMigrationSource(record.TemporaryPath)
	if err != nil {
		return err
	}
	if verifiedTarget.Identity != record.TemporaryIdentity || verifiedTarget.Size != file.Source.Size {
		return fmt.Errorf("复制目标身份或长度不符")
	}
	record.SHA256 = hex.EncodeToString(hash.Sum(nil))
	if err := e.checkpoint("copied", e.journal.Items[itemIndex].VideoID, fileIndex); err != nil {
		return err
	}
	digest, err := hashConsolidationFile(ctx, record.TemporaryPath, nil)
	if err != nil {
		return err
	}
	if digest != record.SHA256 {
		return fmt.Errorf("复制后的完整 SHA-256 校验失败")
	}
	after, err := snapshotMigrationSource(record.TemporaryPath)
	if err != nil {
		return err
	}
	if after != verifiedTarget {
		return fmt.Errorf("摘要核对期间目标已变化")
	}
	record.TargetSnapshot = verifiedTarget
	return nil
}

func hashConsolidationFile(ctx context.Context, path string, progress func(int64) error) (string, error) {
	source, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer source.Close()
	hash := sha256.New()
	buffer := make([]byte, 1024*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
			if progress != nil {
				if err := progress(int64(n)); err != nil {
					return "", err
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// 已由本任务成功移走的视频仍参与原附件归属；不能把确认的共享复制升级成移动，
// 也不能把原来未处理的共享 NFO 在后续项中变成新附件。其他目录变化仍由正常复验拒绝。
func retainConsolidationAttachmentOwners(ctx context.Context, inventory *fileMigrationInventory, item FileMigrationItem, completed map[string]map[string]bool) error {
	dir := filepath.Dir(item.Files[0].Source.RealPath)
	removed := completed[dir]
	if len(removed) == 0 {
		return nil
	}
	if err := inventory.indexAttachments(ctx, dir); err != nil {
		return err
	}
	entries, err := inventory.directory(dir)
	if err != nil {
		return err
	}
	present := make(map[string]bool, len(entries))
	for _, entry := range entries {
		present[entry.Name()] = true
	}
	stems := make(map[string]int)
	for name := range removed {
		if !present[name] {
			stems[strings.TrimSuffix(name, filepath.Ext(name))]++
		}
	}
	for name := range inventory.ownersByDirectory[dir] {
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		if strings.EqualFold(ext, ".nfo") {
			inventory.ownersByDirectory[dir][name] += stems[base]
			continue
		}
		for end := len(base); end > 0; end = strings.LastIndex(base[:end], ".") {
			stem := base[:end]
			if stems[stem] > 0 && subtitleBelongsToStem(name, stem) {
				inventory.ownersByDirectory[dir][name] += stems[stem]
			}
		}
	}
	return nil
}
