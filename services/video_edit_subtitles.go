package services

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"video-master/models"
	"video-master/services/subtitleparser"
)

// 旁挂字幕（合同「预检」旁挂字幕一条）：同名 .srt 经 subtitleparser 读入（含编码识别），按成品时间线
// 重写——切掉区间内的条目、跨切点的条目截断、后续平移；merge 按累计偏移拼接；hd_replace 精确模式原样
// 复制长版 .srt。成品旁的 .srt 必须是新文件：成品名保证它空闲，写入前在字幕锁内再查一次，存在就不写。

// editCue 是重写后的一条字幕（毫秒）。
type editCue struct {
	StartMS int64
	EndMS   int64
	Lines   []string
}

// retimeEditCues 取 cues 中落在来源区间 [inMS, inMS+durMS) 的部分，截断到区间内并平移到成品时间 outMS 起。
func retimeEditCues(cues []subtitleparser.Segment, inMS, durMS, outMS int64) []editCue {
	end := inMS + durMS
	result := []editCue{}
	for _, cue := range cues {
		start, stop := max(cue.StartTimeMs, inMS), min(cue.EndTimeMs, end)
		if stop <= start {
			continue
		}
		lines := []string{}
		for _, line := range cue.Lines {
			if strings.TrimSpace(line) != "" {
				lines = append(lines, line)
			}
		}
		if len(lines) == 0 {
			continue
		}
		result = append(result, editCue{StartMS: start - inMS + outMS, EndMS: stop - inMS + outMS, Lines: lines})
	}
	return result
}

// retimeEditSidecar 按计划片段拼出成品的 .srt 内容；没有任何条目时返回 nil。
func retimeEditSidecar(plan editPlan) ([]byte, error) {
	cache := map[int][]subtitleparser.Segment{}
	cues := []editCue{}
	for _, segment := range plan.Segments {
		source := segment.SidecarSource
		parsed, ok := cache[source]
		if !ok {
			path := subtitleparser.SRTPathForVideo(plan.Sources[source].Path)
			if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
				segments, err := subtitleparser.ParseFile(path)
				if err != nil {
					return nil, fmt.Errorf("读取来源字幕失败")
				}
				parsed = segments
			}
			cache[source] = parsed
		}
		cues = append(cues, retimeEditCues(parsed, segment.SidecarInMS, segment.DurMS, segment.OutMS)...)
	}
	if len(cues) == 0 {
		return nil, nil
	}
	return formatEditSRT(cues), nil
}

func formatEditSRT(cues []editCue) []byte {
	var builder strings.Builder
	for index, cue := range cues {
		if index > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(strconv.Itoa(index + 1))
		builder.WriteByte('\n')
		builder.WriteString(formatEditSRTTime(cue.StartMS))
		builder.WriteString(" --> ")
		builder.WriteString(formatEditSRTTime(cue.EndMS))
		builder.WriteByte('\n')
		builder.WriteString(strings.Join(cue.Lines, "\n"))
		builder.WriteByte('\n')
	}
	return []byte(builder.String())
}

func formatEditSRTTime(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, ms%1000)
}

// writeEditSidecar 在入库之后写成品的 .srt（失败只返回一句不含路径的警告）。
func (s *VideoEditService) writeEditSidecar(ctx context.Context, plan editPlan, output models.Video) string {
	var content []byte
	switch plan.Sidecar.Mode {
	case "copy":
		path := subtitleparser.SRTPathForVideo(plan.Sources[plan.Sidecar.CopySource].Path)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return "长版旁挂字幕不可读，未复制到成品"
		}
		content, err = os.ReadFile(path)
		if err != nil {
			return "长版旁挂字幕不可读，未复制到成品"
		}
	case "retime":
		var err error
		content, err = retimeEditSidecar(plan)
		if err != nil {
			return "来源旁挂字幕读取失败，成品未生成字幕"
		}
	default:
		return ""
	}
	if len(content) == 0 {
		return ""
	}
	target := subtitleparser.SRTPathForVideo(output.Path)
	unlock := lockSubtitleFile(target)
	defer unlock()
	if _, err := os.Lstat(target); err == nil || !os.IsNotExist(err) {
		return "成品旁已有同名字幕文件，未写入"
	}
	if _, err := NewLibrarySubtitleFileWriter(s.dataDir).Replace(ctx, output.ID, target, content); err != nil {
		logVideoEdit("output=%d sidecar subtitle write failed: %s", output.ID, scrubEditMessage(err.Error()))
		return "成品旁挂字幕写入失败，可稍后手动处理"
	}
	return ""
}
