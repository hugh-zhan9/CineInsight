package services

import (
	"path/filepath"
	"testing"

	"video-master/database"
	"video-master/models"
)

// 详情页「推荐」的后端两组：同目录（同一文件夹，按文件名）与同标签（共享非自动标签）。
// 共同前提：只看默认片库可见的片（扫描根内、非失效、未删除），排除本片与同一版本组的其他版本。

type recommendationFixture struct {
	t    *testing.T
	root string
}

func newRecommendationFixture(t *testing.T) *recommendationFixture {
	t.Helper()
	setupVideoServiceTestDB(t)
	root := t.TempDir()
	if err := database.DB.Create(&models.ScanDirectory{Path: root}).Error; err != nil {
		t.Fatal(err)
	}
	return &recommendationFixture{t: t, root: root}
}

func (f *recommendationFixture) dir(name string) string {
	f.t.Helper()
	directory := filepath.Join(f.root, name)
	return directory
}

func (f *recommendationFixture) video(directory, name string) models.Video {
	f.t.Helper()
	video := models.Video{Name: name, Path: filepath.Join(directory, name), Directory: directory, Duration: 600}
	if err := database.DB.Create(&video).Error; err != nil {
		f.t.Fatal(err)
	}
	return video
}

func (f *recommendationFixture) tag(name, automaticKind string) models.Tag {
	f.t.Helper()
	tag := models.Tag{Name: name, AutomaticKind: automaticKind}
	if err := database.DB.Create(&tag).Error; err != nil {
		f.t.Fatal(err)
	}
	return tag
}

func (f *recommendationFixture) attach(video models.Video, tags ...models.Tag) {
	f.t.Helper()
	for _, tag := range tags {
		if err := database.DB.Exec("INSERT INTO video_tags(video_id, tag_id) VALUES (?, ?)", video.ID, tag.ID).Error; err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *recommendationFixture) versionGroup(videos ...models.Video) {
	f.t.Helper()
	group := models.VideoVersionGroup{Title: "组", Revision: 1}
	if err := database.DB.Create(&group).Error; err != nil {
		f.t.Fatal(err)
	}
	for index, video := range videos {
		member := models.VideoVersionMember{GroupID: group.ID, VideoID: video.ID, Position: index + 1}
		if err := database.DB.Create(&member).Error; err != nil {
			f.t.Fatal(err)
		}
	}
}

func recommendationIDs(items []VideoRecommendationItem) []uint {
	ids := make([]uint, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.Video.ID)
	}
	return ids
}

func TestGetVideoRecommendationsSameDirectoryAndSharedTags(t *testing.T) {
	f := newRecommendationFixture(t)
	source := f.video(f.root, "source.mp4")
	sibling := f.video(f.root, "source.4k.mp4")
	f.versionGroup(source, sibling)
	episode1 := f.video(f.root, "ep01.mp4")
	episode2 := f.video(f.root, "ep02.mp4")
	stale := f.video(f.root, "stale.mp4")
	stale.IsStale = true
	stale.StaleReason = models.StaleReasonMissingFile
	if err := database.DB.Save(&stale).Error; err != nil {
		t.Fatal(err)
	}
	otherDir := f.dir("other")
	other := f.video(otherDir, "other.mp4")
	tagOnly := f.video(f.dir("more"), "tag-only.mp4")

	sciFi := f.tag("科幻", "")
	action := f.tag("动作", "")
	lowRes := f.tag("低清", "low_resolution")
	f.attach(source, sciFi, action, lowRes)
	f.attach(episode1, sciFi, action)
	f.attach(episode2, sciFi)
	f.attach(other, sciFi, action)
	f.attach(tagOnly, sciFi)
	f.attach(stale, sciFi, action)

	result, err := (&VideoService{}).GetVideoRecommendations(source.ID)
	if err != nil {
		t.Fatalf("读取推荐失败: %v", err)
	}
	// 同目录：按文件名排，排除本片、同组版本与失效片；别的文件夹不进来。
	if got, want := recommendationIDs(result.Directory), []uint{episode1.ID, episode2.ID}; !equalUintSlices(got, want) {
		t.Fatalf("同目录推荐 = %v，期望 %v", got, want)
	}
	// 同标签：同目录已经给出的两部不再重复；共享两个标签的排在共享一个的前面。
	if got, want := recommendationIDs(result.Tags), []uint{other.ID, tagOnly.ID}; !equalUintSlices(got, want) {
		t.Fatalf("同标签推荐 = %v，期望 %v", got, want)
	}
	if got, want := result.Tags[0].SharedTags, []string{"科幻", "动作"}; !equalStringSlices(got, want) {
		t.Fatalf("共享标签 = %v，期望 %v", got, want)
	}
	if got, want := result.Tags[1].SharedTags, []string{"科幻"}; !equalStringSlices(got, want) {
		t.Fatalf("共享标签 = %v，期望 %v", got, want)
	}
	if len(result.Directory[0].SharedTags) != 0 {
		t.Fatalf("同目录推荐不该带共享标签: %v", result.Directory[0].SharedTags)
	}
}

func TestGetVideoRecommendationsAutomaticTagsDoNotCount(t *testing.T) {
	f := newRecommendationFixture(t)
	source := f.video(f.root, "source.mp4")
	peer := f.video(f.dir("other"), "peer.mp4")
	shortTag := f.tag("短视频", "short_video")
	f.attach(source, shortTag)
	f.attach(peer, shortTag)

	result, err := (&VideoService{}).GetVideoRecommendations(source.ID)
	if err != nil {
		t.Fatalf("读取推荐失败: %v", err)
	}
	if len(result.Tags) != 0 {
		t.Fatalf("只有自动标签时不该有同标签推荐: %v", recommendationIDs(result.Tags))
	}
}

func TestGetVideoRecommendationsRespectsScanRootsAndLimits(t *testing.T) {
	f := newRecommendationFixture(t)
	source := f.video(f.root, "source.mp4")
	// 扫描根之外的目录：有片但不能被推荐到。
	outside := t.TempDir()
	f.video(outside, "outside.mp4")
	// 同目录超过上限时只给前 8 部（按文件名）。
	var episodes []models.Video
	for _, name := range []string{"e01.mp4", "e02.mp4", "e03.mp4", "e04.mp4", "e05.mp4", "e06.mp4", "e07.mp4", "e08.mp4", "e09.mp4"} {
		episodes = append(episodes, f.video(f.root, name))
	}

	result, err := (&VideoService{}).GetVideoRecommendations(source.ID)
	if err != nil {
		t.Fatalf("读取推荐失败: %v", err)
	}
	if got, want := len(result.Directory), videoRecommendationLimit; got != want {
		t.Fatalf("同目录推荐条数 = %d，期望 %d", got, want)
	}
	if got, want := recommendationIDs(result.Directory), recommendationIDs([]VideoRecommendationItem{
		{Video: episodes[0]}, {Video: episodes[1]}, {Video: episodes[2]}, {Video: episodes[3]},
		{Video: episodes[4]}, {Video: episodes[5]}, {Video: episodes[6]}, {Video: episodes[7]},
	}); !equalUintSlices(got, want) {
		t.Fatalf("同目录推荐 = %v，期望 %v", got, want)
	}
	for _, item := range result.Directory {
		if item.Video.ID == source.ID {
			t.Fatal("推荐里不该出现本片")
		}
	}
}

func TestGetVideoRecommendationsMissingVideo(t *testing.T) {
	newRecommendationFixture(t)
	if _, err := (&VideoService{}).GetVideoRecommendations(404); err == nil {
		t.Fatal("不存在的视频应该报错")
	}
}

func equalUintSlices(got, want []uint) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func equalStringSlices(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
