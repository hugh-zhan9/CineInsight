package services

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"gorm.io/gorm"

	"video-master/database"
	"video-master/models"
)

// IINA（以及底层的 mpv）在退出播放时把断点写进 watch_later 目录：文件名是
// 播放路径原文的 MD5（大写十六进制），内容是 mpv 的 conf 片段，其中 start=<秒>
// 就是看到哪儿了。这个文件被删除不只发生在播到结尾：mpv 续播时加载完断点很可能
// 立刻把它删掉（需真机确认），用户也可能自己清掉记录，所以删除事件本身不说明看完了
// （见 settleRemovedEntry 的两道保护）。
//
// 系统播放器一路只记了播放次数，不记进度，"继续观看"视图和行上的进度条
// 对外部播放形同虚设。这里把 IINA 的断点读回来补上这一环。
const iinaWatchLaterRelativeDir = "Library/Application Support/com.colliderli.iina/watch_later"

// iinaSessionTTL 是应用发起的 IINA 会话的登记有效期（D-PC41）。超过这个时间的删除事件
// 不再据墙钟推算——那多半已经不是这一次播放了。
const iinaSessionTTL = 12 * time.Hour

// iinaRemovedWatchedRatio：删除事件时，最后已知位置达到时长的这个比例也判为看完（D-PC41）。
const iinaRemovedWatchedRatio = 0.9

// iinaRemovedMinElapsedCap 是删除事件「距启动太近」的墙钟上限（秒），见 iinaRemovedMinElapsed。
const iinaRemovedMinElapsedCap = 60.0

// iinaRemovedMinElapsedFloor 是这道门槛的绝对下限（秒，A-m6）：短片或离片尾很近的续播，剩余时长的
// 一半可能只有一两秒，与 mpv 加载断点后立刻删文件的时间差不多，挡不住加载时的那次删除。
const iinaRemovedMinElapsedFloor = 10.0

// iinaRemovedMinElapsed 是删除事件距应用发起播放至少要过去的墙钟秒数：
// max(10 秒, min(60 秒, 0.5 ×（时长 − 起播位置）))。不到这个时长的删除多半是 mpv 续播时加载完断点
// 就删掉了文件，与看没看完无关：既不结算，也不消耗会话，之后真正播完的那次删除还要靠它。
// 代价是不到 10 秒就播完的片段收不到看完结算（与「从头播完」同属已知缺口，设计 §8.2）。
func iinaRemovedMinElapsed(duration, startPosition float64) float64 {
	return math.Max(iinaRemovedMinElapsedFloor, math.Min(iinaRemovedMinElapsedCap, 0.5*(duration-startPosition)))
}

// IINAProgressUpdate 是一条被同步的进度。界面拿它就地更新对应的行，
// 不必整表重载——重载会按当前排序重新打分，刚看完的视频会跳到别的位置，
// 用户反而找不到自己刚才在看哪个。
type IINAProgressUpdate struct {
	VideoID              uint    `json:"video_id"`
	WatchPositionSeconds float64 `json:"watch_position_seconds"`
	// Watched 表示这次同步把它判成了看完。前端据此就地补上「已看」徽标，
	// 否则行上只会看到进度条消失，徽标要等下一次整页重载才出现。
	Watched bool `json:"watched"`
}

// IINAProgressSyncResult 汇总一次同步的结果。
type IINAProgressSyncResult struct {
	Scanned int                  `json:"scanned"`
	Updated int                  `json:"updated"`
	Skipped int                  `json:"skipped"`
	Changes []IINAProgressUpdate `json:"changes"`
}

// IINASyncStatus 是设置页展示的 IINA 同步状态（D-PC47）。
type IINASyncStatus struct {
	// Enabled 表示正在监听断点目录。
	Enabled bool `json:"enabled"`
	// WatchingDir 是正在监听的目录；没有在监听时为空。
	WatchingDir string `json:"watching_dir"`
	// LastSyncAt 是最近一次成功同步（含删除事件的看完判定）的时间，从未成功过为 nil。
	LastSyncAt *time.Time `json:"last_sync_at" ts_type:"string"`
	// LastError 是最近一次失败的原因（已去掉绝对路径）；之后成功同步一次就清空。
	LastError string `json:"last_error"`
}

// iinaLaunchedSession 是一次由应用发起的播放（D-PC41）。只在内存里，重启即丢。
//
// 会话在三种情况下结束：删除事件结算（或判定没看完）时消耗掉；同步读到这次播放退出时写下的
// 断点（文件 mtime 不早于 launchedAt，见 endSessionOnExitWrite）；iinaSessionTTL 过期。
type iinaLaunchedSession struct {
	videoID       uint
	path          string
	startPosition float64
	launchedAt    time.Time
}

// IINAProgressService 把 IINA 的播放断点同步进片库。
type IINAProgressService struct {
	mu            sync.Mutex
	watchLaterDir string
	// 测试接缝：读取断点文件内容与修改时间。
	readEntry func(path string) (string, time.Time, error)

	watchMu   sync.Mutex
	watcher   *fsnotify.Watcher
	onSynced  func(IINAProgressSyncResult)
	watchStop chan struct{}

	// notifyWatched 是已看翻转的转发口（接线为 VideoService.NotifyWatchStateChanged）。
	notifyMu      sync.RWMutex
	notifyWatched func(videoID uint, watched bool)

	// sessions 按 watch_later 文件名（路径 MD5）索引应用发起的播放会话。锁序：mu → sessionMu。
	sessionMu sync.Mutex
	sessions  map[string]*iinaLaunchedSession

	statusMu   sync.Mutex
	lastSyncAt *time.Time
	lastError  string
}

func NewIINAProgressService(homeDir string) *IINAProgressService {
	dir := ""
	if strings.TrimSpace(homeDir) != "" {
		dir = filepath.Join(homeDir, iinaWatchLaterRelativeDir)
	}
	return &IINAProgressService{
		watchLaterDir: dir,
		readEntry: func(path string) (string, time.Time, error) {
			info, err := os.Stat(path)
			if err != nil {
				return "", time.Time{}, err
			}
			data, err := os.ReadFile(path)
			return string(data), info.ModTime(), err
		},
		sessions: map[string]*iinaLaunchedSession{},
	}
}

// WatchLaterDir 返回被监听的断点目录，空字符串表示不可用。
func (s *IINAProgressService) WatchLaterDir() string {
	return s.watchLaterDir
}

// Available 表示这台机器上确实存在 IINA 的断点目录。
func (s *IINAProgressService) Available() bool {
	if s.watchLaterDir == "" {
		return false
	}
	info, err := os.Stat(s.watchLaterDir)
	return err == nil && info.IsDir()
}

// iinaWatchLaterName 按 mpv 的规则算断点文件名：路径原文的 MD5 大写十六进制。
func iinaWatchLaterName(path string) string {
	digest := md5.Sum([]byte(path))
	return strings.ToUpper(hex.EncodeToString(digest[:]))
}

// parseIINAStartSeconds 从 watch_later 文件内容里取 start= 的秒数。
// "# redirect entry" 这类没有 start 的条目返回 false。
func parseIINAStartSeconds(content string) (float64, bool) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "start=") {
			continue
		}
		seconds, err := strconv.ParseFloat(strings.TrimPrefix(line, "start="), 64)
		if err != nil || seconds < 0 {
			return 0, false
		}
		return seconds, true
	}
	return 0, false
}

// SetOnSynced 注册"同步完成且确实更新了记录"的回调，用来通知界面刷新。
func (s *IINAProgressService) SetOnSynced(hook func(IINAProgressSyncResult)) {
	s.watchMu.Lock()
	s.onSynced = hook
	s.watchMu.Unlock()
}

// SetWatchedNotifier 注入已看翻转的转发口（接线项：VideoService.NotifyWatchStateChanged）。
// IINA 同步直接写库，观察者由 VideoService 统一持有，这里只负责转发。
func (s *IINAProgressService) SetWatchedNotifier(notify func(videoID uint, watched bool)) {
	s.notifyMu.Lock()
	s.notifyWatched = notify
	s.notifyMu.Unlock()
}

func (s *IINAProgressService) notifyWatchedFlips(videoIDs []uint) {
	if len(videoIDs) == 0 {
		return
	}
	s.notifyMu.RLock()
	notify := s.notifyWatched
	s.notifyMu.RUnlock()
	if notify == nil {
		return
	}
	for _, videoID := range videoIDs {
		notify(videoID, true)
	}
}

// RegisterLaunchedSession 登记一次由应用发起的播放（D-PC41）。startPosition 是本次起播位置：
// 续播为库内断点，从头播为 0。只存内存，iinaSessionTTL 后过期；同一文件再次登记会覆盖。
func (s *IINAProgressService) RegisterLaunchedSession(videoID uint, path string, startPosition float64, launchedAt time.Time) {
	if s == nil || videoID == 0 || strings.TrimSpace(path) == "" {
		return
	}
	if startPosition < 0 || math.IsNaN(startPosition) || math.IsInf(startPosition, 0) {
		startPosition = 0
	}
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	s.pruneSessionsLocked(launchedAt)
	s.sessions[iinaWatchLaterName(path)] = &iinaLaunchedSession{
		videoID: videoID, path: path, startPosition: startPosition, launchedAt: launchedAt,
	}
}

// OnPlaybackLaunched 以当前时间登记会话，签名与 playback_launcher.go 的 onPlaybackLaunched 一致，
// 接线时直接 SetPlaybackLaunchedHook(iinaProgress.OnPlaybackLaunched)。
func (s *IINAProgressService) OnPlaybackLaunched(videoID uint, path string, startPosition float64) {
	s.RegisterLaunchedSession(videoID, path, startPosition, time.Now())
}

func (s *IINAProgressService) pruneSessionsLocked(now time.Time) {
	for name, session := range s.sessions {
		if now.Sub(session.launchedAt) > iinaSessionTTL {
			delete(s.sessions, name)
		}
	}
}

// endSessionOnExitWrite 在同步读到该视频的断点文件、且文件修改时间不早于会话启动时间时结束会话
// （A-I-1）：这说明应用发起的这次播放已经退出并写下了位置——它的进度由同步按常规口径采用，
// 之后再发生的删除（例如数小时后用户直接在 IINA 里打开同一文件、mpv 加载断点后删掉它）
// 不属于这次播放，按未登记处理，不再拿墙钟去推算位置。修改时间早于启动时间的是上一次播放留下的，
// 会话照旧保留。
func (s *IINAProgressService) endSessionOnExitWrite(name string, videoID uint, modTime time.Time) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	session, ok := s.sessions[name]
	if !ok || session.videoID != videoID || modTime.Before(session.launchedAt) {
		return
	}
	delete(s.sessions, name)
}

// peekSession 取一个未过期会话的副本，不移除；过期的顺手删掉。
func (s *IINAProgressService) peekSession(name string, now time.Time) (iinaLaunchedSession, bool) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	session, ok := s.sessions[name]
	if !ok {
		return iinaLaunchedSession{}, false
	}
	if now.Sub(session.launchedAt) > iinaSessionTTL {
		delete(s.sessions, name)
		return iinaLaunchedSession{}, false
	}
	return *session, true
}

// consumeSession 移除 peekSession 取到的那个会话；一次删除事件只结算一次。期间同一文件被重新
// 登记（新的一次播放）时不动新会话。
func (s *IINAProgressService) consumeSession(name string, taken iinaLaunchedSession) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	session, ok := s.sessions[name]
	if ok && session.videoID == taken.videoID && session.launchedAt.Equal(taken.launchedAt) {
		delete(s.sessions, name)
	}
}

// Status 返回设置页展示的同步状态（D-PC47）。
func (s *IINAProgressService) Status() IINASyncStatus {
	status := IINASyncStatus{}
	s.watchMu.Lock()
	if s.watcher != nil {
		status.Enabled = true
		status.WatchingDir = s.watchLaterDir
	}
	s.watchMu.Unlock()
	s.statusMu.Lock()
	if s.lastSyncAt != nil {
		at := *s.lastSyncAt
		status.LastSyncAt = &at
	}
	status.LastError = s.lastError
	s.statusMu.Unlock()
	return status
}

func (s *IINAProgressService) recordSyncSuccess(at time.Time) {
	s.statusMu.Lock()
	s.lastSyncAt = &at
	s.lastError = ""
	s.statusMu.Unlock()
}

func (s *IINAProgressService) recordSyncError(err error) {
	if err == nil {
		return
	}
	s.statusMu.Lock()
	s.lastError = scrubPlaybackProxyPaths(err.Error())
	s.statusMu.Unlock()
}

// StartWatching 监听 IINA 的断点目录。IINA 在退出播放时才写这个文件，
// 所以事件一到就意味着"刚看完一段"，这时候同步最及时——不必等下次启动应用。
func (s *IINAProgressService) StartWatching() error {
	if err := s.startWatching(); err != nil {
		s.recordSyncError(err)
		return err
	}
	return nil
}

func (s *IINAProgressService) startWatching() error {
	if !s.Available() {
		return fmt.Errorf("没有找到 IINA 的播放断点目录")
	}
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if s.watcher != nil {
		return nil
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := watcher.Add(s.watchLaterDir); err != nil {
		watcher.Close()
		return err
	}
	s.watcher = watcher
	stop := make(chan struct{})
	s.watchStop = stop
	go s.watchLoop(watcher, stop)
	log.Printf("[IINA] 开始监听播放断点目录 %s", s.watchLaterDir)
	return nil
}

// StopWatching 停止监听。
func (s *IINAProgressService) StopWatching() {
	s.watchMu.Lock()
	watcher, stop := s.watcher, s.watchStop
	s.watcher, s.watchStop = nil, nil
	s.watchMu.Unlock()
	if stop != nil {
		close(stop)
	}
	if watcher != nil {
		watcher.Close()
	}
}

func (s *IINAProgressService) emitSynced(result IINAProgressSyncResult) {
	s.watchMu.Lock()
	hook := s.onSynced
	s.watchMu.Unlock()
	if hook != nil {
		hook(result)
	}
}

// watchLoop 把一串写事件合并成一次同步：IINA 退出时会连着写文件，
// 每个事件都跑一遍全库扫描没有意义。删除 / 改名事件逐个立即结算，只对本次会话登记过的视频
// 生效（D-PC41）；删除不一定意味着播到了结尾，结算前的保护见 settleRemovedEntry。
func (s *IINAProgressService) watchLoop(watcher *fsnotify.Watcher, stop chan struct{}) {
	const debounce = 400 * time.Millisecond
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-stop:
			if timer != nil {
				timer.Stop()
			}
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				s.handleRemovedEvent(filepath.Base(event.Name), time.Now())
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.NewTimer(debounce)
			timerC = timer.C
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			s.recordSyncError(fmt.Errorf("监听断点目录出错: %w", err))
			log.Printf("[IINA] 监听断点目录出错 err=%v", err)
		case <-timerC:
			timerC = nil
			result, err := s.Sync()
			if err != nil {
				log.Printf("[IINA] 同步播放进度失败 err=%v", err)
				continue
			}
			if result.Updated == 0 {
				continue
			}
			s.emitSynced(result)
		}
	}
}

func (s *IINAProgressService) handleRemovedEvent(name string, eventTime time.Time) {
	change, ok, err := s.settleRemovedEntry(name, eventTime)
	if err != nil {
		s.recordSyncError(err)
		log.Printf("[IINA] 结算删除事件失败 err=%v", err)
		return
	}
	if !ok {
		return
	}
	s.emitSynced(IINAProgressSyncResult{Updated: 1, Changes: []IINAProgressUpdate{change}})
}

// settleRemovedEntry 结算一次 watch_later 删除 / 改名（D-PC41）。
//
// 只对本次会话登记过、仍在进行的播放生效；未登记的删除照旧忽略（多半是用户自己清了 IINA 的记录，
// 或者是应用发起的那次播放退出之后，用户直接在 IINA 里打开同一文件——见 endSessionOnExitWrite）。
//
// 删除并不只在播到结尾时发生：mpv 续播时加载完断点很可能马上删掉文件。墙钟推算的位置在那一刻
// 就是「起播位置 + 一两秒」，库内断点又常常已在 90% 以后，直接结算会把刚开始续播的片子判成看完。
// 所以距启动不到 iinaRemovedMinElapsed 的删除既不结算也不消耗会话，留给之后真正播完的那次删除。
//
// 过了这道门：最后已知位置取库内断点与按墙钟推算的位置（起播位置 + 经过时间）两者的最大值——
// 暂停只会让墙钟推算偏大。满足看完判定或达到时长 90% 时标已看、清零断点，返回给界面的变更
// Watched=true。文件其实还在（被重写）时不算删除。
func (s *IINAProgressService) settleRemovedEntry(name string, eventTime time.Time) (IINAProgressUpdate, bool, error) {
	s.mu.Lock()
	change, flipped, ok, err := s.settleRemovedEntryLocked(name, eventTime)
	s.mu.Unlock()
	if err != nil || !ok {
		return IINAProgressUpdate{}, false, err
	}
	s.recordSyncSuccess(time.Now())
	if flipped {
		s.notifyWatchedFlips([]uint{change.VideoID})
	}
	return change, true, nil
}

func (s *IINAProgressService) settleRemovedEntryLocked(name string, eventTime time.Time) (IINAProgressUpdate, bool, bool, error) {
	if s.watchLaterDir != "" {
		if _, err := os.Stat(filepath.Join(s.watchLaterDir, name)); err == nil {
			return IINAProgressUpdate{}, false, false, nil
		}
	}
	session, ok := s.peekSession(name, eventTime)
	if !ok {
		return IINAProgressUpdate{}, false, false, nil
	}
	var video models.Video
	err := database.DB.Select("id", "duration", "watch_position_seconds", "is_watched").First(&video, session.videoID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		s.consumeSession(name, session)
		return IINAProgressUpdate{}, false, false, nil
	}
	if err != nil {
		return IINAProgressUpdate{}, false, false, err
	}
	if video.Duration <= 0 {
		s.consumeSession(name, session)
		return IINAProgressUpdate{}, false, false, nil
	}
	elapsed := eventTime.Sub(session.launchedAt).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed < iinaRemovedMinElapsed(video.Duration, session.startPosition) {
		// 续播加载断点时的那次删除：会话留着，不结算。
		return IINAProgressUpdate{}, false, false, nil
	}
	s.consumeSession(name, session)
	lastKnown := max(video.WatchPositionSeconds, session.startPosition+elapsed)
	if !isWatchCompleted(lastKnown, video.Duration) && lastKnown < iinaRemovedWatchedRatio*video.Duration {
		return IINAProgressUpdate{}, false, false, nil
	}
	if video.IsWatched && video.WatchPositionSeconds == 0 {
		return IINAProgressUpdate{}, false, false, nil
	}
	flipped, applied, err := markWatchedFromCompletion(database.DB, video.ID,
		map[string]interface{}{"watch_progress_updated_at": eventTime}, time.Now())
	if err != nil || !applied {
		return IINAProgressUpdate{}, false, false, err
	}
	log.Printf("[IINA] 播到结尾判为看完 video_id=%d last_known=%.1f duration=%.1f", video.ID, lastKnown, video.Duration)
	return IINAProgressUpdate{VideoID: video.ID, WatchPositionSeconds: 0, Watched: true}, flipped, true, nil
}

// Sync 遍历库内视频，把 IINA 记下的断点补进 watch_position_seconds（D-PC42：以最近一次写入为准）。
//
//   - 断点文件的修改时间晚于库里的 watch_progress_updated_at 才采用，否则忽略：用户可能在
//     应用内或 Jellyfin 里看得更新，那份记录优先；采用时把进度更新时间写成文件的修改时间。
//   - 已看视频的断点不可续（resumable 为 false），且断点文件不晚于已看时间：那是陈旧记录，
//     跳过，不能把已看的片子拉回"在看"。已看时间与进度时间都为空时无从比较新旧，同样跳过；
//     只缺已看时间（watched_at 为空、进度时间非空）的交给下一条的修改时间比较（见 iinaSkipStaleWatchedEntry）。
//   - 断点停在片尾区间内与应用内播放同一口径判为看完（2026-09-13 裁决）。完成判定前面不再有
//     按位置幅度的守卫，历史遗留行（位置顶到片尾、未标已看）重播后照样自愈。
//
// 读到应用发起的播放退出时写下的断点（修改时间不早于启动时间）会结束那次会话（endSessionOnExitWrite）。
// 断点文件"消失"由监听里的删除事件结算，只对仍在进行的会话生效（见 settleRemovedEntry）。
func (s *IINAProgressService) Sync() (IINAProgressSyncResult, error) {
	s.mu.Lock()
	result, flipped, err := s.syncLocked()
	s.mu.Unlock()
	if err != nil {
		s.recordSyncError(err)
		return result, err
	}
	s.recordSyncSuccess(time.Now())
	s.notifyWatchedFlips(flipped)
	return result, nil
}

func (s *IINAProgressService) syncLocked() (IINAProgressSyncResult, []uint, error) {
	result := IINAProgressSyncResult{Changes: make([]IINAProgressUpdate, 0)}
	if !s.Available() {
		return result, nil, fmt.Errorf("没有找到 IINA 的播放断点目录")
	}

	var videos []models.Video
	if err := database.DB.Select("id, path, duration, watch_position_seconds, is_watched, watched_at, watch_progress_updated_at").Find(&videos).Error; err != nil {
		return result, nil, err
	}
	var flipped []uint
	for _, video := range videos {
		if strings.TrimSpace(video.Path) == "" {
			continue
		}
		result.Scanned++
		name := iinaWatchLaterName(video.Path)
		content, modTime, err := s.readEntry(filepath.Join(s.watchLaterDir, name))
		if err != nil {
			continue // 没有断点：没看过，或者已经看完被 mpv 删掉了
		}
		seconds, ok := parseIINAStartSeconds(content)
		if !ok {
			continue
		}
		if video.Duration > 0 && seconds > video.Duration {
			seconds = video.Duration
		}
		// PG 的时间戳只到微秒：不截断的话，写回去的修改时间读出来总比文件早一点，
		// 同一个文件每次同步都会被当成"更新的写入"。
		modTime = modTime.Truncate(time.Microsecond)
		s.endSessionOnExitWrite(name, video.ID, modTime)
		if iinaSkipStaleWatchedEntry(&video, modTime) {
			result.Skipped++
			continue
		}
		if video.WatchProgressUpdatedAt != nil && !modTime.After(*video.WatchProgressUpdatedAt) {
			result.Skipped++
			continue
		}
		// 条件更新：读库之后若有更新的写入（内嵌预览、Jellyfin），这次就让给它。
		newerOnly := func(db *gorm.DB) *gorm.DB {
			return db.Where("(videos.watch_progress_updated_at IS NULL OR videos.watch_progress_updated_at < ?)", modTime)
		}
		change := IINAProgressUpdate{VideoID: video.ID, WatchPositionSeconds: seconds}
		applied := false
		if isWatchCompleted(seconds, video.Duration) {
			var wasFlipped bool
			wasFlipped, applied, err = markWatchedFromCompletion(database.DB, video.ID,
				map[string]interface{}{"watch_progress_updated_at": modTime}, time.Now(), newerOnly)
			if err != nil {
				return result, flipped, err
			}
			if wasFlipped {
				flipped = append(flipped, video.ID)
			}
			change.WatchPositionSeconds, change.Watched = 0, true
		} else {
			update := database.DB.Model(&models.Video{}).Scopes(newerOnly).Where("videos.id = ?", video.ID).
				Updates(map[string]interface{}{"watch_position_seconds": seconds, "watch_progress_updated_at": modTime})
			if update.Error != nil {
				return result, flipped, update.Error
			}
			applied = update.RowsAffected == 1
		}
		if !applied {
			result.Skipped++
			continue
		}
		result.Updated++
		result.Changes = append(result.Changes, change)
	}
	if result.Updated > 0 {
		log.Printf("[IINA] 同步播放进度 scanned=%d updated=%d skipped=%d", result.Scanned, result.Updated, result.Skipped)
	}
	return result, flipped, nil
}

// iinaSkipStaleWatchedEntry 判定已看、断点不可续的视频要不要跳过这份断点文件（D-PC42、A-m4）：
//   - 有已看时间：文件不晚于它就是标已看之前的陈旧记录，跳过；晚于它是重看，交给后面的比较；
//   - 已看时间与进度时间都为空（升级前的历史行）：无从比较新旧，跳过，不采用 watch_later——
//     否则任何一份残留的断点文件都会把已看的片子拉回「在看」；
//   - 只缺已看时间、进度时间非空：交给后面「文件修改时间晚于进度时间才采用」的比较。
func iinaSkipStaleWatchedEntry(video *models.Video, modTime time.Time) bool {
	if !video.IsWatched || resumable(video) {
		return false
	}
	if video.WatchedAt != nil {
		return !modTime.After(*video.WatchedAt)
	}
	return video.WatchProgressUpdatedAt == nil
}
