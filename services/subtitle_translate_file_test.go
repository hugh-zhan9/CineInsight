package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// mustPrepareTranslatableSubtitle 建一条视频记录，并在同目录写好它的外挂 SRT。
func mustPrepareTranslatableSubtitle(t *testing.T, content string, mode os.FileMode) (videoID uint, videoPath string, srtPath string) {
	t.Helper()
	dir := t.TempDir()
	videoPath = filepath.Join(dir, "matrix.mp4")
	srtPath = filepath.Join(dir, "matrix.srt")
	video := mustCreateGlossaryVideo(t, videoPath)
	if err := os.WriteFile(srtPath, []byte(content), mode); err != nil {
		t.Fatalf("写入测试字幕失败: %v", err)
	}
	return video.ID, videoPath, srtPath
}

const translatableSubtitle = "1\n00:00:00,000 --> 00:00:01,000\nNEO arrives\n\n2\n00:00:01,000 --> 00:00:02,000\nSecond line\n\n"

func TestTranslateSubtitleFileBilingualKeepsOriginalAboveTranslation(t *testing.T) {
	setupVideoServiceTestDB(t)
	server, prompts := startPromptCapturingLLMServer(t, func(string) string {
		return `{"choices":[{"message":{"content":"{\"translations\":[\"尼奥来了\",\"第二句\"]}"}}]}`
	})
	videoID, videoPath, srtPath := mustPrepareTranslatableSubtitle(t, translatableSubtitle, 0644)

	service := NewSubtitleService(t.TempDir())
	result, err := service.TranslateSubtitleFile(context.Background(), videoPath,
		SubtitleTranslateRequest{VideoID: videoID, SourceLang: "en", TargetLang: "zh", Mode: SubtitleTranslateModeBilingual},
		SubtitleTranslationConfig{Provider: "llm", BaseURL: server.URL, Model: "local-qwen"})
	if err != nil {
		t.Fatalf("双语翻译失败: %v", err)
	}
	if result.Entries != 2 || result.TargetLang != "zh" || result.Mode != SubtitleTranslateModeBilingual {
		t.Fatalf("翻译结果不正确: %+v", result)
	}
	if result.Path != srtPath {
		t.Fatalf("翻译应覆盖同名字幕 got=%q want=%q", result.Path, srtPath)
	}
	if len(*prompts) != 1 {
		t.Fatalf("两条字幕应当只发一次翻译请求: %d", len(*prompts))
	}
	got := mustReadSubtitle(t, srtPath)
	if !strings.Contains(got, "NEO arrives\n尼奥来了") || !strings.Contains(got, "Second line\n第二句") {
		t.Fatalf("双语产物应当上行原文、下行译文，实际:\n%s", got)
	}
}

func TestTranslateSubtitleFileTranslatedOnlyReplacesOriginalAndKeepsPermissions(t *testing.T) {
	setupVideoServiceTestDB(t)
	server, _ := startPromptCapturingLLMServer(t, func(string) string {
		return `{"choices":[{"message":{"content":"{\"translations\":[\"尼奥来了\",\"第二句\"]}"}}]}`
	})
	videoID, videoPath, srtPath := mustPrepareTranslatableSubtitle(t, translatableSubtitle, 0600)

	service := NewSubtitleService(t.TempDir())
	if _, err := service.TranslateSubtitleFile(context.Background(), videoPath,
		SubtitleTranslateRequest{VideoID: videoID, SourceLang: "en", TargetLang: "zh", Mode: SubtitleTranslateModeTranslatedOnly},
		SubtitleTranslationConfig{Provider: "llm", BaseURL: server.URL, Model: "local-qwen"}); err != nil {
		t.Fatalf("仅译文翻译失败: %v", err)
	}

	got := mustReadSubtitle(t, srtPath)
	if strings.Contains(got, "NEO arrives") || strings.Contains(got, "Second line") {
		t.Fatalf("仅译文模式不应保留原文，实际:\n%s", got)
	}
	if !strings.Contains(got, "00:00:00,000 --> 00:00:01,000\n尼奥来了") {
		t.Fatalf("仅译文模式应保留时间轴与译文，实际:\n%s", got)
	}
	info, err := os.Stat(srtPath)
	if err != nil {
		t.Fatalf("读取字幕权限失败: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("覆盖写入不应改变字幕权限 got=%o want=0600", perm)
	}
}

func TestTranslateSubtitleFileKeepsOriginalWhenTranslationFails(t *testing.T) {
	setupVideoServiceTestDB(t)
	// 返回条数与请求不符，翻译整轮失败。
	server, _ := startPromptCapturingLLMServer(t, func(string) string {
		return `{"choices":[{"message":{"content":"{\"translations\":[\"只有一条\"]}"}}]}`
	})
	videoID, videoPath, srtPath := mustPrepareTranslatableSubtitle(t, translatableSubtitle, 0644)

	service := NewSubtitleService(t.TempDir())
	if _, err := service.TranslateSubtitleFile(context.Background(), videoPath,
		SubtitleTranslateRequest{VideoID: videoID, SourceLang: "en", TargetLang: "zh", Mode: SubtitleTranslateModeTranslatedOnly},
		SubtitleTranslationConfig{Provider: "llm", BaseURL: server.URL, Model: "local-qwen"}); err == nil {
		t.Fatal("翻译失败时应当报错")
	}
	if got := mustReadSubtitle(t, srtPath); got != translatableSubtitle {
		t.Fatalf("翻译失败必须保留原字幕，实际:\n%s", got)
	}
	if leftovers := mustGlobSubtitleTemporaries(t, filepath.Dir(srtPath)); len(leftovers) != 0 {
		t.Fatalf("失败后不应残留临时文件: %v", leftovers)
	}
}

func TestTranslateSubtitleFileRejectsInvalidRequests(t *testing.T) {
	setupVideoServiceTestDB(t)
	videoID, videoPath, _ := mustPrepareTranslatableSubtitle(t, translatableSubtitle, 0644)
	service := NewSubtitleService(t.TempDir())
	config := SubtitleTranslationConfig{Provider: "llm", BaseURL: "http://127.0.0.1:1", Model: "local-qwen"}

	cases := []struct {
		name    string
		request SubtitleTranslateRequest
		reason  string
	}{
		{"目标语言为空", SubtitleTranslateRequest{VideoID: videoID, SourceLang: "en", Mode: SubtitleTranslateModeBilingual}, "目标语言"},
		{"目标语言为自动", SubtitleTranslateRequest{VideoID: videoID, TargetLang: "auto", Mode: SubtitleTranslateModeBilingual}, "目标语言"},
		{"源语言与目标语言相同", SubtitleTranslateRequest{VideoID: videoID, SourceLang: "chinese", TargetLang: "zh", Mode: SubtitleTranslateModeBilingual}, "无需翻译"},
		{"输出形态非法", SubtitleTranslateRequest{VideoID: videoID, SourceLang: "en", TargetLang: "zh", Mode: "merged"}, "输出形态"},
		{"输出形态缺省", SubtitleTranslateRequest{VideoID: videoID, SourceLang: "en", TargetLang: "zh"}, "输出形态"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := service.TranslateSubtitleFile(context.Background(), videoPath, testCase.request, config)
			if err == nil {
				t.Fatalf("%s 应当被拒绝", testCase.name)
			}
			if !strings.Contains(err.Error(), testCase.reason) {
				t.Fatalf("错误信息应说明原因 %q，实际: %v", testCase.reason, err)
			}
		})
	}
}

func TestTranslateSubtitleFileRequiresExistingSubtitle(t *testing.T) {
	setupVideoServiceTestDB(t)
	videoPath := filepath.Join(t.TempDir(), "matrix.mp4")
	video := mustCreateGlossaryVideo(t, videoPath)

	service := NewSubtitleService(t.TempDir())
	_, err := service.TranslateSubtitleFile(context.Background(), videoPath,
		SubtitleTranslateRequest{VideoID: video.ID, TargetLang: "zh", Mode: SubtitleTranslateModeBilingual},
		SubtitleTranslationConfig{Provider: "llm", BaseURL: "http://127.0.0.1:1", Model: "local-qwen"})
	if err == nil || !strings.Contains(err.Error(), "还没有外挂字幕") {
		t.Fatalf("没有字幕时应当提示先生成字幕，实际: %v", err)
	}
}

func TestTranslateSubtitleFileRejectsEmptySubtitle(t *testing.T) {
	setupVideoServiceTestDB(t)
	videoID, videoPath, _ := mustPrepareTranslatableSubtitle(t, "", 0644)

	service := NewSubtitleService(t.TempDir())
	_, err := service.TranslateSubtitleFile(context.Background(), videoPath,
		SubtitleTranslateRequest{VideoID: videoID, TargetLang: "zh", Mode: SubtitleTranslateModeBilingual},
		SubtitleTranslationConfig{Provider: "llm", BaseURL: "http://127.0.0.1:1", Model: "local-qwen"})
	if err == nil || !strings.Contains(err.Error(), "字幕文件为空") {
		t.Fatalf("空字幕应当被拒绝，实际: %v", err)
	}
}

func TestTranslateSubtitleFileSkipsGlossaryLookupForDeepL(t *testing.T) {
	setupVideoServiceTestDB(t)
	recorder := &recordingRoundTripper{
		label:     "deepl translate existing subtitle",
		responses: func() (string, error) { return `{"translations":[{"text":"尼奥来了"},{"text":"第二句"}]}`, nil },
	}
	original := deeplHTTPClient
	deeplHTTPClient = &http.Client{Transport: recorder}
	t.Cleanup(func() { deeplHTTPClient = original })

	videoID, videoPath, _ := mustPrepareTranslatableSubtitle(t, translatableSubtitle, 0644)

	service := NewSubtitleService(t.TempDir())
	resolved := 0
	service.glossaryResolver = func(uint) ([]GlossaryTerm, error) {
		resolved++
		return nil, nil
	}
	if _, err := service.TranslateSubtitleFile(context.Background(), videoPath,
		SubtitleTranslateRequest{VideoID: videoID, SourceLang: "en", TargetLang: "zh", Mode: SubtitleTranslateModeBilingual},
		SubtitleTranslationConfig{Provider: "deepl", DeepLAPIKey: "test-free-key:fx"}); err != nil {
		t.Fatalf("DeepL 翻译已有字幕失败: %v", err)
	}
	if resolved != 0 {
		t.Fatalf("DeepL 用不上术语表，不该因此多读一次库（%d 次）", resolved)
	}
	if len(recorder.captured) != 1 {
		t.Fatalf("两条字幕应当只发一次 DeepL 请求: %d", len(recorder.captured))
	}
}

func mustReadSubtitle(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取字幕失败: %v", err)
	}
	return string(data)
}

func mustGlobSubtitleTemporaries(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*_translated_temp.srt"))
	if err != nil {
		t.Fatalf("检查临时文件失败: %v", err)
	}
	return matches
}

// 空译文是真实会发生的：LLM 常对拟声词、音乐符号、纯标点行返回空串。
// 写出空块会被 parseSRTEntries 与编辑器解析器一起丢掉，仅译文模式下这条字幕
// 就被永久删除了，双语模式则会让其后所有译文错位。
const subtitleWithUntranslatableLine = "1\n00:00:00,000 --> 00:00:01,000\nLine one\n\n2\n00:00:01,000 --> 00:00:02,000\n♪♪♪\n\n3\n00:00:02,000 --> 00:00:03,000\nLine three\n\n"

func TestTranslateSubtitleFileKeepsEntryWhenTranslationComesBackEmpty(t *testing.T) {
	setupVideoServiceTestDB(t)
	server, _ := startPromptCapturingLLMServer(t, func(string) string {
		return `{"choices":[{"message":{"content":"{\"translations\":[\"第一句\",\"\",\"第三句\"]}"}}]}`
	})
	videoID, videoPath, srtPath := mustPrepareTranslatableSubtitle(t, subtitleWithUntranslatableLine, 0644)

	service := NewSubtitleService(t.TempDir())
	result, err := service.TranslateSubtitleFile(context.Background(), videoPath,
		SubtitleTranslateRequest{VideoID: videoID, SourceLang: "en", TargetLang: "zh", Mode: SubtitleTranslateModeTranslatedOnly},
		SubtitleTranslationConfig{Provider: "llm", BaseURL: server.URL, Model: "local-qwen"})
	if err != nil {
		t.Fatalf("仅译文翻译失败: %v", err)
	}

	entries, err := parseSRTEntries(srtPath)
	if err != nil {
		t.Fatalf("解析翻译结果失败: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("空译文不得让字幕条目消失 got=%d want=3，实际内容:\n%s", len(entries), mustReadSubtitle(t, srtPath))
	}
	if entries[1].Text != "♪♪♪" {
		t.Fatalf("译文为空时应保留原文，实际第二条为 %q", entries[1].Text)
	}
	if entries[0].Text != "第一句" || entries[2].Text != "第三句" {
		t.Fatalf("其余条目应当是译文: %q / %q", entries[0].Text, entries[2].Text)
	}
	if result.Entries != 3 {
		t.Fatalf("回报条数应与字幕条数一致: %d", result.Entries)
	}
}

func TestTranslateSubtitleFileBilingualStaysAlignedWhenTranslationComesBackEmpty(t *testing.T) {
	setupVideoServiceTestDB(t)
	server, _ := startPromptCapturingLLMServer(t, func(string) string {
		return `{"choices":[{"message":{"content":"{\"translations\":[\"第一句\",\"\",\"第三句\"]}"}}]}`
	})
	videoID, videoPath, srtPath := mustPrepareTranslatableSubtitle(t, subtitleWithUntranslatableLine, 0644)

	service := NewSubtitleService(t.TempDir())
	if _, err := service.TranslateSubtitleFile(context.Background(), videoPath,
		SubtitleTranslateRequest{VideoID: videoID, SourceLang: "en", TargetLang: "zh", Mode: SubtitleTranslateModeBilingual},
		SubtitleTranslationConfig{Provider: "llm", BaseURL: server.URL, Model: "local-qwen"}); err != nil {
		t.Fatalf("双语翻译失败: %v", err)
	}

	got := mustReadSubtitle(t, srtPath)
	if !strings.Contains(got, "Line three\n第三句") {
		t.Fatalf("空译文不得让其后的译文错位，实际:\n%s", got)
	}
	if strings.Contains(got, "♪♪♪\n第三句") {
		t.Fatalf("第三句被错配到了第二条字幕上，实际:\n%s", got)
	}
}

func TestTranslateSubtitleFileBilingualKeepsSubtitlePermissions(t *testing.T) {
	setupVideoServiceTestDB(t)
	server, _ := startPromptCapturingLLMServer(t, func(string) string {
		return `{"choices":[{"message":{"content":"{\"translations\":[\"尼奥来了\",\"第二句\"]}"}}]}`
	})
	videoID, videoPath, srtPath := mustPrepareTranslatableSubtitle(t, translatableSubtitle, 0600)

	service := NewSubtitleService(t.TempDir())
	if _, err := service.TranslateSubtitleFile(context.Background(), videoPath,
		SubtitleTranslateRequest{VideoID: videoID, SourceLang: "en", TargetLang: "zh", Mode: SubtitleTranslateModeBilingual},
		SubtitleTranslationConfig{Provider: "llm", BaseURL: server.URL, Model: "local-qwen"}); err != nil {
		t.Fatalf("双语翻译失败: %v", err)
	}
	info, err := os.Stat(srtPath)
	if err != nil {
		t.Fatalf("读取字幕权限失败: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("双语合并不应放宽字幕权限 got=%o want=0600", perm)
	}
}

func TestTranslateSubtitleFileRebuildsSubtitleIndex(t *testing.T) {
	setupVideoServiceTestDB(t)
	server, _ := startPromptCapturingLLMServer(t, func(string) string {
		return `{"choices":[{"message":{"content":"{\"translations\":[\"尼奥来了\",\"第二句\"]}"}}]}`
	})
	videoID, videoPath, srtPath := mustPrepareTranslatableSubtitle(t, translatableSubtitle, 0644)
	if err := indexSubtitleFileForVideoID(videoID, srtPath); err != nil {
		t.Fatalf("建立字幕索引失败: %v", err)
	}

	service := NewSubtitleService(t.TempDir())
	result, err := service.TranslateSubtitleFile(context.Background(), videoPath,
		SubtitleTranslateRequest{VideoID: videoID, SourceLang: "en", TargetLang: "zh", Mode: SubtitleTranslateModeTranslatedOnly},
		SubtitleTranslationConfig{Provider: "llm", BaseURL: server.URL, Model: "local-qwen"})
	if err != nil {
		t.Fatalf("仅译文翻译失败: %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("索引重建不应产生警告: %v", result.Warnings)
	}

	var segments []models.SubtitleSegment
	if err := database.DB.Where("video_id = ?", videoID).Order("segment_index").Find(&segments).Error; err != nil {
		t.Fatalf("读取字幕索引失败: %v", err)
	}
	if len(segments) != 2 {
		t.Fatalf("字幕索引条数不正确: %d", len(segments))
	}
	if segments[0].Text != "尼奥来了" {
		t.Fatalf("字幕索引仍是翻译前的原文: %q", segments[0].Text)
	}
}

// buildLongSubtitle 造一条超过单批（50 条）的字幕，好让取消发生在两批之间。
func buildLongSubtitle(count int) string {
	var builder strings.Builder
	for i := 1; i <= count; i++ {
		fmt.Fprintf(&builder, "%d\n00:%02d:%02d,000 --> 00:%02d:%02d,000\nLine %d\n\n",
			i, (i-1)/60, (i-1)%60, i/60, i%60, i)
	}
	return builder.String()
}

func TestTranslateSubtitleFileCancelKeepsOriginalSubtitle(t *testing.T) {
	setupVideoServiceTestDB(t)
	original := buildLongSubtitle(60)
	videoID, videoPath, srtPath := mustPrepareTranslatableSubtitle(t, original, 0644)
	service := NewSubtitleService(t.TempDir())

	// 第一批一到就叫停：取消要么打断这一批，要么打断下一批，两种都必须保住原字幕。
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&requests, 1) == 1 {
			service.CancelSubtitleTranslation(videoID)
		}
		translations := make([]string, 50)
		for i := range translations {
			translations[i] = fmt.Sprintf("译文%d", i+1)
		}
		content, err := json.Marshal(map[string][]string{"translations": translations})
		if err != nil {
			t.Errorf("构造译文失败: %v", err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{{"message": map[string]string{"content": string(content)}}},
		})
	}))
	defer server.Close()

	_, err := service.TranslateSubtitleFile(context.Background(), videoPath,
		SubtitleTranslateRequest{VideoID: videoID, SourceLang: "en", TargetLang: "zh", Mode: SubtitleTranslateModeTranslatedOnly},
		SubtitleTranslationConfig{Provider: "llm", BaseURL: server.URL, Model: "local-qwen"})
	if err == nil || !strings.Contains(err.Error(), "已取消") {
		t.Fatalf("取消后应当报告已取消，实际: %v", err)
	}
	if got := mustReadSubtitle(t, srtPath); got != original {
		t.Fatalf("取消不得改动原字幕")
	}
	if leftovers := mustGlobSubtitleTemporaries(t, filepath.Dir(srtPath)); len(leftovers) != 0 {
		t.Fatalf("取消后不应残留临时文件: %v", leftovers)
	}
	service.mu.Lock()
	remaining := len(service.translationCancels)
	service.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("翻译结束后应当注销取消登记，实际还剩 %d 条", remaining)
	}
}

func TestTranslateSubtitleFileWarnsAboutUntranslatedEntries(t *testing.T) {
	setupVideoServiceTestDB(t)
	server, _ := startPromptCapturingLLMServer(t, func(string) string {
		return `{"choices":[{"message":{"content":"{\"translations\":[\"第一句\",\"\",\"第三句\"]}"}}]}`
	})
	videoID, videoPath, _ := mustPrepareTranslatableSubtitle(t, subtitleWithUntranslatableLine, 0644)

	service := NewSubtitleService(t.TempDir())
	result, err := service.TranslateSubtitleFile(context.Background(), videoPath,
		SubtitleTranslateRequest{VideoID: videoID, SourceLang: "en", TargetLang: "zh", Mode: SubtitleTranslateModeTranslatedOnly},
		SubtitleTranslationConfig{Provider: "llm", BaseURL: server.URL, Model: "local-qwen"})
	if err != nil {
		t.Fatalf("仅译文翻译失败: %v", err)
	}
	// 保留原文不能是静默的：否则「一条都没翻成」和「全部翻成了」是同一个成功提示。
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "1 条") {
		t.Fatalf("回退保留原文应当告知用户，实际警告: %v", result.Warnings)
	}
}

func TestTranslateSubtitleFileBilingualDoesNotDuplicateFallbackLine(t *testing.T) {
	setupVideoServiceTestDB(t)
	server, _ := startPromptCapturingLLMServer(t, func(string) string {
		return `{"choices":[{"message":{"content":"{\"translations\":[\"第一句\",\"\",\"第三句\"]}"}}]}`
	})
	videoID, videoPath, srtPath := mustPrepareTranslatableSubtitle(t, subtitleWithUntranslatableLine, 0644)

	service := NewSubtitleService(t.TempDir())
	if _, err := service.TranslateSubtitleFile(context.Background(), videoPath,
		SubtitleTranslateRequest{VideoID: videoID, SourceLang: "en", TargetLang: "zh", Mode: SubtitleTranslateModeBilingual},
		SubtitleTranslationConfig{Provider: "llm", BaseURL: server.URL, Model: "local-qwen"}); err != nil {
		t.Fatalf("双语翻译失败: %v", err)
	}

	// 译文回退成原文后照双行写，就成了同一句话上下叠两遍。
	if got := mustReadSubtitle(t, srtPath); strings.Contains(got, "♪♪♪\n♪♪♪") {
		t.Fatalf("回退条目不应在双语里重复两行，实际:\n%s", got)
	}
}

func TestTranslateSubtitleFileCancelWhileWaitingForSubtitleLock(t *testing.T) {
	setupVideoServiceTestDB(t)
	videoID, videoPath, srtPath := mustPrepareTranslatableSubtitle(t, translatableSubtitle, 0644)
	service := NewSubtitleService(t.TempDir())

	// 翻译服务一次都不该被调用：取消发生在等锁期间，拿到锁就应当立刻退出。
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
	}))
	defer server.Close()

	// 占住这个视频的字幕文件锁（现实里是同一视频的字幕生成正在跑），让翻译卡在等锁。
	// 这是包级条带锁，中途 t.Fatal 会把它永久锁死，所以释放走一个幂等的闭包。
	unlock := lockSubtitleFile(videoID)
	released := false
	releaseLock := func() {
		if !released {
			released = true
			unlock()
		}
	}
	defer releaseLock()
	done := make(chan error, 1)
	go func() {
		_, err := service.TranslateSubtitleFile(context.Background(), videoPath,
			SubtitleTranslateRequest{VideoID: videoID, SourceLang: "en", TargetLang: "zh", Mode: SubtitleTranslateModeTranslatedOnly},
			SubtitleTranslationConfig{Provider: "llm", BaseURL: server.URL, Model: "local-qwen"})
		done <- err
	}()

	if !waitForTranslationRegistration(service, videoID) {
		t.Fatal("翻译任务没有在等锁期间完成取消登记，此时点取消会是空转")
	}
	service.CancelSubtitleTranslation(videoID)
	releaseLock()

	err := <-done
	if err == nil || !strings.Contains(err.Error(), "已取消") {
		t.Fatalf("等锁期间取消后应当报告已取消，实际: %v", err)
	}
	if got := mustReadSubtitle(t, srtPath); got != translatableSubtitle {
		t.Fatalf("取消不得改动原字幕")
	}
	if count := atomic.LoadInt32(&requests); count != 0 {
		t.Fatalf("取消后不应再发起翻译请求，实际发了 %d 次", count)
	}
}

// waitForTranslationRegistration 等到翻译任务完成取消登记（它发生在抢文件锁之前）。
func waitForTranslationRegistration(service *SubtitleService, videoID uint) bool {
	for attempt := 0; attempt < 200; attempt++ {
		service.mu.Lock()
		registered := len(service.translationCancels[videoID])
		service.mu.Unlock()
		if registered > 0 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestTranslateSRTKeepsEmptyTranslationOutOfContextWindow(t *testing.T) {
	service := NewSubtitleService(t.TempDir())
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "long.srt")
	outputPath := filepath.Join(dir, "long_out.srt")
	if err := os.WriteFile(inputPath, []byte(buildLongSubtitle(60)), 0644); err != nil {
		t.Fatalf("写入测试字幕失败: %v", err)
	}

	var mu sync.Mutex
	var prompts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("解析请求体失败: %v", err)
			return
		}
		mu.Lock()
		for _, message := range body.Messages {
			if message.Role == "user" {
				prompts = append(prompts, message.Content)
			}
		}
		size := 50
		if len(prompts) > 1 {
			size = 10
		}
		mu.Unlock()
		// 第一批的末尾几条返回空串，它们正好落在喂给下一批的上文窗口里。
		translations := make([]string, size)
		for i := range translations {
			if size == 50 && i >= 45 {
				continue
			}
			translations[i] = fmt.Sprintf("译文%d", i+1)
		}
		content, err := json.Marshal(map[string][]string{"translations": translations})
		if err != nil {
			t.Errorf("构造译文失败: %v", err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{{"message": map[string]string{"content": string(content)}}},
		})
	}))
	defer server.Close()

	translator := NewOpenAICompatibleSubtitleTranslator(SubtitleTranslationConfig{
		Provider: "llm", BaseURL: server.URL, Model: "local-qwen",
	})
	fallbacks, err := service.translateSRTWithProgress(context.Background(), inputPath, outputPath, "en", "zh", translator, nil, nil)
	if err != nil {
		t.Fatalf("翻译失败: %v", err)
	}
	if fallbacks != 5 {
		t.Fatalf("应当回退 5 条空译文，实际 %d", fallbacks)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 {
		t.Fatalf("60 条字幕应当分两批: %d", len(prompts))
	}
	// 空译文若原样进入上文，等于在提示词里示范「返回空是可以的」。
	if strings.Contains(prompts[1], `"target":""`) {
		t.Fatalf("上文窗口不应带上空译文:\n%s", prompts[1])
	}
	if !strings.Contains(prompts[1], `"target":"Line 50"`) {
		t.Fatalf("上文窗口应当带上回退后的原文:\n%s", prompts[1])
	}
}

func TestFinalizeSubtitleArtifactWarnsAboutUntranslatedEntries(t *testing.T) {
	setupVideoServiceTestDB(t)
	server, _ := startPromptCapturingLLMServer(t, func(string) string {
		return `{"choices":[{"message":{"content":"{\"translations\":[\"第一句\",\"\",\"第三句\"]}"}}]}`
	})
	dir := t.TempDir()
	video := mustCreateGlossaryVideo(t, filepath.Join(dir, "matrix.mp4"))
	srtPath := filepath.Join(dir, "matrix.srt")
	if err := os.WriteFile(srtPath, []byte(subtitleWithUntranslatableLine), 0644); err != nil {
		t.Fatalf("写入测试字幕失败: %v", err)
	}

	service := NewSubtitleService(t.TempDir())
	result, err := service.finalizeSubtitleArtifact(context.Background(), 1,
		SubtitleGenerateRequest{VideoID: video.ID, Engine: SubtitleEngineWhisperX},
		srtPath, "en", SubtitleGenerateOptions{
			BilingualEnabled:  true,
			BilingualLang:     "zh",
			TranslationConfig: SubtitleTranslationConfig{Provider: "llm", BaseURL: server.URL, Model: "local-qwen"},
		})
	if err != nil {
		t.Fatalf("双语收尾失败: %v", err)
	}
	// 自动双语和手动翻译走同一套空译文回退，告知口径也该一致。
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "1 条") {
		t.Fatalf("生成流程也应告知回退条数，实际警告: %v", result.Warnings)
	}
}
