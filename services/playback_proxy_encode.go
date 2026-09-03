package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// 转封装可用的编码（D-003）。首个视频流与首个音频流都在名单里才走 remux。
var (
	playbackProxyRemuxableVideoCodecs = map[string]struct{}{"h264": {}, "hevc": {}}
	playbackProxyRemuxableAudioCodecs = map[string]struct{}{"aac": {}, "mp3": {}}
)

const (
	// playbackProxyMaxVideoBitrate 是转码码率上限（8 Mbps，D-003）。
	playbackProxyMaxVideoBitrate int64 = 8_000_000
	// playbackProxyAudioBitrate 是转码音频码率。
	playbackProxyAudioBitrate = "160k"
	// playbackProxyScaleFilter 把**长边**收到 1920 以内且永不放大。
	//
	// 只约束宽度是不够的：竖屏 2160×3840 会被"限宽 1920"放行成 1920×3413，
	// 那比 1080p 还高得多（AC-03 要的是不高于 1080p）。这里按长边分支：
	// 横向（iw>ih）限宽、纵向限高，另一边给 -2 由 ffmpeg 按比例算并对齐到偶数。
	// 表达式在滤镜期求值，看到的是**自动旋转之后**的尺寸，比数据库里存的宽高更准
	// （iPhone 竖拍 .MOV 在容器里是 1920×1080 + rotate 元数据）。
	playbackProxyScaleFilter = "scale='if(gt(iw,ih),min(1920,iw),-2)':'if(gt(iw,ih),-2,min(1920,ih))'"
	// playbackProxyStderrTailBytes 是失败时保留的 stderr 尾部长度（D-001 异常处理）。
	playbackProxyStderrTailBytes = 2 << 10
)

// playbackProxySnapshot 是策略判定需要的技术快照切片。
//
// VideoStreamIndex / AudioStreamIndex 是**容器里的绝对流序号**，不是 `v:0` 这种
// 相对序号：封面图也算一条 video 流，`-map 0:v:0` 有可能选中它而不是正片。
type playbackProxySnapshot struct {
	VideoCodec       string
	AudioCodec       string
	HasVideo         bool
	HasAudio         bool
	VideoStreamIndex int
	AudioStreamIndex int
	VideoBitRate     int64
	TotalBitRate     int64
}

// playbackProxyStreamMaps 给出这次编码要保留的流映射。
// 用绝对序号而不是 `0:v:0` / `0:a:0?`：前者精确指向快照里认定的正片视频轨，
// 绕开封面图那条 attached_pic 流。无音频时只映射视频。
func playbackProxyStreamMaps(snapshot playbackProxySnapshot) []string {
	maps := []string{"-map", fmt.Sprintf("0:%d", snapshot.VideoStreamIndex)}
	if snapshot.HasAudio {
		maps = append(maps, "-map", fmt.Sprintf("0:%d", snapshot.AudioStreamIndex))
	}
	return maps
}

// playbackProxyPlan 是一次编码的完整方案：策略名 + ffmpeg 参数数组。
// 参数集中在这里构造，测试用 stub 直接断言这个数组。
type playbackProxyPlan struct {
	Strategy string
	Args     []string
}

// planPlaybackProxy 按技术快照选 remux 或 transcode（D-003）。
//
// remux：源已经是 H.264/HEVC + AAC/MP3（或无音频），只换容器加 faststart，秒级完成。
// transcode：其余一切，经 VideoToolbox 重编码到 H.264 + AAC，长边收到 1920 以内。
//
// 两条路都只保留一路视频与一路音频：代理只服务预览与手机端，多音轨和字幕流没有用，
// 而且默认流选择会把 ASS 字幕带进 mp4 容器里直接报错。
func planPlaybackProxy(snapshot playbackProxySnapshot, sourcePath, outputPath string) playbackProxyPlan {
	if playbackProxyRemuxable(snapshot) {
		args := []string{"-v", "error", "-y", "-i", sourcePath}
		args = append(args, playbackProxyStreamMaps(snapshot)...)
		args = append(args, "-c", "copy")
		if snapshot.VideoCodec == "hevc" {
			// WKWebView 只认 hvc1 标签；ffprobe 不给我们 codec_tag_string，
			// 所以对 HEVC 一律显式打 hvc1——源本来就是 hvc1 时这是个空操作。
			args = append(args, "-tag:v", "hvc1")
		}
		args = append(args, "-movflags", "+faststart", outputPath)
		return playbackProxyPlan{Strategy: models.PlaybackProxyStrategyRemux, Args: args}
	}
	bitrate := playbackProxyTargetBitrate(snapshot)
	args := []string{"-v", "error", "-y", "-i", sourcePath}
	args = append(args, playbackProxyStreamMaps(snapshot)...)
	args = append(args,
		"-c:v", "h264_videotoolbox",
		"-b:v", strconv.FormatInt(bitrate, 10),
		"-vf", playbackProxyScaleFilter,
		"-c:a", "aac",
		"-b:a", playbackProxyAudioBitrate,
		"-movflags", "+faststart",
		outputPath,
	)
	return playbackProxyPlan{Strategy: models.PlaybackProxyStrategyTranscode, Args: args}
}

func playbackProxyRemuxable(snapshot playbackProxySnapshot) bool {
	if !snapshot.HasVideo {
		return false
	}
	if _, ok := playbackProxyRemuxableVideoCodecs[snapshot.VideoCodec]; !ok {
		return false
	}
	if !snapshot.HasAudio {
		return true
	}
	_, ok := playbackProxyRemuxableAudioCodecs[snapshot.AudioCodec]
	return ok
}

// playbackProxyTargetBitrate 取 min(源码率, 8 Mbps)；源码率未知时用上限。
// 视频流自己的码率优先，没有就退到容器总码率（音频那点占比对上限没有影响）。
func playbackProxyTargetBitrate(snapshot playbackProxySnapshot) int64 {
	source := snapshot.VideoBitRate
	if source <= 0 {
		source = snapshot.TotalBitRate
	}
	if source <= 0 || source > playbackProxyMaxVideoBitrate {
		return playbackProxyMaxVideoBitrate
	}
	return source
}

// loadPlaybackProxySnapshot 从最后一次成功的技术快照里读策略判定所需的字段。
// 快照不新鲜（缺失、有错、指纹不符）时返回 ok=false，调用方先探测一次再来。
func loadPlaybackProxySnapshot(video models.Video) (playbackProxySnapshot, bool, error) {
	if database.DB == nil {
		return playbackProxySnapshot{}, false, errors.New("数据库未初始化")
	}
	var metadata models.VideoTechnicalMetadata
	err := database.DB.First(&metadata, "video_id = ?", video.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return playbackProxySnapshot{}, false, nil
	}
	if err != nil {
		return playbackProxySnapshot{}, false, err
	}
	if !videoTechnicalSnapshotMatchesFile(video, &metadata) {
		return playbackProxySnapshot{}, false, nil
	}
	var streams []models.MediaStream
	if err := database.DB.
		Where("video_id = ?", video.ID).
		Order("stream_index ASC").
		Find(&streams).Error; err != nil {
		return playbackProxySnapshot{}, false, err
	}
	snapshot := playbackProxySnapshot{}
	if metadata.TotalBitRate != nil {
		snapshot.TotalBitRate = *metadata.TotalBitRate
	}
	for _, stream := range streams {
		switch stream.StreamType {
		case "video":
			// 封面图也是一条 video 流，但它不是主视频轨。
			if snapshot.HasVideo || stream.IsAttachedPic {
				continue
			}
			snapshot.HasVideo = true
			snapshot.VideoCodec = strings.ToLower(stream.CodecName)
			snapshot.VideoStreamIndex = stream.StreamIndex
			if stream.BitRate != nil {
				snapshot.VideoBitRate = *stream.BitRate
			}
		case "audio":
			if snapshot.HasAudio {
				continue
			}
			snapshot.HasAudio = true
			snapshot.AudioCodec = strings.ToLower(stream.CodecName)
			snapshot.AudioStreamIndex = stream.StreamIndex
		}
	}
	if !snapshot.HasVideo {
		// 快照存在但里面没有可用视频流：这不是"缺快照"，再探一次也是同样结果。
		return snapshot, false, nil
	}
	return snapshot, true, nil
}

// runPlaybackProxyFFmpeg 是默认的 ffmpeg 执行器。返回 stderr 尾部供错误摘要使用。
func runPlaybackProxyFFmpeg(ctx context.Context, args []string) (string, error) {
	binary, err := findThumbnailFFmpeg()
	if err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, binary, args...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return truncatePlaybackProxyStderr(stderr.String()), err
	}
	return truncatePlaybackProxyStderr(stderr.String()), nil
}

// probePlaybackProxyDuration 读产物时长，用于落位前的可读性校验（流程步骤 7）。
func probePlaybackProxyDuration(ctx context.Context, path string) (float64, error) {
	output, stderr, err := runLocalFFProbe(ctx, path)
	if err != nil {
		if message := strings.TrimSpace(stderr); message != "" {
			return 0, fmt.Errorf("%w: %s", err, truncatePlaybackProxyStderr(message))
		}
		return 0, err
	}
	parsed, err := parseMediaProbeOutput(output)
	if err != nil {
		return 0, err
	}
	if parsed.Duration == nil {
		return 0, errors.New("产物没有可读时长")
	}
	return *parsed.Duration, nil
}

// playbackProxyIsDiskFull 判断一次 ffmpeg 失败是不是磁盘满（D-001 异常处理）。
// ffmpeg 把 ENOSPC 写在 stderr 里而不是退出码里，所以两头都看。
func playbackProxyIsDiskFull(err error, stderr string) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ENOSPC) {
		return true
	}
	return strings.Contains(stderr, "No space left on device")
}

func truncatePlaybackProxyStderr(text string) string {
	trimmed := scrubPlaybackProxyPaths(strings.TrimSpace(text))
	if len(trimmed) <= playbackProxyStderrTailBytes {
		return trimmed
	}
	return trimmed[len(trimmed)-playbackProxyStderrTailBytes:]
}

// playbackProxyAbsolutePathPattern 匹配以 / 开头的连续 token（绝对路径）。
var playbackProxyAbsolutePathPattern = regexp.MustCompile(`/[^\s'"]*`)

// scrubPlaybackProxyPaths 把绝对路径擦成 <path>。
//
// ffmpeg / ffprobe 的报错里几乎一定带源文件全路径，而这段文本会两头外泄：
// 写进 video_playback_proxies.last_error（长期留在库里），以及经批量结果与
// playback-proxy-state 事件送到前端显示。保留 stderr 尾部对排障有用，
// 但路径全文不该跟着走——与仓库日志纪律同一条线。
func scrubPlaybackProxyPaths(text string) string {
	if !strings.Contains(text, "/") {
		return text
	}
	return playbackProxyAbsolutePathPattern.ReplaceAllStringFunc(text, func(match string) string {
		// 尾随标点不属于路径：留着它，"<path>: 原因"这种可读边界才不会被吃掉。
		suffix := ""
		for len(match) > 1 && strings.ContainsRune(":,.;!?)", rune(match[len(match)-1])) {
			suffix = string(match[len(match)-1]) + suffix
			match = match[:len(match)-1]
		}
		return "<path>" + suffix
	})
}
