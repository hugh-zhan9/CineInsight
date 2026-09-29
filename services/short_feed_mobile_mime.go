package services

import (
	"path/filepath"
	"strings"
	"time"
	"video-master/models"
)

// 手机端内嵌白名单（D-PC46，详细设计 §8.7）。
//
// inlinePreviewMIMEs 说的是「桌面预览内嵌能不能直接放」，一个字都不为手机端改动。手机浏览器
// （Safari/Chrome）对 .mov 的支持比桌面预览更宽：只要容器里是 h264/hevc + aac，就能直接播。
// 所以手机端在内嵌白名单之上，按技术快照再认一类 .mov / .m4v。

// shortFeedMobileMIMEVerdictTTL 判定缓存有效期：候选快照每 30 秒重建一次，
// 对每个 .mov 都查快照表与 stat 一遍不划算。
const shortFeedMobileMIMEVerdictTTL = 10 * time.Minute

type shortFeedMobileMIMEVerdict struct {
	// size 与 modTimeNS 是判定时源文件的指纹（stat 得到，不信库里的旧值）：
	// 文件原地替换、哪怕大小碰巧相同，也不沿用旧结论。
	size      int64
	modTimeNS int64
	mime      string
	ok        bool
	at        time.Time
}

// mobileInlineMIME 判定一条视频能否直接发给手机播放，返回下发用的 MIME。
//
// 先走 inlinePreviewMIME；不命中时，扩展名为 .mov / .m4v，快照首个视频流编码 ∈ {h264, hevc}，
// 且音频 ∈ {aac, 无} 才认。snapshot 为 nil（没有技术快照或快照与文件对不上）一律不认：
// 没有证据就不承诺「能播」。
func mobileInlineMIME(video models.Video, snapshot *playbackProxySnapshot) (string, bool) {
	if mimeType, ok := inlinePreviewMIME(video.Path); ok {
		return mimeType, true
	}
	var mimeType string
	switch strings.ToLower(filepath.Ext(video.Path)) {
	case ".mov":
		mimeType = "video/quicktime"
	case ".m4v":
		mimeType = "video/x-m4v"
	default:
		return "", false
	}
	if snapshot == nil || !snapshot.HasVideo {
		return "", false
	}
	switch strings.ToLower(snapshot.VideoCodec) {
	case "h264", "hevc":
	default:
		return "", false
	}
	if snapshot.HasAudio && strings.ToLower(snapshot.AudioCodec) != "aac" {
		return "", false
	}
	return mimeType, true
}

// mobileMIMEForVideo 是 mobileInlineMIME 的服务层入口：只对需要快照的扩展名才去读快照，
// 结果按（视频 ID + 源文件大小 + 修改时间）缓存一段时间。
func (s *ShortFeedService) mobileMIMEForVideo(video models.Video) (string, bool) {
	if mimeType, ok := inlinePreviewMIME(video.Path); ok {
		return mimeType, true
	}
	switch strings.ToLower(filepath.Ext(video.Path)) {
	case ".mov", ".m4v":
	default:
		return "", false
	}

	fingerprint, err := mediaProbeStat(video.Path)
	if err != nil {
		// 源文件读不到：没有证据，不承诺「能播」，也不缓存这个结论。
		return "", false
	}
	now := s.now()
	s.mobileMIMEMu.Lock()
	if verdict, ok := s.mobileMIMECache[video.ID]; ok && verdict.size == fingerprint.size && verdict.modTimeNS == fingerprint.modTimeNS && now.Sub(verdict.at) < shortFeedMobileMIMEVerdictTTL {
		s.mobileMIMEMu.Unlock()
		return verdict.mime, verdict.ok
	}
	s.mobileMIMEMu.Unlock()

	var snapshotPtr *playbackProxySnapshot
	if snapshot, found, err := loadPlaybackProxySnapshot(video); err == nil && found {
		snapshotPtr = &snapshot
	}
	mimeType, ok := mobileInlineMIME(video, snapshotPtr)

	// 还没有技术快照（后台回填可能马上就到）时只缓存一个候选快照周期，别把「暂时没证据」
	// 固化成十分钟的「不能播」。
	cachedAt := now
	if snapshotPtr == nil {
		cachedAt = now.Add(shortFeedCandidateTTL - shortFeedMobileMIMEVerdictTTL)
	}
	s.mobileMIMEMu.Lock()
	if s.mobileMIMECache == nil {
		s.mobileMIMECache = make(map[uint]shortFeedMobileMIMEVerdict)
	}
	s.mobileMIMECache[video.ID] = shortFeedMobileMIMEVerdict{size: fingerprint.size, modTimeNS: fingerprint.modTimeNS, mime: mimeType, ok: ok, at: cachedAt}
	s.mobileMIMEMu.Unlock()
	return mimeType, ok
}
