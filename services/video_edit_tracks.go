package services

import (
	"fmt"
	"strings"
)

// 轨道映射（合同「预检、规格与轨道映射」，Q9）：输出轨按参照来源的流顺序排列；其他来源按
// （语言，同语言内序号）对应，对不上的列入 conflicts，必须由配方 tracks 给出明确选择。

const (
	editTrackAudio    = "audio"
	editTrackSubtitle = "subtitle"
)

// editMapSource 是参与映射的一个非参照来源。
type editMapSource struct {
	VideoID uint
	Role    string
	Streams []editStream
}

type editTrackRef struct {
	Stream int
	Fill   string
}

// editTrackPlan 是一种轨道（音轨或字幕）的映射结果。refs[videoID][output] 给出该来源上的流或填充。
type editTrackPlan struct {
	outputs   []EditOutputTrack
	reference []editStream
	kept      []int
	refs      map[uint]map[int]editTrackRef
}

func normalizeEditLanguage(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	if language == "und" {
		return ""
	}
	return language
}

// autoMatchEditTrack 按语言 + 同语言内序号找对应流；没有就返回 -1。
func autoMatchEditTrack(reference []editStream, output int, candidates []editStream) int {
	language := normalizeEditLanguage(reference[output].Language)
	ordinal := 0
	for j := 0; j < output; j++ {
		if normalizeEditLanguage(reference[j].Language) == language {
			ordinal++
		}
	}
	matches := []editStream{}
	for _, stream := range candidates {
		if normalizeEditLanguage(stream.Language) == language {
			matches = append(matches, stream)
		}
	}
	if ordinal < len(matches) {
		return matches[ordinal].Index
	}
	return -1
}

func findEditStream(streams []editStream, index int) (editStream, bool) {
	for _, stream := range streams {
		if stream.Index == index {
			return stream, true
		}
	}
	return editStream{}, false
}

// editSubtitleCompatible：文本目标接受任意文本字幕（精确模式转码）；图形字幕只能原样复制，编码必须相同。
func editSubtitleCompatible(reference, candidate editStream, mode string) bool {
	refImage, candImage := editImageSubtitle(reference.Codec), editImageSubtitle(candidate.Codec)
	if refImage || candImage || mode == "fast" {
		return reference.Codec == candidate.Codec
	}
	return true
}

func findEditChoice(choices []EditTrackChoice, output int, videoID uint) (EditTrackChoice, bool) {
	for _, choice := range choices {
		if choice.Output == output && choice.VideoID == videoID {
			return choice, true
		}
	}
	return EditTrackChoice{}, false
}

// mapEditTracks 计算一种轨道的映射，并把冲突写进 pre（错误 key：track_conflict:<seq>:<kind>:<output>:<video>）。
func mapEditTracks(pre *EditPreflight, seq int, kind string, refVideo uint, refRole string, reference []editStream,
	others []editMapSource, choices []EditTrackChoice, mode string) editTrackPlan {
	plan := editTrackPlan{outputs: []EditOutputTrack{}, reference: reference, kept: []int{}, refs: map[uint]map[int]editTrackRef{}}
	plan.refs[refVideo] = map[int]editTrackRef{}
	for _, other := range others {
		plan.refs[other.VideoID] = map[int]editTrackRef{}
	}
	fill := EditTrackChoiceSilence
	if kind == editTrackSubtitle {
		fill = EditTrackChoiceNone
	}
	for k, ref := range reference {
		output := EditOutputTrack{
			OutputIndex: k, Language: ref.Language, Title: ref.Title, Codec: ref.Codec, Channels: ref.Channels,
			Image: kind == editTrackSubtitle && editImageSubtitle(ref.Codec), Mappings: []EditTrackMapping{},
		}
		if choice, ok := findEditChoice(choices, k, 0); ok && choice.Choice == EditTrackChoiceDrop {
			output.Dropped = true
			plan.outputs = append(plan.outputs, output)
			continue
		}
		plan.kept = append(plan.kept, k)
		plan.refs[refVideo][k] = editTrackRef{Stream: ref.Index}
		output.Mappings = append(output.Mappings, EditTrackMapping{VideoID: refVideo, Role: refRole, StreamIndex: ref.Index, Status: "mapped"})
		for _, other := range others {
			mapping := EditTrackMapping{VideoID: other.VideoID, Role: other.Role, StreamIndex: -1}
			reason := ""
			if choice, ok := findEditChoice(choices, k, other.VideoID); ok {
				mapping.Explicit = true
				switch choice.Choice {
				case EditTrackChoiceStream:
					stream, found := findEditStream(other.Streams, choice.StreamIndex)
					switch {
					case !found:
						reason = "所选流不存在或类型不对"
					case kind == editTrackSubtitle && !editSubtitleCompatible(ref, stream, mode):
						reason = "所选字幕流与输出轨编码不兼容"
					default:
						mapping.StreamIndex, mapping.Status = stream.Index, "mapped"
					}
				case fill:
					if kind == editTrackSubtitle && output.Image {
						reason = "图形字幕轨不能留空，请选择来源流或整轨不导出"
					} else {
						mapping.Fill, mapping.Status = fill, "filled"
					}
				default:
					reason = "该选择不适用于此轨道"
				}
			} else if index := autoMatchEditTrack(reference, k, other.Streams); index >= 0 {
				stream, _ := findEditStream(other.Streams, index)
				if kind == editTrackSubtitle && !editSubtitleCompatible(ref, stream, mode) {
					reason = "同语言字幕流编码不兼容"
				} else {
					mapping.StreamIndex, mapping.Status = index, "mapped"
				}
			} else {
				reason = "无法按语言与序号对应"
			}
			if reason != "" {
				mapping.Status = "conflict"
				allowed := []string{EditTrackChoiceStream, EditTrackChoiceDrop}
				if !output.Image {
					allowed = append(allowed, fill)
				}
				pre.Conflicts = append(pre.Conflicts, EditTrackConflict{
					Seq: seq, Kind: kind, OutputIndex: k, VideoID: other.VideoID, Reason: reason,
					Candidates: editStreamInfos(other.Streams), AllowedFills: allowed,
				})
				pre.addError("track_conflict", fmt.Sprintf("track_conflict:%d:%s:%d:%d", seq, kind, k, other.VideoID),
					other.VideoID, seq, "%s第 %d 条在视频 %d 上%s", editTrackLabel(kind), k+1, other.VideoID, reason)
			} else {
				plan.refs[other.VideoID][k] = editTrackRef{Stream: mapping.StreamIndex, Fill: mapping.Fill}
			}
			output.Mappings = append(output.Mappings, mapping)
		}
		plan.outputs = append(plan.outputs, output)
	}
	return plan
}

func editTrackLabel(kind string) string {
	if kind == editTrackSubtitle {
		return "字幕轨"
	}
	return "音轨"
}

// planTracks 把保留的输出轨写成计划里的轨道规格。精确模式音频统一 AAC（拼接时编码）、保持声道数；
// 文本字幕转为统一文本编码，图形字幕原样复制；快速模式全部沿用来源编码。
func (tp editTrackPlan) planTracks(kind, mode string) []editPlanTrack {
	tracks := []editPlanTrack{}
	for _, k := range tp.kept {
		ref := tp.reference[k]
		track := editPlanTrack{Language: ref.Language, Title: ref.Title, Codec: ref.Codec, Channels: ref.Channels, Layout: ref.ChannelLayout}
		if kind == editTrackAudio {
			if track.Channels < 1 || track.Channels > 8 {
				track.Channels = 2
				track.Layout = ""
			}
			if mode != "fast" {
				track.Codec = "aac"
			}
		} else {
			track.Image = editImageSubtitle(ref.Codec)
			if !track.Image && mode != "fast" {
				track.Codec = editTextSubtitleTarget(ref.Codec)
			}
		}
		tracks = append(tracks, track)
	}
	return tracks
}

// segmentRefs 给出某个来源片段上每条保留输出轨的来源流或填充。
func (tp editTrackPlan) segmentRefs(videoID uint, source int, inMS int64) []editPlanRef {
	refs := []editPlanRef{}
	mapped := tp.refs[videoID]
	for _, k := range tp.kept {
		ref, ok := mapped[k]
		switch {
		case !ok:
			refs = append(refs, editPlanRef{Source: source, Stream: -1, InMS: inMS})
		case ref.Fill != "":
			refs = append(refs, editPlanRef{Source: -1, Stream: -1, Fill: ref.Fill})
		default:
			refs = append(refs, editPlanRef{Source: source, Stream: ref.Stream, InMS: inMS})
		}
	}
	return refs
}

// unusedStreams 统计某来源上没有被任何保留输出轨用到的流。
func (tp editTrackPlan) unusedStreams(videoID uint, streams []editStream) int {
	used := map[int]bool{}
	for _, k := range tp.kept {
		if ref, ok := tp.refs[videoID][k]; ok && ref.Fill == "" {
			used[ref.Stream] = true
		}
	}
	unused := 0
	for _, stream := range streams {
		if !used[stream.Index] {
			unused++
		}
	}
	return unused
}

func (tp editTrackPlan) hasFill() bool {
	for _, mapped := range tp.refs {
		for _, ref := range mapped {
			if ref.Fill != "" {
				return true
			}
		}
	}
	return false
}
