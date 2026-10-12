package services

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"video-master/database"
	"video-master/models"
)

// StorageReportService 是洞察页「存储体检」页签的只读聚合（2026-10-12）：总占用、按编码的占用、
// 久未观看的大文件与转码可省空间估算。分辨率与目录分布复用洞察页的同名聚合；可回收空间由前端直接读
// 视频与图片清理分析的缓存结果，这里不重复计算。口径与片库、图片库默认视图一致（visibleVideoQuery /
// applyImageVisibility），拔掉移动硬盘后不计。
type StorageReportService struct {
	now func() time.Time
}

func NewStorageReportService() *StorageReportService {
	return &StorageReportService{now: time.Now}
}

const (
	storageDefaultUnwatchedMinBytes = 2 << 30
	storageDefaultUnwatchedMonths   = 12
	storageDefaultListLimit         = 50
	storageMaxListLimit             = 500
	// 转码可省少于这个数的不列：几十 MB 的小片转了也省不出空间。
	storageTranscodeMinSavings = 100 << 20
)

// StorageReportRequest 是体检页的参数；零值取默认（≥2 GiB、12 个月、每个清单 50 项）。
// 两个清单各自给条数，页面上「显示更多」只展开其中一个。
type StorageReportRequest struct {
	UnwatchedMinBytes int64 `json:"unwatched_min_bytes"`
	UnwatchedMonths   int   `json:"unwatched_months"`
	UnwatchedLimit    int   `json:"unwatched_limit"`
	TranscodeLimit    int   `json:"transcode_limit"`
}

type StorageTotals struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
}

// StorageProbeCoverage：Probed 是技术信息探测成功过（video_technical_metadata.probed_at 非空）的可见视频数。
// 没探测成功的不进编码分布（记为「未探测」），也不参与转码估算；探测成功但没有视频流的记为「无视频流」。
type StorageProbeCoverage struct {
	Probed int64 `json:"probed"`
	Total  int64 `json:"total"`
}

type StorageVideoItem struct {
	ID        uint    `json:"id"`
	Title     string  `json:"title"`
	Path      string  `json:"path"`
	Directory string  `json:"directory"`
	Size      int64   `json:"size"`
	Duration  float64 `json:"duration"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	Codec     string  `json:"codec"`
	BitRate   int64   `json:"bit_rate"`
	// LastWatchedAt 是最后一次观看活动，见 storageLastWatched。
	LastWatchedAt  *time.Time `json:"last_watched_at" ts_type:"string"`
	AddedAt        time.Time  `json:"added_at" ts_type:"string"`
	IsFavorite     bool       `json:"is_favorite"`
	PersonalRating *float64   `json:"personal_rating"`
	// 转码清单才有：估算转成 HEVC 后的大小与可省的字节数。
	EstimatedSize    int64 `json:"estimated_size"`
	EstimatedSavings int64 `json:"estimated_savings"`
}

// StorageVideoList 是一个清单：Count / Bytes 是全部命中项的条数与原始大小，Items 只给前 Limit 项。
// Savings 只在转码清单里有值，是全部命中项的估算可省总量。
type StorageVideoList struct {
	Count   int64              `json:"count"`
	Bytes   int64              `json:"bytes"`
	Savings int64              `json:"savings"`
	Items   []StorageVideoItem `json:"items"`
	HasMore bool               `json:"has_more"`
}

// StorageUnwatchedCriteria 是本次实际使用的「久未观看」门槛：大小 ≥ MinBytes，且最后一次观看活动早于
// Cutoff；从没看过的看入库时间早于 Cutoff。
type StorageUnwatchedCriteria struct {
	MinBytes int64     `json:"min_bytes"`
	Months   int       `json:"months"`
	Cutoff   time.Time `json:"cutoff" ts_type:"string"`
}

type StorageReport struct {
	GeneratedAt       time.Time                `json:"generated_at" ts_type:"string"`
	Videos            StorageTotals            `json:"videos"`
	Images            StorageTotals            `json:"images"`
	Probe             StorageProbeCoverage     `json:"probe"`
	ByCodec           []LibraryStatsBucket     `json:"by_codec"`
	ByResolution      []LibraryStatsBucket     `json:"by_resolution"`
	ByDirectory       []LibraryStatsBucket     `json:"by_directory"`
	UnwatchedCriteria StorageUnwatchedCriteria `json:"unwatched_criteria"`
	Unwatched         StorageVideoList         `json:"unwatched"`
	Transcode         StorageVideoList         `json:"transcode"`
}

type storageVideoRow struct {
	ID                     uint
	Name                   string
	DisplayTitle           string
	Path                   string
	Directory              string
	Size                   int64
	Duration               float64
	Width                  int
	Height                 int
	LastPlayedAt           *time.Time
	WatchProgressUpdatedAt *time.Time
	WatchedAt              *time.Time
	CreatedAt              time.Time
	IsFavorite             bool
	PersonalRating         *float64
}

func (s *StorageReportService) GetStorageReport(request StorageReportRequest) (*StorageReport, error) {
	if database.DB == nil {
		return nil, fmt.Errorf("database is not initialized")
	}
	now := s.now()
	criteria := StorageUnwatchedCriteria{MinBytes: request.UnwatchedMinBytes, Months: request.UnwatchedMonths}
	if criteria.MinBytes <= 0 {
		criteria.MinBytes = storageDefaultUnwatchedMinBytes
	}
	if criteria.Months <= 0 {
		criteria.Months = storageDefaultUnwatchedMonths
	}
	criteria.Cutoff = now.AddDate(0, -criteria.Months, 0)

	query, err := visibleVideoQuery(database.DB.Model(&models.Video{}))
	if err != nil {
		return nil, err
	}
	var rows []storageVideoRow
	if err := query.Select("videos.id, videos.name, videos.display_title, videos.path, videos.directory, videos.size, videos.duration, " +
		"videos.width, videos.height, videos.last_played_at, videos.watch_progress_updated_at, videos.watched_at, videos.created_at, " +
		"videos.is_favorite, videos.personal_rating").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	codecs, err := storageMainVideoCodecs()
	if err != nil {
		return nil, err
	}
	probed, err := storageProbedVideoIDs()
	if err != nil {
		return nil, err
	}
	// 久未观看先按视频表上的时间粗筛，再对粗筛出的这批补上播放账本里的观看时间。
	lastWatched := make(map[uint]*time.Time)
	var unwatchedIDs []uint
	for _, row := range rows {
		last := storageLatest(row.LastPlayedAt, row.WatchProgressUpdatedAt, row.WatchedAt)
		lastWatched[row.ID] = last
		if storageIsUnwatched(row, last, criteria) {
			unwatchedIDs = append(unwatchedIDs, row.ID)
		}
	}
	if err := storageMergePlayEvents(unwatchedIDs, lastWatched); err != nil {
		return nil, err
	}

	report := &StorageReport{GeneratedAt: now, UnwatchedCriteria: criteria}
	byCodec := map[string]*LibraryStatsBucket{}
	var unwatched, transcode []StorageVideoItem
	for _, row := range rows {
		report.Videos.Count++
		report.Videos.Bytes += row.Size
		// 有视频流记录的一定探测成功过（老数据可能没写 probed_at）。
		codec := codecs[row.ID]
		label := "未探测"
		if probed[row.ID] || codec != "" {
			report.Probe.Probed++
			label = "无视频流"
		}
		if codec != "" {
			label = storageCodecLabel(codec)
		}
		bucket := byCodec[label]
		if bucket == nil {
			bucket = &LibraryStatsBucket{Label: label}
			byCodec[label] = bucket
		}
		bucket.Count++
		bucket.Bytes += row.Size

		item := storageItem(row, codec, lastWatched[row.ID])
		if storageIsUnwatched(row, lastWatched[row.ID], criteria) {
			report.Unwatched.Count++
			report.Unwatched.Bytes += row.Size
			unwatched = append(unwatched, item)
		}
		if estimate, ok := storageTranscodeEstimate(codec, row.Size, row.Duration, row.Height); ok {
			item.EstimatedSize = estimate
			item.EstimatedSavings = row.Size - estimate
			report.Transcode.Count++
			report.Transcode.Bytes += row.Size
			report.Transcode.Savings += item.EstimatedSavings
			transcode = append(transcode, item)
		}
	}
	report.Probe.Total = report.Videos.Count
	for _, bucket := range byCodec {
		report.ByCodec = append(report.ByCodec, *bucket)
	}
	sort.SliceStable(report.ByCodec, func(i, j int) bool {
		if report.ByCodec[i].Bytes != report.ByCodec[j].Bytes {
			return report.ByCodec[i].Bytes > report.ByCodec[j].Bytes
		}
		return report.ByCodec[i].Label < report.ByCodec[j].Label
	})
	sort.SliceStable(unwatched, func(i, j int) bool {
		if unwatched[i].Size != unwatched[j].Size {
			return unwatched[i].Size > unwatched[j].Size
		}
		return unwatched[i].ID < unwatched[j].ID
	})
	sort.SliceStable(transcode, func(i, j int) bool {
		if transcode[i].EstimatedSavings != transcode[j].EstimatedSavings {
			return transcode[i].EstimatedSavings > transcode[j].EstimatedSavings
		}
		return transcode[i].ID < transcode[j].ID
	})
	report.Unwatched.Items, report.Unwatched.HasMore = storageFirst(unwatched, storageListLimit(request.UnwatchedLimit))
	report.Transcode.Items, report.Transcode.HasMore = storageFirst(transcode, storageListLimit(request.TranscodeLimit))

	if report.ByResolution, err = libraryStorageByResolution(); err != nil {
		return nil, err
	}
	if report.ByDirectory, err = libraryStorageByDirectory(); err != nil {
		return nil, err
	}
	if err := applyImageVisibility(database.DB.Model(&models.Image{}), database.DB).
		Select("COUNT(*) AS count, COALESCE(SUM(size), 0) AS bytes").
		Scan(&report.Images).Error; err != nil {
		return nil, err
	}
	return report, nil
}

// storageMainVideoCodecs 给出每个视频主视频流（非封面图、流序号最小）的编码名。
func storageMainVideoCodecs() (map[uint]string, error) {
	var streams []struct {
		VideoID     uint
		StreamIndex int
		CodecName   string
	}
	if err := database.DB.Model(&models.MediaStream{}).
		Select("video_id, stream_index, codec_name").
		Where("stream_type = ? AND is_attached_pic = ?", "video", false).
		Order("video_id, stream_index").
		Scan(&streams).Error; err != nil {
		return nil, err
	}
	codecs := make(map[uint]string, len(streams))
	for _, stream := range streams {
		if _, seen := codecs[stream.VideoID]; seen {
			continue
		}
		if name := strings.ToLower(strings.TrimSpace(stream.CodecName)); name != "" {
			codecs[stream.VideoID] = name
		}
	}
	return codecs, nil
}

// storageProbedVideoIDs 是技术信息探测成功过的视频。
func storageProbedVideoIDs() (map[uint]bool, error) {
	var ids []uint
	if err := database.DB.Model(&models.VideoTechnicalMetadata{}).Where("probed_at IS NOT NULL").Pluck("video_id", &ids).Error; err != nil {
		return nil, err
	}
	probed := make(map[uint]bool, len(ids))
	for _, id := range ids {
		probed[id] = true
	}
	return probed, nil
}

// storageMergePlayEvents 把播放账本里的观看时间并进 lastWatched：内嵌与 Jellyfin 的有效观看只记在
// play_events 里（D-PC42、PLAY-07），只看视频表会把刚看过的片当成「从未播放」。逐行读而不是 MAX()，
// 因为 SQLite 不按日期类型解析聚合结果；只查粗筛出的候选，分批避免 IN 列表过长。
func storageMergePlayEvents(ids []uint, lastWatched map[uint]*time.Time) error {
	const chunk = 500
	for start := 0; start < len(ids); start += chunk {
		var events []struct {
			VideoID  uint
			PlayedAt time.Time
		}
		batch := ids[start:min(start+chunk, len(ids))]
		if err := database.DB.Model(&models.PlayEvent{}).Select("video_id, played_at").
			Where("video_id IN ?", batch).Scan(&events).Error; err != nil {
			return err
		}
		for _, event := range events {
			playedAt := event.PlayedAt
			lastWatched[event.VideoID] = storageLatest(lastWatched[event.VideoID], &playedAt)
		}
	}
	return nil
}

// storageLatest 返回几个时间里最晚的一个；都为空时返回 nil。
func storageLatest(times ...*time.Time) *time.Time {
	var latest *time.Time
	for _, at := range times {
		if at != nil && !at.IsZero() && (latest == nil || at.After(*latest)) {
			latest = at
		}
	}
	return latest
}

func storageItem(row storageVideoRow, codec string, lastWatched *time.Time) StorageVideoItem {
	title := strings.TrimSpace(row.DisplayTitle)
	if title == "" {
		title = row.Name
	}
	item := StorageVideoItem{
		ID: row.ID, Title: title, Path: row.Path, Directory: row.Directory, Size: row.Size, Duration: row.Duration,
		Width: row.Width, Height: row.Height, LastWatchedAt: lastWatched, AddedAt: row.CreatedAt,
		IsFavorite: row.IsFavorite, PersonalRating: row.PersonalRating,
	}
	if codec != "" {
		item.Codec = storageCodecLabel(codec)
	}
	if row.Duration > 0 && row.Size > 0 {
		item.BitRate = int64(float64(row.Size) * 8 / row.Duration)
	}
	return item
}

// 久未观看：大小达到门槛，且最后一次观看活动早于截止时间；从没看过的按入库时间算。最后一次观看活动取
// 几处记录里最晚的：桌面启动播放（last_played_at）、内嵌与 Jellyfin 的进度（watch_progress_updated_at）、
// 标已看（watched_at）与播放账本（play_events）。
func storageIsUnwatched(row storageVideoRow, lastWatched *time.Time, criteria StorageUnwatchedCriteria) bool {
	if row.Size < criteria.MinBytes {
		return false
	}
	if lastWatched != nil {
		return lastWatched.Before(criteria.Cutoff)
	}
	return !row.CreatedAt.IsZero() && row.CreatedAt.Before(criteria.Cutoff)
}

// 参与转码估算的老编码：HEVC、AV1、VP9 已经够省，ProRes、DNxHD 这类剪辑中间格式不按观看用途估。
var storageLegacyCodecs = map[string]bool{
	"h264": true, "mpeg4": true, "mpeg2video": true, "mpeg1video": true, "msmpeg4v1": true, "msmpeg4v2": true,
	"msmpeg4v3": true, "wmv1": true, "wmv2": true, "wmv3": true, "vc1": true, "h263": true, "flv1": true,
	"rv10": true, "rv20": true, "rv30": true, "rv40": true, "theora": true, "vp8": true, "mjpeg": true,
}

// storageHEVCTargetBitRate 是按画面高度取的 HEVC 常用总码率（bit/s），估算的下限。
func storageHEVCTargetBitRate(height int) float64 {
	switch {
	case height < 720:
		return 1.5e6
	case height < 1080:
		return 3e6
	case height < 1440:
		return 6e6
	case height < 2160:
		return 9e6
	default:
		return 18e6
	}
}

// storageTranscodeEstimate 估算老编码视频转成 HEVC 后的大小：通常能省一半，但不低于该分辨率常用码率
// 乘时长。可省不足 storageTranscodeMinSavings 的、编码或时长分辨率未知的返回 false。只是估算，实际
// 取决于画质设置。
func storageTranscodeEstimate(codec string, size int64, duration float64, height int) (int64, bool) {
	if !storageLegacyCodecs[codec] || size <= 0 || duration <= 0 || height <= 0 {
		return 0, false
	}
	floor := int64(storageHEVCTargetBitRate(height) * duration / 8)
	estimate := max(size/2, floor)
	if size-estimate < storageTranscodeMinSavings {
		return 0, false
	}
	return estimate, true
}

var storageCodecLabels = map[string]string{
	"h264": "H.264", "hevc": "HEVC", "av1": "AV1", "vp9": "VP9", "vp8": "VP8", "mpeg4": "MPEG-4",
	"mpeg2video": "MPEG-2", "mpeg1video": "MPEG-1", "vc1": "VC-1", "wmv3": "WMV 9", "wmv2": "WMV 8",
	"wmv1": "WMV 7", "msmpeg4v3": "MS MPEG-4 v3", "msmpeg4v2": "MS MPEG-4 v2", "msmpeg4v1": "MS MPEG-4 v1",
	"h263": "H.263", "flv1": "Sorenson Spark", "theora": "Theora", "mjpeg": "Motion JPEG", "prores": "ProRes",
	"dnxhd": "DNxHD", "rv40": "RealVideo 4", "rv30": "RealVideo 3", "rv20": "RealVideo 2", "rv10": "RealVideo 1",
}

func storageCodecLabel(codec string) string {
	if label, ok := storageCodecLabels[codec]; ok {
		return label
	}
	return strings.ToUpper(codec)
}

func storageListLimit(limit int) int {
	if limit <= 0 {
		return storageDefaultListLimit
	}
	return min(limit, storageMaxListLimit)
}

func storageFirst(items []StorageVideoItem, limit int) ([]StorageVideoItem, bool) {
	if len(items) <= limit {
		if items == nil {
			items = []StorageVideoItem{}
		}
		return items, false
	}
	return items[:limit], true
}
