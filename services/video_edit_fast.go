package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// 快速模式（合同「预检」，Q8）：所有参与片段的每条映射流编码参数一致、没有静音/空字幕填充、
// 不需要缩放时才可选。切点向前取到不晚于请求时间的关键帧，预检返回实际时间；不满足时返回原因，
// 不静默改为精确模式。

func editStreamBySource(sources map[uint]*editSourceCtx, plan editPlan, source int, kind string, index int) (editStream, bool) {
	sc := sources[plan.Sources[source].VideoID]
	if sc == nil {
		return editStream{}, false
	}
	switch kind {
	case "video":
		if sc.probe.Video != nil && sc.probe.Video.Index == index {
			return *sc.probe.Video, true
		}
		return editStream{}, false
	case editTrackAudio:
		return findEditStream(sc.probe.Audio, index)
	default:
		return findEditStream(sc.probe.Subtitles, index)
	}
}

// editFastReasons 列出该计划不能用快速模式的原因（空表示可以）。
func editFastReasons(plan editPlan, sources map[uint]*editSourceCtx) []string {
	reasons := []string{}
	add := func(reason string) {
		for _, existing := range reasons {
			if existing == reason {
				return
			}
		}
		reasons = append(reasons, reason)
	}
	var firstVideo *editStream
	for _, segment := range plan.Segments {
		video, ok := editStreamBySource(sources, plan, segment.VideoSource, "video", segment.VideoStream)
		if !ok {
			add("来源视频流不可用")
			continue
		}
		if firstVideo == nil {
			firstVideo = &video
		} else if video.Codec != firstVideo.Codec || video.Profile != firstVideo.Profile || video.Width != firstVideo.Width ||
			video.Height != firstVideo.Height || video.PixFmt != firstVideo.PixFmt || video.FrameRate != firstVideo.FrameRate ||
			video.TimeBase != firstVideo.TimeBase {
			add("来源视频编码参数不一致")
		}
		if video.Width != plan.Video.Width || video.Height != plan.Video.Height {
			add("需要缩放画面")
		}
	}
	checkTracks := func(kind string, count int, refsOf func(editPlanSegment) []editPlanRef) {
		for k := 0; k < count; k++ {
			var first *editStream
			for _, segment := range plan.Segments {
				ref := refsOf(segment)[k]
				if ref.Fill != "" || ref.Stream < 0 {
					add("存在静音或空字幕填充")
					continue
				}
				stream, ok := editStreamBySource(sources, plan, ref.Source, kind, ref.Stream)
				if !ok {
					add("映射的流不可用")
					continue
				}
				if first == nil {
					first = &stream
				} else if stream.Codec != first.Codec || stream.SampleRate != first.SampleRate || stream.Channels != first.Channels {
					add(editTrackLabel(kind) + "编码参数不一致")
				}
			}
		}
	}
	checkTracks(editTrackAudio, len(plan.Audio), func(segment editPlanSegment) []editPlanRef { return segment.Audio })
	checkTracks(editTrackSubtitle, len(plan.Subtitles), func(segment editPlanSegment) []editPlanRef { return segment.Subtitles })
	return reasons
}

// applyFastMode 计算快速模式可用性与切点；当前模式为 fast 时把切点换成关键帧并调整片段。
func (s *VideoEditService) applyFastMode(ctx context.Context, pre *EditPreflight, mode string, plans []editPlan, sources map[uint]*editSourceCtx) []editPlan {
	reasons := []string{}
	for _, plan := range plans {
		for _, reason := range editFastReasons(plan, sources) {
			if !oneOf(reason, reasons...) {
				reasons = append(reasons, reason)
			}
		}
	}
	if len(plans) == 0 {
		reasons = append(reasons, "没有可导出的项")
	}
	cuts := []EditCutPoint{}
	if len(reasons) == 0 {
	collect:
		for _, plan := range plans {
			for index, segment := range plan.Segments {
				if segment.RequestedInMS <= 0 {
					continue
				}
				startSeconds := 0.0
				if sc := sources[plan.Sources[segment.VideoSource].VideoID]; sc != nil {
					startSeconds = sc.probe.StartSeconds
				}
				actual, err := s.editKeyframeAtOrBefore(ctx, plan.Sources[segment.VideoSource].Path, segment.RequestedInMS, startSeconds)
				if err != nil {
					reasons = append(reasons, "无法读取关键帧位置")
					cuts = []EditCutPoint{}
					break collect
				}
				cuts = append(cuts, EditCutPoint{Seq: plan.Seq, Segment: index, VideoID: plan.Sources[segment.VideoSource].VideoID,
					RequestedMS: segment.RequestedInMS, ActualMS: actual})
			}
		}
	}
	pre.Fast = EditFastInfo{Available: len(reasons) == 0, Reasons: reasons, CutPoints: cuts}
	if mode != "fast" {
		return plans
	}
	if !pre.Fast.Available {
		pre.addError("fast_unavailable", "fast_unavailable", 0, 0, "快速模式不可用：%s", strings.Join(reasons, "；"))
		return plans
	}
	// 同一来源上前一段保留到 prevEnd、本段想从更晚的位置开始（中间那段要删掉），关键帧却前移到 prevEnd
	// 或更早：什么也没删，甚至重复画面。这是导出错误，不只是「切点前移」的提示。
	for _, cut := range cuts {
		for p := range plans {
			if plans[p].Seq != cut.Seq || cut.Segment == 0 {
				continue
			}
			prev, segment := plans[p].Segments[cut.Segment-1], plans[p].Segments[cut.Segment]
			prevEnd := prev.InMS + prev.DurMS
			if prev.VideoSource == segment.VideoSource && cut.RequestedMS > prevEnd && cut.ActualMS <= prevEnd {
				pre.addError("fast_cut_invalid", fmt.Sprintf("fast_cut_invalid:%d:%d", cut.Seq, cut.Segment), cut.VideoID, cut.Seq,
					"快速模式下切点只能前移到 %s 的关键帧，不晚于 %s，要删除的部分一点也删不掉；请改用精确模式或调整区间",
					editClock(cut.ActualMS), editClock(prevEnd))
			}
		}
	}
	maxShift := int64(0)
	digest := sha256.New()
	for _, cut := range cuts {
		fmt.Fprintf(digest, "%d:%d:%d:%d;", cut.Seq, cut.Segment, cut.RequestedMS, cut.ActualMS)
		maxShift = max(maxShift, cut.RequestedMS-cut.ActualMS)
		for p := range plans {
			if plans[p].Seq == cut.Seq {
				shiftEditSegment(&plans[p].Segments[cut.Segment], cut.RequestedMS-cut.ActualMS)
			}
		}
	}
	for p := range plans {
		out := int64(0)
		plans[p].DurationMS = 0
		for index := range plans[p].Segments {
			plans[p].Segments[index].OutMS = out
			out += plans[p].Segments[index].DurMS
		}
		plans[p].DurationMS = out
		editEstimatePlan(&plans[p])
	}
	if len(cuts) > 0 {
		pre.addWarning("fast_cut_points", "fast_cut_points:"+hex.EncodeToString(digest.Sum(nil))[:12], 0, 0,
			"快速模式按关键帧切：%d 处切点前移，最多前移 %.2f 秒", len(cuts), float64(maxShift)/1000)
	}
	return plans
}

// shiftEditSegment 把片段起点前移 delta（取到关键帧），终点不变；同段其他轨与旁挂字幕同步前移。
func shiftEditSegment(segment *editPlanSegment, delta int64) {
	if delta <= 0 {
		return
	}
	segment.InMS -= delta
	segment.DurMS += delta
	for i := range segment.Audio {
		if segment.Audio[i].Fill == "" {
			segment.Audio[i].InMS = max(segment.Audio[i].InMS-delta, 0)
		}
	}
	for i := range segment.Subtitles {
		if segment.Subtitles[i].Fill == "" {
			segment.Subtitles[i].InMS = max(segment.Subtitles[i].InMS-delta, 0)
		}
	}
	segment.SidecarInMS = max(segment.SidecarInMS-delta, 0)
}

// editKeyframeAtOrBefore 用 ffprobe -skip_frame nokey -read_intervals 在请求时间之前的窗口里找
// 最后一个关键帧，逐步放宽窗口。返回值向下取整到毫秒：片段命令配合 -copypriorss 0 丢掉位置之前的包，
// 不晚于关键帧的位置保证第一个保留下来的视频包就是这个关键帧（mpegts 的定位不精确，单靠 -ss 会多带一段音频）。
// requestedMS 与返回值都相对容器 start_time（与 -ss 同一时基）；-read_intervals 与 pts_time 是流的
// 绝对时间戳，所以区间加上 startSeconds、读回的时间减去它（mpegts 常见 1.4 秒左右的 start_time）。
func (s *VideoEditService) editKeyframeAtOrBefore(ctx context.Context, path string, requestedMS int64, startSeconds float64) (int64, error) {
	ffprobe, err := s.findFFprobe()
	if err != nil {
		return 0, err
	}
	for _, window := range []int64{15_000, 120_000, requestedMS} {
		start := max(requestedMS-window, 0)
		interval := fmt.Sprintf("%.6f%%%.6f", startSeconds+float64(start)/1000, startSeconds+float64(requestedMS+1)/1000)
		probeCtx, cancel := context.WithTimeout(ctx, videoEditProbeTimeout)
		stdout, _, err := s.runOutput(probeCtx, ffprobe, []string{"-v", "error", "-select_streams", "v:0", "-skip_frame", "nokey",
			"-show_entries", "frame=pts_time,best_effort_timestamp_time", "-read_intervals", interval, "-of", "csv=p=0", path})
		cancel()
		if err != nil {
			return 0, err
		}
		best := -1.0
		for _, line := range strings.Split(stdout, "\n") {
			for _, field := range strings.Split(strings.TrimSpace(line), ",") {
				value, err := strconv.ParseFloat(strings.TrimSpace(field), 64)
				if err != nil {
					continue
				}
				value -= startSeconds
				if value < -0.0005 {
					continue
				}
				value = max(value, 0)
				if value*1000 <= float64(requestedMS)+0.5 && value > best {
					best = value
				}
				break
			}
		}
		if best >= 0 {
			return min(int64(math.Floor(best*1000+1e-6)), requestedMS), nil
		}
		if start == 0 {
			break
		}
	}
	return 0, fmt.Errorf("找不到关键帧")
}
