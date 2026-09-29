package services

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const writerTestSRT = "1\n00:00:00,000 --> 00:00:01,000\noriginal\n"

func mustReadBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", filepath.Base(path), err)
	}
	return data
}

// mustCreateSubtitleVideo 在临时目录里建一条视频记录；srt 非 nil 时同时写好同名 .srt。
func mustCreateSubtitleVideo(t *testing.T, name string, srt []byte) (models.Video, string) {
	t.Helper()
	dir := t.TempDir()
	videoPath := filepath.Join(dir, name)
	if err := os.WriteFile(videoPath, []byte("video"), 0644); err != nil {
		t.Fatalf("写入视频失败: %v", err)
	}
	video := models.Video{Name: name, Path: videoPath, Directory: dir, Size: 5}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	srtPath := subtitleparser.SRTPathForVideo(videoPath)
	if srt != nil {
		if err := os.WriteFile(srtPath, srt, 0644); err != nil {
			t.Fatalf("写入字幕失败: %v", err)
		}
	}
	return video, srtPath
}

func mustListDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// ===== 写入器（D-PC13） =====

func TestSubtitleFileWriterReplaceBacksUpKeepsFiveAndPermissionsMEDIA05(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "movie.srt")
	writer := NewSubtitleFileWriter(t.TempDir())

	first, err := writer.Replace(context.Background(), 7, target, []byte("v0"))
	if err != nil || first.Replaced || first.BackupID != "" {
		t.Fatalf("新建文件不应产生备份: %+v err=%v", first, err)
	}
	if err := os.Chmod(target, 0600); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 7; version++ {
		result, err := writer.Replace(context.Background(), 7, target, []byte("v"+string(rune('0'+version))))
		if err != nil || !result.Replaced || result.BackupID == "" {
			t.Fatalf("第 %d 次覆盖应当先备份: %+v err=%v", version, result, err)
		}
	}
	backups, err := writer.ListBackups(7)
	if err != nil {
		t.Fatalf("列出备份失败: %v", err)
	}
	if len(backups) != 5 {
		t.Fatalf("每个视频只保留最新 5 份，实际 %d 份", len(backups))
	}
	newest := mustReadBytes(t, filepath.Join(writer.dataDir, subtitleBackupDirName, "7", backups[0].ID+".srt"))
	if string(newest) != "v6" {
		t.Fatalf("最新一份备份应是覆盖前的内容 v6，实际 %q", newest)
	}
	oldest := mustReadBytes(t, filepath.Join(writer.dataDir, subtitleBackupDirName, "7", backups[4].ID+".srt"))
	if string(oldest) != "v2" {
		t.Fatalf("最旧一份应是 v2（v0、v1 已被淘汰），实际 %q", oldest)
	}
	if string(mustReadBytes(t, target)) != "v7" {
		t.Fatal("目标应是最后写入的内容")
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("覆盖后应保持原文件权限 0600: %v %v", info, err)
	}
	if names := mustListDir(t, dir); len(names) != 1 {
		t.Fatalf("目标目录不应残留临时文件: %v", names)
	}
}

func TestSubtitleFileWriterRestoreBackupBacksUpCurrentFirstMEDIA05(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "movie.srt")
	writer := NewSubtitleFileWriter(t.TempDir())
	if err := os.WriteFile(target, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Replace(context.Background(), 3, target, []byte("new")); err != nil {
		t.Fatal(err)
	}
	backups, _ := writer.ListBackups(3)
	if len(backups) != 1 {
		t.Fatalf("应有 1 份备份: %+v", backups)
	}

	result, err := writer.RestoreBackup(context.Background(), 3, target, backups[0].ID)
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if string(mustReadBytes(t, target)) != "old" {
		t.Fatal("恢复后应回到上一版")
	}
	if result.BackupID == "" {
		t.Fatal("恢复前应先备份当前字幕")
	}
	backups, _ = writer.ListBackups(3)
	if len(backups) != 2 {
		t.Fatalf("恢复不应丢掉备份，且要多出恢复前的那份: %+v", backups)
	}
	if _, err := writer.RestoreBackup(context.Background(), 3, target, "../../etc/passwd"); err == nil {
		t.Fatal("非法备份编号必须被拒绝")
	}
}

func TestSubtitleFileWriterFailurePathsLeaveOriginalUntouchedMEDIA01(t *testing.T) {
	t.Run("数据目录不可用", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "movie.srt")
		if err := os.WriteFile(target, []byte(writerTestSRT), 0644); err != nil {
			t.Fatal(err)
		}
		_, err := NewSubtitleFileWriter("").Replace(context.Background(), 1, target, []byte("new"))
		if err == nil || strings.Contains(err.Error(), dir) {
			t.Fatalf("应报错且不含路径: %v", err)
		}
		if string(mustReadBytes(t, target)) != writerTestSRT || len(mustListDir(t, dir)) != 1 {
			t.Fatal("失败后原字幕必须不变且不留临时文件")
		}
	})
	t.Run("替换失败", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "movie.srt")
		if err := os.WriteFile(target, []byte(writerTestSRT), 0644); err != nil {
			t.Fatal(err)
		}
		writer := NewSubtitleFileWriter(t.TempDir())
		writer.replaceFile = func(_, _ string) error { return errors.New("boom") }
		if _, err := writer.Replace(context.Background(), 1, target, []byte("new")); err == nil {
			t.Fatal("替换失败应当报错")
		}
		if string(mustReadBytes(t, target)) != writerTestSRT || len(mustListDir(t, dir)) != 1 {
			t.Fatal("失败后原字幕必须不变且不留临时文件")
		}
		if backups, _ := writer.ListBackups(1); len(backups) != 0 {
			t.Fatalf("替换失败不应留下备份: %+v", backups)
		}
	})
	t.Run("已取消", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "movie.srt")
		if err := os.WriteFile(target, []byte(writerTestSRT), 0644); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := NewSubtitleFileWriter(t.TempDir()).Replace(ctx, 1, target, []byte("new")); err == nil {
			t.Fatal("已取消应当报错")
		}
		if string(mustReadBytes(t, target)) != writerTestSRT {
			t.Fatal("取消后原字幕必须不变")
		}
	})
	t.Run("符号链接", func(t *testing.T) {
		dir := t.TempDir()
		real := filepath.Join(t.TempDir(), "real.srt")
		if err := os.WriteFile(real, []byte(writerTestSRT), 0644); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, "movie.srt")
		if err := os.Symlink(real, target); err != nil {
			t.Skipf("平台不支持符号链接: %v", err)
		}
		if _, err := NewSubtitleFileWriter(t.TempDir()).Replace(context.Background(), 1, target, []byte("new")); err == nil {
			t.Fatal("符号链接必须被拒绝")
		}
		if info, _ := os.Lstat(target); info.Mode()&os.ModeSymlink == 0 || string(mustReadBytes(t, real)) != writerTestSRT {
			t.Fatal("符号链接与其指向的文件都不能被改动")
		}
	})
}

// ===== 字幕生成不再先写后验（MEDIA-01） =====

func hallucinatedSegments() []subtitleparser.Segment {
	segments := make([]subtitleparser.Segment, 0, 10)
	for i := 0; i < 10; i++ {
		segments = append(segments, subtitleparser.Segment{
			Index: i + 1, StartTimeMs: int64(i) * 1000, EndTimeMs: int64(i)*1000 + 900, Text: "Thank you.",
		})
	}
	return segments
}

func realSegments() []subtitleparser.Segment {
	return []subtitleparser.Segment{
		{Index: 1, StartTimeMs: 0, EndTimeMs: 1000, Text: "Hello there"},
		{Index: 2, StartTimeMs: 1000, EndTimeMs: 2000, Text: "General Kenobi"},
	}
}

func TestSubtitleGenerationHallucinationKeepsOriginalAndPendingIsForceReplaceableMEDIA01(t *testing.T) {
	setupVideoServiceTestDB(t)
	original := []byte("1\n00:00:00,000 --> 00:00:01,000\nhand-made subtitle\n")
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", original)
	service := NewSubtitleService(t.TempDir())
	req := SubtitleGenerateRequest{VideoID: video.ID, Engine: SubtitleEngineWhisperX, SourceLang: "en"}

	result, err := service.commitTranscription(context.Background(), 1, req, video.Path, "en", hallucinatedSegments(), SubtitleGenerateOptions{})
	if err != nil {
		t.Fatalf("幻觉应以校验失败结果返回: %v", err)
	}
	if result.Status != SubtitleResultStatusValidationFailed || !result.ForceEligible {
		t.Fatalf("应返回可强制的校验失败: %+v", result)
	}
	if !bytes.Equal(mustReadBytes(t, srtPath), original) {
		t.Fatal("幻觉路径下原 .srt 必须逐字节不变")
	}
	pendingPath := subtitlePendingPath(srtPath)
	artifact := service.peekPendingSubtitle(video.ID)
	if artifact == nil || artifact.SRTPath != pendingPath {
		t.Fatalf("pending 应指向临时文件: %+v", artifact)
	}
	if _, err := os.Stat(pendingPath); err != nil {
		t.Fatalf("临时文件应保留给强制生成: %v", err)
	}

	// 强制生成：临时文件 Replace 到 .srt，旧字幕进备份，临时文件消失。
	forced, err := service.finalizeSubtitleArtifact(context.Background(), 1, req, pendingPath, "en", SubtitleGenerateOptions{ForceGenerate: true})
	if err != nil || forced.Status != SubtitleResultStatusSuccess {
		t.Fatalf("强制生成失败: %+v err=%v", forced, err)
	}
	if !strings.Contains(string(mustReadBytes(t, srtPath)), "Thank you.") {
		t.Fatal("强制生成后 .srt 应是转写结果")
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("强制生成后临时文件应删除: %v", err)
	}
	backups, err := service.subtitleWriter().ListBackups(video.ID)
	if err != nil || len(backups) != 1 {
		t.Fatalf("旧字幕应进入备份: %+v err=%v", backups, err)
	}
	backupBytes := mustReadBytes(t, filepath.Join(service.BaseDir, subtitleBackupDirName, "1", backups[0].ID+".srt"))
	if !bytes.Equal(backupBytes, original) {
		t.Fatal("备份应是覆盖前的原字幕")
	}
}

func TestSubtitleGenerationDiscardPendingDeletesTemporaryFileMEDIA01(t *testing.T) {
	setupVideoServiceTestDB(t)
	original := []byte(writerTestSRT)
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", original)
	service := NewSubtitleService(t.TempDir())
	req := SubtitleGenerateRequest{VideoID: video.ID, Engine: SubtitleEngineWhisperX}
	if _, err := service.commitTranscription(context.Background(), 1, req, video.Path, "en", hallucinatedSegments(), SubtitleGenerateOptions{}); err != nil {
		t.Fatal(err)
	}
	pendingPath := subtitlePendingPath(srtPath)
	if _, err := os.Stat(pendingPath); err != nil {
		t.Fatalf("应有临时文件: %v", err)
	}

	if err := service.DiscardPendingSubtitle(video.ID); err != nil {
		t.Fatalf("放弃失败: %v", err)
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("放弃后临时文件应删除: %v", err)
	}
	if service.peekPendingSubtitle(video.ID) != nil {
		t.Fatal("放弃后不应再有 pending 登记")
	}
	if !bytes.Equal(mustReadBytes(t, srtPath), original) {
		t.Fatal("放弃不能动原字幕")
	}
	if err := service.DiscardPendingSubtitle(video.ID); err != nil {
		t.Fatalf("重复放弃应当无害: %v", err)
	}
}

func TestSubtitleGenerationEmptyResultKeepsOriginalMEDIA01(t *testing.T) {
	setupVideoServiceTestDB(t)
	original := []byte(writerTestSRT)
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", original)
	service := NewSubtitleService(t.TempDir())
	req := SubtitleGenerateRequest{VideoID: video.ID, Engine: SubtitleEngineWhisperX}

	for name, options := range map[string]SubtitleGenerateOptions{"普通": {}, "强制": {ForceGenerate: true}} {
		t.Run(name, func(t *testing.T) {
			blank := []subtitleparser.Segment{{Index: 1, StartTimeMs: 0, EndTimeMs: 1000, Text: "   "}}
			result, err := service.commitTranscription(context.Background(), 1, req, video.Path, "en", blank, options)
			if err == nil || result != nil {
				t.Fatalf("空结果应当报错: result=%+v err=%v", result, err)
			}
			if !bytes.Equal(mustReadBytes(t, srtPath), original) {
				t.Fatal("空结果路径下原 .srt 必须逐字节不变")
			}
			if _, statErr := os.Stat(subtitlePendingPath(srtPath)); !os.IsNotExist(statErr) {
				t.Fatalf("空结果不应留下临时文件: %v", statErr)
			}
		})
	}
	if _, err := service.commitTranscription(context.Background(), 1, req, video.Path, "en", nil, SubtitleGenerateOptions{}); err == nil {
		t.Fatal("零段转写结果也应当报错")
	}
}

func TestSubtitleGenerationCancelKeepsOriginalMEDIA01(t *testing.T) {
	setupVideoServiceTestDB(t)
	original := []byte(writerTestSRT)
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", original)
	service := NewSubtitleService(t.TempDir())
	req := SubtitleGenerateRequest{VideoID: video.ID, Engine: SubtitleEngineWhisperX}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := service.commitTranscription(ctx, 1, req, video.Path, "en", realSegments(), SubtitleGenerateOptions{})
	if err != nil || result.Status != SubtitleResultStatusCancelled {
		t.Fatalf("取消应返回 cancelled 结果: %+v err=%v", result, err)
	}
	if !bytes.Equal(mustReadBytes(t, srtPath), original) {
		t.Fatal("取消路径下原 .srt 必须逐字节不变")
	}
	if _, statErr := os.Stat(subtitlePendingPath(srtPath)); !os.IsNotExist(statErr) {
		t.Fatalf("取消不应留下临时文件: %v", statErr)
	}

	// 收尾阶段取消（例如翻译期间）同样不动原字幕，并清掉临时文件。
	pendingPath := subtitlePendingPath(srtPath)
	if err := os.WriteFile(pendingPath, []byte("1\n00:00:00,000 --> 00:00:01,000\nnew\n"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err = service.finalizeSubtitleArtifact(ctx, 1, req, pendingPath, "en", SubtitleGenerateOptions{})
	if err != nil || result.Status != SubtitleResultStatusCancelled {
		t.Fatalf("收尾阶段取消应返回 cancelled: %+v err=%v", result, err)
	}
	if !bytes.Equal(mustReadBytes(t, srtPath), original) {
		t.Fatal("收尾阶段取消后原 .srt 必须逐字节不变")
	}
	if _, statErr := os.Stat(pendingPath); !os.IsNotExist(statErr) {
		t.Fatalf("收尾阶段取消应删除临时文件: %v", statErr)
	}
}

func TestSubtitleGenerationSuccessReplacesOriginalWithBackupMEDIA01(t *testing.T) {
	setupVideoServiceTestDB(t)
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", []byte(writerTestSRT))
	service := NewSubtitleService(t.TempDir())
	req := SubtitleGenerateRequest{VideoID: video.ID, Engine: SubtitleEngineWhisperX}

	result, err := service.commitTranscription(context.Background(), 1, req, video.Path, "en", realSegments(), SubtitleGenerateOptions{})
	if err != nil || result.Status != SubtitleResultStatusSuccess || result.Path != srtPath {
		t.Fatalf("正常生成应成功: %+v err=%v", result, err)
	}
	if !strings.Contains(string(mustReadBytes(t, srtPath)), "General Kenobi") {
		t.Fatal(".srt 应是转写结果")
	}
	if _, statErr := os.Stat(subtitlePendingPath(srtPath)); !os.IsNotExist(statErr) {
		t.Fatalf("成功后不应留下临时文件: %v", statErr)
	}
	if backups, _ := service.subtitleWriter().ListBackups(video.ID); len(backups) != 1 {
		t.Fatalf("覆盖既有字幕应备份: %+v", backups)
	}
}

// ===== 覆盖预告（D-PC13） =====

func TestGetSubtitleOverwriteInfoListsVideosSharingTheSubtitleMEDIA01(t *testing.T) {
	setupVideoServiceTestDB(t)
	service := NewSubtitleService(t.TempDir())
	video, _ := mustCreateSubtitleVideo(t, "movie.mp4", nil)

	info, err := service.GetSubtitleOverwriteInfo(video)
	if err != nil || info.Exists || len(info.SharedWith) != 0 {
		t.Fatalf("没有字幕也没有同名视频: %+v err=%v", info, err)
	}

	dir := filepath.Dir(video.Path)
	sibling := models.Video{Name: "movie.mkv", Path: filepath.Join(dir, "movie.mkv"), Directory: dir}
	other := models.Video{Name: "movie.part2.mp4", Path: filepath.Join(dir, "movie.part2.mp4"), Directory: dir}
	stale := models.Video{Name: "movie.avi", Path: filepath.Join(dir, "movie.avi"), Directory: dir, IsStale: true}
	for _, row := range []*models.Video{&sibling, &other, &stale} {
		if err := database.DB.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(subtitleparser.SRTPathForVideo(video.Path), []byte(writerTestSRT), 0644); err != nil {
		t.Fatal(err)
	}

	info, err = service.GetSubtitleOverwriteInfo(video)
	if err != nil || !info.Exists {
		t.Fatalf("应报告字幕存在: %+v err=%v", info, err)
	}
	if len(info.SharedWith) != 1 || info.SharedWith[0].ID != sibling.ID || info.SharedWith[0].Name != "movie.mkv" {
		t.Fatalf("只应列出同基本名的活跃视频: %+v", info.SharedWith)
	}
}

// ===== 编码（MEDIA-02） =====

func gbkSubtitle(t *testing.T) []byte {
	t.Helper()
	text := "1\n00:00:00,000 --> 00:00:01,000\n你好，世界。今天天气很好，我们一起去公园散步吧。\n\n2\n00:00:01,000 --> 00:00:02,000\n这是第二句字幕，用来提高识别的可靠性。\n"
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatalf("编码 GBK 失败: %v", err)
	}
	return encoded
}

func TestSubtitleWorkbenchRefusesGBKThenConvertsAndRestoresMEDIA02(t *testing.T) {
	setupSubtitleSearchTestDB(t)
	video, srtPath := createSubtitleWorkbenchFixture(t)
	gbk := gbkSubtitle(t)
	if err := os.WriteFile(srtPath, gbk, 0644); err != nil {
		t.Fatal(err)
	}
	service := newTestSubtitleWorkbench(t)

	_, err := service.GetDocument(*video)
	var coded *SubtitleCodedError
	if !errors.As(err, &coded) || coded.Code != SubtitleErrorEncodingNotUTF8 || coded.DetectedEncoding != "gb18030" {
		t.Fatalf("打开 GBK 字幕应返回 subtitle_encoding_not_utf8: %v", err)
	}
	if strings.Contains(err.Error(), filepath.Dir(srtPath)) {
		t.Fatalf("错误文案不应含路径: %v", err)
	}
	if !bytes.Equal(mustReadBytes(t, srtPath), gbk) {
		t.Fatal("拒绝时不得改动字幕")
	}

	result, err := service.ConvertToUTF8(*video, "gb18030")
	if err != nil || result.Encoding != "gb18030" || result.BackupID == "" {
		t.Fatalf("转换失败: %+v err=%v", result, err)
	}
	converted := mustReadBytes(t, srtPath)
	if !strings.Contains(string(converted), "你好，世界") {
		t.Fatalf("转换后应是 UTF-8 中文: %q", converted)
	}
	if document, err := service.GetDocument(*video); err != nil || len(document.Entries) != 2 {
		t.Fatalf("转换后应能打开: %v", err)
	}
	if _, err := service.ConvertToUTF8(*video, ""); err == nil {
		t.Fatal("已经是 UTF-8 时不应再转换")
	}

	// 转换前的 GBK 原文在备份里，恢复后逐字节回来。
	subtitleService := &SubtitleService{BaseDir: service.dataDir}
	backups, err := subtitleService.ListSubtitleBackups(*video)
	if err != nil || len(backups) != 1 {
		t.Fatalf("应有转换前的备份: %+v err=%v", backups, err)
	}
	if _, err := subtitleService.RestoreSubtitleBackup(*video, backups[0].ID); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if !bytes.Equal(mustReadBytes(t, srtPath), gbk) {
		t.Fatal("恢复后应回到转换前的 GBK 字节")
	}
}

func TestSubtitleWorkbenchConvertRejectsStaleEncodingHintMEDIA02(t *testing.T) {
	setupSubtitleSearchTestDB(t)
	video, srtPath := createSubtitleWorkbenchFixture(t)
	gbk := gbkSubtitle(t)
	if err := os.WriteFile(srtPath, gbk, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := newTestSubtitleWorkbench(t).ConvertToUTF8(*video, "big5"); err == nil {
		t.Fatal("前端给出的编码与实际不一致时必须拒绝")
	}
	if !bytes.Equal(mustReadBytes(t, srtPath), gbk) {
		t.Fatal("拒绝时不得改动字幕")
	}
}

func TestTranslateSubtitleFileRefusesGBKWithoutCallingTranslatorMEDIA02(t *testing.T) {
	setupVideoServiceTestDB(t)
	server, prompts := startPromptCapturingLLMServer(t, func(string) string {
		return `{"choices":[{"message":{"content":"{\"translations\":[\"x\"]}"}}]}`
	})
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", gbkSubtitle(t))
	before := mustReadBytes(t, srtPath)

	service := NewSubtitleService(t.TempDir())
	_, err := service.TranslateSubtitleFile(context.Background(), video.Path,
		SubtitleTranslateRequest{VideoID: video.ID, TargetLang: "en", Mode: SubtitleTranslateModeTranslatedOnly},
		SubtitleTranslationConfig{Provider: "llm", BaseURL: server.URL, Model: "local-qwen"})
	var coded *SubtitleCodedError
	if !errors.As(err, &coded) || coded.Code != SubtitleErrorEncodingNotUTF8 || coded.DetectedEncoding != "gb18030" {
		t.Fatalf("翻译 GBK 字幕应返回 subtitle_encoding_not_utf8: %v", err)
	}
	if len(*prompts) != 0 {
		t.Fatalf("拒绝时不应发出翻译请求: %d", len(*prompts))
	}
	if !bytes.Equal(mustReadBytes(t, srtPath), before) {
		t.Fatal("拒绝时不得改动字幕")
	}
}

// ===== 翻译覆盖后可恢复（MEDIA-05） =====

func TestTranslateSubtitleFileBacksUpAndCanRestorePreviousVersionMEDIA05(t *testing.T) {
	setupVideoServiceTestDB(t)
	server, _ := startPromptCapturingLLMServer(t, func(string) string {
		return `{"choices":[{"message":{"content":"{\"translations\":[\"尼奥来了\",\"第二句\"]}"}}]}`
	})
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", []byte(translatableSubtitle))
	service := NewSubtitleService(t.TempDir())

	result, err := service.TranslateSubtitleFile(context.Background(), video.Path,
		SubtitleTranslateRequest{VideoID: video.ID, SourceLang: "en", TargetLang: "zh", Mode: SubtitleTranslateModeTranslatedOnly},
		SubtitleTranslationConfig{Provider: "llm", BaseURL: server.URL, Model: "local-qwen"})
	if err != nil || result.BackupID == "" {
		t.Fatalf("翻译应先备份原字幕: %+v err=%v", result, err)
	}
	if !strings.Contains(string(mustReadBytes(t, srtPath)), "尼奥来了") {
		t.Fatal("翻译结果应写入 .srt")
	}
	if _, err := service.RestoreSubtitleBackup(video, result.BackupID); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if string(mustReadBytes(t, srtPath)) != translatableSubtitle {
		t.Fatal("恢复后应逐字节回到翻译前的字幕")
	}
}

func TestSubtitleWorkbenchSaveBacksUpPreviousVersionMEDIA05(t *testing.T) {
	setupSubtitleSearchTestDB(t)
	video, srtPath := createSubtitleWorkbenchFixture(t)
	service := newTestSubtitleWorkbench(t)
	document, err := service.GetDocument(*video)
	if err != nil {
		t.Fatal(err)
	}
	document.Entries[0].Text = "edited"
	result, err := service.SaveDocument(*video, SubtitleSaveRequest{VideoID: video.ID, Fingerprint: document.Fingerprint, Entries: document.Entries})
	if err != nil || result.Status != SubtitleSaveStatusSaved || result.BackupID == "" {
		t.Fatalf("保存应先备份: %+v err=%v", result, err)
	}
	writer := NewSubtitleFileWriter(service.dataDir)
	if _, err := writer.RestoreBackup(context.Background(), video.ID, srtPath, result.BackupID); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if !strings.Contains(string(mustReadBytes(t, srtPath)), "original") {
		t.Fatal("恢复后应回到保存前的字幕")
	}
}

// ===== 工作台容错（MEDIA-06） =====

func TestSubtitleWorkbenchOpensZeroDurationAndBlocksSaveWithFirstIssueMEDIA06(t *testing.T) {
	setupSubtitleSearchTestDB(t)
	video, srtPath := createSubtitleWorkbenchFixture(t)
	content := "1\n00:00:05,000 --> 00:00:05,000\ninstant\n\n2\n00:00:06,000 --> 00:00:07,000\nnext\n"
	if err := os.WriteFile(srtPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	service := newTestSubtitleWorkbench(t)

	document, err := service.GetDocument(*video)
	if err != nil {
		t.Fatalf("零时长字幕应当能打开: %v", err)
	}
	if len(document.Issues) != 1 || document.Issues[0].Index != 1 || document.Issues[0].Kind != subtitleparser.DocumentIssueZeroDuration {
		t.Fatalf("应报告第 1 条零时长: %+v", document.Issues)
	}

	rejected, err := service.SaveDocument(*video, SubtitleSaveRequest{VideoID: video.ID, Fingerprint: document.Fingerprint, Entries: document.Entries})
	if err != nil || rejected.Status != SubtitleSaveStatusRejected || rejected.ErrorCode != SubtitleWorkbenchErrorValidation {
		t.Fatalf("有问题时保存应被拒绝: %+v err=%v", rejected, err)
	}
	if rejected.FirstIssueEntryIndex != 1 || rejected.FirstIssueClientID != document.Entries[0].ClientID {
		t.Fatalf("应指出第一个问题的位置: %+v", rejected)
	}
	if string(mustReadBytes(t, srtPath)) != content {
		t.Fatal("拒绝时不得改动字幕")
	}

	// 一键修复的结果：结束 = 开始 + 500ms。
	document.Entries[0].EndTimeMs = document.Entries[0].StartTimeMs + 500
	saved, err := service.SaveDocument(*video, SubtitleSaveRequest{VideoID: video.ID, Fingerprint: document.Fingerprint, Entries: document.Entries})
	if err != nil || saved.Status != SubtitleSaveStatusSaved {
		t.Fatalf("修复后应能保存: %+v err=%v", saved, err)
	}
}

func TestSubtitleWorkbenchMissingSubtitleOpensBlankAndCreatesOnSaveMEDIA06(t *testing.T) {
	setupSubtitleSearchTestDB(t)
	video, srtPath := createSubtitleWorkbenchFixture(t)
	if err := os.Remove(srtPath); err != nil {
		t.Fatal(err)
	}
	service := newTestSubtitleWorkbench(t)

	_, err := service.GetDocument(*video)
	var coded *SubtitleCodedError
	if !errors.As(err, &coded) || coded.Code != SubtitleErrorMissing {
		t.Fatalf("没有字幕应返回 subtitle_missing: %v", err)
	}
	if strings.Contains(err.Error(), filepath.Dir(srtPath)) {
		t.Fatalf("错误文案不应含路径: %v", err)
	}

	blank, err := service.NewBlankDocument(*video)
	if err != nil || len(blank.Entries) != 0 || blank.Fingerprint != (SubtitleFingerprint{}) {
		t.Fatalf("应以空文档打开: %+v err=%v", blank, err)
	}
	if empty, err := service.SaveDocument(*video, SubtitleSaveRequest{VideoID: video.ID, Fingerprint: blank.Fingerprint}); err != nil || empty.Status != SubtitleSaveStatusRejected {
		t.Fatalf("空文档不能直接保存: %+v err=%v", empty, err)
	}
	saved, err := service.SaveDocument(*video, SubtitleSaveRequest{
		VideoID: video.ID, Fingerprint: blank.Fingerprint,
		Entries: []subtitleparser.EditorSegment{{ClientID: "new-1", StartTimeMs: 0, EndTimeMs: 1500, Text: "hello"}},
	})
	if err != nil || saved.Status != SubtitleSaveStatusSaved || saved.BackupID != "" {
		t.Fatalf("保存应创建文件且没有备份: %+v err=%v", saved, err)
	}
	if !strings.Contains(string(mustReadBytes(t, srtPath)), "hello") {
		t.Fatal("字幕文件应被创建")
	}
	if _, err := service.NewBlankDocument(*video); err == nil {
		t.Fatal("文件已存在时不能再以空文档打开")
	}

	// 以空文档打开之后文件被别处创建：保存必须当作冲突，不能覆盖。
	if err := os.Remove(srtPath); err != nil {
		t.Fatal(err)
	}
	blank, _ = service.NewBlankDocument(*video)
	if err := os.WriteFile(srtPath, []byte("1\n00:00:00,000 --> 00:00:01,000\nexternal\n"), 0644); err != nil {
		t.Fatal(err)
	}
	conflict, err := service.SaveDocument(*video, SubtitleSaveRequest{
		VideoID: video.ID, Fingerprint: blank.Fingerprint,
		Entries: []subtitleparser.EditorSegment{{ClientID: "new-1", StartTimeMs: 0, EndTimeMs: 1500, Text: "mine"}},
	})
	if err != nil || conflict.ErrorCode != SubtitleWorkbenchErrorConflict {
		t.Fatalf("外部新建的字幕不能被覆盖: %+v err=%v", conflict, err)
	}
}

// ===== 只认同名 .srt（MEDIA-08） =====

func TestSubtitleEditAndTranslateReportNotSidecarSRTWhenOnlyOtherSubtitlesExistMEDIA08(t *testing.T) {
	setupVideoServiceTestDB(t)
	video, _ := mustCreateSubtitleVideo(t, "movie.mp4", nil)
	if err := os.WriteFile(filepath.Join(filepath.Dir(video.Path), "movie.en.ass"), []byte("[Script Info]"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := newTestSubtitleWorkbench(t).GetDocument(video)
	var coded *SubtitleCodedError
	if !errors.As(err, &coded) || coded.Code != SubtitleErrorNotSidecarSRT {
		t.Fatalf("工作台应返回 subtitle_not_sidecar_srt: %v", err)
	}
	if strings.Contains(err.Error(), "请先生成字幕") {
		t.Fatalf("不应再提示先生成字幕: %v", err)
	}

	_, err = NewSubtitleService(t.TempDir()).TranslateSubtitleFile(context.Background(), video.Path,
		SubtitleTranslateRequest{VideoID: video.ID, TargetLang: "zh", Mode: SubtitleTranslateModeBilingual},
		SubtitleTranslationConfig{Provider: "llm", BaseURL: "http://127.0.0.1:1", Model: "local-qwen"})
	if !errors.As(err, &coded) || coded.Code != SubtitleErrorNotSidecarSRT {
		t.Fatalf("翻译应返回 subtitle_not_sidecar_srt: %v", err)
	}

	// 内嵌字幕流同样算「有其他字幕」。
	embedded, _ := mustCreateSubtitleVideo(t, "embedded.mkv", nil)
	if err := database.DB.Create(&models.MediaStream{VideoID: embedded.ID, StreamIndex: 2, StreamType: "subtitle", CodecName: "subrip"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := newTestSubtitleWorkbench(t).GetDocument(embedded); !errors.As(err, &coded) || coded.Code != SubtitleErrorNotSidecarSRT {
		t.Fatalf("内嵌字幕应返回 subtitle_not_sidecar_srt: %v", err)
	}

	// 什么字幕都没有：仍是 subtitle_missing。
	none, _ := mustCreateSubtitleVideo(t, "none.mp4", nil)
	if _, err := newTestSubtitleWorkbench(t).GetDocument(none); !errors.As(err, &coded) || coded.Code != SubtitleErrorMissing {
		t.Fatalf("没有任何字幕应返回 subtitle_missing: %v", err)
	}
}

// ===== 重译一致性（MEDIA-07） =====

func TestSubtitleWorkbenchRetranslateTranslationLineOnlyReplacesSecondLineMEDIA07(t *testing.T) {
	service := newTestSubtitleWorkbench(t)
	translator := &recordingContextualTranslator{reply: echoTranslationReply("译:")}
	service.translatorFactory = func(SubtitleTranslationConfig) (SubtitleTranslator, error) { return translator, nil }
	var resolvedTargets []string
	service.glossaryResolver = func(_ uint, target string) ([]GlossaryTerm, error) {
		resolvedTargets = append(resolvedTargets, target)
		return nil, nil
	}
	request := SubtitleRetranslateRequest{
		VideoID: 1, SourceLang: "en", TargetLang: "zh", Mode: SubtitleRetranslateModeTranslationLine,
		Entries: []SubtitleRetranslateEntry{
			{ClientID: "a", Text: "Hello\n旧译文"},
			{ClientID: "b", Text: "World\n旧译文\n第三行保留"},
			{ClientID: "c", Text: "Solo"},
		},
	}

	result, err := service.Retranslate(context.Background(), request, SubtitleTranslationConfig{})
	if err != nil {
		t.Fatalf("重译失败: %v", err)
	}
	if got := translator.requests[0].Texts; len(got) != 3 || got[0] != "Hello" || got[1] != "World" || got[2] != "Solo" {
		t.Fatalf("只应把第一行送去翻译: %#v", got)
	}
	want := []string{"Hello\n译:Hello", "World\n译:World\n第三行保留", "Solo\n译:Solo"}
	for index, entry := range result.Entries {
		if entry.Text != want[index] {
			t.Fatalf("第 %d 条应只替换第二行: got %q want %q", index+1, entry.Text, want[index])
		}
	}
	if len(resolvedTargets) != 1 || resolvedTargets[0] != "zh" {
		t.Fatalf("术语表应按目标语言解析: %#v", resolvedTargets)
	}

	// 默认（空 mode）仍是整条替换。
	request.Mode = ""
	result, err = service.Retranslate(context.Background(), request, SubtitleTranslationConfig{})
	if err != nil || result.Entries[0].Text != "译:Hello\n旧译文" {
		t.Fatalf("默认应整条送去翻译并整条替换: %+v err=%v", result, err)
	}
	request.Mode = "bogus"
	if _, err := service.Retranslate(context.Background(), request, SubtitleTranslationConfig{}); err == nil {
		t.Fatal("未知重译范围应当报错")
	}
}

func TestSubtitleWorkbenchRetranslateKeepsEntryAndWarnsOnEmptyTranslationMEDIA07(t *testing.T) {
	service := newTestSubtitleWorkbench(t)
	translator := &recordingContextualTranslator{reply: func(request TranslationRequest) ([]string, error) {
		return []string{"", "译:" + request.Texts[1]}, nil
	}}
	service.translatorFactory = func(SubtitleTranslationConfig) (SubtitleTranslator, error) { return translator, nil }
	service.glossaryResolver = func(uint, string) ([]GlossaryTerm, error) { return nil, nil }

	for _, mode := range []SubtitleRetranslateMode{SubtitleRetranslateModeWholeEntry, SubtitleRetranslateModeTranslationLine} {
		result, err := service.Retranslate(context.Background(), SubtitleRetranslateRequest{
			VideoID: 1, TargetLang: "zh", Mode: mode,
			Entries: []SubtitleRetranslateEntry{{ClientID: "a", Text: "Keep\n保留"}, {ClientID: "b", Text: "Next\n下一句"}},
		}, SubtitleTranslationConfig{})
		if err != nil {
			t.Fatalf("%s 重译失败: %v", mode, err)
		}
		if result.Entries[0].Text != "Keep\n保留" && mode == SubtitleRetranslateModeTranslationLine {
			t.Fatalf("译文为空时条目应保持原样: %q", result.Entries[0].Text)
		}
		if mode == SubtitleRetranslateModeWholeEntry && result.Entries[0].Text != "Keep\n保留" {
			t.Fatalf("译文为空时应保留原文而不是写入空文本: %q", result.Entries[0].Text)
		}
		if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "1 条") {
			t.Fatalf("应告知回退条数: %v", result.Warnings)
		}
	}
}

func TestApplyTranslationFallbackIsSharedByAllTranslationPathsMEDIA07(t *testing.T) {
	out, count := applyTranslationFallback([]string{"a", "b", "c"}, []string{"甲", "  ", ""})
	if count != 2 || out[0] != "甲" || out[1] != "b" || out[2] != "c" {
		t.Fatalf("空译文与纯空白译文都应回退成原文: %v count=%d", out, count)
	}
}

func TestGlossaryResolveForVideoTargetLanguagePriorityMEDIA07(t *testing.T) {
	setupVideoServiceTestDB(t)
	collection := mustCreateGlossaryCollection(t, "黑客帝国")
	video := mustCreateGlossaryVideo(t, filepath.Join(t.TempDir(), "matrix.mp4"))
	mustLinkGlossaryCollectionVideo(t, collection.ID, video.ID, 1)
	scope := collectionScope(collection.ID)

	mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Neo", TargetTerm: "全局通用"})
	globalZh := mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Neo", TargetTerm: "全局中文", TargetLanguage: "zh"})
	mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Trinity", TargetTerm: "崔妮蒂", TargetLanguage: "zh"})
	mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Trinity", TargetTerm: "トリニティ", TargetLanguage: "ja"})

	resolve := func(target string) map[string]string {
		terms, err := NewTranslationGlossaryService().ResolveForVideo(video.ID, target)
		if err != nil {
			t.Fatalf("解析术语失败: %v", err)
		}
		byTerm := map[string]string{}
		for _, term := range terms {
			byTerm[term.SourceTerm] = term.TargetTerm
		}
		return byTerm
	}

	// 全局+语言 > 全局+空；只注入匹配目标语言的条目。
	zh := resolve("zh")
	if zh["Neo"] != "全局中文" || zh["Trinity"] != "崔妮蒂" {
		t.Fatalf("zh: %v", zh)
	}
	ja := resolve("ja")
	if ja["Neo"] != "全局通用" || ja["Trinity"] != "トリニティ" {
		t.Fatalf("ja: %v", ja)
	}
	if fr := resolve("fr"); fr["Neo"] != "全局通用" || len(fr) != 1 {
		t.Fatalf("fr 只应有所有语言条目: %v", fr)
	}
	if none := resolve(""); none["Neo"] != "全局通用" || len(none) != 1 {
		t.Fatalf("未指定目标语言只应有所有语言条目: %v", none)
	}

	// 作品集+空 > 全局+语言；作品集+语言 > 作品集+空。
	mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{CollectionID: scope, SourceTerm: "Neo", TargetTerm: "作品集通用"})
	if got := resolve("zh")["Neo"]; got != "作品集通用" {
		t.Fatalf("作品集+空 应压过 全局+语言: %q", got)
	}
	mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{CollectionID: scope, SourceTerm: "Neo", TargetTerm: "作品集中文", TargetLanguage: "zh"})
	if got := resolve("zh")["Neo"]; got != "作品集中文" {
		t.Fatalf("作品集+语言 应最优先: %q", got)
	}
	if got := resolve("ja")["Neo"]; got != "作品集通用" {
		t.Fatalf("ja 不应吃到 zh 条目: %q", got)
	}

	// 唯一键含 target_language：改语言撞上同作用域同源词时报冲突。
	conflict := globalZh
	conflict.TargetLanguage = ""
	if _, err := NewTranslationGlossaryService().Upsert(conflict); !errors.Is(err, ErrGlossaryTermConflict) {
		t.Fatalf("改成已存在的 (作用域, 语言, 源词) 应报冲突: %v", err)
	}
	// 语言按翻译流程的口径规整。
	saved := mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Morpheus", TargetTerm: "墨菲斯", TargetLanguage: " Chinese "})
	if saved.TargetLanguage != "zh" {
		t.Fatalf("目标语言应规整为 zh: %q", saved.TargetLanguage)
	}
}
