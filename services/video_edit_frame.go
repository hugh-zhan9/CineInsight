package services

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"video-master/models"
)

// EditFrameRequest 按项目来源与指纹定位真实相邻帧；direction 为 -1/0/1。
type EditFrameRequest struct {
	ProjectID     uint   `json:"project_id"`
	VideoID       uint   `json:"video_id"`
	SourceVersion string `json:"source_version"`
	AtMS          int64  `json:"at_ms"`
	Direction     int    `json:"direction"`
}

// EditFramePosition 返回可供单帧预览及配方使用的毫秒时间。
type EditFramePosition struct {
	AtMS int64 `json:"at_ms"`
}

// NavigateFrame 读取局部实际PTS，避免用平均帧率推算可变帧率素材。
func (s *VideoEditService) NavigateFrame(ctx context.Context, request EditFrameRequest) (*EditFramePosition, error) {
	if request.AtMS < 0 || request.Direction < -1 || request.Direction > 1 || request.SourceVersion == "" {
		return nil, editError("frame_invalid", "帧定位参数无效")
	}
	project, err := loadEditProject(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	recipe, err := parseEditRecipe(project.RecipeJSON)
	if err != nil {
		return nil, err
	}
	member := false
	if project.Kind == models.VideoEditKindCrop && recipe.Crop != nil {
		for _, item := range recipe.Crop.Items {
			if item.VideoID == request.VideoID {
				member = true
				break
			}
		}
	}
	if !member {
		return nil, editError("frame_invalid", "来源不属于当前裁剪项目")
	}
	if err := s.slot.Acquire(ctx); err != nil {
		return nil, err
	}
	defer s.slot.Release()
	pre := &EditPreflight{}
	sources, err := s.collectEditSources(ctx, pre, []uint{request.VideoID})
	if err != nil {
		return nil, err
	}
	if len(pre.Errors) > 0 {
		return nil, editError(pre.Errors[0].Code, "%s", pre.Errors[0].Message)
	}
	sc := sources[request.VideoID]
	if request.SourceVersion != editCropSourceVersion(sc.size, sc.modTimeNS) {
		return nil, editError("source_changed", "来源已变化，请重新读取画面")
	}
	if request.AtMS >= sc.probe.DurationMS {
		return nil, editError("frame_invalid", "时间点超出视频时长")
	}
	times, err := s.readEditFrameTimes(ctx, sc, request.AtMS)
	if err != nil {
		return nil, err
	}
	start := max(request.AtMS-10_000, 0)
	end := min(request.AtMS+10_000, sc.probe.DurationMS)
	index := sort.Search(len(times), func(i int) bool { return times[i] >= request.AtMS })
	if index == len(times) {
		if end < sc.probe.DurationMS {
			return nil, editError("frame_unavailable", "局部窗口未读到目标帧，请重新选择时间点")
		}
		index = len(times) - 1
	}
	target := index + request.Direction
	if (target < 0 && start > 0) || (target >= len(times) && end < sc.probe.DurationMS) {
		return nil, editError("frame_unavailable", "局部窗口未读到相邻帧，请调整时间点后重试")
	}
	index = max(0, min(len(times)-1, target))
	return &EditFramePosition{AtMS: times[index]}, nil
}

// readEditFrameTimes 为逐帧定位有界读取局部实际PTS。
func (s *VideoEditService) readEditFrameTimes(ctx context.Context, sc *editSourceCtx, atMS int64) ([]int64, error) {
	binary, err := s.findFFprobe()
	if err != nil {
		return nil, err
	}
	// 时间窗口和输出均有界；不扫描整片，也不在失败时猜测相邻帧。
	start := max(atMS-10_000, 0)
	end := min(atMS+10_000, sc.probe.DurationMS)
	interval := fmt.Sprintf("%.6f%%%.6f", sc.probe.StartSeconds+float64(start)/1000, sc.probe.StartSeconds+float64(end)/1000)
	probeCtx, cancel := context.WithTimeout(ctx, videoEditProbeTimeout)
	defer cancel()
	stdout, _, err := s.runOutput(probeCtx, binary, []string{"-v", "error", "-threads", "2", "-select_streams", "V:0", "-show_entries", "frame=best_effort_timestamp_time", "-read_intervals", interval, "-of", "csv=p=0", sc.path})
	if err != nil {
		return nil, editError("frame_unavailable", "逐帧定位失败，请重新选择时间点")
	}
	size, modified, err := editFileFingerprint(sc.path)
	if err != nil || size != sc.size || modified != sc.modTimeNS {
		return nil, editError("source_changed", "帧定位期间来源发生变化")
	}
	times := editFrameTimes(stdout, sc.probe.StartSeconds, sc.probe.DurationMS)
	if len(times) == 0 {
		return nil, editError("frame_unavailable", "没有读到实际帧时间，请重新选择时间点")
	}
	return times, nil
}

func editFrameTimes(raw string, startSeconds float64, durationMS int64) []int64 {
	times := []int64{}
	for _, line := range strings.Split(raw, "\n") {
		value, err := strconv.ParseFloat(strings.TrimSpace(strings.Split(line, ",")[0]), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		value -= startSeconds
		if value < -0.0005 {
			continue
		}
		ms := int64(math.Floor(max(0, value)*1000 + 1e-6))
		if ms < durationMS {
			times = append(times, ms)
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	unique := times[:0]
	for _, ms := range times {
		if len(unique) == 0 || unique[len(unique)-1] != ms {
			unique = append(unique, ms)
		}
	}
	return unique
}
