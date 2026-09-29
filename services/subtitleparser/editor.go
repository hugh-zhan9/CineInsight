package subtitleparser

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const (
	MaxEditorFileBytes    = 16 << 20
	MaxEditorSegments     = 100_000
	MaxEditorSegmentBytes = 64 << 10
)

var strictTimestampPattern = regexp.MustCompile(`^(\d{2,}):([0-5]\d):([0-5]\d)[,.](\d{3})$`)

type EditorIssueCode string

const (
	EditorIssueEmptyDocument   EditorIssueCode = "empty_document"
	EditorIssueTooManySegments EditorIssueCode = "too_many_segments"
	EditorIssueMissingClientID EditorIssueCode = "missing_client_id"
	EditorIssueDuplicateID     EditorIssueCode = "duplicate_client_id"
	EditorIssueNegativeTime    EditorIssueCode = "negative_time"
	EditorIssueInvalidRange    EditorIssueCode = "invalid_time_range"
	EditorIssueOverlap         EditorIssueCode = "overlap"
	EditorIssueEmptyText       EditorIssueCode = "empty_text"
	EditorIssueInvalidText     EditorIssueCode = "invalid_text"
	EditorIssueTextTooLarge    EditorIssueCode = "text_too_large"
	EditorIssueFileTooLarge    EditorIssueCode = "file_too_large"
)

type EditorSegment struct {
	Index       int    `json:"index,omitempty"`
	ClientID    string `json:"client_id"`
	StartTimeMs int64  `json:"start_time_ms"`
	EndTimeMs   int64  `json:"end_time_ms"`
	Text        string `json:"text"`
}

type EditorValidationIssue struct {
	EntryIndex int             `json:"entry_index,omitempty"`
	ClientID   string          `json:"client_id,omitempty"`
	Code       EditorIssueCode `json:"code"`
	Message    string          `json:"message"`
}

func ParseStrict(content []byte) ([]EditorSegment, error) {
	if len(content) > MaxEditorFileBytes {
		return nil, fmt.Errorf("字幕文件超过编辑器 %d 字节的上限", MaxEditorFileBytes)
	}
	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	normalized = strings.TrimPrefix(normalized, unicodeBOM)
	lines := strings.Split(normalized, "\n")

	blocks := make([][]string, 0)
	current := make([]string, 0)
	flush := func() {
		if len(current) == 0 {
			return
		}
		blocks = append(blocks, current)
		current = nil
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		current = append(current, line)
	}
	flush()
	if len(blocks) == 0 {
		return nil, fmt.Errorf("字幕文件是空的")
	}
	if len(blocks) > MaxEditorSegments {
		return nil, fmt.Errorf("字幕包含 %d 条，超过上限 %d 条", len(blocks), MaxEditorSegments)
	}

	segments := make([]EditorSegment, 0, len(blocks))
	for blockIndex, block := range blocks {
		if len(block) < 2 {
			return nil, fmt.Errorf("字幕第 %d 条缺少时间轴", blockIndex+1)
		}
		index, err := strconv.Atoi(strings.TrimSpace(block[0]))
		if err != nil || index < 1 {
			return nil, fmt.Errorf("字幕第 %d 条的序号无效", blockIndex+1)
		}
		start, end, err := parseStrictTimeRange(strings.TrimSpace(block[1]))
		if err != nil {
			return nil, fmt.Errorf("字幕第 %d 条的时间轴格式无效", blockIndex+1)
		}
		text := ""
		if len(block) > 2 {
			text = strings.Join(block[2:], "\n")
		}
		// Enforce the D-002 hard limits here so the editor never opens a document that
		// save validation would reject forever. Overlap and end<=start are deliberately
		// not checked: they block saving but are meant to be fixed inside the workbench
		// (reported through DetectDocumentIssues, D-PC15).
		normalized := normalizeEditorText(text)
		if strings.TrimSpace(normalized) == "" {
			return nil, fmt.Errorf("字幕第 %d 条没有文本", blockIndex+1)
		}
		if len([]byte(normalized)) > MaxEditorSegmentBytes {
			return nil, fmt.Errorf("字幕第 %d 条文本超过 %d 字节", blockIndex+1, MaxEditorSegmentBytes)
		}
		segments = append(segments, EditorSegment{
			Index:       index,
			ClientID:    fmt.Sprintf("cue-%d", blockIndex+1),
			StartTimeMs: start,
			EndTimeMs:   end,
			Text:        text,
		})
	}
	return segments, nil
}

// DocumentIssueKind 是打开字幕时报告的格式问题种类（D-PC15）。
type DocumentIssueKind string

const (
	DocumentIssueZeroDuration   DocumentIssueKind = "zero_duration"
	DocumentIssueEndBeforeStart DocumentIssueKind = "end_before_start"
	DocumentIssueOverlap        DocumentIssueKind = "overlap"
)

// DocumentIssue 指出第几条（从 1 起）有什么问题。它随文档一起返回，不阻止打开。
type DocumentIssue struct {
	Index int               `json:"index"`
	Kind  DocumentIssueKind `json:"kind"`
}

// DetectDocumentIssues 列出零时长、结束早于开始、与上一条重叠三类问题。
// 一条字幕可以同时命中时长问题与重叠。
func DetectDocumentIssues(segments []EditorSegment) []DocumentIssue {
	issues := make([]DocumentIssue, 0)
	for i, segment := range segments {
		switch {
		case segment.EndTimeMs == segment.StartTimeMs:
			issues = append(issues, DocumentIssue{Index: i + 1, Kind: DocumentIssueZeroDuration})
		case segment.EndTimeMs < segment.StartTimeMs:
			issues = append(issues, DocumentIssue{Index: i + 1, Kind: DocumentIssueEndBeforeStart})
		}
		if i > 0 && segment.StartTimeMs < segments[i-1].EndTimeMs {
			issues = append(issues, DocumentIssue{Index: i + 1, Kind: DocumentIssueOverlap})
		}
	}
	return issues
}

func ValidateEditorSegments(segments []EditorSegment) []EditorValidationIssue {
	issues := make([]EditorValidationIssue, 0)
	if len(segments) == 0 {
		return append(issues, EditorValidationIssue{Code: EditorIssueEmptyDocument, Message: "字幕至少需要一条内容"})
	}
	if len(segments) > MaxEditorSegments {
		issues = append(issues, EditorValidationIssue{Code: EditorIssueTooManySegments, Message: fmt.Sprintf("字幕条数不能超过 %d 条", MaxEditorSegments)})
	}

	seenIDs := make(map[string]struct{}, len(segments))
	for i, segment := range segments {
		entryIndex := i + 1
		clientID := strings.TrimSpace(segment.ClientID)
		if clientID == "" {
			issues = append(issues, editorIssue(entryIndex, clientID, EditorIssueMissingClientID, "缺少条目标识"))
		} else if _, exists := seenIDs[clientID]; exists {
			issues = append(issues, editorIssue(entryIndex, clientID, EditorIssueDuplicateID, "条目标识重复"))
		} else {
			seenIDs[clientID] = struct{}{}
		}
		if segment.StartTimeMs < 0 || segment.EndTimeMs < 0 {
			issues = append(issues, editorIssue(entryIndex, clientID, EditorIssueNegativeTime, "时间不能为负数"))
		}
		if segment.EndTimeMs <= segment.StartTimeMs {
			issues = append(issues, editorIssue(entryIndex, clientID, EditorIssueInvalidRange, "结束时间必须晚于开始时间"))
		}
		if i > 0 && segment.StartTimeMs < segments[i-1].EndTimeMs {
			issues = append(issues, editorIssue(entryIndex, clientID, EditorIssueOverlap, "与上一条字幕时间重叠"))
		}
		normalizedText := normalizeEditorText(segment.Text)
		if strings.TrimSpace(normalizedText) == "" {
			issues = append(issues, editorIssue(entryIndex, clientID, EditorIssueEmptyText, "字幕文本不能为空"))
		}
		if len([]byte(normalizedText)) > MaxEditorSegmentBytes {
			issues = append(issues, editorIssue(entryIndex, clientID, EditorIssueTextTooLarge, fmt.Sprintf("字幕文本超过 %d 字节", MaxEditorSegmentBytes)))
		}
		for _, line := range strings.Split(normalizedText, "\n") {
			if strings.TrimSpace(line) == "" {
				issues = append(issues, editorIssue(entryIndex, clientID, EditorIssueInvalidText, "字幕文本中不能包含空行"))
				break
			}
		}
	}
	return issues
}

func SerializeEditorSegments(segments []EditorSegment) ([]byte, []EditorValidationIssue) {
	issues := ValidateEditorSegments(segments)
	if len(issues) != 0 {
		return nil, issues
	}

	var builder strings.Builder
	for i, segment := range segments {
		builder.WriteString(strconv.Itoa(i + 1))
		builder.WriteByte('\n')
		builder.WriteString(formatEditorTimestamp(segment.StartTimeMs))
		builder.WriteString(" --> ")
		builder.WriteString(formatEditorTimestamp(segment.EndTimeMs))
		builder.WriteByte('\n')
		builder.WriteString(normalizeEditorText(segment.Text))
		builder.WriteByte('\n')
		if i < len(segments)-1 {
			builder.WriteByte('\n')
		}
		if builder.Len() > MaxEditorFileBytes {
			return nil, []EditorValidationIssue{{Code: EditorIssueFileTooLarge, Message: fmt.Sprintf("字幕文件超过编辑器 %d 字节的上限", MaxEditorFileBytes)}}
		}
	}
	return []byte(builder.String()), nil
}

func parseStrictTimeRange(line string) (int64, int64, error) {
	parts := strings.Split(line, "-->")
	if len(parts) != 2 {
		return 0, 0, strconv.ErrSyntax
	}
	start, err := parseStrictTimestamp(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, err
	}
	end, err := parseStrictTimestamp(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, err
	}
	return start, end, nil
}

func parseStrictTimestamp(value string) (int64, error) {
	matches := strictTimestampPattern.FindStringSubmatch(value)
	if len(matches) != 5 {
		return 0, strconv.ErrSyntax
	}
	hours, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil || hours > math.MaxInt64/3_600_000 {
		return 0, strconv.ErrRange
	}
	minutes, _ := strconv.ParseInt(matches[2], 10, 64)
	seconds, _ := strconv.ParseInt(matches[3], 10, 64)
	milliseconds, _ := strconv.ParseInt(matches[4], 10, 64)
	return hours*3_600_000 + minutes*60_000 + seconds*1_000 + milliseconds, nil
}

func formatEditorTimestamp(value int64) string {
	hours := value / 3_600_000
	value %= 3_600_000
	minutes := value / 60_000
	value %= 60_000
	seconds := value / 1_000
	milliseconds := value % 1_000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", hours, minutes, seconds, milliseconds)
}

func normalizeEditorText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Trim(text, "\n")
}

func editorIssue(entryIndex int, clientID string, code EditorIssueCode, message string) EditorValidationIssue {
	return EditorValidationIssue{EntryIndex: entryIndex, ClientID: clientID, Code: code, Message: message}
}
