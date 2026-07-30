package services

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

func TestVideoDetailsUpdateIsAtomicAndDoesNotRenameFile(t *testing.T) {
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	people := NewPersonService(dataDir)
	collections := NewCollectionService(dataDir)
	svc := NewVideoDetailService(people, collections)
	video := createProbeTestVideo(t)
	originalPath := video.Path
	originalName := video.Name
	removedPerson, _ := people.CreatePerson("Old Actor", "")
	keptPerson, _ := people.CreatePerson("New Actor", "")
	if err := people.SetVideoPeople(video.ID, []uint{removedPerson.ID}); err != nil {
		t.Fatalf("建立旧人物关系失败: %v", err)
	}
	removedCollection, _ := collections.CreateCollection("Old Collection", "")
	keptCollection, _ := collections.CreateCollection("New Collection", "")
	if err := collections.AddCollectionVideo(removedCollection.ID, video.ID); err != nil {
		t.Fatalf("建立旧作品集关系失败: %v", err)
	}
	rating := 0.0

	detail, err := svc.UpdateVideoDetails(VideoDetailsUpdate{
		VideoID:        video.ID,
		DisplayTitle:   "Local Display Title",
		OriginalTitle:  "Original Title",
		PersonalRating: &rating,
		PersonIDs:      []uint{keptPerson.ID, keptPerson.ID},
		CollectionIDs:  []uint{keptCollection.ID, keptCollection.ID},
	})
	if err != nil {
		t.Fatalf("更新视频详情失败: %v", err)
	}
	if detail.EffectiveTitle != "Local Display Title" || detail.Video.PersonalRating == nil || *detail.Video.PersonalRating != 0 {
		t.Fatalf("标题 fallback 或 0 分语义错误: %#v", detail)
	}
	if detail.Video.Path != originalPath || detail.Video.Name != originalName {
		t.Fatalf("编辑标题不得重命名文件: %#v", detail.Video)
	}
	if len(detail.People) != 1 || detail.People[0].Person.ID != keptPerson.ID {
		t.Fatalf("人物完整集合更新错误: %#v", detail.People)
	}
	if len(detail.Collections) != 1 || detail.Collections[0].Collection.ID != keptCollection.ID {
		t.Fatalf("作品集完整集合更新错误: %#v", detail.Collections)
	}
	if err := database.DB.First(&models.Person{}, removedPerson.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("显式移除最后人物关系应清理人物: %v", err)
	}
	var removedMembership int64
	if err := database.DB.Model(&models.CollectionVideo{}).
		Where("collection_id = ? AND video_id = ?", removedCollection.ID, video.ID).
		Count(&removedMembership).Error; err != nil {
		t.Fatalf("统计移除作品集关系失败: %v", err)
	}
	if removedMembership != 0 {
		t.Fatalf("旧作品集关系未移除: count=%d", removedMembership)
	}

	invalidRating := 0.3
	_, err = svc.UpdateVideoDetails(VideoDetailsUpdate{
		VideoID: video.ID, DisplayTitle: "must rollback", PersonalRating: &invalidRating,
		PersonIDs: []uint{999999}, CollectionIDs: []uint{keptCollection.ID},
	})
	if err == nil {
		t.Fatal("非法评分/关系目标应拒绝")
	}
	after, err := svc.GetVideoDetails(video.ID)
	if err != nil {
		t.Fatalf("读取回滚后的详情失败: %v", err)
	}
	if after.Video.DisplayTitle != "Local Display Title" || len(after.People) != 1 || after.People[0].Person.ID != keptPerson.ID {
		t.Fatalf("无效请求产生部分更新: %#v", after)
	}
}

func TestVideoDetailsFallsBackToFilenameAndAggregatesStoredTechnicalData(t *testing.T) {
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	svc := NewVideoDetailService(NewPersonService(dataDir), NewCollectionService(dataDir))
	video := createProbeTestVideo(t)
	info, err := os.Stat(video.Path)
	if err != nil {
		t.Fatalf("读取视频文件状态失败: %v", err)
	}
	now := time.Now()
	size := info.Size()
	mtime := info.ModTime().UnixNano()
	metadata := models.VideoTechnicalMetadata{
		VideoID: video.ID, FormatName: "matroska", SuccessfulSourceSize: &size,
		SuccessfulSourceModTimeNS: &mtime, ProbedAt: &now, LastAttemptAt: &now,
	}
	if err := database.DB.Create(&metadata).Error; err != nil {
		t.Fatalf("创建技术快照失败: %v", err)
	}
	if err := database.DB.Create(&models.MediaStream{VideoID: video.ID, StreamIndex: 0, StreamType: "video", CodecName: "hevc"}).Error; err != nil {
		t.Fatalf("创建媒体流失败: %v", err)
	}
	srtPath := filepath.Join(filepath.Dir(video.Path), "movie.srt")
	state := models.SubtitleIndexState{VideoID: video.ID, SubtitlePath: srtPath, SegmentCount: 3, LastCheckedAt: now}
	if err := database.DB.Create(&state).Error; err != nil {
		t.Fatalf("创建外置字幕状态失败: %v", err)
	}
	for index := 0; index < 3; index++ {
		if err := database.DB.Create(&models.SubtitleSegment{VideoID: video.ID, SegmentIndex: index, SubtitlePath: srtPath}).Error; err != nil {
			t.Fatalf("创建外置字幕片段失败: %v", err)
		}
	}

	detail, err := svc.GetVideoDetails(video.ID)
	if err != nil {
		t.Fatalf("读取聚合详情失败: %v", err)
	}
	if detail.EffectiveTitle != video.Name {
		t.Fatalf("空显示标题应回退文件名: got=%q want=%q", detail.EffectiveTitle, video.Name)
	}
	if detail.TechnicalMetadata == nil || detail.TechnicalMetadata.FormatName != "matroska" || len(detail.Streams) != 1 {
		t.Fatalf("技术快照聚合错误: %#v", detail)
	}
	if detail.TechnicalStatus.State != TechnicalStateCurrent || detail.TechnicalStatus.IsStale {
		t.Fatalf("技术快照当前状态错误: %#v", detail.TechnicalStatus)
	}
	if detail.ExternalSubtitle == nil || detail.ExternalSubtitle.Path != srtPath || detail.ExternalSubtitle.Language != "unknown" || detail.ExternalSubtitle.SegmentCount != 3 || detail.ExternalSubtitle.LastSegmentIndex != 2 {
		t.Fatalf("外置字幕聚合错误: %#v", detail.ExternalSubtitle)
	}
}

func TestVideoDetailsLoadsRelationshipCountsWithoutPerEntityQueries(t *testing.T) {
	setupVideoServiceTestDB(t)
	people := NewPersonService(t.TempDir())
	collections := NewCollectionService(t.TempDir())
	svc := NewVideoDetailService(people, collections)
	video := createProbeTestVideo(t)
	for index := 0; index < 12; index++ {
		person, err := people.CreatePerson(fmt.Sprintf("Actor %02d", index), "")
		if err != nil {
			t.Fatalf("创建查询计数人物失败: %v", err)
		}
		if err := database.DB.Create(&models.VideoPerson{VideoID: video.ID, PersonID: person.ID}).Error; err != nil {
			t.Fatalf("创建查询计数人物关系失败: %v", err)
		}
		collection, err := collections.CreateCollection(fmt.Sprintf("Collection %02d", index), "")
		if err != nil {
			t.Fatalf("创建查询计数作品集失败: %v", err)
		}
		if err := database.DB.Create(&models.CollectionVideo{CollectionID: collection.ID, VideoID: video.ID, Position: 1}).Error; err != nil {
			t.Fatalf("创建查询计数作品集关系失败: %v", err)
		}
	}
	queryCount := 0
	const callbackName = "test:count-video-detail-queries"
	if err := database.DB.Callback().Query().Before("gorm:query").Register(callbackName, func(*gorm.DB) {
		queryCount++
	}); err != nil {
		t.Fatalf("注册详情查询计数回调失败: %v", err)
	}
	defer database.DB.Callback().Query().Remove(callbackName)

	detail, err := svc.GetVideoDetails(video.ID)
	if err != nil {
		t.Fatalf("批量读取详情关系失败: %v", err)
	}
	if len(detail.People) != 12 || len(detail.Collections) != 12 || queryCount > 10 {
		t.Fatalf("详情关系计数不应按实体增加查询: people=%d collections=%d queries=%d", len(detail.People), len(detail.Collections), queryCount)
	}
}

func TestVideoDetailsMarksStoredSnapshotStaleWithoutProbing(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewVideoDetailService(NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	video := createProbeTestVideo(t)
	oldSize := int64(1)
	oldMTime := int64(1)
	now := time.Now()
	if err := database.DB.Create(&models.VideoTechnicalMetadata{
		VideoID: video.ID, SuccessfulSourceSize: &oldSize, SuccessfulSourceModTimeNS: &oldMTime, ProbedAt: &now,
	}).Error; err != nil {
		t.Fatalf("创建过期技术快照失败: %v", err)
	}
	detail, err := svc.GetVideoDetails(video.ID)
	if err != nil {
		t.Fatalf("读取过期详情失败: %v", err)
	}
	if detail.TechnicalStatus.State != TechnicalStateStale || !detail.TechnicalStatus.IsStale {
		t.Fatalf("过期状态未识别: %#v", detail.TechnicalStatus)
	}
}

func TestVideoDetailsRejectsNonFiniteRating(t *testing.T) {
	setupVideoServiceTestDB(t)
	svc := NewVideoDetailService(NewPersonService(t.TempDir()), NewCollectionService(t.TempDir()))
	video := createProbeTestVideo(t)
	rating := math.NaN()
	if _, err := svc.UpdateVideoDetails(VideoDetailsUpdate{VideoID: video.ID, PersonalRating: &rating}); err == nil {
		t.Fatal("NaN 评分应拒绝")
	}
}

func TestTrashDeleteAndRestorePreserveMediaDetailsRelationshipsAndOrder(t *testing.T) {
	setupVideoServiceTestDB(t)
	dataDir := t.TempDir()
	people := NewPersonService(dataDir)
	collections := NewCollectionService(dataDir)
	videoService := NewVideoService(newMediaProbeServiceWithRunner(func(context.Context, string) ([]byte, string, error) {
		return []byte(multiStreamFFProbeFixture), "", nil
	}))
	first := createProbeTestVideo(t)
	second := createProbeTestVideo(t)
	person, _ := people.CreatePerson("Preserved Actor", "")
	collection, _ := collections.CreateCollection("Preserved Collection", "")
	if err := people.SetVideoPeople(first.ID, []uint{person.ID}); err != nil {
		t.Fatalf("建立待恢复人物关系失败: %v", err)
	}
	if err := collections.AddCollectionVideo(collection.ID, first.ID); err != nil {
		t.Fatalf("建立待恢复作品集关系失败: %v", err)
	}
	if err := collections.AddCollectionVideo(collection.ID, second.ID); err != nil {
		t.Fatalf("建立第二作品集成员失败: %v", err)
	}
	if err := videoService.technicalProbe().Refresh(context.Background(), first.ID); err != nil {
		t.Fatalf("建立待恢复技术快照失败: %v", err)
	}

	if err := videoService.DeleteVideo(first.ID, false); err != nil {
		t.Fatalf("软删除带详情视频失败: %v", err)
	}
	var personCount int64
	if err := database.DB.Model(&models.Person{}).Where("id = ?", person.ID).Count(&personCount).Error; err != nil || personCount != 1 {
		t.Fatalf("软删除不得清理人物: count=%d err=%v", personCount, err)
	}
	duringDelete, err := collections.GetCollectionDetail(collection.ID)
	if err != nil {
		t.Fatalf("软删除期间读取作品集失败: %v", err)
	}
	if len(duringDelete.Videos) != 1 || duringDelete.Videos[0].Video.ID != second.ID || duringDelete.Videos[0].Position != 2 {
		t.Fatalf("软删除成员应隐藏但保留槽位: %#v", duringDelete.Videos)
	}
	entries, err := videoService.ListTrashEntries()
	if err != nil || len(entries) != 1 {
		t.Fatalf("读取回收站条目失败: entries=%#v err=%v", entries, err)
	}
	if _, err := videoService.RestoreTrashEntry(entries[0].ID); err != nil {
		t.Fatalf("恢复带详情视频失败: %v", err)
	}
	afterRestore, err := collections.GetCollectionDetail(collection.ID)
	if err != nil {
		t.Fatalf("恢复后读取作品集失败: %v", err)
	}
	if len(afterRestore.Videos) != 2 || afterRestore.Videos[0].Video.ID != first.ID || afterRestore.Videos[0].Position != 1 || afterRestore.Videos[1].Video.ID != second.ID || afterRestore.Videos[1].Position != 2 {
		t.Fatalf("恢复后作品集顺序未原样恢复: %#v", afterRestore.Videos)
	}
	detail, err := NewVideoDetailService(people, collections).GetVideoDetails(first.ID)
	if err != nil {
		t.Fatalf("恢复后读取聚合详情失败: %v", err)
	}
	if len(detail.People) != 1 || detail.People[0].Person.ID != person.ID || detail.TechnicalMetadata == nil || len(detail.Streams) == 0 {
		t.Fatalf("恢复后人物或技术快照丢失: %#v", detail)
	}
}
