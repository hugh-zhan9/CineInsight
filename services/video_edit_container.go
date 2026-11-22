package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// editContainerProfile 冻结整片裁剪的原容器及专用编码参数，旧计划为空。
type editContainerProfile struct {
	Muxer        string `json:"muxer"`
	Format       string `json:"format"`
	VideoEncoder string `json:"video_encoder"`
	VideoCodec   string `json:"video_codec"`
	AudioEncoder string `json:"audio_encoder"`
	AudioCodec   string `json:"audio_codec"`
	Identity     string `json:"identity"`
	SampleRate   int    `json:"sample_rate"`
	AudioBitRate int    `json:"audio_bit_rate"`
}

func editPublishMode(mode string) string {
	if mode == "replace_source" {
		return mode
	}
	return "save_copy"
}

func fullCropContainerProfile(path, format string) (*editContainerProfile, error) {
	p := &editContainerProfile{AudioEncoder: "aac", AudioCodec: "aac", SampleRate: 48000, AudioBitRate: 192000}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v":
		p.Muxer, p.Format = "mp4", "mp4"
	case ".mov", ".qt":
		p.Muxer, p.Format = "mov", "mov"
	case ".mkv":
		p.Muxer, p.Format = "matroska", "matroska"
	case ".avi", ".divx":
		p.Muxer, p.Format, p.VideoEncoder, p.VideoCodec, p.AudioEncoder, p.AudioCodec = "avi", "avi", "mpeg4", "mpeg4", "libmp3lame", "mp3"
	case ".wmv", ".asf":
		p.Muxer, p.Format, p.VideoEncoder, p.VideoCodec, p.AudioEncoder, p.AudioCodec = "asf", "asf", "msmpeg4", "msmpeg4v3", "wmav2", "wmav2"
	case ".flv":
		p.Muxer, p.Format, p.VideoEncoder, p.VideoCodec, p.AudioEncoder, p.AudioCodec = "flv", "flv", "flv", "flv1", "libmp3lame", "mp3"
	case ".f4v":
		p.Muxer, p.Format = "f4v", "mov"
	case ".webm":
		p.Muxer, p.Format, p.VideoEncoder, p.VideoCodec, p.AudioEncoder, p.AudioCodec = "webm", "webm", "libvpx-vp9", "vp9", "libopus", "opus"
	case ".ts":
		p.Muxer, p.Format = "mpegts", "mpegts"
	case ".3gp":
		p.Muxer, p.Format = "3gp", "mov"
	case ".mpg", ".mpeg":
		p.Muxer, p.Format, p.VideoEncoder, p.VideoCodec, p.AudioEncoder, p.AudioCodec = "mpeg", "mpeg", "mpeg1video", "mpeg1video", "mp2", "mp2"
	case ".vob":
		p.Muxer, p.Format, p.VideoEncoder, p.VideoCodec, p.AudioEncoder, p.AudioCodec = "vob", "mpeg", "mpeg2video", "mpeg2video", "mp2", "mp2"
	case ".rm", ".rmvb":
		p.Muxer, p.Format, p.VideoEncoder, p.VideoCodec, p.AudioEncoder, p.AudioCodec = "rm", "rm", "rv10", "rv10", "ac3", "ac3"
	default:
		return nil, editError("container_profile_unsupported", "不支持保持此来源容器的裁剪输出")
	}
	if !oneOf(p.Format, strings.Split(format, ",")...) {
		return nil, editError("source_container_mismatch", "文件扩展名与实际容器不一致")
	}
	return p, nil
}

// fullCropMayCopyAudio lists native audio codecs in the export compatibility
// profiles. An input accepted by a demuxer is not necessarily suitable for
// the corresponding output profile. For example, AVI AAC is readable by
// FFmpeg, but is outside the legacy AVI playback profile.
func fullCropMayCopyAudio(p *editContainerProfile, stream editStream) bool {
	if stream.Channels < 1 || stream.Channels > 8 {
		return false
	}
	codec := stream.Codec
	if codec == p.AudioCodec {
		return true
	}
	switch p.Muxer {
	case "mp4":
		return oneOf(codec, "mp3", "ac3", "eac3", "alac")
	case "mov":
		return oneOf(codec, "mp3", "ac3", "eac3", "alac", "pcm_s16le", "pcm_s16be", "pcm_s24le", "pcm_s24be", "pcm_s32le", "pcm_s32be", "pcm_f32le", "pcm_f32be", "pcm_f64le", "pcm_f64be", "adpcm_ima_qt")
	case "matroska":
		return oneOf(codec, "mp2", "mp3", "ac3", "eac3", "dts", "truehd", "flac", "alac", "opus", "vorbis", "wavpack", "pcm_s16le", "pcm_s16be", "pcm_s24le", "pcm_s24be", "pcm_s32le", "pcm_s32be", "pcm_f32le", "pcm_f64le")
	case "avi":
		return oneOf(codec, "mp2", "ac3", "pcm_u8", "pcm_s16le", "pcm_s24le", "pcm_s32le")
	case "asf":
		return oneOf(codec, "wmav1", "wmapro", "wmalossless", "mp3")
	case "flv":
		return codec == "aac"
	case "webm":
		return codec == "vorbis"
	case "mpegts":
		return oneOf(codec, "mp2", "mp3", "ac3", "eac3", "aac_latm")
	case "3gp":
		return oneOf(codec, "amr_nb", "amr_wb")
	case "vob":
		return oneOf(codec, "ac3", "pcm_dvd")
	}
	return false
}

func fullCropAudioSupported(p *editContainerProfile, source editStream) bool {
	return source.Channels >= 1 && source.Channels <= 8 &&
		(fullCropMayCopyAudio(p, source) || source.Channels <= 2 || !oneOf(p.AudioEncoder, "libmp3lame", "wmav2", "mp2"))
}

// fullCropCopiedAudioBitRate uses the reported rate or a PCM rate
// derived from its stored sample width. Unknown compressed rates are handled
// as a source-wide size bound by configureFullCrop.
func fullCropCopiedAudioBitRate(stream editStream) int64 {
	rate := stream.BitRate
	if stream.SampleRate <= 0 || stream.Channels <= 0 {
		return rate
	}
	width := 0
	switch {
	case stream.Codec == "pcm_u8":
		width = 8
	case strings.HasPrefix(stream.Codec, "pcm_s16"):
		width = 16
	case strings.HasPrefix(stream.Codec, "pcm_s24"):
		width = 24
	case strings.HasPrefix(stream.Codec, "pcm_s32"), strings.HasPrefix(stream.Codec, "pcm_f32"):
		width = 32
	case strings.HasPrefix(stream.Codec, "pcm_f64"):
		width = 64
	}
	return max(rate, int64(stream.SampleRate)*int64(stream.Channels)*int64(width))
}

func (s *VideoEditService) configureFullCrop(plan *editPlan, sc *editSourceCtx) error {
	profile, err := fullCropContainerProfile(sc.path, sc.probe.FormatName)
	if err != nil {
		return err
	}
	identity, err := s.inspectEditContainer(sc.path, sc.probe.FormatName)
	if err != nil || identity != editProfileIdentity(profile) {
		return editError("source_container_mismatch", "文件扩展名与真实容器标识不一致")
	}
	profile.Identity = identity
	if profile.VideoEncoder == "" {
		// 只有这些容器允许沿用现有HEVC/HDR规格，其余容器明确使用H.264。
		hevc := oneOf(profile.Muxer, "mp4", "mov", "matroska") && plan.Video.Codec == "hevc"
		profile.VideoEncoder, profile.VideoCodec = editVideoEncoderName(s.goos, hevc), "h264"
		if hevc {
			profile.VideoCodec = "hevc"
		}
	}
	preservedPixFmt := ""
	if editSourceHighDepth(sc.probe.Video) {
		switch {
		case profile.VideoCodec == "vp9" && oneOf(sc.probe.Video.PixFmt, "yuv420p10le", "yuv422p10le", "yuv440p10le", "yuv444p10le", "gbrp10le", "yuv420p12le", "yuv422p12le", "yuv440p12le", "yuv444p12le", "gbrp12le"):
			preservedPixFmt = sc.probe.Video.PixFmt
		case profile.VideoCodec == "hevc" && plan.Video.Main10 && !editSourceAbove10Bit(sc.probe.Video):
			preservedPixFmt = plan.Video.PixFmt
		case profile.VideoEncoder == "libx264" && oneOf(sc.probe.Video.PixFmt, "yuv420p10le", "yuv422p10le", "yuv444p10le"):
			preservedPixFmt = sc.probe.Video.PixFmt
		default:
			return editError("container_profile_incompatible", "当前原容器编码配置不能保留来源位深")
		}
	}
	if editStreamHDR(sc.probe.Video) && !oneOf(profile.VideoCodec, "hevc", "vp9") {
		return editError("container_profile_incompatible", "原容器编码方案不能保留HDR，请保留原片")
	}
	if len(plan.Attachments) > 0 && profile.Muxer != "matroska" {
		return editError("container_profile_incompatible", "原容器不能保留字体附件")
	}
	for i := range plan.Subtitles {
		track := &plan.Subtitles[i]
		switch profile.Muxer {
		case "matroska":
		case "mp4", "mov", "3gp", "f4v":
			if track.Image {
				return editError("container_profile_incompatible", "原容器不能保留图形字幕")
			}
			track.Codec = "mov_text"
		case "webm":
			if track.Image {
				return editError("container_profile_incompatible", "WebM不能保留图形字幕")
			}
			track.Codec = "webvtt"
		default:
			return editError("container_profile_incompatible", "原容器编码方案无法保留选定字幕，请明确调整字幕轨道")
		}
	}
	audioBits := float64(0)
	unknownCopiedAudio := false
	if len(plan.Audio) > 0 {
		if len(plan.Segments) != 1 || len(plan.Segments[0].Audio) != len(plan.Audio) {
			return editError("container_profile_incompatible", "原容器的音轨映射不完整")
		}
		sourceAudio := make(map[int]editStream, len(sc.probe.Audio))
		for _, stream := range sc.probe.Audio {
			sourceAudio[stream.Index] = stream
		}
		for i, ref := range plan.Segments[0].Audio {
			source := sourceAudio[ref.Stream]
			if ref.Fill != "" || ref.Source != plan.Segments[0].VideoSource || !fullCropAudioSupported(profile, source) {
				return editError("container_profile_incompatible", "原容器无法保留第%d条选定音轨的声道数", i+1)
			}
			track := &plan.Audio[i]
			track.CopyAudio = fullCropMayCopyAudio(profile, source)
			track.Codec = profile.AudioCodec
			if track.CopyAudio {
				track.Codec = source.Codec
				if rate := fullCropCopiedAudioBitRate(source); rate > 0 {
					audioBits += float64(rate)
				} else {
					unknownCopiedAudio = true
				}
			} else {
				audioBits += float64(profile.AudioBitRate)
			}
		}
	}
	for i := range plan.Sources {
		plan.Sources[i].UpdatedAt = sc.video.UpdatedAt
		plan.Sources[i].RecordSize = sc.video.Size
	}
	plan.ContainerProfile, plan.PublishMode = profile, "replace_source"
	plan.Video.Encoder, plan.Video.Codec = profile.VideoEncoder, profile.VideoCodec
	if profile.VideoCodec != "hevc" {
		plan.Video.Main10 = false
		plan.Video.PixFmt = "yuv420p"
	}
	if preservedPixFmt != "" {
		plan.Video.PixFmt = preservedPixFmt
	}
	plan.Container = profile.Muxer
	// 目标基本名和扩展名逐字保留；发布由替换分支负责，不能选 (2) 名称。
	plan.OutputExt = filepath.Ext(sc.path)
	plan.OutputBase = strings.TrimSuffix(filepath.Base(sc.path), plan.OutputExt)
	plan.OutputDir = filepath.Dir(sc.path)
	plan.Sidecar = editPlanSidecar{Mode: "none", CopySource: -1}
	seconds := float64(plan.DurationMS) / 1000
	estimated := (float64(plan.Video.BitRate) + audioBits) / 8 * seconds
	if unknownCopiedAudio {
		// These full-file tracks together cannot exceed the original file.
		// Use a single source-wide allowance, not an assumed AAC bitrate.
		estimated += max(float64(sc.size), float64(sc.probe.BitRate)/8*seconds)
	}
	plan.EstimatedBytes = int64(estimated*1.05) + 1<<20
	plan.WorkBytes = plan.EstimatedBytes
	return nil
}

// fullCropArgs 是短样本和正式编码共同的参数生成器，永不依赖后缀猜容器。
func fullCropArgs(plan editPlan, target string) []string {
	segment := plan.Segments[0]
	profile := plan.ContainerProfile
	args := []string{"-nostdin", "-hide_banner", "-v", "error", "-y", "-threads", "2", "-filter_threads", "1", "-i", plan.Sources[segment.VideoSource].Path, "-map", fmt.Sprintf("0:%d", segment.VideoStream)}
	for _, ref := range segment.Audio {
		args = append(args, "-map", fmt.Sprintf("0:%d", ref.Stream))
	}
	for _, ref := range segment.Subtitles {
		args = append(args, "-map", fmt.Sprintf("0:%d", ref.Stream))
	}
	for _, attachment := range plan.Attachments {
		args = append(args, "-map", fmt.Sprintf("0:%d", attachment.Stream))
	}
	args = append(args, "-filter:v", editVideoFilter(plan.Video))
	args = append(args, editVideoEncodeArgs(plan.Video)...)
	if profile.VideoEncoder == "libvpx-vp9" {
		args = append(args, "-deadline", "good", "-cpu-used", "4", "-threads", "2")
	}
	if profile.VideoCodec == "hevc" && oneOf(profile.Muxer, "mov", "mp4") {
		args = append(args, "-tag:v", "hvc1")
	}
	for i, track := range plan.Audio {
		codec := fmt.Sprintf("-c:a:%d", i)
		if track.CopyAudio {
			args = append(args, codec, "copy")
		} else {
			args = append(args, codec, profile.AudioEncoder, fmt.Sprintf("-ar:a:%d", i), strconv.Itoa(profile.SampleRate),
				fmt.Sprintf("-b:a:%d", i), strconv.Itoa(profile.AudioBitRate), fmt.Sprintf("-ac:a:%d", i), strconv.Itoa(track.Channels))
		}
		args = append(args, editStreamMetadata("a", i, track)...)
	}
	for i, track := range plan.Subtitles {
		codec := track.Codec
		if track.Image {
			codec = "copy"
		}
		args = append(args, fmt.Sprintf("-c:s:%d", i), codec)
		args = append(args, editStreamMetadata("s", i, track)...)
	}
	if len(plan.Attachments) > 0 {
		args = append(args, "-c:t", "copy")
		for i, a := range plan.Attachments {
			args = append(args, fmt.Sprintf("-metadata:s:t:%d", i), "filename="+a.Filename, fmt.Sprintf("-metadata:s:t:%d", i), "mimetype="+a.MimeType)
		}
	}
	return append(args, "-frames:v", strconv.FormatInt(editFrameCount(plan.DurationMS, plan.Video.FrameRate), 10), "-t", editSeconds(plan.DurationMS), "-map_metadata", "-1", "-map_chapters", "-1", "-progress", "pipe:1", "-nostats", "-f", profile.Muxer, target)
}

// fullCropSamplePlan 只在短样本中省略明确晚于取样窗口的音轨。部分容器不会写入没有音频包的轨道，
// 不能把这种短样本的缺轨当成整片无法导出；正式编码和发布校验仍要求保留所有选中的音轨。
func fullCropSamplePlan(plan editPlan, source editProbe) editPlan {
	plan.DurationMS = min(plan.DurationMS, 1000)
	if len(plan.Audio) == 0 {
		return plan
	}
	segment := plan.Segments[0]
	late := map[int]bool{}
	for _, stream := range source.Audio {
		if relativeStart := stream.StartSeconds - source.StartSeconds; relativeStart >= float64(plan.DurationMS)/1000 {
			late[stream.Index] = true
		}
	}
	if len(late) == 0 {
		return plan
	}
	tracks := make([]editPlanTrack, 0, len(plan.Audio))
	refs := make([]editPlanRef, 0, len(segment.Audio))
	for i, ref := range segment.Audio {
		if !late[ref.Stream] {
			tracks = append(tracks, plan.Audio[i])
			refs = append(refs, ref)
		}
	}
	segment.Audio = refs
	plan.Audio = tracks
	plan.Segments = []editPlanSegment{segment}
	return plan
}

func (s *VideoEditService) sampleFullCrop(ctx context.Context, plan editPlan, source editProbe) error {
	if plan.ContainerProfile == nil {
		return nil
	}
	if len(plan.Segments) != 1 || len(plan.Sources) != 1 {
		return editError("container_profile_incompatible", "整片裁剪计划不是单来源")
	}
	dir, err := os.MkdirTemp("", "cineinsight-crop-sample-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	plan = fullCropSamplePlan(plan, source)
	ffmpeg, err := s.findFFmpeg()
	if err != nil {
		return editError("encoder_unavailable", "找不到ffmpeg")
	}
	sampleCtx, cancel := context.WithTimeout(ctx, videoEditProbeTimeout)
	defer cancel()
	target := filepath.Join(dir, "final.part")
	tail, err := s.runProgress(sampleCtx, ffmpeg, fullCropArgs(plan, target), func(int64) {})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return editError("container_profile_incompatible", "原容器短样本编码失败：%s", scrubEditPaths(tail, plan.Sources[0].Path, dir))
	}
	if err = s.verifyEditOutput(sampleCtx, plan, target); err != nil {
		return editError("container_profile_incompatible", "原容器短样本校验失败：%s", err.Error())
	}
	return editSourcesUnchanged(plan)
}

func (s *VideoEditService) encodeFullCrop(ctx context.Context, ffmpeg string, plan editPlan, dir string, reporter *editProgressReporter) (string, error) {
	target := filepath.Join(dir, "final.part")
	tail, err := s.runProgress(ctx, ffmpeg, fullCropArgs(plan, target), func(ms int64) {
		reporter.report(ctx, "encode", 0.95*float64(min(ms, plan.DurationMS))/float64(max(plan.DurationMS, 1)), false)
	})
	if err != nil {
		return "", editFFmpegError(ctx, err, tail, "encode_failed")
	}
	return target, nil
}

func (s *VideoEditService) verifyFullCropProfile(plan editPlan, probe editProbe, path string) error {
	p := plan.ContainerProfile
	identity, err := s.inspectEditContainer(path, probe.FormatName)
	if err != nil || identity != p.Identity {
		return editError("verify_failed", "成品真实容器标识与冻结计划不一致")
	}
	if probe.Video == nil || probe.Video.Codec != p.VideoCodec || !oneOf(p.Format, strings.Split(probe.FormatName, ",")...) {
		return editError("verify_failed", "成品容器或视频编码与冻结计划不一致")
	}
	if max(probe.Video.BitDepth, editPixelDepth(probe.Video.PixFmt)) < editPixelDepth(plan.Video.PixFmt) {
		return editError("verify_failed", "成品位深低于冻结计划")
	}
	for i, track := range probe.Audio {
		if i >= len(plan.Audio) {
			break // stream count is verified by verifyEditOutput
		}
		planned := plan.Audio[i]
		expected := p.AudioCodec // 旧计划的 Codec 不是原容器实际输出的编码。
		if planned.CopyAudio {
			expected = planned.Codec
		}
		if track.Codec != expected {
			return editError("verify_failed", "成品第%d条音轨编码与冻结计划不一致", i+1)
		}
		if planned.Channels > 0 && track.Channels != planned.Channels {
			return editError("verify_failed", "成品第%d条音轨声道数与冻结计划不一致", i+1)
		}
	}
	return nil
}

// fullCropTrackConflicts 暴露现有轨道选择UI可执行的整轨不导出选项；未选择前不改计划。
func (s *VideoEditService) fullCropTrackConflicts(pre *EditPreflight, plan editPlan, sc *editSourceCtx, audio, subtitles editTrackPlan) {
	p, err := fullCropContainerProfile(sc.path, sc.probe.FormatName)
	if err != nil {
		return
	}
	conflict := func(kind string, index int, reason string) {
		pre.Conflicts = append(pre.Conflicts, EditTrackConflict{ScopeVideoID: sc.video.ID, Seq: plan.Seq, Kind: kind, OutputIndex: index, VideoID: sc.video.ID, Reason: reason, Candidates: []EditStreamInfo{}, AllowedFills: []string{EditTrackChoiceDrop}})
		pre.addError("track_conflict", fmt.Sprintf("container_track:%d:%s:%d", plan.Seq, kind, index), sc.video.ID, plan.Seq, "%s第%d条%s", editTrackLabel(kind), index+1, reason)
	}
	for _, k := range subtitles.kept {
		track := subtitles.reference[k]
		image := editImageSubtitle(track.Codec)
		if p.Muxer != "matroska" && (image || !oneOf(p.Muxer, "mp4", "mov", "3gp", "f4v", "webm")) {
			conflict(editTrackSubtitle, k, "不受原容器支持，请明确选择整轨不导出")
		}
	}
	for _, k := range audio.kept {
		track := audio.reference[k]
		if !fullCropAudioSupported(p, track) {
			conflict(editTrackAudio, k, "声道数不受原容器编码器支持，请明确选择整轨不导出")
		}
	}
}
