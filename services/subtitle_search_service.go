package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SubtitleSearchMatch struct {
	Video   models.Video           `json:"video"`
	Segment subtitleparser.Segment `json:"segment"`
}

type SubtitleSearchFilters struct {
	TagIDs    []uint
	MinSize   int64
	MaxSize   int64
	MinHeight int
	MaxHeight int
	Limit     int
}

type SubtitleSearchService struct{}

func uniqueUintIDs(values []uint) []uint {
	seen := make(map[uint]struct{}, len(values))
	result := make([]uint, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s *SubtitleSearchService) SearchSubtitleMatches(keyword string, limit int) ([]SubtitleSearchMatch, error) {
	return s.SearchSubtitleMatchesWithFilters(keyword, SubtitleSearchFilters{Limit: limit})
}

// SearchSubtitleMatchesWithFilters 先查现有索引（D-PC23）：命中视频的字幕文件改过就就地重建这几条
// 再查一次；结果不满一页时说明磁盘上可能还有没进索引的字幕，按节流在后台做全库同步，
// 这一次直接返回当前结果，同步完成后前端收到 subtitle-index-synced 再刷新。
func (s *SubtitleSearchService) SearchSubtitleMatchesWithFilters(keyword string, filters SubtitleSearchFilters) ([]SubtitleSearchMatch, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return []SubtitleSearchMatch{}, nil
	}
	if filters.Limit <= 0 {
		filters.Limit = 20
	}

	matches, stale, err := s.searchIndexedSubtitleMatches(keyword, filters.Limit, filters)
	if err != nil {
		return nil, err
	}
	if stale {
		matches, _, err = s.searchIndexedSubtitleMatches(keyword, filters.Limit, filters)
		if err != nil {
			return nil, err
		}
	}
	if len(matches) < filters.Limit {
		if err := syncSubtitleIndexesFromFilesystem(); err != nil {
			return nil, err
		}
	}
	return matches, nil
}

// GetSubtitleIndexSyncStatus 返回全库字幕索引同步的状态：是否在跑、上次完成时间（视图上的「上次同步时间」）。
func (s *SubtitleSearchService) GetSubtitleIndexSyncStatus() SubtitleIndexSyncStatus {
	return subtitleIndexSync.status(database.DB)
}

// SyncSubtitleIndexNow 是「立即同步」：不看节流间隔，在后台开始一轮全库同步（已有一轮在跑时沿用它），
// 立即返回；完成后发 subtitle-index-synced。
func (s *SubtitleSearchService) SyncSubtitleIndexNow() (SubtitleIndexSyncStatus, error) {
	db := database.DB
	if db == nil {
		return SubtitleIndexSyncStatus{}, errors.New("数据库未初始化")
	}
	subtitleIndexSync.request(db, true)
	return subtitleIndexSync.status(db), nil
}

func (s *SubtitleSearchService) searchIndexedSubtitleMatches(keyword string, limit int, filters SubtitleSearchFilters) ([]SubtitleSearchMatch, bool, error) {
	pattern := "%" + strings.ToLower(escapeSQLLike(keyword)) + "%"
	type firstHit struct {
		VideoID      uint
		SegmentIndex int
	}

	var hits []firstHit
	query := database.DB.Model(&models.SubtitleSegment{}).
		Select("subtitle_segments.video_id, MIN(subtitle_segments.segment_index) AS segment_index").
		Joins("JOIN videos ON videos.id = subtitle_segments.video_id AND videos.deleted_at IS NULL").
		Where("LOWER(text) LIKE ? ESCAPE '\\'", pattern).
		Group("subtitle_segments.video_id")

	if filters.MinSize > 0 {
		query = query.Where("videos.size >= ?", filters.MinSize)
	}
	if filters.MaxSize > 0 {
		query = query.Where("videos.size < ?", filters.MaxSize)
	}
	if filters.MinHeight > 0 {
		query = query.Where("videos.height >= ?", filters.MinHeight)
	}
	if filters.MaxHeight > 0 {
		query = query.Where("videos.height <= ?", filters.MaxHeight)
	}
	if tagIDs := uniqueUintIDs(filters.TagIDs); len(tagIDs) > 0 {
		query = query.Joins("JOIN video_tags ON video_tags.video_id = subtitle_segments.video_id").
			Where("video_tags.tag_id IN ?", tagIDs).
			Group("subtitle_segments.video_id").
			Having("COUNT(DISTINCT video_tags.tag_id) = ?", len(tagIDs))
	}

	err := query.
		Order("subtitle_segments.video_id desc").
		Limit(limit).
		Scan(&hits).Error
	if err != nil {
		return nil, false, err
	}
	if len(hits) == 0 {
		return []SubtitleSearchMatch{}, false, nil
	}

	videoIDs := make([]uint, 0, len(hits))
	for _, hit := range hits {
		videoIDs = append(videoIDs, hit.VideoID)
	}

	var videos []models.Video
	if err := database.DB.Preload("Tags").Where("id IN ?", videoIDs).Find(&videos).Error; err != nil {
		return nil, false, err
	}
	videosByID := make(map[uint]models.Video, len(videos))
	for _, video := range videos {
		videosByID[video.ID] = video
	}

	matches := make([]SubtitleSearchMatch, 0, len(hits))
	staleIndex := false
	for _, hit := range hits {
		video, ok := videosByID[hit.VideoID]
		if !ok {
			staleIndex = true
			_ = deleteSubtitleIndex(hit.VideoID)
			continue
		}
		current, err := isSubtitleIndexCurrent(video, subtitleparser.SRTPathForVideo(video.Path))
		if err != nil || !current {
			staleIndex = true
			_ = ensureSubtitleIndexForVideo(video)
			continue
		}

		var indexed models.SubtitleSegment
		if err := database.DB.
			Where("video_id = ? AND segment_index = ?", hit.VideoID, hit.SegmentIndex).
			First(&indexed).Error; err != nil {
			staleIndex = true
			continue
		}
		matches = append(matches, SubtitleSearchMatch{
			Video: video,
			Segment: subtitleparser.Segment{
				Index:       indexed.SegmentIndex,
				StartTimeMs: indexed.StartTimeMs,
				EndTimeMs:   indexed.EndTimeMs,
				Text:        indexed.Text,
				Lines:       splitSubtitleLines(indexed.Text),
			},
		})
	}

	return matches, staleIndex, nil
}

// syncSubtitleIndexesFromFilesystem 是「无字幕」视图与字幕关键词搜索的前置同步（D-PC23，设计里叫
// syncSubtitleIndexForViews）；复用同一筛选的随机播放、代理入队、NFO 导出也经过这里。
//
// 它不再在调用方的线程里对全库做 stat：距上次完成不足 10 分钟直接返回（调用方查到的就是当前索引），
// 否则在后台开一轮同步并立即返回；那一轮完成后发 subtitle-index-synced，前端据此刷新。
func syncSubtitleIndexesFromFilesystem() error {
	db := database.DB
	if db == nil {
		return errors.New("数据库未初始化")
	}
	subtitleIndexSync.request(db, false)
	return nil
}

const (
	// subtitleIndexSyncInterval 是两轮后台全库同步之间的最短间隔（D-PC23）。「立即同步」不受它限制。
	subtitleIndexSyncInterval = 10 * time.Minute
	subtitleIndexSyncedEvent  = "subtitle-index-synced"
)

// SubtitleIndexSyncStatus 是全库字幕索引同步的状态，也是 subtitle-index-synced 事件的载荷。
type SubtitleIndexSyncStatus struct {
	Running bool `json:"running"`
	// LastSyncedAt 是当前数据库上一轮同步完成的时间；本次运行还没有完成过同步时为空。
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty" ts_type:"string"`
	// Checked 是上一轮检查过的视频数。
	Checked int `json:"checked"`
	// Error 是上一轮列出视频时的错误（单条视频的失败不计入，与原来的同步口径一致）。
	Error string `json:"error,omitempty"`
}

type subtitleIndexSyncRun struct {
	db     *gorm.DB
	cancel context.CancelFunc
	done   chan struct{}
	// gate 是开这一轮时的 startGate：非空时等它关闭（或本轮被取消）才开始。
	gate <-chan struct{}
}

// subtitleIndexSyncer 持有后台同步的节流状态。状态跟着 *gorm.DB 走：换库（恢复备份、测试换夹具）
// 之后上一轮的完成时间不再有意义，旧库上还在跑的那一轮被取消，只写它自己捕获的那个连接。
type subtitleIndexSyncer struct {
	mu       sync.Mutex
	db       *gorm.DB
	lastDone time.Time
	checked  int
	lastErr  string
	run      *subtitleIndexSyncRun
	emit     func(SubtitleIndexSyncStatus)
	now      func() time.Time
	// startGate 仅供测试，生产中恒为 nil：非空时之后新开的一轮停在开始之前，直到它被关闭。
	// 视图查询是「先发起后台同步、再读索引」，不拦住的话一轮很快的同步可能赶在首屏读索引之前
	// 跑完，「首屏返回缓存」就无法确定地观察。
	startGate chan struct{}
}

var subtitleIndexSync = &subtitleIndexSyncer{now: time.Now}

// setSubtitleIndexSyncEmitter 设置 subtitle-index-synced 事件的出口（SubtitleService.SetContext 里接上）。
func setSubtitleIndexSyncEmitter(emit func(SubtitleIndexSyncStatus)) {
	subtitleIndexSync.mu.Lock()
	subtitleIndexSync.emit = emit
	subtitleIndexSync.mu.Unlock()
}

// request 按需开一轮后台同步。已有一轮在跑时返回那一轮（started=false）；未到节流间隔且不是 force 时
// 返回 nil。
func (s *subtitleIndexSyncer) request(db *gorm.DB, force bool) (run *subtitleIndexSyncRun, started bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.switchDBLocked(db)
	if s.run != nil {
		return s.run, false
	}
	if !force && !s.lastDone.IsZero() && s.now().Sub(s.lastDone) < subtitleIndexSyncInterval {
		return nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	run = &subtitleIndexSyncRun{db: db, cancel: cancel, done: make(chan struct{}), gate: s.startGate}
	s.run = run
	go s.execute(ctx, run)
	return run, true
}

func (s *subtitleIndexSyncer) switchDBLocked(db *gorm.DB) {
	if s.db == db {
		return
	}
	if s.run != nil {
		s.run.cancel()
		s.run = nil
	}
	s.db, s.lastDone, s.checked, s.lastErr = db, time.Time{}, 0, ""
}

func (s *subtitleIndexSyncer) execute(ctx context.Context, run *subtitleIndexSyncRun) {
	defer run.cancel()
	if run.gate != nil {
		select {
		case <-run.gate:
		case <-ctx.Done():
		}
	}
	checked, err := syncSubtitleIndexesNow(ctx, run.db)
	cancelled := ctx.Err() != nil

	s.mu.Lock()
	current := s.run == run
	var status SubtitleIndexSyncStatus
	var emit func(SubtitleIndexSyncStatus)
	if current {
		s.run = nil
		// 被取消的一轮（换库、恢复备份前的停机）不算完成，不刷新节流时间。
		// 失败的一轮（例如维护围栏期间第一条查询就被拒绝）也不算完成：只记下原因，
		// 「上次同步时间」与检查条数保持上一次成功的值，下一次请求不被节流挡住（M-6）。
		if !cancelled {
			if err != nil {
				s.lastErr = err.Error()
			} else {
				s.lastDone, s.checked = s.now(), checked
				s.lastErr = ""
			}
			status, emit = s.statusLocked(), s.emit
		}
	}
	s.mu.Unlock()
	close(run.done)
	if emit != nil {
		emit(status)
	}
}

func (s *subtitleIndexSyncer) status(db *gorm.DB) SubtitleIndexSyncStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != db {
		return SubtitleIndexSyncStatus{}
	}
	return s.statusLocked()
}

func (s *subtitleIndexSyncer) statusLocked() SubtitleIndexSyncStatus {
	status := SubtitleIndexSyncStatus{Running: s.run != nil, Checked: s.checked, Error: s.lastErr}
	if !s.lastDone.IsZero() {
		last := s.lastDone
		status.LastSyncedAt = &last
	}
	return status
}

// stopSubtitleIndexSyncAndWait 取消正在跑的后台同步并等它退出（恢复备份关库之前调用）。
func stopSubtitleIndexSyncAndWait() {
	subtitleIndexSync.mu.Lock()
	run := subtitleIndexSync.run
	subtitleIndexSync.mu.Unlock()
	if run == nil {
		return
	}
	run.cancel()
	<-run.done
}

// syncSubtitleIndexesNow 对 db 上的全部视频逐条核对字幕索引（含旁挂字幕标记），返回检查过的条数。
// 单条视频的失败忽略，与原来的同步口径一致；ctx 取消时在下一条之前停下。维护围栏的拒绝不是
// 单条视频的问题，整轮按失败结束。
func syncSubtitleIndexesNow(ctx context.Context, db *gorm.DB) (int, error) {
	if db == nil {
		return 0, errors.New("数据库未初始化")
	}
	var videos []models.Video
	if err := db.WithContext(ctx).Select("id", "path").Order("id desc").Find(&videos).Error; err != nil {
		return 0, err
	}
	sidecars := newSubtitleSidecarDirCache()
	checked := 0
	for _, video := range videos {
		if err := ctx.Err(); err != nil {
			return checked, err
		}
		if err := ensureSubtitleIndexForVideoOn(db, video, sidecars); errors.Is(err, database.ErrMaintenance) {
			return checked, err
		}
		checked++
	}
	return checked, nil
}

func ensureSubtitleIndexForVideo(video models.Video) error {
	return ensureSubtitleIndexForVideoOn(database.DB, video, nil)
}

// readSubtitleSidecarDir 是全库同步读视频目录的入口，测试用它数读目录的次数。
var readSubtitleSidecarDir = os.ReadDir

// subtitleSidecarDirCache 是一轮全库同步内按目录缓存的目录项（M-6）：同一目录下有上千个视频时，
// 逐条调用 HasSidecarSubtitle 等于把整个目录读上千遍（视频数 × 目录项数）。
//
// 判定规则仍只有 subtitle_sidecar.go 那一份：文件名经 IsSidecarSubtitleName 判定，与
// ListSidecarSubtitles 一样排除目录、只认（跟随符号链接后的）普通文件；这里只负责不重复读目录。
type subtitleSidecarDirCache struct {
	dirs map[string]*subtitleSidecarDirListing
}

type subtitleSidecarDirListing struct {
	// names 与 lower 一一对应，按小写名排序，用前缀二分找到候选。
	names []string
	lower []string
	err   error
}

func newSubtitleSidecarDirCache() *subtitleSidecarDirCache {
	return &subtitleSidecarDirCache{dirs: map[string]*subtitleSidecarDirListing{}}
}

func (c *subtitleSidecarDirCache) listing(directory string) *subtitleSidecarDirListing {
	if cached, ok := c.dirs[directory]; ok {
		return cached
	}
	listing := &subtitleSidecarDirListing{}
	entries, err := readSubtitleSidecarDir(directory)
	switch {
	case os.IsNotExist(err):
		// 目录不存在按「没有」处理，与 ListSidecarSubtitles 一致。
	case err != nil:
		listing.err = fmt.Errorf("读取视频目录失败: %s", subtitleIOReason(err))
	default:
		type entryName struct{ name, lower string }
		kept := make([]entryName, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			kept = append(kept, entryName{name: entry.Name(), lower: strings.ToLower(entry.Name())})
		}
		sort.Slice(kept, func(i, j int) bool { return kept[i].lower < kept[j].lower })
		listing.names = make([]string, len(kept))
		listing.lower = make([]string, len(kept))
		for index, entry := range kept {
			listing.names[index], listing.lower[index] = entry.name, entry.lower
		}
	}
	c.dirs[directory] = listing
	return listing
}

// hasSidecar 与 HasSidecarSubtitle(videoPath) 同义，目录只在这一轮里读一次。
func (c *subtitleSidecarDirCache) hasSidecar(videoPath string) (bool, error) {
	directory := filepath.Dir(videoPath)
	listing := c.listing(directory)
	if listing.err != nil {
		return false, listing.err
	}
	// 旁挂字幕的名字一定以「视频基本名 + .」开头（大小写不敏感）：只看这一段前缀的候选，
	// 最终是否算数仍由 IsSidecarSubtitleName 判定。
	base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	if base == "" {
		return false, nil
	}
	prefix := strings.ToLower(base) + "."
	for index := sort.SearchStrings(listing.lower, prefix); index < len(listing.lower) && strings.HasPrefix(listing.lower[index], prefix); index++ {
		name := listing.names[index]
		if !IsSidecarSubtitleName(videoPath, name) {
			continue
		}
		if info, err := os.Stat(filepath.Join(directory, name)); err == nil && info.Mode().IsRegular() {
			return true, nil
		}
	}
	return false, nil
}

// ensureSubtitleIndexForVideoOn 让 video 的字幕索引与磁盘上的同名 .srt 一致。sidecars 非空时
// （只有全库同步这么做）顺带写 subtitle_index_states.has_sidecar（D-PC17），目录项按一轮缓存。
// 扫描与搜索命中的就地刷新不写它，免得每条视频都多读一次整个目录。
func ensureSubtitleIndexForVideoOn(db *gorm.DB, video models.Video, sidecars *subtitleSidecarDirCache) error {
	srtPath := subtitleparser.SRTPathForVideo(video.Path)
	if _, err := os.Stat(srtPath); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := db.Where("video_id = ?", video.ID).Delete(&models.SubtitleSegment{}).Error; err != nil {
			return err
		}
		if err := upsertSubtitleIndexStateTx(db, video.ID, srtPath, 0, 0, 0); err != nil {
			return err
		}
	} else {
		current, err := isSubtitleIndexCurrentOn(db, video, srtPath)
		if err != nil || !current {
			segments, err := subtitleparser.ParseFile(srtPath)
			if err != nil {
				return err
			}
			if err := replaceSubtitleIndexOn(db, video, srtPath, segments); err != nil {
				return err
			}
		}
	}
	if sidecars == nil {
		return nil
	}
	hasSidecar, err := sidecars.hasSidecar(video.Path)
	if err != nil {
		// 目录读不了就不知道有没有旁挂字幕：保留原值，不把它当成「没有」。
		return err
	}
	return db.Model(&models.SubtitleIndexState{}).
		Where("video_id = ? AND has_sidecar <> ?", video.ID, hasSidecar).
		Update("has_sidecar", hasSidecar).Error
}

func indexSubtitleFileForVideoID(videoID uint, srtPath string) error {
	var video models.Video
	if err := database.DB.First(&video, videoID).Error; err != nil {
		return err
	}
	if srtPath == "" {
		srtPath = subtitleparser.SRTPathForVideo(video.Path)
	}
	srtPath = filepath.Clean(srtPath)
	segments, err := subtitleparser.ParseFile(srtPath)
	if err != nil {
		return err
	}
	return replaceSubtitleIndex(video, srtPath, segments)
}

func replaceSubtitleIndex(video models.Video, srtPath string, segments []subtitleparser.Segment) error {
	return replaceSubtitleIndexOn(database.DB, video, srtPath, segments)
}

// replaceSubtitleIndexOn 在 db 上重建一条视频的字幕索引。db 就是当前库时走 database.Transaction，
// 保留整笔事务期间的维护闸门；后台同步捕获的旧连接（库已换掉）只写它自己。
func replaceSubtitleIndexOn(db *gorm.DB, video models.Video, srtPath string, segments []subtitleparser.Segment) error {
	info, err := os.Stat(srtPath)
	if err != nil {
		return err
	}
	fn := func(tx *gorm.DB) error {
		return replaceSubtitleIndexTx(tx, video, srtPath, info, segments)
	}
	if db == database.DB {
		return database.Transaction(fn)
	}
	return db.Transaction(fn)
}

func rebuildSubtitleIndexTx(tx *gorm.DB, video models.Video) error {
	srtPath := subtitleparser.SRTPathForVideo(video.Path)
	info, err := os.Stat(srtPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := tx.Where("video_id = ?", video.ID).Delete(&models.SubtitleSegment{}).Error; err != nil {
			return err
		}
		return upsertSubtitleIndexStateTx(tx, video.ID, srtPath, 0, 0, 0)
	}
	segments, err := subtitleparser.ParseFile(srtPath)
	if err != nil {
		return err
	}
	return replaceSubtitleIndexTx(tx, video, srtPath, info, segments)
}

func replaceSubtitleIndexTx(tx *gorm.DB, video models.Video, srtPath string, info os.FileInfo, segments []subtitleparser.Segment) error {
	if err := tx.Where("video_id = ?", video.ID).Delete(&models.SubtitleSegment{}).Error; err != nil {
		return err
	}

	indexed := make([]models.SubtitleSegment, 0, len(segments))
	for idx, segment := range segments {
		text := strings.TrimSpace(segment.Text)
		if text == "" {
			continue
		}
		indexed = append(indexed, models.SubtitleSegment{
			VideoID:         video.ID,
			SegmentIndex:    idx + 1,
			StartTimeMs:     segment.StartTimeMs,
			EndTimeMs:       segment.EndTimeMs,
			Text:            text,
			SubtitlePath:    srtPath,
			SubtitleModTime: info.ModTime().UnixNano(),
		})
	}
	if len(indexed) > 0 {
		if err := tx.CreateInBatches(indexed, 500).Error; err != nil {
			return err
		}
	}
	return upsertSubtitleIndexStateTx(tx, video.ID, srtPath, info.ModTime().UnixNano(), info.Size(), len(indexed))
}

func deleteSubtitleIndex(videoID uint) error {
	return database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("video_id = ?", videoID).Delete(&models.SubtitleSegment{}).Error; err != nil {
			return err
		}
		return tx.Where("video_id = ?", videoID).Delete(&models.SubtitleIndexState{}).Error
	})
}

func isSubtitleIndexCurrent(video models.Video, srtPath string) (bool, error) {
	return isSubtitleIndexCurrentOn(database.DB, video, srtPath)
}

func isSubtitleIndexCurrentOn(db *gorm.DB, video models.Video, srtPath string) (bool, error) {
	info, err := os.Stat(srtPath)
	if err != nil {
		return false, err
	}

	var state models.SubtitleIndexState
	err = db.
		Where("video_id = ?", video.ID).
		First(&state).Error
	if err != nil {
		return false, err
	}

	return filepath.Clean(state.SubtitlePath) == filepath.Clean(srtPath) &&
		state.SubtitleModTime == info.ModTime().UnixNano() &&
		state.SubtitleSize == info.Size(), nil
}

func upsertSubtitleIndexStateTx(tx *gorm.DB, videoID uint, srtPath string, modTime int64, size int64, segmentCount int) error {
	now := time.Now()
	state := models.SubtitleIndexState{
		VideoID:         videoID,
		SubtitlePath:    filepath.Clean(srtPath),
		SubtitleModTime: modTime,
		SubtitleSize:    size,
		SegmentCount:    segmentCount,
		LastCheckedAt:   now,
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "video_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"subtitle_path":     state.SubtitlePath,
			"subtitle_mod_time": state.SubtitleModTime,
			"subtitle_size":     state.SubtitleSize,
			"segment_count":     state.SegmentCount,
			"last_checked_at":   state.LastCheckedAt,
			"updated_at":        now,
		}),
	}).Create(&state).Error
}

func splitSubtitleLines(text string) []string {
	lines := strings.Split(text, "\n")
	clean := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			clean = append(clean, line)
		}
	}
	if len(clean) == 0 && strings.TrimSpace(text) != "" {
		return []string{strings.TrimSpace(text)}
	}
	return clean
}
