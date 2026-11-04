package services

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/jpeg"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// recompressPNG 只重压原IDAT的原始字节；其余块（包括动画帧和元数据）逐字节保留。
func recompressPNG(input []byte) ([]byte, error) {
	if len(input) < 33 || !bytes.Equal(input[:8], []byte("\x89PNG\r\n\x1a\n")) {
		return nil, errors.New("compression_unsupported: 不是完整PNG")
	}
	type chunk struct {
		kind      string
		raw, data []byte
	}
	var chunks []chunk
	var compressed bytes.Buffer
	offset := 8
	ended := false
	for offset < len(input) {
		if len(input)-offset < 12 {
			return nil, errors.New("compression_invalid: PNG块不完整")
		}
		size := int64(binary.BigEndian.Uint32(input[offset:]))
		if size > int64(len(input)-offset-12) {
			return nil, errors.New("compression_invalid: PNG块长度错误")
		}
		end := offset + 12 + int(size)
		raw := input[offset:end]
		kind := string(raw[4:8])
		data := raw[8 : len(raw)-4]
		if crc32.ChecksumIEEE(raw[4:len(raw)-4]) != binary.BigEndian.Uint32(raw[len(raw)-4:]) {
			return nil, errors.New("compression_invalid: PNG校验失败")
		}
		chunks = append(chunks, chunk{kind, raw, data})
		if kind == "IDAT" {
			compressed.Write(data)
		}
		offset = end
		if kind == "IEND" {
			ended = true
			break
		}
	}
	if !ended || offset != len(input) || compressed.Len() == 0 {
		return nil, errors.New("compression_unsupported: PNG结构不受支持")
	}
	reader, err := zlib.NewReader(bytes.NewReader(compressed.Bytes()))
	if err != nil {
		return nil, err
	}
	plain, err := io.ReadAll(io.LimitReader(reader, 256<<20+1))
	closeErr := reader.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(plain) > 256<<20 {
		return nil, errors.New("compression_too_large: PNG解压数据超出256 MiB上限")
	}
	var resultStream bytes.Buffer
	writer, _ := zlib.NewWriterLevel(&resultStream, zlib.BestCompression)
	if _, err = writer.Write(plain); err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	// IDAT原文完全一致即不改变像素、滤波、位深、透明度或任何帧；未知块原样保留。
	var result bytes.Buffer
	result.Write(input[:8])
	written := false
	for _, part := range chunks {
		if part.kind != "IDAT" {
			result.Write(part.raw)
			continue
		}
		if written {
			continue
		}
		written = true
		data := resultStream.Bytes()
		_ = binary.Write(&result, binary.BigEndian, uint32(len(data)))
		result.WriteString("IDAT")
		result.Write(data)
		crc := crc32.NewIEEE()
		_, _ = crc.Write([]byte("IDAT"))
		_, _ = crc.Write(data)
		_ = binary.Write(&result, binary.BigEndian, crc.Sum32())
	}
	return result.Bytes(), nil
}

func jpegMetadata(data []byte) ([]string, error) {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return nil, errors.New("compression_invalid: JPEG头无效")
	}
	var markers []string
	for offset := 2; offset < len(data); {
		if data[offset] != 0xff {
			offset++
			continue
		}
		for offset < len(data) && data[offset] == 0xff {
			offset++
		}
		if offset >= len(data) {
			break
		}
		marker := data[offset]
		offset++
		if marker == 0 || marker == 0xd8 || marker >= 0xd0 && marker <= 0xd7 {
			continue
		}
		if marker == 0xd9 {
			if offset != len(data) {
				return nil, errors.New("compression_unsupported: JPEG含附加数据（例如动态照片的视频），为完整保留内容暂不压缩")
			}
			sort.Strings(markers)
			return markers, nil
		}
		if offset+2 > len(data) {
			break
		}
		size := int(binary.BigEndian.Uint16(data[offset:]))
		if size < 2 || size > len(data)-offset {
			break
		}
		if marker >= 0xe0 && marker <= 0xef || marker == 0xfe {
			markers = append(markers, string([]byte{marker})+string(data[offset:offset+size]))
		}
		offset += size
	}
	return nil, errors.New("compression_invalid: JPEG结构不完整")
}

func losslessJPEGEqual(ctx context.Context, before, after []byte) error {
	aMeta, err := jpegMetadata(before)
	if err != nil {
		return err
	}
	bMeta, err := jpegMetadata(after)
	if err != nil {
		return err
	}
	// jpegtran会为没有JFIF头的标准YCbCr JPEG补一个无物理单位、1:1像素的头；
	// 原有APP/COM标记必须逐字节保留，除此以外只允许这个不丢信息的标准头。
	remaining := append([]string{}, bMeta...)
	for _, marker := range aMeta {
		found := -1
		for index, candidate := range remaining {
			if candidate == marker {
				found = index
				break
			}
		}
		if found < 0 {
			return errors.New("compression_not_lossless: JPEG元数据未完整保留")
		}
		remaining = append(remaining[:found], remaining[found+1:]...)
	}
	for _, marker := range remaining {
		if marker != "\xe0\x00\x10JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00" {
			return errors.New("compression_not_lossless: JPEG元数据发生变化")
		}
	}
	a, err := jpeg.Decode(bytes.NewReader(before))
	if err != nil {
		return err
	}
	b, err := jpeg.Decode(bytes.NewReader(after))
	if err != nil {
		return err
	}
	if a.Bounds() != b.Bounds() {
		return errors.New("compression_not_lossless: JPEG尺寸变化")
	}
	for y := a.Bounds().Min.Y; y < a.Bounds().Max.Y; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for x := a.Bounds().Min.X; x < a.Bounds().Max.X; x++ {
			ar, ag, ab, aa := a.At(x, y).RGBA()
			br, bg, bb, ba := b.At(x, y).RGBA()
			if ar != br || ag != bg || ab != bb || aa != ba {
				return errors.New("compression_not_lossless: JPEG像素变化")
			}
		}
	}
	return nil
}

func findJPEGTran() (string, error) {
	executable, _ := os.Executable()
	if index := strings.Index(executable, ".app/Contents/"); index >= 0 {
		path := filepath.Join(executable[:index+len(".app/Contents/")], "Resources", "image-tools", "jpegtran")
		if info, err := os.Stat(path); err == nil && info.Mode()&0111 != 0 {
			return path, nil
		}
		return "", errors.New("compression_tool_unavailable: JPEG无损压缩组件未随应用安装")
	}
	path, err := exec.LookPath("jpegtran")
	if err != nil {
		return "", errors.New("compression_tool_unavailable: JPEG无损压缩组件不可用")
	}
	return path, nil
}

func (s *MediaProcessingService) compressImage(ctx context.Context, entry *processingEntry) error {
	file, err := os.Open(entry.source.path)
	if err != nil {
		return err
	}
	defer file.Close()
	input, err := io.ReadAll(io.LimitReader(file, processingMaxBytes+1))
	if err != nil {
		return err
	}
	if int64(len(input)) > processingMaxBytes {
		return errors.New("compression_too_large: 图片超过处理上限")
	}
	format := strings.ToLower(strings.TrimPrefix(filepath.Ext(entry.source.path), "."))
	var output []byte
	switch format {
	case "png":
		output, err = recompressPNG(input)
	case "jpg", "jpeg":
		var config image.Config
		config, err = jpeg.DecodeConfig(bytes.NewReader(input))
		if err != nil {
			return err
		}
		if int64(config.Width)*int64(config.Height) > processingMaxPixels {
			return errors.New("compression_too_large: 图片超过5000万像素")
		}
		binaryPath, findErr := findJPEGTran()
		if findErr != nil {
			return findErr
		}
		candidate := filepath.Join(entry.dir, "candidate."+format)
		if _, err = runEnhancementCommand(ctx, binaryPath, []string{"-copy", "all", "-optimize", "-outfile", candidate, entry.source.path}); err != nil {
			return errors.New("compression_failed: JPEG无损优化失败")
		}
		output, err = os.ReadFile(candidate)
		if err == nil {
			err = losslessJPEGEqual(ctx, input, output)
		}
	default:
		return errors.New("compression_unsupported: 当前严格无损优化支持PNG和JPEG；其他格式不会重编码或删除元数据")
	}
	if err != nil {
		return err
	}
	if len(output) >= len(input) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entry.output = filepath.Join(entry.dir, "compressed."+format)
	if err = os.WriteFile(entry.output, output, 0600); err != nil {
		return err
	}
	s.mu.Lock()
	entry.view.Format = format
	s.mu.Unlock()
	return nil
}
