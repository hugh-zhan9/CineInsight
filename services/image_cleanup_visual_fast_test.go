package services

import (
	"bytes"
	"image"
	"image/jpeg"
	"math/rand/v2"
	"testing"
)

// opaqueImage 隐藏具体类型，强制 imageCleanupVisualFromImage 走经 img.At 的通用路径。
type opaqueImage struct{ image.Image }

func randomYCbCr(rng *rand.Rand, rect image.Rectangle, ratio image.YCbCrSubsampleRatio) *image.YCbCr {
	img := image.NewYCbCr(rect, ratio)
	for index := range img.Y {
		img.Y[index] = byte(rng.IntN(256))
	}
	for index := range img.Cb {
		img.Cb[index] = byte(rng.IntN(256))
		img.Cr[index] = byte(rng.IntN(256))
	}
	return img
}

// YCbCr 快路径必须与通用路径逐字节相同：各种色度抽样、奇数尺寸、非零起点与子图。
func TestImageCleanupVisualYCbCrFastPathIsByteIdentical(t *testing.T) {
	rng := rand.New(rand.NewPCG(32, 32))
	ratios := []image.YCbCrSubsampleRatio{
		image.YCbCrSubsampleRatio444, image.YCbCrSubsampleRatio422, image.YCbCrSubsampleRatio420,
		image.YCbCrSubsampleRatio440, image.YCbCrSubsampleRatio411, image.YCbCrSubsampleRatio410,
	}
	cases := 0
	for _, ratio := range ratios {
		for trial := 0; trial < 12; trial++ {
			width, height := 1+rng.IntN(97), 1+rng.IntN(83)
			if trial == 0 {
				width, height = 480, 357
			}
			minX, minY := rng.IntN(7)-3, rng.IntN(7)-3
			img := randomYCbCr(rng, image.Rect(minX, minY, minX+width, minY+height), ratio)
			images := []image.Image{img}
			if width > 4 && height > 4 {
				// 子图的 Rect 起点非零、Stride 大于宽度，最容易暴露下标换算错误。
				images = append(images, img.SubImage(image.Rect(minX+1, minY+2, minX+width-1, minY+height-1)))
			}
			for _, candidate := range images {
				fast := imageCleanupVisualFromImage(candidate)
				slow := imageCleanupVisualFromImage(opaqueImage{candidate})
				if fast == nil || slow == nil || *fast != *slow {
					t.Fatalf("ratio %v %v 快路径不一致", ratio, candidate.Bounds())
				}
				cases++
			}
		}
	}
	// 真实 JPEG 解码结果（4:2:0）也一致。
	var buf bytes.Buffer
	source := image.NewRGBA(image.Rect(0, 0, 131, 77))
	for index := range source.Pix {
		source.Pix[index] = byte(rng.IntN(256))
	}
	if err := jpeg.Encode(&buf, source, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded.(*image.YCbCr); !ok {
		t.Fatalf("夹具应解码为 YCbCr: %T", decoded)
	}
	if fast, slow := imageCleanupVisualFromImage(decoded), imageCleanupVisualFromImage(opaqueImage{decoded}); *fast != *slow {
		t.Fatal("JPEG 解码结果快路径不一致")
	}
	// 空图仍返回 nil。
	empty := image.NewYCbCr(image.Rect(0, 0, 0, 0), image.YCbCrSubsampleRatio420)
	if imageCleanupVisualFromImage(empty) != nil {
		t.Fatal("空图应返回 nil")
	}
	t.Logf("cases=%d identical", cases)
}

func BenchmarkImageCleanupVisualFromJPEG(b *testing.B) {
	data := perfLikeThumbnailJPEG(b)
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		b.Fatal(err)
	}
	b.Run("fast", func(b *testing.B) {
		for range b.N {
			imageCleanupVisualFromImage(decoded)
		}
	})
	b.Run("generic", func(b *testing.B) {
		for range b.N {
			imageCleanupVisualFromImage(opaqueImage{decoded})
		}
	})
}

func perfLikeThumbnailJPEG(b *testing.B) []byte {
	rng := rand.New(rand.NewPCG(1, 5))
	img := image.NewRGBA(image.Rect(0, 0, 480, 360))
	for index := range img.Pix {
		img.Pix[index] = byte(rng.IntN(256))
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88}); err != nil {
		b.Fatal(err)
	}
	return buf.Bytes()
}
