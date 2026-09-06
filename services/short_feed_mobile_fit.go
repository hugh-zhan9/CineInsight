package services

import (
	"context"
	"log"
	"os"
	"time"

	"video-master/database"
	"video-master/models"
)

// 手机端直连上限（用户裁决 2026-09-07），刻意与播放代理的产物规格一致：长边 ≤1920、
// 总码率 ≤8 Mbps。超过任一项的源文件，就算容器在内嵌白名单里（mp4 / webm），在 Wi-Fi 上
// 也会卡——2.4G 实际吞吐只有二三十 Mbps，安卓浏览器还可能解不了 4K。这类文件有代理就发代理。
const (
	shortFeedMobileMaxLongSide       = 1920
	shortFeedMobileMaxBitRate  int64 = 8_000_000
	// 判定结果按源指纹缓存：Safari 一次播放会发很多个 Range 请求，每次都查两张快照表没有意义。
	shortFeedHeavyVerdictTTL = 10 * time.Minute
	// 同一条视频的自动代理请求最多十分钟提一次：代理生成要几分钟，期间的每个 Range 请求都会走到这里。
	shortFeedAutoProxyInterval = 10 * time.Minute
)

type shortFeedHeavyVerdict struct {
	fingerprint playbackProxyFingerprint
	heavy       bool
	at          time.Time
}

// sourceBitRateForMobile 估算源文件码率：优先技术快照的总码率，快照缺失或不新鲜时用
// 大小 × 8 / 时长；时长未知则给 0（只能靠分辨率判定）。
func sourceBitRateForMobile(video models.Video, fileSize int64, snapshotBitRate int64) int64 {
	if snapshotBitRate > 0 {
		return snapshotBitRate
	}
	if video.Duration <= 0 || fileSize <= 0 {
		return 0
	}
	return int64(float64(fileSize) * 8 / video.Duration)
}

// sourceTooHeavyForMobile 判定源文件是否超出手机端直连上限。
func sourceTooHeavyForMobile(video models.Video, fileSize int64, snapshotBitRate int64) bool {
	longSide := video.Width
	if video.Height > longSide {
		longSide = video.Height
	}
	if longSide > shortFeedMobileMaxLongSide {
		return true
	}
	return sourceBitRateForMobile(video, fileSize, snapshotBitRate) > shortFeedMobileMaxBitRate
}

// sourceTooHeavyForMobileCached 带缓存的判定：同一源指纹十分钟内只算一次。
func (s *ShortFeedService) sourceTooHeavyForMobileCached(video models.Video, info os.FileInfo) bool {
	fingerprint := playbackProxyFingerprintOf(info)
	now := s.now()
	s.mobileFitMu.Lock()
	if verdict, ok := s.heavyVerdicts[video.ID]; ok && verdict.fingerprint.matches(fingerprint) && now.Sub(verdict.at) < shortFeedHeavyVerdictTTL {
		s.mobileFitMu.Unlock()
		return verdict.heavy
	}
	s.mobileFitMu.Unlock()

	var snapshotBitRate int64
	if snapshot, ok, err := loadPlaybackProxySnapshot(video); err == nil && ok {
		snapshotBitRate = snapshot.TotalBitRate
	}
	heavy := sourceTooHeavyForMobile(video, info.Size(), snapshotBitRate)

	s.mobileFitMu.Lock()
	if s.heavyVerdicts == nil {
		s.heavyVerdicts = make(map[uint]shortFeedHeavyVerdict)
	}
	s.heavyVerdicts[video.ID] = shortFeedHeavyVerdict{fingerprint: fingerprint, heavy: heavy, at: now}
	s.mobileFitMu.Unlock()
	return heavy
}

// requestMobileFitProxy 在手机端实际播到一条超标源文件、而代理还没有时，请求后台生成一份。
// 受「自动生成播放代理」开关约束（关掉就只发源文件），每条视频十分钟最多提一次，
// 入队本身是非阻塞的：这次请求照发源文件，下次就能换上代理。
func (s *ShortFeedService) requestMobileFitProxy(proxies *PlaybackProxyService, videoID uint) {
	if proxies == nil || videoID == 0 {
		return
	}
	now := s.now()
	s.mobileFitMu.Lock()
	if last, ok := s.autoProxyRequestedAt[videoID]; ok && now.Sub(last) < shortFeedAutoProxyInterval {
		s.mobileFitMu.Unlock()
		return
	}
	if s.autoProxyRequestedAt == nil {
		s.autoProxyRequestedAt = make(map[uint]time.Time)
	}
	s.autoProxyRequestedAt[videoID] = now
	s.mobileFitMu.Unlock()

	var settings models.Settings
	if err := database.DB.Select("auto_compatibility_proxy").First(&settings).Error; err != nil || !settings.AutoCompatibilityProxy {
		return
	}
	if _, err := proxies.EnqueueMobileFitCandidates(context.Background(), []uint{videoID}); err != nil {
		log.Printf("[ShortFeed] 请求生成手机端代理失败 video_id=%d err=%v", videoID, err)
	}
}

// EnqueueMobileFitCandidates 是手机端按需路径的入口：源文件已在白名单里但超出直连上限，
// 所以不走 filterPlaybackProxyCandidates 的白名单筛选，只确认视频仍然活跃。
func (s *PlaybackProxyService) EnqueueMobileFitCandidates(parent context.Context, videoIDs []uint) (PlaybackProxyStatus, error) {
	if s == nil {
		return PlaybackProxyStatus{}, errPlaybackProxyServiceMissing
	}
	if len(videoIDs) == 0 || database.DB == nil {
		return s.Status(), nil
	}
	var active []uint
	if err := database.DB.Model(&models.Video{}).Where("id IN ?", videoIDs).Pluck("id", &active).Error; err != nil {
		return s.Status(), err
	}
	if len(active) == 0 {
		return s.Status(), nil
	}
	return s.enqueue(parent, active)
}
