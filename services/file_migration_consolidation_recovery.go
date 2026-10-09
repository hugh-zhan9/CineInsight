package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"video-master/database"
	"video-master/models"
)

type consolidationArtifact struct {
	path     string
	exists   bool
	snapshot FileMigrationSource
	verified bool
	err      error
}

// inspectConsolidationArtifact 的完整摘要在路径锁外计算。实际删除前在短锁内再比快照。
func inspectConsolidationArtifact(ctx context.Context, path, identity, digest string, expected FileMigrationSource, partial bool) consolidationArtifact {
	result := consolidationArtifact{path: path}
	if path == "" {
		return result
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return result
	}
	result.exists = true
	if err != nil {
		result.err = err
		return result
	}
	if identity == "" || !info.Mode().IsRegular() || stableFileIdentity(info) != identity {
		result.err = fmt.Errorf("归属无法确认，已保留: %s", path)
		return result
	}
	snapshot, err := snapshotMigrationSource(path)
	if err != nil {
		result.err = err
		return result
	}
	result.snapshot = snapshot
	if snapshot.RealPath != path {
		result.err = fmt.Errorf("实际位置已变化，已保留: %s", path)
		return result
	}
	if !partial {
		if snapshot.Size != expected.Size || snapshot.ModTimeNS != expected.ModTimeNS || snapshot.Mode != expected.Mode {
			result.err = fmt.Errorf("文件版本已变化，已保留: %s", path)
			return result
		}
		if digest != "" {
			actual, err := hashConsolidationFile(ctx, path, nil)
			if err != nil {
				result.err = err
				return result
			}
			if actual != digest {
				result.err = fmt.Errorf("内容摘要不符，已保留: %s", path)
				return result
			}
		}
	}
	result.verified = true
	return result
}

func (a consolidationArtifact) recheck() error {
	if !a.exists {
		return nil
	}
	if !a.verified {
		return a.err
	}
	actual, err := snapshotMigrationSource(a.path)
	if err != nil {
		return err
	}
	if actual != a.snapshot {
		return fmt.Errorf("对账期间文件已变化，已保留: %s", a.path)
	}
	return nil
}

func (s *VideoService) reconcileConsolidation(ctx context.Context, e *consolidationExecution) error {
	var failures []error
	completed := 0
	for i := range e.plan.Preview.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		previouslyCommitted := e.journal.Items[i].Phase == "committed"
		committed, err := s.reconcileConsolidationItem(ctx, e, i)
		if errors.Is(err, ErrConsolidationConflict) {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if committed {
			completed++
			if !previouslyCommitted {
				e.task.Completed++
			}
		}
		if err != nil {
			e.journal.Items[i].Error = err.Error()
			failures = append(failures, err)
		}
		if err := e.persistItem(i); err != nil {
			return err
		}
	}
	e.task.Completed = completed
	if err := e.persist(); err != nil {
		return errors.Join(errors.Join(failures...), err)
	}
	return errors.Join(failures...)
}

func (s *VideoService) reconcileConsolidationItem(ctx context.Context, e *consolidationExecution, index int) (bool, error) {
	item := e.plan.Preview.Items[index]
	record := &e.journal.Items[index]
	var video models.Video
	if err := database.DB.WithContext(ctx).First(&video, item.VideoID).Error; err != nil {
		return false, err
	}
	if item.Stay {
		if record.Phase != "committed" {
			return false, nil
		}
		if video.Path != item.SourcePath {
			return false, fmt.Errorf("视频路径已变化: %s", video.Path)
		}
		if err := verifyConsolidationSource(item.Files[0].Source); err != nil {
			return false, err
		}
		return true, nil
	}
	releaseSubtitles, err := tryLockConsolidationSubtitles(consolidationSubtitlePaths(item))
	if err != nil {
		return false, err
	}
	defer releaseSubtitles()
	type locations struct{ source, staged, target, temp consolidationArtifact }
	files := make([]locations, len(item.Files))
	if e.hooks != nil && e.hooks.reconcile != nil {
		if err := e.hooks.reconcile(ctx); err != nil {
			return false, err
		}
	}
	for i, file := range item.Files {
		log := record.Files[i]
		files[i] = locations{
			source: inspectConsolidationArtifact(ctx, file.Source.RealPath, file.Source.Identity, log.SHA256, file.Source, false),
			staged: inspectConsolidationArtifact(ctx, log.StagedSourcePath, file.Source.Identity, log.SHA256, file.Source, false),
			target: inspectConsolidationArtifact(ctx, file.Destination, log.PublishedIdentity, log.SHA256, log.TargetSnapshot, false),
			temp:   inspectConsolidationArtifact(ctx, log.TemporaryPath, log.TemporaryIdentity, log.SHA256, log.TargetSnapshot, log.Phase == "staging"),
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	releasePaths, err := lockLibraryPaths()
	if err != nil {
		return false, err
	}
	defer releasePaths()
	// 文件判据和任务 CAS 都要在真正操作前重新核对；不把旧版本执行者当恢复者。
	var current models.CleanupConsolidationTask
	if err := database.DB.First(&current, e.task.ID).Error; err != nil {
		return false, err
	}
	if current.Status != "running" || current.Version != e.task.Version || current.OwnerScope != e.task.OwnerScope {
		return false, ErrConsolidationConflict
	}
	if err := database.DB.First(&video, item.VideoID).Error; err != nil {
		return false, err
	}
	if err := validateConsolidationDirectory(e.plan.Preview.DestinationInfo); err != nil {
		return false, err
	}
	if video.Path == item.DestinationPath {
		for i, places := range files {
			if record.Files[i].SHA256 == "" || !places.target.exists || !places.target.verified {
				return false, fmt.Errorf("数据库已指向目标，但目标未验证；保留原/目标/暂存文件: %s → %s (%s)", item.SourcePath, item.DestinationPath, record.Files[i].StagedSourcePath)
			}
			if err := places.target.recheck(); err != nil {
				return false, err
			}
		}
		record.Phase = "committed"
		for i := range record.Files {
			record.Files[i].Phase = "committed"
		}
		// 同盘残留可安全去掉任务名字；跨盘的源仍交既有迁移残留入口管理。
		for i, places := range files {
			log := &record.Files[i]
			file := item.Files[i]
			if places.staged.exists && !file.CrossVolume && !log.Copied && !file.CopyOnly {
				if err := places.staged.recheck(); err != nil {
					return true, err
				}
				if err := os.Remove(places.staged.path); err != nil {
					return true, err
				}
				log.StagedSourcePath = ""
			}
		}
		return true, refreshConsolidationSubtitleIndex(item)
	}
	if video.Path != item.SourcePath {
		return false, fmt.Errorf("数据库路径已被外部修改，保留原/目标/暂存文件: %s → %s，当前 %s", item.SourcePath, item.DestinationPath, video.Path)
	}
	// 先证明每一个家庭成员都能恢复，避免验证一半就删除另一半的证据。
	for i, places := range files {
		file := item.Files[i]
		resolved, err := resolveMigrationPath(file.Source.Path)
		if err != nil {
			return false, err
		}
		if resolved != file.Source.RealPath {
			return false, fmt.Errorf("来源目录别名已变化，已保留全部文件: %s / %s", file.Source.Path, file.Source.RealPath)
		}
		if places.source.exists {
			if !places.source.verified {
				return false, places.source.err
			}
		} else if file.CopyOnly || !places.staged.exists || !places.staged.verified {
			return false, fmt.Errorf("原件无法确认；保留位置 %s / %s / %s", file.Source.RealPath, file.Destination, record.Files[i].StagedSourcePath)
		}
		for _, artifact := range []consolidationArtifact{places.source, places.staged, places.target, places.temp} {
			if artifact.exists {
				if err := artifact.recheck(); err != nil {
					return false, err
				}
			}
		}
	}
	for i, places := range files {
		if !places.source.exists {
			if err := publishConsolidationNoReplace(places.staged.path, item.Files[i].Source.RealPath); err != nil {
				return false, err
			}
			record.Files[i].StagedSourcePath = ""
		}
		// 只删除已验证的本任务输出；源及不明占位者完全保留。
		for _, artifact := range []consolidationArtifact{places.target, places.temp} {
			if artifact.exists {
				if err := os.Remove(artifact.path); err != nil {
					return false, err
				}
			}
		}
	}
	record.Phase = "planned"
	return false, nil
}
