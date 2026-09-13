package services

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"video-master/database"
	"video-master/models"
)

// IINA（以及底层的 mpv）在退出播放时把断点写进 watch_later 目录：文件名是
// 播放路径原文的 MD5（大写十六进制），内容是 mpv 的 conf 片段，其中 start=<秒>
// 就是看到哪儿了。播放到结尾时这个文件会被删掉。
//
// 系统播放器一路只记了播放次数，不记进度，"继续观看"视图和行上的进度条
// 对外部播放形同虚设。这里把 IINA 的断点读回来补上这一环。
const iinaWatchLaterRelativeDir = "Library/Application Support/com.colliderli.iina/watch_later"

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

// IINAProgressService 把 IINA 的播放断点同步进片库。
type IINAProgressService struct {
	mu            sync.Mutex
	watchLaterDir string
	// 测试接缝
	readEntry func(path string) (string, error)

	watchMu   sync.Mutex
	watcher   *fsnotify.Watcher
	onSynced  func(IINAProgressSyncResult)
	watchStop chan struct{}
}

func NewIINAProgressService(homeDir string) *IINAProgressService {
	dir := ""
	if strings.TrimSpace(homeDir) != "" {
		dir = filepath.Join(homeDir, iinaWatchLaterRelativeDir)
	}
	return &IINAProgressService{
		watchLaterDir: dir,
		readEntry: func(path string) (string, error) {
			data, err := os.ReadFile(path)
			return string(data), err
		},
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

// StartWatching 监听 IINA 的断点目录。IINA 在退出播放时才写这个文件，
// 所以事件一到就意味着"刚看完一段"，这时候同步最及时——不必等下次启动应用。
func (s *IINAProgressService) StartWatching() error {
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

// watchLoop 把一串写事件合并成一次同步：IINA 退出时会连着写文件，
// 每个事件都跑一遍全库扫描没有意义。
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
			// 只关心写入和创建；删除意味着看完了，但我们不据此改"已看"，忽略即可。
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
			s.watchMu.Lock()
			hook := s.onSynced
			s.watchMu.Unlock()
			if hook != nil {
				hook(result)
			}
		}
	}
}

// Sync 遍历库内视频，把 IINA 记下的断点补进 watch_position_seconds。
// 只往前推进进度，不回退：用户可能在应用内看得更远，那份记录更新。
//
// 断点文件"消失"仍然不据此判已看（可能是用户自己清了 IINA 的记录）；但断点位置
// 停在片尾是明确信号，与应用内播放同一口径判为看完（2026-09-13 裁决）。桌面「播放」
// 按钮走的就是 IINA，这条路不判的话，用户看到的还是「看到 00:28 / 00:28」却没标已看。
// 已经标记已看的视频直接跳过：看完了就没有断点可续，一份陈旧的 watch_later 记录
// 不该再把它拉回"在看"。
func (s *IINAProgressService) Sync() (IINAProgressSyncResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := IINAProgressSyncResult{Changes: make([]IINAProgressUpdate, 0)}
	if !s.Available() {
		return result, fmt.Errorf("没有找到 IINA 的播放断点目录")
	}

	var videos []models.Video
	if err := database.DB.Select("id, path, duration, watch_position_seconds, is_watched").Find(&videos).Error; err != nil {
		return result, err
	}
	for _, video := range videos {
		if strings.TrimSpace(video.Path) == "" {
			continue
		}
		result.Scanned++
		entryPath := filepath.Join(s.watchLaterDir, iinaWatchLaterName(video.Path))
		content, err := s.readEntry(entryPath)
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
		// 看完的不再回写：位置被清成 0 之后，"只前进不后退"这条防线就挡不住
		// 陈旧断点了，会把已看的片子重新变成"看到一半"。
		if video.IsWatched {
			result.Skipped++
			continue
		}
		// 完成判定必须排在前向守卫之前。两者幅度同为 1 秒，放在后面会有一段死区：
		// 库里存着 26.6 的 28 秒片子，用户续播到 27.3 退出，27.3 <= 26.6+1 先被跳过，
		// 判定根本跑不到。历史遗留行（位置已顶到片尾、is_watched 仍为 false）更是
		// 恒满足 seconds <= position+1，放在后面就永远不会自愈——而「不回填」这条
		// 裁决正是建立在"再播一次自然就好"上的。
		completed := isWatchedCompletionPosition(video.Duration, seconds)
		// 只前进不后退，且忽略毫秒级抖动
		if !completed && seconds <= video.WatchPositionSeconds+1 {
			result.Skipped++
			continue
		}
		updates := map[string]interface{}{"watch_position_seconds": seconds}
		recorded := seconds
		if completed {
			applyWatchedCompletionUpdates(updates, &video, time.Now())
			recorded = 0
		}
		if err := database.DB.Model(&models.Video{}).
			Where("id = ?", video.ID).
			Updates(updates).Error; err != nil {
			return result, err
		}
		result.Updated++
		result.Changes = append(result.Changes, IINAProgressUpdate{VideoID: video.ID, WatchPositionSeconds: recorded, Watched: completed})
	}
	if result.Updated > 0 {
		log.Printf("[IINA] 同步播放进度 scanned=%d updated=%d skipped=%d", result.Scanned, result.Updated, result.Skipped)
	}
	return result, nil
}
