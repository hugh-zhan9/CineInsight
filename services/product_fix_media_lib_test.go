package services

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services/subtitleparser"

	"golang.org/x/text/encoding/traditionalchinese"
	"gorm.io/gorm"
)

// 本文件收纳「片库查询与字幕后端」独立评审修复的回归用例；测试名里的问题 ID 与问题清单一一对应。

// ===== I-1：检索/索引路径对怪编码保持宽松（MEDIA-02 回归） =====

const strangeEncodingSRT = "1\n00:00:00,000 --> 00:00:01,000\ncaf\xe9 na\xefve \x81\xff\n\n2\n00:00:01,000 --> 00:00:02,000\nsecond line\n"

func TestMEDIA02IndexPathsToleratesUnrecognizedEncoding(t *testing.T) {
	setupVideoServiceTestDB(t)
	cases := map[string][]byte{
		"cp1252 字节":      []byte(strangeEncodingSRT),
		"UTF-8 BOM 夹坏字节": append([]byte{0xEF, 0xBB, 0xBF}, []byte(strangeEncodingSRT)...),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", content)
			if _, _, err := subtitleparser.DecodeSubtitleBytes(content); !errors.Is(err, subtitleparser.ErrSubtitleEncodingUnknown) {
				t.Fatalf("前提：该样本必须是识别不了的编码, err=%v", err)
			}
			// 回收站恢复与扫描走的重建路径。
			err := database.Transaction(func(tx *gorm.DB) error { return rebuildSubtitleIndexTx(tx, video) })
			if err != nil {
				t.Fatalf("rebuildSubtitleIndexTx 不得因怪编码报错: %v", err)
			}
			// 字幕索引刷新（同步/保存后刷新）路径。
			if err := ensureSubtitleIndexForVideo(video); err != nil {
				t.Fatalf("ensureSubtitleIndexForVideo 不得因怪编码报错: %v", err)
			}
			if err := indexSubtitleFileForVideoID(video.ID, srtPath); err != nil {
				t.Fatalf("indexSubtitleFileForVideoID 不得因怪编码报错: %v", err)
			}
			var count int64
			database.DB.Model(&models.SubtitleSegment{}).Where("video_id = ?", video.ID).Count(&count)
			if count != 2 {
				t.Fatalf("宽松解析应仍能索引出 2 条字幕: %d", count)
			}
			// 工作台与翻译入口仍坚持 subtitle_encoding_not_utf8。
			if err := ensureUTF8SubtitleContent(content); err == nil {
				t.Fatal("ensureUTF8SubtitleContent 必须拒绝怪编码")
			} else if coded := (*SubtitleCodedError)(nil); !errors.As(err, &coded) || coded.Code != SubtitleErrorEncodingNotUTF8 {
				t.Fatalf("应为 subtitle_encoding_not_utf8: %v", err)
			}
		})
	}
}

// ===== I-2：Big5 / GB18030 歧义（MEDIA-02） =====

// big5Sample 挑的是 Big5 字节在 GB18030 下同样合法的常用字，且不含全角标点（Big5 的 A1xx 标点在
// GB18030 下无定义，会被识别为不干净）——这正是两种编码都能「干净」解码的歧义场景。
const big5Sample = "這是我說不有和那什麼所會還要對從過想道"

func big5SubtitleBytes(t *testing.T) []byte {
	t.Helper()
	body := "1\n00:00:00,000 --> 00:00:01,000\n" + big5Sample + "\n\n2\n00:00:01,000 --> 00:00:02,000\n天今昨朋友家裡國愛情生問題事\n"
	encoded, err := traditionalchinese.Big5.NewEncoder().String(body)
	if err != nil {
		t.Fatalf("编码 Big5 失败: %v", err)
	}
	return []byte(encoded)
}

func TestMEDIA02AmbiguousBig5GB18030ReturnsCandidatesAndConvertsByChoice(t *testing.T) {
	setupVideoServiceTestDB(t)
	raw := big5SubtitleBytes(t)

	detection, err := subtitleparser.DetectSubtitleEncoding(raw)
	if err != nil {
		t.Fatalf("识别失败: %v", err)
	}
	if len(detection.Candidates) != 2 || detection.Candidates[0].Encoding != subtitleparser.EncodingGB18030 || detection.Candidates[1].Encoding != subtitleparser.EncodingBig5 {
		t.Fatalf("Big5 样本两种编码都能干净解码，应返回歧义候选: %+v", detection)
	}
	if detection.Encoding != subtitleparser.EncodingGB18030 {
		t.Fatalf("主推测应保持 GB18030: %s", detection.Encoding)
	}
	if detection.Candidates[1].Preview == "" || !strings.Contains(detection.Candidates[1].Preview, big5Sample) {
		t.Fatalf("Big5 候选预览应是正确的繁体文字: %q", detection.Candidates[1].Preview)
	}
	if detection.Candidates[0].Preview == detection.Candidates[1].Preview {
		t.Fatal("两个候选预览必须不同，否则用户无从选择")
	}
	if got := strings.Count(detection.Candidates[1].Preview, "\n") + 1; got > 3 {
		t.Fatalf("预览最多 3 条字幕文本: %d", got)
	}
	// 非歧义（纯 GBK 简体样本被 Big5 解出的可能是乱码/不干净）与 UTF-8 不带候选。
	if d, err := subtitleparser.DetectSubtitleEncoding([]byte(writerTestSRT)); err != nil || len(d.Candidates) != 0 {
		t.Fatalf("UTF-8 不应带候选: %+v err=%v", d, err)
	}

	// 服务层：打开工作台时以带候选的 subtitle_encoding_not_utf8 返回。
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", raw)
	workbench := NewSubtitleWorkbenchService(nil)
	workbench.dataDir = t.TempDir()
	_, err = workbench.GetDocument(video)
	var coded *SubtitleCodedError
	if !errors.As(err, &coded) || coded.Code != SubtitleErrorEncodingNotUTF8 || len(coded.Candidates) != 2 {
		t.Fatalf("应返回带候选的编码错误: %v", err)
	}

	// 按用户选择的 gb18030 转换：得到与 Big5 不同的文字。
	gbText, err := subtitleparser.DecodeSubtitleBytesAs(raw, subtitleparser.EncodingGB18030)
	if err != nil {
		t.Fatal(err)
	}
	big5Text, err := subtitleparser.DecodeSubtitleBytesAs(raw, subtitleparser.EncodingBig5)
	if err != nil {
		t.Fatal(err)
	}
	if gbText == big5Text || !strings.Contains(big5Text, big5Sample) {
		t.Fatalf("Big5 样本按 big5 应得到正确文字，按 gb18030 应不同:\nbig5=%q\ngb=%q", big5Text, gbText)
	}
	if _, err := workbench.ConvertToUTF8(video, "utf-16le"); err == nil {
		t.Fatal("不在候选里的编码必须拒绝")
	}
	result, err := workbench.ConvertToUTF8(video, subtitleparser.EncodingBig5)
	if err != nil || result.Encoding != subtitleparser.EncodingBig5 {
		t.Fatalf("按 big5 转换失败: %+v err=%v", result, err)
	}
	if got := string(mustReadBytes(t, srtPath)); got != big5Text {
		t.Fatalf("写回的文本必须是按所选编码解码的结果:\n%q\nwant %q", got, big5Text)
	}
}

func TestMEDIA02ConvertBySelectedGB18030DiffersFromBig5(t *testing.T) {
	setupVideoServiceTestDB(t)
	raw := big5SubtitleBytes(t)
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", raw)
	workbench := NewSubtitleWorkbenchService(nil)
	workbench.dataDir = t.TempDir()
	if _, err := workbench.ConvertToUTF8(video, subtitleparser.EncodingGB18030); err != nil {
		t.Fatalf("按 gb18030 转换: %v", err)
	}
	if strings.Contains(string(mustReadBytes(t, srtPath)), big5Sample) {
		t.Fatal("按 gb18030 转换不应得到 Big5 的正确文字")
	}
}

func TestMEDIA02ParseFileFallsBackToRawBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.srt")
	if err := os.WriteFile(path, []byte(strangeEncodingSRT), 0644); err != nil {
		t.Fatal(err)
	}
	segments, err := subtitleparser.ParseFile(path)
	if err != nil || len(segments) != 2 {
		t.Fatalf("识别不了编码时 ParseFile 应宽松解析: %+v err=%v", segments, err)
	}
}

// ===== I-3：术语表按 ID 编辑的冲突查询带目标语言（MEDIA-07） =====

func TestMEDIA07GlossaryEditByIDOnlyConflictsWithinSameTargetLanguage(t *testing.T) {
	setupVideoServiceTestDB(t)
	zh := mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Neo", TargetTerm: "尼奥", TargetLanguage: "zh"})
	mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Neo", TargetTerm: "ネオ", TargetLanguage: "ja"})

	// 同源词存在另一语言条目：只改译文可以保存。
	saved, err := NewTranslationGlossaryService().Upsert(models.TranslationGlossaryEntry{
		ID: zh.ID, SourceTerm: "Neo", TargetTerm: "尼欧", TargetLanguage: "zh",
	})
	if err != nil || saved.TargetTerm != "尼欧" {
		t.Fatalf("另一语言存在同源词时只改译文应能保存: %+v err=%v", saved, err)
	}
	// 目标语言经规整后再比对：ZH 与 zh 同一语言。
	zh2 := mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Trinity", TargetTerm: "崔妮蒂", TargetLanguage: "zh"})
	mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Morpheus", TargetTerm: "墨菲斯", TargetLanguage: "zh"})
	if _, err := NewTranslationGlossaryService().Upsert(models.TranslationGlossaryEntry{
		ID: zh2.ID, SourceTerm: "morpheus", TargetTerm: "别的", TargetLanguage: "ZH",
	}); !errors.Is(err, ErrGlossaryTermConflict) {
		t.Fatalf("同语言同源词仍应冲突: %v", err)
	}
}

// ===== I-4：统一「是否有字幕」判定（MEDIA-08） =====

func TestMEDIA08SidecarSubtitleRules(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "movie.mp4")
	files := []string{
		"movie.mp4",
		"movie.srt",                         // 同名 .srt 本身：不算旁挂
		"movie.zh.srt",                      // 算
		"movie.EN.ASS",                      // 大小写不敏感：算
		"movie.ja.vtt",                      // 算
		"movie.cineinsight-pending.srt",     // 应用临时文件：不算
		".movie.srt.cineinsight-tmp-abcdef", // 应用临时文件：不算
		"movie.zh_translated_temp.srt",      // 翻译中间文件：不算
		"movie.txt",                         // 扩展名不对
		"movie2.zh.srt",                     // 基本名不同
		"other.zh.srt",                      // 别的视频的字幕
		"movie.real-target.srt",             // 符号链接到普通文件：算（作为链接目标）
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "movie.real-target.srt"), filepath.Join(dir, "movie.de.srt")); err != nil {
		t.Skipf("平台不支持符号链接: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing-target"), filepath.Join(dir, "movie.fr.srt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "movie.dir.srt"), 0755); err != nil {
		t.Fatal(err)
	}

	names, err := ListSidecarSubtitles(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"movie.EN.ASS", "movie.de.srt", "movie.ja.vtt", "movie.real-target.srt", "movie.zh.srt"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("旁挂字幕判定:\n got %v\nwant %v", names, want)
	}
	if ok, err := HasSidecarSubtitle(videoPath); err != nil || !ok {
		t.Fatalf("HasSidecarSubtitle: %v %v", ok, err)
	}

	empty := t.TempDir()
	if ok, err := HasSidecarSubtitle(filepath.Join(empty, "movie.mp4")); err != nil || ok {
		t.Fatalf("空目录: %v %v", ok, err)
	}
	if names, err := ListSidecarSubtitles(filepath.Join(empty, "gone", "movie.mp4")); err != nil || len(names) != 0 {
		t.Fatalf("目录不存在按没有处理: %v %v", names, err)
	}
	if IsSidecarSubtitleName("/x/movie.mp4", "MOVIE.SRT") {
		t.Fatal("大小写不同的同名 .srt 也是同名字幕本身")
	}
}

func TestMEDIA08MissingSidecarErrorUsesSharedRules(t *testing.T) {
	setupVideoServiceTestDB(t)
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "movie.mp4")
	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: dir}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	codeOf := func(err error) string {
		var coded *SubtitleCodedError
		if errors.As(err, &coded) {
			return coded.Code
		}
		return ""
	}
	if got := codeOf(missingSidecarError(video.ID, videoPath)); got != SubtitleErrorMissing {
		t.Fatalf("什么字幕都没有: %q", got)
	}
	// 只有应用临时文件不算有字幕。
	if err := os.WriteFile(filepath.Join(dir, "movie.cineinsight-pending.srt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := codeOf(missingSidecarError(video.ID, videoPath)); got != SubtitleErrorMissing {
		t.Fatalf("临时文件不算字幕: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "movie.zh.ass"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := codeOf(missingSidecarError(video.ID, videoPath)); got != SubtitleErrorNotSidecarSRT {
		t.Fatalf("旁挂 .ass: %q", got)
	}
	if err := os.Remove(filepath.Join(dir, "movie.zh.ass")); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.MediaStream{VideoID: video.ID, StreamIndex: 2, StreamType: "subtitle"}).Error; err != nil {
		t.Fatal(err)
	}
	if got := codeOf(missingSidecarError(video.ID, videoPath)); got != SubtitleErrorNotSidecarSRT {
		t.Fatalf("内嵌字幕流: %q", got)
	}
	// 读库出错时返回错误，不悄悄当作 subtitle_missing。
	if err := database.DB.Migrator().DropTable(&models.MediaStream{}); err != nil {
		t.Fatal(err)
	}
	err := missingSidecarError(video.ID, videoPath)
	if err == nil || codeOf(err) != "" {
		t.Fatalf("读库失败应返回普通错误而不是编码错误: %v", err)
	}
}

func TestMEDIA08EmbeddedSubtitleLiteralDefinedOnce(t *testing.T) {
	root := ".."
	literal := "stream_type = 'subtitle'"
	count := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case "node_modules", ".git", ".loopx", ".claude", "frontend", "docs", "build":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		count += strings.Count(string(data), literal)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("%q 全仓非测试代码应只出现一次: %d", literal, count)
	}
	if !strings.Contains(hasAnySubtitleSQL, hasEmbeddedSubtitleSQL) {
		t.Fatal("hasAnySubtitleSQL 必须由 hasEmbeddedSubtitleSQL 拼成")
	}
}

// ===== Minor 2：保存视图新建/更新重名统一 saved_view_name_taken（LIB-15） =====

func TestLIB15SavedViewNameConflictMappedOnCreateAndUpdate(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := &VideoService{}
	a, err := svc.SaveLibraryView(SavedLibraryViewInput{Name: "甲"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveLibraryView(SavedLibraryViewInput{Name: "乙"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveLibraryView(SavedLibraryViewInput{Name: " 甲 "}); !errors.Is(err, ErrSavedViewNameTaken) {
		t.Fatalf("新建重名应返回 saved_view_name_taken: %v", err)
	}
	// 用真实的后端报错验证识别函数（竞态路径绕过预检时就是这条报错）。
	raceErr := database.DB.Model(&models.SavedLibraryView{}).Where("id = ?", a.ID).Update("name", "乙").Error
	if raceErr == nil || !savedViewNameConflict(raceErr) {
		t.Fatalf("真实唯一键冲突应被识别: %v", raceErr)
	}
	for _, message := range []string{
		`ERROR: duplicate key value violates unique constraint "idx_saved_library_views_name_active" (SQLSTATE 23505)`,
	} {
		if !savedViewNameConflict(errors.New(message)) {
			t.Fatalf("应识别 Postgres 报错: %s", message)
		}
	}
	if !savedViewNameConflict(gorm.ErrDuplicatedKey) || savedViewNameConflict(errors.New("disk I/O error")) || savedViewNameConflict(nil) {
		t.Fatal("识别函数边界不对")
	}
}

// ===== Minor 3：NFO 导出不处理失效视图（META-02 同批） =====

func TestLIB01LocalMetadataExportExcludesStaleEvenForStaleFilter(t *testing.T) {
	setupVideoServiceTestDB(t)
	live := libVisVideo(t, "/lib/a/x.mp4", nil)
	stale := libVisVideo(t, "/lib/b/x.mp4", func(v *models.Video) { v.IsStale = true })
	service := &LocalMetadataService{}
	ids, err := service.collectExportVideoIDs(context.Background(), LibraryFilter{})
	if err != nil || !reflect.DeepEqual(ids, []uint{live.ID}) {
		t.Fatalf("默认筛选: %v err=%v", ids, err)
	}
	ids, err = service.collectExportVideoIDs(context.Background(), LibraryFilter{SmartView: LibraryViewStale})
	if err != nil || len(ids) != 0 {
		t.Fatalf("失效视图不得导出 NFO（含 %d）: %v err=%v", stale.ID, ids, err)
	}
}

// ===== Minor 5：人物条件与标签同语义（META-02） =====

func TestMETA02MergePeopleRewritesSavedViewsAndActivePersonIDs(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewPersonService(t.TempDir())
	target := models.Person{DisplayName: "目标"}
	src1, src2, other := models.Person{DisplayName: "来源一"}, models.Person{DisplayName: "来源二"}, models.Person{DisplayName: "无关"}
	for _, person := range []*models.Person{&target, &src1, &src2, &other} {
		if err := database.DB.Create(person).Error; err != nil {
			t.Fatal(err)
		}
	}
	views := &VideoService{}
	both, err := views.SaveLibraryView(SavedLibraryViewInput{Name: "含目标与来源", LibraryFilter: LibraryFilter{PersonIDs: []uint{target.ID, src1.ID, other.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	onlySources, err := views.SaveLibraryView(SavedLibraryViewInput{Name: "两个来源", LibraryFilter: LibraryFilter{PersonIDs: []uint{src1.ID, src2.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	untouched, err := views.SaveLibraryView(SavedLibraryViewInput{Name: "无关", LibraryFilter: LibraryFilter{PersonIDs: []uint{other.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MergePeople(target.ID, []uint{src1.ID, src2.ID}); err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	reload := func(id uint) []uint {
		var view models.SavedLibraryView
		if err := database.DB.First(&view, id).Error; err != nil {
			t.Fatal(err)
		}
		ids, err := savedViewPersonIDs(view)
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	if got := reload(both.ID); !reflect.DeepEqual(got, []uint{target.ID, other.ID}) {
		t.Fatalf("来源 ID 应改写为目标并去重: %v", got)
	}
	if got := reload(onlySources.ID); !reflect.DeepEqual(got, []uint{target.ID}) {
		t.Fatalf("多个来源应合成一个目标: %v", got)
	}
	if got := reload(untouched.ID); !reflect.DeepEqual(got, []uint{other.ID}) {
		t.Fatalf("无关视图不应改动: %v", got)
	}

	// 删除人物后视图保留原 ID，应用前 activePersonIDs 忽略并计数。
	if err := database.DB.Delete(&models.Person{}, other.ID).Error; err != nil {
		t.Fatal(err)
	}
	res, err := views.FilterActivePersonIDs([]uint{other.ID, target.ID, target.ID, 9999})
	if err != nil || !reflect.DeepEqual(res.PersonIDs, []uint{target.ID}) || res.Dropped != 2 {
		t.Fatalf("activePersonIDs: %+v err=%v", res, err)
	}
	if got := reload(untouched.ID); !reflect.DeepEqual(got, []uint{other.ID}) {
		t.Fatalf("删除人物不改写视图，保留原 ID: %v", got)
	}
	res, err = views.FilterActivePersonIDs(nil)
	if err != nil || len(res.PersonIDs) != 0 || res.Dropped != 0 {
		t.Fatalf("空输入: %+v err=%v", res, err)
	}
}

func savedViewPersonIDs(view models.SavedLibraryView) ([]uint, error) {
	var ids []uint
	err := json.Unmarshal([]byte(view.PersonIDsJSON), &ids)
	return ids, err
}

// ===== Minor 6：生成/翻译提前拒绝非普通文件；替换失败保留 pending（MEDIA-01） =====

func TestMEDIA01GenerateAndTranslateRejectNonRegularTargetEarly(t *testing.T) {
	setupVideoServiceTestDB(t)
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "movie.mp4")
	real := filepath.Join(dir, "real.srt")
	if err := os.WriteFile(videoPath, []byte("v"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte(writerTestSRT), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "movie.srt")); err != nil {
		t.Skipf("平台不支持符号链接: %v", err)
	}
	video := models.Video{Name: "movie.mp4", Path: videoPath, Directory: dir}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	service := NewSubtitleService(t.TempDir())

	// 生成：在查引擎状态、跑识别之前就报错。
	_, err := service.executeSubtitleTask(context.Background(), 1,
		SubtitleGenerateRequest{VideoID: video.ID, Engine: SubtitleEngineWhisperX, SourceLang: "en"}, videoPath, SubtitleGenerateOptions{})
	if err == nil || !strings.Contains(err.Error(), "不是普通文件") {
		t.Fatalf("生成应提前拒绝符号链接目标: %v", err)
	}
	// 翻译：不必翻译就失败（配置不完整也不会走到翻译器）。
	_, err = service.TranslateSubtitleFile(context.Background(), videoPath,
		SubtitleTranslateRequest{VideoID: video.ID, SourceLang: "en", TargetLang: "zh", Mode: SubtitleTranslateModeTranslatedOnly},
		SubtitleTranslationConfig{Provider: "llm", BaseURL: "http://127.0.0.1:1", Model: "x"})
	if err == nil || !strings.Contains(err.Error(), "不是普通文件") {
		t.Fatalf("翻译应提前拒绝符号链接目标: %v", err)
	}
	if string(mustReadBytes(t, real)) != writerTestSRT {
		t.Fatal("被链接的真实文件不得被改动")
	}
}

func TestMEDIA01ReplaceFailureKeepsPendingAndForceRetryFinishesWithoutTranscribing(t *testing.T) {
	setupVideoServiceTestDB(t)
	original := []byte(writerTestSRT)
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", original)
	// BaseDir 为空时已有字幕无法备份，Replace 必然失败：正是「收尾替换失败」的场景。
	service := NewSubtitleService("")
	req := SubtitleGenerateRequest{VideoID: video.ID, Engine: SubtitleEngineWhisperX, SourceLang: "en"}

	if _, err := service.commitTranscription(context.Background(), 1, req, video.Path, "en", realSegments(), SubtitleGenerateOptions{}); err == nil {
		t.Fatal("替换应失败")
	}
	pendingPath := subtitlePendingPath(srtPath)
	if _, err := os.Stat(pendingPath); err != nil {
		t.Fatalf("替换失败必须保留 pending 临时文件: %v", err)
	}
	if artifact := service.peekPendingSubtitle(video.ID); artifact == nil || artifact.SRTPath != pendingPath || artifact.VideoPath != video.Path {
		t.Fatalf("替换失败必须保留登记: %+v", artifact)
	}
	if string(mustReadBytes(t, srtPath)) != string(original) {
		t.Fatal("替换失败时原字幕不得改动")
	}

	// 「强制生成」重试：不重跑识别（未准备任何引擎），直接收尾。
	service.BaseDir = t.TempDir()
	result, err := service.executeSubtitleTask(context.Background(), 2, req, video.Path, SubtitleGenerateOptions{ForceGenerate: true})
	if err != nil || result.Status != SubtitleResultStatusSuccess {
		t.Fatalf("强制重试应直接收尾成功: %+v err=%v", result, err)
	}
	if !strings.Contains(string(mustReadBytes(t, srtPath)), "Hello there") {
		t.Fatal("重试后 .srt 应是转写结果")
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("重试成功后临时文件应删除: %v", err)
	}
	if service.peekPendingSubtitle(video.ID) != nil {
		t.Fatal("重试成功后应清掉登记")
	}
	if backups, _ := service.subtitleWriter().ListBackups(video.ID); len(backups) != 1 {
		t.Fatalf("旧字幕应进备份: %+v", backups)
	}
}

// 强制生成分支的端到端：校验未过缓存 pending → 强制生成时查找 pending、加锁、收尾、清登记。
func TestMEDIA01ForceGenerateBranchEndToEnd(t *testing.T) {
	setupVideoServiceTestDB(t)
	original := []byte(writerTestSRT)
	video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", original)
	service := NewSubtitleService(t.TempDir())
	req := SubtitleGenerateRequest{VideoID: video.ID, Engine: SubtitleEngineWhisperX, SourceLang: "en"}

	result, err := service.commitTranscription(context.Background(), 1, req, video.Path, "en", hallucinatedSegments(), SubtitleGenerateOptions{})
	if err != nil || result.Status != SubtitleResultStatusValidationFailed {
		t.Fatalf("应校验失败并缓存 pending: %+v err=%v", result, err)
	}
	if service.peekPendingSubtitle(video.ID) == nil {
		t.Fatal("pending 应已登记")
	}

	// 强制生成的收尾期间字幕文件锁必须被持有：收尾中途（双语翻译前读术语表那一刻）另起
	// 协程抢同一 .srt 的锁，必须等到收尾结束才拿得到。探针挂在 glossaryResolver 上，
	// 它只在 finalizeSubtitleArtifact 内部、Replace 之前被调用。
	translations := make([]string, 10)
	for i := range translations {
		translations[i] = "谢谢。"
	}
	reply, _ := json.Marshal(map[string][]string{"translations": translations})
	server, _ := startPromptCapturingLLMServer(t, func(string) string {
		body, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": string(reply)}}}})
		return string(body)
	})
	probe := make(chan bool, 1) // true：收尾期间抢锁被挡住
	probeAcquired := make(chan func(), 1)
	service.glossaryResolver = func(uint, string) ([]GlossaryTerm, error) {
		go func() { probeAcquired <- lockSubtitleFile(srtPath) }()
		select {
		case release := <-probeAcquired:
			release()
			probe <- false
		case <-time.After(200 * time.Millisecond):
			probe <- true
		}
		return nil, nil
	}
	forceOptions := SubtitleGenerateOptions{
		ForceGenerate: true, BilingualEnabled: true, BilingualLang: "zh",
		TranslationConfig: SubtitleTranslationConfig{Provider: "llm", BaseURL: server.URL, Model: "local-qwen"},
	}
	forced := make(chan error, 1)
	go func() {
		got, err := service.executeSubtitleTask(context.Background(), 2, req, video.Path, forceOptions)
		if err == nil && got.Status != SubtitleResultStatusSuccess {
			err = errors.New("强制生成未成功: " + string(got.Status))
		}
		forced <- err
	}()
	select {
	case err := <-forced:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("强制生成超时")
	}
	select {
	case blocked := <-probe:
		if !blocked {
			t.Fatal("收尾期间另一协程拿到了同一 .srt 的锁：强制生成分支没有持锁")
		}
	default:
		t.Fatal("探针没有运行：收尾没有走到双语翻译")
	}
	// 收尾结束后锁已释放：探针协程随即拿到锁，放掉它（包级条带锁，不能留着）。
	select {
	case release := <-probeAcquired:
		release()
	case <-time.After(5 * time.Second):
		t.Fatal("收尾结束后锁应已释放")
	}
	unlock := lockSubtitleFile(srtPath)
	unlock()
	if !strings.Contains(string(mustReadBytes(t, srtPath)), "Thank you.") {
		t.Fatal("强制生成后 .srt 应是转写结果")
	}
	if _, err := os.Stat(subtitlePendingPath(srtPath)); !os.IsNotExist(err) {
		t.Fatalf("临时文件应删除: %v", err)
	}
	if service.peekPendingSubtitle(video.ID) != nil {
		t.Fatal("应清掉 pending 登记")
	}
	if backups, _ := service.subtitleWriter().ListBackups(video.ID); len(backups) != 1 {
		t.Fatalf("旧字幕应进备份: %+v", backups)
	}
}

// ===== Minor 7：空/空白旧 .srt 当作空白文档打开（MEDIA-06） =====

func TestMEDIA06EmptyOrBlankSRTOpensAsBlankDocumentAndSavesWithBackup(t *testing.T) {
	setupVideoServiceTestDB(t)
	for name, content := range map[string][]byte{
		"0 字节":    {},
		"空白":      []byte("  \n\t\n"),
		"BOM 加空白": append([]byte{0xEF, 0xBB, 0xBF}, []byte("\n ")...),
	} {
		t.Run(name, func(t *testing.T) {
			video, srtPath := mustCreateSubtitleVideo(t, "movie.mp4", content)
			workbench := NewSubtitleWorkbenchService(nil)
			workbench.dataDir = t.TempDir()
			document, err := workbench.GetDocument(video)
			if err != nil {
				t.Fatalf("空文件应能作为空白文档打开: %v", err)
			}
			if len(document.Entries) != 0 || document.Fingerprint.SHA256 == "" || document.Fingerprint.Size != int64(len(content)) {
				t.Fatalf("空白文档指纹应取现有文件: %+v", document)
			}
			saved, err := workbench.SaveDocument(video, SubtitleSaveRequest{
				VideoID: video.ID, Fingerprint: document.Fingerprint,
				Entries: []subtitleparser.EditorSegment{{ClientID: "a", StartTimeMs: 0, EndTimeMs: 1000, Text: "新增字幕"}},
			})
			if err != nil || saved.Status != SubtitleSaveStatusSaved {
				t.Fatalf("保存应成功: %+v err=%v", saved, err)
			}
			if saved.BackupID == "" {
				t.Fatal("已有（哪怕是空的）旧文件必须正常备份后替换")
			}
			if !strings.Contains(string(mustReadBytes(t, srtPath)), "新增字幕") {
				t.Fatal("字幕应已写入")
			}
		})
	}
}

// ===== Minor 8：备份/恢复权限；按 .srt 路径加锁（MEDIA-05） =====

func TestMEDIA05BackupInheritsPermissionAndRestoreToDeletedTargetUsesIt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "movie.srt")
	writer := NewSubtitleFileWriter(t.TempDir())
	if _, err := writer.Replace(context.Background(), 3, target, []byte("v0")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0640); err != nil {
		t.Fatal(err)
	}
	result, err := writer.Replace(context.Background(), 3, target, []byte("v1"))
	if err != nil || result.BackupID == "" {
		t.Fatalf("覆盖应产生备份: %+v err=%v", result, err)
	}
	backupPath := filepath.Join(writer.dataDir, subtitleBackupDirName, "3", result.BackupID+".srt")
	if info, err := os.Stat(backupPath); err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("备份权限应继承原文件 0640: %v %v", info, err)
	}
	// 目标被删除后恢复：用备份记录的原权限，而不是默认 0644。
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.RestoreBackup(context.Background(), 3, target, result.BackupID); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0640 || string(mustReadBytes(t, target)) != "v0" {
		t.Fatalf("恢复到已删除目标应使用原权限 0640: %v %v", info, err)
	}
}

func TestMEDIA05SubtitleLockIsKeyedByNormalizedSRTPath(t *testing.T) {
	srtA := subtitleparser.SRTPathForVideo("/media/movie.mp4")
	srtB := subtitleparser.SRTPathForVideo("/media/./movie.mkv")
	unlock := lockSubtitleFile(srtA)
	acquired := make(chan func(), 1)
	go func() { acquired <- lockSubtitleFile(srtB) }()
	select {
	case <-acquired:
		t.Fatal("movie.mp4 与 movie.mkv 共用 movie.srt，必须互斥")
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case release := <-acquired:
		release()
	case <-time.After(5 * time.Second):
		t.Fatal("释放后应能拿到锁")
	}
}

// ===== Minor 9：subtitleFinalPathForPending 拒绝非 pending 形态（MEDIA-01） =====

func TestMEDIA01FinalPathForPendingRejectsNonPending(t *testing.T) {
	if _, err := subtitleFinalPathForPending("/a/movie.srt"); err == nil {
		t.Fatal("非 pending 形态必须报错")
	}
	got, err := subtitleFinalPathForPending(subtitlePendingPath("/a/movie.srt"))
	if err != nil || got != "/a/movie.srt" {
		t.Fatalf("往返: %q %v", got, err)
	}
}
