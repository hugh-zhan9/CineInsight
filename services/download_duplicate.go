package services

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 下载完自动查重（设计 docs/loopx/design/2026-10-12-color-wall-and-download-dedupe，D-DD-01..05）：
// 浏览器插件下载完成并入库之后，异步和片库比一次。判据沿用清理中心，不另立标准——
// 相同文件：大小一致且三段采样哈希一致（getPartialHash）；其他版本：视频指纹按清理中心同一阈值
// 相近、时长可比，且这一对没有被判过「不是同一部」（仍有效的近似重复忽略）。
// 查重只读片库，新片的视频指纹在这里顺带算出并写入（与补全任务写同一张表）。
const (
	DownloadDuplicateFound   = "found"
	DownloadDuplicateNone    = "none"
	DownloadDuplicateFailed  = "failed"
	DownloadDuplicateSkipped = "skipped"

	DownloadDuplicateExact   = "exact"
	DownloadDuplicateVersion = "version"

	// 与对方比：对方像素多 / 少 5% 以上为更清楚 / 更模糊；像素相近时按文件大小。
	DownloadRelationSame    = "same"
	DownloadRelationHigher  = "higher"
	DownloadRelationLower   = "lower"
	DownloadRelationLarger  = "larger"
	DownloadRelationSmaller = "smaller"
	DownloadRelationSimilar = "similar"

	downloadDuplicateCheckTimeout = 2 * time.Minute
)

// DownloadDuplicateResult 是落进 browser_download_tasks.duplicate_result 的查重结论（D-DD-03）：
// 只存结论与对方视频 ID，标题、分辨率、大小读取时按 ID 现查。
// VideoID / SourceSize / SourceModTimeNS 记下「查的是哪一份」：读取时这次下载的片库记录或文件
// 已经不是它（同名路径后来换成了别的文件、被删了、重新入库换了记录），这条结论就作废，
// 不能拿去删别的文件或写忽略（2026-10-12 审查 I-2）。
type DownloadDuplicateResult struct {
	Status          string                   `json:"status"`
	CheckedAt       time.Time                `json:"checked_at"`
	Matches         []DownloadDuplicateMatch `json:"matches"`
	UnhashedVideos  int64                    `json:"unhashed_videos"`
	Error           string                   `json:"error"`
	VideoID         uint                     `json:"video_id"`
	SourceSize      int64                    `json:"source_size"`
	SourceModTimeNS int64                    `json:"source_mod_time_ns"`
	// VersionError：「其他版本」那一步没做完（例如纯音轨文件算不出视频指纹），相同文件的结论照样保留。
	VersionError string `json:"version_error"`
}

// downloadDuplicateStageError 标出查重失败在哪一步。界面与日志只用 Stage：底层错误可能带着
// 本地路径（os 与 ffmpeg 的报错），不往外说。
type downloadDuplicateStageError struct {
	Stage string
	Err   error
}

func (e *downloadDuplicateStageError) Error() string { return e.Stage + ": " + e.Err.Error() }
func (e *downloadDuplicateStageError) Unwrap() error { return e.Err }

// downloadDuplicateErrorText 是能给用户看、能进日志的失败说明（不带路径）。
func downloadDuplicateErrorText(err error) string {
	var stage *downloadDuplicateStageError
	if errors.As(err, &stage) {
		return stage.Stage + "失败"
	}
	return "查重时出错"
}

type DownloadDuplicateMatch struct {
	VideoID  uint   `json:"video_id"`
	Kind     string `json:"kind"`
	Relation string `json:"relation"`
}

// DownloadDuplicateView 是下载任务里给界面的查重结果：匹配项带上对方此刻的标题、分辨率与大小，
// 对方已删除或不再可见的不返回。
type DownloadDuplicateView struct {
	Status         string                       `json:"status"`
	Matches        []DownloadDuplicateMatchView `json:"matches"`
	UnhashedVideos int64                        `json:"unhashed_videos"`
	Error          string                       `json:"error"`
	VersionError   string                       `json:"version_error"`
}

type DownloadDuplicateMatchView struct {
	VideoID  uint   `json:"video_id"`
	Kind     string `json:"kind"`
	Relation string `json:"relation"`
	Title    string `json:"title"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Size     int64  `json:"size"`
}

// downloadPerceptualHasher 是查重要用的那一个指纹能力：算出并写入一部视频的视频指纹
// （PerceptualHashService.Refresh）。单测注入替身，免得真跑 ffmpeg。
type downloadPerceptualHasher interface {
	Refresh(ctx context.Context, video models.Video) error
}

// DownloadDuplicateChecker 做一次查重。PerceptualHash 为空时只做精确比对。
type DownloadDuplicateChecker struct {
	PerceptualHash downloadPerceptualHasher
}

// Check 比对 outputPath（这次下载的文件）与片库。videoID 是它入库后的记录，没入库时为 0，
// 此时只做精确比对（D-DD-01）。
func (c *DownloadDuplicateChecker) Check(ctx context.Context, outputPath string, videoID uint) (DownloadDuplicateResult, error) {
	result := DownloadDuplicateResult{Status: DownloadDuplicateNone, CheckedAt: time.Now(), Matches: []DownloadDuplicateMatch{}, VideoID: videoID}
	info, err := os.Stat(outputPath)
	if err != nil || !info.Mode().IsRegular() {
		result.Status = DownloadDuplicateSkipped
		return result, nil
	}
	result.SourceSize, result.SourceModTimeNS = info.Size(), info.ModTime().UnixNano()
	exact, err := downloadExactDuplicates(ctx, outputPath, info.Size(), videoID)
	if err != nil {
		return result, &downloadDuplicateStageError{Stage: "比对相同文件", Err: err}
	}
	result.Matches = append(result.Matches, exact...)
	if videoID != 0 && c.PerceptualHash != nil {
		versions, unhashed, err := c.versionDuplicates(ctx, videoID, exact)
		switch {
		case err != nil && errors.Is(ctx.Err(), context.Canceled):
			// 整个查重被取消（应用退出）：不留结论。
			return result, err
		case err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
			// 超时只算「其他版本」没做完，已经找到的相同文件照样报告（审查复核 Minor 2）。
			result.VersionError = "比对其他版本超时"
		case err != nil:
			// 「其他版本」没做完不连累已经找到的相同文件（2026-10-12 审查 I-4）。
			result.VersionError = downloadDuplicateErrorText(err)
		default:
			result.Matches = append(result.Matches, versions...)
			result.UnhashedVideos = unhashed
		}
	}
	if len(result.Matches) > 0 {
		result.Status = DownloadDuplicateFound
	}
	return result, nil
}

// downloadExactDuplicates 在可见视频里找同大小的候选，再比三段采样哈希（清理中心精确重复的判据）。
// 候选文件读不到（盘没接）的跳过，不算失败。
func downloadExactDuplicates(ctx context.Context, path string, size int64, selfID uint) ([]DownloadDuplicateMatch, error) {
	query, err := visibleVideoQuery(database.DB.WithContext(ctx).Model(&models.Video{}))
	if err != nil {
		return nil, err
	}
	var candidates []models.Video
	if err := query.Select("videos.id", "videos.path").
		Where("videos.size = ? AND videos.id <> ? AND videos.path <> ?", size, selfID, path).
		Find(&candidates).Error; err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	hash, err := getPartialHash(path)
	if err != nil {
		return nil, err
	}
	matches := []DownloadDuplicateMatch{}
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		other, err := getPartialHash(candidate.Path)
		if err != nil || other != hash {
			continue
		}
		matches = append(matches, DownloadDuplicateMatch{VideoID: candidate.ID, Kind: DownloadDuplicateExact, Relation: DownloadRelationSame})
	}
	return matches, nil
}

// versionDuplicates 算出新片的视频指纹，与片库里已有指纹、时长可比的可见视频比对。
// 返回匹配与「片库里还没有可用指纹的可见视频数」（这些没参与比对，界面要说明，D-DD-02）。
func (c *DownloadDuplicateChecker) versionDuplicates(ctx context.Context, videoID uint, exact []DownloadDuplicateMatch) ([]DownloadDuplicateMatch, int64, error) {
	var self models.Video
	if err := database.DB.WithContext(ctx).First(&self, videoID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	if err := c.PerceptualHash.Refresh(ctx, self); err != nil {
		return nil, 0, &downloadDuplicateStageError{Stage: "计算视频指纹", Err: err}
	}
	var selfHash models.VideoPerceptualHash
	if err := database.DB.WithContext(ctx).First(&selfHash, "video_id = ?", videoID).Error; err != nil {
		return nil, 0, &downloadDuplicateStageError{Stage: "计算视频指纹", Err: err}
	}
	if !perceptualHashRowComplete(selfHash) {
		return nil, 0, &downloadDuplicateStageError{Stage: "计算视频指纹", Err: errors.New("指纹不完整")}
	}
	query, err := visibleVideoQuery(database.DB.WithContext(ctx).Model(&models.Video{}))
	if err != nil {
		return nil, 0, &downloadDuplicateStageError{Stage: "查询片库", Err: err}
	}
	// 只读时长可比的那一小段（与 perceptualDurationsComparable 同一界限再放宽一点），不把全库指纹都读进来。
	low, high := downloadDurationWindow(self.Duration)
	var rows []struct {
		models.VideoPerceptualHash
		Duration float64
		Width    int
		Height   int
		Size     int64
		Path     string
	}
	// 先在库里粗筛：指纹记录的大小与视频记录一致（不逐个读文件，外置盘上几万个文件太慢）；
	// 真正命中的几个再按文件此刻的大小与修改时间核对（清理中心的口径），对不上的旧指纹不算。
	if err := query.Select("video_perceptual_hashes.*, videos.duration, videos.width, videos.height, videos.size, videos.path").
		Joins("JOIN video_perceptual_hashes ON video_perceptual_hashes.video_id = videos.id").
		Where("videos.id <> ? AND video_perceptual_hashes.source_size = videos.size AND videos.duration BETWEEN ? AND ?", videoID, low, high).
		Scan(&rows).Error; err != nil {
		return nil, 0, &downloadDuplicateStageError{Stage: "查询片库", Err: err}
	}
	unhashed, err := downloadUnhashedVisibleVideos(ctx, videoID)
	if err != nil {
		return nil, 0, &downloadDuplicateStageError{Stage: "查询片库", Err: err}
	}
	already := make(map[uint]bool, len(exact))
	for _, match := range exact {
		already[match.VideoID] = true
	}
	var hits []uint
	for _, row := range rows {
		if !perceptualHashRowComplete(row.VideoPerceptualHash) {
			continue
		}
		if already[row.VideoID] || !perceptualDurationsComparable(self.Duration, row.Duration) || !perceptualRowsMatch(selfHash, row.VideoPerceptualHash) {
			continue
		}
		info, err := os.Stat(row.Path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != row.SourceSize || info.ModTime().UnixNano() != row.SourceModTimeNS {
			continue
		}
		hits = append(hits, row.VideoID)
	}
	if len(hits) == 0 {
		return nil, unhashed, nil
	}
	dismissed, err := downloadDismissedPairs(ctx, videoID, hits)
	if err != nil {
		return nil, 0, &downloadDuplicateStageError{Stage: "查询片库", Err: err}
	}
	byID := make(map[uint]int, len(rows))
	for index, row := range rows {
		byID[row.VideoID] = index
	}
	matches := []DownloadDuplicateMatch{}
	for _, id := range hits {
		if _, skip := dismissed[cleanupVideoPairKey(videoID, id)]; skip {
			continue
		}
		row := rows[byID[id]]
		matches = append(matches, DownloadDuplicateMatch{
			VideoID: id, Kind: DownloadDuplicateVersion,
			Relation: downloadRelation(self.Width, self.Height, self.Size, row.Width, row.Height, row.Size),
		})
	}
	return matches, unhashed, nil
}

// downloadDurationWindow 是时长可比（perceptualDurationsComparable：相差不超过 max(3 秒, 较长者的 2%)）
// 的一个外包区间，供 SQL 预筛；精确判定仍由 perceptualDurationsComparable 做。
func downloadDurationWindow(duration float64) (float64, float64) {
	low := math.Min(duration-3, duration*0.98)
	high := math.Max(duration+3, duration/0.98)
	return math.Max(0, low-1), high + 1
}

// downloadUnhashedVisibleVideos 数片库里（除这次下载外）还没有可用指纹的可见视频：没有指纹行、
// 指纹不完整或指纹记录的大小与视频记录对不上的。
func downloadUnhashedVisibleVideos(ctx context.Context, selfID uint) (int64, error) {
	query, err := visibleVideoQuery(database.DB.WithContext(ctx).Model(&models.Video{}))
	if err != nil {
		return 0, err
	}
	var count int64
	err = query.Where("videos.id <> ?", selfID).
		Where(`NOT EXISTS (SELECT 1 FROM video_perceptual_hashes h WHERE h.video_id = videos.id AND h.source_size = videos.size
			AND h.hash_early <> '' AND h.hash_middle <> '' AND h.hash_late <> '')`).
		Count(&count).Error
	return count, err
}

// downloadDismissedPairs 返回仍然有效的「不是同一部」忽略（与清理中心的近似重复忽略同一张表）。
func downloadDismissedPairs(ctx context.Context, selfID uint, others []uint) (map[[2]uint]struct{}, error) {
	ids := append([]uint{selfID}, others...)
	var videos []models.Video
	if err := database.DB.WithContext(ctx).Select("id", "path").Where("id IN ?", ids).Find(&videos).Error; err != nil {
		return nil, err
	}
	current := make(map[uint]string, len(videos))
	for _, video := range videos {
		if fingerprint, err := statCleanupFileFingerprint(video.Path); err == nil {
			current[video.ID] = fingerprint
		}
	}
	return loadActiveNearDuplicateDismissals(current)
}

// downloadRelation 说明「对方」相对这次下载：像素多 / 少 5% 以上为更清楚 / 更模糊，否则按大小。
func downloadRelation(width, height int, size int64, otherWidth, otherHeight int, otherSize int64) string {
	pixels, otherPixels := float64(width*height), float64(otherWidth*otherHeight)
	if pixels > 0 && otherPixels > 0 {
		switch {
		case otherPixels >= pixels*1.05:
			return DownloadRelationHigher
		case otherPixels <= pixels*0.95:
			return DownloadRelationLower
		}
	}
	if size > 0 && otherSize > 0 {
		switch {
		case float64(otherSize) >= float64(size)*1.05:
			return DownloadRelationLarger
		case float64(otherSize) <= float64(size)*0.95:
			return DownloadRelationSmaller
		}
	}
	return DownloadRelationSimilar
}

// encodeDownloadDuplicateResult / decodeDownloadDuplicateResult 是落库列的编解码；空串表示还没查。
func encodeDownloadDuplicateResult(result DownloadDuplicateResult) string {
	data, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	return string(data)
}

func decodeDownloadDuplicateResult(raw string) (DownloadDuplicateResult, bool) {
	if raw == "" {
		return DownloadDuplicateResult{}, false
	}
	var result DownloadDuplicateResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return DownloadDuplicateResult{}, false
	}
	return result, true
}

// downloadDuplicateStillApplies 核对一条结论查的还是不是这次下载现在的那一份：片库记录一致、
// 文件还在且大小与修改时间没变（2026-10-12 审查 I-2）。没记下文件指纹的（skipped / 早期结论）只核记录。
func downloadDuplicateStillApplies(result DownloadDuplicateResult, task BrowserDownloadTask) bool {
	if result.VideoID != task.VideoID {
		return false
	}
	if result.SourceSize == 0 && result.SourceModTimeNS == 0 {
		return true
	}
	info, err := os.Stat(task.OutputPath)
	return err == nil && info.Mode().IsRegular() && info.Size() == result.SourceSize && info.ModTime().UnixNano() == result.SourceModTimeNS
}

// resolveDownloadDuplicateViews 把一批任务的查重结论换成界面用的视图：按 ID 一次查出对方视频，
// 对方已删除或不在片库可见范围内的匹配丢掉；这次下载自己的记录或文件已经变了（结论作废）、
// 或自己的片库记录已经不可见（移到废纸篓之后）的，整条提示不再显示。
func resolveDownloadDuplicateViews(db *gorm.DB, tasks []BrowserDownloadTask) {
	ids := []uint{}
	for _, task := range tasks {
		if result, ok := decodeDownloadDuplicateResult(task.duplicateResult); ok {
			for _, match := range result.Matches {
				ids = append(ids, match.VideoID)
			}
			if task.VideoID != 0 {
				ids = append(ids, task.VideoID)
			}
		}
	}
	videos := map[uint]models.Video{}
	if len(ids) > 0 && db != nil {
		query, err := visibleVideoQuery(db.Model(&models.Video{}))
		if err == nil {
			var rows []models.Video
			if err := query.Select("videos.id", "videos.name", "videos.display_title", "videos.width", "videos.height", "videos.size").
				Where("videos.id IN ?", uniqueUintIDs(ids)).Find(&rows).Error; err == nil {
				for _, row := range rows {
					videos[row.ID] = row
				}
			}
		}
	}
	for index := range tasks {
		result, ok := decodeDownloadDuplicateResult(tasks[index].duplicateResult)
		if !ok || !downloadDuplicateStillApplies(result, tasks[index]) {
			tasks[index].Duplicate = nil
			continue
		}
		if self := tasks[index].VideoID; self != 0 {
			if _, visible := videos[self]; !visible {
				tasks[index].Duplicate = nil
				continue
			}
		}
		view := &DownloadDuplicateView{Status: result.Status, UnhashedVideos: result.UnhashedVideos, Error: result.Error,
			VersionError: result.VersionError, Matches: []DownloadDuplicateMatchView{}}
		for _, match := range result.Matches {
			video, found := videos[match.VideoID]
			if !found {
				continue
			}
			title := video.DisplayTitle
			if title == "" {
				title = video.Name
			}
			view.Matches = append(view.Matches, DownloadDuplicateMatchView{
				VideoID: match.VideoID, Kind: match.Kind, Relation: match.Relation,
				Title: title, Width: video.Width, Height: video.Height, Size: video.Size,
			})
		}
		// 结论是「找到」但对方都已不在：对界面就是没有重复了。
		if view.Status == DownloadDuplicateFound && len(view.Matches) == 0 {
			view.Status = DownloadDuplicateNone
		}
		tasks[index].Duplicate = view
	}
}
