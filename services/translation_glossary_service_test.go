package services

import (
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func mustCreateGlossaryCollection(t *testing.T, name string) models.MediaCollection {
	t.Helper()
	collection := models.MediaCollection{Name: name, NormalizedName: strings.ToLower(name)}
	if err := database.DB.Create(&collection).Error; err != nil {
		t.Fatalf("创建作品集失败: %v", err)
	}
	return collection
}

func mustCreateGlossaryVideo(t *testing.T, path string) models.Video {
	t.Helper()
	video := models.Video{Name: path, Path: path}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	return video
}

func mustLinkGlossaryCollectionVideo(t *testing.T, collectionID, videoID uint, position int) {
	t.Helper()
	if err := database.DB.Create(&models.CollectionVideo{CollectionID: collectionID, VideoID: videoID, Position: position}).Error; err != nil {
		t.Fatalf("加入作品集失败: %v", err)
	}
}

func mustUpsertGlossaryEntry(t *testing.T, entry models.TranslationGlossaryEntry) models.TranslationGlossaryEntry {
	t.Helper()
	saved, err := NewTranslationGlossaryService().Upsert(entry)
	if err != nil {
		t.Fatalf("写入术语失败 source=%q: %v", entry.SourceTerm, err)
	}
	return *saved
}

func mustSetGlossaryUpdatedAt(t *testing.T, id uint, updatedAt time.Time) {
	t.Helper()
	if err := database.DB.Model(&models.TranslationGlossaryEntry{}).Where("id = ?", id).UpdateColumn("updated_at", updatedAt).Error; err != nil {
		t.Fatalf("设置术语更新时间失败: %v", err)
	}
}

func collectionScope(id uint) *uint { return &id }

func TestGlossaryUpsertMaterializesScopeKeyAndLowercasedTerm(t *testing.T) {
	setupVideoServiceTestDB(t)
	collection := mustCreateGlossaryCollection(t, "黑客帝国")

	global := mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "  Neo  ", TargetTerm: " 尼奥 ", Note: " 主角 "})
	if global.ScopeKey != 0 || global.CollectionID != nil {
		t.Fatalf("全局条目应当 scope_key=0 且 collection_id 为空: %+v", global)
	}
	if global.SourceTerm != "Neo" || global.SourceTermLower != "neo" || global.TargetTerm != "尼奥" || global.Note != "主角" {
		t.Fatalf("术语未按写入时规范化: %+v", global)
	}

	scoped := mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{
		CollectionID: collectionScope(collection.ID), SourceTerm: "NEO", TargetTerm: "尼欧",
	})
	if scoped.ScopeKey != int64(collection.ID) {
		t.Fatalf("作品集条目 scope_key 应为 collection_id: %+v", scoped)
	}
	if scoped.ID == global.ID {
		t.Fatalf("不同作用域的同源词应当是两条独立记录: %+v / %+v", global, scoped)
	}
}

func TestGlossaryUpsertReplacesSameScopeSameTermCaseInsensitively(t *testing.T) {
	setupVideoServiceTestDB(t)
	first := mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Neo", TargetTerm: "尼奥"})
	second := mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "NEO", TargetTerm: "尼欧", Note: "改口径"})
	if second.ID != first.ID {
		t.Fatalf("同作用域同源词应当就地更新，first=%d second=%d", first.ID, second.ID)
	}
	if second.TargetTerm != "尼欧" || second.Note != "改口径" || second.SourceTerm != "NEO" {
		t.Fatalf("更新后的字段不正确: %+v", second)
	}
	entries, err := NewTranslationGlossaryService().List(nil)
	if err != nil {
		t.Fatalf("列出全局术语失败: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("同源词不应产生第二条记录: %+v", entries)
	}
}

func TestGlossaryUpsertByIDRenamesTermAndRejectsConflict(t *testing.T) {
	setupVideoServiceTestDB(t)
	neo := mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Neo", TargetTerm: "尼奥"})
	trinity := mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Trinity", TargetTerm: "崔妮蒂"})

	renamed := mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{ID: neo.ID, SourceTerm: "Neo Anderson", TargetTerm: "尼奥·安德森"})
	if renamed.ID != neo.ID || renamed.SourceTermLower != "neo anderson" {
		t.Fatalf("按 ID 改源词失败: %+v", renamed)
	}

	_, err := NewTranslationGlossaryService().Upsert(models.TranslationGlossaryEntry{ID: trinity.ID, SourceTerm: "neo anderson", TargetTerm: "别的"})
	if err != ErrGlossaryTermConflict {
		t.Fatalf("改名撞上同作用域另一条应报冲突，实际 err=%v", err)
	}
}

func TestGlossaryUpsertRejectsEmptyAndOverlongTerms(t *testing.T) {
	setupVideoServiceTestDB(t)
	service := NewTranslationGlossaryService()
	cases := []struct {
		label string
		entry models.TranslationGlossaryEntry
	}{
		{"空源词", models.TranslationGlossaryEntry{SourceTerm: "   ", TargetTerm: "尼奥"}},
		{"空目标词", models.TranslationGlossaryEntry{SourceTerm: "Neo", TargetTerm: " "}},
		{"超长源词", models.TranslationGlossaryEntry{SourceTerm: strings.Repeat("a", 201), TargetTerm: "尼奥"}},
		{"超长目标词", models.TranslationGlossaryEntry{SourceTerm: "Neo", TargetTerm: strings.Repeat("尼", 201)}},
		{"超长备注", models.TranslationGlossaryEntry{SourceTerm: "Neo", TargetTerm: "尼奥", Note: strings.Repeat("注", 501)}},
	}
	for _, testCase := range cases {
		if _, err := service.Upsert(testCase.entry); err == nil {
			t.Fatalf("%s 应当被拒绝", testCase.label)
		}
	}
	if _, err := service.Upsert(models.TranslationGlossaryEntry{
		SourceTerm: strings.Repeat("a", 200), TargetTerm: strings.Repeat("尼", 200), Note: strings.Repeat("注", 500),
	}); err != nil {
		t.Fatalf("源词/目标词 200 字符、备注 500 字符的边界值应当被接受: %v", err)
	}
}

func TestGlossaryListIsScopedAndDeleteRemovesEntry(t *testing.T) {
	setupVideoServiceTestDB(t)
	collection := mustCreateGlossaryCollection(t, "黑客帝国")
	mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Neo", TargetTerm: "尼奥"})
	scoped := mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{CollectionID: collectionScope(collection.ID), SourceTerm: "Neo", TargetTerm: "尼欧"})

	service := NewTranslationGlossaryService()
	globalEntries, err := service.List(nil)
	if err != nil {
		t.Fatalf("列出全局术语失败: %v", err)
	}
	if len(globalEntries) != 1 || globalEntries[0].TargetTerm != "尼奥" {
		t.Fatalf("全局作用域不应看到作品集条目: %+v", globalEntries)
	}
	scopedEntries, err := service.List(collectionScope(collection.ID))
	if err != nil {
		t.Fatalf("列出作品集术语失败: %v", err)
	}
	if len(scopedEntries) != 1 || scopedEntries[0].ID != scoped.ID {
		t.Fatalf("作品集作用域条目不正确: %+v", scopedEntries)
	}

	if err := service.Delete(scoped.ID); err != nil {
		t.Fatalf("删除术语失败: %v", err)
	}
	scopedEntries, err = service.List(collectionScope(collection.ID))
	if err != nil {
		t.Fatalf("列出作品集术语失败: %v", err)
	}
	if len(scopedEntries) != 0 {
		t.Fatalf("删除后作品集作用域应为空: %+v", scopedEntries)
	}
}

func TestGlossaryUniqueKeyRejectsDuplicateScopeAndTerm(t *testing.T) {
	setupVideoServiceTestDB(t)
	// 绕过服务层直接写库：这里验证的是唯一索引本身在两个后端上都建了出来。
	if err := database.DB.Create(&models.TranslationGlossaryEntry{
		ScopeKey: 0, SourceTerm: "Neo", SourceTermLower: "neo", TargetTerm: "尼奥",
	}).Error; err != nil {
		t.Fatalf("写入首条术语失败: %v", err)
	}
	err := database.DB.Create(&models.TranslationGlossaryEntry{
		ScopeKey: 0, SourceTerm: "neo", SourceTermLower: "neo", TargetTerm: "尼欧",
	}).Error
	if err == nil {
		t.Fatalf("(scope_key, source_term_lower) 唯一键未生效")
	}
}

func TestGlossaryEntriesCascadeWhenCollectionRowIsDeleted(t *testing.T) {
	setupVideoServiceTestDB(t)
	collection := mustCreateGlossaryCollection(t, "黑客帝国")
	scoped := mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{CollectionID: collectionScope(collection.ID), SourceTerm: "Neo", TargetTerm: "尼欧"})
	global := mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Trinity", TargetTerm: "崔妮蒂"})

	if err := database.DB.Unscoped().Delete(&models.MediaCollection{}, collection.ID).Error; err != nil {
		t.Fatalf("硬删除作品集失败: %v", err)
	}

	var remaining []models.TranslationGlossaryEntry
	if err := database.DB.Find(&remaining).Error; err != nil {
		t.Fatalf("读取术语失败: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != global.ID {
		t.Fatalf("作品集删除应当级联删掉它的条目（%d），只留全局条目（%d）：%+v", scoped.ID, global.ID, remaining)
	}
}

func TestResolveForVideoPrefersCollectionScopeOverGlobal(t *testing.T) {
	setupVideoServiceTestDB(t)
	collection := mustCreateGlossaryCollection(t, "黑客帝国")
	video := mustCreateGlossaryVideo(t, "/library/matrix.mp4")
	mustLinkGlossaryCollectionVideo(t, collection.ID, video.ID, 1)

	mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Neo", TargetTerm: "全局尼奥"})
	mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Trinity", TargetTerm: "崔妮蒂"})
	mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{CollectionID: collectionScope(collection.ID), SourceTerm: "neo", TargetTerm: "作品集尼奥"})

	terms, err := NewTranslationGlossaryService().ResolveForVideo(video.ID)
	if err != nil {
		t.Fatalf("解析术语生效集失败: %v", err)
	}
	if len(terms) != 2 {
		t.Fatalf("生效集应当是 2 条（覆盖后的 Neo 与全局 Trinity）: %+v", terms)
	}
	if terms[0].SourceTerm != "neo" || terms[0].TargetTerm != "作品集尼奥" {
		t.Fatalf("作品集级未覆盖全局级: %+v", terms)
	}
	if terms[1].SourceTerm != "Trinity" || terms[1].TargetTerm != "崔妮蒂" {
		t.Fatalf("全局条目应当保留: %+v", terms)
	}
}

func TestResolveForVideoTakesNewestEntryWhenCollectionsConflict(t *testing.T) {
	setupVideoServiceTestDB(t)
	older := mustCreateGlossaryCollection(t, "系列 A")
	newer := mustCreateGlossaryCollection(t, "系列 B")
	video := mustCreateGlossaryVideo(t, "/library/matrix.mp4")
	mustLinkGlossaryCollectionVideo(t, older.ID, video.ID, 1)
	mustLinkGlossaryCollectionVideo(t, newer.ID, video.ID, 1)

	olderEntry := mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{CollectionID: collectionScope(older.ID), SourceTerm: "Neo", TargetTerm: "旧尼奥"})
	newerEntry := mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{CollectionID: collectionScope(newer.ID), SourceTerm: "Neo", TargetTerm: "新尼奥"})
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	mustSetGlossaryUpdatedAt(t, olderEntry.ID, base)
	mustSetGlossaryUpdatedAt(t, newerEntry.ID, base.Add(time.Hour))

	terms, err := NewTranslationGlossaryService().ResolveForVideo(video.ID)
	if err != nil {
		t.Fatalf("解析术语生效集失败: %v", err)
	}
	if len(terms) != 1 || terms[0].TargetTerm != "新尼奥" {
		t.Fatalf("多作品集冲突应取 updated_at 最新的一条: %+v", terms)
	}

	// 反向：让旧作品集的条目变成最新，结论必须跟着翻转。
	mustSetGlossaryUpdatedAt(t, olderEntry.ID, base.Add(2*time.Hour))
	terms, err = NewTranslationGlossaryService().ResolveForVideo(video.ID)
	if err != nil {
		t.Fatalf("解析术语生效集失败: %v", err)
	}
	if len(terms) != 1 || terms[0].TargetTerm != "旧尼奥" {
		t.Fatalf("更新时间翻转后应改取另一条: %+v", terms)
	}
}

func TestResolveForVideoIgnoresDeletedCollectionsAndReturnsGlobalOnly(t *testing.T) {
	setupVideoServiceTestDB(t)
	collection := mustCreateGlossaryCollection(t, "黑客帝国")
	video := mustCreateGlossaryVideo(t, "/library/matrix.mp4")
	mustLinkGlossaryCollectionVideo(t, collection.ID, video.ID, 1)
	mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{SourceTerm: "Neo", TargetTerm: "全局尼奥"})
	mustUpsertGlossaryEntry(t, models.TranslationGlossaryEntry{CollectionID: collectionScope(collection.ID), SourceTerm: "Neo", TargetTerm: "作品集尼奥"})

	if err := database.DB.Delete(&models.MediaCollection{}, collection.ID).Error; err != nil {
		t.Fatalf("软删除作品集失败: %v", err)
	}

	terms, err := NewTranslationGlossaryService().ResolveForVideo(video.ID)
	if err != nil {
		t.Fatalf("解析术语生效集失败: %v", err)
	}
	if len(terms) != 1 || terms[0].TargetTerm != "全局尼奥" {
		t.Fatalf("软删除的作品集不应再参与生效集: %+v", terms)
	}

	lonely := mustCreateGlossaryVideo(t, "/library/other.mp4")
	terms, err = NewTranslationGlossaryService().ResolveForVideo(lonely.ID)
	if err != nil {
		t.Fatalf("解析术语生效集失败: %v", err)
	}
	if len(terms) != 1 || terms[0].TargetTerm != "全局尼奥" {
		t.Fatalf("无作品集的视频只应拿到全局条目: %+v", terms)
	}
}
