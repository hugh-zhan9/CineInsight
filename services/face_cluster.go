package services

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// 相似度与聚类阈值（D-017）。都是常量：fixture 钉住，允许在 ±20% 内校准，
// 超出这个范围就不是调参而是换设计，要回 spec。
const (
	// faceEmbeddingDims 是 ArcFace w600k_r50 的输出维度。
	faceEmbeddingDims = 512
	// faceEmbeddingBytes 是一条向量在库里的字节数（float32 × 512）。
	faceEmbeddingBytes = faceEmbeddingDims * 4
	// faceClusterMergeThreshold 是"归入现有簇"的点积下限。
	faceClusterMergeThreshold = 0.55
	// facePersonSeedThreshold 是"这个簇可能是这个人"的点积下限（人物头像种子）。
	facePersonSeedThreshold = 0.60
	// faceCandidateMinObservations 是簇进入人物候选的最少观测数：
	// 一两张脸的簇噪声太大，建议出来只会浪费用户的确认动作。
	faceCandidateMinObservations = 3
)

// encodeFaceEmbedding 把归一化向量编码成库里的 BLOB（float32 小端序）。
func encodeFaceEmbedding(vector []float32) []byte {
	blob := make([]byte, len(vector)*4)
	for i, value := range vector {
		binary.LittleEndian.PutUint32(blob[i*4:], math.Float32bits(value))
	}
	return blob
}

// decodeFaceEmbedding 把库里的 BLOB 解回向量。长度不对就报错——
// 一条长度不对的向量参与点积只会得出无意义的相似度。
func decodeFaceEmbedding(blob []byte) ([]float32, error) {
	if len(blob) != faceEmbeddingBytes {
		return nil, fmt.Errorf("人脸向量长度异常: %d 字节（应为 %d）", len(blob), faceEmbeddingBytes)
	}
	vector := make([]float32, faceEmbeddingDims)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return vector, nil
}

// normalizeFaceEmbedding 返回 L2 归一化后的向量；零向量返回 false。
//
// worker 已经归一化过，这里再做一次是因为库里的向量也可能来自旧版本或被外部改过，
// 而"相似度 = 点积"这个约定只在单位向量上成立。
func normalizeFaceEmbedding(vector []float32) ([]float32, bool) {
	if len(vector) != faceEmbeddingDims {
		return nil, false
	}
	var sum float64
	for _, value := range vector {
		sum += float64(value) * float64(value)
	}
	if sum <= 0 {
		return nil, false
	}
	norm := math.Sqrt(sum)
	out := make([]float32, len(vector))
	for i, value := range vector {
		out[i] = float32(float64(value) / norm)
	}
	return out, true
}

// faceSimilarity 是两条单位向量的点积（D-017：相似度在 Go 侧算，不依赖 pgvector）。
func faceSimilarity(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

// faceClusterCentroid 是聚类时用到的簇代表。
type faceClusterCentroid struct {
	ClusterID uint
	Vector    []float32
	Count     int
}

// bestFaceCluster 返回与 vector 最相似的簇及其相似度；一个都不够相似时返回 nil。
func bestFaceCluster(vector []float32, clusters []faceClusterCentroid, threshold float64) (*faceClusterCentroid, float64) {
	var best *faceClusterCentroid
	bestScore := threshold
	for i := range clusters {
		score := faceSimilarity(vector, clusters[i].Vector)
		if score >= bestScore {
			// >= 让"恰好等于阈值"也算命中，且同分时保留先出现（id 更小）的簇，
			// 结果与遍历顺序一起是确定的。
			if best != nil && score == bestScore {
				continue
			}
			best = &clusters[i]
			bestScore = score
		}
	}
	if best == nil {
		return nil, 0
	}
	return best, bestScore
}

// updateFaceCentroid 把一条新向量并入簇代表：按观测数加权平均再归一化（D-017）。
func updateFaceCentroid(centroid []float32, count int, vector []float32) []float32 {
	if len(centroid) != len(vector) || count <= 0 {
		normalized, ok := normalizeFaceEmbedding(vector)
		if !ok {
			return centroid
		}
		return normalized
	}
	merged := make([]float32, len(centroid))
	weight := float64(count)
	for i := range merged {
		merged[i] = float32((float64(centroid[i])*weight + float64(vector[i])) / (weight + 1))
	}
	normalized, ok := normalizeFaceEmbedding(merged)
	if !ok {
		return centroid
	}
	return normalized
}

// faceBBox 是相对坐标（0–1）的人脸框。
type faceBBox struct {
	X float64
	Y float64
	W float64
	H float64
}

// clamp01 把相对坐标收进 [0,1]：worker 已经裁过一次，这里防的是"库里读回来的
// 值被外部改过"。
func clamp01(value float64) float64 {
	if value < 0 || math.IsNaN(value) {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func (b faceBBox) normalized() faceBBox {
	out := faceBBox{X: clamp01(b.X), Y: clamp01(b.Y), W: clamp01(b.W), H: clamp01(b.H)}
	if out.X+out.W > 1 {
		out.W = 1 - out.X
	}
	if out.Y+out.H > 1 {
		out.H = 1 - out.Y
	}
	return out
}

// String 是入库的 bbox 文本："x,y,w,h"，六位小数。
func (b faceBBox) String() string {
	box := b.normalized()
	return strings.Join([]string{
		strconv.FormatFloat(box.X, 'f', 6, 64),
		strconv.FormatFloat(box.Y, 'f', 6, 64),
		strconv.FormatFloat(box.W, 'f', 6, 64),
		strconv.FormatFloat(box.H, 'f', 6, 64),
	}, ",")
}

// Hash 是 bbox 的量化哈希，作为观测唯一键的一部分（5.1.2）。
//
// 量化到千分之一：同一份源重跑时检测框会有浮点级抖动，直接拿原值进唯一键
// 等于每轮都插一条新观测。四个三位数拼起来共 12 字符，落在 varchar(16) 内。
func (b faceBBox) Hash() string {
	box := b.normalized()
	quantize := func(value float64) int {
		scaled := int(math.Round(value * 1000))
		if scaled < 0 {
			return 0
		}
		if scaled > 999 {
			return 999
		}
		return scaled
	}
	return fmt.Sprintf("%03d%03d%03d%03d", quantize(box.X), quantize(box.Y), quantize(box.W), quantize(box.H))
}

// parseFaceBBox 解析入库的 bbox 文本。
func parseFaceBBox(raw string) (faceBBox, error) {
	parts := strings.Split(strings.TrimSpace(raw), ",")
	if len(parts) != 4 {
		return faceBBox{}, fmt.Errorf("bbox 格式异常: %q", raw)
	}
	values := make([]float64, 4)
	for i, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return faceBBox{}, fmt.Errorf("bbox 数值异常: %q", raw)
		}
		values[i] = value
	}
	return faceBBox{X: values[0], Y: values[1], W: values[2], H: values[3]}.normalized(), nil
}
