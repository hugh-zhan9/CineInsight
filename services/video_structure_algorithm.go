package services

import (
	"errors"
	"math"
	"sort"
	"video-master/models"
)

const structureAlgorithmVersion = "local_structure_v1"
const structureFrameWidth = 96
const structureFrameHeight = 54
const structureFrameBytes = structureFrameWidth * structureFrameHeight
const structureWindowMS int64 = 120000
const structureMaxSegments = 500

type structureFrame struct {
	PTS  int64
	Gray []byte
}
type structureWindow struct {
	PendingFramePTS   int64
	Segments          []models.VideoStructureSegment
	PendingStartMS    int64
	PreviousSamplePTS int64
	PreviousSample    []byte
}

func structureDifference(a, b []byte) float64 {
	if len(a) != structureFrameBytes || len(b) != structureFrameBytes {
		return 0
	}
	total := 0
	for i, v := range a {
		delta := int(v) - int(b[i])
		if delta < 0 {
			delta = -delta
		}
		total += delta
	}
	return float64(total) / (255 * float64(structureFrameBytes))
}
func structureSegment(start, end int64, basis string, score float64) models.VideoStructureSegment {
	return models.VideoStructureSegment{StartMS: start, EndMS: end, RepresentativeMS: start + (end-start)/2, Basis: basis, Score: score}
}
func analyzeStructureWindow(state models.VideoStructureState, frames []structureFrame, end int64, chapterBoundaries ...[]int64) (structureWindow, error) {
	result := structureWindow{PendingStartMS: state.PendingStartMS, PendingFramePTS: state.PendingFramePTS, PreviousSamplePTS: state.PreviousSamplePTS, PreviousSample: append([]byte(nil), state.PreviousSample...)}
	if end <= state.CheckpointMS || end > state.DurationMS || len(frames) > 245 {
		return result, errors.New("structure_invalid_window")
	}
	interval := int64(180000)
	if adaptive := (state.DurationMS + 499) / 500; adaptive > interval {
		interval = adaptive
	}
	for _, frame := range frames {
		if len(frame.Gray) != structureFrameBytes || frame.PTS < state.CheckpointMS || frame.PTS >= end || frame.PTS < result.PreviousSamplePTS {
			return result, errors.New("structure_invalid_frame")
		}
		if len(result.PreviousSample) > 0 && frame.PTS == result.PreviousSamplePTS {
			continue
		}
		if len(result.PreviousSample) == 0 {
			result.PendingFramePTS = frame.PTS
		}
		difference := structureDifference(result.PreviousSample, frame.Gray)
		elapsed := frame.PTS - result.PendingStartMS
		chapterCut := false
		if len(chapterBoundaries) > 0 {
			boundaries := chapterBoundaries[0]
			index := sort.Search(len(boundaries), func(i int) bool { return boundaries[i] > frame.PTS })
			if index > 0 {
				point := boundaries[index-1]
				chapterCut = point > result.PendingStartMS && frame.PTS-point <= 500
			}
		}
		if elapsed >= 5000 && (difference >= 0.30 || elapsed >= interval || chapterCut) {
			basis := "interval"
			if difference >= 0.30 {
				basis = "change"
			}
			if chapterCut {
				basis = "embedded"
				difference = 2
			}
			segment := structureSegment(result.PendingStartMS, frame.PTS, basis, difference)
			segment.RepresentativeMS = result.PendingFramePTS
			result.Segments = append(result.Segments, segment)
			result.PendingFramePTS = frame.PTS
			result.PendingStartMS = frame.PTS
		}
		result.PreviousSamplePTS = frame.PTS
		result.PreviousSample = append(result.PreviousSample[:0], frame.Gray...)
	}
	if end == state.DurationMS && result.PendingStartMS < end {
		segment := structureSegment(result.PendingStartMS, end, "interval", 0)
		segment.RepresentativeMS = result.PendingFramePTS
		if end-result.PendingStartMS < 5000 && len(result.Segments) > 0 {
			result.Segments[len(result.Segments)-1].EndMS = end
			result.Segments[len(result.Segments)-1].Score = 0
		} else {
			result.Segments = append(result.Segments, segment)
		}
		result.PendingStartMS = end
	}
	return result, nil
}

// Selected boundaries are queried in score order, capped at 499 before entering memory.
func compactStructureSegments(boundaries []models.VideoStructureSegment, duration int64) []models.VideoStructureSegment {
	sort.Slice(boundaries, func(i, j int) bool { return boundaries[i].EndMS < boundaries[j].EndMS })
	result := make([]models.VideoStructureSegment, 0, len(boundaries)+1)
	start := int64(0)
	for _, boundary := range boundaries {
		if boundary.EndMS <= start || boundary.EndMS >= duration {
			continue
		}
		result = append(result, structureSegment(start, boundary.EndMS, boundary.Basis, boundary.Score))
		start = boundary.EndMS
	}
	if start < duration {
		result = append(result, structureSegment(start, duration, "interval", 0))
	}
	for i := range result {
		result[i].Position = i
	}
	return result
}
func structureFrameQuality(gray []byte) (float64, bool) {
	if len(gray) != structureFrameBytes {
		return 0, true
	}
	black := 0
	var energy float64
	for y := 1; y < structureFrameHeight-1; y++ {
		for x := 1; x < structureFrameWidth-1; x++ {
			i := y*structureFrameWidth + x
			v := gray[i]
			if v < 12 {
				black++
			}
			lap := float64(4*int(v) - int(gray[i-1]) - int(gray[i+1]) - int(gray[i-structureFrameWidth]) - int(gray[i+structureFrameWidth]))
			energy += lap * lap
		}
	}
	return energy / float64((structureFrameWidth-2)*(structureFrameHeight-2)), float64(black) > 0.95*float64((structureFrameWidth-2)*(structureFrameHeight-2))
}
func validStructureSegments(rows []models.VideoStructureSegment, duration int64) bool {
	if len(rows) == 0 || len(rows) > structureMaxSegments {
		return false
	}
	end := int64(0)
	for _, row := range rows {
		if row.StartMS != end || row.EndMS <= row.StartMS || row.EndMS > duration || row.RepresentativeMS < row.StartMS || row.RepresentativeMS >= row.EndMS || !row.RepresentativeReady || math.IsNaN(row.Score) || math.IsInf(row.Score, 0) {
			return false
		}
		end = row.EndMS
	}
	return end == duration
}
