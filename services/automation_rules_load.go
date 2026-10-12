package services

import (
	"context"
	"math"
	"strings"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 取数：按 id 批量读出求值需要的属性；手动运行先用 SQL 粗筛候选（目录前缀、大小、高度、时长），
// 再在 Go 侧精筛。粗筛只会多选不会漏选（SQLite 的 LIKE 不分大小写，Go 侧再按原样比较）。

// loadAutomationFacts 读一批（≤500）未删除媒体的属性，按 id 升序；不存在或已删除的 id 不出现在结果里。
func loadAutomationFacts(ctx context.Context, kind string, ids []uint, needCodec bool) ([]automationMediaFacts, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	db := database.DB.WithContext(ctx)
	if kind == AutomationMediaImage {
		var images []models.Image
		if err := db.Select("id", "path", "directory", "size", "exif_parsed_at", "gps_latitude", "gps_longitude").
			Where("id IN ?", ids).Order("id ASC").Find(&images).Error; err != nil {
			return nil, err
		}
		facts := make([]automationMediaFacts, 0, len(images))
		for _, image := range images {
			facts = append(facts, imageAutomationFacts(image))
		}
		return facts, nil
	}
	var videos []models.Video
	if err := db.Select("id", "path", "directory", "size", "duration", "height").
		Where("id IN ?", ids).Order("id ASC").Find(&videos).Error; err != nil {
		return nil, err
	}
	codecs := map[uint]string{}
	if needCodec && len(videos) > 0 {
		var err error
		if codecs, err = automationVideoCodecs(db, ids); err != nil {
			return nil, err
		}
	}
	facts := make([]automationMediaFacts, 0, len(videos))
	for _, video := range videos {
		facts = append(facts, videoAutomationFacts(video, codecs[video.ID]))
	}
	return facts, nil
}

// automationVideoCodecs 取每个视频首个视频流（非封面图、流序号最小）的编码名，与存储体检同一口径。
func automationVideoCodecs(db *gorm.DB, ids []uint) (map[uint]string, error) {
	var streams []struct {
		VideoID     uint
		StreamIndex int
		CodecName   string
	}
	if err := db.Model(&models.MediaStream{}).
		Select("video_id, stream_index, codec_name").
		Where("video_id IN ? AND stream_type = ? AND is_attached_pic = ?", ids, "video", false).
		Order("video_id, stream_index").
		Scan(&streams).Error; err != nil {
		return nil, err
	}
	codecs := make(map[uint]string, len(streams))
	for _, stream := range streams {
		if _, seen := codecs[stream.VideoID]; seen {
			continue
		}
		codecs[stream.VideoID] = strings.ToLower(strings.TrimSpace(stream.CodecName))
	}
	return codecs, nil
}

// automationCandidateIDs 是手动运行与预览的候选：可见范围内（视频走片库默认口径，图片走图片库口径），
// 再叠加能下推到 SQL 的条件。按 id 升序。
func automationCandidateIDs(ctx context.Context, rule *compiledAutomationRule) ([]uint, error) {
	db := database.DB.WithContext(ctx)
	table := "videos"
	var query *gorm.DB
	if rule.MediaKind == AutomationMediaImage {
		table = "images"
		query = applyImageVisibility(db.Model(&models.Image{}), db)
	} else {
		var err error
		if query, err = visibleVideoQuery(db.Model(&models.Video{})); err != nil {
			return nil, err
		}
	}
	for _, condition := range rule.Conditions {
		query = applyAutomationPrefilter(query, table, condition)
	}
	var ids []uint
	if err := query.Order(table+".id ASC").Pluck(table+".id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// applyAutomationPrefilter 把一项条件里能下推的部分写成 SQL；其余条件留给 Go 侧。
func applyAutomationPrefilter(query *gorm.DB, table string, condition AutomationRuleCondition) *gorm.DB {
	switch condition.Type {
	case automationCondUnderDirectory:
		prefix := escapeSQLLikePrefix(scanRootChildPrefix(condition.Path)) + "%"
		return query.Where("("+table+".path = ? OR "+table+".path LIKE ? ESCAPE '\\')", condition.Path, prefix)
	case automationCondSizeBetween:
		return applyAutomationRange(query, table+".size", condition, true)
	case automationCondDuration:
		return applyAutomationRange(query, table+".duration", condition, false)
	case automationCondHeight:
		return applyAutomationRange(query, table+".height", condition, true)
	}
	return query
}

// applyAutomationRange 写闭区间；时长与高度为 0 视为未知，直接排除。整数列的边界取整时向外放宽，只多不漏。
func applyAutomationRange(query *gorm.DB, column string, condition AutomationRuleCondition, integer bool) *gorm.DB {
	if condition.Type != automationCondSizeBetween {
		query = query.Where(column + " > 0")
	}
	if condition.Min != nil && !(integer && *condition.Min >= math.MaxInt64) {
		var min any = *condition.Min
		if integer {
			min = int64(math.Floor(*condition.Min))
		}
		query = query.Where(column+" >= ?", min)
	}
	// 超出 int64 的上限等于没有上限（粗筛只多不漏）。
	if condition.Max != nil && !(integer && *condition.Max >= math.MaxInt64) {
		var max any = *condition.Max
		if integer {
			max = int64(math.Ceil(*condition.Max))
		}
		query = query.Where(column+" <= ?", max)
	}
	return query
}
