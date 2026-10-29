package services

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"video-master/models"
)

// EditCropRect 是转正、方形像素画布上的固定裁剪框，单位为像素。
type EditCropRect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// EditCropItem 每个来源单独产出；source_version 绑定用户所预览的文件。
type EditCropItem struct {
	VideoID       uint          `json:"video_id"`
	Rect          *EditCropRect `json:"rect"`
	SourceVersion string        `json:"source_version"`
	Confirmed     bool          `json:"confirmed"`
}

// EditCropRecipe 是固定黑边裁剪批次。
type EditCropRecipe struct {
	Items []EditCropItem `json:"items"`
}

// EditCropResult 是自动检测建议；已确认项不覆盖，未识别时保留原框。
type EditCropResult struct {
	VideoID uint          `json:"video_id"`
	Status  string        `json:"status"`
	Rect    *EditCropRect `json:"rect"`
	Applied bool          `json:"applied"`
}

func validateCropStructure(crop *EditCropRecipe) error {
	if crop == nil || len(crop.Items) < 1 || len(crop.Items) > videoEditTrimMaxItems {
		return editError("recipe_invalid", "画面裁剪需要 1–%d 项", videoEditTrimMaxItems)
	}
	seen := map[uint]bool{}
	for _, item := range crop.Items {
		if item.VideoID == 0 || seen[item.VideoID] {
			return editError("recipe_invalid", "裁剪来源为空或重复")
		}
		seen[item.VideoID] = true
		if len(item.SourceVersion) > 80 {
			return editError("recipe_invalid", "裁剪来源版本无效")
		}
	}
	return nil
}

func editCropSourceVersion(size, modTime int64) string { return fmt.Sprintf("%d:%d", size, modTime) }

// editDisplaySize 与 FFmpeg 自动转正后的归一化画布一致，旧三类计划不使用它。
func editDisplaySize(video *editStream) (int, int) {
	if video == nil {
		return 0, 0
	}
	sar := editFrameRateValue(strings.ReplaceAll(video.SampleAspectRatio, ":", "/"))
	if sar <= 0 || math.IsNaN(sar) || math.IsInf(sar, 0) {
		sar = 1
	}
	w, h := float64(video.Width)*sar, float64(video.Height)
	if math.Abs(math.Mod(video.Rotation, 180)) == 90 {
		w, h = h, w
	}
	if math.IsNaN(w) || math.IsInf(w, 0) || w < 2 || h < 2 || w > 32768 || h > 32768 {
		return 0, 0
	}
	return int(math.Round(w)) &^ 1, int(math.Round(h)) &^ 1
}

func editCropValid(r *EditCropRect, width, height int) bool {
	return r != nil && width >= 2 && height >= 2 && r.X >= 0 && r.Y >= 0 && r.Width >= 2 && r.Height >= 2 &&
		r.X <= width && r.Y <= height && r.Width <= width-r.X && r.Height <= height-r.Y &&
		r.X%2 == 0 && r.Y%2 == 0 && r.Width%2 == 0 && r.Height%2 == 0
}

func (s *VideoEditService) buildCropPlans(pre *EditPreflight, mode string, recipe EditRecipe, sources map[uint]*editSourceCtx, completed map[int]bool) []editPlan {
	plans := []editPlan{}
	for index, item := range recipe.Crop.Items {
		seq := index + 1
		if completed[seq] {
			continue
		}
		sc := sources[item.VideoID]
		if sc == nil || !sc.ok {
			continue
		}
		width, height := editDisplaySize(sc.probe.Video)
		key := fmt.Sprint(seq)
		if !editCropValid(item.Rect, width, height) {
			pre.addError("crop_invalid", "crop_invalid:"+key, item.VideoID, seq, "第 %d 项请设置画布内的有效裁剪框（坐标和宽高须为偶数，宽高至少 2 像素）", seq)
			continue
		}
		if item.SourceVersion != editCropSourceVersion(sc.size, sc.modTimeNS) {
			pre.addError("source_changed", "source_changed:"+key, item.VideoID, seq, "第 %d 项的来源已变化，请重新预览并设置裁剪框", seq)
		}
		if !item.Confirmed {
			pre.addError("unconfirmed", "unconfirmed:"+key, item.VideoID, seq, "第 %d 项尚未确认裁剪区域", seq)
		}
		b := newEditPlanBuilder(seq, models.VideoEditKindCrop, mode)
		b.plan.Video = s.planVideoSpec(sc, mode)
		rect := *item.Rect
		b.plan.Video.Crop, b.plan.Video.CanvasWidth, b.plan.Video.CanvasHeight = &rect, width, height
		b.plan.Video.Width, b.plan.Video.Height = rect.Width, rect.Height
		audio := mapEditTracks(pre, seq, editTrackAudio, item.VideoID, "source", sc.probe.Audio, nil, nil, mode)
		subs := mapEditTracks(pre, seq, editTrackSubtitle, item.VideoID, "source", sc.probe.Subtitles, nil, nil, mode)
		source := b.source(sc)
		b.plan.Segments = append(b.plan.Segments, editPlanSegment{VideoSource: source, VideoStream: sc.probe.Video.Index, DurMS: sc.probe.DurationMS,
			Audio: audio.segmentRefs(item.VideoID, source, 0), Subtitles: subs.segmentRefs(item.VideoID, source, 0), SidecarSource: source})
		s.completeEditPlan(pre, b, []*editSourceCtx{sc}, sc, " (裁剪)", audio, subs)
		pre.addWarning("crop_content", "crop_content:"+key, item.VideoID, seq, "《%s》框外画面（包括画面内字幕）将被裁掉；内嵌字幕定位可能受影响，请检查多个时间点", sc.video.Name)
		plans = append(plans, b.plan)
	}
	return plans
}

var editCropPattern = regexp.MustCompile(`crop=(\d+):(\d+):(\d+):(\d+)`)

// editCropUnion 保守保留所有有效取样框的并集，不因某一暗场缩小有效画面。
func editCropUnion(rects []EditCropRect) *EditCropRect {
	if len(rects) == 0 {
		return nil
	}
	r := rects[0]
	right, bottom := r.X+r.Width, r.Y+r.Height
	for _, other := range rects[1:] {
		r.X, r.Y = min(r.X, other.X), min(r.Y, other.Y)
		right, bottom = max(right, other.X+other.Width), max(bottom, other.Y+other.Height)
	}
	r.Width, r.Height = right-r.X, bottom-r.Y
	return &r
}

func parseEditCropSample(tail string, width, height int) *EditCropRect {
	rects := []EditCropRect{}
	for _, match := range editCropPattern.FindAllStringSubmatch(tail, -1) {
		var values [4]int
		for i := range values {
			values[i], _ = strconv.Atoi(match[i+1])
		}
		r := EditCropRect{Width: values[0], Height: values[1], X: values[2], Y: values[3]}
		if editCropValid(&r, width, height) {
			rects = append(rects, r)
		}
	}
	return editCropUnion(rects)
}

func (s *VideoEditService) analyzeCrops(ctx context.Context, projectID uint, recipe *EditRecipe, result *EditAnalysisResult) error {
	ffmpeg, err := s.findFFmpeg()
	if err != nil {
		return editError("analysis_failed", "找不到 ffmpeg")
	}
	ids := []uint{}
	for _, item := range recipe.Crop.Items {
		if !item.Confirmed {
			ids = append(ids, item.VideoID)
		}
	}
	pre := &EditPreflight{}
	sources, err := s.collectEditSources(ctx, pre, ids)
	if err != nil {
		return err
	}
	if len(pre.Errors) > 0 {
		return editError(pre.Errors[0].Code, "%s", pre.Errors[0].Message)
	}
	for index := range recipe.Crop.Items {
		item := &recipe.Crop.Items[index]
		if item.Confirmed {
			result.Crops = append(result.Crops, EditCropResult{VideoID: item.VideoID, Status: "confirmed"})
			continue
		}
		sc := sources[item.VideoID]
		width, height := editDisplaySize(sc.probe.Video)
		if width < 2 || height < 2 {
			return editError("analysis_failed", "无法确定画面尺寸")
		}
		samples := []EditCropRect{}
		for sample := 0; sample < 7; sample++ {
			at := sc.probe.DurationMS * int64(sample+1) / 8
			runCtx, cancel := context.WithTimeout(ctx, videoEditProbeTimeout)
			_, tail, runErr := s.runOutput(runCtx, ffmpeg, []string{"-nostdin", "-hide_banner", "-v", "info", "-threads", "2", "-filter_threads", "1", "-ss", editSeconds(at), "-i", sc.path,
				"-map", fmt.Sprintf("0:%d", sc.probe.Video.Index), "-an", "-sn", "-vf", fmt.Sprintf("scale=%d:%d,setsar=1,cropdetect=limit=0.08:round=2:reset=0:skip=0", width, height), "-frames:v", "8", "-t", "2", "-f", "null", "-"})
			cancel()
			if runErr != nil {
				return editError("analysis_failed", "《%s》检测黑边失败：%s", sc.video.Name, scrubEditPaths(runErr.Error(), sc.path))
			}
			if r := parseEditCropSample(tail, width, height); r != nil {
				samples = append(samples, *r)
			}
			s.setAnalysisProgress(projectID, (float64(index)+float64(sample+1)/7)/float64(len(recipe.Crop.Items)))
		}
		size, modTime, err := editFileFingerprint(sc.path)
		if err != nil || size != sc.size || modTime != sc.modTimeNS {
			return editError("source_changed", "《%s》检测期间来源发生变化", sc.video.Name)
		}
		detection := EditCropResult{VideoID: item.VideoID, Status: "undetected"}
		if len(samples) >= 3 {
			detection.Rect = editCropUnion(samples)
			detection.Status, detection.Applied = "detected", true
			if *detection.Rect == (EditCropRect{Width: width, Height: height}) {
				detection.Status = "no_borders"
			}
			item.Rect, item.SourceVersion, item.Confirmed = detection.Rect, editCropSourceVersion(size, modTime), false
		}
		result.Crops = append(result.Crops, detection)
	}
	return ctx.Err()
}
