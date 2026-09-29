package services

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 旁挂字幕判定（D-PC17）：「视频同目录有没有旁挂字幕」全仓只在这里定义一次。
// 字幕生成/翻译的缺失提示、后续 subtitle_index_states.has_sidecar 的写入都必须调用这里的函数，
// 不得各自再写目录扫描。规则：
//   - 同目录，文件名以「视频基本名 + .」开头（大小写不敏感，与 macOS/Windows 文件系统一致）；
//   - 扩展名为 srt / ass / ssa / vtt（大小写不敏感）；
//   - 排除与视频同名的 .srt 本身（那是「同名字幕」，不算旁挂）；
//   - 排除应用自己的临时文件：生成流程的 pending 文件（现行的隐藏名
//     `.<基本名>.cineinsight-pending.srt` 与旧名 `<基本名>.cineinsight-pending.srt`，按后缀一并排除）、
//     写入器的 .cineinsight-tmp-* 临时文件、翻译流程的 _translated_temp.srt；
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

// ListSidecarSubtitles 返回 videoPath 同目录下的旁挂字幕文件名（按文件名排序，与 os.ReadDir 同序）。
// 目录不存在按「没有」处理；其他读取失败返回错误，由调用方决定，不悄悄当作没有。
func ListSidecarSubtitles(videoPath string) ([]string, error) {
	listing, err := readSubtitleSidecarDirListing(filepath.Dir(videoPath))
	if err != nil {
		return nil, err
	}
	return listing.sidecarSubtitles(videoPath, false), nil
}

// HasSidecarSubtitle 报告 videoPath 同目录是否存在旁挂字幕。
func HasSidecarSubtitle(videoPath string) (bool, error) {
	listing, err := readSubtitleSidecarDirListing(filepath.Dir(videoPath))
	if err != nil {
		return false, err
	}
	return len(listing.sidecarSubtitles(videoPath, true)) > 0, nil
}

// readSubtitleSidecarDir 是旁挂字幕判定读视频目录的唯一入口，测试用它数读目录的次数。
var readSubtitleSidecarDir = os.ReadDir

// subtitleSidecarDirListing 是读一次目录得到的候选：非目录项的名字，按小写名排序，
// 供按「基本名 + .」前缀二分。「读目录 + 只认普通文件」全仓只在这里实现（m8）：单条判定
// （ListSidecarSubtitles / HasSidecarSubtitle）与全库同步的按目录缓存（subtitleSidecarDirCache）共用。
type subtitleSidecarDirListing struct {
	directory string
	// names 与 lower 一一对应。
	names []string
	lower []string
}

// readSubtitleSidecarDirListing 读 directory 一次。目录不存在按「没有」处理（空列表）；
// 其他读取失败返回错误。
func readSubtitleSidecarDirListing(directory string) (*subtitleSidecarDirListing, error) {
	listing := &subtitleSidecarDirListing{directory: directory}
	entries, err := readSubtitleSidecarDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return listing, nil
		}
		return nil, fmt.Errorf("读取视频目录失败: %s", subtitleIOReason(err))
	}
	type entryName struct{ name, lower string }
	kept := make([]entryName, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		kept = append(kept, entryName{name: entry.Name(), lower: strings.ToLower(entry.Name())})
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].lower < kept[j].lower })
	listing.names = make([]string, len(kept))
	listing.lower = make([]string, len(kept))
	for index, entry := range kept {
		listing.names[index], listing.lower[index] = entry.name, entry.lower
	}
	return listing, nil
}

// sidecarSubtitles 返回这个目录里属于 videoPath 的旁挂字幕文件名，按文件名排序（与 os.ReadDir 同序）；
// firstOnly 时找到一个就返回。名字经 IsSidecarSubtitleName 判定，且跟随符号链接后必须是普通文件
// （悬空链接、指向目录的链接都不算）。
func (l *subtitleSidecarDirListing) sidecarSubtitles(videoPath string, firstOnly bool) []string {
	// 旁挂字幕的名字一定以「视频基本名 + .」开头（大小写不敏感）：只看这一段前缀的候选。
	base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	if base == "" {
		return nil
	}
	prefix := strings.ToLower(base) + "."
	var names []string
	for index := sort.SearchStrings(l.lower, prefix); index < len(l.lower) && strings.HasPrefix(l.lower[index], prefix); index++ {
		name := l.names[index]
		if !IsSidecarSubtitleName(videoPath, name) {
			continue
		}
		if info, err := os.Stat(filepath.Join(l.directory, name)); err != nil || !info.Mode().IsRegular() {
			continue
		}
		names = append(names, name)
		if firstOnly {
			break
		}
	}
	sort.Strings(names)
	return names
}

// subtitleSidecarDirCache 是一轮全库同步内按目录缓存的目录项（M-6）：同一目录下有上千个视频时，
// 逐条调用 HasSidecarSubtitle 等于把整个目录读上千遍（视频数 × 目录项数）。判定与单条入口是同一份
// （subtitleSidecarDirListing），这里只负责一个目录在这一轮里只读一次。
type subtitleSidecarDirCache struct {
	dirs map[string]subtitleSidecarDirCacheEntry
}

type subtitleSidecarDirCacheEntry struct {
	listing *subtitleSidecarDirListing
	err     error
}

func newSubtitleSidecarDirCache() *subtitleSidecarDirCache {
	return &subtitleSidecarDirCache{dirs: map[string]subtitleSidecarDirCacheEntry{}}
}

// hasSidecar 与 HasSidecarSubtitle(videoPath) 同义，目录只在这一轮里读一次（读取失败同样缓存）。
func (c *subtitleSidecarDirCache) hasSidecar(videoPath string) (bool, error) {
	directory := filepath.Dir(videoPath)
	cached, ok := c.dirs[directory]
	if !ok {
		cached.listing, cached.err = readSubtitleSidecarDirListing(directory)
		c.dirs[directory] = cached
	}
	if cached.err != nil {
		return false, cached.err
	}
	return len(cached.listing.sidecarSubtitles(videoPath, true)) > 0, nil
}
