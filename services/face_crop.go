package services

import (
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// 裁剪图规格（D-020、5.3）。
const (
	// faceCropMaxEdge 是裁剪图的长边上限：它只用来在审阅面板里认脸。
	faceCropMaxEdge = 160
	// faceCropPadRatio 是在检测框外扩的比例：只留框内的话头发与下巴都被切掉，
	// 用户很难认出这是谁。
	faceCropPadRatio = 0.25
	faceCropQuality  = 85
	// faceCropTempPrefix 是"临时文件 + rename"的前缀：进程被 kill 时会留下这类
	// 残件，用量统计跳过它们、每轮分析开始时清扫（同一前缀只此一处定义）。
	faceCropTempPrefix = ".face-"
)

// cropFaceThumbnail 从源图裁出人脸缩略图写到 destPath（≤ faceCropMaxEdge）。
//
// 只用标准库解码（JPEG / PNG / GIF）：视频帧是我们自己抽的 JPEG，图片经
// ResolveImageView 后常规格式是原文件。WebP 这类标准库解不了的格式没有裁剪图
// （观测照常入库、crop_path 留空），不额外引依赖也不再拉一个 ffmpeg 进程。
//
// orientation 是 EXIF 方向标记（0/1 表示不旋转）：worker 用 OpenCV 解码时会按
// EXIF 转正，这里必须做同一件事，否则 bbox 与像素对不上，裁出来的是胳膊。
func cropFaceThumbnail(sourcePath string, orientation int, box faceBBox, destPath string) error {
	file, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	decoded, _, err := image.Decode(file)
	file.Close()
	if err != nil {
		return fmt.Errorf("裁剪图解码失败: %w", err)
	}
	decoded = applyEXIFOrientation(decoded, orientation)

	bounds := decoded.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return fmt.Errorf("裁剪图源尺寸异常: %dx%d", width, height)
	}

	region := paddedFaceRect(box, width, height)
	if region.Dx() <= 0 || region.Dy() <= 0 {
		return fmt.Errorf("裁剪区域为空")
	}
	region = region.Add(bounds.Min)

	cropped := image.NewRGBA(image.Rect(0, 0, region.Dx(), region.Dy()))
	draw.Draw(cropped, cropped.Bounds(), decoded, region.Min, draw.Src)
	thumbnail := downscaleToMaxEdge(cropped, faceCropMaxEdge)

	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(destPath), faceCropTempPrefix+"*.jpg")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	if err := jpeg.Encode(temp, thumbnail, &jpeg.Options{Quality: faceCropQuality}); err != nil {
		temp.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, destPath); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

// paddedFaceRect 把相对 bbox 外扩后换算成像素矩形（原点为 0,0）。
func paddedFaceRect(box faceBBox, width, height int) image.Rectangle {
	normalized := box.normalized()
	padX := normalized.W * faceCropPadRatio
	padY := normalized.H * faceCropPadRatio
	left := clamp01(normalized.X - padX)
	top := clamp01(normalized.Y - padY)
	right := clamp01(normalized.X + normalized.W + padX)
	bottom := clamp01(normalized.Y + normalized.H + padY)

	x0 := int(math.Floor(left * float64(width)))
	y0 := int(math.Floor(top * float64(height)))
	x1 := int(math.Ceil(right * float64(width)))
	y1 := int(math.Ceil(bottom * float64(height)))
	if x1 > width {
		x1 = width
	}
	if y1 > height {
		y1 = height
	}
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	return image.Rect(x0, y0, x1, y1)
}

// downscaleToMaxEdge 用箱式平均把长边缩到 maxEdge 以内。
// 标准库没有缩放函数，而这里要的只是一张 160px 的脸——箱式平均足够，
// 也不必为此引入 x/image。
func downscaleToMaxEdge(source *image.RGBA, maxEdge int) image.Image {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	longest := width
	if height > longest {
		longest = height
	}
	if longest <= maxEdge || maxEdge <= 0 {
		return source
	}
	scale := float64(maxEdge) / float64(longest)
	outWidth := int(math.Max(1, math.Round(float64(width)*scale)))
	outHeight := int(math.Max(1, math.Round(float64(height)*scale)))
	out := image.NewRGBA(image.Rect(0, 0, outWidth, outHeight))
	for y := 0; y < outHeight; y++ {
		srcY0 := y * height / outHeight
		srcY1 := (y + 1) * height / outHeight
		if srcY1 <= srcY0 {
			srcY1 = srcY0 + 1
		}
		for x := 0; x < outWidth; x++ {
			srcX0 := x * width / outWidth
			srcX1 := (x + 1) * width / outWidth
			if srcX1 <= srcX0 {
				srcX1 = srcX0 + 1
			}
			var sumR, sumG, sumB, sumA, count uint64
			for sy := srcY0; sy < srcY1; sy++ {
				for sx := srcX0; sx < srcX1; sx++ {
					offset := source.PixOffset(bounds.Min.X+sx, bounds.Min.Y+sy)
					sumR += uint64(source.Pix[offset])
					sumG += uint64(source.Pix[offset+1])
					sumB += uint64(source.Pix[offset+2])
					sumA += uint64(source.Pix[offset+3])
					count++
				}
			}
			if count == 0 {
				continue
			}
			offset := out.PixOffset(x, y)
			out.Pix[offset] = uint8(sumR / count)
			out.Pix[offset+1] = uint8(sumG / count)
			out.Pix[offset+2] = uint8(sumB / count)
			out.Pix[offset+3] = uint8(sumA / count)
		}
	}
	return out
}

// applyEXIFOrientation 把 EXIF 的八种方向标记落实到像素上。
// 0（未知）与 1（正常）原样返回。
func applyEXIFOrientation(source image.Image, orientation int) image.Image {
	switch orientation {
	case 2, 3, 4, 5, 6, 7, 8:
	default:
		return source
	}
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	outWidth, outHeight := width, height
	if orientation >= 5 {
		outWidth, outHeight = height, width
	}
	out := image.NewRGBA(image.Rect(0, 0, outWidth, outHeight))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			var dx, dy int
			switch orientation {
			case 2: // 水平镜像
				dx, dy = width-1-x, y
			case 3: // 旋转 180°
				dx, dy = width-1-x, height-1-y
			case 4: // 垂直镜像
				dx, dy = x, height-1-y
			case 5: // 转置
				dx, dy = y, x
			case 6: // 顺时针 90°
				dx, dy = height-1-y, x
			case 7: // 反转置
				dx, dy = height-1-y, width-1-x
			case 8: // 逆时针 90°
				dx, dy = y, width-1-x
			}
			out.Set(dx, dy, source.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	return out
}

// faceCropOrientation 读源文件的 EXIF 方向标记；读不到就当不用旋转。
// 只对图片有意义：视频帧是 ffmpeg 现抽的 JPEG，不带 EXIF。
func faceCropOrientation(path string) int {
	if strings.TrimSpace(path) == "" {
		return 0
	}
	data, err := ParseImageEXIF(path)
	if err != nil {
		return 0
	}
	return data.ExifOrientation
}
