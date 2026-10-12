package services

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// LostPurgeRequest 是清除的预览与执行请求；执行时 Confirm 必须等于「清除记录」。
type LostPurgeRequest struct {
	Kind    string `json:"kind"`
	IDs     []uint `json:"ids"`
	Confirm string `json:"confirm"`
}

// LostPurgePreview 是清除的连带影响（详细设计 1.3），只统计通过逐条复核（与执行同一套，只读）的条目。
// IDs 是统计到的那些条目：确认后只执行它们（执行时还会再复核一次）；Skipped 是其余条目按跳过原因的计数。
// 观看笔记保留，只告知。
type LostPurgePreview struct {
	Kind                   string         `json:"kind"`
	IDs                    []uint         `json:"ids"`
	Count                  int            `json:"count"`
	Size                   int64          `json:"size"`
	NotPurgeable           int            `json:"not_purgeable"`
	Skipped                map[string]int `json:"skipped"`
	PlayEvents             int64          `json:"play_events"`
	ImageViewEvents        int64          `json:"image_view_events"`
	TagLinks               int64          `json:"tag_links"`
	PersonLinks            int64          `json:"person_links"`
	CollectionLinks        int64          `json:"collection_links"`
	VersionGroupsDissolved int            `json:"version_groups_dissolved"`
	VersionGroupsShrunk    int            `json:"version_groups_shrunk"`
	Bookmarks              int64          `json:"bookmarks"`
	DiaryEntries           int64          `json:"diary_entries"`
	DerivedBytes           int64          `json:"derived_bytes"`
}

// purgeableLostRows 对给定 id 逐条做与执行相同的复核（purgeCheckRow，只读），返回通过的记录（按请求顺序）与
// 其余条目按跳过原因的计数。
func purgeableLostRows(ctx context.Context, kind string, ids []uint) ([]lostRow, map[string]int, error) {
	ids = uniqueUintIDs(ids)
	found, err := loadLostRowsByID(ctx, kind, ids)
	if err != nil {
		return nil, nil, err
	}
	classifier, err := newLostClassifier(kind)
	if err != nil {
		return nil, nil, err
	}
	rows := make([]lostRow, 0, len(found))
	skipped := map[string]int{}
	for _, id := range ids {
		row, ok := found[id]
		var skip errLostSkip
		switch err := purgeCheckRow(ctx, kind, classifier, row, ok); {
		case err == nil:
			rows = append(rows, row)
		case errors.As(err, &skip):
			skipped[skip.reason]++
		default:
			return nil, nil, err
		}
	}
	return rows, skipped, nil
}

// countIn 分批数 table 里 column 落在 ids 中的行。
func countIn(ctx context.Context, table, column string, ids []uint) (int64, error) {
	var total int64
	for start := 0; start < len(ids); start += lostBatchSize {
		end := min(start+lostBatchSize, len(ids))
		var count int64
		if err := database.DB.WithContext(ctx).Table(table).Where(column+" IN ?", ids[start:end]).Count(&count).Error; err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

// PreviewLostPurge 统计清除的连带影响（只读）。
func (s *LostMediaService) PreviewLostPurge(ctx context.Context, request LostPurgeRequest) (*LostPurgePreview, error) {
	if err := validLostMediaKind(request.Kind); err != nil {
		return nil, err
	}
	rows, skipped, err := purgeableLostRows(ctx, request.Kind, request.IDs)
	if err != nil {
		return nil, fmt.Errorf("读取失联条目失败: %w", err)
	}
	preview := &LostPurgePreview{Kind: request.Kind, Skipped: skipped}
	for _, count := range skipped {
		preview.NotPurgeable += count
	}
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		preview.Count++
		preview.Size += row.Size
	}
	preview.IDs = ids
	type relatedCount struct {
		target        *int64
		table, column string
	}
	counts := []relatedCount{
		{&preview.PlayEvents, "play_events", "video_id"},
		{&preview.TagLinks, "video_tags", "video_id"},
		{&preview.PersonLinks, "video_people", "video_id"},
		{&preview.CollectionLinks, "collection_videos", "video_id"},
		{&preview.Bookmarks, "video_bookmarks", "video_id"},
		{&preview.DiaryEntries, "viewing_diary_entries", "video_id"},
	}
	if request.Kind == LostMediaKindImage {
		counts = []relatedCount{
			{&preview.ImageViewEvents, "image_view_events", "image_id"},
			{&preview.TagLinks, "image_tags", "image_id"},
			{&preview.PersonLinks, "image_people", "image_id"},
		}
	}
	for _, count := range counts {
		if *count.target, err = countIn(ctx, count.table, count.column, ids); err != nil {
			return nil, fmt.Errorf("统计连带影响失败: %w", err)
		}
	}
	if request.Kind == LostMediaKindVideo {
		if preview.VersionGroupsDissolved, preview.VersionGroupsShrunk, err = versionGroupImpact(ctx, ids); err != nil {
			return nil, fmt.Errorf("统计版本组影响失败: %w", err)
		}
	}
	if preview.DerivedBytes, err = s.derivedBytes(ctx, request.Kind, ids); err != nil {
		return nil, fmt.Errorf("统计派生文件失败: %w", err)
	}
	return preview, nil
}

// versionGroupImpact 数这些视频所在的版本组里，清除后会解散（剩不足两个成员行）与缩小的组数。
func versionGroupImpact(ctx context.Context, ids []uint) (dissolved, shrunk int, err error) {
	purged := map[uint]int{}
	for start := 0; start < len(ids); start += lostBatchSize {
		end := min(start+lostBatchSize, len(ids))
		var members []models.VideoVersionMember
		if err := database.DB.WithContext(ctx).Select("video_id", "group_id").Where("video_id IN ?", ids[start:end]).Find(&members).Error; err != nil {
			return 0, 0, err
		}
		for _, member := range members {
			purged[member.GroupID]++
		}
	}
	for groupID, removed := range purged {
		var total int64
		if err := database.DB.WithContext(ctx).Model(&models.VideoVersionMember{}).Where("group_id = ?", groupID).Count(&total).Error; err != nil {
			return 0, 0, err
		}
		if total-int64(removed) < versionGroupMinMembers {
			dissolved++
		} else {
			shrunk++
		}
	}
	return dissolved, shrunk, nil
}

// derivedPaths 是一条记录在数据目录里的派生文件与目录（详细设计 1.3）：视频的缩略图与拖动预览帧、海报副本、
// 字幕备份；图片的缩略图与查看大图；以及人脸观测的裁剪图。数据目录不可用时为空（不猜相对路径）。
func (s *LostMediaService) derivedPaths(kind string, id uint, cropNames []string) []string {
	if s.dataDir == "" || !filepath.IsAbs(s.dataDir) {
		return nil
	}
	name := strconv.FormatUint(uint64(id), 10)
	var paths []string
	if kind == LostMediaKindVideo {
		thumbnails := filepath.Join(s.dataDir, "thumbnails")
		paths = append(paths, filepath.Join(thumbnails, name+".jpg"))
		// 拖动预览帧按 <id>.seek.<描述>.jpg 命名：列目录按精确前缀找（数据目录路径里可能有 Glob 元字符）。
		if entries, err := os.ReadDir(thumbnails); err == nil {
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasPrefix(entry.Name(), name+".seek.") {
					paths = append(paths, filepath.Join(thumbnails, entry.Name()))
				}
			}
		}
		paths = append(paths,
			filepath.Join(s.dataDir, "media-details", "videos", name),
			filepath.Join(s.dataDir, subtitleBackupDirName, name))
	} else {
		paths = append(paths,
			filepath.Join(s.dataDir, "image-thumbnails", name+".jpg"),
			filepath.Join(s.dataDir, "image-thumbnails", name+".view.jpg"))
	}
	for _, crop := range cropNames {
		base := filepath.Base(filepath.Clean(crop))
		if crop == "" || base == "." || base == ".." || base == string(filepath.Separator) {
			continue
		}
		paths = append(paths, filepath.Join(faceDataDirFor(s.dataDir), base))
	}
	return paths
}

// pathBytes 是一个文件或目录（递归）的字节数；不存在为 0。
func pathBytes(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// derivedBytes 估算清除会删掉的派生文件大小（含播放代理）。
func (s *LostMediaService) derivedBytes(ctx context.Context, kind string, ids []uint) (int64, error) {
	var total int64
	for start := 0; start < len(ids); start += lostBatchSize {
		end := min(start+lostBatchSize, len(ids))
		chunk := ids[start:end]
		var crops []struct {
			MediaID  uint
			CropPath string
		}
		if err := database.DB.WithContext(ctx).Model(&models.FaceObservation{}).Select("media_id, crop_path").
			Where("media_kind = ? AND media_id IN ? AND crop_path <> ?", kind, chunk, "").Scan(&crops).Error; err != nil {
			return 0, err
		}
		cropsByID := map[uint][]string{}
		for _, crop := range crops {
			cropsByID[crop.MediaID] = append(cropsByID[crop.MediaID], crop.CropPath)
		}
		for _, id := range chunk {
			for _, path := range s.derivedPaths(kind, id, cropsByID[id]) {
				total += pathBytes(path)
			}
		}
		if kind == LostMediaKindVideo {
			var proxyBytes int64
			if err := database.DB.WithContext(ctx).Model(&models.VideoPlaybackProxy{}).Select("COALESCE(SUM(output_size), 0)").
				Where("video_id IN ? AND status = ?", chunk, models.PlaybackProxyStatusReady).Scan(&proxyBytes).Error; err != nil {
				return 0, err
			}
			total += proxyBytes
		}
	}
	return total, nil
}

// removeDerivedFiles 删掉派生文件。删不掉只记日志、不算失败，留给孤儿文件回收（详细设计 1.3）。
func removeDerivedFiles(paths []string) {
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			log.Printf("失联清除：派生文件删除失败，留给孤儿回收 err=%v", pathlessError(err))
		}
	}
}

var errLostPurgeNoLongerLost = errors.New("失联条目状态已变化")

// ApplyLostPurge 彻底清除确认找不回来的条目（D-LD-01/04）：Confirm 必须等于「清除记录」。逐条在路径锁内复核，
// 任一条件不满足就跳过并说明原因；不因一条失败中断整批；取消时已清除的保留。不进废纸篓。
func (s *LostMediaService) ApplyLostPurge(parent context.Context, request LostPurgeRequest) (*LostMediaBatchResult, error) {
	if request.Confirm != LostPurgeConfirmText {
		return nil, ErrLostPurgeUnconfirmed
	}
	if err := validLostMediaKind(request.Kind); err != nil {
		return nil, err
	}
	ids := uniqueUintIDs(request.IDs)
	if len(ids) == 0 {
		return nil, lostMediaInvalid("没有选择要清除的条目")
	}
	ctx, finish, err := s.begin(parent, "purge", len(ids))
	if err != nil {
		return nil, err
	}
	defer finish()
	result := &LostMediaBatchResult{Items: []LostMediaItemOutcome{}}
	for _, id := range ids {
		if ctx.Err() != nil {
			result.Cancelled = true
			break
		}
		stop := recordLostOutcome(ctx, result, "清除", id, s.purgeOne(ctx, request.Kind, id))
		s.advance()
		if stop {
			break
		}
	}
	log.Printf("失联清除 kind=%s requested=%d purged=%d skipped=%d failed=%d cancelled=%t",
		request.Kind, len(ids), result.Done, result.Skipped, result.Failed, result.Cancelled)
	return result, nil
}

// purgeRecheck 在路径写锁内重读一条并复核（执行用）。
func purgeRecheck(ctx context.Context, kind string, id uint) (lostRow, error) {
	found, err := loadLostRowsByID(ctx, kind, []uint{id})
	if err != nil {
		return lostRow{}, err
	}
	classifier, err := newLostClassifier(kind)
	if err != nil {
		return lostRow{}, err
	}
	row, ok := found[id]
	return row, purgeCheckRow(ctx, kind, classifier, row, ok)
}

// purgeCheckRow 是清除的逐条复核（详细设计 1.3 第 1 步，预览与执行共用、只读）：仍属可清除归类、卷已挂载、
// 文件确实不存在、没有进行中的超分任务。found=false 表示这条已不是失联记录。不满足时返回 errLostSkip。
func purgeCheckRow(ctx context.Context, kind string, classifier *lostClassifier, row lostRow, found bool) error {
	if !found {
		return lostSkip(LostSkipNoLongerLost)
	}
	if reason := classifier.reason(row); !lostReasonActionable(reason) {
		return lostSkipForReason(reason)
	}
	if mediaPathUnavailable(row.Path) != nil {
		return lostSkip(LostSkipVolumeOffline)
	}
	if _, statErr := os.Stat(row.Path); !errors.Is(statErr, fs.ErrNotExist) {
		return lostSkip(LostSkipFilePresent)
	}
	if kind == LostMediaKindVideo {
		if err := ensureNoActiveEnhancement(database.DB.WithContext(ctx), row.ID); errors.Is(err, ErrVideoHasActiveEnhancement) {
			return lostSkip(LostSkipEnhancementActive)
		} else if err != nil {
			return err
		}
	}
	return nil
}

// lostPurgeBeforeTxHook 是单测替身：在逐条复核通过之后、开清除事务之前调用，用来模拟两者之间别的入口改了状态。
var lostPurgeBeforeTxHook func(id uint)

// scannerMissingEntry 读出扫描器回收站行的 missing 条目（与 lostVideoCondition 同口径）；没有时返回 nil。
func scannerMissingEntry(ctx context.Context, videoID uint) (*trashRow, error) {
	var row trashRow
	result := database.DB.WithContext(ctx).Table(videoTrashKind.table).Select(videoTrashKind.selectColumns()).
		Where("video_id = ? AND deleted_by = ? AND file_moved = ? AND state = ? AND mode IN ?",
			videoID, "scanner", false, trashStateDeleted, []string{models.TrashModeMissing, ""}).Limit(1).Scan(&row)
	if result.Error != nil || result.RowsAffected == 0 {
		return nil, result.Error
	}
	return &row, nil
}

// purgeOne 在路径写锁内复核并清除一条（详细设计 1.3 第 1–3 步）。
func (s *LostMediaService) purgeOne(ctx context.Context, kind string, id uint) error {
	unlock, err := lockLostKind(ctx, kind)
	if err != nil {
		return err
	}
	defer unlock()
	row, err := purgeRecheck(ctx, kind, id)
	if err != nil {
		return err
	}
	if hook := lostPurgeBeforeTxHook; hook != nil {
		hook(id)
	}
	var proxy *models.VideoPlaybackProxy
	var entry *trashRow
	if kind == LostMediaKindVideo {
		if proxy, err = loadProxyRowFrom(database.DB.WithContext(ctx), id); err != nil {
			return err
		}
		if row.deleted() {
			if entry, err = scannerMissingEntry(ctx, id); err != nil {
				return err
			}
		}
	}
	var cleanup lostReferenceCleanup
	err = database.TransactionWithContext(ctx, func(tx *gorm.DB) error {
		// 复核与事务之间别的入口（播放成功、扫描）可能已经清掉失效：事务里再确认一次仍是失联。
		var still int64
		if err := lostConditionQuery(tx, kind).Where(lostTable(kind)+".id = ?", id).Count(&still).Error; err != nil {
			return err
		}
		if still != 1 {
			return errLostPurgeNoLongerLost
		}
		if kind == LostMediaKindVideo {
			if err := removeFromVersionGroupTx(tx, id, time.Now()); err != nil {
				return err
			}
		}
		if cleanup, err = deleteLostReferencesTx(ctx, tx, kind, id); err != nil {
			return err
		}
		if entry != nil {
			if err := retireTrashEntryTx(tx, videoTrashKind, *entry, []string{trashStateDeleted}); err != nil {
				return err
			}
		}
		if kind == LostMediaKindVideo {
			return hardDeleteVideoTx(tx, id)
		}
		return hardDeleteImageTx(tx, id)
	})
	switch {
	case errors.Is(err, errLostPurgeNoLongerLost):
		return lostSkip(LostSkipNoLongerLost)
	case errors.Is(err, ErrVideoHasActiveEnhancement):
		return lostSkip(LostSkipEnhancementActive)
	case err != nil:
		return err
	}
	crops := make([]string, 0, len(cleanup.observations))
	for _, observation := range cleanup.observations {
		crops = append(crops, observation.CropPath)
	}
	removeDerivedFiles(s.derivedPaths(kind, id, crops))
	if proxy != nil && s.proxies != nil {
		s.proxies.discardProxy(*proxy, "lost_purged")
	}
	if cleanup.queueChanged {
		s.notifyQueueChanged()
	}
	log.Printf("失联清除 kind=%s id=%d source=%s", kind, id, row.source())
	return nil
}
