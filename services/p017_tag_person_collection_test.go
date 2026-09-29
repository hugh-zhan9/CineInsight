package services

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// P-017：标签、人物、本地资料与建议作品集（后端）的回归测试。
// 测试名里的问题 ID 与问题清单一一对应：META01（规则 4）、META02、META03、META05、
// META07、META09、META10、META12（规则 6）、META13、META14、PLAY14（评分）。META15 在
// collection_suggestion_service_test.go。

func p017AIState(t *testing.T, videoID uint) models.AITaggingState {
	t.Helper()
	state := models.AITaggingState{VideoID: videoID, Status: models.AITaggingStateStatusCompleted, EvidenceFingerprint: "fp"}
	if err := database.DB.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	return state
}

func p017Fingerprint(t *testing.T, videoID uint) string {
	t.Helper()
	var state models.AITaggingState
	if err := database.DB.Where("video_id = ?", videoID).First(&state).Error; err != nil {
		t.Fatal(err)
	}
	return state.EvidenceFingerprint
}

func p017Video(t *testing.T, name string) models.Video {
	t.Helper()
	video := models.Video{Name: name, Path: "/p017/" + name}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	return video
}

// META01：只改颜色不重排队；内容变化（改名、改分类、新建、删除）才重排队。
func TestTagServiceMETA01ResetsAITaggingOnlyWhenVocabularyChanges(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := p017Video(t, "a.mp4")
	tag := models.Tag{Name: "科幻", Color: "#111111"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	svc := &TagService{}
	p017AIState(t, video.ID)

	if err := svc.UpdateTag(tag.ID, "科幻", "#222222"); err != nil {
		t.Fatal(err)
	}
	if got := p017Fingerprint(t, video.ID); got != "fp" {
		t.Fatalf("只改颜色不应重排队，fingerprint=%q", got)
	}
	if err := svc.UpdateTagWithCategory(tag.ID, "科幻", "#333333", ""); err != nil {
		t.Fatal(err)
	}
	if got := p017Fingerprint(t, video.ID); got != "fp" {
		t.Fatalf("分类与名字都没变不应重排队，fingerprint=%q", got)
	}

	renamed := []struct {
		name string
		act  func() error
	}{
		{"改名", func() error { return svc.UpdateTag(tag.ID, "科幻片", "#333333") }},
		{"改分类", func() error { return svc.UpdateTagWithCategory(tag.ID, "科幻片", "#333333", "类型") }},
		{"新建", func() error { _, err := svc.CreateTag("悬疑", ""); return err }},
		{"合并", func() error {
			other, err := svc.CreateTag("推理", "")
			if err != nil {
				return err
			}
			p017ResetFingerprint(t, video.ID)
			_, err = svc.MergeTags([]uint{other.ID}, tag.ID)
			return err
		}},
		{"删除", func() error { return svc.DeleteTag(tag.ID) }},
	}
	for _, step := range renamed {
		p017ResetFingerprint(t, video.ID)
		if err := step.act(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if got := p017Fingerprint(t, video.ID); got != "" {
			t.Fatalf("%s应重排队，fingerprint=%q", step.name, got)
		}
	}
}

func p017ResetFingerprint(t *testing.T, videoID uint) {
	t.Helper()
	if err := database.DB.Model(&models.AITaggingState{}).Where("video_id = ?", videoID).Updates(map[string]any{
		"status": models.AITaggingStateStatusCompleted, "evidence_fingerprint": "fp",
	}).Error; err != nil {
		t.Fatal(err)
	}
}

// META12：「人物」分类的标签不在 AI 词表内，词表变化判定与候选作废都排除它。
func TestTagServiceMETA12PersonCategoryTagsAreOutsideAIVocabulary(t *testing.T) {
	setupVideoServiceTestDB(t)
	if isAITagEligible(models.Tag{Name: "张三", Namespace: " 人物 "}) {
		t.Fatal("人物分类标签不应入 AI 词表")
	}
	if !isAITagEligible(models.Tag{Name: "科幻", Namespace: "类型"}) {
		t.Fatal("普通分类标签应入词表")
	}
	video := p017Video(t, "a.mp4")
	svc := &TagService{}
	p017AIState(t, video.ID)

	person, err := svc.CreateTagWithCategory("李四", "", "人物")
	if err != nil {
		t.Fatal(err)
	}
	if got := p017Fingerprint(t, video.ID); got != "fp" {
		t.Fatalf("新建人物标签不应重排队，fingerprint=%q", got)
	}
	if err := svc.UpdateTag(person.ID, "李四改", person.Color); err != nil {
		t.Fatal(err)
	}
	if got := p017Fingerprint(t, video.ID); got != "fp" {
		t.Fatalf("人物标签改名不应重排队，fingerprint=%q", got)
	}

	// 已指向人物标签的待审候选（历史遗留）在下一次词表对账时作废。
	candidate := models.AITagCandidate{VideoID: video.ID, MatchedTagID: &person.ID, SuggestedName: "李四改", Confidence: "high", Status: models.AITagCandidateStatusPending}
	if err := database.DB.Create(&candidate).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTag("科幻", ""); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.First(&candidate, candidate.ID).Error; err != nil {
		t.Fatal(err)
	}
	if candidate.Status != models.AITagCandidateStatusSuperseded {
		t.Fatalf("指向人物标签的待审候选应作废: %+v", candidate)
	}
}

// META02：撤销后完整复原（标签、打标关系、新建的人物与关系），已存在的关系不被误删。
func TestTagPersonConversionMETA02UndoRestoresEverything(t *testing.T) {
	tag, videos, images := conversionFixture(t)
	svc := &TagService{}
	result, err := svc.ConvertTagToPerson(TagPersonConversionRequest{TagID: tag.ID, TagName: tag.Name, CreateNew: true})
	if err != nil || result.ConversionID == 0 {
		t.Fatalf("转换失败: %+v err=%v", result, err)
	}
	records, err := svc.ListTagPersonConversions(10)
	if err != nil || len(records) != 1 || records[0].State != models.TagPersonConversionApplied ||
		records[0].TagName != tag.Name || records[0].VideoCount != 2 || records[0].ImageCount != 2 || records[0].PersonName != tag.Name {
		t.Fatalf("转换记录不符: %+v err=%v", records, err)
	}

	undo, err := svc.UndoTagPersonConversion(result.ConversionID)
	if err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	if !undo.PersonDeleted || undo.Tag.ID != tag.ID {
		t.Fatalf("撤销回执不符: %+v", undo)
	}
	var restored models.Tag
	if err := database.DB.First(&restored, tag.ID).Error; err != nil || restored.Name != tag.Name {
		t.Fatalf("标签应恢复为活跃: %+v err=%v", restored, err)
	}
	assertConversionCount(t, "video_tags", "tag_id = ?", []any{tag.ID}, 2)
	assertConversionCount(t, "image_tags", "tag_id = ?", []any{tag.ID}, 2)
	assertConversionCount(t, "video_people", "1=1", nil, 0)
	assertConversionCount(t, "image_people", "1=1", nil, 0)
	assertConversionCount(t, "people", "1=1", nil, 0)
	_ = videos
	_ = images

	if _, err := svc.UndoTagPersonConversion(result.ConversionID); !errors.Is(err, ErrConversionNotApplied) {
		t.Fatalf("重复撤销应返回 conversion_not_applied: %v", err)
	}
	if _, err := svc.UndoTagPersonConversion(9999); !errors.Is(err, ErrConversionNotFound) {
		t.Fatalf("不存在的记录: %v", err)
	}
	records, _ = svc.ListTagPersonConversions(10)
	if records[0].State != models.TagPersonConversionUndone || records[0].UndoneAt == nil {
		t.Fatalf("撤销后记录应为 undone: %+v", records[0])
	}
}

// META02：关联到已有人物时，撤销只删除本次新增的关系，保留人物与转换前的关系。
func TestTagPersonConversionMETA02UndoKeepsPreexistingPersonRelations(t *testing.T) {
	tag, videos, _ := conversionFixture(t)
	person := models.Person{DisplayName: "张三"}
	if err := database.DB.Create(&person).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.VideoPerson{VideoID: videos[0].ID, PersonID: person.ID}).Error; err != nil {
		t.Fatal(err)
	}
	svc := &TagService{}
	result, err := svc.ConvertTagToPerson(TagPersonConversionRequest{TagID: tag.ID, TagName: tag.Name, TargetPersonID: person.ID})
	if err != nil {
		t.Fatal(err)
	}
	assertConversionCount(t, "video_people", "person_id = ?", []any{person.ID}, 2)
	if _, err := svc.UndoTagPersonConversion(result.ConversionID); err != nil {
		t.Fatal(err)
	}
	assertConversionCount(t, "video_people", "person_id = ?", []any{person.ID}, 1)
	assertConversionCount(t, "video_people", "person_id = ? AND video_id = ?", []any{person.ID, videos[0].ID}, 1)
	assertConversionCount(t, "image_people", "person_id = ?", []any{person.ID}, 0)
	assertConversionCount(t, "people", "id = ?", []any{person.ID}, 1)
}

// META02：期间已用同名重新建过标签时返回 tag_name_taken，整个撤销回滚。
func TestTagPersonConversionMETA02UndoRollsBackOnNameTaken(t *testing.T) {
	tag, _, _ := conversionFixture(t)
	svc := &TagService{}
	result, err := svc.ConvertTagToPerson(TagPersonConversionRequest{TagID: tag.ID, TagName: tag.Name, CreateNew: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTag(tag.Name, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UndoTagPersonConversion(result.ConversionID); !errors.Is(err, ErrTagNameTaken) {
		t.Fatalf("应返回 tag_name_taken: %v", err)
	}
	var record models.TagPersonConversion
	if err := database.DB.First(&record, result.ConversionID).Error; err != nil || record.State != models.TagPersonConversionApplied {
		t.Fatalf("回滚后记录仍应是 applied: %+v err=%v", record, err)
	}
	assertConversionCount(t, "video_people", "person_id = ?", []any{result.Person.ID}, 2)
	assertConversionCount(t, "people", "id = ?", []any{result.Person.ID}, 1)
	assertConversionCount(t, "video_tags", "tag_id = ?", []any{tag.ID}, 0)
}

func p017NFO(t *testing.T, root, name, body string) models.Video {
	t.Helper()
	videoPath := filepath.Join(root, name+".mp4")
	mustCreateFile(t, videoPath)
	if body != "" {
		if err := os.WriteFile(filepath.Join(root, name+".nfo"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	video := models.Video{Name: name + ".mp4", Path: videoPath, Directory: root}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	return video
}

// META03：同一批次内同一规范化来源名只决策一次，create_new 只创建一个人物，后续视频复用。
func TestLocalMetadataMETA03BatchCreatesSameNamedPersonOnce(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	service := NewLocalMetadataService(t.TempDir(), NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	var videos []models.Video
	for _, name := range []string{"one", "two", "three"} {
		videos = append(videos, p017NFO(t, root, name, `<movie><actor><name>New  Actor</name></actor></movie>`))
	}
	ids := []uint{videos[0].ID, videos[1].ID, videos[2].ID}
	preview := service.PreviewBatch(ids)
	if len(preview.Diffs) != 3 || len(preview.PeopleDecisions) != 1 {
		t.Fatalf("同名来源应合并为一个决策: %+v", preview.PeopleDecisions)
	}
	decision := preview.PeopleDecisions[0]
	if decision.NormalizedName != "new actor" || decision.DefaultMode != "create_new" || len(decision.VideoIDs) != 3 {
		t.Fatalf("决策不符: %+v", decision)
	}

	requests := make([]LocalMetadataApplyRequest, 0, len(preview.Diffs))
	for _, diff := range preview.Diffs {
		requests = append(requests, LocalMetadataApplyRequest{
			VideoID: diff.VideoID, ManifestSHA256: diff.ManifestSHA256, CurrentSHA256: diff.CurrentSHA256,
			SelectedFields: []string{"people"},
		})
	}
	result := service.ApplyBatch(LocalMetadataBatchApplyRequest{
		Requests:         requests,
		BatchResolutions: map[string]LocalMetadataResolution{"new actor": {Mode: "create_new"}},
	})
	if result.Succeeded != 3 || result.Failed != 0 {
		t.Fatalf("批量应用结果: %+v", result)
	}
	assertConversionCount(t, "people", "1=1", nil, 1)
	assertConversionCount(t, "video_people", "1=1", nil, 3)
}

// META03：即使每个请求各自带了 create_new，同一批次内也只创建一次。
func TestLocalMetadataMETA03PerRequestCreateNewIsAlsoDeduplicatedInBatch(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	service := NewLocalMetadataService(t.TempDir(), NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	var ids []uint
	for _, name := range []string{"one", "two"} {
		ids = append(ids, p017NFO(t, root, name, `<movie><actor><name>Solo</name></actor></movie>`).ID)
	}
	preview := service.PreviewBatch(ids)
	requests := make([]LocalMetadataApplyRequest, 0, 2)
	for _, diff := range preview.Diffs {
		requests = append(requests, LocalMetadataApplyRequest{
			VideoID: diff.VideoID, ManifestSHA256: diff.ManifestSHA256, CurrentSHA256: diff.CurrentSHA256,
			SelectedFields:    []string{"people"},
			PeopleResolutions: []LocalMetadataResolution{{NormalizedName: "solo", Mode: "create_new"}},
		})
	}
	result := service.ApplyBatch(LocalMetadataBatchApplyRequest{Requests: requests})
	if result.Succeeded != 2 {
		t.Fatalf("批量应用结果: %+v", result)
	}
	assertConversionCount(t, "people", "1=1", nil, 1)
	assertConversionCount(t, "video_people", "1=1", nil, 2)
}

// META09：diff 带视频名，人物差异按名字给出 added/removed/kept。
func TestLocalMetadataMETA09DiffCarriesVideoNameAndPeopleNameDelta(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	service := NewLocalMetadataService(t.TempDir(), NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	video := p017NFO(t, root, "movie", `<movie><actor><name>Alice</name></actor><actor><name>Bob</name></actor></movie>`)
	alice, bob, carol := models.Person{DisplayName: "Alice"}, models.Person{DisplayName: "Bob"}, models.Person{DisplayName: "Carol"}
	for _, person := range []*models.Person{&alice, &carol} {
		if err := database.DB.Create(person).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.DB.Create(&models.VideoPerson{VideoID: video.ID, PersonID: person.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	_ = bob
	diff, err := service.GetDiff(video.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diff.VideoName != "movie.mp4" || diff.VideoPath != video.Path {
		t.Fatalf("diff 应带视频名与路径: %+v", diff)
	}
	eq := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	if !eq(diff.People.Added, []string{"Bob"}) || !eq(diff.People.Removed, []string{"Carol"}) || !eq(diff.People.Kept, []string{"Alice"}) {
		t.Fatalf("人物名字差异不符: added=%v removed=%v kept=%v", diff.People.Added, diff.People.Removed, diff.People.Kept)
	}
	missing := p017NFO(t, root, "nonfo", "")
	missingDiff, err := service.GetDiff(missing.ID)
	if err != nil || missingDiff.VideoName != "nonfo.mp4" {
		t.Fatalf("无来源的 diff 也应带视频名: %+v err=%v", missingDiff, err)
	}
}

// META09：应用自己写出 NFO 后回写已应用 manifest，下次不被判成「有更新」。
func TestLocalMetadataMETA09ExportSyncsAppliedManifest(t *testing.T) {
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	service := NewLocalMetadataService(t.TempDir(), NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	video := p017NFO(t, root, "movie", "")
	if err := database.DB.Model(&video).Update("display_title", "本地标题").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.ExportVideoNFO(nil, video.ID); err != nil { //nolint:staticcheck // nil 上下文由被测方法兜底
		t.Fatalf("写出 NFO 失败: %v", err)
	}
	var state models.VideoLocalMetadataState
	if err := database.DB.First(&state, "video_id = ?", video.ID).Error; err != nil {
		t.Fatalf("写出后应有状态行: %v", err)
	}
	if state.Status != LocalMetadataStateCurrent || state.AppliedManifestSHA256 == "" || state.AppliedManifestSHA256 != state.ObservedManifestSHA256 || state.AppliedAt == nil {
		t.Fatalf("写出后应记为已应用: %+v", state)
	}
	diff, err := service.GetDiff(video.ID)
	if err != nil || diff.Status != LocalMetadataStateCurrent {
		t.Fatalf("重新读取 diff 应为 current: %+v err=%v", diff, err)
	}
	if err := service.ObserveVideo(video.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.First(&state, "video_id = ?", video.ID).Error; err != nil || state.Status != LocalMetadataStateCurrent {
		t.Fatalf("扫描观察后仍应为 current: %+v err=%v", state, err)
	}
}

// META05：删除人物清空两张关系表，命名簇回到未命名；影响统计按关系行计数。
func TestPersonServiceMETA05DeletePersonReturnsFaceClustersToUnnamed(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewPersonService(t.TempDir())
	person, err := svc.CreatePerson("王五", "")
	if err != nil {
		t.Fatal(err)
	}
	video := p017Video(t, "a.mp4")
	image := models.Image{Name: "a.jpg", Path: "/p017/a.jpg"}
	if err := database.DB.Create(&image).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.AddPersonVideo(person.ID, video.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddPersonImages(person.ID, []uint{image.ID}); err != nil {
		t.Fatal(err)
	}
	cluster := models.FaceCluster{Status: models.FaceClusterStatusNamed, PersonID: &person.ID}
	if err := database.DB.Create(&cluster).Error; err != nil {
		t.Fatal(err)
	}
	impact, err := svc.GetPersonDeletionImpact(person.ID)
	if err != nil || impact.VideoCount != 1 || impact.ImageCount != 1 || impact.FaceClusterCount != 1 {
		t.Fatalf("影响统计不符: %+v err=%v", impact, err)
	}
	if err := svc.DeletePerson(person.ID); err != nil {
		t.Fatal(err)
	}
	assertConversionCount(t, "people", "id = ?", []any{person.ID}, 0)
	assertConversionCount(t, "video_people", "person_id = ?", []any{person.ID}, 0)
	assertConversionCount(t, "image_people", "person_id = ?", []any{person.ID}, 0)
	if err := database.DB.First(&cluster, cluster.ID).Error; err != nil {
		t.Fatal(err)
	}
	if cluster.Status != models.FaceClusterStatusUnnamed || cluster.PersonID != nil {
		t.Fatalf("人脸簇应回到未命名: %+v", cluster)
	}
	if err := svc.DeletePerson(person.ID); !errors.Is(err, ErrPersonNotFound) {
		t.Fatalf("重复删除应返回 person_not_found: %v", err)
	}
	if _, err := svc.GetPersonDeletionImpact(person.ID); !errors.Is(err, ErrPersonNotFound) {
		t.Fatalf("影响统计应返回 person_not_found: %v", err)
	}
	// 零关系人物也能删掉（此前永远删不掉）。
	lonely, _ := svc.CreatePerson("零关系", "")
	if err := svc.DeletePerson(lonely.ID); err != nil {
		t.Fatalf("零关系人物应可删除: %v", err)
	}
}

// META05：合并人物——关系去重改写、簇改指向目标、来源被删、目标无头像时复制来源头像。
func TestPersonServiceMETA05MergePeople(t *testing.T) {
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	svc := NewPersonService(dataDir)
	target, _ := svc.CreatePerson("目标", "")
	source, _ := svc.CreatePerson("来源", "")
	source2, _ := svc.CreatePerson("来源二", "")
	v1, v2, v3 := p017Video(t, "1.mp4"), p017Video(t, "2.mp4"), p017Video(t, "3.mp4")
	for personID, videoIDs := range map[uint][]uint{target.ID: {v1.ID, v2.ID}, source.ID: {v2.ID, v3.ID}, source2.ID: {v3.ID}} {
		if err := svc.AddPersonVideos(personID, videoIDs); err != nil {
			t.Fatal(err)
		}
	}
	image := models.Image{Name: "a.jpg", Path: "/p017/a.jpg"}
	if err := database.DB.Create(&image).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.AddPersonImages(source.ID, []uint{image.ID}); err != nil {
		t.Fatal(err)
	}
	cluster := models.FaceCluster{Status: models.FaceClusterStatusNamed, PersonID: &source.ID}
	if err := database.DB.Create(&cluster).Error; err != nil {
		t.Fatal(err)
	}
	avatarSource := filepath.Join(t.TempDir(), "avatar.png")
	writeTestPNG(t, avatarSource)
	withAvatar, err := svc.SetPersonAvatar(source.ID, avatarSource)
	if err != nil || withAvatar.AvatarPath == "" {
		t.Fatalf("设置来源头像失败: %+v err=%v", withAvatar, err)
	}
	oldAvatarFile := filepath.Join(dataDir, "media-details", filepath.FromSlash(withAvatar.AvatarPath))

	if _, err := svc.MergePeople(target.ID, []uint{target.ID}); !errors.Is(err, ErrInvalidMerge) {
		t.Fatalf("目标出现在来源中应返回 invalid_merge: %v", err)
	}
	if _, err := svc.MergePeople(target.ID, []uint{9999}); !errors.Is(err, ErrPersonNotFound) {
		t.Fatalf("来源不存在应返回 person_not_found: %v", err)
	}
	if _, err := svc.MergePeople(9999, []uint{source.ID}); !errors.Is(err, ErrPersonNotFound) {
		t.Fatalf("目标不存在应返回 person_not_found: %v", err)
	}
	assertConversionCount(t, "people", "1=1", nil, 3) // 失败的合并不留痕迹。

	result, err := svc.MergePeople(target.ID, []uint{source.ID, source2.ID})
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	if result.MergedCount != 2 || result.Target.Person.ID != target.ID || result.Target.ActiveVideoCount != 3 || result.Target.ActiveImageCount != 1 {
		t.Fatalf("合并回执不符: %+v", result)
	}
	assertConversionCount(t, "people", "1=1", nil, 1)
	assertConversionCount(t, "video_people", "person_id = ?", []any{target.ID}, 3)
	assertConversionCount(t, "video_people", "person_id <> ?", []any{target.ID}, 0)
	assertConversionCount(t, "image_people", "person_id = ?", []any{target.ID}, 1)
	if err := database.DB.First(&cluster, cluster.ID).Error; err != nil || cluster.PersonID == nil || *cluster.PersonID != target.ID || cluster.Status != models.FaceClusterStatusNamed {
		t.Fatalf("簇应改指向目标并保持命名: %+v err=%v", cluster, err)
	}
	var merged models.Person
	if err := database.DB.First(&merged, target.ID).Error; err != nil || merged.AvatarPath == "" {
		t.Fatalf("目标应获得来源头像: %+v err=%v", merged, err)
	}
	if _, err := os.Stat(oldAvatarFile); !os.IsNotExist(err) {
		t.Fatalf("来源头像文件应在提交后删除: err=%v", err)
	}
	if _, err := svc.images.Resolve(merged.AvatarPath); err != nil {
		t.Fatalf("目标头像文件应存在: %v", err)
	}
}

// META07：合并标签同事务改写活跃保存视图里的 tag_ids_json（来源 → 目标并去重）。
func TestTagServiceMETA07MergeRewritesSavedViews(t *testing.T) {
	setupVideoServiceTestDB(t)
	source, target, other := models.Tag{Name: "来源"}, models.Tag{Name: "目标"}, models.Tag{Name: "其它"}
	for _, tag := range []*models.Tag{&source, &target, &other} {
		if err := database.DB.Create(tag).Error; err != nil {
			t.Fatal(err)
		}
	}
	views := []models.SavedLibraryView{
		{Name: "含来源", TagIDsJSON: "[" + itoa(source.ID) + "," + itoa(other.ID) + "]"},
		{Name: "来源目标都有", TagIDsJSON: "[" + itoa(source.ID) + "," + itoa(target.ID) + "]"},
		{Name: "无关", TagIDsJSON: "[" + itoa(other.ID) + "]"},
		{Name: "空", TagIDsJSON: "[]"},
	}
	if err := database.DB.Create(&views).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (&TagService{}).MergeTags([]uint{source.ID}, target.ID); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"含来源":    "[" + itoa(target.ID) + "," + itoa(other.ID) + "]",
		"来源目标都有": "[" + itoa(target.ID) + "]",
		"无关":     "[" + itoa(other.ID) + "]",
		"空":      "[]",
	}
	for name, expected := range want {
		var view models.SavedLibraryView
		if err := database.DB.Where("name = ?", name).First(&view).Error; err != nil {
			t.Fatal(err)
		}
		if view.TagIDsJSON != expected {
			t.Fatalf("视图 %q 的 tag_ids_json=%s，期望 %s", name, view.TagIDsJSON, expected)
		}
	}
}

// META10：低清 = w>0 && h>0 && max(w,h)<1920 && min(w,h)<1080；1920×800 宽银幕 1080p 不算。
func TestTagServiceMETA10LowResolutionUsesBothDimensions(t *testing.T) {
	cases := []struct {
		w, h int
		low  bool
	}{
		{1920, 800, false}, {1920, 1080, false}, {1280, 720, true}, {1919, 1079, true},
		{1080, 1920, false}, {720, 1280, true}, {0, 720, false}, {1280, 0, false}, {2560, 800, false},
	}
	setupVideoServiceTestDB(t)
	videos := make([]models.Video, 0, len(cases))
	for index, c := range cases {
		if got := isLowResolutionVideo(c.w, c.h); got != c.low {
			t.Fatalf("isLowResolutionVideo(%d,%d)=%v，期望 %v", c.w, c.h, got, c.low)
		}
		videos = append(videos, models.Video{Name: itoa(uint(index)) + ".mp4", Path: "/p017/res/" + itoa(uint(index)) + ".mp4", Duration: 600, Width: c.w, Height: c.h})
	}
	if err := database.DB.Create(&videos).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (&TagService{}).SyncShortVideoTags(); err != nil {
		t.Fatal(err)
	}
	var low models.Tag
	if err := database.DB.Where("automatic_kind = ?", lowResolutionAutomaticTagKind).First(&low).Error; err != nil {
		t.Fatal(err)
	}
	for index, c := range cases {
		var count int64
		database.DB.Table("video_tags").Where("video_id = ? AND tag_id = ?", videos[index].ID, low.ID).Count(&count)
		if (count == 1) != c.low {
			t.Fatalf("批量对账 %dx%d 低清标签=%v，期望 %v", c.w, c.h, count == 1, c.low)
		}
		if err := database.Transaction(func(tx *gorm.DB) error { return syncShortVideoTagForVideo(tx, videos[index].ID) }); err != nil {
			t.Fatal(err)
		}
		database.DB.Table("video_tags").Where("video_id = ? AND tag_id = ?", videos[index].ID, low.ID).Count(&count)
		if (count == 1) != c.low {
			t.Fatalf("单视频对账 %dx%d 低清标签=%v，期望 %v", c.w, c.h, count == 1, c.low)
		}
	}
}

// META13：人工覆盖可见，清除后立即重新跟随自动规则。
func TestTagServiceMETA13ClearOverrideReturnsToAutomaticRule(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := models.Video{Name: "low.mp4", Path: "/p017/low.mp4", Duration: 600, Width: 1280, Height: 720}
	other := models.Video{Name: "hd.mp4", Path: "/p017/hd.mp4", Duration: 600, Width: 1920, Height: 1080}
	if err := database.DB.Create(&[]*models.Video{&video, &other}).Error; err != nil {
		t.Fatal(err)
	}
	svc := &TagService{}
	if _, err := svc.SyncShortVideoTags(); err != nil {
		t.Fatal(err)
	}
	var low models.Tag
	if err := database.DB.Where("automatic_kind = ?", lowResolutionAutomaticTagKind).First(&low).Error; err != nil {
		t.Fatal(err)
	}
	videoService := &VideoService{}
	if err := videoService.RemoveTagFromVideo(video.ID, low.ID); err != nil { // 人工移除
		t.Fatal(err)
	}
	if err := videoService.AddTagToVideo(other.ID, low.ID); err != nil { // 人工添加
		t.Fatal(err)
	}
	overrides, err := svc.GetVideoAutomaticTagOverrides(video.ID)
	if err != nil || len(overrides) != 1 || overrides[0].AutomaticKind != lowResolutionAutomaticTagKind || overrides[0].Present {
		t.Fatalf("应看到一条「去掉」覆盖: %+v err=%v", overrides, err)
	}
	linked := func(videoID uint) bool {
		var count int64
		database.DB.Table("video_tags").Where("video_id = ? AND tag_id = ?", videoID, low.ID).Count(&count)
		return count == 1
	}
	if linked(video.ID) {
		t.Fatal("覆盖期间不应带低清标签")
	}
	if err := svc.ClearVideoAutomaticTagOverride(video.ID, lowResolutionAutomaticTagKind); err != nil {
		t.Fatal(err)
	}
	if !linked(video.ID) {
		t.Fatal("清除覆盖后应立即重新打上低清标签")
	}
	if overrides, _ = svc.GetVideoAutomaticTagOverrides(video.ID); len(overrides) != 0 {
		t.Fatalf("覆盖行应被删除: %+v", overrides)
	}
	// 另一视频的「加上」覆盖清除后，按规则（1080p 不低清）去掉标签。
	if err := svc.ClearVideoAutomaticTagOverride(other.ID, lowResolutionAutomaticTagKind); err != nil {
		t.Fatal(err)
	}
	if linked(other.ID) {
		t.Fatal("清除「加上」覆盖后应回到规则：不带低清标签")
	}
	if err := svc.ClearVideoAutomaticTagOverride(video.ID, "bogus"); err == nil {
		t.Fatal("未知自动标签种类应报错")
	}
	if err := svc.ClearVideoAutomaticTagOverride(99999, lowResolutionAutomaticTagKind); err == nil {
		t.Fatal("视频不存在应报错")
	}
}

// META14：删除与合并确认框用的使用计数：只数仍可见的视频与图片，未使用的标签为 0。
func TestTagServiceMETA14UsageCounts(t *testing.T) {
	setupVideoServiceTestDB(t)
	used, unused := models.Tag{Name: "常用"}, models.Tag{Name: "冷门"}
	for _, tag := range []*models.Tag{&used, &unused} {
		if err := database.DB.Create(tag).Error; err != nil {
			t.Fatal(err)
		}
	}
	v1, v2 := p017Video(t, "1.mp4"), p017Video(t, "2.mp4")
	images := []models.Image{{Name: "a.jpg", Path: "/p017/a.jpg"}, {Name: "b.jpg", Path: "/p017/b.jpg"}}
	if err := database.DB.Create(&images).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{v1.ID, v2.ID} {
		if err := database.DB.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?)", id, used.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, image := range images {
		if err := database.DB.Exec("INSERT INTO image_tags(image_id, tag_id) VALUES (?, ?)", image.ID, used.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Delete(&v2).Error; err != nil {
		t.Fatal(err)
	}
	svc := &TagService{}
	counts, err := svc.GetTagUsageCounts([]uint{used.ID, unused.ID})
	if err != nil {
		t.Fatal(err)
	}
	if counts[used.ID] != (TagUsageCount{Videos: 1, Images: 2}) || counts[unused.ID] != (TagUsageCount{}) {
		t.Fatalf("计数不符: %+v", counts)
	}
	if _, present := counts[unused.ID]; !present {
		t.Fatal("未使用的标签也应出现在结果里")
	}
	if empty, err := svc.GetTagUsageCounts(nil); err != nil || len(empty) != 0 {
		t.Fatalf("空入参应返回空结果: %+v err=%v", empty, err)
	}
}

// PLAY14：星级评分轻量即时保存，0.5 步进，null 清空，返回带标签的视频。
func TestVideoDetailServicePLAY14UpdateVideoRating(t *testing.T) {
	setupVideoServiceTestDB(t)
	video := p017Video(t, "a.mp4")
	tag := models.Tag{Name: "科幻"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&video).Association("Tags").Append(&tag); err != nil {
		t.Fatal(err)
	}
	svc := NewVideoDetailService(NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	rating := 7.5
	saved, err := svc.UpdateVideoRating(video.ID, &rating)
	if err != nil || saved.PersonalRating == nil || *saved.PersonalRating != 7.5 || len(saved.Tags) != 1 {
		t.Fatalf("保存评分失败: %+v err=%v", saved, err)
	}
	invalid := 7.3
	if _, err := svc.UpdateVideoRating(video.ID, &invalid); err == nil {
		t.Fatal("非 0.5 步进应被拒绝")
	}
	tooHigh := 10.5
	if _, err := svc.UpdateVideoRating(video.ID, &tooHigh); err == nil {
		t.Fatal("超出范围应被拒绝")
	}
	var stored models.Video
	if err := database.DB.First(&stored, video.ID).Error; err != nil || stored.PersonalRating == nil || *stored.PersonalRating != 7.5 {
		t.Fatalf("被拒绝的输入不应改动已存评分: %+v err=%v", stored, err)
	}
	cleared, err := svc.UpdateVideoRating(video.ID, nil)
	if err != nil || cleared.PersonalRating != nil {
		t.Fatalf("null 应清空评分: %+v err=%v", cleared, err)
	}
	if _, err := svc.UpdateVideoRating(99999, &rating); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("视频不存在应返回 ErrRecordNotFound: %v", err)
	}
}
