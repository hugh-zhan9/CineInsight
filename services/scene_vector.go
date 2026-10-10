package services

import (
	"container/heap"
	"encoding/binary"
	"errors"
	"math"
	"sort"
)

const (
	sceneEmbeddingDims = 512
	// sceneQuantizedBytes 是一条 int8 量化向量的 BLOB 长度：4 字节小端 float32 scale + 512 字节 int8。
	sceneQuantizedBytes = 4 + sceneEmbeddingDims
	// sceneSegmentSimilarity 是相邻帧并入同一段的余弦下限（与段首帧比较）。
	sceneSegmentSimilarity = 0.92
)

var errSceneVectorShape = errors.New("scene_vector_shape")

// quantizeSceneVector 按合同做 int8 对称量化：scale = max|x|，q = round(x/scale×127)。
// 全零向量的 scale 为 0，q 全为 0（点积恒为 0）。
func quantizeSceneVector(vector []float32) ([]byte, error) {
	if len(vector) != sceneEmbeddingDims {
		return nil, errSceneVectorShape
	}
	var scale float32
	for _, value := range vector {
		if abs := float32(math.Abs(float64(value))); abs > scale {
			scale = abs
		}
	}
	blob := make([]byte, sceneQuantizedBytes)
	binary.LittleEndian.PutUint32(blob[:4], math.Float32bits(scale))
	if scale == 0 {
		return blob, nil
	}
	for index, value := range vector {
		q := math.Round(float64(value/scale) * 127)
		if q > 127 {
			q = 127
		} else if q < -127 {
			q = -127
		}
		blob[4+index] = byte(int8(q))
	}
	return blob, nil
}

// dequantizeSceneVector 把 BLOB 还原成 float32（测试与诊断用；检索走 sceneQuantizedDot）。
func dequantizeSceneVector(blob []byte) ([]float32, error) {
	if len(blob) != sceneQuantizedBytes {
		return nil, errSceneVectorShape
	}
	scale := math.Float32frombits(binary.LittleEndian.Uint32(blob[:4])) / 127
	out := make([]float32, sceneEmbeddingDims)
	for index := range out {
		out[index] = float32(int8(blob[4+index])) * scale
	}
	return out, nil
}

// sceneQuantizedDot 计算 float32 查询向量与一条 int8 量化向量的点积。
// 两边都已 L2 归一化时结果就是余弦相似度（量化误差内）。
func sceneQuantizedDot(query []float32, blob []byte) (float32, bool) {
	if len(blob) != sceneQuantizedBytes || len(query) != sceneEmbeddingDims {
		return 0, false
	}
	scale := math.Float32frombits(binary.LittleEndian.Uint32(blob[:4]))
	payload := blob[4:sceneQuantizedBytes]
	var sum float32
	for index, value := range query {
		sum += value * float32(int8(payload[index]))
	}
	return sum * scale / 127, true
}

func sceneDotFloat(a, b []float32) float32 {
	var sum float32
	for index := range a {
		sum += a[index] * b[index]
	}
	return sum
}

// sceneSegmentDraft 是一个视频建索引时算出来、尚未落库的段。
type sceneSegmentDraft struct {
	StartMS int64
	EndMS   int64
	Vector  []float32
	Caption string
}

// sceneSegmentEnd 把段终点夹到视频时长内；时长未知（<=0）或夹完不再晚于起点时保持原值。
func sceneSegmentEnd(startMS, endMS, durationMS int64) int64 {
	if durationMS > startMS && endMS > durationMS {
		return durationMS
	}
	return endMS
}

// buildSceneSegments 按合同分段：第 n 帧（从 0 起）代表 n×间隔毫秒；首帧开新段，
// 后续帧与段首向量余弦 ≥0.92 时延长段终点，否则开新段；段向量取段首帧向量；
// 段区间为 [首帧时间, 末帧时间+间隔)，夹到视频时长。
func buildSceneSegments(vectors [][]float32, intervalMS, durationMS int64) []sceneSegmentDraft {
	segments := make([]sceneSegmentDraft, 0, len(vectors)/2+1)
	for index, vector := range vectors {
		at := int64(index) * intervalMS
		if len(segments) > 0 {
			current := &segments[len(segments)-1]
			if sceneDotFloat(current.Vector, vector) >= sceneSegmentSimilarity {
				current.EndMS = sceneSegmentEnd(current.StartMS, at+intervalMS, durationMS)
				continue
			}
		}
		segments = append(segments, sceneSegmentDraft{
			StartMS: at,
			EndMS:   sceneSegmentEnd(at, at+intervalMS, durationMS),
			Vector:  vector,
		})
	}
	return segments
}

// buildSceneCaptionSegments 是外部描述的分段：没有向量可比，只把描述完全相同的相邻帧
// 并成一段；空描述的帧不成段（它什么也检索不到）。
func buildSceneCaptionSegments(captions []string, intervalMS, durationMS int64) []sceneSegmentDraft {
	segments := make([]sceneSegmentDraft, 0, len(captions))
	previous := -2
	for index, caption := range captions {
		at := int64(index) * intervalMS
		if caption == "" {
			continue
		}
		if len(segments) > 0 && previous == index-1 && segments[len(segments)-1].Caption == caption {
			current := &segments[len(segments)-1]
			current.EndMS = sceneSegmentEnd(current.StartMS, at+intervalMS, durationMS)
			previous = index
			continue
		}
		segments = append(segments, sceneSegmentDraft{
			StartMS: at,
			EndMS:   sceneSegmentEnd(at, at+intervalMS, durationMS),
			Caption: caption,
		})
		previous = index
	}
	return segments
}

// sceneVisualCandidate 是检索扫描出来的一条候选段。
type sceneVisualCandidate struct {
	SegmentID  uint
	VideoID    uint
	StartMS    int64
	EndMS      int64
	IntervalMS int64
	Score      float32
}

// sceneCandidateWorse 定义堆序：分数低者更差；同分时段 ID 大者更差（结果可复现）。
func sceneCandidateWorse(a, b sceneVisualCandidate) bool {
	if a.Score != b.Score {
		return a.Score < b.Score
	}
	return a.SegmentID > b.SegmentID
}

type sceneCandidateHeap []sceneVisualCandidate

func (h sceneCandidateHeap) Len() int           { return len(h) }
func (h sceneCandidateHeap) Less(i, j int) bool { return sceneCandidateWorse(h[i], h[j]) }
func (h sceneCandidateHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *sceneCandidateHeap) Push(x any)        { *h = append(*h, x.(sceneVisualCandidate)) }
func (h *sceneCandidateHeap) Pop() any {
	old := *h
	item := old[len(old)-1]
	*h = old[:len(old)-1]
	return item
}

// sceneTopK 维护分数最高的 limit 条候选（最小堆，堆顶是当前最差的一条）。
type sceneTopK struct {
	limit int
	items sceneCandidateHeap
}

func newSceneTopK(limit int) *sceneTopK {
	if limit < 1 {
		limit = 1
	}
	return &sceneTopK{limit: limit, items: make(sceneCandidateHeap, 0, limit)}
}

func (t *sceneTopK) offer(candidate sceneVisualCandidate) {
	if len(t.items) < t.limit {
		heap.Push(&t.items, candidate)
		return
	}
	if sceneCandidateWorse(t.items[0], candidate) {
		t.items[0] = candidate
		heap.Fix(&t.items, 0)
	}
}

// sorted 按分数从高到低返回（同分按段 ID 升序）。
func (t *sceneTopK) sorted() []sceneVisualCandidate {
	out := append([]sceneVisualCandidate(nil), t.items...)
	sort.Slice(out, func(i, j int) bool { return sceneCandidateWorse(out[j], out[i]) })
	return out
}

// mergeAdjacentSceneCandidates 把同一视频里相邻（间隔不超过一个采样间隔）的命中段并成
// 一个区间，分数取最大；返回按分数降序（同分按视频 ID、起点升序）。
func mergeAdjacentSceneCandidates(candidates []sceneVisualCandidate) []sceneVisualCandidate {
	byVideo := map[uint][]sceneVisualCandidate{}
	for _, candidate := range candidates {
		byVideo[candidate.VideoID] = append(byVideo[candidate.VideoID], candidate)
	}
	merged := make([]sceneVisualCandidate, 0, len(candidates))
	for _, group := range byVideo {
		sort.Slice(group, func(i, j int) bool { return group[i].StartMS < group[j].StartMS })
		current := group[0]
		for _, next := range group[1:] {
			gap := max(current.IntervalMS, next.IntervalMS)
			if next.StartMS-current.EndMS <= gap {
				current.EndMS = max(current.EndMS, next.EndMS)
				if next.Score > current.Score {
					current.Score = next.Score
					current.SegmentID = next.SegmentID
				}
				continue
			}
			merged = append(merged, current)
			current = next
		}
		merged = append(merged, current)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Score != merged[j].Score {
			return merged[i].Score > merged[j].Score
		}
		if merged[i].VideoID != merged[j].VideoID {
			return merged[i].VideoID < merged[j].VideoID
		}
		return merged[i].StartMS < merged[j].StartMS
	})
	return merged
}
