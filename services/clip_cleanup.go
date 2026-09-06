package services

import (
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CleanupClipGroup 是一对"完整片 + 从它里面截下来的片段"（D-028）。
//
// 建议保留 Full；Clip 才是可清理的那一份，EstimatedSavings 因此等于 Clip 的体积。
// 前端默认不勾选任何一项：截取片段有可能是用户自己剪出来留着用的素材，
// 算法只负责把它摆到眼前。
type CleanupClipGroup struct {
	Full             models.Video `json:"full"`
	Clip             models.Video `json:"clip"`
	OffsetSeconds    float64      `json:"offset_seconds"`
	MatchRate        float64      `json:"match_rate"`
	EstimatedSavings int64        `json:"estimated_savings"`
}

// clipSequence 是一条参与匹配的有效序列：指纹与磁盘上的文件一致、帧数够、能解码。
type clipSequence struct {
	video      models.Video
	hashes     []uint64
	intervalMS int
	sourceSize int64
	sourceMod  int64
}

// loadCleanupClipGroups 用有效的帧哈希序列找出截取片段候选（D-027、D-028）。
//
// 返回值第二项是"还没有可用序列的视频数"（没回填过的 + 源文件变过失效的），
// 清理面板据此提示可以补全帧哈希。
//
// excluded 里是已经被别的类别认领的视频对（精确重复）与用户忽略过的对，
// 它们不再作为截取候选出现。
func loadCleanupClipGroups(excluded map[[2]uint]struct{}) ([]CleanupClipGroup, int64, error) {
	startedAt := time.Now()
	// 只取用得上的列，且不预载标签：一行序列的 blob 就有 ~30 KB，整库读一遍已经
	// 是这一步的内存峰值，没必要再把标签关联和其余列拖进来。截取候选的保留项由
	// 长度决定，不走 isPreferredCleanupVideo，所以这里不需要 Tags。
	var rows []models.VideoFrameHashSequence
	err := database.DB.
		Select("video_id", "interval_ms", "hashes", "frame_count", "source_size", "source_mod_time_ns", "last_error").
		Preload("Video", func(db *gorm.DB) *gorm.DB {
			return db.Select("id", "name", "path", "directory", "size", "duration", "resolution")
		}).
		Order("video_id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	scope, err := loadCleanupPathScope()
	if err != nil {
		return nil, 0, err
	}
	sequences := make([]clipSequence, 0, len(rows))
	usable := make(map[uint]clipSequence, len(rows))
	for _, row := range rows {
		if row.Video.ID == 0 || !scope.contains(row.Video.Path) {
			continue
		}
		// 与回填的新鲜判定同一套口径（frameHashSequenceMatchesFile）：带 last_error、
		// 没有帧、或采样间隔不是当前值的行都不参与匹配，等回填重算。间隔不同的两条
		// 序列无法逐帧对齐，留着它只会让"待补全"与"能匹配"两个口径互相打架。
		if row.LastError != "" || row.FrameCount <= 0 || row.IntervalMS != clipFrameIntervalMS {
			continue
		}
		info, statErr := os.Stat(row.Video.Path)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		if info.Size() != row.SourceSize || info.ModTime().UnixNano() != row.SourceModTimeNS {
			continue
		}
		hashes := decodeFrameHashes(row.Hashes)
		if len(hashes) == 0 {
			continue
		}
		sequence := clipSequence{
			video:      row.Video,
			hashes:     hashes,
			intervalMS: row.IntervalMS,
			sourceSize: row.SourceSize,
			sourceMod:  row.SourceModTimeNS,
		}
		sequences = append(sequences, sequence)
		usable[row.Video.ID] = sequence
	}
	// 解码之后 blob 已经在 sequences 里有了一份 uint64 拷贝，原始行不再需要：
	// 放掉引用，别在后面的两两比较期间白占一倍内存。
	rows = nil

	staleCount, err := countVideosWithoutUsableFrameHash(scope, usable)
	if err != nil {
		return nil, 0, err
	}
	if len(sequences) < 2 {
		return []CleanupClipGroup{}, staleCount, nil
	}

	dismissed, err := loadClipDismissals()
	if err != nil {
		return nil, 0, err
	}

	// 按帧数从多到少排序：候选对只可能是"前面的当完整片、后面的当截取片段"，
	// 万级视频下这一步把 O(N²) 的比较压到实际需要比的那部分（见下面的二分）。
	sort.Slice(sequences, func(i, j int) bool {
		if len(sequences[i].hashes) != len(sequences[j].hashes) {
			return len(sequences[i].hashes) > len(sequences[j].hashes)
		}
		return sequences[i].video.ID < sequences[j].video.ID
	})

	// 一个截取片段只报一条候选：同一段素材可能同时对上好几个完整片（同一部片的
	// 多个版本），全都报出来会让"可释放空间"把同一个文件算好几遍。留命中率最高的
	// 那一条，命中率相同留更长的完整片（ID 小者优先，结果稳定）。
	best := make(map[uint]CleanupClipGroup)
	comparedPairs := 0
	for full := 0; full < len(sequences); full++ {
		maxClipFrames := int(clipMaxDurationRatio * float64(len(sequences[full].hashes)))
		// 序列按帧数降序，第一个满足 len ≤ 0.9·len(A) 的位置之后才可能是候选片段。
		start := sort.Search(len(sequences), func(index int) bool {
			return len(sequences[index].hashes) <= maxClipFrames
		})
		if start <= full {
			start = full + 1
		}
		for clip := start; clip < len(sequences); clip++ {
			comparedPairs++
			candidate, ok := evaluateClipPair(sequences[full], sequences[clip], excluded, dismissed)
			if !ok {
				continue
			}
			existing, exists := best[sequences[clip].video.ID]
			if !exists || candidate.MatchRate > existing.MatchRate {
				best[sequences[clip].video.ID] = candidate
			}
		}
	}

	groups := make([]CleanupClipGroup, 0, len(best))
	for _, group := range best {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Full.ID != groups[j].Full.ID {
			return groups[i].Full.ID < groups[j].Full.ID
		}
		return groups[i].Clip.ID < groups[j].Clip.ID
	})
	log.Printf("[Cleanup] clip matching sequences=%d pairs_compared=%d groups=%d pending_frame_hash=%d elapsed=%s",
		len(sequences), comparedPairs, len(groups), staleCount, time.Since(startedAt).Round(time.Millisecond))
	return groups, staleCount, nil
}

// evaluateClipPair 判一对是否成候选：排除、间隔一致、逐帧对齐。
func evaluateClipPair(full, clip clipSequence, excluded map[[2]uint]struct{}, dismissed map[[2]uint]models.ClipDismissal) (CleanupClipGroup, bool) {
	if full.video.ID == clip.video.ID {
		return CleanupClipGroup{}, false
	}
	// 采样间隔不同的两条序列无法逐帧对齐，跳过而不是硬比（回填会把旧间隔的行重算）。
	if full.intervalMS != clip.intervalMS || full.intervalMS <= 0 {
		return CleanupClipGroup{}, false
	}
	// 已经被精确重复认领的对不再作为截取出现。
	if _, skip := excluded[cleanupVideoPairKey(full.video.ID, clip.video.ID)]; skip {
		return CleanupClipGroup{}, false
	}
	if clipDismissalStillApplies(dismissed, full, clip) {
		return CleanupClipGroup{}, false
	}
	offset, rate, ok := MatchClip(full.hashes, clip.hashes, full.intervalMS)
	if !ok {
		return CleanupClipGroup{}, false
	}
	return CleanupClipGroup{
		Full:             full.video,
		Clip:             clip.video,
		OffsetSeconds:    float64(offset) * clipFrameIntervalSeconds(full.intervalMS),
		MatchRate:        rate,
		EstimatedSavings: clip.video.Size,
	}, true
}

// clipDismissalStillApplies 报告这一对是否被用户忽略过且双方文件都没变。
// 任一侧重编码就是一份新素材，忽略随之失效（D-028）。
func clipDismissalStillApplies(dismissed map[[2]uint]models.ClipDismissal, full, clip clipSequence) bool {
	record, exists := dismissed[[2]uint{full.video.ID, clip.video.ID}]
	if !exists {
		return false
	}
	return record.FullSourceSize == full.sourceSize && record.FullSourceModTimeNS == full.sourceMod &&
		record.ClipSourceSize == clip.sourceSize && record.ClipSourceModTimeNS == clip.sourceMod
}

// countVideosWithoutUsableFrameHash 统计扫描根之内还没有可用序列的活跃视频数。
//
// 没回填过与失效待重算合成一个数：对用户来说这两种情况的动作完全一样——点一次
// "补全帧哈希"。分开报两个数字只会让面板上多一行没人看的统计。
func countVideosWithoutUsableFrameHash(scope cleanupPathScope, usable map[uint]clipSequence) (int64, error) {
	scoped, err := applyScanRootScope(database.DB.Model(&models.Video{}).Select("id", "path"))
	if err != nil {
		return 0, err
	}
	var videos []models.Video
	if err := scoped.Find(&videos).Error; err != nil {
		return 0, err
	}
	var missing int64
	for _, video := range videos {
		if !scope.contains(video.Path) {
			continue
		}
		if _, ok := usable[video.ID]; !ok {
			missing++
		}
	}
	return missing, nil
}

func loadClipDismissals() (map[[2]uint]models.ClipDismissal, error) {
	var dismissals []models.ClipDismissal
	if err := database.DB.Find(&dismissals).Error; err != nil {
		return nil, err
	}
	byPair := make(map[[2]uint]models.ClipDismissal, len(dismissals))
	for _, dismissal := range dismissals {
		byPair[[2]uint{dismissal.VideoFullID, dismissal.VideoClipID}] = dismissal
	}
	return byPair, nil
}

// DismissClipCandidate 记下"这一对不是截取片段"，后续分析不再报（D-028）。
//
// 连同双方当时的源指纹一起存：任一侧重编码之后这条忽略自动失效，候选重新出现。
// 指纹取自帧哈希序列行——那一行的指纹已经与磁盘文件核对过，比这里再 stat 一次
// 更能保证"忽略的就是刚才看到的那一对"。
func DismissClipCandidate(fullID, clipID uint) error {
	if fullID == 0 || clipID == 0 {
		return errors.New("忽略截取片段需要完整片与片段的视频 ID")
	}
	if fullID == clipID {
		return errors.New("完整片与截取片段不能是同一个视频")
	}
	full, err := loadFrameHashFingerprint(fullID)
	if err != nil {
		return err
	}
	clip, err := loadFrameHashFingerprint(clipID)
	if err != nil {
		return err
	}
	dismissal := models.ClipDismissal{
		VideoFullID:         fullID,
		VideoClipID:         clipID,
		FullSourceSize:      full.SourceSize,
		FullSourceModTimeNS: full.SourceModTimeNS,
		ClipSourceSize:      clip.SourceSize,
		ClipSourceModTimeNS: clip.SourceModTimeNS,
	}
	// 同一对重复忽略要刷新指纹：上一条可能记的是重编码之前的那两个文件。
	return database.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "video_full_id"}, {Name: "video_clip_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"full_source_size", "full_source_mod_time_ns", "clip_source_size", "clip_source_mod_time_ns", "updated_at"}),
	}).Create(&dismissal).Error
}

func loadFrameHashFingerprint(videoID uint) (models.VideoFrameHashSequence, error) {
	var row models.VideoFrameHashSequence
	err := database.DB.Select("video_id", "interval_ms", "frame_count", "source_size", "source_mod_time_ns", "last_error").
		First(&row, "video_id = ?", videoID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.VideoFrameHashSequence{}, fmt.Errorf("视频 %d 还没有帧哈希序列，无法记录忽略", videoID)
	}
	if err != nil {
		return models.VideoFrameHashSequence{}, err
	}
	return row, nil
}

// cleanupExactDuplicatePairs 把精确重复组摊成两两配对，供截取候选排除使用。
//
// 只看精确重复：近似重复与同源是"同一部片的不同版本"，与"A 里截了一段成了 B"
// 是不同的判断，那两类不参与排除（设计 4.6.4 只点了 DuplicateGroups）。
func cleanupExactDuplicatePairs(groups []CleanupDuplicateGroup) map[[2]uint]struct{} {
	pairs := make(map[[2]uint]struct{})
	for _, group := range groups {
		members := append([]models.Video{group.Original}, group.Candidates...)
		for i := 0; i < len(members); i++ {
			for j := i + 1; j < len(members); j++ {
				pairs[cleanupVideoPairKey(members[i].ID, members[j].ID)] = struct{}{}
			}
		}
	}
	return pairs
}
