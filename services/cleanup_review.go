package services

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// loadCleanupReviewPairs 收口“不是同片 / 不是同源”的否决，所有相似关系类别共用。
// 截取片段的“忽略”有方向与源文件版本边界，仍单独保存在 ClipDismissal。
//
// current 是用来核对近似重复忽略是否失效的 size:mtimeNS（分析时本轮 stat 的结果，
// 回读缓存时是缓存算出那一刻的结果）；同源否决不带指纹，照旧一直有效。
func loadCleanupReviewPairs(current map[uint]string) (map[[2]uint]struct{}, error) {
	pairs, err := loadActiveNearDuplicateDismissals(current)
	if err != nil {
		return nil, err
	}
	rejected, err := loadRejectedSameSourcePairs()
	if err != nil {
		return nil, err
	}
	for pair := range rejected {
		pairs[pair] = struct{}{}
	}
	return pairs, nil
}

// filterCleanupReviewDecisions 只过滤结果快照，不扫描媒体或重跑分析。
// 旧缓存与分析期间保存的决定都要在交给调用方前复核，不能只相信计算时的排除集。
func filterCleanupReviewDecisions(analysis *CleanupAnalysis) (*CleanupAnalysis, error) {
	if analysis == nil {
		return nil, nil
	}
	result := *analysis
	if len(analysis.NearDuplicateGroups)+len(analysis.SameSourceGroups)+len(analysis.ClipGroups)+
		len(analysis.LowDuration)+len(analysis.LowResolution) == 0 {
		return &result, nil
	}
	pairs, err := loadCleanupReviewPairs(analysis.sourceFingerprints)
	if err != nil {
		return nil, err
	}
	result.NearDuplicateGroups = make([]CleanupDuplicateGroup, 0, len(analysis.NearDuplicateGroups))
	for _, group := range analysis.NearDuplicateGroups {
		result.NearDuplicateGroups = append(result.NearDuplicateGroups, splitReviewedCleanupGroup(group, pairs)...)
	}
	result.SameSourceGroups = make([]CleanupSameSourceGroup, 0, len(analysis.SameSourceGroups))
	for _, group := range analysis.SameSourceGroups {
		if _, denied := pairs[cleanupVideoPairKey(group.Preferred.ID, group.Alternative.ID)]; !denied {
			result.SameSourceGroups = append(result.SameSourceGroups, group)
		}
	}
	if len(analysis.ClipGroups) > 0 {
		dismissed, err := loadClipDismissals()
		if err != nil {
			return nil, err
		}
		result.ClipGroups = make([]CleanupClipGroup, 0, len(analysis.ClipGroups))
		for _, group := range analysis.ClipGroups {
			if _, denied := pairs[cleanupVideoPairKey(group.Full.ID, group.Clip.ID)]; denied {
				continue
			}
			full := clipSequence{video: group.Full, sourceSize: group.fullFingerprint.size, sourceMod: group.fullFingerprint.modTimeNS}
			clip := clipSequence{video: group.Clip, sourceSize: group.clipFingerprint.size, sourceMod: group.clipFingerprint.modTimeNS}
			if !clipDismissalStillApplies(dismissed, full, clip) {
				result.ClipGroups = append(result.ClipGroups, group)
			}
		}
	}
	if len(analysis.LowDuration)+len(analysis.LowResolution) > 0 {
		videoDismissals, err := loadCleanupVideoDismissals()
		if err != nil {
			return nil, err
		}
		result.LowDuration = filterDismissedCleanupVideos(analysis.LowDuration, models.CleanupDismissalCategoryShort, videoDismissals, analysis.sourceFingerprints)
		result.LowResolution = filterDismissedCleanupVideos(analysis.LowResolution, models.CleanupDismissalCategoryLow, videoDismissals, analysis.sourceFingerprints)
	}
	return &result, nil
}

// 组内任意两人被判为不同片，都不能再放在同一组；其余成员仍可审阅。
// 与近似重复生成器一样，各组互不重叠、组内两两相容，保留原来的推荐顺序。
func splitReviewedCleanupGroup(group CleanupDuplicateGroup, denied map[[2]uint]struct{}) []CleanupDuplicateGroup {
	remaining := append([]models.Video{group.Original}, group.Candidates...)
	groups := make([]CleanupDuplicateGroup, 0, 1)
	for len(remaining) > 1 {
		members := []models.Video{remaining[0]}
		next := make([]models.Video, 0, len(remaining)-1)
		for _, candidate := range remaining[1:] {
			allowed := true
			for _, member := range members {
				if _, blocked := denied[cleanupVideoPairKey(member.ID, candidate.ID)]; blocked {
					allowed = false
					break
				}
			}
			if allowed {
				members = append(members, candidate)
			} else {
				next = append(next, candidate)
			}
		}
		if len(members) > 1 {
			groups = append(groups, CleanupDuplicateGroup{Original: members[0], Candidates: members[1:], Reason: group.Reason})
		}
		remaining = next
	}
	return groups
}

// ===== 忽略记录的文件指纹（D-PC31）=====

// cleanupFileFingerprint 是忽略记录里的文件指纹：size:mtimeNS。
func cleanupFileFingerprint(size, modTimeNS int64) string {
	return strconv.FormatInt(size, 10) + ":" + strconv.FormatInt(modTimeNS, 10)
}

// statCleanupFileFingerprint 读文件此刻的指纹；目录与其他非普通文件不算。
func statCleanupFileFingerprint(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("不是普通文件")
	}
	return cleanupFileFingerprint(info.Size(), info.ModTime().UnixNano()), nil
}

// cleanupDismissalSideApplies 判断忽略记录的一侧是否仍然有效：记录为空（历史行）永不失效；
// 当前指纹未知（这一侧本轮读不到）无从核对，照旧有效；否则必须与当前一致。
func cleanupDismissalSideApplies(recorded string, current map[uint]string, mediaID uint) bool {
	if recorded == "" {
		return true
	}
	now, known := current[mediaID]
	return !known || now == recorded
}

// ===== 极短片段 / 极低分辨率的忽略（D-PC31、APP-11）=====

type cleanupVideoDismissalKey struct {
	videoID  uint
	category string
}

// DismissCleanupVideo 忽略一个「极短片段」（category=short）或「极低分辨率」（category=low）候选，
// 连同文件此刻的指纹写入 cleanup_video_dismissals；文件变了忽略随之失效，候选重新出现。
// 同一视频同一类别重复忽略会刷新指纹。
func DismissCleanupVideo(videoID uint, category string) error {
	if videoID == 0 {
		return errors.New("忽略候选需要视频 ID")
	}
	if category != models.CleanupDismissalCategoryShort && category != models.CleanupDismissalCategoryLow {
		return fmt.Errorf("未知的忽略类别：%s", category)
	}
	fingerprints, err := loadVideoFileFingerprints([]uint{videoID})
	if err != nil {
		return err
	}
	dismissal := models.CleanupVideoDismissal{VideoID: videoID, Category: category, Fingerprint: fingerprints[videoID]}
	return database.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "video_id"}, {Name: "category"}},
		DoUpdates: clause.AssignmentColumns([]string{"fingerprint", "updated_at"}),
	}).Create(&dismissal).Error
}

func loadCleanupVideoDismissals() (map[cleanupVideoDismissalKey]string, error) {
	var rows []models.CleanupVideoDismissal
	if err := database.DB.Select("video_id", "category", "fingerprint").Find(&rows).Error; err != nil {
		return nil, err
	}
	dismissals := make(map[cleanupVideoDismissalKey]string, len(rows))
	for _, row := range rows {
		dismissals[cleanupVideoDismissalKey{videoID: row.VideoID, category: row.Category}] = row.Fingerprint
	}
	return dismissals, nil
}

// filterDismissedCleanupVideos 去掉仍然有效的忽略所覆盖的候选；返回新切片，不改共享缓存。
func filterDismissedCleanupVideos(videos []models.Video, category string, dismissals map[cleanupVideoDismissalKey]string, current map[uint]string) []models.Video {
	if videos == nil {
		return nil
	}
	kept := make([]models.Video, 0, len(videos))
	for _, video := range videos {
		recorded, dismissed := dismissals[cleanupVideoDismissalKey{videoID: video.ID, category: category}]
		if dismissed && cleanupDismissalSideApplies(recorded, current, video.ID) {
			continue
		}
		kept = append(kept, video)
	}
	return kept
}

// ===== 「已忽略」管理列表与撤销（D-PC31）=====

// 忽略记录的类别。kind 同时决定读哪张表；同源否决不在此列，由同源审阅页处理。
const (
	CleanupDismissalKindNearDuplicate      = "near_duplicate"       // near_duplicate_dismissals
	CleanupDismissalKindClip               = "clip"                 // clip_dismissals
	CleanupDismissalKindShort              = "short"                // cleanup_video_dismissals.category=short
	CleanupDismissalKindLow                = "low"                  // cleanup_video_dismissals.category=low
	CleanupDismissalKindImageNearDuplicate = "image_near_duplicate" // image_near_duplicate_dismissals
)

// CleanupDismissalMedia 是忽略记录涉及的一个媒体。Missing 表示它已被删除（软删或永久删除）。
type CleanupDismissalMedia struct {
	ID      uint   `json:"id"`
	Name    string `json:"name"`
	Missing bool   `json:"missing"`
}

// CleanupDismissalItem 是一条忽略记录。Media 的顺序：近似重复为低/高 ID，截取片段为完整片/片段，
// 极短/极低只有一个视频。
type CleanupDismissalItem struct {
	ID        uint                    `json:"id"`
	Kind      string                  `json:"kind"`
	Media     []CleanupDismissalMedia `json:"media"`
	CreatedAt time.Time               `json:"created_at" ts_type:"string"`
}

// CleanupDismissalPage 是一页忽略记录（按记录 ID 倒序，NextCursor 作为下一次的 cursor）。
type CleanupDismissalPage struct {
	Items      []CleanupDismissalItem `json:"items"`
	NextCursor uint                   `json:"next_cursor"`
	HasMore    bool                   `json:"has_more"`
}

// CleanupDismissalUndoResult 是撤销结果；Removed 是实际删掉的记录数（已不存在的 ID 不计）。
type CleanupDismissalUndoResult struct {
	Kind    string `json:"kind"`
	Removed int64  `json:"removed"`
}

type cleanupDismissalRow struct {
	ID        uint
	MediaA    uint
	MediaB    uint
	CreatedAt time.Time
}

// cleanupDismissalSource 描述一个 kind 对应的表与列。
type cleanupDismissalSource struct {
	model       interface{}
	firstCol    string
	secondCol   string // 为空表示只涉及一个媒体
	category    string // 非空时按 cleanup_video_dismissals.category 过滤
	imageMedias bool
}

func cleanupDismissalSourceFor(kind string) (cleanupDismissalSource, error) {
	switch kind {
	case CleanupDismissalKindNearDuplicate:
		return cleanupDismissalSource{model: &models.NearDuplicateDismissal{}, firstCol: "video_low_id", secondCol: "video_high_id"}, nil
	case CleanupDismissalKindClip:
		return cleanupDismissalSource{model: &models.ClipDismissal{}, firstCol: "video_full_id", secondCol: "video_clip_id"}, nil
	case CleanupDismissalKindShort:
		return cleanupDismissalSource{model: &models.CleanupVideoDismissal{}, firstCol: "video_id", category: models.CleanupDismissalCategoryShort}, nil
	case CleanupDismissalKindLow:
		return cleanupDismissalSource{model: &models.CleanupVideoDismissal{}, firstCol: "video_id", category: models.CleanupDismissalCategoryLow}, nil
	case CleanupDismissalKindImageNearDuplicate:
		return cleanupDismissalSource{model: &models.ImageNearDuplicateDismissal{}, firstCol: "image_low_id", secondCol: "image_high_id", imageMedias: true}, nil
	}
	return cleanupDismissalSource{}, fmt.Errorf("未知的忽略类别：%s", kind)
}

func (source cleanupDismissalSource) query(db *gorm.DB) *gorm.DB {
	query := db.Model(source.model)
	if source.category != "" {
		query = query.Where("category = ?", source.category)
	}
	return query
}

// ListCleanupDismissals 按记录 ID 倒序分页列出某一类忽略记录（D-PC31「已忽略」页签）。
// cursor 为上一页的 NextCursor（0 表示第一页）；limit ≤0 取 50，上限 200。
func ListCleanupDismissals(kind string, cursor uint, limit int) (*CleanupDismissalPage, error) {
	source, err := cleanupDismissalSourceFor(kind)
	if err != nil {
		return nil, err
	}
	limit = normalizeEntityPageLimit(limit)
	columns := "id, created_at, " + source.firstCol + " AS media_a"
	if source.secondCol != "" {
		columns += ", " + source.secondCol + " AS media_b"
	}
	query := source.query(database.DB).Select(columns)
	if cursor > 0 {
		query = query.Where("id < ?", cursor)
	}
	var rows []cleanupDismissalRow
	if err := query.Order("id DESC").Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("列出忽略记录失败: %w", err)
	}
	page := &CleanupDismissalPage{Items: make([]CleanupDismissalItem, 0, len(rows))}
	if len(rows) > limit {
		page.HasMore = true
		rows = rows[:limit]
	}
	if len(rows) == 0 {
		return page, nil
	}
	mediaIDs := make([]uint, 0, len(rows)*2)
	for _, row := range rows {
		mediaIDs = append(mediaIDs, row.MediaA)
		if source.secondCol != "" {
			mediaIDs = append(mediaIDs, row.MediaB)
		}
	}
	medias, err := loadCleanupDismissalMedia(uniqueUintIDs(mediaIDs), source.imageMedias)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		item := CleanupDismissalItem{ID: row.ID, Kind: kind, CreatedAt: row.CreatedAt, Media: []CleanupDismissalMedia{medias.describe(row.MediaA)}}
		if source.secondCol != "" {
			item.Media = append(item.Media, medias.describe(row.MediaB))
		}
		page.Items = append(page.Items, item)
	}
	page.NextCursor = rows[len(rows)-1].ID
	return page, nil
}

type cleanupDismissalMediaIndex map[uint]CleanupDismissalMedia

func (index cleanupDismissalMediaIndex) describe(id uint) CleanupDismissalMedia {
	if media, ok := index[id]; ok {
		return media
	}
	// 永久删除之后行已不在：忽略记录没有外键，会惰性滞留，照样列出供撤销。
	return CleanupDismissalMedia{ID: id, Missing: true}
}

func loadCleanupDismissalMedia(ids []uint, images bool) (cleanupDismissalMediaIndex, error) {
	index := make(cleanupDismissalMediaIndex, len(ids))
	for _, chunk := range chunkUintIDs(ids, cleanupRankChunkSize) {
		if len(chunk) == 0 {
			continue
		}
		if images {
			var rows []models.Image
			if err := database.DB.Unscoped().Select("id", "name", "deleted_at").Where("id IN ?", chunk).Find(&rows).Error; err != nil {
				return nil, fmt.Errorf("读取忽略记录的图片失败: %w", err)
			}
			for _, row := range rows {
				index[row.ID] = CleanupDismissalMedia{ID: row.ID, Name: row.Name, Missing: row.DeletedAt.IsValid()}
			}
			continue
		}
		var rows []models.Video
		if err := database.DB.Unscoped().Select("id", "name", "deleted_at").Where("id IN ?", chunk).Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("读取忽略记录的视频失败: %w", err)
		}
		for _, row := range rows {
			index[row.ID] = CleanupDismissalMedia{ID: row.ID, Name: row.Name, Missing: row.DeletedAt.IsValid()}
		}
	}
	return index, nil
}

// UndoCleanupDismissals 撤销某一类里的若干条忽略记录（按记录 ID）。撤销后候选在下一次分析里
// 重新参与；已缓存的结果由调用方标为可能过期。近似重复的撤销不会恢复当时一并否决的同源关系。
func UndoCleanupDismissals(kind string, ids []uint) (*CleanupDismissalUndoResult, error) {
	source, err := cleanupDismissalSourceFor(kind)
	if err != nil {
		return nil, err
	}
	ids = uniqueUintIDs(ids)
	result := &CleanupDismissalUndoResult{Kind: kind}
	if len(ids) == 0 {
		return result, nil
	}
	for _, chunk := range chunkUintIDs(ids, cleanupRankChunkSize) {
		deleted := source.query(database.DB).Where("id IN ?", chunk).Delete(source.model)
		if deleted.Error != nil {
			return nil, fmt.Errorf("撤销忽略失败: %w", deleted.Error)
		}
		result.Removed += deleted.RowsAffected
	}
	return result, nil
}
