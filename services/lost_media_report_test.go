package services

import (
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// D-LD-02：两种存法、两类媒体、六种归类都出现在报告里；卷未挂载与读取出错不可清除；用户删除的不算。
func TestLostMediaReportClassifiesVideos(t *testing.T) {
	service, _ := setupLostMediaTest(t)
	root := lostTestRoot(t, LostMediaKindVideo)
	reasons := map[string]string{
		models.StaleReasonOfflineRoot:  LostReasonOffline,
		models.StaleReasonReadError:    LostReasonReadError,
		models.StaleReasonMissingFile:  LostReasonMissing,
		models.StaleReasonRemovedRoot:  LostReasonRemovedRoot,
		models.StaleReasonOutsideRoots: LostReasonOutsideRoots,
		"":                             LostReasonUnknown,
	}
	want := map[uint]string{}
	for stale, class := range reasons {
		video := lostTestStaleVideo(t, filepath.Join(root, "a", "v-"+class+".mp4"), 10, stale)
		want[video.ID] = class
	}
	trashed, _ := lostTestScannerTrashVideo(t, filepath.Join(root, "b", "gone.mp4"), 20)
	want[trashed.ID] = LostReasonMissing
	// 用户删除的软删行与正常行都不算失联。
	userDeleted := models.Video{Name: "u.mp4", Path: filepath.Join(root, "u.mp4"), Directory: root}
	database.DB.Create(&userDeleted)
	database.DB.Unscoped().Model(&models.Video{}).Where("id = ?", userDeleted.ID).
		Updates(map[string]any{"deleted_at": time.Now(), "deleted_by": "user"})
	database.DB.Create(&models.Video{Name: "ok.mp4", Path: filepath.Join(root, "ok.mp4"), Directory: root})

	report, err := service.GetLostMediaReport(lostTestContext(), LostMediaKindVideo)
	if err != nil {
		t.Fatal(err)
	}
	if report.Total != len(want) || report.Purgeable != len(want)-2 {
		t.Fatalf("总数/可清除数不对: %+v", report)
	}
	items := lostReportItems(t, service, report)
	for id, class := range want {
		item, ok := items[id]
		if !ok || item.Reason != class {
			t.Fatalf("条目 %d 归类应为 %s，实际 %+v", id, class, item)
		}
		if item.Purgeable != (class != LostReasonOffline && class != LostReasonReadError) {
			t.Fatalf("条目 %d（%s）可清除标记不对", id, class)
		}
	}
	if items[trashed.ID].Source != LostSourceScannerTrash {
		t.Fatalf("扫描器回收站行的来源不对: %+v", items[trashed.ID])
	}
	if len(report.Roots) != 1 || report.Roots[0].State != lostRootActive || len(report.Roots[0].Directories) != 2 {
		t.Fatalf("应按根 → 目录分成一个根两个目录: %+v", report.Roots)
	}
}

// D-LD-02：图片的两种存法，原因按 image_visibility 现算；已移除的根从路径前缀推断，推不出的归「其他位置」。
func TestLostMediaReportClassifiesImagesAndGroupsRoots(t *testing.T) {
	service, _ := setupLostMediaTest(t)
	root := lostTestRoot(t, LostMediaKindImage)
	offlineRoot := filepath.Join(t.TempDir(), "unplugged")
	database.DB.Create(&models.ImageDirectory{Path: offlineRoot})
	removedRoot := filepath.Join(t.TempDir(), "removed")
	removedDir := models.ImageDirectory{Path: removedRoot, Alias: "旧图库"}
	database.DB.Create(&removedDir)
	database.DB.Delete(&removedDir)

	missing := lostTestImage(t, filepath.Join(root, "x", "m.jpg"), 5, true, false)
	offline := lostTestImage(t, filepath.Join(offlineRoot, "o.jpg"), 5, true, false)
	hidden := lostTestImage(t, filepath.Join(root, "x", "h.jpg"), 5, true, true)
	removed := lostTestImage(t, filepath.Join(removedRoot, "r.jpg"), 5, true, false)
	elsewhere := lostTestImage(t, "/nowhere/else/e.jpg", 5, true, false)
	lostTestImage(t, filepath.Join(root, "fine.jpg"), 5, false, false)
	userDeleted := lostTestImage(t, filepath.Join(root, "user.jpg"), 5, true, false)
	database.DB.Unscoped().Model(&models.Image{}).Where("id = ?", userDeleted.ID).
		Updates(map[string]any{"deleted_at": time.Now(), "deleted_by": "user"})

	report, err := service.GetLostMediaReport(lostTestContext(), LostMediaKindImage)
	if err != nil {
		t.Fatal(err)
	}
	items := lostReportItems(t, service, report)
	expect := map[uint]string{missing.ID: LostReasonMissing, offline.ID: LostReasonOffline, hidden.ID: LostReasonMissing,
		removed.ID: LostReasonRemovedRoot, elsewhere.ID: LostReasonRemovedRoot}
	if report.Total != len(expect) {
		t.Fatalf("总数应为 %d: %+v", len(expect), report)
	}
	for id, class := range expect {
		if items[id].Reason != class {
			t.Fatalf("图片 %d 归类应为 %s，实际 %+v", id, class, items[id])
		}
	}
	if items[offline.ID].Purgeable || !items[hidden.ID].Purgeable || items[hidden.ID].Source != LostSourceScannerTrash {
		t.Fatalf("离线不可清除、扫描隐藏可清除: %+v", items)
	}
	states := map[string]string{}
	for _, group := range report.Roots {
		states[group.State] = group.Label
	}
	if states[lostRootRemoved] != "旧图库" || states[lostRootOther] != "其他位置" || len(report.Roots) != 4 {
		t.Fatalf("根分组不对: %+v", report.Roots)
	}
}
