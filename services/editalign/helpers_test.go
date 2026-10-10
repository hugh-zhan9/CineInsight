package editalign

import (
	"context"
	"math"
	"math/bits"
	"testing"
)

// splitmix64 是确定性的伪随机源，测试夹具不依赖 math/rand 的版本行为。
type splitmix64 uint64

func (s *splitmix64) next() uint64 {
	*s += 0x9e3779b97f4a7c15
	z := uint64(*s)
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func (s *splitmix64) intn(n int) int { return int(s.next() % uint64(n)) }

const contentFrameMS = 40 // 合成内容按 25fps 定义

// synthContent 是一段合成"母带"：每个内容帧有一枚 dHash（镜头内每帧随机翻一位，
// 镜头切换时换成新的随机指纹）与一个镜头编号；black 帧是低信息黑场。
type synthContent struct {
	seed   uint64
	hashes []uint64
	shot   []int
	black  []bool
}

func newSynthContent(seed uint64, seconds int) *synthContent {
	rng := splitmix64(seed)
	frames := seconds * 1000 / contentFrameMS
	c := &synthContent{seed: seed, hashes: make([]uint64, frames), shot: make([]int, frames), black: make([]bool, frames)}
	shot, left := -1, 0
	var hash uint64
	for f := range frames {
		if left == 0 {
			shot++
			left = 25 + rng.intn(76) // 1–4 秒一个镜头
			for {
				hash = rng.next()
				if ones := bits.OnesCount64(hash); ones >= 22 && ones <= 42 {
					break
				}
			}
		} else {
			hash ^= 1 << rng.intn(64)
		}
		left--
		c.hashes[f], c.shot[f] = hash, shot
	}
	return c
}

// setBlack 把 [fromMS, toMS) 的内容改成黑场。
func (c *synthContent) setBlack(fromMS, toMS int64) {
	for f := fromMS / contentFrameMS; f < toMS/contentFrameMS && int(f) < len(c.black); f++ {
		c.black[f] = true
		c.hashes[f] = 0
	}
}

// pixels 生成内容帧 f 的 32×32 小图：每个镜头是一幅随机参数的平滑图案并匀速平移，
// 相邻帧差小、帧距越大差越大（不周期重复），与真实镜头里的运动相似。
func (c *synthContent) pixels(f int) []byte {
	out := make([]byte, 32*32)
	if c.black[f] {
		for i := range out {
			out[i] = 16
		}
		return out
	}
	shotStart := f
	for shotStart > 0 && c.shot[shotStart-1] == c.shot[f] {
		shotStart--
	}
	rng := splitmix64(c.seed*7919 + uint64(c.shot[f])*104729)
	unit := func() float64 { return float64(rng.next()%10000) / 10000 }
	fx, fy := 0.15+0.3*unit(), 0.15+0.3*unit()
	p1, p2 := 6.28*unit(), 6.28*unit()
	velocity := 0.4 + 0.8*unit()
	shift := velocity * float64(f-shotStart)
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			u := float64(x) + shift
			v := 128 + 60*math.Sin(fx*u+p1) + 45*math.Cos(fy*float64(y)+0.7*fx*u+p2)
			out[y*32+x] = byte(min(max(v, 0), 255))
		}
	}
	return out
}

// piece 把文件时间 [fileMS, fileMS+durMS) 映射到某段内容的 contentMS 起点。
type piece struct {
	content           *synthContent
	fileMS, contentMS int64
	durMS             int64
}

// synthVideo 是由若干内容片拼成的合成视频；未覆盖的时间是黑场。
type synthVideo struct {
	pieces     []piece
	durationMS int64
	frameMS    int64 // 本文件的帧间隔（原帧率）
}

func (v synthVideo) frameAt(tMS int64) (*synthContent, int, bool) {
	t := tMS / v.frameMS * v.frameMS // 该时刻显示的那一帧
	for _, p := range v.pieces {
		if t >= p.fileMS && t < p.fileMS+p.durMS {
			f := int((p.contentMS + t - p.fileMS) / contentFrameMS)
			if f >= 0 && f < len(p.content.hashes) {
				return p.content, f, true
			}
		}
	}
	return nil, 0, false
}

func (v synthVideo) sequence(stepMS int64) Sequence {
	seq := Sequence{StepMS: stepMS}
	for t := int64(0); t < v.durationMS; t += stepMS {
		var hash uint64
		if c, f, ok := v.frameAt(t); ok {
			hash = c.hashes[f]
		}
		seq.Hashes = append(seq.Hashes, hash)
	}
	return seq
}

func (v synthVideo) reader() GrayFrameReader {
	return func(ctx context.Context, startMS, durationMS int64) ([]GrayFrame, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var frames []GrayFrame
		first := (startMS + v.frameMS - 1) / v.frameMS
		for k := first; k*v.frameMS < startMS+durationMS && k*v.frameMS < v.durationMS; k++ {
			t := k * v.frameMS
			pixels := make([]byte, 32*32)
			for i := range pixels {
				pixels[i] = 16
			}
			if c, f, ok := v.frameAt(t); ok {
				pixels = c.pixels(f)
			}
			frames = append(frames, GrayFrame{PTSMS: t, Pixels: pixels})
		}
		return frames, nil
	}
}

func (v synthVideo) source(id uint, stepMS int64, withFrames bool) Source {
	s := Source{ID: id, DurationMS: v.durationMS, Hashes: v.sequence(stepMS)}
	if withFrames {
		s.Frames = v.reader()
	}
	return s
}

func within(t *testing.T, name string, got, want, tolerance int64) {
	t.Helper()
	if d := got - want; d < -tolerance || d > tolerance {
		t.Errorf("%s = %d, want %d ± %d (off by %d)", name, got, want, tolerance, d)
	}
}
