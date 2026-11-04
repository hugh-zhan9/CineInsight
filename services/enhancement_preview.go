package services

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// runInference 让原持久任务和临时图片/样片共用同一个可取消推理槽。
func (s *EnhancementService) runInference(ctx context.Context, args []string) (string, error) {
	s.mu.Lock()
	if s.inferenceSlot == nil {
		s.inferenceSlot = NewMediaWorkSlot()
	}
	slot := s.inferenceSlot
	s.mu.Unlock()
	if err := slot.Acquire(ctx); err != nil {
		return "", err
	}
	defer slot.Release()
	return s.runCommand(ctx, s.Capability().BinaryPath, args)
}

func orientProcessingImage(input image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return input
	}
	w, h := input.Bounds().Dx(), input.Bounds().Dy()
	ow, oh := w, h
	if orientation >= 5 {
		ow, oh = h, w
	}
	output := image.NewNRGBA64(image.Rect(0, 0, ow, oh))
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			sx, sy := x, y
			switch orientation {
			case 2:
				sx = w - 1 - x
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sy = h - 1 - y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			case 8:
				sx, sy = w-1-y, x
			}
			output.Set(x, y, input.At(input.Bounds().Min.X+sx, input.Bounds().Min.Y+sy))
		}
	}
	return output
}

func writeProcessingPNG(path string, input image.Image) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	encodeErr := png.Encode(file, input)
	closeErr := file.Close()
	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}

func processingEnhancementWorkingBytes(width, height, regionWidth, regionHeight int) int64 {
	source := int64(width) * int64(height)
	if regionWidth == 0 && regionHeight == 0 {
		return source * 76
	}
	return source*16 + int64(regionWidth+64)*int64(regionHeight+64)*40 + int64(regionWidth)*int64(regionHeight)*20
}

func (s *MediaProcessingService) enhanceImage(ctx context.Context, entry *processingEntry, request MediaProcessingRequest) error {
	if s.enhancement == nil || !s.enhancement.Capability().Available {
		return errors.New("processing_enhancement_unavailable: 请先在设置中准备本地超分组件和模型")
	}
	spec, ok := EnhancementProfiles[request.Profile]
	if !ok {
		return errors.New("processing_invalid: 请选择普通真人或动漫模型")
	}
	file, err := os.Open(entry.source.path)
	if err != nil {
		return err
	}
	config, _, err := image.DecodeConfig(file)
	file.Close()
	if err != nil {
		return errors.New("processing_unsupported: 局部增强暂支持可直接解码的JPEG与PNG图片")
	}
	if int64(config.Width)*int64(config.Height) > processingMaxPixels {
		return errors.New("processing_too_large: 图片超过5000万像素")
	}
	if (request.Width != 0 || request.Height != 0) && (request.Width < 1 || request.Height < 1 || request.Width > 1024 || request.Height > 1024) {
		return errors.New("processing_invalid: 试处理区域不能超过1024×1024")
	}
	// 在任何整图解码/方向转换/2×输出分配前，按最坏16位来源与多缓冲估算CPU工作集。
	if processingEnhancementWorkingBytes(config.Width, config.Height, request.Width, request.Height) > 1<<30 {
		return errors.New("processing_too_large: 增强预计工作集超过1 GiB；可以先试较小选区")
	}
	file, err = os.Open(entry.source.path)
	if err != nil {
		return err
	}
	decoded, _, err := image.Decode(file)
	file.Close()
	if err != nil {
		return err
	}
	exif, err := ParseImageEXIF(entry.source.path)
	if err != nil {
		return err
	}
	decoded = orientProcessingImage(decoded, exif.ExifOrientation)
	width, height := decoded.Bounds().Dx(), decoded.Bounds().Dy()
	rect := image.Rect(0, 0, width, height)
	if request.Width != 0 || request.Height != 0 {
		if request.Width <= 0 || request.Height <= 0 || request.Width > 1024 || request.Height > 1024 || request.X < 0 || request.Y < 0 || request.X > width-request.Width || request.Y > height-request.Height {
			return errors.New("processing_invalid: 请选择图内不超过1024×1024的试处理区域")
		}
		rect = image.Rect(request.X, request.Y, request.X+request.Width, request.Y+request.Height)
	}
	// 提供32像素上下文，结果只展示用户选中的同一片区域。
	halo := rect.Inset(-32).Intersect(decoded.Bounds())
	crop := image.NewNRGBA(halo.Sub(halo.Min))
	draw.Draw(crop, crop.Bounds(), decoded, halo.Min, draw.Src)
	inputPath := filepath.Join(entry.dir, "input.png")
	if err = writeProcessingPNG(inputPath, crop); err != nil {
		return err
	}
	rawPath := filepath.Join(entry.dir, "enhanced.png")
	args := append([]string{"-i", inputPath, "-o", rawPath, "-n", spec.ModelName, "-m", s.enhancement.Capability().ModelDir}, spec.ExtraArgs...)
	for index := 0; index < len(args)-1; index++ {
		if args[index] == "-t" {
			args[index+1] = "256"
		}
	}
	if _, err = s.enhancement.runInference(ctx, args); err != nil {
		return errors.New("processing_inference_failed: 本地增强失败，请检查模型或重试")
	}
	resultFile, err := os.Open(rawPath)
	if err != nil {
		return err
	}
	resultInfo, statErr := resultFile.Stat()
	if statErr != nil || resultInfo.Size() > processingMaxBytes*2 {
		resultFile.Close()
		return errors.New("processing_output_invalid: 超分产物超出大小上限")
	}
	resultConfig, configErr := png.DecodeConfig(resultFile)
	if configErr != nil || resultConfig.Width != halo.Dx()*2 || resultConfig.Height != halo.Dy()*2 {
		resultFile.Close()
		return errors.New("processing_output_invalid: 超分输出尺寸不符")
	}
	if _, err = resultFile.Seek(0, 0); err != nil {
		resultFile.Close()
		return err
	}
	enhanced, err := png.Decode(resultFile)
	resultFile.Close()
	if err != nil {
		return err
	}
	if enhanced.Bounds().Dx() != halo.Dx()*2 || enhanced.Bounds().Dy() != halo.Dy()*2 {
		return errors.New("processing_output_invalid: 超分输出尺寸不符")
	}
	out := image.NewNRGBA(image.Rect(0, 0, rect.Dx()*2, rect.Dy()*2))
	draw.Draw(out, out.Bounds(), enhanced, image.Pt((rect.Min.X-halo.Min.X)*2, (rect.Min.Y-halo.Min.Y)*2), draw.Src)
	comparison := image.NewNRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(comparison, comparison.Bounds(), decoded, rect.Min, draw.Src)
	entry.output = filepath.Join(entry.dir, "result.png")
	entry.comparison = filepath.Join(entry.dir, "comparison.png")
	if err = writeProcessingPNG(entry.output, out); err != nil {
		return err
	}
	if err = writeProcessingPNG(entry.comparison, comparison); err != nil {
		return err
	}
	_ = os.Remove(rawPath)
	_ = os.Remove(inputPath)
	s.mu.Lock()
	entry.view.Width = out.Bounds().Dx()
	entry.view.Height = out.Bounds().Dy()
	entry.view.Format = "png"
	s.mu.Unlock()
	return nil
}

func (s *MediaProcessingService) enhanceSample(ctx context.Context, entry *processingEntry, request MediaProcessingRequest) error {
	if s.enhancement == nil || !s.enhancement.Capability().Available {
		return errors.New("processing_enhancement_unavailable: 请先准备本地超分组件和模型")
	}
	spec, ok := EnhancementProfiles[request.Profile]
	if !ok {
		return errors.New("processing_invalid: 模型无效")
	}
	if math.IsNaN(request.StartSeconds) || math.IsInf(request.StartSeconds, 0) || math.IsNaN(request.DurationSeconds) || math.IsInf(request.DurationSeconds, 0) || request.StartSeconds < 0 || request.DurationSeconds <= 0 || request.DurationSeconds > 10 {
		return errors.New("processing_invalid: 样片区间必须在片内且不超过10秒")
	}
	info, err := s.enhancement.probeEnhancementSource(ctx, entry.source.path)
	if err != nil {
		return err
	}
	if err = validateEnhancementInput(info); err != nil {
		return err
	}
	if request.StartSeconds+request.DurationSeconds > info.Duration+0.001 {
		return errors.New("processing_invalid: 样片区间超出片长")
	}
	ffmpeg, err := s.enhancement.findFFmpeg()
	if err != nil {
		return err
	}
	start := int64(math.Round(request.StartSeconds * info.FPS))
	frames := int64(math.Round(request.DurationSeconds * info.FPS))
	if frames < 1 {
		return errors.New("processing_invalid: 样片不足一帧")
	}
	var segments []string
	for offset := int64(0); offset < frames; offset += 8 {
		count := int64(8)
		if frames-offset < count {
			count = frames - offset
		}
		if int64(info.Width)*int64(info.Height)*count*20 > 1<<30 {
			return errors.New("processing_too_large: 此分辨率的试处理工作集过大")
		}
		name := fmt.Sprintf("sample-%04d.mkv", len(segments))
		if err = s.enhancement.runChunkWithPhase(ctx, ffmpeg, spec, entry.source.path, entry.dir, name, info, start+offset, count, func(string) {}, true); err != nil {
			return err
		}
		segments = append(segments, name)
		var bytes int64
		entries, readErr := os.ReadDir(entry.dir)
		if readErr != nil {
			return readErr
		}
		for _, item := range entries {
			stat, statErr := item.Info()
			if statErr != nil {
				return statErr
			}
			if !stat.IsDir() {
				bytes += stat.Size()
			}
		}
		if bytes > 512<<20 {
			return errors.New("processing_too_large: 样片临时数据超过512 MiB")
		}
	}
	var list strings.Builder
	for _, name := range segments {
		fmt.Fprintf(&list, "file '%s'\n", name)
	}
	listPath := filepath.Join(entry.dir, "sample.txt")
	if err = os.WriteFile(listPath, []byte(list.String()), 0600); err != nil {
		return err
	}
	entry.output = filepath.Join(entry.dir, "sample.mp4")
	if _, err = s.enhancement.runCommand(ctx, ffmpeg, []string{"-v", "error", "-f", "concat", "-safe", "0", "-i", listPath, "-an", "-c:v", "h264_videotoolbox", "-b:v", "8000000", "-pix_fmt", "yuv420p", "-movflags", "+faststart", entry.output}); err != nil {
		return errors.New("processing_encode_failed: 样片预览编码失败")
	}
	entry.comparison = filepath.Join(entry.dir, "comparison.mp4")
	if _, err = s.enhancement.runCommand(ctx, ffmpeg, []string{"-v", "error", "-ss", strconv.FormatFloat(float64(start)/info.FPS, 'f', 6, 64), "-i", entry.source.path, "-frames:v", strconv.FormatInt(frames, 10), "-an", "-c:v", "h264_videotoolbox", "-b:v", "8000000", "-pix_fmt", "yuv420p", "-movflags", "+faststart", entry.comparison}); err != nil {
		return errors.New("processing_encode_failed: 原片对照编码失败")
	}
	for _, name := range segments {
		_ = os.Remove(filepath.Join(entry.dir, name))
	}
	_ = os.Remove(listPath)
	s.mu.Lock()
	entry.view.Width = info.Width * 2
	entry.view.Height = info.Height * 2
	entry.view.Format = "mp4"
	s.mu.Unlock()
	return nil
}
