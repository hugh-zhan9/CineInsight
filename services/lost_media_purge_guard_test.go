package services

import (
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// M10：复核通过之后、事务之前别的入口（例如播放成功）清掉了失效——事务里的「仍是失联」复核让这条跳过。
func TestLostMediaPurgeRechecksInsideTransaction(t *testing.T) {
	service, _ := setupLostMediaTest(t)
	root := lostTestRoot(t, LostMediaKindVideo)
	video := lostTestStaleVideo(t, filepath.Join(root, "a.mp4"), 1, models.StaleReasonMissingFile)
	lostPurgeBeforeTxHook = func(id uint) {
		database.DB.Model(&models.Video{}).Where("id = ?", id).Updates(map[string]any{"is_stale": false, "stale_reason": ""})
	}
	t.Cleanup(func() { lostPurgeBeforeTxHook = nil })
	result, err := service.ApplyLostPurge(lostTestContext(), LostPurgeRequest{Kind: LostMediaKindVideo, IDs: []uint{video.ID}, Confirm: LostPurgeConfirmText})
	if err != nil || result.Done != 0 || result.Skipped != 1 || result.Items[0].Reason != LostSkipNoLongerLost {
		t.Fatalf("事务里应复核到已不失联并跳过: %+v err=%v", result, err)
	}
	if lostCount(t, "videos", "id = ?", video.ID) != 1 {
		t.Fatalf("已不失联的条目不应被删")
	}
}

// M10：三人版本组清掉一个后剩两人，组保留、revision +1、成员只少这一个。
func TestLostMediaPurgeShrinksVersionGroup(t *testing.T) {
	service, _ := setupLostMediaTest(t)
	root := lostTestRoot(t, LostMediaKindVideo)
	gone := lostTestStaleVideo(t, filepath.Join(root, "gone.mp4"), 1, models.StaleReasonMissingFile)
	keepA := models.Video{Name: "a.mp4", Path: filepath.Join(root, "a.mp4"), Directory: root}
	keepB := models.Video{Name: "b.mp4", Path: filepath.Join(root, "b.mp4"), Directory: root}
	group := models.VideoVersionGroup{Title: "g", Revision: 4}
	lostMustCreate(t, &keepA, &keepB, &group)
	lostMustCreate(t,
		&models.VideoVersionMember{VideoID: gone.ID, GroupID: group.ID, Label: "x", Position: 1},
		&models.VideoVersionMember{VideoID: keepA.ID, GroupID: group.ID, Label: "a", Position: 2},
		&models.VideoVersionMember{VideoID: keepB.ID, GroupID: group.ID, Label: "b", Position: 3})
	preview, err := service.PreviewLostPurge(lostTestContext(), LostPurgeRequest{Kind: LostMediaKindVideo, IDs: []uint{gone.ID}})
	if err != nil || preview.VersionGroupsShrunk != 1 || preview.VersionGroupsDissolved != 0 {
		t.Fatalf("预览应报缩小 1 个组: %+v err=%v", preview, err)
	}
	result, err := service.ApplyLostPurge(lostTestContext(), LostPurgeRequest{Kind: LostMediaKindVideo, IDs: preview.IDs, Confirm: LostPurgeConfirmText})
	if err != nil || result.Done != 1 {
		t.Fatalf("清除失败: %+v err=%v", result, err)
	}
	var after models.VideoVersionGroup
	if err := database.DB.First(&after, group.ID).Error; err != nil || after.Revision != 5 {
		t.Fatalf("组应保留且 revision +1: %+v err=%v", after, err)
	}
	if lostCount(t, "video_version_members", "group_id = ?", group.ID) != 2 {
		t.Fatalf("组里应剩两个成员")
	}
}

// M10：卷未挂载的扫描器回收站行与用户自己删进回收站的条目，执行时都跳过。
func TestLostMediaPurgeSkipsOfflineScannerTrashAndUserTrash(t *testing.T) {
	service, _ := setupLostMediaTest(t)
	root := lostTestRoot(t, LostMediaKindVideo)
	mount := filepath.Join(root, "mount")
	lostTestOfflineVolume(t, mount)
	offline, _ := lostTestScannerTrashVideo(t, filepath.Join(mount, "o.mp4"), 1)
	userTrashed := models.Video{Name: "u.mp4", Path: filepath.Join(root, "u.mp4"), Directory: root}
	lostMustCreate(t, &userTrashed)
	database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", userTrashed.ID).
		Updates(map[string]any{"deleted_at": time.Now(), "deleted_by": "user"})
	result, err := service.ApplyLostPurge(lostTestContext(), LostPurgeRequest{Kind: LostMediaKindVideo,
		IDs: []uint{offline.ID, userTrashed.ID}, Confirm: LostPurgeConfirmText})
	if err != nil || result.Done != 0 || result.Skipped != 2 {
		t.Fatalf("两条都应跳过: %+v err=%v", result, err)
	}
	reasons := map[uint]string{}
	for _, item := range result.Items {
		reasons[item.ID] = item.Reason
	}
	if reasons[offline.ID] != LostSkipVolumeOffline || reasons[userTrashed.ID] != LostSkipNoLongerLost {
		t.Fatalf("跳过原因不对: %+v", reasons)
	}
	if lostCount(t, "videos", "id IN ?", []uint{offline.ID, userTrashed.ID}) != 2 {
		t.Fatalf("跳过的条目不应被删")
	}
}

// M4/M6：排在扫描（路径读锁）后面等锁时取消，这一条报「已取消、未处理」，不算失败，也不动它。
func TestLostMediaPurgeCancelWhileWaitingForLock(t *testing.T) {
	service, _ := setupLostMediaTest(t)
	root := lostTestRoot(t, LostMediaKindVideo)
	video := lostTestStaleVideo(t, filepath.Join(root, "a.mp4"), 1, models.StaleReasonMissingFile)
	unlockScan, err := rLockLibraryPaths()
	if err != nil {
		t.Fatal(err)
	}
	defer unlockScan()
	done := make(chan *LostMediaBatchResult, 1)
	go func() {
		result, _ := service.ApplyLostPurge(lostTestContext(), LostPurgeRequest{Kind: LostMediaKindVideo, IDs: []uint{video.ID}, Confirm: LostPurgeConfirmText})
		done <- result
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !service.Status().Running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	service.Cancel()
	select {
	case result := <-done:
		if result == nil || !result.Cancelled || result.Failed != 0 || result.Done != 0 || len(result.Items) != 1 ||
			result.Items[0].Outcome != LostOutcomeCancelled {
			t.Fatalf("等锁时取消应报已取消、未处理: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("等锁时取消没有返回")
	}
	if lostCount(t, "videos", "id = ?", video.ID) != 1 {
		t.Fatalf("取消的条目不应被删")
	}
}

// M4：图片排在图片扫描（imagePathMutationMu 读锁）后面等锁时同样可以取消。
func TestLostMediaImageRelinkCancelWhileWaitingForLock(t *testing.T) {
	service, _ := setupLostMediaTest(t)
	root := lostTestRoot(t, LostMediaKindImage)
	image := lostTestImage(t, filepath.Join(root, "a.jpg"), 1, true, false)
	imagePathMutationMu.RLock()
	defer imagePathMutationMu.RUnlock()
	done := make(chan *LostMediaBatchResult, 1)
	go func() {
		result, _ := service.ApplyLostRelink(lostTestContext(), LostRelinkApplyRequest{Kind: LostMediaKindImage,
			Pairs: []LostRelinkPair{{ID: image.ID, NewPath: filepath.Join(root, "b.jpg")}}})
		done <- result
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !service.Status().Running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	service.Cancel()
	select {
	case result := <-done:
		if result == nil || !result.Cancelled || result.Failed != 0 || len(result.Items) != 1 || result.Items[0].Outcome != LostOutcomeCancelled {
			t.Fatalf("等图片锁时取消应报已取消、未处理: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("等图片锁时取消没有返回")
	}
}
