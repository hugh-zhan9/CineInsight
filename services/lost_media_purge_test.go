package services

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

func lostMustCreate(t *testing.T, values ...any) {
	t.Helper()
	for _, value := range values {
		if err := database.DB.Create(value).Error; err != nil {
			t.Fatalf("建夹具失败 %T: %v", value, err)
		}
	}
}

func lostCount(t *testing.T, table, where string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := database.DB.Table(table).Where(where, args...).Count(&count).Error; err != nil {
		t.Fatalf("统计 %s 失败: %v", table, err)
	}
	return count
}

// lostRichVideoFixture 给视频挂上清除要处理的全部引用：标签、人物、作品集、两条播放记录（日记引用其中一条）、
// 书签、两人版本组、三种忽略、人脸观测与写入记录、播放队列当前条目、规则账本、两种批量编辑明细，以及派生文件。
func lostRichVideoFixture(t *testing.T, dataDir string, video, partner models.Video) (diaryID, groupID, imageBatchItemID uint, derived []string) {
	t.Helper()
	tag := models.Tag{Name: "t-" + strconv.Itoa(int(video.ID))}
	person := models.Person{DisplayName: "p"}
	collection := models.MediaCollection{Name: "c", NormalizedName: "c"}
	lostMustCreate(t, &tag, &person, &collection)
	database.DB.Exec("INSERT INTO video_tags (video_id, tag_id) VALUES (?, ?)", video.ID, tag.ID)
	lostMustCreate(t, &models.VideoPerson{VideoID: video.ID, PersonID: person.ID},
		&models.CollectionVideo{CollectionID: collection.ID, VideoID: video.ID, Position: 1})
	event := models.PlayEvent{VideoID: video.ID, PlayedAt: time.Now(), Source: "app"}
	lostMustCreate(t, &event, &models.PlayEvent{VideoID: video.ID, PlayedAt: time.Now(), Source: "app"})
	diary := models.ViewingDiaryEntry{VideoID: &video.ID, Title: "看过", WatchedOn: "2026-10-01", SourcePlayEventID: &event.ID, Revision: 1}
	lostMustCreate(t, &diary, &models.VideoBookmark{VideoID: video.ID, VideoTitle: "v", Title: "b", Revision: 1})
	group := models.VideoVersionGroup{Title: "g", Revision: 3}
	lostMustCreate(t, &group)
	lostMustCreate(t,
		&models.VideoVersionMember{VideoID: video.ID, GroupID: group.ID, Label: "a", Position: 1},
		&models.VideoVersionMember{VideoID: partner.ID, GroupID: group.ID, Label: "b", Position: 2})
	low, high := min(video.ID, partner.ID), max(video.ID, partner.ID)
	lostMustCreate(t, &models.NearDuplicateDismissal{VideoLowID: low, VideoHighID: high},
		&models.ClipDismissal{VideoFullID: partner.ID, VideoClipID: video.ID},
		&models.VideoVersionSuggestionDismissal{VideoLowID: low, VideoHighID: high})
	cluster := models.FaceCluster{}
	lostMustCreate(t, &cluster)
	observation := models.FaceObservation{MediaKind: models.FaceMediaKindVideo, MediaID: video.ID, ClusterID: &cluster.ID, BBoxHash: "h"}
	lostMustCreate(t, &observation)
	crop := strconv.Itoa(int(observation.ID)) + ".jpg"
	database.DB.Model(&models.FaceObservation{}).Where("id = ?", observation.ID).Update("crop_path", crop)
	lostMustCreate(t, &models.FaceRelationWrite{PersonID: person.ID, MediaKind: "video", MediaID: video.ID, ClusterID: cluster.ID})
	entry := models.PlaybackQueueEntry{VideoID: video.ID, Title: "v", Position: 1}
	lostMustCreate(t, &entry)
	lostMustCreate(t, &models.PlaybackQueueState{ID: 1, Revision: 5, Player: "app", CurrentEntryID: &entry.ID, Status: "playing"})
	rule := models.AutomationRule{Name: "r", MediaKind: "video", Enabled: true, ConditionsJSON: "[]", ActionsJSON: "[]"}
	lostMustCreate(t, &rule)
	lostMustCreate(t, &models.AutomationRuleWrite{RuleID: &rule.ID, MediaKind: "video", MediaID: video.ID, Action: "add_tag", TargetID: tag.ID})
	videoBatch := models.MetadataBatchEdit{Kind: "video", SummaryJSON: "{}"}
	imageBatch := models.MetadataBatchEdit{Kind: "image", SummaryJSON: "{}"}
	lostMustCreate(t, &videoBatch, &imageBatch)
	lostMustCreate(t, &models.MetadataBatchEditItem{BatchID: videoBatch.ID, MediaID: video.ID, Field: "tags", BeforeJSON: "[]", AfterJSON: "[]"})
	imageItem := models.MetadataBatchEditItem{BatchID: imageBatch.ID, MediaID: video.ID, Field: "tags", BeforeJSON: "[]", AfterJSON: "[]"}
	lostMustCreate(t, &imageItem)
	name := strconv.Itoa(int(video.ID))
	derived = []string{
		filepath.Join(dataDir, "thumbnails", name+".jpg"),
		filepath.Join(dataDir, "thumbnails", name+".seek.f10-i500.jpg"),
		filepath.Join(dataDir, "media-details", "videos", name, "poster.jpg"),
		filepath.Join(dataDir, "subtitle-backups", name, "1.srt"),
		filepath.Join(dataDir, "faces", crop),
	}
	for _, path := range derived {
		lostTestWriteFile(t, path, 100)
	}
	return diary.ID, group.ID, imageItem.ID, derived
}

// D-LD-01/04：预览计数与实际一致；无外键表与派生文件被清掉；版本组剩一个成员时解散；观看笔记保留、日记引用置空；
// 扫描器回收站行连同条目一起清除。
func TestLostMediaPurgeCleansReferencesAndDerivedFiles(t *testing.T) {
	service, dataDir := setupLostMediaTest(t)
	root := lostTestRoot(t, LostMediaKindVideo)
	video := lostTestStaleVideo(t, filepath.Join(root, "gone.mp4"), 1000, models.StaleReasonMissingFile)
	partner := models.Video{Name: "keep.mp4", Path: filepath.Join(root, "keep.mp4"), Directory: root, Size: 5}
	lostMustCreate(t, &partner)
	diaryID, groupID, imageItemID, derived := lostRichVideoFixture(t, dataDir, video, partner)
	trashed, entry := lostTestScannerTrashVideo(t, filepath.Join(root, "trashed.mp4"), 300)

	request := LostPurgeRequest{Kind: LostMediaKindVideo, IDs: []uint{video.ID, trashed.ID}}
	preview, err := service.PreviewLostPurge(lostTestContext(), request)
	if err != nil {
		t.Fatal(err)
	}
	want := LostPurgePreview{Kind: LostMediaKindVideo, IDs: []uint{video.ID, trashed.ID}, Count: 2, Size: 1300, Skipped: map[string]int{},
		PlayEvents: 2, TagLinks: 1, PersonLinks: 1, CollectionLinks: 1, VersionGroupsDissolved: 1, Bookmarks: 1, DiaryEntries: 1, DerivedBytes: 500}
	if !reflect.DeepEqual(*preview, want) {
		t.Fatalf("预览计数不对:\n got %+v\nwant %+v", *preview, want)
	}

	queueEvents := 0
	service.SetPlaybackQueueEmitter(func(uint64) { queueEvents++ })
	request.Confirm = LostPurgeConfirmText
	result, err := service.ApplyLostPurge(lostTestContext(), request)
	if err != nil || result.Done != 2 || result.Skipped != 0 || result.Failed != 0 {
		t.Fatalf("清除结果不对: %+v err=%v", result, err)
	}
	for _, id := range []uint{video.ID, trashed.ID} {
		if n := lostCount(t, "videos", "id = ?", id); n != 0 {
			t.Fatalf("记录 %d 应被硬删", id)
		}
	}
	gone := map[string]int64{
		"play_events":                         lostCount(t, "play_events", "video_id = ?", video.ID),
		"video_tags":                          lostCount(t, "video_tags", "video_id = ?", video.ID),
		"near_duplicate_dismissals":           lostCount(t, "near_duplicate_dismissals", "1 = 1"),
		"clip_dismissals":                     lostCount(t, "clip_dismissals", "1 = 1"),
		"video_version_suggestion_dismissals": lostCount(t, "video_version_suggestion_dismissals", "1 = 1"),
		"face_observations":                   lostCount(t, "face_observations", "media_id = ?", video.ID),
		"face_relation_writes":                lostCount(t, "face_relation_writes", "media_id = ?", video.ID),
		"face_clusters":                       lostCount(t, "face_clusters", "1 = 1"),
		"playback_queue_entries":              lostCount(t, "playback_queue_entries", "1 = 1"),
		"automation_rule_writes":              lostCount(t, "automation_rule_writes", "1 = 1"),
		"metadata_batch_edit_items(video)":    lostCount(t, "metadata_batch_edit_items", "id <> ?", imageItemID),
		"video_version_groups":                lostCount(t, "video_version_groups", "id = ?", groupID),
		"video_version_members":               lostCount(t, "video_version_members", "1 = 1"),
		"video_trash_entries":                 lostCount(t, "video_trash_entries", "id = ?", entry.ID),
		"diary source_play_event_id":          lostCount(t, "viewing_diary_entries", "source_play_event_id IS NOT NULL"),
	}
	for name, count := range gone {
		if count != 0 {
			t.Fatalf("%s 应被清掉，剩 %d", name, count)
		}
	}
	if lostCount(t, "viewing_diary_entries", "id = ?", diaryID) != 1 || lostCount(t, "video_bookmarks", "video_id = ?", video.ID) != 1 {
		t.Fatalf("观看笔记应保留")
	}
	if lostCount(t, "metadata_batch_edit_items", "id = ?", imageItemID) != 1 || lostCount(t, "videos", "id = ?", partner.ID) != 1 {
		t.Fatalf("图片批次的同号明细与版本组的另一个成员不应被动")
	}
	var queue models.PlaybackQueueState
	database.DB.First(&queue, 1)
	if queue.CurrentEntryID != nil || queue.Revision != 6 || queueEvents != 1 {
		t.Fatalf("当前队列条目被删时队列应回到空闲、+1 并通知一次队列面板（%d 次）: %+v", queueEvents, queue)
	}
	for _, path := range derived {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("派生文件应被删除: %s", filepath.Base(path))
		}
	}
}

// D-LD-04：卷未挂载、文件又出现、超分进行中、已不失联各跳过一条，其余照常清除；卷未挂载的归类本身也不清除。
func TestLostMediaPurgeSkipsWithReasons(t *testing.T) {
	service, _ := setupLostMediaTest(t)
	root := lostTestRoot(t, LostMediaKindVideo)
	mount := filepath.Join(root, "mount")
	lostTestOfflineVolume(t, mount)
	purgeable := lostTestStaleVideo(t, filepath.Join(root, "a.mp4"), 1, models.StaleReasonMissingFile)
	offline := lostTestStaleVideo(t, filepath.Join(mount, "b.mp4"), 1, models.StaleReasonMissingFile)
	present := lostTestStaleVideo(t, filepath.Join(root, "c.mp4"), 1, "")
	lostTestWriteFile(t, present.Path, 1)
	enhancing := lostTestStaleVideo(t, filepath.Join(root, "d.mp4"), 1, models.StaleReasonRemovedRoot)
	lostMustCreate(t, &models.VideoEnhancementTask{VideoID: enhancing.ID, Profile: "general", Scale: 2,
		Status: models.EnhancementStatusRunning, OutputBasename: "out"})
	healthy := models.Video{Name: "e.mp4", Path: filepath.Join(root, "e.mp4"), Directory: root}
	lostMustCreate(t, &healthy)
	offlineClass := lostTestStaleVideo(t, filepath.Join(root, "f.mp4"), 1, models.StaleReasonOfflineRoot)

	ids := []uint{purgeable.ID, offline.ID, present.ID, enhancing.ID, healthy.ID, offlineClass.ID}
	result, err := service.ApplyLostPurge(lostTestContext(), LostPurgeRequest{Kind: LostMediaKindVideo, IDs: ids, Confirm: LostPurgeConfirmText})
	if err != nil || result.Done != 1 || result.Skipped != 5 || result.Failed != 0 {
		t.Fatalf("应清除 1 条、跳过 5 条: %+v err=%v", result, err)
	}
	reasons := map[uint]string{}
	for _, item := range result.Items {
		reasons[item.ID] = item.Reason
	}
	want := map[uint]string{offline.ID: LostSkipVolumeOffline, present.ID: LostSkipFilePresent, enhancing.ID: LostSkipEnhancementActive,
		healthy.ID: LostSkipNoLongerLost, offlineClass.ID: LostSkipVolumeOffline}
	for id, reason := range want {
		if reasons[id] != reason {
			t.Fatalf("条目 %d 跳过原因应为 %s，实际 %q", id, reason, reasons[id])
		}
		if lostCount(t, "videos", "id = ?", id) != 1 {
			t.Fatalf("跳过的条目 %d 不应被删", id)
		}
	}
	if lostCount(t, "videos", "id = ?", purgeable.ID) != 0 {
		t.Fatalf("其余条目照常清除")
	}
}

// 清除需要确认文本，否则整批拒绝、什么都不删。
func TestLostMediaPurgeRequiresConfirmText(t *testing.T) {
	service, _ := setupLostMediaTest(t)
	root := lostTestRoot(t, LostMediaKindVideo)
	video := lostTestStaleVideo(t, filepath.Join(root, "a.mp4"), 1, models.StaleReasonMissingFile)
	for _, confirm := range []string{"", "清除", "清除记录 "} {
		_, err := service.ApplyLostPurge(lostTestContext(), LostPurgeRequest{Kind: LostMediaKindVideo, IDs: []uint{video.ID}, Confirm: confirm})
		if LostMediaErrorCode(err) != "lost_purge_unconfirmed" {
			t.Fatalf("确认文本 %q 应被拒绝: %v", confirm, err)
		}
	}
	if lostCount(t, "videos", "id = ?", video.ID) != 1 {
		t.Fatalf("未确认时不应删除")
	}
}

// 图片清除：标签、查看记录、人脸观测与缩略图一并清掉；扫描隐藏的图片同样可清除。
func TestLostMediaPurgeImages(t *testing.T) {
	service, dataDir := setupLostMediaTest(t)
	root := lostTestRoot(t, LostMediaKindImage)
	image := lostTestImage(t, filepath.Join(root, "a.jpg"), 10, true, true)
	tag := models.Tag{Name: "img"}
	lostMustCreate(t, &tag, &models.ImageViewEvent{ImageID: image.ID, ViewedAt: time.Now(), Source: "app"},
		&models.FaceObservation{MediaKind: models.FaceMediaKindImage, MediaID: image.ID, BBoxHash: "x"})
	database.DB.Exec("INSERT INTO image_tags (image_id, tag_id) VALUES (?, ?)", image.ID, tag.ID)
	thumb := filepath.Join(dataDir, "image-thumbnails", strconv.Itoa(int(image.ID))+".view.jpg")
	lostTestWriteFile(t, thumb, 7)
	preview, err := service.PreviewLostPurge(lostTestContext(), LostPurgeRequest{Kind: LostMediaKindImage, IDs: []uint{image.ID}})
	if err != nil || preview.Count != 1 || preview.ImageViewEvents != 1 || preview.TagLinks != 1 || preview.DerivedBytes != 7 {
		t.Fatalf("图片预览不对: %+v err=%v", preview, err)
	}
	result, err := service.ApplyLostPurge(lostTestContext(), LostPurgeRequest{Kind: LostMediaKindImage, IDs: []uint{image.ID}, Confirm: LostPurgeConfirmText})
	if err != nil || result.Done != 1 {
		t.Fatalf("图片清除失败: %+v err=%v", result, err)
	}
	if lostCount(t, "images", "id = ?", image.ID)+lostCount(t, "image_tags", "1 = 1")+lostCount(t, "image_view_events", "1 = 1")+
		lostCount(t, "face_observations", "1 = 1") != 0 {
		t.Fatalf("图片及其关联应被清掉")
	}
	if _, err := os.Stat(thumb); !os.IsNotExist(err) {
		t.Fatalf("图片缩略图应被删除")
	}
}

// M2：预览做与执行相同的逐条复核，只把通过的条目列进 IDs；确认时只执行这些。预览时不可清除的条目，
// 即使确认前变成可清除，也不会被清除。
func TestLostMediaPurgePreviewListsOnlyRecheckedIDs(t *testing.T) {
	service, _ := setupLostMediaTest(t)
	root := lostTestRoot(t, LostMediaKindVideo)
	mount := filepath.Join(root, "mount")
	lostTestOfflineVolume(t, mount)
	purgeable := lostTestStaleVideo(t, filepath.Join(root, "a.mp4"), 1, models.StaleReasonMissingFile)
	present := lostTestStaleVideo(t, filepath.Join(root, "b.mp4"), 2, models.StaleReasonMissingFile)
	lostTestWriteFile(t, present.Path, 2)
	offline, _ := lostTestScannerTrashVideo(t, filepath.Join(mount, "c.mp4"), 3)

	preview, err := service.PreviewLostPurge(lostTestContext(), LostPurgeRequest{Kind: LostMediaKindVideo, IDs: []uint{purgeable.ID, present.ID, offline.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(preview.IDs, []uint{purgeable.ID}) || preview.Count != 1 || preview.Size != 1 || preview.NotPurgeable != 2 ||
		!reflect.DeepEqual(preview.Skipped, map[string]int{LostSkipFilePresent: 1, LostSkipVolumeOffline: 1}) {
		t.Fatalf("预览应只列出通过复核的条目并按原因计数跳过的: %+v", preview)
	}
	if err := os.Remove(present.Path); err != nil {
		t.Fatal(err)
	}
	result, err := service.ApplyLostPurge(lostTestContext(), LostPurgeRequest{Kind: LostMediaKindVideo, IDs: preview.IDs, Confirm: LostPurgeConfirmText})
	if err != nil || result.Done != 1 {
		t.Fatalf("应只清除预览列出的 1 条: %+v err=%v", result, err)
	}
	if lostCount(t, "videos", "id = ?", present.ID) != 1 || lostCount(t, "videos", "id = ?", offline.ID) != 1 {
		t.Fatalf("预览时不可清除的条目不应被清除")
	}
}
