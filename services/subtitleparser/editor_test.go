package subtitleparser

import (
	"strings"
	"testing"
)

func TestParseStrictPreservesMultilineAndLongHours(t *testing.T) {
	content := "\ufeff7\r\n120:01:02,003 --> 120:01:04,005\r\nfirst line\r\n第二行\r\n"

	segments, err := ParseStrict([]byte(content))
	if err != nil {
		t.Fatalf("strict parse failed: %v", err)
	}
	if len(segments) != 1 {
		t.Fatalf("segment count = %d, want 1", len(segments))
	}
	if segments[0].Index != 7 || segments[0].StartTimeMs != 432062003 || segments[0].EndTimeMs != 432064005 {
		t.Fatalf("unexpected segment: %#v", segments[0])
	}
	if segments[0].Text != "first line\n第二行" {
		t.Fatalf("text = %q", segments[0].Text)
	}
}

func TestParseStrictRejectsMalformedBlockInsteadOfSkippingIt(t *testing.T) {
	content := strings.Join([]string{
		"1",
		"00:00:00,000 --> 00:00:01,000",
		"valid",
		"",
		"2",
		"not a timestamp",
		"must not disappear",
	}, "\n")

	if _, err := ParseStrict([]byte(content)); err == nil {
		t.Fatal("expected malformed block to fail strict parsing")
	}
}

func TestParseStrictRejectsEntriesThatCouldNeverBeSavedBack(t *testing.T) {
	cases := map[string]string{
		"empty text": strings.Join([]string{
			"1", "00:00:01,000 --> 00:00:02,000", "",
			"2", "00:00:03,000 --> 00:00:04,000", "present",
		}, "\n"),
		"oversized text": strings.Join([]string{
			"1", "00:00:01,000 --> 00:00:02,000", strings.Repeat("x", MaxEditorSegmentBytes+1),
		}, "\n"),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseStrict([]byte(content)); err == nil {
				t.Fatal("opening a document that can never be saved must fail")
			}
		})
	}
}

// MEDIA-06：零时长与结束早于开始的条目可以打开，问题以 issue 形式报告而不是拒绝加载。
func TestParseStrictOpensZeroDurationAndReversedEntriesAsIssuesMEDIA06(t *testing.T) {
	content := strings.Join([]string{
		"1", "00:00:05,000 --> 00:00:05,000", "instant",
		"",
		"2", "00:00:09,000 --> 00:00:08,000", "reversed",
		"",
		"3", "00:00:07,900 --> 00:00:10,000", "overlaps reversed end",
	}, "\n")

	segments, err := ParseStrict([]byte(content))
	if err != nil {
		t.Fatalf("零时长/倒置条目应当能打开: %v", err)
	}
	if len(segments) != 3 {
		t.Fatalf("segments = %#v", segments)
	}
	issues := DetectDocumentIssues(segments)
	want := []DocumentIssue{
		{Index: 1, Kind: DocumentIssueZeroDuration},
		{Index: 2, Kind: DocumentIssueEndBeforeStart},
		{Index: 3, Kind: DocumentIssueOverlap},
	}
	if len(issues) != len(want) {
		t.Fatalf("issues = %#v, want %#v", issues, want)
	}
	for i := range want {
		if issues[i] != want[i] {
			t.Fatalf("issues[%d] = %#v, want %#v", i, issues[i], want[i])
		}
	}
	if !hasEditorIssueCode(ValidateEditorSegments(segments), EditorIssueInvalidRange) {
		t.Fatal("保存前校验必须仍然拒绝 end<=start")
	}
}

func TestParseStrictAcceptsOverlapSoTheEditorCanFixIt(t *testing.T) {
	content := strings.Join([]string{
		"1", "00:00:00,000 --> 00:00:02,000", "first",
		"",
		"2", "00:00:01,500 --> 00:00:03,000", "second",
	}, "\n")

	segments, err := ParseStrict([]byte(content))
	if err != nil {
		t.Fatalf("overlap blocks saving, not opening: %v", err)
	}
	if len(segments) != 2 {
		t.Fatalf("segments = %#v", segments)
	}
	if !hasEditorIssueCode(ValidateEditorSegments(segments), EditorIssueOverlap) {
		t.Fatal("overlap must still be reported as a save-blocking issue")
	}
}

func TestValidateEditorSegmentsRejectsOverlapAndEmptyText(t *testing.T) {
	issues := ValidateEditorSegments([]EditorSegment{
		{ClientID: "a", StartTimeMs: 0, EndTimeMs: 2000, Text: "first"},
		{ClientID: "b", StartTimeMs: 1500, EndTimeMs: 3000, Text: "  "},
	})

	if !hasEditorIssueCode(issues, EditorIssueOverlap) {
		t.Fatalf("expected overlap issue, got %#v", issues)
	}
	if !hasEditorIssueCode(issues, EditorIssueEmptyText) {
		t.Fatalf("expected empty text issue, got %#v", issues)
	}
}

func TestSerializeEditorSegmentsRenumbersAndNormalizes(t *testing.T) {
	content, issues := SerializeEditorSegments([]EditorSegment{
		{ClientID: "first", StartTimeMs: 1000, EndTimeMs: 2500, Text: "hello\r\nworld"},
		{ClientID: "second", StartTimeMs: 2500, EndTimeMs: 3000, Text: "again"},
	})
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %#v", issues)
	}
	want := "1\n00:00:01,000 --> 00:00:02,500\nhello\nworld\n\n2\n00:00:02,500 --> 00:00:03,000\nagain\n"
	if string(content) != want {
		t.Fatalf("serialized content = %q, want %q", string(content), want)
	}
}

func hasEditorIssueCode(issues []EditorValidationIssue, code EditorIssueCode) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
