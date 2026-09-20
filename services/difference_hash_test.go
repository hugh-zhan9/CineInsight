package services

import (
	"image"
	"image/color"
	"math/bits"
	"testing"
)

// pointSampledDifferenceHash 是本次改动之前的实现：每个格子只取一个像素。
// 留在测试里是为了把"改进"本身钉住——只断言新实现的绝对数值，改回点采样时
// 一样能通过一个宽松的阈值。
func pointSampledDifferenceHash(src image.Image, region image.Rectangle) uint64 {
	luma := func(x, y, width, height int) uint32 {
		px := region.Min.X + minInt(region.Dx()-1, x*region.Dx()/width)
		py := region.Min.Y + minInt(region.Dy()-1, y*region.Dy()/height)
		r, g, b, _ := src.At(px, py).RGBA()
		return (299*r + 587*g + 114*b) / 1000
	}
	var result uint64
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if luma(x, y, 9, 8) > luma(x+1, y, 9, 8) {
				result |= uint64(1) << uint(y*8+x)
			}
		}
	}
	return result
}

// detailedImage 带高频细节：点采样对这种图最脆弱，正是照片的常态。
func detailedImage(width, height int) image.Image {
	result := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			// 低频的大块渐变决定 dHash 应有的形状，高频噪声叠在上面。
			base := (x*255/width + y*128/height) % 255
			noise := ((x*37 + y*91) % 61) - 30
			value := base + noise
			if value < 0 {
				value = 0
			}
			if value > 255 {
				value = 255
			}
			result.Set(x, y, color.RGBA{R: uint8(value), G: uint8(value), B: uint8(value), A: 255})
		}
	}
	return result
}

// downscale 用面积平均缩图，模拟"同一张图的另一个尺寸"。
func downscale(src image.Image, width, height int) image.Image {
	bounds := src.Bounds()
	result := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		y0, y1 := y*bounds.Dy()/height, (y+1)*bounds.Dy()/height
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < width; x++ {
			x0, x1 := x*bounds.Dx()/width, (x+1)*bounds.Dx()/width
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var total, count uint32
			for py := y0; py < y1; py++ {
				for px := x0; px < x1; px++ {
					r, g, b, _ := src.At(bounds.Min.X+px, bounds.Min.Y+py).RGBA()
					total += (299*r + 587*g + 114*b) / 1000
					count++
				}
			}
			average := uint8(total / count / 257)
			result.Set(x, y, color.RGBA{R: average, G: average, B: average, A: 255})
		}
	}
	return result
}

// 同一张图的两个尺寸必须落在同一个指纹附近。近似重复的判定阈值是 8，光是尺寸差异
// 就吃掉一半预算的话，再叠一点压缩或裁剪就越过阈值报不出来——这正是改成面积平均
// 之前的实际情况（实测库内同图的 480px 与 2560px 两版距离中位数为 4）。
func TestDifferenceHashSurvivesDownscale(t *testing.T) {
	large := detailedImage(720, 540)
	small := downscale(large, 180, 135)

	areaDistance := bits.OnesCount64(
		differenceHash(large, large.Bounds()) ^ differenceHash(small, small.Bounds()),
	)
	pointDistance := bits.OnesCount64(
		pointSampledDifferenceHash(large, large.Bounds()) ^ pointSampledDifferenceHash(small, small.Bounds()),
	)

	if areaDistance > 2 {
		t.Fatalf("面积平均后同图不同尺寸的距离应 ≤2，实际 %d", areaDistance)
	}
	// 钉住改进本身：点采样在同一组输入上必须明显更差，否则这条用例证明不了什么。
	if pointDistance <= areaDistance {
		t.Fatalf("这组夹具没能体现点采样的缺陷（point=%d area=%d），换一组更有细节的输入",
			pointDistance, areaDistance)
	}
}

// 不同内容仍然要拉得开：降噪不等于把所有图都压成同一个哈希。
func TestDifferenceHashStillSeparatesDifferentImages(t *testing.T) {
	left := detailedImage(400, 300)
	right := patternedImage(400, 300)
	distance := bits.OnesCount64(differenceHash(left, left.Bounds()) ^ differenceHash(right, right.Bounds()))
	if distance <= imageCleanupHammingThreshold {
		t.Fatalf("两张不同的图距离应当超过近似阈值 %d，实际 %d", imageCleanupHammingThreshold, distance)
	}
}

// 比 9×8 网格还小的图、以及退化成一个像素的 region，都不许 panic。
func TestDifferenceHashHandlesTinyAndDegenerateRegions(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
	}{
		{"比网格还小", 8, 7},
		{"单像素", 1, 1},
		{"一行", 16, 1},
		{"一列", 1, 16},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			img := detailedImage(testCase.width, testCase.height)
			first := differenceHash(img, img.Bounds())
			second := differenceHash(img, img.Bounds())
			if first != second {
				t.Fatalf("同一输入两次结果不同: %016x vs %016x", first, second)
			}
		})
	}

	// 空 region 直接返回 0，不进循环。
	empty := detailedImage(10, 10)
	if got := differenceHash(empty, image.Rect(5, 5, 5, 5)); got != 0 {
		t.Fatalf("空 region 应返回 0，实际 %016x", got)
	}
}

// 每轴 64 的采样上限只在格子边长超过它时才生效。这里构造一张格子边长 >64 的图，
// 确认仍然稳定出结果——上限本身不会在真实调用点触发（缩略图长边 480、抽帧宽 512）。
func TestDifferenceHashCapsSamplesOnHugeImages(t *testing.T) {
	huge := detailedImage(9*80, 8*80) // 每个格子 80×80，超过 64 的上限
	first := differenceHash(huge, huge.Bounds())
	second := differenceHash(huge, huge.Bounds())
	if first != second {
		t.Fatalf("超大图两次结果不同: %016x vs %016x", first, second)
	}
	if first == 0 {
		t.Fatal("超大图不该得到全零哈希")
	}
}

// 位序是既有契约：video_visual_fingerprints 里存过的哈希按 y*8+x 解释，换位序
// 会让所有历史数据无声地错位。
//
// 夹具必须逐行不同。早先这里用的是整张图单调变暗，64 位全置 1，哈希是
// ffffffffffffffff——任何位序的排列（含把 y*8+x 写成 x*8+y）都得到同一个值，这条
// 用例因此一个位序改动都拦不住，正是它存在要防的那件事。
func TestDifferenceHashBitOrderIsStable(t *testing.T) {
	// 9×8 网格、每格 10×10 像素。偶数网格行从左到右变暗（每一位都置 1 = 0xff），
	// 奇数网格行反过来变亮（每一位都清 0 = 0x00），于是期望值按行交替。
	img := image.NewRGBA(image.Rect(0, 0, 90, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 90; x++ {
			value := uint8(255 - x*2)
			if (y/10)%2 == 1 {
				value = uint8(40 + x*2)
			}
			img.Set(x, y, color.RGBA{R: value, G: value, B: value, A: 255})
		}
	}
	// 第 y 个网格行占据 hash 的第 y 个字节：0、2、4、6 行是 0xff，其余是 0x00。
	const expected = uint64(0x00FF00FF00FF00FF)
	hash := differenceHash(img, img.Bounds())
	if hash != expected {
		t.Fatalf("位序变了：期望 %016x，实际 %016x（转置成 x*8+y 会得到 5555555555555555）", expected, hash)
	}
}
