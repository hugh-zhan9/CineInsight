package subtitleparser

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	blockSeparator   = regexp.MustCompile(`\n\s*\n`)
	timestampPattern = regexp.MustCompile(`^(\d{2}):(\d{2}):(\d{2})[,.](\d{3})$`)
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

	return Parse(string(data))
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
		segment, err := parseBlock(blockIndex+1, block)
		if err != nil {
			return nil, err
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

func parseBlock(blockNumber int, block string) (Segment, error) {
	lines := strings.Split(strings.TrimSpace(block), "\n")
	if len(lines) < 3 {
		return Segment{}, fmt.Errorf("invalid srt block %d: expected at least 3 lines", blockNumber)
	}

	index, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return Segment{}, fmt.Errorf("invalid srt block %d index: %w", blockNumber, err)
	}

	startMs, endMs, err := parseTimeRange(strings.TrimSpace(lines[1]))
	if err != nil {
		return Segment{}, fmt.Errorf("invalid srt block %d time range: %w", blockNumber, err)
	}

	textLines := make([]string, 0, len(lines)-2)
	for _, line := range lines[2:] {
		textLines = append(textLines, strings.TrimSpace(line))
	}

	return Segment{
		Index:       index,
		StartTimeMs: startMs,
		EndTimeMs:   endMs,
		Text:        strings.Join(textLines, "\n"),
		Lines:       textLines,
	}, nil
}

func parseTimeRange(line string) (int64, int64, error) {
	parts := strings.Split(line, "-->")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected start --> end format")
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
		return 0, fmt.Errorf("invalid timestamp %q", value)
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
