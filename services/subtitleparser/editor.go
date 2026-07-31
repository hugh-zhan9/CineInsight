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
		return nil, fmt.Errorf("subtitle exceeds %d byte editor limit", MaxEditorFileBytes)
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
		return nil, fmt.Errorf("subtitle is empty")
	}
	if len(blocks) > MaxEditorSegments {
		return nil, fmt.Errorf("subtitle contains %d entries, limit is %d", len(blocks), MaxEditorSegments)
	}

	segments := make([]EditorSegment, 0, len(blocks))
	for blockIndex, block := range blocks {
		if len(block) < 2 {
			return nil, fmt.Errorf("subtitle block %d is missing a timestamp", blockIndex+1)
		}
		index, err := strconv.Atoi(strings.TrimSpace(block[0]))
		if err != nil || index < 1 {
			return nil, fmt.Errorf("subtitle block %d has an invalid index", blockIndex+1)
		}
		start, end, err := parseStrictTimeRange(strings.TrimSpace(block[1]))
		if err != nil {
			return nil, fmt.Errorf("subtitle block %d has an invalid timestamp: %w", blockIndex+1, err)
		}
		text := ""
		if len(block) > 2 {
			text = strings.Join(block[2:], "\n")
		}
		// Enforce the D-002 hard limits here so the editor never opens a document that
		// save validation would reject forever. Overlap is deliberately not checked:
		// it blocks saving but is meant to be fixed inside the workbench.
		if end <= start {
			return nil, fmt.Errorf("subtitle block %d ends before it starts", blockIndex+1)
		}
		normalized := normalizeEditorText(text)
		if strings.TrimSpace(normalized) == "" {
			return nil, fmt.Errorf("subtitle block %d has no text", blockIndex+1)
		}
		if len([]byte(normalized)) > MaxEditorSegmentBytes {
			return nil, fmt.Errorf("subtitle block %d text exceeds %d bytes", blockIndex+1, MaxEditorSegmentBytes)
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

func ValidateEditorSegments(segments []EditorSegment) []EditorValidationIssue {
	issues := make([]EditorValidationIssue, 0)
	if len(segments) == 0 {
		return append(issues, EditorValidationIssue{Code: EditorIssueEmptyDocument, Message: "subtitle must contain at least one entry"})
	}
	if len(segments) > MaxEditorSegments {
		issues = append(issues, EditorValidationIssue{Code: EditorIssueTooManySegments, Message: fmt.Sprintf("subtitle entry limit is %d", MaxEditorSegments)})
	}

	seenIDs := make(map[string]struct{}, len(segments))
	for i, segment := range segments {
		entryIndex := i + 1
		clientID := strings.TrimSpace(segment.ClientID)
		if clientID == "" {
			issues = append(issues, editorIssue(entryIndex, clientID, EditorIssueMissingClientID, "entry client ID is required"))
		} else if _, exists := seenIDs[clientID]; exists {
			issues = append(issues, editorIssue(entryIndex, clientID, EditorIssueDuplicateID, "entry client ID must be unique"))
		} else {
			seenIDs[clientID] = struct{}{}
		}
		if segment.StartTimeMs < 0 || segment.EndTimeMs < 0 {
			issues = append(issues, editorIssue(entryIndex, clientID, EditorIssueNegativeTime, "entry times must be non-negative"))
		}
		if segment.EndTimeMs <= segment.StartTimeMs {
			issues = append(issues, editorIssue(entryIndex, clientID, EditorIssueInvalidRange, "entry end time must be after start time"))
		}
		if i > 0 && segment.StartTimeMs < segments[i-1].EndTimeMs {
			issues = append(issues, editorIssue(entryIndex, clientID, EditorIssueOverlap, "entry overlaps the previous entry"))
		}
		normalizedText := normalizeEditorText(segment.Text)
		if strings.TrimSpace(normalizedText) == "" {
			issues = append(issues, editorIssue(entryIndex, clientID, EditorIssueEmptyText, "entry text is required"))
		}
		if len([]byte(normalizedText)) > MaxEditorSegmentBytes {
			issues = append(issues, editorIssue(entryIndex, clientID, EditorIssueTextTooLarge, fmt.Sprintf("entry text exceeds %d bytes", MaxEditorSegmentBytes)))
		}
		for _, line := range strings.Split(normalizedText, "\n") {
			if strings.TrimSpace(line) == "" {
				issues = append(issues, editorIssue(entryIndex, clientID, EditorIssueInvalidText, "entry text cannot contain a blank separator line"))
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
			return nil, []EditorValidationIssue{{Code: EditorIssueFileTooLarge, Message: fmt.Sprintf("subtitle exceeds %d byte editor limit", MaxEditorFileBytes)}}
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
