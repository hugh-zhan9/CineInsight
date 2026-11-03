package services

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"
)

func TestManualChaptersCASBoundsAndDeletedMediaRetention(t *testing.T) {
	video, source := notesFixture(t)
	ctx := context.Background()
	service := NewChapterService(nil)
	chapters := []ChapterInput{{Title: "开篇", StartMS: 0, EndMS: 1000}, {Title: "尾声", StartMS: 2000, EndMS: 3000, Summary: "人工说明"}}
	saved, err := service.SaveVideoChapters(ctx, SaveChaptersRequest{VideoID: video.ID, SourceVersion: source, Chapters: chapters})
	if err != nil || saved.Revision != 1 || len(saved.Chapters) != 2 {
		t.Fatalf("saved %+v %v", saved, err)
	}
	if _, err := service.SaveVideoChapters(ctx, SaveChaptersRequest{VideoID: video.ID, SourceVersion: source, Chapters: chapters}); !errors.Is(err, ErrChapterConflict) {
		t.Fatalf("stale create %v", err)
	}
	invalid := append([]ChapterInput{}, chapters...)
	invalid[1].StartMS = 500
	if _, err := service.SaveVideoChapters(ctx, SaveChaptersRequest{VideoID: video.ID, SourceVersion: source, ExpectedRevision: 1, Chapters: invalid}); !errors.Is(err, ErrChapterInvalid) {
		t.Fatalf("overlap %v", err)
	}
	if _, err := service.SaveVideoChapters(ctx, SaveChaptersRequest{VideoID: video.ID, SourceVersion: source, ExpectedRevision: 1}); !errors.Is(err, ErrChapterInvalid) {
		t.Fatalf("implicit clear %v", err)
	}
	resolved, err := service.ResolveChapter(ctx, ResolveChapterRequest{SetID: saved.SetID, ChapterID: saved.Chapters[0].ID, Revision: saved.Revision})
	if err != nil || resolved.SourceVersion != source || resolved.StartMS != 0 {
		t.Fatalf("resolve %+v %v", resolved, err)
	}
	if err := database.DB.Unscoped().Delete(&models.Video{}, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	page, err := service.GetVideoChapterSets(ctx, video.ID)
	if err != nil || page.Manual == nil || len(page.Manual.Chapters) != 2 || page.Manual.SourceStatus != "unavailable" {
		t.Fatalf("retained %+v %v", page, err)
	}
	if _, err := service.ResolveChapter(ctx, ResolveChapterRequest{SetID: saved.SetID, ChapterID: saved.Chapters[0].ID, Revision: saved.Revision}); !errors.Is(err, ErrBookmarkSourceUnavailable) {
		t.Fatalf("deleted source %v", err)
	}
}
func TestChaptersDoNotAcceptChangedSourceOrUnknownDuration(t *testing.T) {
	video, source := notesFixture(t)
	service := NewChapterService(nil)
	ctx := context.Background()
	request := SaveChaptersRequest{VideoID: video.ID, SourceVersion: source, Chapters: []ChapterInput{{Title: "章", StartMS: 0, EndMS: 1000}}}
	if err := database.DB.Model(&video).Update("duration", 0).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveVideoChapters(ctx, request); !errors.Is(err, ErrChapterDurationUnknown) {
		t.Fatalf("unknown duration %v", err)
	}
	if err := database.DB.Model(&video).Update("duration", 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(video.Path, []byte("replaced media"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveVideoChapters(ctx, request); !errors.Is(err, ErrBookmarkSourceChanged) {
		t.Fatalf("old source %v", err)
	}
}
func TestProbeChaptersDistinguishesUnreadEmptyAndMalformed(t *testing.T) {
	unread, err := parseMediaProbeOutput([]byte(`{"format":{"duration":"10"}}`))
	if err != nil || unread.ChaptersJSON != nil {
		t.Fatalf("unread %+v %v", unread, err)
	}
	empty, err := parseMediaProbeOutput([]byte(`{"format":{"duration":"10"},"chapters":[]}`))
	if err != nil || empty.ChaptersJSON == nil || *empty.ChaptersJSON != "[]" {
		t.Fatalf("empty %+v %v", empty, err)
	}
	parsed, err := parseMediaProbeOutput([]byte(`{"format":{"duration":"10"},"chapters":[{"start_time":"0.125","end_time":"4.500","tags":{"title":"开篇"}}]}`))
	if err != nil || parsed.ChaptersJSON == nil {
		t.Fatalf("parsed %+v %v", parsed, err)
	}
	for _, body := range []string{`{"chapters":[{"start_time":"NaN","end_time":"5"}]}`, `{"chapters":[{"start_time":"4","end_time":"3"}]}`} {
		if parsed, err := parseMediaProbeOutput([]byte(body)); err != nil || parsed.ChaptersError != "chapter_metadata_invalid" || parsed.ChaptersJSON != nil {
			t.Fatalf("bad chapter projection not isolated: %+v %v", parsed, err)
		}
	}
}

func TestProbeChaptersDoNotInvalidateUsableTechnicalSnapshot(t *testing.T) {
	parsed, err := parseMediaProbeOutput([]byte(`{"format":{"format_name":"mp4","duration":"10"},"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":640,"height":360}],"chapters":[{"start_time":"NaN","end_time":"5"}]}`))
	if err != nil || parsed.PrimaryVideo == nil || parsed.PrimaryVideo.CodecName != "h264" || parsed.Duration == nil || *parsed.Duration != 10 {
		t.Fatalf("chapter error broke technical snapshot: %+v %v", parsed, err)
	}
}

func TestChapterProbePersistsInvalidProjectionWithoutLosingTechnicalFields(t *testing.T) {
	video, _ := notesFixture(t)
	probe := newMediaProbeServiceWithRunner(func(context.Context, string) ([]byte, string, error) {
		return []byte(`{"format":{"format_name":"mp4","duration":"10"},"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":640,"height":360}],"chapters":[{"start_time":"NaN","end_time":"5"}]}`), "", nil
	})
	if err := probe.Refresh(context.Background(), video.ID); err != nil {
		t.Fatal(err)
	}
	var metadata models.VideoTechnicalMetadata
	if err := database.DB.First(&metadata, video.ID).Error; err != nil {
		t.Fatal(err)
	}
	if metadata.FormatName != "mp4" || metadata.ChaptersError != "chapter_metadata_invalid" || metadata.ChaptersJSON != nil {
		t.Fatalf("projection %+v", metadata)
	}
	sets, err := NewChapterService(nil).GetVideoChapterSets(context.Background(), video.ID)
	if err != nil || sets.Embedded.SourceStatus != "invalid" || sets.DurationMS != 10000 {
		t.Fatalf("view %+v %v", sets, err)
	}
}
func TestChapterRealFFProbeAndManualReprobeIsolation(t *testing.T) {
	video, source := notesFixture(t)
	service := NewChapterService(nil)
	ctx := context.Background()
	manual, err := service.SaveVideoChapters(ctx, SaveChaptersRequest{VideoID: video.ID, SourceVersion: source, Chapters: []ChapterInput{{Title: "人工章节保留", StartMS: 0, EndMS: 1000}}})
	if err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(t.TempDir(), "chapters.ffmeta")
	if err := os.WriteFile(metadata, []byte(";FFMETADATA1\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=1500\ntitle=First\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=1500\nEND=3000\ntitle=Second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary, err := findThumbnailFFmpeg()
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(binary, "-nostdin", "-v", "error", "-y", "-filter_threads", "2", "-f", "lavfi", "-i", "color=c=black:s=160x90:r=15", "-i", metadata, "-map_metadata", "1", "-map_chapters", "1", "-t", "3", "-c:v", "libx264", "-threads", "2", "-pix_fmt", "yuv420p", video.Path).CombinedOutput()
	if err != nil {
		t.Fatalf("synthetic chapter media: %v %s", err, output)
	}
	if err := NewMediaProbeService().Refresh(ctx, video.ID); err != nil {
		t.Fatal(err)
	}
	sets, err := service.GetVideoChapterSets(ctx, video.ID)
	if err != nil || len(sets.Embedded.Chapters) != 2 || sets.Embedded.Chapters[1].StartMS != 1500 || sets.Embedded.SourceStatus != "ready" {
		t.Fatalf("real chapters %+v %v", sets, err)
	}
	if sets.Manual.Revision != manual.Revision || sets.Manual.Chapters[0] != manual.Chapters[0] || sets.Manual.SourceStatus != "changed" {
		t.Fatalf("reprobe overwrote manual %+v", sets.Manual)
	}
	location, err := service.ResolveChapter(ctx, ResolveChapterRequest{SetID: sets.Embedded.SetID, ChapterID: sets.Embedded.Chapters[1].ID, Revision: sets.Embedded.Revision})
	if err != nil || location.StartMS != 1500 || location.SourceVersion != sets.SourceVersion {
		t.Fatalf("resolve %+v %v", location, err)
	}
	mustNoteCount(t, &models.PlayEvent{}, 0)
	mustNoteCount(t, &models.ViewingDiaryEntry{}, 0)
}

func TestChapterResolveRejectsRangeAfterDurationCorrection(t *testing.T) {
	video, source := notesFixture(t)
	service := NewChapterService(nil)
	ctx := context.Background()
	set, err := service.SaveVideoChapters(ctx, SaveChaptersRequest{VideoID: video.ID, SourceVersion: source, Chapters: []ChapterInput{{Title: "旧片长下的章节", StartMS: 2000, EndMS: 3000}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&video).Update("duration", 1).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveChapter(ctx, ResolveChapterRequest{SetID: set.SetID, ChapterID: set.Chapters[0].ID, Revision: set.Revision}); !errors.Is(err, ErrChapterInvalid) {
		t.Fatalf("out-of-range chapter resolved after corrected duration: %v", err)
	}
}
