package services

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// playbackProxyDirName 是代理目录名，位于应用数据目录下（D-001）。
// 代理绝不写进用户的媒体目录，也绝不进 videos 表。
const playbackProxyDirName = "proxies"

// playbackProxyTempDirName 是同卷临时目录：产物先写这里，校验通过后 rename
// 到最终路径（同一个文件系统上 rename 是原子的，半个文件永远不会被当成代理）。
const playbackProxyTempDirName = ".tmp"

// playbackProxyLastUsedThrottle 是 last_used_at 的写入节流窗口（D-005）。
// 手机端一次滑动会打好几次媒体路由，每次都 UPDATE 一行毫无意义。
const playbackProxyLastUsedThrottle = 60 * time.Second

// playbackProxyEvictionGrace 是 LRU 淘汰的保护窗口（D-005）：刚用过的代理
// 不淘汰，正在播的那一份不会在播放中途被删。
//
// 必须**严格大于**节流窗口：节流让表里的 last_used_at 最多陈旧一个节流窗口，
// 两者等长时「刚刚还在播」的那一行会正好落在 cutoff 之外被算成过期。
// 取两倍再配上 touchProxy 里的"陈旧即强刷"，才真正兜住正在播的那一份。
const playbackProxyEvictionGrace = 2 * playbackProxyLastUsedThrottle

// NormalizeProxyCacheLimitBytes 把设置里的上限收敛成生效值（D-005）。
// 0 是"不限"这个真实取值，负数没有意义，按默认 50 GiB 处理。
func NormalizeProxyCacheLimitBytes(value int64) int64 {
	if value < 0 {
		return database.DefaultProxyCacheLimitBytes
	}
	return value
}

// PlaybackProxyView 是一份代理的对外形态（详情抽屉与设置页读它）。
// 不含路径：代理是隐藏派生文件，路径不该经绑定外泄。
type PlaybackProxyView struct {
	VideoID    uint      `json:"video_id"`
	Strategy   string    `json:"strategy"`
	Status     string    `json:"status"`
	OutputSize int64     `json:"output_size"`
	LastUsedAt time.Time `json:"last_used_at" ts_type:"string"`
	LastError  string    `json:"last_error"`
	CreatedAt  time.Time `json:"created_at" ts_type:"string"`
}

// playbackProxyFileNamePattern 是代理文件名的形状：`<videoID>-<16 位十六进制>.mp4`。
//
// 只有匹配它的文件才被认作"我们造的代理"。这一条不是洁癖：数据目录可能被用户
// 加成扫描根，或者手工放过别的东西进来，「清空全部代理」不能顺手删掉不认识的文件。
var playbackProxyFileNamePattern = regexp.MustCompile(`^\d+-[0-9a-f]{16}\.mp4$`)

// PlaybackProxyUsage 是代理目录的占用总览（设置页「播放代理」分区读它）。
type PlaybackProxyUsage struct {
	TotalBytes int64 `json:"total_bytes"`
	Count      int   `json:"count"`
	// LimitBytes 为 0 表示不限。
	LimitBytes int64 `json:"limit_bytes"`
	// OrphanCount / OrphanBytes 是"文件名符合命名规则但表里没有对应行"的产物。
	// 只看表会把它们算成 0，而它们照样占着磁盘（回退旧版本、表被清过、
	// 写表失败但文件已落位都会留下这种文件）。「清空全部」会一并删掉。
	OrphanCount int   `json:"orphan_count"`
	OrphanBytes int64 `json:"orphan_bytes"`
	// ForeignFiles 是代理目录里不符合命名规则的文件数：不统计、不删除，只报数。
	ForeignFiles int `json:"foreign_files"`
}

// resolvedPlaybackProxy 是一次命中的代理：调用方拿它换源。
type resolvedPlaybackProxy struct {
	Path       string
	Strategy   string
	OutputSize int64
}

// playbackProxyFingerprint 是源文件指纹（size + mtime_ns），
// 与缩略图、pHash、技术快照同一口径。
type playbackProxyFingerprint struct {
	size      int64
	modTimeNS int64
}

func playbackProxyFingerprintOf(info os.FileInfo) playbackProxyFingerprint {
	return playbackProxyFingerprint{size: info.Size(), modTimeNS: info.ModTime().UnixNano()}
}

func (f playbackProxyFingerprint) matches(other playbackProxyFingerprint) bool {
	return f.size == other.size && f.modTimeNS == other.modTimeNS
}

// playbackProxyFingerprintHash 是文件名里那 16 位：源指纹的 sha256 前 16 个十六进制字符。
// 指纹变一点文件名就变，所以同一个视频的新旧代理永远不会互相覆盖。
func playbackProxyFingerprintHash(f playbackProxyFingerprint) string {
	sum := sha256.Sum256([]byte(strconv.FormatInt(f.size, 10) + ":" + strconv.FormatInt(f.modTimeNS, 10)))
	return hex.EncodeToString(sum[:])[:16]
}

func playbackProxyFileName(videoID uint, f playbackProxyFingerprint) string {
	return fmt.Sprintf("%d-%s.mp4", videoID, playbackProxyFingerprintHash(f))
}

// ErrPlaybackProxyDirUnavailable 表示应用数据目录没解析出来，代理目录无从谈起。
//
// 这时候绝不能退到相对路径 `proxies/`：那会以进程工作目录为基准动手删文件。
// 所有会删东西的入口一律直接拒绝。
var ErrPlaybackProxyDirUnavailable = errors.New("应用数据目录不可用，播放代理功能已停用")

// Dir 返回代理目录（~/.CineInsight/proxies）。数据目录无效时返回空串。
func (s *PlaybackProxyService) Dir() string {
	if s == nil || s.dataDir == "" {
		return ""
	}
	return filepath.Join(s.dataDir, playbackProxyDirName)
}

// dirAvailable 报告代理目录是否可用。
func (s *PlaybackProxyService) dirAvailable() bool {
	return s != nil && s.dataDir != ""
}

func (s *PlaybackProxyService) tempDir() string {
	if !s.dirAvailable() {
		return ""
	}
	return filepath.Join(s.Dir(), playbackProxyTempDirName)
}

func (s *PlaybackProxyService) proxyPath(videoID uint, f playbackProxyFingerprint) string {
	if !s.dirAvailable() {
		return ""
	}
	return filepath.Join(s.Dir(), playbackProxyFileName(videoID, f))
}

func (s *PlaybackProxyService) proxyPathForRow(row models.VideoPlaybackProxy) string {
	return s.proxyPath(row.VideoID, playbackProxyFingerprint{size: row.SourceSize, modTimeNS: row.SourceModTimeNS})
}

// loadProxyRow 读一行代理元数据；没有就返回 nil, nil。
func loadProxyRow(videoID uint) (*models.VideoPlaybackProxy, error) {
	if database.DB == nil {
		return nil, nil
	}
	var row models.VideoPlaybackProxy
	err := database.DB.Where("video_id = ?", videoID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// GetPlaybackProxy 返回一个视频当前的代理元数据（没有则 nil）。
// 只读：不做指纹校验，也不刷新 last_used_at——那是消费路径的事。
func (s *PlaybackProxyService) GetPlaybackProxy(videoID uint) (*PlaybackProxyView, error) {
	row, err := loadProxyRow(videoID)
	if err != nil || row == nil {
		return nil, err
	}
	view := playbackProxyViewOf(*row)
	return &view, nil
}

func playbackProxyViewOf(row models.VideoPlaybackProxy) PlaybackProxyView {
	return PlaybackProxyView{
		VideoID:    row.VideoID,
		Strategy:   row.Strategy,
		Status:     row.Status,
		OutputSize: row.OutputSize,
		LastUsedAt: row.LastUsedAt,
		LastError:  row.LastError,
		CreatedAt:  row.CreatedAt,
	}
}

// resolveValidProxy 是消费路径的唯一入口（D-002、D-004）。
//
// 命中前必须拿调用方刚 stat 出来的源指纹跟表里的比一遍：不一致就把文件和行
// 一起删掉，本次请求按"没有代理"处理。这是设计里唯一的失效发现时机——不做后台巡检。
//
// touch 为 true 时顺带刷新 last_used_at（每视频 60 秒节流）。
func (s *PlaybackProxyService) resolveValidProxy(videoID uint, source playbackProxyFingerprint, touch bool) *resolvedPlaybackProxy {
	if !s.dirAvailable() {
		return nil
	}
	row, err := loadProxyRow(videoID)
	if err != nil || row == nil {
		return nil
	}
	if row.Status != models.PlaybackProxyStatusReady {
		return nil
	}
	stored := playbackProxyFingerprint{size: row.SourceSize, modTimeNS: row.SourceModTimeNS}
	if !stored.matches(source) {
		s.discardProxy(*row, "source_changed")
		return nil
	}
	path := s.proxyPathForRow(*row)
	info, statErr := os.Stat(path)
	if statErr != nil || info.IsDir() {
		// 文件被外部删了（回滚旧版本、手工清理），表行是孤儿，一起收掉。
		s.discardProxy(*row, "file_missing")
		return nil
	}
	if touch {
		s.touchProxy(videoID, row.LastUsedAt)
	}
	return &resolvedPlaybackProxy{Path: path, Strategy: row.Strategy, OutputSize: info.Size()}
}

// discardProxy 删掉一份失效代理的文件与表行。永不触碰源文件。
func (s *PlaybackProxyService) discardProxy(row models.VideoPlaybackProxy, reason string) {
	path := s.proxyPathForRow(row)
	if path == "" {
		// 目录无效：只能收表行，不猜路径。
		log.Printf("播放代理目录不可用，仅清理记录 video_id=%d reason=%s", row.VideoID, reason)
	} else if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("删除失效播放代理文件失败 video_id=%d reason=%s err=%v", row.VideoID, reason, err)
	}
	if database.DB != nil {
		if err := database.DB.Where("video_id = ?", row.VideoID).Delete(&models.VideoPlaybackProxy{}).Error; err != nil {
			log.Printf("删除失效播放代理记录失败 video_id=%d reason=%s err=%v", row.VideoID, reason, err)
			return
		}
	}
	s.forgetTouch(row.VideoID)
	log.Printf("播放代理已失效并删除 video_id=%d reason=%s", row.VideoID, reason)
}

// touchProxy 刷新 last_used_at。storedLastUsedAt 是表里当前的值：
// 它一旦老过保护窗口，就绕过节流强制刷一次——否则「持续在播但每次都被节流」的
// 代理会在表里越来越旧，最后被 LRU 当成最久没用过的删掉。
func (s *PlaybackProxyService) touchProxy(videoID uint, storedLastUsedAt time.Time) {
	now := s.now()
	stale := now.Sub(storedLastUsedAt) >= playbackProxyEvictionGrace
	s.touchMu.Lock()
	if last, ok := s.touchedAt[videoID]; ok && !stale && now.Sub(last) < playbackProxyLastUsedThrottle {
		s.touchMu.Unlock()
		return
	}
	if s.touchedAt == nil {
		s.touchedAt = make(map[uint]time.Time)
	}
	s.touchedAt[videoID] = now
	s.touchMu.Unlock()
	if database.DB == nil {
		return
	}
	if err := database.DB.Model(&models.VideoPlaybackProxy{}).
		Where("video_id = ?", videoID).
		Update("last_used_at", now).Error; err != nil {
		log.Printf("刷新播放代理使用时间失败 video_id=%d err=%v", videoID, err)
	}
}

func (s *PlaybackProxyService) forgetTouch(videoID uint) {
	s.touchMu.Lock()
	delete(s.touchedAt, videoID)
	s.touchMu.Unlock()
}

// DeleteForVideo 是删除级联入口（D-002）：视频进回收站或被永久删除时调用。
// nil 接收者与不存在的行都当成成功——级联失败不该把删除操作本身弄挂。
func (s *PlaybackProxyService) DeleteForVideo(videoID uint) {
	if s == nil {
		return
	}
	if !s.dirAvailable() {
		log.Printf("播放代理目录不可用，跳过删除级联 video_id=%d", videoID)
		return
	}
	row, err := loadProxyRow(videoID)
	if err != nil {
		log.Printf("读取播放代理记录失败（删除级联） video_id=%d err=%v", videoID, err)
		return
	}
	if row == nil {
		return
	}
	s.discardProxy(*row, "video_deleted")
}

// DeletePlaybackProxy 删除单个视频的代理（详情抽屉「删除此代理」）。
func (s *PlaybackProxyService) DeletePlaybackProxy(videoID uint) error {
	if s == nil {
		return errors.New("播放代理服务未初始化")
	}
	if !s.dirAvailable() {
		return ErrPlaybackProxyDirUnavailable
	}
	row, err := loadProxyRow(videoID)
	if err != nil {
		return fmt.Errorf("读取播放代理记录失败: %w", err)
	}
	if row == nil {
		return nil
	}
	s.discardProxy(*row, "user_deleted")
	s.emitStatus()
	return nil
}

// ClearPlaybackProxies 清空全部代理：表清空、目录里**符合命名规则**的文件删净
// （设置页「清空全部」）。
//
// 只删 `<videoID>-<16 位十六进制>.mp4`：数据目录有可能被用户加成扫描根，或者
// 手工放过别的东西进来，认不出来的文件一律跳过并报数，绝不"顺手清理"。
// 临时目录是个子目录，留着不动——那里可能有正在跑的编码。
func (s *PlaybackProxyService) ClearPlaybackProxies() (PlaybackProxyUsage, error) {
	if s == nil {
		return PlaybackProxyUsage{}, errors.New("播放代理服务未初始化")
	}
	if !s.dirAvailable() {
		return PlaybackProxyUsage{}, ErrPlaybackProxyDirUnavailable
	}
	if database.DB != nil {
		if err := database.DB.Where("1 = 1").Delete(&models.VideoPlaybackProxy{}).Error; err != nil {
			return PlaybackProxyUsage{}, fmt.Errorf("清空播放代理记录失败: %w", err)
		}
	}
	s.touchMu.Lock()
	s.touchedAt = make(map[uint]time.Time)
	s.touchMu.Unlock()
	entries, err := os.ReadDir(s.Dir())
	if err != nil && !os.IsNotExist(err) {
		return PlaybackProxyUsage{}, fmt.Errorf("读取播放代理目录失败: %w", err)
	}
	removed, foreign := 0, 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !playbackProxyFileNamePattern.MatchString(entry.Name()) {
			foreign++
			continue
		}
		if err := os.Remove(filepath.Join(s.Dir(), entry.Name())); err != nil && !os.IsNotExist(err) {
			return PlaybackProxyUsage{}, fmt.Errorf("删除播放代理文件失败: %w", err)
		}
		removed++
	}
	log.Printf("播放代理已清空 removed=%d foreign_skipped=%d", removed, foreign)
	usage, err := s.GetPlaybackProxyUsage()
	s.emitStatus()
	return usage, err
}

// GetPlaybackProxyUsage 返回占用、数量、上限，外加目录里的孤儿与陌生文件。
//
// 只统计 ready 行会漏掉"文件在、表里没行"的产物（回退旧版本、表被清过、
// 写表失败但文件已落位），它们照样占着磁盘。孤儿单列一档，别混进 Count：
// LRU 只管得着有行的那些，混在一起会让"占用 vs 上限"这笔账对不上。
func (s *PlaybackProxyService) GetPlaybackProxyUsage() (PlaybackProxyUsage, error) {
	usage := PlaybackProxyUsage{LimitBytes: s.cacheLimitBytes()}
	if database.DB == nil {
		return usage, nil
	}
	var rows []models.VideoPlaybackProxy
	if err := database.DB.
		Where("status = ?", models.PlaybackProxyStatusReady).
		Find(&rows).Error; err != nil {
		return usage, fmt.Errorf("统计播放代理占用失败: %w", err)
	}
	known := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		usage.TotalBytes += row.OutputSize
		known[playbackProxyFileName(row.VideoID, playbackProxyFingerprint{size: row.SourceSize, modTimeNS: row.SourceModTimeNS})] = struct{}{}
	}
	usage.Count = len(rows)
	if !s.dirAvailable() {
		return usage, nil
	}
	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		if os.IsNotExist(err) {
			return usage, nil
		}
		return usage, fmt.Errorf("读取播放代理目录失败: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !playbackProxyFileNamePattern.MatchString(entry.Name()) {
			usage.ForeignFiles++
			continue
		}
		if _, ok := known[entry.Name()]; ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		usage.OrphanCount++
		usage.OrphanBytes += info.Size()
	}
	return usage, nil
}

// cacheLimitBytes 读设置里的上限；读不到就按默认 50 GiB。
func (s *PlaybackProxyService) cacheLimitBytes() int64 {
	if database.DB == nil {
		return database.DefaultProxyCacheLimitBytes
	}
	var settings models.Settings
	if err := database.DB.Select("proxy_cache_limit_bytes").First(&settings).Error; err != nil {
		return database.DefaultProxyCacheLimitBytes
	}
	return NormalizeProxyCacheLimitBytes(settings.ProxyCacheLimitBytes)
}

// EnforcePlaybackProxyLimit 按上限做一次 LRU 整理（D-005）。
//
// 每次成功写入之后在同一个 worker 里跑一遍，设置页的「立即整理」也调它。
// 按 last_used_at 从旧到新删，跳过 60 秒内用过的行；上限为 0 时什么都不做。
// 只删代理文件，永不触碰源文件。
func (s *PlaybackProxyService) EnforcePlaybackProxyLimit() (PlaybackProxyUsage, error) {
	if s == nil {
		return PlaybackProxyUsage{}, errors.New("播放代理服务未初始化")
	}
	if !s.dirAvailable() {
		return PlaybackProxyUsage{}, ErrPlaybackProxyDirUnavailable
	}
	limit := s.cacheLimitBytes()
	usage, err := s.GetPlaybackProxyUsage()
	if err != nil {
		return usage, err
	}
	if limit <= 0 || usage.TotalBytes <= limit || database.DB == nil {
		return usage, nil
	}
	var rows []models.VideoPlaybackProxy
	if err := database.DB.
		Where("status = ?", models.PlaybackProxyStatusReady).
		Order("last_used_at ASC, video_id ASC").
		Find(&rows).Error; err != nil {
		return usage, fmt.Errorf("读取播放代理淘汰顺序失败: %w", err)
	}
	cutoff := s.now().Add(-playbackProxyEvictionGrace)
	total := usage.TotalBytes
	evicted := 0
	for _, row := range rows {
		if total <= limit {
			break
		}
		if row.LastUsedAt.After(cutoff) {
			// 刚用过的不动：正在播的那一份不能在播放中途被删。
			continue
		}
		s.discardProxy(row, "lru_evicted")
		total -= row.OutputSize
		evicted++
	}
	if evicted > 0 {
		log.Printf("播放代理 LRU 整理完成 evicted=%d limit=%d", evicted, limit)
	}
	final, err := s.GetPlaybackProxyUsage()
	if err != nil {
		return usage, err
	}
	return final, nil
}

// upsertProxyRow 按 video_id 覆盖写一行终态记录。
func upsertProxyRow(row models.VideoPlaybackProxy) error {
	if database.DB == nil {
		return errors.New("数据库未初始化")
	}
	return database.DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "video_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"source_size", "source_mod_time_ns", "strategy", "status",
			"output_size", "last_used_at", "last_error", "updated_at",
		}),
	}).Create(&row).Error
}
