package services

import (
	"math"
	"math/rand"
	"sort"
	"testing"
)

// unitVector 生成可复现的 L2 归一化随机向量。
func unitVector(rng *rand.Rand) []float32 {
	vector := make([]float32, sceneEmbeddingDims)
	var norm float64
	for index := range vector {
		value := rng.NormFloat64()
		vector[index] = float32(value)
		norm += value * value
	}
	norm = math.Sqrt(norm)
	for index := range vector {
		vector[index] = float32(float64(vector[index]) / norm)
	}
	return vector
}

// blendVector 返回 normalize(a*(1-w) + b*w)：w 越小越像 a。
func blendVector(a, b []float32, weight float32) []float32 {
	out := make([]float32, len(a))
	var norm float64
	for index := range a {
		out[index] = a[index]*(1-weight) + b[index]*weight
		norm += float64(out[index] * out[index])
	}
	norm = math.Sqrt(norm)
	for index := range out {
		out[index] = float32(float64(out[index]) / norm)
	}
	return out
}

func TestQuantizeSceneVectorLayoutAndRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	vector := unitVector(rng)
	blob, err := quantizeSceneVector(vector)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) != 516 {
		t.Fatalf("BLOB 应为 4 字节 scale + 512 字节 int8 = 516，实际 %d", len(blob))
	}
	restored, err := dequantizeSceneVector(blob)
	if err != nil {
		t.Fatal(err)
	}
	var maxAbs float32
	for _, value := range vector {
		maxAbs = float32(math.Max(float64(maxAbs), math.Abs(float64(value))))
	}
	for index := range vector {
		if diff := math.Abs(float64(vector[index] - restored[index])); diff > float64(maxAbs)/127/2+1e-6 {
			t.Fatalf("第 %d 维还原误差 %.6f 超过半个量化步长", index, diff)
		}
	}
	// 最大分量必须量化成 ±127（scale = max|x|）。
	found := false
	for _, b := range blob[4:] {
		if int8(b) == 127 || int8(b) == -127 {
			found = true
		}
	}
	if !found {
		t.Fatal("最大分量应量化为 ±127")
	}
	if _, err := quantizeSceneVector(vector[:10]); err == nil {
		t.Fatal("维度不对的向量应被拒绝")
	}
	zero, _ := quantizeSceneVector(make([]float32, sceneEmbeddingDims))
	if score, ok := sceneQuantizedDot(vector, zero); !ok || score != 0 {
		t.Fatalf("全零向量的点积应为 0: %v %v", score, ok)
	}
}

func TestSceneQuantizedDotMatchesFloatWithinTolerance(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	query := unitVector(rng)
	for trial := 0; trial < 50; trial++ {
		doc := blendVector(query, unitVector(rng), rng.Float32())
		blob, _ := quantizeSceneVector(doc)
		got, ok := sceneQuantizedDot(query, blob)
		if !ok {
			t.Fatal("合法 BLOB 应能打分")
		}
		if diff := math.Abs(float64(got - sceneDotFloat(query, doc))); diff > 0.01 {
			t.Fatalf("int8 点积与 float32 相差 %.5f", diff)
		}
	}
	if _, ok := sceneQuantizedDot(query, []byte{1, 2, 3}); ok {
		t.Fatal("长度不对的 BLOB 不该打分")
	}
}

// TC-17：int8 量化后的排序与 float32 排序一致（夹具：与查询相似度拉开的 40 条向量）。
func TestSceneInt8RankingEqualsFloat32Ranking(t *testing.T) {
	rng := rand.New(rand.NewSource(23))
	query := unitVector(rng)
	type scored struct {
		id    int
		float float32
		int8  float32
	}
	items := make([]scored, 0, 40)
	for index := 0; index < 40; index++ {
		// 权重按 0.024 递增，相邻两条的真实相似度至少差约 0.02，远大于量化误差。
		doc := blendVector(query, unitVector(rng), 0.02+float32(index)*0.024)
		blob, _ := quantizeSceneVector(doc)
		quantized, _ := sceneQuantizedDot(query, blob)
		items = append(items, scored{id: index, float: sceneDotFloat(query, doc), int8: quantized})
	}
	byFloat := append([]scored(nil), items...)
	sort.Slice(byFloat, func(i, j int) bool { return byFloat[i].float > byFloat[j].float })
	top := newSceneTopK(len(items))
	for _, item := range items {
		top.offer(sceneVisualCandidate{SegmentID: uint(item.id + 1), Score: item.int8})
	}
	byInt8 := top.sorted()
	for rank := range byFloat {
		if uint(byFloat[rank].id+1) != byInt8[rank].SegmentID {
			t.Fatalf("第 %d 名不一致：float32=%d int8=%d", rank, byFloat[rank].id+1, byInt8[rank].SegmentID)
		}
	}
}

func TestSceneTopKKeepsBestAndBreaksTiesBySegmentID(t *testing.T) {
	top := newSceneTopK(3)
	for index, score := range []float32{0.1, 0.9, 0.5, 0.9, 0.2, 0.7} {
		top.offer(sceneVisualCandidate{SegmentID: uint(index + 1), Score: score})
	}
	got := top.sorted()
	want := []uint{2, 4, 6}
	for index, candidate := range got {
		if candidate.SegmentID != want[index] {
			t.Fatalf("top-3 应为 %v，实际第 %d 个是 %d", want, index, candidate.SegmentID)
		}
	}
}

// 分段：与段首帧余弦 ≥0.92 延长，否则开新段；段区间 [首帧, 末帧+间隔) 并夹到时长。
func TestBuildSceneSegmentsThresholdAndClamp(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	a, b := unitVector(rng), unitVector(rng)
	nearA := blendVector(a, b, 0.05)  // 与 a 的余弦远高于 0.92
	driftA := blendVector(a, b, 0.45) // 与 a 的余弦低于 0.92
	if sceneDotFloat(a, nearA) < sceneSegmentSimilarity || sceneDotFloat(a, driftA) >= sceneSegmentSimilarity {
		t.Fatalf("夹具不满足阈值前提: near=%.3f drift=%.3f", sceneDotFloat(a, nearA), sceneDotFloat(a, driftA))
	}
	segments := buildSceneSegments([][]float32{a, nearA, a, driftA, b}, 5000, 23000)
	if len(segments) != 3 {
		t.Fatalf("应分成 3 段，实际 %d: %+v", len(segments), segments)
	}
	if segments[0].StartMS != 0 || segments[0].EndMS != 15000 {
		t.Fatalf("首段应为 [0,15000)，实际 [%d,%d)", segments[0].StartMS, segments[0].EndMS)
	}
	if segments[1].StartMS != 15000 || segments[1].EndMS != 20000 {
		t.Fatalf("第二段应为 [15000,20000)，实际 [%d,%d)", segments[1].StartMS, segments[1].EndMS)
	}
	if segments[2].StartMS != 20000 || segments[2].EndMS != 23000 {
		t.Fatalf("末段应夹到时长 [20000,23000)，实际 [%d,%d)", segments[2].StartMS, segments[2].EndMS)
	}
	if &segments[0].Vector[0] != &a[0] {
		t.Fatal("段向量应取段首帧向量")
	}
	if got := buildSceneSegments(nil, 5000, 0); len(got) != 0 {
		t.Fatal("没有帧就没有段")
	}
	if got := buildSceneSegments([][]float32{a}, 5000, 0); len(got) != 1 || got[0].EndMS != 5000 {
		t.Fatalf("时长未知时不夹: %+v", got)
	}
}

func TestBuildSceneCaptionSegmentsMergesEqualNeighbours(t *testing.T) {
	segments := buildSceneCaptionSegments([]string{"海边日落", "海边日落", "", "海边日落", "城市夜景"}, 2000, 0)
	if len(segments) != 3 {
		t.Fatalf("应为 3 段，实际 %+v", segments)
	}
	if segments[0].StartMS != 0 || segments[0].EndMS != 4000 || segments[1].StartMS != 6000 || segments[2].Caption != "城市夜景" {
		t.Fatalf("描述分段不对: %+v", segments)
	}
}

func TestMergeAdjacentSceneCandidates(t *testing.T) {
	merged := mergeAdjacentSceneCandidates([]sceneVisualCandidate{
		{SegmentID: 1, VideoID: 1, StartMS: 0, EndMS: 5000, IntervalMS: 5000, Score: 0.3},
		{SegmentID: 2, VideoID: 1, StartMS: 10000, EndMS: 15000, IntervalMS: 5000, Score: 0.6}, // 间隔 5000 = 一个采样间隔 → 合并
		{SegmentID: 3, VideoID: 1, StartMS: 30000, EndMS: 35000, IntervalMS: 5000, Score: 0.5}, // 间隔 15000 → 不并
		{SegmentID: 4, VideoID: 2, StartMS: 15000, EndMS: 20000, IntervalMS: 5000, Score: 0.55},
	})
	if len(merged) != 3 {
		t.Fatalf("应合并为 3 条，实际 %+v", merged)
	}
	if merged[0].VideoID != 1 || merged[0].StartMS != 0 || merged[0].EndMS != 15000 || merged[0].Score != 0.6 {
		t.Fatalf("合并区间应为 [0,15000) 且分数取最大 0.6: %+v", merged[0])
	}
	if merged[1].VideoID != 2 || merged[2].StartMS != 30000 {
		t.Fatalf("合并后应按分数降序: %+v", merged)
	}
}

// 规模基准：合成 20 万段的点积扫描（不作为 SLA）。
func BenchmarkSceneQuantizedScan200k(b *testing.B) {
	rng := rand.New(rand.NewSource(3))
	query := unitVector(rng)
	const segments = 200_000
	blobs := make([][]byte, segments)
	base := unitVector(rng)
	for index := range blobs {
		blobs[index], _ = quantizeSceneVector(blendVector(base, unitVector(rng), 0.5))
	}
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		top := newSceneTopK(50)
		for index, blob := range blobs {
			score, _ := sceneQuantizedDot(query, blob)
			top.offer(sceneVisualCandidate{SegmentID: uint(index + 1), Score: score})
		}
		_ = top.sorted()
	}
}
