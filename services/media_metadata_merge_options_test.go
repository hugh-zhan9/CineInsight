package services

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// P-032 评审 I-2（§9.1 主代理裁决）：截取片段只是完整片的一小段，合并时跳过观看状态与字幕；
// 标签、人物、作品集、收藏 / 点赞 / 评分照常合并。默认选项（零值）的行为由 IMG03 既有用例钉住。

// p032ClipMergeFixture 建一对 keeper / source：source 已看、有断点、有同名 .srt，并带齐其余整理成果。
func p032ClipMergeFixture(t *testing.T, root, prefix string) (keeper, source models.Video, collection models.MediaCollection) {
	t.Helper()
	keeper = p016Video(t, root, prefix+"-full.mp4", prefix+"-full-content", 1920, 1080)
	source = p016Video(t, root, prefix+"-clip.mp4", prefix+"-clip-content", 1920, 1080)
	watchedAt := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	p016UpdateVideo(t, keeper.ID, map[string]interface{}{"watch_position_seconds": 10.0})
	p016UpdateVideo(t, source.ID, map[string]interface{}{
		"is_watched": true, "watched_at": watchedAt, "watch_position_seconds": 100.0,
		"is_favorite": true, "favorited_at": watchedAt, "is_liked": true, "personal_rating": 8.0,
	})
	p016AttachVideoTag(t, source.ID, p016Tag(t, prefix+"-手工", "").ID)
	p016AttachVideoPerson(t, source.ID, p016Person(t, prefix+"-甲").ID)
	collection = p016Collection(t, prefix+"-系列")
	p016AddToCollection(t, collection.ID, source.ID, 2)
	mustWriteSizedFile(t, filepath.Join(root, prefix+"-clip.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\n片段字幕\n"))
	return keeper, source, collection
}

func TestMergeMediaMetadataIMG03ClipGroupSkipsPlaybackStateAndSubtitle(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	keeper, source, collection := p032ClipMergeFixture(t, root, "clip")

	setter := &p016WatchedSetter{}
	result, err := MergeMediaMetadata(MediaMergeKindVideo, keeper.ID, []uint{source.ID}, MediaMetadataMergeDeps{
		Watched: setter, Subtitles: NewSubtitleFileWriter(t.TempDir()),
		Options: MediaMetadataMergeOptions{SkipPlaybackState: true, SkipSubtitle: true},
	})
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	if result.TagsAdded != 1 || result.PeopleAdded != 1 || result.CollectionsAdded != 1 ||
		!result.FavoriteChanged || !result.LikedChanged || !result.RatingChanged {
		t.Fatalf("标签、人物、作品集、收藏 / 点赞 / 评分应照常合并: %+v", result)
	}
	if result.ProgressChanged || result.WatchedChanged || result.SubtitleMoved || len(result.Warnings) != 0 {
		t.Fatalf("截取片段组不应合并观看状态与字幕: %+v", result)
	}
	if len(setter.calls) != 0 {
		t.Fatalf("跳过观看状态时不应调用已看 setter（不触发已看同步）: %v", setter.calls)
	}

	merged := p016ReloadVideo(t, keeper.ID)
	if merged.IsWatched || merged.WatchedAt != nil || merged.WatchPositionSeconds != 10 {
		t.Fatalf("keeper 的已看与断点应保持不变: watched=%v at=%v position=%v", merged.IsWatched, merged.WatchedAt, merged.WatchPositionSeconds)
	}
	if len(merged.Tags) != 1 || !merged.IsFavorite || merged.FavoritedAt == nil || !merged.IsLiked ||
		merged.PersonalRating == nil || *merged.PersonalRating != 8 {
		t.Fatalf("其余整理成果应已合并: %+v", merged)
	}
	var people int64
	database.DB.Model(&models.VideoPerson{}).Where("video_id = ?", keeper.ID).Count(&people)
	if people != 1 {
		t.Fatalf("人物应已合并: %d", people)
	}
	var membership models.CollectionVideo
	if err := database.DB.Where("collection_id = ? AND video_id = ?", collection.ID, keeper.ID).First(&membership).Error; err != nil || membership.Position != 2 {
		t.Fatalf("作品集应已合并并放在来源的位置: %+v err=%v", membership, err)
	}
	if _, err := os.Stat(filepath.Join(root, "clip-full.srt")); !os.IsNotExist(err) {
		t.Fatalf("跳过字幕时不应给 keeper 写同名 .srt: %v", err)
	}
	var indexed int64
	database.DB.Model(&models.SubtitleIndexState{}).Where("video_id = ?", keeper.ID).Count(&indexed)
	if indexed != 0 {
		t.Fatalf("跳过字幕时不应刷新 keeper 的字幕索引: %d", indexed)
	}
}

// 两项各管各的：只跳过字幕时已看照常合并，只跳过观看状态时字幕照常复制。
func TestMergeMediaMetadataIMG03OptionsAreIndependent(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()

	keeper, source, _ := p032ClipMergeFixture(t, root, "subtitle-only")
	setter := &p016WatchedSetter{}
	result, err := MergeMediaMetadata(MediaMergeKindVideo, keeper.ID, []uint{source.ID}, MediaMetadataMergeDeps{
		Watched: setter, Subtitles: NewSubtitleFileWriter(t.TempDir()),
		Options: MediaMetadataMergeOptions{SkipSubtitle: true},
	})
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	if !result.WatchedChanged || !result.ProgressChanged || result.SubtitleMoved || len(setter.calls) != 1 {
		t.Fatalf("只跳过字幕：已看与断点照常合并、字幕不复制: %+v calls=%v", result, setter.calls)
	}
	if merged := p016ReloadVideo(t, keeper.ID); !merged.IsWatched || merged.WatchPositionSeconds != 100 {
		t.Fatalf("已看与断点应已合并: %+v", merged)
	}
	if _, err := os.Stat(filepath.Join(root, "subtitle-only-full.srt")); !os.IsNotExist(err) {
		t.Fatalf("只跳过字幕时不应复制字幕: %v", err)
	}

	keeper, source, _ = p032ClipMergeFixture(t, root, "playback-only")
	setter = &p016WatchedSetter{}
	result, err = MergeMediaMetadata(MediaMergeKindVideo, keeper.ID, []uint{source.ID}, MediaMetadataMergeDeps{
		Watched: setter, Subtitles: NewSubtitleFileWriter(t.TempDir()),
		Options: MediaMetadataMergeOptions{SkipPlaybackState: true},
	})
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	if result.WatchedChanged || result.ProgressChanged || !result.SubtitleMoved || len(setter.calls) != 0 {
		t.Fatalf("只跳过观看状态：字幕照常复制、已看与断点不动: %+v calls=%v", result, setter.calls)
	}
	if merged := p016ReloadVideo(t, keeper.ID); merged.IsWatched || merged.WatchPositionSeconds != 10 {
		t.Fatalf("已看与断点应保持不变: %+v", merged)
	}
	if _, err := os.Stat(filepath.Join(root, "playback-only-full.srt")); err != nil {
		t.Fatalf("字幕应复制成 keeper 的同名 .srt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "playback-only-clip.srt")); err != nil {
		t.Fatalf("来源字幕应留在原处: %v", err)
	}
}

// P-032 复审 m3：数据库合并已提交、补已看状态失败时，错误带 merge_committed: 前缀，数据库部分确实已生效；
// 事务里的失败（这里用不存在的保留项）不带前缀。
func TestMergeMediaMetadataIMG03WatchedSyncFailureIsMarkedCommitted(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	keeper, source, collection := p032ClipMergeFixture(t, root, "committed")

	failing := &p016WatchedSetter{err: errors.New("setter down")}
	result, err := MergeMediaMetadata(MediaMergeKindVideo, keeper.ID, []uint{source.ID}, MediaMetadataMergeDeps{
		Watched: failing, Subtitles: NewSubtitleFileWriter(t.TempDir()),
	})
	if err == nil || result != nil {
		t.Fatalf("已看翻转失败应返回错误: result=%+v err=%v", result, err)
	}
	if !strings.HasPrefix(err.Error(), MediaMergeCommittedErrorPrefix) {
		t.Fatalf("错误应带 %q 前缀: %v", MediaMergeCommittedErrorPrefix, err)
	}
	if !errors.Is(err, failing.err) {
		t.Fatalf("错误应保留 setter 的原因: %v", err)
	}
	if len(failing.calls) != 1 || failing.calls[0] != keeper.ID {
		t.Fatalf("应尝试翻转保留项的已看: %v", failing.calls)
	}

	merged := p016ReloadVideo(t, keeper.ID)
	if len(merged.Tags) != 1 || !merged.IsFavorite || !merged.IsLiked || merged.PersonalRating == nil || *merged.PersonalRating != 8 {
		t.Fatalf("数据库合并应已提交: %+v", merged)
	}
	if merged.WatchPositionSeconds != 100 {
		t.Fatalf("断点应已在事务里合并: %v", merged.WatchPositionSeconds)
	}
	if merged.IsWatched {
		t.Fatal("setter 失败时已看不应被翻转")
	}
	var people int64
	database.DB.Model(&models.VideoPerson{}).Where("video_id = ?", keeper.ID).Count(&people)
	if people != 1 {
		t.Fatalf("人物应已合并: %d", people)
	}
	var membership models.CollectionVideo
	if err := database.DB.Where("collection_id = ? AND video_id = ?", collection.ID, keeper.ID).First(&membership).Error; err != nil {
		t.Fatalf("作品集应已合并: %v", err)
	}

	missingKeeper := keeper.ID + source.ID + 100
	if _, err := MergeMediaMetadata(MediaMergeKindVideo, missingKeeper, []uint{source.ID}, MediaMetadataMergeDeps{Watched: &p016WatchedSetter{}}); err == nil ||
		strings.HasPrefix(err.Error(), MediaMergeCommittedErrorPrefix) {
		t.Fatalf("事务里的失败不应带已提交前缀: %v", err)
	}
}

// 图片没有观看状态和字幕：两项选项被忽略，其余整理成果照常合并。
func TestMergeMediaMetadataIMG03ImageIgnoresOptions(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	keeper := imageCleanupCreateImage(t, filepath.Join(dir, "keeper.jpg"), []byte("keeper"), "", 400, 300)
	source := imageCleanupCreateImage(t, filepath.Join(dir, "source.jpg"), []byte("source"), "", 400, 300)
	if err := database.DB.Model(&models.Image{}).Where("id = ?", source.ID).Updates(map[string]interface{}{
		"is_favorite": true, "favorited_at": time.Now(), "is_liked": true, "personal_rating": 6.0,
	}).Error; err != nil {
		t.Fatal(err)
	}
	result, err := MergeMediaMetadata(MediaMergeKindImage, keeper.ID, []uint{source.ID}, MediaMetadataMergeDeps{
		Options: MediaMetadataMergeOptions{SkipPlaybackState: true, SkipSubtitle: true},
	})
	if err != nil {
		t.Fatalf("图片合并失败: %v", err)
	}
	if !result.FavoriteChanged || !result.LikedChanged || !result.RatingChanged {
		t.Fatalf("图片合并不受选项影响: %+v", result)
	}
}
