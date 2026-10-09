package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

// P-016 清理中心（后端）的回归用例：保留建议与元数据合并（IMG-03）、忽略的指纹失效 / 移出成员 /
// 列表与撤销（IMG-07）、覆盖率（IMG-11）、分析取消与图片登记（IMG-12）、极短 / 极低忽略（APP-11）、
// 阈值读设置（META-10 清理侧）。

// p016Video 建一条"已探测过"的视频：四项元数据齐全且 size 与磁盘一致，分析时不再调 ffprobe。
func p016Video(t *testing.T, root, name, content string, width, height int) models.Video {
	t.Helper()
	path := filepath.Join(root, name)
	mustWriteSizedFile(t, path, []byte(content))
	video := models.Video{
		Name: name, Path: path, Directory: root, Size: int64(len(content)),
		Duration: 600, Resolution: fmt.Sprintf("%dx%d", width, height), Width: width, Height: height,
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	return video
}

func p016Tag(t *testing.T, name, automaticKind string) models.Tag {
	t.Helper()
	tag := models.Tag{Name: name, Color: "#123456", AutomaticKind: automaticKind}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	return tag
}

func p016AttachVideoTag(t *testing.T, videoID, tagID uint) {
	t.Helper()
	if err := database.DB.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?)", videoID, tagID).Error; err != nil {
		t.Fatalf("关联标签失败: %v", err)
	}
}

func p016Person(t *testing.T, name string) models.Person {
	t.Helper()
	person := models.Person{DisplayName: name}
	if err := database.DB.Create(&person).Error; err != nil {
		t.Fatalf("创建人物失败: %v", err)
	}
	return person
}

func p016AttachVideoPerson(t *testing.T, videoID, personID uint) {
	t.Helper()
	if err := database.DB.Create(&models.VideoPerson{VideoID: videoID, PersonID: personID}).Error; err != nil {
		t.Fatalf("关联人物失败: %v", err)
	}
}

func p016Collection(t *testing.T, name string) models.MediaCollection {
	t.Helper()
	collection := models.MediaCollection{Name: name, NormalizedName: strings.ToLower(name)}
	if err := database.DB.Create(&collection).Error; err != nil {
		t.Fatalf("创建作品集失败: %v", err)
	}
	return collection
}

func p016AddToCollection(t *testing.T, collectionID, videoID uint, position int) {
	t.Helper()
	if err := database.DB.Create(&models.CollectionVideo{CollectionID: collectionID, VideoID: videoID, Position: position}).Error; err != nil {
		t.Fatalf("加入作品集失败: %v", err)
	}
}

func p016UpdateVideo(t *testing.T, videoID uint, updates map[string]interface{}) {
	t.Helper()
	if err := database.DB.Model(&models.Video{}).Where("id = ?", videoID).Updates(updates).Error; err != nil {
		t.Fatalf("更新视频失败: %v", err)
	}
}

func p016ReloadVideo(t *testing.T, videoID uint) models.Video {
	t.Helper()
	var video models.Video
	if err := database.DB.Preload("Tags").First(&video, videoID).Error; err != nil {
		t.Fatalf("读回视频失败: %v", err)
	}
	return video
}

// p016WatchedSetter 记录合并提交后对已看 setter 的调用，再转给真正的 VideoService。
type p016WatchedSetter struct {
	calls []uint
	err   error
}

func (s *p016WatchedSetter) SetVideoWatched(videoID uint, watched bool) (*models.Video, error) {
	s.calls = append(s.calls, videoID)
	if s.err != nil {
		return nil, s.err
	}
	return (&VideoService{}).SetVideoWatched(videoID, watched)
}

func p016GroupIDs(group CleanupDuplicateGroup) []uint {
	return append([]uint{group.Original.ID}, videoIDs(group.Candidates)...)
}

// ===== IMG-03：保留建议看整理成果 =====

// 精确重复里分辨率更高的那一份什么都没整理过，另一份被收藏、打了手工标签：
// 建议保留的必须是整理过的那一份。早先的规则等于「像素一样就留 ID 小的」，而且
// 精确重复的查询没预载标签，卡片上连标签都看不到。
func TestCleanupIMG03KeeperSuggestionPrefersCuratedVideo(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	plain := p016Video(t, root, "plain-4k.mp4", "same-payload-bytes", 3840, 2160)
	curated := p016Video(t, root, "curated-720.mp4", "same-payload-bytes", 1280, 720)
	p016UpdateVideo(t, curated.ID, map[string]interface{}{"is_favorite": true, "favorited_at": time.Now()})
	manual := p016Tag(t, "珍藏", "")
	p016AttachVideoTag(t, curated.ID, manual.ID)

	// 同源：分辨率高的一方没有整理，低的一方有评分 → 建议保留有评分的一方，可释放空间随之变。
	sameHigh := p016Video(t, root, "same-high.mp4", "same-high-content", 1920, 1080)
	sameRated := p016Video(t, root, "same-rated.mp4", "same-rated-content-longer", 1280, 720)
	p016UpdateVideo(t, sameRated.ID, map[string]interface{}{"personal_rating": 8.5})
	if err := database.DB.Create(&models.VideoSameSourceRelation{
		VideoAID: sameHigh.ID, VideoBID: sameRated.ID, VideoAFingerprint: "fa", VideoBFingerprint: "fb",
		Status: models.VideoSameSourceStatusDetected, Confidence: "high", DetectionVersion: "test",
	}).Error; err != nil {
		t.Fatal(err)
	}

	// 近似重复：像素大的一方没有整理，另一方在作品集里。
	nearBig := p016Video(t, root, "near-big.mp4", "near-big-content", 1920, 1080)
	nearCollected := p016Video(t, root, "near-collected.mp4", "near-collected-content-x", 1280, 720)
	seedPerceptualHashRow(t, nearBig, "0000000000000000")
	seedPerceptualHashRow(t, nearCollected, "0000000000000001")
	collection := p016Collection(t, "系列")
	p016AddToCollection(t, collection.ID, nearCollected.ID, 1)

	result, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(result.DuplicateGroups) != 1 || result.DuplicateGroups[0].Original.ID != curated.ID {
		t.Fatalf("精确重复应建议保留整理过的 %d: %+v", curated.ID, result.DuplicateGroups)
	}
	original := result.DuplicateGroups[0].Original
	if len(original.Tags) != 1 || original.Tags[0].ID != manual.ID {
		t.Fatalf("精确重复成员应带出标签（修掉漏预载）: %+v", original.Tags)
	}
	if got := result.DuplicateGroups[0].Candidates[0].Tags; got == nil || len(got) != 0 {
		t.Fatalf("没有标签的成员应是空数组而不是 null: %#v", got)
	}
	if c := result.Curation[curated.ID]; !c.Favorite || !c.Tags || c.Score() != 2 {
		t.Fatalf("整理项不对: %+v", c)
	}
	if c := result.Curation[plain.ID]; c.Score() != 0 {
		t.Fatalf("未整理的一份整理分应为 0: %+v", c)
	}

	if len(result.SameSourceGroups) != 1 {
		t.Fatalf("应有一组同源: %+v", result.SameSourceGroups)
	}
	same := result.SameSourceGroups[0]
	if same.Preferred.ID != sameRated.ID || same.Alternative.ID != sameHigh.ID || same.EstimatedSavings != sameHigh.Size {
		t.Fatalf("同源应建议保留有评分的一方，可释放空间取另一方: %+v", same)
	}

	if len(result.NearDuplicateGroups) != 1 || result.NearDuplicateGroups[0].Original.ID != nearCollected.ID {
		t.Fatalf("近似重复应建议保留在作品集里的一份: %+v", result.NearDuplicateGroups)
	}
	if !result.Curation[nearCollected.ID].Collections {
		t.Fatalf("作品集整理项缺失: %+v", result.Curation[nearCollected.ID])
	}
}

// 元组比较：整理分 > 像素 > 体积 > ID。
func TestCleanupIMG03PreferenceTupleOrder(t *testing.T) {
	a := models.Video{ID: 1, Width: 1920, Height: 1080, Size: 100}
	b := models.Video{ID: 2, Width: 1280, Height: 720, Size: 900}
	if !isPreferredCleanupVideo(a, b, nil) {
		t.Fatal("整理分相同时像素大者优先")
	}
	curation := map[uint]CleanupCuration{b.ID: {Progress: true}}
	if !isPreferredCleanupVideo(b, a, curation) {
		t.Fatal("整理分高者优先于像素")
	}
	c := models.Video{ID: 3, Width: 1920, Height: 1080, Size: 200}
	if !isPreferredCleanupVideo(c, a, nil) {
		t.Fatal("像素相同时体积大者优先")
	}
	d := models.Video{ID: 4, Width: 1920, Height: 1080, Size: 100}
	if !isPreferredCleanupVideo(a, d, nil) || isPreferredCleanupVideo(d, a, nil) {
		t.Fatal("其余都相同时 ID 小者优先")
	}
	// 标签数不再单独比较：它已并入整理分的「有非自动标签」一项。
	withTags := models.Video{ID: 5, Width: 1920, Height: 1080, Size: 100, Tags: []models.Tag{{ID: 1}, {ID: 2}}}
	if !isPreferredCleanupVideo(a, withTags, nil) {
		t.Fatal("旧规则的标签数比较应已退役（整理分为准）")
	}
}

// 8 项整理信息逐项取齐；自动标签、已删除作品集不算，已看或有断点算观看进度。
func TestCleanupIMG03CurationCountsAllEightSignals(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	full := p016Video(t, root, "full.mp4", "full-content", 1920, 1080)
	bare := p016Video(t, root, "bare.mp4", "bare-content", 1920, 1080)
	watched := p016Video(t, root, "watched.mp4", "watched-content", 1920, 1080)

	p016UpdateVideo(t, full.ID, map[string]interface{}{
		"is_favorite": true, "favorited_at": time.Now(), "is_liked": true, "personal_rating": 6.0, "watch_position_seconds": 30.0,
	})
	p016AttachVideoPerson(t, full.ID, p016Person(t, "演员甲").ID)
	p016AttachVideoTag(t, full.ID, p016Tag(t, "手工", "").ID)
	mustWriteSizedFile(t, filepath.Join(root, "full.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\n你好\n"))
	collection := p016Collection(t, "在用")
	p016AddToCollection(t, collection.ID, full.ID, 1)

	// bare 只有自动标签、只在已删除的作品集里，全都不算。
	p016AttachVideoTag(t, bare.ID, p016Tag(t, "短视频", shortVideoAutomaticTagKind).ID)
	deleted := p016Collection(t, "已删")
	p016AddToCollection(t, deleted.ID, bare.ID, 1)
	if err := database.DB.Delete(&deleted).Error; err != nil {
		t.Fatal(err)
	}
	p016UpdateVideo(t, watched.ID, map[string]interface{}{"is_watched": true})

	curation, err := loadCleanupVideoCuration(context.Background(), []uint{full.ID, bare.ID, watched.ID})
	if err != nil {
		t.Fatal(err)
	}
	want := CleanupCuration{Favorite: true, Liked: true, Rating: true, People: true, Tags: true, Subtitle: true, Collections: true, Progress: true}
	if curation[full.ID] != want || curation[full.ID].Score() != 8 {
		t.Fatalf("8 项都应命中: %+v", curation[full.ID])
	}
	if curation[bare.ID] != (CleanupCuration{}) {
		t.Fatalf("自动标签与已删除作品集不算整理成果: %+v", curation[bare.ID])
	}
	if got := curation[watched.ID]; !got.Progress || got.Score() != 1 {
		t.Fatalf("已看应计入观看进度: %+v", got)
	}
}

// 图片：收藏 / 评分 / 人物 / 标签计分；近似重复换了保留项后，"有多像"按新的保留项重算。
func TestImageCleanupIMG03KeeperPrefersCuratedImage(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	same := bytes.Repeat([]byte("x"), 2048)
	imageCleanupCreateImage(t, filepath.Join(dir, "big.jpg"), same, "", 4000, 3000)
	favorite := imageCleanupCreateImage(t, filepath.Join(dir, "fav.jpg"), same, "", 100, 100)
	if err := database.DB.Model(&models.Image{}).Where("id = ?", favorite.ID).Updates(map[string]interface{}{"is_favorite": true, "favorited_at": time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	// 链：c—d 距离 3，d—e 距离 6，c—e 距离 3；c 像素最大，d 有人物。
	imageCleanupCreateImage(t, filepath.Join(dir, "c.jpg"), bytes.Repeat([]byte("c"), 300), "abcd000000000000", 400, 400)
	withPerson := imageCleanupCreateImage(t, filepath.Join(dir, "d.jpg"), bytes.Repeat([]byte("d"), 301), "abcd000000000007", 200, 200)
	imageCleanupCreateImage(t, filepath.Join(dir, "e.jpg"), bytes.Repeat([]byte("e"), 302), "abcd000000000038", 100, 100)
	person := p016Person(t, "合影")
	if err := database.DB.Create(&models.ImagePerson{ImageID: withPerson.ID, PersonID: person.ID}).Error; err != nil {
		t.Fatal(err)
	}

	analysis, err := newImageCleanupTestService().AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(analysis.DuplicateGroups) != 1 || analysis.DuplicateGroups[0].Original.ID != favorite.ID {
		t.Fatalf("精确重复应建议保留收藏的一张: %+v", analysis.DuplicateGroups)
	}
	if !analysis.DuplicateGroups[0].Original.Curation.Favorite {
		t.Fatalf("成员应带出整理项: %+v", analysis.DuplicateGroups[0].Original.Curation)
	}
	if len(analysis.NearDuplicateGroups) != 1 {
		t.Fatalf("应有一组近似重复: %+v", analysis.NearDuplicateGroups)
	}
	near := analysis.NearDuplicateGroups[0]
	if near.Original.ID != withPerson.ID || !near.Original.Curation.People {
		t.Fatalf("近似重复应建议保留有人物的一张: %+v", near.Original)
	}
	if near.MaxHammingDistance != 6 {
		t.Fatalf("距离应按新保留项重算（期望 6），实际 %d", near.MaxHammingDistance)
	}
}

// ===== IMG-03：删除前合并元数据 =====

func TestMergeMediaMetadataIMG03MergesVideoCuration(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	keeper := p016Video(t, root, "keeper.mp4", "keeper-content", 1920, 1080)
	first := p016Video(t, root, "first.mp4", "first-content", 1280, 720)
	second := p016Video(t, root, "second.mp4", "second-content", 1280, 720)
	other := p016Video(t, root, "other.mp4", "other-content", 1280, 720)

	early := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	later := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	p016UpdateVideo(t, keeper.ID, map[string]interface{}{"watch_position_seconds": 10.0})
	p016UpdateVideo(t, first.ID, map[string]interface{}{
		"is_favorite": true, "favorited_at": later, "is_liked": true, "personal_rating": 7.5, "watch_position_seconds": 100.0,
	})
	p016UpdateVideo(t, second.ID, map[string]interface{}{"is_favorite": true, "favorited_at": early, "is_watched": true, "watched_at": early})

	manualA, manualB := p016Tag(t, "手工甲", ""), p016Tag(t, "手工乙", "")
	automatic := p016Tag(t, "低清", lowResolutionAutomaticTagKind)
	p016AttachVideoTag(t, first.ID, manualA.ID)
	p016AttachVideoTag(t, first.ID, automatic.ID)
	p016AttachVideoTag(t, second.ID, manualB.ID)
	personA, personB := p016Person(t, "甲"), p016Person(t, "乙")
	p016AttachVideoPerson(t, first.ID, personA.ID)
	p016AttachVideoPerson(t, second.ID, personA.ID)
	p016AttachVideoPerson(t, second.ID, personB.ID)

	// series：other(1) first(3) second(5) → keeper 放在最靠前的来源位置 3。
	series := p016Collection(t, "系列")
	p016AddToCollection(t, series.ID, other.ID, 1)
	p016AddToCollection(t, series.ID, first.ID, 3)
	p016AddToCollection(t, series.ID, second.ID, 5)
	// shared：keeper 本来就在，位置不动。
	shared := p016Collection(t, "共有")
	p016AddToCollection(t, shared.ID, keeper.ID, 1)
	p016AddToCollection(t, shared.ID, first.ID, 2)
	// removed：已删除的作品集不加入。
	removed := p016Collection(t, "已删除")
	p016AddToCollection(t, removed.ID, second.ID, 1)
	if err := database.DB.Delete(&removed).Error; err != nil {
		t.Fatal(err)
	}

	// 字幕：keeper 没有同名 .srt，first 有。
	subtitle := []byte("1\n00:00:01,000 --> 00:00:02,000\n合并过来的字幕\n")
	mustWriteSizedFile(t, filepath.Join(root, "first.srt"), subtitle)

	// keeper 上一条待审的 AI 候选恰好就是 manualA：合并等同于手动加上它，候选作废。
	candidate := models.AITagCandidate{VideoID: keeper.ID, SuggestedName: manualA.Name, NormalizedName: manualA.Name, MatchedTagID: &manualA.ID, Confidence: "high", Status: models.AITagCandidateStatusPending}
	if err := database.DB.Create(&candidate).Error; err != nil {
		t.Fatal(err)
	}

	setter := &p016WatchedSetter{}
	result, err := MergeMediaMetadata(MediaMergeKindVideo, keeper.ID, []uint{first.ID, second.ID, first.ID}, MediaMetadataMergeDeps{
		Watched: setter, Subtitles: NewSubtitleFileWriter(t.TempDir()),
	})
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("不应有警告: %v", result.Warnings)
	}
	if result.TagsAdded != 2 || result.PeopleAdded != 2 || result.CollectionsAdded != 1 {
		t.Fatalf("并集计数不对: %+v", result)
	}
	if !result.FavoriteChanged || !result.LikedChanged || !result.RatingChanged || !result.ProgressChanged || !result.WatchedChanged || !result.SubtitleMoved {
		t.Fatalf("变更标记不对: %+v", result)
	}

	merged := p016ReloadVideo(t, keeper.ID)
	tagIDs := make([]uint, 0, len(merged.Tags))
	for _, tag := range merged.Tags {
		tagIDs = append(tagIDs, tag.ID)
	}
	if !sameUintSet(tagIDs, []uint{manualA.ID, manualB.ID}) {
		t.Fatalf("只合并非自动标签: %v", tagIDs)
	}
	var people []uint
	if err := database.DB.Model(&models.VideoPerson{}).Where("video_id = ?", keeper.ID).Pluck("person_id", &people).Error; err != nil {
		t.Fatal(err)
	}
	if !sameUintSet(people, []uint{personA.ID, personB.ID}) {
		t.Fatalf("人物应取并集: %v", people)
	}
	if !merged.IsFavorite || merged.FavoritedAt == nil || !merged.FavoritedAt.Equal(early) {
		t.Fatalf("收藏取或、收藏时间取最早: favorite=%v at=%v", merged.IsFavorite, merged.FavoritedAt)
	}
	if !merged.IsLiked || merged.PersonalRating == nil || *merged.PersonalRating != 7.5 {
		t.Fatalf("点赞取或、评分取最大: liked=%v rating=%v", merged.IsLiked, merged.PersonalRating)
	}
	if merged.WatchPositionSeconds != 100 {
		t.Fatalf("keeper 未看时断点取最大: %v", merged.WatchPositionSeconds)
	}
	if !merged.IsWatched || merged.WatchedAt == nil {
		t.Fatalf("已看取或（经已看 setter）: %+v", merged)
	}
	if len(setter.calls) != 1 || setter.calls[0] != keeper.ID {
		t.Fatalf("已看应经 setter 翻转一次（观察者由它通知）: %v", setter.calls)
	}

	var positions []models.CollectionVideo
	if err := database.DB.Where("video_id = ?", keeper.ID).Order("collection_id").Find(&positions).Error; err != nil {
		t.Fatal(err)
	}
	if len(positions) != 2 || positions[0].CollectionID != series.ID || positions[0].Position != 3 ||
		positions[1].CollectionID != shared.ID || positions[1].Position != 1 {
		t.Fatalf("作品集：加入来源所在的作品集并放在来源的位置，已在的不动、已删除的不加: %+v", positions)
	}

	keeperSRT := filepath.Join(root, "keeper.srt")
	content, err := os.ReadFile(keeperSRT)
	if err != nil || !bytes.Equal(content, subtitle) {
		t.Fatalf("字幕应写成 keeper 的同名 .srt: %q err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(root, "first.srt")); err != nil {
		t.Fatalf("来源字幕应留在原处: %v", err)
	}
	var index models.SubtitleIndexState
	if err := database.DB.Where("video_id = ?", keeper.ID).First(&index).Error; err != nil || index.SegmentCount != 1 {
		t.Fatalf("keeper 的字幕索引应已刷新: %+v err=%v", index, err)
	}
	var superseded models.AITagCandidate
	if err := database.DB.First(&superseded, candidate.ID).Error; err != nil || superseded.Status != models.AITagCandidateStatusSuperseded {
		t.Fatalf("合并加上的手工标签应作废同标签的待审候选: %+v err=%v", superseded, err)
	}

	// 合并可重复执行：第二次什么都不再变。
	again, err := MergeMediaMetadata(MediaMergeKindVideo, keeper.ID, []uint{first.ID, second.ID}, MediaMetadataMergeDeps{Watched: setter})
	if err != nil {
		t.Fatalf("重复合并失败: %v", err)
	}
	if again.TagsAdded+again.PeopleAdded+again.CollectionsAdded != 0 || again.FavoriteChanged || again.RatingChanged || again.WatchedChanged || again.SubtitleMoved {
		t.Fatalf("重复合并应是幂等的: %+v", again)
	}
}

// keeper 已看时断点不动、也不再翻转已看。
func TestMergeMediaMetadataIMG03KeeperWatchedKeepsPosition(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	keeper := p016Video(t, root, "keeper.mp4", "keeper-content", 1920, 1080)
	source := p016Video(t, root, "source.mp4", "source-content", 1280, 720)
	p016UpdateVideo(t, keeper.ID, map[string]interface{}{"is_watched": true, "watched_at": time.Now()})
	p016UpdateVideo(t, source.ID, map[string]interface{}{"is_watched": true, "watch_position_seconds": 200.0})

	setter := &p016WatchedSetter{}
	result, err := MergeMediaMetadata(MediaMergeKindVideo, keeper.ID, []uint{source.ID}, MediaMetadataMergeDeps{Watched: setter})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProgressChanged || result.WatchedChanged || len(setter.calls) != 0 {
		t.Fatalf("keeper 已看时不写断点、不调 setter: %+v calls=%v", result, setter.calls)
	}
	if merged := p016ReloadVideo(t, keeper.ID); merged.WatchPositionSeconds != 0 {
		t.Fatalf("断点不应被写入: %v", merged.WatchPositionSeconds)
	}
}

// 收藏不变量：收藏为真必带时间。来源收藏但没有时间（升级前旧行）→ 记为现在；来源已取消收藏、
// 残留旧时间 → 不带回来。
func TestMergeMediaMetadataIMG03FavoriteKeepsFavoritedAtInvariant(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	keeper := p016Video(t, root, "keeper.mp4", "keeper-content", 1920, 1080)
	legacy := p016Video(t, root, "legacy.mp4", "legacy-content", 1280, 720)
	unfavorited := p016Video(t, root, "unfav.mp4", "unfav-content", 1280, 720)
	p016UpdateVideo(t, legacy.ID, map[string]interface{}{"is_favorite": true})
	p016UpdateVideo(t, unfavorited.ID, map[string]interface{}{"favorited_at": time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)})

	if _, err := MergeMediaMetadata(MediaMergeKindVideo, keeper.ID, []uint{unfavorited.ID}, MediaMetadataMergeDeps{Watched: &p016WatchedSetter{}}); err != nil {
		t.Fatal(err)
	}
	if merged := p016ReloadVideo(t, keeper.ID); merged.IsFavorite || merged.FavoritedAt != nil {
		t.Fatalf("没有收藏的来源不能带回旧时间: %+v", merged)
	}
	before := time.Now().Add(-time.Second)
	if _, err := MergeMediaMetadata(MediaMergeKindVideo, keeper.ID, []uint{legacy.ID}, MediaMetadataMergeDeps{Watched: &p016WatchedSetter{}}); err != nil {
		t.Fatal(err)
	}
	merged := p016ReloadVideo(t, keeper.ID)
	if !merged.IsFavorite || merged.FavoritedAt == nil || merged.FavoritedAt.Before(before) {
		t.Fatalf("收藏为真必须带收藏时间: %+v", merged)
	}
}

// 事务中途失败：整体回滚，不翻转已看、不迁移字幕，调用方据此不进入删除。
func TestMergeMediaMetadataIMG03FailureRollsBackEverything(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	keeper := p016Video(t, root, "keeper.mp4", "keeper-content", 1920, 1080)
	source := p016Video(t, root, "source.mp4", "source-content", 1280, 720)
	p016UpdateVideo(t, source.ID, map[string]interface{}{"is_watched": true, "is_liked": true})
	p016AttachVideoTag(t, source.ID, p016Tag(t, "手工", "").ID)
	p016AttachVideoPerson(t, source.ID, p016Person(t, "甲").ID)
	mustWriteSizedFile(t, filepath.Join(root, "source.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\n字幕\n"))

	// 读作品集时注入失败：标签与人物已经在事务里写过了，必须一并回滚。
	callback := "p016:fail-collection-read"
	if err := database.DB.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "collection_videos" {
			_ = tx.AddError(errors.New("injected failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}

	setter := &p016WatchedSetter{}
	if _, err := MergeMediaMetadata(MediaMergeKindVideo, keeper.ID, []uint{source.ID}, MediaMetadataMergeDeps{Watched: setter, Subtitles: NewSubtitleFileWriter(t.TempDir())}); err == nil {
		t.Fatal("事务失败应返回错误")
	}
	merged := p016ReloadVideo(t, keeper.ID)
	var people int64
	database.DB.Model(&models.VideoPerson{}).Where("video_id = ?", keeper.ID).Count(&people)
	if len(merged.Tags) != 0 || people != 0 || merged.IsLiked || merged.IsWatched {
		t.Fatalf("失败的合并必须整体回滚: tags=%d people=%d liked=%v watched=%v", len(merged.Tags), people, merged.IsLiked, merged.IsWatched)
	}
	if len(setter.calls) != 0 {
		t.Fatalf("失败时不应翻转已看: %v", setter.calls)
	}
	if _, err := os.Stat(filepath.Join(root, "keeper.srt")); !os.IsNotExist(err) {
		t.Fatalf("失败时不应迁移字幕: %v", err)
	}

	// 已看 setter 失败同样返回错误（数据库部分已提交且可重复执行，调用方不进入删除）。
	database.DB.Callback().Query().Remove(callback)
	failing := &p016WatchedSetter{err: errors.New("setter down")}
	if _, err := MergeMediaMetadata(MediaMergeKindVideo, keeper.ID, []uint{source.ID}, MediaMetadataMergeDeps{Watched: failing}); err == nil {
		t.Fatal("已看翻转失败应返回错误")
	}
}

// 字幕写入失败只作为警告，数据库合并照常生效。
func TestMergeMediaMetadataIMG03SubtitleFailureIsWarning(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	keeperDir := filepath.Join(root, "keeper-dir")
	keeper := p016Video(t, keeperDir, "keeper.mp4", "keeper-content", 1920, 1080)
	source := p016Video(t, root, "source.mp4", "source-content", 1280, 720)
	p016AttachVideoTag(t, source.ID, p016Tag(t, "手工", "").ID)
	mustWriteSizedFile(t, filepath.Join(root, "source.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\n字幕\n"))
	if err := os.Chmod(keeperDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(keeperDir, 0o755) })

	result, err := MergeMediaMetadata(MediaMergeKindVideo, keeper.ID, []uint{source.ID}, MediaMetadataMergeDeps{
		Watched: &p016WatchedSetter{}, Subtitles: NewSubtitleFileWriter(t.TempDir()),
	})
	if err != nil {
		t.Fatalf("字幕失败不应让合并失败: %v", err)
	}
	if result.SubtitleMoved || len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "字幕迁移失败") {
		t.Fatalf("应返回一条字幕警告: %+v", result)
	}
	if strings.Contains(result.Warnings[0], root) {
		t.Fatalf("警告不应含绝对路径: %q", result.Warnings[0])
	}
	if merged := p016ReloadVideo(t, keeper.ID); len(merged.Tags) != 1 {
		t.Fatalf("数据库合并应已生效: %+v", merged.Tags)
	}
}

func TestMergeMediaMetadataIMG03MergesImageCuration(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	keeper := imageCleanupCreateImage(t, filepath.Join(dir, "keeper.jpg"), []byte("keeper"), "", 400, 300)
	source := imageCleanupCreateImage(t, filepath.Join(dir, "source.jpg"), []byte("source"), "", 400, 300)
	at := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := database.DB.Model(&models.Image{}).Where("id = ?", source.ID).Updates(map[string]interface{}{
		"is_favorite": true, "favorited_at": at, "is_liked": true, "personal_rating": 9.0,
	}).Error; err != nil {
		t.Fatal(err)
	}
	tag := p016Tag(t, "风景", "")
	if err := database.DB.Exec("INSERT INTO image_tags(image_id, tag_id) VALUES (?, ?)", source.ID, tag.ID).Error; err != nil {
		t.Fatal(err)
	}
	person := p016Person(t, "乙")
	if err := database.DB.Create(&models.ImagePerson{ImageID: source.ID, PersonID: person.ID}).Error; err != nil {
		t.Fatal(err)
	}

	result, err := MergeMediaMetadata(MediaMergeKindImage, keeper.ID, []uint{source.ID}, MediaMetadataMergeDeps{})
	if err != nil {
		t.Fatalf("图片合并失败: %v", err)
	}
	if result.TagsAdded != 1 || result.PeopleAdded != 1 || !result.FavoriteChanged || !result.LikedChanged || !result.RatingChanged {
		t.Fatalf("图片合并结果不对: %+v", result)
	}
	var merged models.Image
	if err := database.DB.Preload("Tags").First(&merged, keeper.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(merged.Tags) != 1 || !merged.IsFavorite || merged.FavoritedAt == nil || !merged.FavoritedAt.Equal(at) || !merged.IsLiked || merged.PersonalRating == nil || *merged.PersonalRating != 9 {
		t.Fatalf("图片整理成果未合并: %+v", merged)
	}
	var people int64
	database.DB.Model(&models.ImagePerson{}).Where("image_id = ?", keeper.ID).Count(&people)
	if people != 1 {
		t.Fatalf("图片人物应合并: %d", people)
	}
}

func TestMergeMediaMetadataIMG03RejectsInvalidInput(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	keeper := p016Video(t, root, "keeper.mp4", "keeper-content", 1920, 1080)
	source := p016Video(t, root, "source.mp4", "source-content", 1280, 720)
	deps := MediaMetadataMergeDeps{Watched: &p016WatchedSetter{}}
	cases := []struct {
		name    string
		kind    string
		keeper  uint
		sources []uint
		deps    MediaMetadataMergeDeps
	}{
		{"未知类别", "audio", keeper.ID, []uint{source.ID}, deps},
		{"缺保留项", MediaMergeKindVideo, 0, []uint{source.ID}, deps},
		{"没有来源", MediaMergeKindVideo, keeper.ID, []uint{0}, deps},
		{"保留项也在来源里", MediaMergeKindVideo, keeper.ID, []uint{source.ID, keeper.ID}, deps},
		{"来源不存在", MediaMergeKindVideo, keeper.ID, []uint{source.ID + 100}, deps},
		{"缺已看写入器", MediaMergeKindVideo, keeper.ID, []uint{source.ID}, MediaMetadataMergeDeps{}},
	}
	for _, tc := range cases {
		if _, err := MergeMediaMetadata(tc.kind, tc.keeper, tc.sources, tc.deps); err == nil {
			t.Fatalf("%s 应当报错", tc.name)
		}
	}
}

// ===== IMG-07：忽略可移出、可失效、可撤销 =====

func TestCleanupIMG07NearDuplicateDismissalExpiresWhenFileChanges(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	a := p016Video(t, root, "a.mp4", "near-a-content", 1920, 1080)
	b := p016Video(t, root, "b.mp4", "near-b-content-longer", 1920, 1080)
	seedPerceptualHashRow(t, a, "0000000000000000")
	seedPerceptualHashRow(t, b, "0000000000000000")

	analysis, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil || len(analysis.NearDuplicateGroups) != 1 {
		t.Fatalf("应先认出一组近似重复: %+v err=%v", analysis, err)
	}
	if err := DismissNearDuplicateGroup([]uint{b.ID, a.ID}); err != nil {
		t.Fatal(err)
	}
	var row models.NearDuplicateDismissal
	if err := database.DB.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	infoA, _ := os.Stat(a.Path)
	infoB, _ := os.Stat(b.Path)
	if row.FingerprintA != cleanupFileFingerprint(infoA.Size(), infoA.ModTime().UnixNano()) ||
		row.FingerprintB != cleanupFileFingerprint(infoB.Size(), infoB.ModTime().UnixNano()) {
		t.Fatalf("忽略应记下双方 size:mtimeNS: %+v", row)
	}
	oldCache := &CleanupService{status: CleanupStatus{Completed: true, Analysis: analysis}}
	if got := oldCache.Status(); got.Error != "" || len(got.Analysis.NearDuplicateGroups) != 0 {
		t.Fatalf("回读缓存应服从忽略: %+v", got)
	}
	if fresh, _ := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{}); len(fresh.NearDuplicateGroups) != 0 {
		t.Fatalf("重新分析应服从忽略: %+v", fresh.NearDuplicateGroups)
	}

	// b 被替换成另一个文件（重新算过感知哈希）：忽略失效，候选重新出现。
	mustWriteSizedFile(t, b.Path, []byte("near-b-content-re-encoded"))
	p016UpdateVideo(t, b.ID, map[string]interface{}{"size": int64(len("near-b-content-re-encoded"))})
	if err := database.DB.Where("video_id = ?", b.ID).Delete(&models.VideoPerceptualHash{}).Error; err != nil {
		t.Fatal(err)
	}
	seedPerceptualHashRow(t, b, "0000000000000000")
	fresh, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil || len(fresh.NearDuplicateGroups) != 1 {
		t.Fatalf("文件变了忽略应失效: %+v err=%v", fresh, err)
	}
	// 旧缓存仍是当初那两个文件，已忽略过就不能拿新文件的指纹让它复现。
	if got := oldCache.Status(); len(got.Analysis.NearDuplicateGroups) != 0 {
		t.Fatalf("旧缓存不能因新指纹复现已忽略的一对: %+v", got.Analysis.NearDuplicateGroups)
	}

	// 再忽略一次：同一对只刷新指纹、不新增行。
	if err := DismissNearDuplicateGroup([]uint{a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	var count int64
	database.DB.Model(&models.NearDuplicateDismissal{}).Count(&count)
	if count != 1 {
		t.Fatalf("重复忽略只刷新指纹: %d", count)
	}
	if again, _ := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{}); len(again.NearDuplicateGroups) != 0 {
		t.Fatalf("刷新指纹后应重新生效: %+v", again.NearDuplicateGroups)
	}

	// 历史行（指纹为空）永不失效。
	database.DB.Model(&models.NearDuplicateDismissal{}).Where("1 = 1").Updates(map[string]interface{}{"fingerprint_a": "", "fingerprint_b": ""})
	mustWriteSizedFile(t, a.Path, []byte("near-a-changed-again"))
	p016UpdateVideo(t, a.ID, map[string]interface{}{"size": int64(len("near-a-changed-again"))})
	database.DB.Where("video_id = ?", a.ID).Delete(&models.VideoPerceptualHash{})
	seedPerceptualHashRow(t, a, "0000000000000000")
	if legacy, _ := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{}); len(legacy.NearDuplicateGroups) != 0 {
		t.Fatalf("历史忽略行不应失效: %+v", legacy.NearDuplicateGroups)
	}

	if err := DismissNearDuplicateGroup([]uint{a.ID, a.ID}); err == nil {
		t.Fatal("去重后不足两个视频应报错")
	}
	missing := p016Video(t, root, "gone.mp4", "gone-content", 1920, 1080)
	os.Remove(missing.Path)
	if err := DismissNearDuplicateGroup([]uint{a.ID, missing.ID}); err == nil {
		t.Fatal("文件读不到时不能写一条空指纹的忽略")
	}
}

// 缓存里没有某一侧当时的指纹（无从核对）时，忽略照旧生效：不能因为核对不了就把用户否决过的一对放回来。
func TestCleanupIMG07UnverifiableFingerprintKeepsDismissal(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	a := p016Video(t, root, "a.mp4", "unverifiable-a", 1920, 1080)
	b := p016Video(t, root, "b.mp4", "unverifiable-b-x", 1920, 1080)
	if err := DismissNearDuplicateGroup([]uint{a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	analysis := &CleanupAnalysis{NearDuplicateGroups: []CleanupDuplicateGroup{{Original: a, Candidates: []models.Video{b}}}}
	svc := &CleanupService{status: CleanupStatus{Completed: true, Analysis: analysis}}
	if got := svc.Status(); got.Error != "" || len(got.Analysis.NearDuplicateGroups) != 0 {
		t.Fatalf("无从核对指纹时忽略应照旧生效: %+v", got)
	}
	// 能核对且对不上时才失效。
	analysis.sourceFingerprints = map[uint]string{a.ID: "1:1", b.ID: "2:2"}
	if got := svc.Status(); len(got.Analysis.NearDuplicateGroups) != 1 {
		t.Fatalf("指纹对不上时忽略应失效: %+v", got.Analysis.NearDuplicateGroups)
	}
}

func TestCleanupIMG07DismissMemberOnlyRemovesThatMember(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	a := p016Video(t, root, "a.mp4", "member-a", 1920, 1080)
	b := p016Video(t, root, "b.mp4", "member-b-x", 1920, 1080)
	c := p016Video(t, root, "c.mp4", "member-c-xx", 1920, 1080)
	for _, video := range []models.Video{a, b, c} {
		seedPerceptualHashRow(t, video, "0000000000000000")
	}
	analysis, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil || len(analysis.NearDuplicateGroups) != 1 || len(p016GroupIDs(analysis.NearDuplicateGroups[0])) != 3 {
		t.Fatalf("三个视频应成一组: %+v err=%v", analysis, err)
	}
	if err := DismissNearDuplicateMember([]uint{a.ID, b.ID, c.ID}, c.ID); err != nil {
		t.Fatal(err)
	}
	var rows []models.NearDuplicateDismissal
	database.DB.Order("video_low_id, video_high_id").Find(&rows)
	if len(rows) != 2 || rows[0].VideoHighID != c.ID || rows[1].VideoHighID != c.ID {
		t.Fatalf("只写该成员与其他成员的配对: %+v", rows)
	}
	cached := &CleanupService{status: CleanupStatus{Completed: true, Analysis: analysis}}
	for _, got := range []*CleanupAnalysis{cached.Status().Analysis, mustAnalyzeCleanup(t)} {
		if len(got.NearDuplicateGroups) != 1 || !sameUintSet(p016GroupIDs(got.NearDuplicateGroups[0]), []uint{a.ID, b.ID}) {
			t.Fatalf("移出成员后其余两个仍成组: %+v", got.NearDuplicateGroups)
		}
	}
	if err := DismissNearDuplicateMember([]uint{a.ID, b.ID}, c.ID); err == nil {
		t.Fatal("成员不在组里应报错")
	}
	if err := DismissNearDuplicateMember([]uint{c.ID}, c.ID); err == nil {
		t.Fatal("组里只剩自己应报错")
	}
}

func mustAnalyzeCleanup(t *testing.T) *CleanupAnalysis {
	t.Helper()
	analysis, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	return analysis
}

func TestCleanupIMG07ListAndUndoDismissals(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	a := p016Video(t, root, "a.mp4", "list-a", 1920, 1080)
	b := p016Video(t, root, "b.mp4", "list-b-x", 1920, 1080)
	seedPerceptualHashRow(t, a, "0000000000000000")
	seedPerceptualHashRow(t, b, "0000000000000000")
	if err := DismissNearDuplicateGroup([]uint{a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.ClipDismissal{VideoFullID: a.ID, VideoClipID: b.ID}).Error; err != nil {
		t.Fatal(err)
	}
	shorts := make([]models.Video, 0, 3)
	for i := 0; i < 3; i++ {
		video := p016Video(t, root, fmt.Sprintf("short-%d.mp4", i), fmt.Sprintf("short-content-%d", i), 1920, 1080)
		if err := DismissCleanupVideo(video.ID, models.CleanupDismissalCategoryShort); err != nil {
			t.Fatal(err)
		}
		shorts = append(shorts, video)
	}
	if err := DismissCleanupVideo(a.ID, models.CleanupDismissalCategoryLow); err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.ImageNearDuplicateDismissal{ImageLowID: 900, ImageHighID: 901}).Error; err != nil {
		t.Fatal(err)
	}

	near, err := ListCleanupDismissals(CleanupDismissalKindNearDuplicate, 0, 0)
	if err != nil || len(near.Items) != 1 || near.HasMore {
		t.Fatalf("近似重复忽略列表不对: %+v err=%v", near, err)
	}
	if media := near.Items[0].Media; len(media) != 2 || media[0].ID != a.ID || media[0].Name != "a.mp4" || media[1].ID != b.ID || media[0].Missing {
		t.Fatalf("忽略记录应带双方名称: %+v", media)
	}
	clip, err := ListCleanupDismissals(CleanupDismissalKindClip, 0, 10)
	if err != nil || len(clip.Items) != 1 || clip.Items[0].Media[0].ID != a.ID || clip.Items[0].Media[1].ID != b.ID {
		t.Fatalf("截取忽略列表不对: %+v err=%v", clip, err)
	}
	low, err := ListCleanupDismissals(CleanupDismissalKindLow, 0, 10)
	if err != nil || len(low.Items) != 1 || len(low.Items[0].Media) != 1 || low.Items[0].Media[0].ID != a.ID {
		t.Fatalf("极低忽略列表不应混入极短: %+v err=%v", low, err)
	}
	images, err := ListCleanupDismissals(CleanupDismissalKindImageNearDuplicate, 0, 10)
	if err != nil || len(images.Items) != 1 || !images.Items[0].Media[0].Missing {
		t.Fatalf("图片忽略列表（媒体已不在时标 missing）: %+v err=%v", images, err)
	}

	first, err := ListCleanupDismissals(CleanupDismissalKindShort, 0, 2)
	if err != nil || len(first.Items) != 2 || !first.HasMore || first.Items[0].Media[0].ID != shorts[2].ID {
		t.Fatalf("极短忽略第一页（新到旧）: %+v err=%v", first, err)
	}
	second, err := ListCleanupDismissals(CleanupDismissalKindShort, first.NextCursor, 2)
	if err != nil || len(second.Items) != 1 || second.HasMore || second.Items[0].Media[0].ID != shorts[0].ID {
		t.Fatalf("极短忽略第二页: %+v err=%v", second, err)
	}
	if _, err := ListCleanupDismissals("same_source", 0, 10); err == nil {
		t.Fatal("同源否决不在忽略列表里，未知类别应报错")
	}

	// 删除视频后仍然列出（标 missing），可以撤销。
	if err := database.DB.Delete(&models.Video{}, shorts[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if page, _ := ListCleanupDismissals(CleanupDismissalKindShort, first.NextCursor, 2); !page.Items[0].Media[0].Missing || page.Items[0].Media[0].Name == "" {
		t.Fatalf("软删的视频应标 missing 并保留名称: %+v", page.Items[0].Media)
	}

	// 撤销只作用于本类：拿极短的记录 ID 去撤极低，一条都不删。
	if result, err := UndoCleanupDismissals(CleanupDismissalKindLow, []uint{first.Items[0].ID}); err != nil || result.Removed != 0 {
		t.Fatalf("跨类别撤销不应生效: %+v err=%v", result, err)
	}
	if result, err := UndoCleanupDismissals(CleanupDismissalKindShort, []uint{first.Items[0].ID, first.Items[0].ID, 0}); err != nil || result.Removed != 1 {
		t.Fatalf("撤销应删掉一条: %+v err=%v", result, err)
	}
	if result, err := UndoCleanupDismissals(CleanupDismissalKindNearDuplicate, []uint{near.Items[0].ID}); err != nil || result.Removed != 1 {
		t.Fatalf("撤销近似重复忽略: %+v err=%v", result, err)
	}
	if analysis := mustAnalyzeCleanup(t); len(analysis.NearDuplicateGroups) != 1 {
		t.Fatalf("撤销后近似重复应重新出现: %+v", analysis.NearDuplicateGroups)
	}
	if _, err := UndoCleanupDismissals("unknown", []uint{1}); err == nil {
		t.Fatal("未知类别撤销应报错")
	}
}

func TestImageCleanupIMG07MemberDismissalAndFingerprintExpiry(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	a := imageCleanupCreateImage(t, filepath.Join(dir, "a.jpg"), bytes.Repeat([]byte("a"), 300), "abcd000000000000", 100, 100)
	b := imageCleanupCreateImage(t, filepath.Join(dir, "b.jpg"), bytes.Repeat([]byte("b"), 301), "abcd000000000000", 100, 100)
	c := imageCleanupCreateImage(t, filepath.Join(dir, "c.jpg"), bytes.Repeat([]byte("c"), 302), "abcd000000000000", 100, 100)
	svc := newImageCleanupTestService()
	analysis, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil || len(analysis.NearDuplicateGroups) != 1 || len(imageCleanupGroupIDs(analysis.NearDuplicateGroups[0])) != 3 {
		t.Fatalf("三张图应成一组: %+v err=%v", analysis, err)
	}
	if err := DismissImageNearDuplicateMember([]uint{a.ID, b.ID, c.ID}, c.ID); err != nil {
		t.Fatal(err)
	}
	var rows []models.ImageNearDuplicateDismissal
	database.DB.Find(&rows)
	if len(rows) != 2 || rows[0].FingerprintA == "" || rows[0].FingerprintB == "" {
		t.Fatalf("移出成员只写两条带指纹的配对: %+v", rows)
	}
	analysis, _ = svc.AnalyzeImageCleanupCandidates()
	if len(analysis.NearDuplicateGroups) != 1 || !sameUintSet(imageCleanupGroupIDs(analysis.NearDuplicateGroups[0]), []uint{a.ID, b.ID}) {
		t.Fatalf("其余两张仍成组: %+v", analysis.NearDuplicateGroups)
	}

	// c 被替换并重新算过指纹：忽略失效，c 回到组里。
	if err := os.WriteFile(c.Path, bytes.Repeat([]byte("C"), 303), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(c.Path)
	database.DB.Model(&models.Image{}).Where("id = ?", c.ID).Updates(map[string]interface{}{"size": info.Size(), "hash_source_size": info.Size(), "hash_source_mod_time_ns": info.ModTime().UnixNano()})
	analysis, _ = svc.AnalyzeImageCleanupCandidates()
	if len(analysis.NearDuplicateGroups) != 1 || len(imageCleanupGroupIDs(analysis.NearDuplicateGroups[0])) != 3 {
		t.Fatalf("文件变了忽略应失效: %+v", analysis.NearDuplicateGroups)
	}
	if err := DismissImageNearDuplicateMember([]uint{a.ID, b.ID}, c.ID); err == nil {
		t.Fatal("成员不在组里应报错")
	}
}

// ===== IMG-11：覆盖率 =====

func TestCleanupIMG11CoverageSeparatesNotComputedFromNoDuplicates(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	ready := p016Video(t, root, "ready.mp4", "ready-content", 1920, 1080)
	stale := p016Video(t, root, "stale.mp4", "stale-content", 1920, 1080)
	p016Video(t, root, "bare.mp4", "bare-content", 1920, 1080)
	marked := p016Video(t, root, "marked-stale.mp4", "marked-content", 1920, 1080)
	offline := p016Video(t, root, "offline.mp4", "offline-content", 1920, 1080)
	oldVersion := p016Video(t, root, "old-version.mp4", "old-version-content", 1920, 1080)

	seedPerceptualHashRow(t, ready, "0000000000000000")
	seedFrameHashSequence(t, ready, randomFrameHashes(11, 30))
	seedPerceptualHashRow(t, stale, "1111111111111111")
	database.DB.Model(&models.VideoPerceptualHash{}).Where("video_id = ?", stale.ID).Update("source_size", 999)
	if err := database.DB.Create(&models.VideoFrameHashSequence{VideoID: stale.ID, IntervalMS: clipFrameIntervalMS, Hashes: []byte{}, LastError: "decode failed", ComputedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	seedPerceptualHashRow(t, marked, "2222222222222222")
	p016UpdateVideo(t, marked.ID, map[string]interface{}{"is_stale": true, "stale_reason": models.StaleReasonMissingFile})
	seedPerceptualHashRow(t, offline, "3333333333333333")
	if err := os.Remove(offline.Path); err != nil {
		t.Fatal(err)
	}
	for _, fingerprint := range []models.VideoVisualFingerprint{
		{VideoID: ready.ID, ContentFingerprint: "c1", AlgorithmVersion: sameSourceFingerprintVersion, FrameHashesJSON: "{}"},
		{VideoID: oldVersion.ID, ContentFingerprint: "c2", AlgorithmVersion: "same-source-dhash-v1", FrameHashesJSON: "{}"},
		{VideoID: marked.ID, ContentFingerprint: "c3", AlgorithmVersion: sameSourceFingerprintVersion, FrameHashesJSON: "{}"},
	} {
		if err := database.DB.Create(&fingerprint).Error; err != nil {
			t.Fatal(err)
		}
	}

	analysis := mustAnalyzeCleanup(t)
	want := CleanupCoverage{
		// total 不含标了失效的 marked；offline 读不到但有完整的行，无从核对，照算已算过。
		PerceptualHash: CleanupCoverageCount{Done: 2, Total: 5},
		FrameHash:      CleanupCoverageCount{Done: 1, Total: 5},
		SameSource:     CleanupSameSourceCoverage{Evaluated: 1, Total: 5},
	}
	if analysis.Coverage != want {
		t.Fatalf("覆盖率不对: got=%+v want=%+v", analysis.Coverage, want)
	}
	// 缓存回读保留覆盖率。
	cached := &CleanupService{status: CleanupStatus{Completed: true, Analysis: analysis}}
	if got := cached.Status(); got.Analysis.Coverage != want {
		t.Fatalf("回读缓存丢了覆盖率: %+v", got.Analysis.Coverage)
	}

	// 空库：total 与 done 都是 0。
	setupCleanupServiceTestDB(t)
	if empty := mustAnalyzeCleanup(t); empty.Coverage != (CleanupCoverage{}) {
		t.Fatalf("空库覆盖率应全为 0: %+v", empty.Coverage)
	}
}

func TestImageCleanupIMG11CoverageCountsHashedImages(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	imageCleanupCreateImage(t, filepath.Join(dir, "fresh.jpg"), bytes.Repeat([]byte("a"), 100), "abcd000000000000", 10, 10)
	imageCleanupCreateImage(t, filepath.Join(dir, "none.jpg"), bytes.Repeat([]byte("b"), 101), "", 10, 10)
	stale := imageCleanupCreateImage(t, filepath.Join(dir, "stale.jpg"), bytes.Repeat([]byte("c"), 102), "abcd000000000001", 10, 10)
	database.DB.Model(&models.Image{}).Where("id = ?", stale.ID).Update("hash_source_size", stale.HashSourceSize+1)
	offlineHashed := imageCleanupCreateImage(t, filepath.Join(dir, "offline-hashed.jpg"), bytes.Repeat([]byte("d"), 103), "abcd000000000002", 10, 10)
	offlineBare := imageCleanupCreateImage(t, filepath.Join(dir, "offline-bare.jpg"), bytes.Repeat([]byte("e"), 104), "", 10, 10)
	os.Remove(offlineHashed.Path)
	os.Remove(offlineBare.Path)

	analysis, err := newImageCleanupTestService().AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatal(err)
	}
	if want := (CleanupCoverageCount{Done: 2, Total: 5}); analysis.Coverage.PerceptualHash != want {
		t.Fatalf("图片覆盖率不对: got=%+v want=%+v", analysis.Coverage.PerceptualHash, want)
	}
}

// ===== IMG-12：分析可取消，图片分析登记为 image_cleanup =====

func TestCleanupIMG12AnalysisStopsOnCancelledContext(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	p016Video(t, root, "a.mp4", "a-content", 1920, 1080)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := (&CleanupService{}).analyzeCleanupCandidates(ctx, CleanupCriteria{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消的上下文应让视频分析返回 context.Canceled: %v", err)
	}
	setupImageServiceTestDB(t)
	imageCleanupCreateImage(t, filepath.Join(t.TempDir(), "a.jpg"), []byte("a"), "", 1, 1)
	if _, _, err := newImageCleanupTestService().analyzeImageCleanupCandidates(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消的上下文应让图片分析返回 context.Canceled: %v", err)
	}
}

// p016BlockFirstQuery 让第一次读 table 的查询停在回调里，直到 release 被关闭。
func p016BlockFirstQuery(t *testing.T, table string) (reached, release chan struct{}) {
	t.Helper()
	reached, release = make(chan struct{}), make(chan struct{})
	var once sync.Once
	name := "p016:block-" + table
	if err := database.DB.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table != table {
			return
		}
		blocked := false
		once.Do(func() { blocked = true })
		if blocked {
			close(reached)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DB.Callback().Query().Remove(name) })
	return reached, release
}

func p016Await(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(cleanupRunWaitTimeout):
		t.Fatalf("等待%s超时", what)
	}
}

func TestCleanupIMG12CancelRunningVideoAnalysis(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	a := p016Video(t, root, "a.mp4", "cancel-a", 1920, 1080)
	b := p016Video(t, root, "b.mp4", "cancel-b-x", 1920, 1080)
	seedPerceptualHashRow(t, a, "0000000000000000")
	seedPerceptualHashRow(t, b, "0000000000000000")
	reached, release := p016BlockFirstQuery(t, "video_perceptual_hashes")

	svc := &CleanupService{}
	if err := svc.CancelAnalysis(); !errors.Is(err, ErrCleanupAnalysisNotRunning) {
		t.Fatalf("没在跑时取消应报未运行: %v", err)
	}
	finished := watchCleanupRunFinished(svc)
	if _, err := svc.StartAnalysis(CleanupCriteria{}); err != nil {
		t.Fatal(err)
	}
	p016Await(t, reached, "分析进入近似重复阶段")
	if err := svc.CancelAnalysis(); err != nil {
		t.Fatalf("运行中取消失败: %v", err)
	}
	if status := svc.Status(); !status.Running {
		t.Fatalf("goroutine 停下之前仍应是运行中（不会并发起第二轮）: %+v", status)
	}
	close(release)
	waitForCleanupRunFinished(t, finished)

	status := svc.Status()
	if status.Running || status.Completed || !status.Cancelled || status.Error != "" || status.Analysis != nil {
		t.Fatalf("取消后的状态不对: %+v", status)
	}
	if status.Progress.Stage != "done" || !strings.Contains(status.Progress.Message, "取消") {
		t.Fatalf("取消也要发终止阶段: %+v", status.Progress)
	}
	// 取消之后可以重新分析。
	finished = watchCleanupRunFinished(svc)
	if _, err := svc.StartAnalysis(CleanupCriteria{}); err != nil {
		t.Fatal(err)
	}
	waitForCleanupRunFinished(t, finished)
	if status := svc.Status(); !status.Completed || status.Cancelled || len(status.Analysis.NearDuplicateGroups) != 1 {
		t.Fatalf("重新分析应正常完成: %+v", status)
	}
}

func TestImageCleanupIMG12CancelAndRegistersImageCleanupTask(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	same := bytes.Repeat([]byte("s"), 512)
	imageCleanupCreateImage(t, filepath.Join(dir, "a.jpg"), same, "", 10, 10)
	imageCleanupCreateImage(t, filepath.Join(dir, "b.jpg"), same, "", 20, 20)

	var mu sync.Mutex
	seen := make([][]string, 0)
	finished := make(chan struct{}, 4)
	registry := NewBackgroundTaskRegistry()
	registry.SetOnChange(func(running []string) {
		mu.Lock()
		seen = append(seen, append([]string(nil), running...))
		mu.Unlock()
		if len(running) == 0 {
			finished <- struct{}{}
		}
	})
	svc := newImageCleanupTestService()
	svc.SetBackgroundTaskRegistry(registry)
	if err := svc.CancelImageCleanupAnalysis(); !errors.Is(err, ErrImageCleanupAnalysisNotRunning) {
		t.Fatalf("没在跑时取消应报未运行: %v", err)
	}

	reached, release := p016BlockFirstQuery(t, "images")
	if _, err := svc.StartImageCleanupAnalysis(); err != nil {
		t.Fatal(err)
	}
	p016Await(t, reached, "图片分析读图片表")
	if err := svc.CancelImageCleanupAnalysis(); err != nil {
		t.Fatalf("运行中取消失败: %v", err)
	}
	close(release)
	p016Await(t, finished, "图片分析收尾")
	status := svc.GetImageCleanupStatus()
	if status.Running || status.Completed || !status.Cancelled || status.Analysis != nil || status.Progress.Stage != "done" {
		t.Fatalf("取消后的图片分析状态不对: %+v", status)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 || len(seen[0]) != 1 || seen[0][0] != string(BackgroundTaskImageCleanup) {
		t.Fatalf("图片清理分析应登记为 image_cleanup: %v", seen)
	}
}

// ===== APP-11：极短片段 / 极低分辨率可以忽略，徽标随之消退 =====

func TestCleanupAPP11DismissShortAndLowCandidates(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	short := clipFixtureVideo(t, root, "short.mp4", "short-content")
	small := clipFixtureVideo(t, root, "small.mp4", "small-content-x")
	criteria := CleanupCriteria{MinDuration: 5 * time.Second, MinWidth: 480, MinHeight: 320}
	analysis, err := (&CleanupService{}).AnalyzeCleanupCandidates(criteria)
	if err != nil || !sameUintSet(videoIDs(analysis.LowDuration), []uint{short.ID}) || !sameUintSet(videoIDs(analysis.LowResolution), []uint{small.ID}) {
		t.Fatalf("两类候选应各有一条: %+v err=%v", analysis, err)
	}

	if err := DismissCleanupVideo(short.ID, models.CleanupDismissalCategoryShort); err != nil {
		t.Fatal(err)
	}
	cached := &CleanupService{status: CleanupStatus{Completed: true, Analysis: analysis}}
	got := cached.Status().Analysis
	if len(got.LowDuration) != 0 || len(got.LowResolution) != 1 {
		t.Fatalf("回读缓存应只去掉被忽略的极短候选: %+v", got)
	}
	if len(analysis.LowDuration) != 1 {
		t.Fatal("过滤不能改共享缓存")
	}
	// 忽略「极低」对「极短」不生效，反之亦然。
	if err := DismissCleanupVideo(short.ID, models.CleanupDismissalCategoryLow); err != nil {
		t.Fatal(err)
	}
	if err := DismissCleanupVideo(small.ID, models.CleanupDismissalCategoryLow); err != nil {
		t.Fatal(err)
	}
	if err := DismissCleanupVideo(small.ID, models.CleanupDismissalCategoryLow); err != nil {
		t.Fatalf("重复忽略应幂等: %v", err)
	}
	var count int64
	database.DB.Model(&models.CleanupVideoDismissal{}).Count(&count)
	if count != 3 {
		t.Fatalf("每个 (视频, 类别) 只一条: %d", count)
	}
	fresh, _ := (&CleanupService{}).AnalyzeCleanupCandidates(criteria)
	if len(fresh.LowDuration) != 0 || len(fresh.LowResolution) != 0 {
		t.Fatalf("忽略后两类都应清空（徽标消退）: %+v", fresh)
	}

	// 文件变了：忽略失效，候选重新出现。
	mustWriteSizedFile(t, short.Path, []byte("short-content-re-encoded"))
	fresh, _ = (&CleanupService{}).AnalyzeCleanupCandidates(criteria)
	if !sameUintSet(videoIDs(fresh.LowDuration), []uint{short.ID}) {
		t.Fatalf("文件变了极短候选应重新出现: %+v", fresh.LowDuration)
	}

	for _, bad := range []struct {
		id       uint
		category string
	}{{short.ID, "tiny"}, {0, models.CleanupDismissalCategoryShort}, {short.ID + 100, models.CleanupDismissalCategoryShort}} {
		if err := DismissCleanupVideo(bad.id, bad.category); err == nil {
			t.Fatalf("非法忽略应报错: %+v", bad)
		}
	}
}

// ===== META-10（清理侧）：阈值读设置 =====

func TestCleanupMETA10ThresholdsComeFromSettings(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	// 没有设置行：用默认 5 / 480 / 320。
	criteria, err := CleanupCriteriaFromSettings()
	if err != nil || criteria != (CleanupCriteria{MinDuration: 5 * time.Second, MinWidth: 480, MinHeight: 320}) {
		t.Fatalf("默认阈值不对: %+v err=%v", criteria, err)
	}
	settings := models.Settings{VideoExtensions: ".mp4", PlayWeight: 2, CleanupShortSeconds: 40, CleanupLowWidth: 1280, CleanupLowHeight: 800}
	if err := database.DB.Create(&settings).Error; err != nil {
		t.Fatal(err)
	}
	short := clipFixtureVideo(t, root, "short.mp4", "short-content")   // 2 秒，1280×720
	small := clipFixtureVideo(t, root, "small.mp4", "small-content-x") // 30 秒，320×240
	normal := clipFixtureVideo(t, root, "normal.mp4", "normal-content-xx")

	svc := &CleanupService{}
	finished := watchCleanupRunFinished(svc)
	if _, err := svc.StartAnalysisFromSettings(); err != nil {
		t.Fatal(err)
	}
	waitForCleanupRunFinished(t, finished)
	analysis := svc.Status().Analysis
	if analysis == nil {
		t.Fatal("分析应已完成")
	}
	if analysis.Thresholds != (CleanupThresholds{ShortSeconds: 40, LowWidth: 1280, LowHeight: 800}) {
		t.Fatalf("结果应带本轮阈值: %+v", analysis.Thresholds)
	}
	// 12 秒的 normal 在 40 秒阈值下也是极短；1280×720 在 1280×800 阈值下算极低。
	if !sameUintSet(videoIDs(analysis.LowDuration), []uint{short.ID, small.ID, normal.ID}) {
		t.Fatalf("极短应按设置的 40 秒判定: %v", videoIDs(analysis.LowDuration))
	}
	if !sameUintSet(videoIDs(analysis.LowResolution), []uint{short.ID, small.ID}) {
		t.Fatalf("极低应按设置的 1280×800 判定: %v", videoIDs(analysis.LowResolution))
	}

	// ≤0 视为默认值。
	if err := database.DB.Model(&models.Settings{}).Where("id = ?", settings.ID).Updates(map[string]interface{}{
		"cleanup_short_seconds": 0, "cleanup_low_width": -1, "cleanup_low_height": 0,
	}).Error; err != nil {
		t.Fatal(err)
	}
	criteria, err = CleanupCriteriaFromSettings()
	if err != nil || criteria != (CleanupCriteria{MinDuration: 5 * time.Second, MinWidth: 480, MinHeight: 320}) {
		t.Fatalf("≤0 应取默认值: %+v err=%v", criteria, err)
	}
}

// IMG-07：AI 查找同源与清理分析同一口径——近似重复忽略在任一侧文件变了之后不再算数，
// 读不到的文件无从核对时照旧算数。
func TestAISameSourceIMG07DismissalFollowsFileFingerprint(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	a := p016Video(t, root, "a.mp4", "same-source-a", 1920, 1080)
	b := p016Video(t, root, "b.mp4", "same-source-b-x", 1920, 1080)
	if err := DismissNearDuplicateGroup([]uint{a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	pair := cleanupVideoPairKey(a.ID, b.ID)
	loaded := func() bool {
		t.Helper()
		pairs, err := loadActiveNearDuplicateDismissals(currentSameSourceFingerprints(a, []models.Video{b}))
		if err != nil {
			t.Fatal(err)
		}
		_, ok := pairs[pair]
		return ok
	}
	if !loaded() {
		t.Fatal("文件未变时忽略应生效")
	}
	mustWriteSizedFile(t, b.Path, []byte("same-source-b-re-encoded"))
	if loaded() {
		t.Fatal("一侧文件变了忽略应失效，不能再跳过这一对")
	}
	if err := os.Remove(b.Path); err != nil {
		t.Fatal(err)
	}
	if !loaded() {
		t.Fatal("读不到的文件无从核对，忽略应照旧生效")
	}
}
