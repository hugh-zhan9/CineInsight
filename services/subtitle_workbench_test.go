package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"
)

type subtitleWorkbenchTranslatorFunc func(context.Context, []string, string, string) ([]string, error)

func (f subtitleWorkbenchTranslatorFunc) Translate(ctx context.Context, texts []string, sourceLang, targetLang string) ([]string, error) {
	return f(ctx, texts, sourceLang, targetLang)
}

func TestSubtitleWorkbenchSaveReplacesSRTAndRefreshesIndexWithoutBackup(t *testing.T) {
	setupSubtitleSearchTestDB(t)
	video, srtPath := createSubtitleWorkbenchFixture(t)
	service := NewSubtitleWorkbenchService(nil)

	document, err := service.GetDocument(*video)
	if err != nil {
		t.Fatalf("get document: %v", err)
	}
	document.Entries[0].Text = "updated text"
	result, err := service.SaveDocument(*video, SubtitleSaveRequest{
		VideoID:     video.ID,
		Fingerprint: document.Fingerprint,
		Entries:     document.Entries,
	})
	if err != nil {
		t.Fatalf("save document: %v", err)
	}
	if result.Status != SubtitleSaveStatusSaved {
		t.Fatalf("status = %q, want saved; result=%#v", result.Status, result)
	}
	content, err := os.ReadFile(srtPath)
	if err != nil {
		t.Fatalf("read saved SRT: %v", err)
	}
	if !strings.Contains(string(content), "updated text") {
		t.Fatalf("saved content = %q", string(content))
	}
	matches, err := (&SubtitleSearchService{}).SearchSubtitleMatches("updated text", 10)
	if err != nil || len(matches) != 1 || matches[0].Video.ID != video.ID {
		t.Fatalf("subtitle index not refreshed: matches=%#v err=%v", matches, err)
	}
	backups, err := filepath.Glob(srtPath + "*")
	if err != nil {
		t.Fatalf("glob subtitle artifacts: %v", err)
	}
	if len(backups) != 1 || backups[0] != srtPath {
		t.Fatalf("unexpected backup/temp artifacts: %#v", backups)
	}
}

func TestSubtitleWorkbenchSaveRejectsExternalChange(t *testing.T) {
	setupSubtitleSearchTestDB(t)
	video, srtPath := createSubtitleWorkbenchFixture(t)
	service := NewSubtitleWorkbenchService(nil)
	document, err := service.GetDocument(*video)
	if err != nil {
		t.Fatalf("get document: %v", err)
	}
	if err := os.WriteFile(srtPath, []byte("1\n00:00:00,000 --> 00:00:01,000\nexternal\n"), 0644); err != nil {
		t.Fatalf("external write: %v", err)
	}
	originalModTime := time.Unix(0, document.Fingerprint.ModTimeNS)
	if err := os.Chtimes(srtPath, originalModTime, originalModTime); err != nil {
		t.Fatalf("restore source modtime: %v", err)
	}
	info, err := os.Stat(srtPath)
	if err != nil {
		t.Fatalf("stat externally changed SRT: %v", err)
	}
	if info.Size() != document.Fingerprint.Size || info.ModTime().UnixNano() != document.Fingerprint.ModTimeNS {
		t.Fatalf("test setup must preserve size and modtime: stat=%d/%d fingerprint=%d/%d", info.Size(), info.ModTime().UnixNano(), document.Fingerprint.Size, document.Fingerprint.ModTimeNS)
	}
	document.Entries[0].Text = "editor"

	result, err := service.SaveDocument(*video, SubtitleSaveRequest{
		VideoID:     video.ID,
		Fingerprint: document.Fingerprint,
		Entries:     document.Entries,
	})
	if err != nil {
		t.Fatalf("save conflict should be a structured result: %v", err)
	}
	if result.Status != SubtitleSaveStatusRejected || result.ErrorCode != SubtitleWorkbenchErrorConflict {
		t.Fatalf("unexpected result: %#v", result)
	}
	content, _ := os.ReadFile(srtPath)
	if !strings.Contains(string(content), "external") || strings.Contains(string(content), "editor") {
		t.Fatalf("external content was overwritten: %q", string(content))
	}
}

func TestSubtitleWorkbenchReportsSavedWhenIndexRefreshFails(t *testing.T) {
	setupSubtitleSearchTestDB(t)
	video, srtPath := createSubtitleWorkbenchFixture(t)
	service := NewSubtitleWorkbenchService(nil)
	document, err := service.GetDocument(*video)
	if err != nil {
		t.Fatalf("get document: %v", err)
	}
	document.Entries[0].Text = "saved despite index error"
	sqlDB, err := database.DB.DB()
	if err != nil {
		t.Fatalf("get SQL database: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close SQL database: %v", err)
	}

	result, err := service.SaveDocument(*video, SubtitleSaveRequest{
		VideoID: video.ID, Fingerprint: document.Fingerprint, Entries: document.Entries,
	})
	if err != nil {
		t.Fatalf("save document: %v", err)
	}
	if result.Status != SubtitleSaveStatusIndexPending || result.Fingerprint == nil {
		t.Fatalf("unexpected result: %#v", result)
	}
	content, err := os.ReadFile(srtPath)
	if err != nil {
		t.Fatalf("read saved SRT: %v", err)
	}
	if !strings.Contains(string(content), "saved despite index error") {
		t.Fatalf("file save should remain authoritative: %q", string(content))
	}
}

func TestSubtitleWorkbenchReplaceFailureLeavesOriginalUntouched(t *testing.T) {
	setupSubtitleSearchTestDB(t)
	video, srtPath := createSubtitleWorkbenchFixture(t)
	service := NewSubtitleWorkbenchService(nil)
	service.replaceFile = func(_, _ string) error { return errors.New("replace failed") }
	document, err := service.GetDocument(*video)
	if err != nil {
		t.Fatalf("get document: %v", err)
	}
	document.Entries[0].Text = "editor"

	result, err := service.SaveDocument(*video, SubtitleSaveRequest{
		VideoID:     video.ID,
		Fingerprint: document.Fingerprint,
		Entries:     document.Entries,
	})
	if err != nil {
		t.Fatalf("replace failure should be a structured result: %v", err)
	}
	if result.Status != SubtitleSaveStatusRejected || result.ErrorCode != SubtitleWorkbenchErrorReplaceFailed {
		t.Fatalf("unexpected result: %#v", result)
	}
	content, _ := os.ReadFile(srtPath)
	if !strings.Contains(string(content), "original") || strings.Contains(string(content), "editor") {
		t.Fatalf("original changed after replace failure: %q", string(content))
	}
	artifacts, _ := filepath.Glob(filepath.Join(filepath.Dir(srtPath), ".movie.srt.tmp-*"))
	if len(artifacts) != 0 {
		t.Fatalf("temporary files leaked: %#v", artifacts)
	}
}

func TestSubtitleWorkbenchRetranslateIsAllOrNothing(t *testing.T) {
	service := NewSubtitleWorkbenchService(nil)
	service.translatorFactory = func(SubtitleTranslationConfig) (SubtitleTranslator, error) {
		return subtitleWorkbenchTranslatorFunc(func(_ context.Context, texts []string, _, _ string) ([]string, error) {
			return []string{"only one"}, nil
		}), nil
	}
	request := SubtitleRetranslateRequest{
		VideoID:    1,
		SourceLang: "en",
		TargetLang: "zh",
		Entries: []SubtitleRetranslateEntry{
			{ClientID: "a", Text: "one"},
			{ClientID: "b", Text: "two"},
		},
	}
	if _, err := service.Retranslate(context.Background(), request, SubtitleTranslationConfig{}); err == nil {
		t.Fatal("expected mismatched translation count to fail")
	}
}

func TestSubtitleWorkbenchRefusesSymlinkedSubtitle(t *testing.T) {
	setupSubtitleSearchTestDB(t)
	video, srtPath := createSubtitleWorkbenchFixture(t)
	realDir := t.TempDir()
	realTarget := filepath.Join(realDir, "movie.zh.srt")
	original := []byte("1\n00:00:00,000 --> 00:00:01,000\nreal target\n")
	if err := os.WriteFile(realTarget, original, 0644); err != nil {
		t.Fatalf("write link target: %v", err)
	}
	if err := os.Remove(srtPath); err != nil {
		t.Fatalf("remove plain SRT: %v", err)
	}
	if err := os.Symlink(realTarget, srtPath); err != nil {
		t.Fatalf("symlink SRT: %v", err)
	}

	service := NewSubtitleWorkbenchService(nil)
	if _, err := service.GetDocument(*video); err == nil {
		t.Fatal("editing a symlinked subtitle must be refused")
	}

	// Even a caller holding a stale fingerprint must not be able to clobber the link.
	result, err := service.SaveDocument(*video, SubtitleSaveRequest{
		VideoID: video.ID,
		Entries: []subtitleparser.EditorSegment{{StartTimeMs: 0, EndTimeMs: 1000, Text: "edited"}},
	})
	if err == nil && result != nil && result.Status == SubtitleSaveStatusSaved {
		t.Fatal("saving through a symlink must not report success")
	}
	info, err := os.Lstat(srtPath)
	if err != nil {
		t.Fatalf("lstat subtitle path: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced by a regular file")
	}
	content, err := os.ReadFile(realTarget)
	if err != nil {
		t.Fatalf("read link target: %v", err)
	}
	if string(content) != string(original) {
		t.Fatalf("link target content changed: %q", string(content))
	}
}

func createSubtitleWorkbenchFixture(t *testing.T) (*models.Video, string) {
	t.Helper()
	root := t.TempDir()
	videoPath := filepath.Join(root, "movie.mp4")
	srtPath := filepath.Join(root, "movie.srt")
	if err := os.WriteFile(videoPath, []byte("video"), 0644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	if err := os.WriteFile(srtPath, []byte("1\n00:00:00,000 --> 00:00:01,000\noriginal\n"), 0644); err != nil {
		t.Fatalf("write SRT: %v", err)
	}
	video := &models.Video{Name: "movie.mp4", Path: videoPath, Directory: root, Size: 5}
	if err := database.DB.Create(video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	return video, srtPath
}
