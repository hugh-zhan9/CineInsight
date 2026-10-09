package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"video-master/database"
	"video-master/models"
)

type consolidationDirectoryVersion struct {
	RealPath  string
	Identity  string
	Size      int64
	ModTimeNS int64
	Mode      os.FileMode
	Exists    bool
}

// 路径按原始链接顺序解析；缺失目录也留下字面目标，以发现扫描期间别名改向。
func snapshotConsolidationDirectoryVersion(path string) (consolidationDirectoryVersion, error) {
	real, err := resolveMigrationPath(path)
	if err != nil {
		return consolidationDirectoryVersion{}, err
	}
	result := consolidationDirectoryVersion{RealPath: real}
	info, err := os.Stat(real)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !info.IsDir() {
		return result, fmt.Errorf("引用父路径不是目录: %s", path)
	}
	result.Identity = stableFileIdentity(info)
	result.Size = info.Size()
	result.ModTimeNS = info.ModTime().UnixNano()
	result.Mode = info.Mode()
	result.Exists = true
	if result.Identity == "" {
		return result, fmt.Errorf("引用目录没有稳定身份: %s", path)
	}
	return result, nil
}

func uncleanMigrationParent(path string) string {
	end := strings.LastIndex(path, string(filepath.Separator))
	if end < 0 {
		return "."
	}
	if end == 0 {
		return string(filepath.Separator)
	}
	return path[:end]
}

type consolidationPublishSnapshot struct {
	active      []fileMigrationActivePath
	directories map[string]consolidationDirectoryVersion
	relevant    []string
}

// 全库路径解析、来源附件与目标目录枚举全部在路径锁外。每项重新构建，不能按未变
// 的 DB Path 复用旧的符号链接索引。单个 OS stat 不可取消，外部进程不受应用锁约束。
func prepareConsolidationPublishSnapshot(ctx context.Context, e *consolidationExecution, index int) (*consolidationPublishSnapshot, error) {
	item := e.plan.Preview.Items[index]
	snapshot := &consolidationPublishSnapshot{directories: make(map[string]consolidationDirectoryVersion), relevant: []string{uncleanMigrationParent(item.SourcePath), filepath.Dir(item.Files[0].Source.RealPath), e.plan.Preview.DestinationInfo.Path, e.plan.Preview.DestinationInfo.RealPath}}
	observe := func(path string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, exists := snapshot.directories[path]; exists {
			return nil
		}
		version, err := snapshotConsolidationDirectoryVersion(path)
		if err != nil {
			return err
		}
		snapshot.directories[path] = version
		if e.hooks != nil && e.hooks.inventoryPath != nil {
			return e.hooks.inventoryPath(ctx, path)
		}
		return nil
	}
	for _, path := range snapshot.relevant {
		if err := observe(path); err != nil {
			return nil, err
		}
	}
	inventory, err := newFileMigrationInventory(ctx)
	if err != nil {
		return nil, err
	}
	inventory.observePath = func(path string) error { return observe(uncleanMigrationParent(path)) }
	if err := inventory.loadActivePaths(ctx); err != nil {
		return nil, err
	}
	if err := retainConsolidationAttachmentOwners(ctx, inventory, item, e.completedSourceNames); err != nil {
		return nil, err
	}
	if err := validateConsolidationItem(ctx, inventory, item, e.plan.Preview.DestinationInfo); err != nil {
		return nil, err
	}
	snapshot.active = inventory.activePaths
	for path, before := range snapshot.directories {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		after, err := snapshotConsolidationDirectoryVersion(path)
		if err != nil {
			return nil, err
		}
		if after != before {
			return nil, fmt.Errorf("构建发布库存期间目录或别名已变化: %s", path)
		}
	}
	if err := e.checkpoint("inventory_ready", item.VideoID, -1); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// 锁内完整 pairs SQL 比较保护读锁下的扫描/重定位写入；不能省略，也不在锁内
// 重建任何全库文件系统索引。这里只复验本家庭和相关目录，变化立即停止。
func (snapshot *consolidationPublishSnapshot) recheck(ctx context.Context, item FileMigrationItem) error {
	rows, err := loadMigrationActivePaths(ctx)
	if err != nil {
		return err
	}
	if !slices.Equal(rows, snapshot.active) {
		return fmt.Errorf("发布前活跃视频引用已变化，请重新预览")
	}
	for _, path := range snapshot.relevant {
		current, err := snapshotConsolidationDirectoryVersion(path)
		if err != nil {
			return err
		}
		if current != snapshot.directories[path] {
			return fmt.Errorf("发布前目录或别名已变化: %s", path)
		}
	}
	var video models.Video
	if err := database.DB.WithContext(ctx).First(&video, item.VideoID).Error; err != nil {
		return err
	}
	if video.Path != item.SourcePath || video.IsStale {
		return fmt.Errorf("发布前视频路径或状态已变化")
	}
	for _, file := range item.Files {
		if err := verifyConsolidationSource(file.Source); err != nil {
			return err
		}
		if _, err := os.Lstat(file.Destination); err == nil {
			return fmt.Errorf("确认的目标已占用: %s", file.Destination)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
