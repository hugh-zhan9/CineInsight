package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 旁挂字幕判定（D-PC17）：「视频同目录有没有旁挂字幕」全仓只在这里定义一次。
// 字幕生成/翻译的缺失提示、后续 subtitle_index_states.has_sidecar 的写入都必须调用这里的函数，
// 不得各自再写目录扫描。规则：
//   - 同目录，文件名以「视频基本名 + .」开头（大小写不敏感，与 macOS/Windows 文件系统一致）；
//   - 扩展名为 srt / ass / ssa / vtt（大小写不敏感）；
//   - 排除与视频同名的 .srt 本身（那是「同名字幕」，不算旁挂）；
//   - 排除应用自己的临时文件：生成流程的 pending 文件、写入器的 .cineinsight-tmp-* 临时文件、
//     翻译流程的 _translated_temp.srt；
//   - 排除目录与非普通文件（悬空符号链接等）；指向普通文件的符号链接算数。
var sidecarSubtitleExtensions = map[string]struct{}{".srt": {}, ".ass": {}, ".ssa": {}, ".vtt": {}}

// translatedTempSuffix 是翻译流程中间文件的后缀（小写）。
const translatedTempSuffix = "_translated_temp.srt"

// IsSidecarSubtitleName 只按文件名判断 name 是否是 videoPath 的旁挂字幕（不访问文件系统）。
func IsSidecarSubtitleName(videoPath, name string) bool {
	base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	if base == "" {
		return false
	}
	lower := strings.ToLower(name)
	prefix := strings.ToLower(base) + "."
	if !strings.HasPrefix(lower, prefix) {
		return false
	}
	if _, ok := sidecarSubtitleExtensions[filepath.Ext(lower)]; !ok {
		return false
	}
	if lower == prefix+"srt" {
		return false
	}
	if strings.HasSuffix(lower, strings.ToLower(subtitlePendingSuffix)) ||
		strings.HasSuffix(lower, translatedTempSuffix) ||
		strings.Contains(lower, ".cineinsight-tmp-") {
		return false
	}
	return true
}

// ListSidecarSubtitles 返回 videoPath 同目录下的旁挂字幕文件名（ReadDir 序）。
// 目录不存在按「没有」处理；其他读取失败返回错误，由调用方决定，不悄悄当作没有。
func ListSidecarSubtitles(videoPath string) ([]string, error) {
	directory := filepath.Dir(videoPath)
	entries, err := os.ReadDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取视频目录失败: %s", subtitleIOReason(err))
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !IsSidecarSubtitleName(videoPath, entry.Name()) {
			continue
		}
		info, err := os.Stat(filepath.Join(directory, entry.Name()))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

// HasSidecarSubtitle 报告 videoPath 同目录是否存在旁挂字幕。
func HasSidecarSubtitle(videoPath string) (bool, error) {
	names, err := ListSidecarSubtitles(videoPath)
	return len(names) > 0, err
}
