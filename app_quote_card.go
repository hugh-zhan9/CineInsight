package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// 台词卡片（2026-10-12）：前端在 canvas 上把「画面 + 台词 + 片名 + 时间」合成 PNG，
// 这里只负责弹原生保存框、校验并写出用户选定的文件；不读媒体、不写库，日志不记台词与路径。

// quoteCardMaxBytes 是解码后的 PNG 上限；1920 长边的卡片通常只有几 MB。
const quoteCardMaxBytes = 30 << 20

// quoteCardNameMaxRunes 限制默认文件名长度（不含扩展名），过长的片名截掉。
const quoteCardNameMaxRunes = 80

var errQuoteCardInvalid = errors.New("台词卡图片无效：需要不超过 30 MB 的 PNG")

// selectQuoteCardPath 弹原生保存框；用户取消时返回空串。测试里替换成桩。
var selectQuoteCardPath = func(ctx context.Context, defaultName string) (string, error) {
	return wailsruntime.SaveFileDialog(ctx, wailsruntime.SaveDialogOptions{Title: "保存台词卡", DefaultFilename: defaultName, Filters: []wailsruntime.FileFilter{{DisplayName: "PNG 图片", Pattern: "*.png"}}})
}

// SaveQuoteCard 把前端合成的台词卡（PNG 的 base64，不带 data: 前缀）保存到用户在保存框里选的位置，
// 返回保存路径；用户取消返回空串。数据先校验再弹框，坏数据不会让用户白选一次位置。
func (a *App) SaveQuoteCard(pngBase64 string, suggestedName string) (string, error) {
	data, err := decodeQuoteCardPNG(pngBase64)
	if err != nil {
		return "", err
	}
	if a.ctx == nil {
		return "", errors.New("应用窗口尚未就绪")
	}
	path, err := selectQuoteCardPath(a.ctx, quoteCardDefaultName(suggestedName))
	if err != nil {
		return "", errors.New("打开保存对话框失败")
	}
	if path == "" {
		return "", nil
	}
	// 只写用户在保存框里确认过的路径，不擅自补扩展名：补了就可能悄悄覆盖一个没被确认过的同名文件。
	if err := writeQuoteCardFile(path, data); err != nil {
		log.Printf("API SaveQuoteCard bytes=%d err=%v", len(data), err)
		return "", err
	}
	log.Printf("API SaveQuoteCard bytes=%d saved", len(data))
	return path, nil
}

// decodeQuoteCardPNG 先按编码长度挡掉明显超限的数据（比上限对应的编码长度多出一组以上就不解码，
// 不为它分配内存），解码后再核确切字节数，最后读 PNG 头（签名 + IHDR）。
func decodeQuoteCardPNG(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" || len(encoded) > base64.StdEncoding.EncodedLen(quoteCardMaxBytes)+4 {
		return nil, errQuoteCardInvalid
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) > quoteCardMaxBytes {
		return nil, errQuoteCardInvalid
	}
	if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil {
		return nil, errQuoteCardInvalid
	}
	return data, nil
}

// quoteCardDefaultName 把前端建议的名字整理成保存框的默认文件名：去掉路径分隔符、
// 文件系统不接受的字符与控制字符，截断过长的片名，统一以 .png 结尾。
func quoteCardDefaultName(suggested string) string {
	name := strings.TrimSpace(suggested)
	if strings.EqualFold(filepath.Ext(name), ".png") {
		name = name[:len(name)-len(".png")]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(strings.TrimSpace(name), ".")
	if utf8.RuneCountInString(name) > quoteCardNameMaxRunes {
		name = strings.TrimSpace(string([]rune(name)[:quoteCardNameMaxRunes]))
	}
	if name == "" {
		name = "台词卡"
	}
	return name + ".png"
}

// writeQuoteCardFile 先写同目录临时文件再改名：写到一半失败不会留下半张图，也不会破坏原有同名文件；
// 同名文件已在保存框里确认过替换，改名直接覆盖它（目标是符号链接时替换链接本身，不写穿到链接目标）。
func writeQuoteCardFile(path string, data []byte) error {
	failed := errors.New("保存台词卡失败，请检查目录权限和磁盘空间")
	temp, err := os.CreateTemp(filepath.Dir(path), ".quote-card-*.png")
	if err != nil {
		return failed
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	_, writeErr := temp.Write(data)
	if writeErr == nil {
		writeErr = temp.Chmod(0o644)
	}
	if writeErr == nil {
		writeErr = temp.Sync()
	}
	closeErr := temp.Close()
	if writeErr != nil || closeErr != nil {
		return failed
	}
	if err := os.Rename(tempPath, path); err != nil {
		return failed
	}
	return nil
}
