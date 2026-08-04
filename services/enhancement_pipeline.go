package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

const enhancementChunkFrames = 120

// enhancementProbeInfo 是超分输入闭集校验需要的探测结果。
type enhancementProbeInfo struct {
	VideoStreams     int
	AudioStreams     int
	SubtitleStreams  int
	Width            int
	Height           int
	PixelFormat      string
	FieldOrder       string
	ColorTransfer    string
	ColorPrimaries   string
	BitsPerRawSample int
	AvgFrameRate     string
	RealFrameRate    string
	Duration         float64
	FPS              float64
}

type enhancementSegment struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Frames int64  `json:"frames"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type enhancementSegmentManifest struct {
	Segments []enhancementSegment `json:"segments"`
}

func (s *EnhancementService) probeEnhancementSource(ctx context.Context, path string) (enhancementProbeInfo, error) {
	info := enhancementProbeInfo{}
	ffprobe, err := s.findFFprobe()
	if err != nil {
		return info, fmt.Errorf("unsupported_input: 未找到 ffprobe: %v", err)
	}
	stdout, stderrTail, err := s.runCommandOutput(ctx, ffprobe, []string{
		"-v", "error", "-show_format", "-show_streams", "-print_format", "json", path,
	})
	if err != nil {
		return info, fmt.Errorf("decode_failed: ffprobe 探测失败: %v: %s", err, stderrTail)
	}
	return parseEnhancementProbe([]byte(stdout))
}

func parseEnhancementProbe(raw []byte) (enhancementProbeInfo, error) {
	info := enhancementProbeInfo{}
	var payload struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType        string `json:"codec_type"`
			Width            int    `json:"width"`
			Height           int    `json:"height"`
			PixFmt           string `json:"pix_fmt"`
			FieldOrder       string `json:"field_order"`
			ColorTransfer    string `json:"color_transfer"`
			ColorPrimaries   string `json:"color_primaries"`
			AvgFrameRate     string `json:"avg_frame_rate"`
			RFrameRate       string `json:"r_frame_rate"`
			Disposition      map[string]int
			BitsPerRawSample string `json:"bits_per_raw_sample"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return info, fmt.Errorf("decode_failed: ffprobe 输出不可解析: %v", err)
	}
	info.Duration, _ = strconv.ParseFloat(payload.Format.Duration, 64)
	for _, stream := range payload.Streams {
		switch stream.CodecType {
		case "video":
			if stream.Disposition["attached_pic"] == 1 {
				continue
			}
			info.VideoStreams++
			if info.VideoStreams == 1 {
				info.Width, info.Height = stream.Width, stream.Height
				info.PixelFormat = stream.PixFmt
				info.FieldOrder = stream.FieldOrder
				info.ColorTransfer = stream.ColorTransfer
				info.ColorPrimaries = stream.ColorPrimaries
				if bits, err := strconv.Atoi(stream.BitsPerRawSample); err == nil {
					info.BitsPerRawSample = bits
				}
				info.AvgFrameRate = stream.AvgFrameRate
				info.RealFrameRate = stream.RFrameRate
			}
		case "audio":
			info.AudioStreams++
		case "subtitle":
			info.SubtitleStreams++
		}
	}
	info.FPS = parseFrameRate(info.RealFrameRate)
	return info, nil
}

func parseFrameRate(value string) float64 {
	parts := strings.SplitN(value, "/", 2)
	if len(parts) != 2 {
		rate, _ := strconv.ParseFloat(value, 64)
		return rate
	}
	numerator, err1 := strconv.ParseFloat(parts[0], 64)
	denominator, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil || denominator == 0 {
		return 0
	}
	return numerator / denominator
}

// validateEnhancementInput 执行 P-012 §2 的输入闭集校验；任何不满足都在
// 创建任务前拒绝，不做隐式转换。
func validateEnhancementInput(info enhancementProbeInfo) error {
	if info.VideoStreams != 1 {
		return fmt.Errorf("unsupported_input: 需要恰好一个主视频流（实际 %d）", info.VideoStreams)
	}
	if info.Width <= 0 || info.Height <= 0 {
		return errors.New("unsupported_input: 视频宽高无效")
	}
	long, short := info.Width, info.Height
	if short > long {
		long, short = short, long
	}
	if long > 1920 || short > 1080 {
		return fmt.Errorf("unsupported_input: 首版只支持不超过 1920×1080 的输入（实际 %d×%d）", info.Width, info.Height)
	}
	if strings.Contains(info.PixelFormat, "10le") || strings.Contains(info.PixelFormat, "12le") || strings.Contains(info.PixelFormat, "16le") || strings.Contains(info.PixelFormat, "10be") {
		return fmt.Errorf("unsupported_input: 首版只支持 8-bit 输入（实际 %s）", info.PixelFormat)
	}
	transfer := strings.ToLower(info.ColorTransfer)
	if transfer == "smpte2084" || transfer == "arib-std-b67" {
		return errors.New("unsupported_input: 首版不支持 HDR 输入")
	}
	if strings.HasPrefix(strings.ToLower(info.ColorPrimaries), "bt2020") {
		return errors.New("unsupported_input: 首版不支持 BT.2020 色域输入")
	}
	if info.BitsPerRawSample > 8 {
		return fmt.Errorf("unsupported_input: 首版只支持 8-bit 输入（实际 %d-bit）", info.BitsPerRawSample)
	}
	switch strings.ToLower(info.FieldOrder) {
	case "", "progressive", "unknown":
	default:
		return fmt.Errorf("unsupported_input: 首版只支持逐行扫描（实际 %s）", info.FieldOrder)
	}
	if info.FPS <= 0 {
		return errors.New("unsupported_input: 无法确定帧率")
	}
	if avg := parseFrameRate(info.AvgFrameRate); avg > 0 && math.Abs(avg-info.FPS)/info.FPS > 0.001 {
		return errors.New("unsupported_input: 首版只支持恒定帧率（检测到可变帧率）")
	}
	if info.Duration <= 0 {
		return errors.New("unsupported_input: 无法确定视频时长")
	}
	return nil
}

// interruptionResult 区分两种 ctx 取消：用户取消（数据库为 cancel_requested）
// 走终态清理并返回 nil；生命周期停止保持任务可恢复并向上返回 ctx 错误。
func (s *EnhancementService) interruptionResult(ctx context.Context, task models.VideoEnhancementTask) error {
	if s.taskCancelRequested(task.ID) {
		s.finishCancelled(task)
		return nil
	}
	return ctx.Err()
}

// processTask 执行单个任务的完整流水线。返回 error 仅表示生命周期中断
// （任务保持可恢复状态）；业务失败在内部写终态并返回 nil。
func (s *EnhancementService) processTask(ctx context.Context, task models.VideoEnhancementTask) error {
	video := task.Video
	spec, ok := EnhancementProfiles[task.Profile]
	if !ok {
		s.failTask(task.ID, "unsupported_input", "未知配置")
		return nil
	}
	logEnhancement("task=%d start profile=%s runtime=%s", task.ID, task.Profile, task.RuntimeVersion)
	s.updatePhase(task.ID, models.EnhancementPhasePreflight)

	// —— preflight：源稳定性、SHA-256、闭集校验、帧数 ——
	info, err := os.Lstat(video.Path)
	if err != nil || !info.Mode().IsRegular() {
		s.failTask(task.ID, "source_changed", "源文件不可读")
		return nil
	}
	if info.Size() != task.SourceSize || info.ModTime().UnixNano() != task.SourceModTimeNS {
		s.failTask(task.ID, "source_changed", "源文件在任务创建后被修改")
		s.cleanupTaskWorkdir(task)
		return nil
	}
	digest, err := sha256File(video.Path)
	if err != nil {
		s.failTask(task.ID, "source_changed", err.Error())
		return nil
	}
	if ctx.Err() != nil {
		return s.interruptionResult(ctx, task)
	}
	if task.SourceSHA256 == "" {
		if err := database.DB.Model(&models.VideoEnhancementTask{}).
			Where("id = ?", task.ID).Update("source_sha256", digest).Error; err != nil {
			s.failTask(task.ID, "publish_failed", err.Error())
			return nil
		}
		task.SourceSHA256 = digest
	} else if task.SourceSHA256 != digest {
		s.failTask(task.ID, "source_changed", "源文件哈希与任务创建时不一致")
		s.cleanupTaskWorkdir(task)
		return nil
	}

	probeInfo, err := s.probeEnhancementSource(ctx, video.Path)
	if err != nil {
		if ctx.Err() != nil {
			return s.interruptionResult(ctx, task)
		}
		s.failTask(task.ID, enhancementErrorCode(err, "decode_failed"), err.Error())
		return nil
	}
	if err := validateEnhancementInput(probeInfo); err != nil {
		s.failTask(task.ID, "unsupported_input", err.Error())
		return nil
	}
	// 用视频流 packet 计数取精确总帧数（容器时长×fps 常有 ±1–2 帧误差，
	// 会让最终 verify 的帧数一致性检查在长任务末尾误报）。
	totalFrames, err := s.countVideoPackets(ctx, video.Path)
	if err != nil || totalFrames <= 0 {
		if ctx.Err() != nil {
			return s.interruptionResult(ctx, task)
		}
		s.failTask(task.ID, "unsupported_input", fmt.Sprintf("无法确定总帧数: %v", err))
		return nil
	}
	if err := database.DB.Model(&models.VideoEnhancementTask{}).
		Where("id = ?", task.ID).Update("total_frames", totalFrames).Error; err != nil {
		s.failTask(task.ID, "publish_failed", err.Error())
		return nil
	}
	task.TotalFrames = totalFrames

	workdir := enhancementWorkdir(task)
	if err := os.MkdirAll(workdir, 0700); err != nil {
		s.failTask(task.ID, "publish_failed", err.Error())
		return nil
	}

	manifest, err := loadEnhancementManifest(workdir)
	if err != nil {
		manifest = enhancementSegmentManifest{}
	}
	committed, manifestErr := manifestCommittedFrames(workdir, &manifest)
	if manifestErr != nil {
		s.failTask(task.ID, "verify_failed", manifestErr.Error())
		return nil
	}
	_ = database.DB.Model(&models.VideoEnhancementTask{}).
		Where("id = ?", task.ID).Update("committed_frames", committed).Error

	ffmpeg, err := s.findFFmpeg()
	if err != nil {
		s.failTask(task.ID, "runtime_unavailable", err.Error())
		return nil
	}

	// —— 分块流水线：extract → enhance → encode，批后立即清理帧 ——
	for committed < totalFrames {
		if ctx.Err() != nil {
			return s.interruptionResult(ctx, task)
		}
		if s.taskCancelRequested(task.ID) {
			s.finishCancelled(task)
			return nil
		}
		if err := s.ensureDiskFloor(video.Path, task.SourceSize, probeInfo.Width, probeInfo.Height); err != nil {
			s.failTask(task.ID, "disk_insufficient", err.Error())
			s.cleanupTaskWorkdir(task)
			return nil
		}
		if stable, message := enhancementSourceStable(video.Path, task); !stable {
			s.failTask(task.ID, "source_changed", message)
			s.cleanupTaskWorkdir(task)
			return nil
		}
		frames := totalFrames - committed
		if frames > enhancementChunkFrames {
			frames = enhancementChunkFrames
		}
		segmentIndex := len(manifest.Segments)
		segmentName := fmt.Sprintf("seg-%05d.cispart", segmentIndex)
		if err := s.runChunk(ctx, ffmpeg, spec, task, video.Path, workdir, segmentName, probeInfo, committed, frames); err != nil {
			if ctx.Err() != nil {
				return s.interruptionResult(ctx, task)
			}
			if s.taskCancelRequested(task.ID) {
				s.finishCancelled(task)
				return nil
			}
			s.failTask(task.ID, enhancementErrorCode(err, "inference_failed"), err.Error())
			s.cleanupTaskWorkdir(task)
			return nil
		}
		segmentPath := filepath.Join(workdir, segmentName)
		segmentInfo, statErr := os.Stat(segmentPath)
		segmentDigest, hashErr := sha256File(segmentPath)
		if statErr != nil || hashErr != nil {
			s.failTask(task.ID, "encode_failed", "分段校验点生成失败")
			return nil
		}
		manifest.Segments = append(manifest.Segments, enhancementSegment{
			Index: segmentIndex, Name: segmentName, Frames: frames,
			Size: segmentInfo.Size(), SHA256: segmentDigest,
		})
		if err := saveEnhancementManifest(workdir, manifest); err != nil {
			s.failTask(task.ID, "publish_failed", err.Error())
			s.cleanupTaskWorkdir(task)
			return nil
		}
		committed += frames
		_ = database.DB.Model(&models.VideoEnhancementTask{}).
			Where("id = ?", task.ID).Update("committed_frames", committed).Error
		s.emitTaskByID(task.ID)
	}

	// —— 合并 + 校验 + 原子发布 ——
	s.updatePhase(task.ID, models.EnhancementPhaseEncode)
	stagingPath := filepath.Join(workdir, "final.cispart")
	if err := s.concatSegments(ctx, ffmpeg, video.Path, workdir, manifest, stagingPath, probeInfo); err != nil {
		if ctx.Err() != nil {
			return s.interruptionResult(ctx, task)
		}
		s.failTask(task.ID, "encode_failed", err.Error())
		return nil
	}
	s.updatePhase(task.ID, models.EnhancementPhaseVerify)
	if err := s.verifyStagedOutput(ctx, task, stagingPath, probeInfo); err != nil {
		if ctx.Err() != nil {
			return s.interruptionResult(ctx, task)
		}
		s.failTask(task.ID, enhancementErrorCode(err, "verify_failed"), err.Error())
		s.cleanupTaskWorkdir(task)
		return nil
	}
	if currentDigest, err := sha256File(video.Path); err != nil || currentDigest != task.SourceSHA256 {
		s.failTask(task.ID, "source_changed", "发布前源文件哈希校验失败")
		s.cleanupTaskWorkdir(task)
		return nil
	}
	if s.taskCancelRequested(task.ID) {
		s.finishCancelled(task)
		return nil
	}

	s.mu.Lock()
	s.publishing = true
	s.mu.Unlock()
	s.updatePhase(task.ID, models.EnhancementPhasePublish)
	if err := s.publishOutput(ctx, task, video, stagingPath); err != nil {
		s.failTask(task.ID, "publish_failed", err.Error())
		return nil
	}
	s.cleanupTaskWorkdir(task)
	s.emitTaskByID(task.ID)
	logEnhancement("task=%d completed frames=%d", task.ID, totalFrames)
	return nil
}

func (s *EnhancementService) finishCancelled(task models.VideoEnhancementTask) {
	now := s.now()
	_ = database.DB.Model(&models.VideoEnhancementTask{}).
		Where("id = ? AND status IN ?", task.ID, enhancementActiveStatuses()).
		Updates(map[string]any{"status": models.EnhancementStatusCancelled, "error_code": "cancelled", "finished_at": &now}).Error
	s.cleanupTaskWorkdir(task)
	s.emitTaskByID(task.ID)
	logEnhancement("task=%d cancelled", task.ID)
}

func enhancementSourceStable(path string, task models.VideoEnhancementTask) (bool, string) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, "源文件不可访问"
	}
	if info.Size() != task.SourceSize || info.ModTime().UnixNano() != task.SourceModTimeNS {
		return false, "源文件在处理期间被修改"
	}
	return true, ""
}

// runChunk 处理一批固定帧数：无损 PNG 抽帧 → sidecar 超分 → HEVC 分段。
func (s *EnhancementService) runChunk(ctx context.Context, ffmpeg string, spec EnhancementProfileSpec, task models.VideoEnhancementTask, sourcePath, workdir, segmentName string, info enhancementProbeInfo, startFrame, frames int64) error {
	inDir := filepath.Join(workdir, "frames-in")
	outDir := filepath.Join(workdir, "frames-out")
	for _, dir := range []string{inDir, outDir} {
		_ = os.RemoveAll(dir)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("publish_failed: %v", err)
		}
	}
	defer os.RemoveAll(inDir)
	defer os.RemoveAll(outDir)

	s.updatePhase(task.ID, models.EnhancementPhaseExtract)
	// 目标定到批首帧前半帧处：输入侧 -ss 输出 pts >= 目标 的帧，半帧偏移
	// 使浮点微秒舍入不会丢掉边界帧（非整数 fps 也稳定）。
	startSeconds := (float64(startFrame) - 0.5) / info.FPS
	if startSeconds < 0 {
		startSeconds = 0
	}
	if tail, err := s.runCommand(ctx, ffmpeg, []string{
		"-v", "error", "-ss", strconv.FormatFloat(startSeconds, 'f', 6, 64),
		"-i", sourcePath, "-frames:v", strconv.FormatInt(frames, 10),
		"-fps_mode", "passthrough", "-f", "image2",
		filepath.Join(inDir, "%06d.png"),
	}); err != nil {
		return fmt.Errorf("decode_failed: 抽帧失败: %v: %s", err, tail)
	}
	extracted, err := countFilesWithSuffix(inDir, ".png")
	if err != nil || extracted != frames {
		return fmt.Errorf("decode_failed: 抽帧数量不完整（%d/%d）", extracted, frames)
	}

	s.updatePhase(task.ID, models.EnhancementPhaseEnhance)
	sidecarArgs := append([]string{"-i", inDir, "-o", outDir, "-n", spec.ModelName, "-m", s.capability.ModelDir}, spec.ExtraArgs...)
	if tail, err := s.runCommand(ctx, s.capability.BinaryPath, sidecarArgs); err != nil {
		return fmt.Errorf("inference_failed: 超分推理失败: %v: %s", err, tail)
	}
	enhanced, err := countFilesWithSuffix(outDir, ".png")
	if err != nil || enhanced != extracted {
		return fmt.Errorf("inference_failed: 输出帧数不完整（%d/%d）", enhanced, extracted)
	}

	s.updatePhase(task.ID, models.EnhancementPhaseEncode)
	segmentPath := filepath.Join(workdir, segmentName)
	if tail, err := s.runCommand(ctx, ffmpeg, []string{
		"-v", "error", "-framerate", info.RealFrameRate,
		"-i", filepath.Join(outDir, "%06d.png"),
		"-c:v", "libx265", "-preset", "medium", "-crf", "18", "-pix_fmt", "yuv420p",
		"-f", "matroska", "-y", segmentPath,
	}); err != nil {
		return fmt.Errorf("encode_failed: 分段编码失败: %v: %s", err, tail)
	}
	if info, err := os.Stat(segmentPath); err != nil || info.Size() == 0 {
		return errors.New("encode_failed: 分段输出为空")
	}
	return nil
}

func (s *EnhancementService) concatSegments(ctx context.Context, ffmpeg, sourcePath, workdir string, manifest enhancementSegmentManifest, stagingPath string, info enhancementProbeInfo) error {
	listPath := filepath.Join(workdir, "segments.txt")
	var builder strings.Builder
	for _, segment := range manifest.Segments {
		builder.WriteString("file '")
		builder.WriteString(strings.ReplaceAll(filepath.Join(workdir, segment.Name), "'", `'\''`))
		builder.WriteString("'\n")
	}
	if err := os.WriteFile(listPath, []byte(builder.String()), 0644); err != nil {
		return err
	}
	args := []string{
		"-v", "error", "-f", "concat", "-safe", "0", "-i", listPath,
		"-i", sourcePath,
		"-map", "0:v:0",
	}
	if info.AudioStreams > 0 {
		args = append(args, "-map", "1:a", "-c:a", "copy")
	}
	if info.SubtitleStreams > 0 {
		args = append(args, "-map", "1:s", "-c:s", "copy")
	}
	args = append(args,
		"-map_metadata", "1", "-map_chapters", "1",
		"-c:v", "copy", "-f", "matroska", "-y", stagingPath,
	)
	tail, err := s.runCommand(ctx, ffmpeg, args)
	if err != nil {
		return fmt.Errorf("合并分段失败: %v: %s", err, tail)
	}
	return nil
}

// verifyStagedOutput 校验 staging 输出（P-012 §5）。
func (s *EnhancementService) verifyStagedOutput(ctx context.Context, task models.VideoEnhancementTask, stagingPath string, source enhancementProbeInfo) error {
	staged, err := s.probeEnhancementSource(ctx, stagingPath)
	if err != nil {
		return fmt.Errorf("verify_failed: 输出不可探测: %v", err)
	}
	if staged.VideoStreams != 1 {
		return fmt.Errorf("verify_failed: 输出主视频流数量错误（%d）", staged.VideoStreams)
	}
	if staged.Width != source.Width*2 || staged.Height != source.Height*2 {
		return fmt.Errorf("verify_failed: 输出尺寸 %d×%d 不是源的 2×", staged.Width, staged.Height)
	}
	frameDuration := 1.0 / source.FPS
	tolerance := math.Max(frameDuration, 0.1)
	if math.Abs(staged.Duration-source.Duration) > tolerance {
		return fmt.Errorf("verify_failed: 输出时长偏差 %.3fs 超出容差", math.Abs(staged.Duration-source.Duration))
	}
	if staged.AudioStreams != source.AudioStreams || staged.SubtitleStreams != source.SubtitleStreams {
		return fmt.Errorf("verify_failed: 输出音轨/字幕流数量与源不一致")
	}
	packets, err := s.countVideoPackets(ctx, stagingPath)
	if err != nil {
		return fmt.Errorf("verify_failed: %v", err)
	}
	if packets != task.TotalFrames {
		return fmt.Errorf("verify_failed: 输出帧数 %d 与预期 %d 不一致", packets, task.TotalFrames)
	}
	ffmpeg, err := s.findFFmpeg()
	if err != nil {
		return fmt.Errorf("verify_failed: %v", err)
	}
	samples := []float64{0, staged.Duration / 2, math.Max(0, staged.Duration-frameDuration*2)}
	for _, sample := range samples {
		if tail, err := s.runCommand(ctx, ffmpeg, []string{
			"-v", "error", "-ss", strconv.FormatFloat(sample, 'f', 3, 64),
			"-i", stagingPath, "-frames:v", "1", "-f", "null", "-",
		}); err != nil {
			return fmt.Errorf("verify_failed: 采样解码失败 @%.1fs: %v: %s", sample, err, tail)
		}
	}
	return nil
}

func (s *EnhancementService) countVideoPackets(ctx context.Context, path string) (int64, error) {
	ffprobe, err := s.findFFprobe()
	if err != nil {
		return 0, err
	}
	stdout, _, err := s.runCommandOutput(ctx, ffprobe, []string{
		"-v", "error", "-select_streams", "v:0", "-count_packets",
		"-show_entries", "stream=nb_read_packets", "-of", "csv=p=0", path,
	})
	if err != nil {
		return 0, fmt.Errorf("统计输出帧数失败: %v", err)
	}
	value := strings.TrimSpace(stdout)
	packets, err := strconv.ParseInt(strings.TrimSpace(strings.Split(value, "\n")[len(strings.Split(value, "\n"))-1]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("帧数输出不可解析: %q", value)
	}
	return packets, nil
}

// publishOutput 在窄临界区内原子发布：再查冲突 → rename → 单事务建
// videos 记录、自动标签、已确认同源关系并完成任务。事务失败回滚文件。
func (s *EnhancementService) publishOutput(ctx context.Context, task models.VideoEnhancementTask, source models.Video, stagingPath string) error {
	targetPath := filepath.Join(filepath.Dir(source.Path), task.OutputBasename)

	sourceFingerprint, err := sampledFileContentFingerprint(source.Path)
	if err != nil {
		return fmt.Errorf("源指纹计算失败: %v", err)
	}
	outputFingerprint, err := sampledFileContentFingerprint(stagingPath)
	if err != nil {
		return fmt.Errorf("输出指纹计算失败: %v", err)
	}

	release := BeginLibraryMaintenance()
	defer release()

	if _, err := os.Lstat(targetPath); err == nil {
		return fmt.Errorf("output_conflict: 输出路径在发布时已被占用")
	}
	var occupied models.Video
	if err := database.DB.Unscoped().Where("path = ?", targetPath).First(&occupied).Error; err == nil {
		return fmt.Errorf("output_conflict: 输出路径已被片库记录占用")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err := fsyncEnhancementFile(stagingPath); err != nil {
		return fmt.Errorf("发布前 fsync 失败: %v", err)
	}
	if err := os.Rename(stagingPath, targetPath); err != nil {
		return fmt.Errorf("发布 rename 失败: %v", err)
	}
	_ = syncSubtitleParentDirectory(filepath.Dir(targetPath))
	info, err := os.Stat(targetPath)
	if err != nil {
		return fmt.Errorf("发布后无法读取输出: %v", err)
	}

	output := models.Video{
		Name:      task.OutputBasename,
		Path:      targetPath,
		Directory: filepath.Dir(targetPath),
		Size:      info.Size(),
	}
	now := s.now()
	txErr := database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&output).Error; err != nil {
			return err
		}
		if err := syncShortVideoTagForVideo(tx, output.ID); err != nil {
			return err
		}
		pairA, pairB, err := normalizedVideoPair(source.ID, output.ID)
		if err != nil {
			return err
		}
		fingerprintA, fingerprintB := sourceFingerprint, outputFingerprint
		if source.ID != pairA {
			fingerprintA, fingerprintB = fingerprintB, fingerprintA
		}
		var existingRelation models.VideoSameSourceRelation
		if err := tx.Where("video_a_id = ? AND video_b_id = ?", pairA, pairB).First(&existingRelation).Error; err == nil {
			return fmt.Errorf("同源关系对已存在（可能包含历史否认），拒绝覆盖")
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		relation := models.VideoSameSourceRelation{
			VideoAID: pairA, VideoBID: pairB,
			VideoAFingerprint: fingerprintA, VideoBFingerprint: fingerprintB,
			Status:           models.VideoSameSourceStatusDetected,
			Confidence:       models.AITagConfidenceHigh,
			Reasoning:        "由视频超分生成：输出与源为确定性同源",
			DetectionVersion: fmt.Sprintf("enhancement-v1:%s:%s", task.Profile, task.ModelVersion),
			IsUnread:         false,
			ReviewedAt:       &now,
		}
		if err := tx.Create(&relation).Error; err != nil {
			return err
		}
		// IsUnread=false 是零值，GORM Create 会让位给列默认 true，显式落列。
		if err := tx.Model(&relation).UpdateColumn("is_unread", false).Error; err != nil {
			return err
		}
		return tx.Model(&models.VideoEnhancementTask{}).
			Where("id = ?", task.ID).
			Updates(map[string]any{
				"status": models.EnhancementStatusCompleted, "phase": models.EnhancementPhasePublish,
				"output_video_id": output.ID, "relation_id": relation.ID, "finished_at": &now,
			}).Error
	})
	if txErr != nil {
		// 数据库失败：把最终文件移回工作目录，绝不遗留不可追踪文件。
		if moveErr := os.Rename(targetPath, stagingPath); moveErr != nil {
			_ = os.Remove(targetPath)
		}
		return txErr
	}

	// 事务外的可再生派生数据：技术快照与视觉指纹缓存，失败不影响一致性。
	if err := s.probe.Refresh(context.Background(), output.ID); err != nil {
		logEnhancement("task=%d output=%d technical snapshot deferred: %v", task.ID, output.ID, sanitizeEnhancementError(err.Error()))
	} else if err := database.Transaction(func(tx *gorm.DB) error {
		// Refresh 填充时长后重同步短视频自动标签（发布事务内时长为 0 是 no-op）。
		return syncShortVideoTagForVideo(tx, output.ID)
	}); err != nil {
		logEnhancement("task=%d output=%d short-video tag sync deferred: %v", task.ID, output.ID, sanitizeEnhancementError(err.Error()))
	}
	if s.sameSource != nil {
		var outputVideo models.Video
		if err := database.DB.First(&outputVideo, output.ID).Error; err == nil {
			if _, _, err := s.sameSource.ensureFingerprint(context.Background(), outputVideo, false); err != nil {
				logEnhancement("task=%d output=%d fingerprint deferred: %v", task.ID, output.ID, sanitizeEnhancementError(err.Error()))
			}
		}
	}
	return nil
}

// reconcilePublishedTask 处理 rename 后崩溃的任务：最终文件合法则完成入库，
// 不合法则删除并置失败（P-012 §5）。
func (s *EnhancementService) reconcilePublishedTask(ctx context.Context, task models.VideoEnhancementTask) {
	if task.OutputVideoID != nil {
		return
	}
	source := task.Video
	targetPath := filepath.Join(filepath.Dir(source.Path), task.OutputBasename)
	staging := filepath.Join(enhancementWorkdir(task), "final.cispart")
	if _, err := os.Lstat(targetPath); errors.Is(err, os.ErrNotExist) {
		if _, stagingErr := os.Lstat(staging); stagingErr == nil {
			// rename 未发生：留给 worker 从 verify/publish 重新推进。
			_ = database.DB.Model(&models.VideoEnhancementTask{}).
				Where("id = ?", task.ID).Update("phase", models.EnhancementPhaseVerify).Error
			return
		}
		s.failTask(task.ID, "publish_failed", "发布中断且输出缺失，请重试")
		s.cleanupTaskWorkdir(task)
		return
	}
	if digest, err := sha256File(source.Path); err != nil || digest != task.SourceSHA256 {
		_ = os.Remove(targetPath)
		s.failTask(task.ID, "source_changed", "对账时源文件哈希不一致，已删除未入库输出")
		s.cleanupTaskWorkdir(task)
		return
	}
	staged, err := s.probeEnhancementSource(ctx, targetPath)
	if err != nil || staged.VideoStreams != 1 {
		_ = os.Remove(targetPath)
		s.failTask(task.ID, "verify_failed", "对账时输出不合法，已删除")
		s.cleanupTaskWorkdir(task)
		return
	}
	// 把文件移回工作目录并走正常 publish 事务，保证同一致性路径。
	if err := os.Rename(targetPath, staging); err != nil {
		s.failTask(task.ID, "publish_failed", "对账时无法接管输出文件")
		return
	}
	if err := s.publishOutput(ctx, task, source, staging); err != nil {
		s.failTask(task.ID, "publish_failed", err.Error())
		return
	}
	s.cleanupTaskWorkdir(task)
	s.emitTaskByID(task.ID)
	logEnhancement("task=%d reconciled after crash", task.ID)
}

func fsyncEnhancementFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func loadEnhancementManifest(workdir string) (enhancementSegmentManifest, error) {
	manifest := enhancementSegmentManifest{}
	raw, err := os.ReadFile(filepath.Join(workdir, "segments.json"))
	if err != nil {
		return manifest, err
	}
	err = json.Unmarshal(raw, &manifest)
	return manifest, err
}

func saveEnhancementManifest(workdir string, manifest enhancementSegmentManifest) error {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	temp := filepath.Join(workdir, ".segments.json.tmp")
	file, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, filepath.Join(workdir, "segments.json"))
}

// manifestCommittedFrames 校验清单分段：文件缺失在首个缺口处截断（崩溃时
// 未提交批次整批重做）；已提交分段的大小或哈希不符视为损坏，返回错误让
// 任务失败（P-012 §4：checkpoint 已提交但内容不符不得拼接）。
func manifestCommittedFrames(workdir string, manifest *enhancementSegmentManifest) (int64, error) {
	var committed int64
	valid := manifest.Segments[:0]
	for _, segment := range manifest.Segments {
		path := filepath.Join(workdir, segment.Name)
		info, err := os.Stat(path)
		if err != nil {
			break
		}
		if segment.Size > 0 && info.Size() != segment.Size {
			return 0, fmt.Errorf("verify_failed: 分段 %s 大小与校验点不符", segment.Name)
		}
		if segment.SHA256 != "" {
			digest, err := sha256File(path)
			if err != nil || digest != segment.SHA256 {
				return 0, fmt.Errorf("verify_failed: 分段 %s 哈希与校验点不符", segment.Name)
			}
		}
		valid = append(valid, segment)
		committed += segment.Frames
	}
	manifest.Segments = valid
	return committed, nil
}

func countFilesWithSuffix(dir, suffix string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var count int64
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), suffix) {
			count++
		}
	}
	return count, nil
}

// enhancementErrorCode 从错误消息前缀提取固定错误码。
func enhancementErrorCode(err error, fallback string) string {
	message := err.Error()
	for _, code := range []string{
		"runtime_unavailable", "unsupported_input", "output_conflict", "disk_insufficient",
		"source_changed", "decode_failed", "inference_failed", "encode_failed",
		"verify_failed", "publish_failed",
	} {
		if strings.HasPrefix(message, code+":") || strings.Contains(message, " "+code+":") {
			return code
		}
	}
	return fallback
}
