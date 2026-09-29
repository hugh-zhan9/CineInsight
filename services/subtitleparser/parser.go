package subtitleparser

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	blockSeparator   = regexp.MustCompile(`\n\s*\n`)
	timestampPattern = regexp.MustCompile(`(\d{2}):(\d{2}):(\d{2})[,.](\d{3})`)
	unicodeBOM       = "\ufeff"
)

// Segment is the Go-side structured subtitle model for downstream search/translation flows.
type Segment struct {
	Index       int      `json:"index"`
	StartTimeMs int64    `json:"start_time_ms"`
	EndTimeMs   int64    `json:"end_time_ms"`
	Text        string   `json:"text"`
	Lines       []string `json:"lines"`
}

func ParseFile(path string) ([]Segment, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// 经编码识别再解析：GBK/Big5/UTF-16 字幕不再被当成乱码索引（D-PC14）。
	// 识别不了编码时保持旧的宽松行为（按原字节解析，绝不报错）：检索/索引路径不能因为
	// 一份怪编码的 .srt 而失败；只有工作台打开与翻译读取才坚持 subtitle_encoding_not_utf8。
	text, _, err := DecodeSubtitleBytes(data)
	if err != nil {
		// 非法字节替换成 U+FFFD：PostgreSQL 的 text 列拒绝非法 UTF-8（SQLSTATE 22021），
		// 原样写入会让索引与回收站恢复在 PG 上失败；可读的部分照常可搜。
		text = strings.ToValidUTF8(string(data), "\uFFFD")
	}

	return Parse(text)
}

func SRTPathForVideo(videoPath string) string {
	return strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".srt"
}

func Parse(content string) ([]Segment, error) {
	normalized := normalizeContent(content)
	if normalized == "" {
		return nil, nil
	}

	blocks := blockSeparator.Split(normalized, -1)
	segments := make([]Segment, 0, len(blocks))

	for blockIndex, block := range blocks {
		segment, ok := parseBlock(blockIndex+1, block)
		if !ok {
			continue
		}
		segments = append(segments, segment)
	}

	return segments, nil
}

func normalizeContent(content string) string {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	normalized = strings.TrimSpace(strings.TrimPrefix(normalized, unicodeBOM))
	return normalized
}

func parseBlock(blockNumber int, block string) (Segment, bool) {
	lines := strings.Split(strings.TrimSpace(block), "\n")
	if len(lines) < 2 {
		return Segment{}, false
	}

	index := blockNumber
	timeLineIndex := 0
	if parsedIndex, err := strconv.Atoi(strings.TrimSpace(lines[0])); err == nil {
		index = parsedIndex
		timeLineIndex = 1
	}

	if len(lines) <= timeLineIndex+1 {
		return Segment{}, false
	}

	startMs, endMs, err := parseTimeRange(strings.TrimSpace(lines[timeLineIndex]))
	if err != nil {
		return Segment{}, false
	}

	if endMs < startMs {
		endMs = startMs
	}

	textLines := make([]string, 0, len(lines)-timeLineIndex-1)
	for _, line := range lines[timeLineIndex+1:] {
		textLines = append(textLines, strings.TrimSpace(line))
	}

	text := strings.TrimSpace(strings.Join(textLines, "\n"))
	if text == "" {
		return Segment{}, false
	}

	return Segment{
		Index:       index,
		StartTimeMs: startMs,
		EndTimeMs:   endMs,
		Text:        text,
		Lines:       textLines,
	}, true
}

func parseTimeRange(line string) (int64, int64, error) {
	parts := strings.Split(line, "-->")
	if len(parts) != 2 {
		return 0, 0, strconv.ErrSyntax
	}

	startMs, err := parseTimestamp(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, err
	}

	endMs, err := parseTimestamp(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, err
	}

	return startMs, endMs, nil
}

func parseTimestamp(value string) (int64, error) {
	matches := timestampPattern.FindStringSubmatch(value)
	if len(matches) != 5 {
		return 0, strconv.ErrSyntax
	}

	hours, _ := strconv.Atoi(matches[1])
	minutes, _ := strconv.Atoi(matches[2])
	seconds, _ := strconv.Atoi(matches[3])
	milliseconds, _ := strconv.Atoi(matches[4])

	total := int64(hours)*60*60*1000 +
		int64(minutes)*60*1000 +
		int64(seconds)*1000 +
		int64(milliseconds)

	return total, nil
}
