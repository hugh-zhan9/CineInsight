package services

import (
	"path/filepath"
	"testing"
	"video-master/database"
	"video-master/models"
)

func lostCandidatesByID(preview *LostRelinkPreview) map[uint]LostRelinkCandidate {
	byID := map[uint]LostRelinkCandidate{}
	for _, item := range preview.Items {
		byID[item.ID] = item
	}
	return byID
}

func lostSelectedPairs(preview *LostRelinkPreview) []LostRelinkPair {
	pairs := []LostRelinkPair{}
	for _, item := range preview.Items {
		if item.Selected {
			pairs = append(pairs, LostRelinkPair{ID: item.ID, NewPath: item.NewPath})
		}
	}
	return pairs
}

// D-LD-03 目录搬家：同相对路径同大小 exact，大小不同 size_mismatch 不可勾，被占用的不可执行；
// 执行后 id 不变、失效清除；扫描器回收站行找回后 id 不变、回收站条目退役。
func TestLostMediaRelinkMovePairsAndApplies(t *testing.T) {
	service, _ := setupLostMediaTest(t)
	oldRoot := lostTestRoot(t, LostMediaKindVideo)
	newRoot := lostTestRoot(t, LostMediaKindVideo)
	exact := lostTestStaleVideo(t, filepath.Join(oldRoot, "sub", "a.mp4"), 10, models.StaleReasonMissingFile)
	lostTestWriteFile(t, filepath.Join(newRoot, "sub", "a.mp4"), 10)
	mismatch := lostTestStaleVideo(t, filepath.Join(oldRoot, "b.mp4"), 10, models.StaleReasonRemovedRoot)
	lostTestWriteFile(t, filepath.Join(newRoot, "b.mp4"), 11)
	trashed, entry := lostTestScannerTrashVideo(t, filepath.Join(oldRoot, "c.mp4"), 7)
	lostTestWriteFile(t, filepath.Join(newRoot, "c.mp4"), 7)
	occupied := lostTestStaleVideo(t, filepath.Join(oldRoot, "d.mp4"), 5, "")
	lostTestWriteFile(t, filepath.Join(newRoot, "d.mp4"), 5)
	database.DB.Create(&models.Video{Name: "d.mp4", Path: filepath.Join(newRoot, "d.mp4"), Directory: newRoot, Size: 5})
	lostTestStaleVideo(t, filepath.Join(oldRoot, "e.mp4"), 5, models.StaleReasonMissingFile)
	offline := lostTestStaleVideo(t, filepath.Join(oldRoot, "f.mp4"), 3, models.StaleReasonOfflineRoot)
	lostTestWriteFile(t, filepath.Join(newRoot, "f.mp4"), 3)

	preview, err := service.PreviewLostRelinkMove(lostTestContext(), LostRelinkMoveRequest{Kind: LostMediaKindVideo, OldDirectory: oldRoot, NewDirectory: newRoot})
	if err != nil {
		t.Fatal(err)
	}
	byID := lostCandidatesByID(preview)
	if c := byID[exact.ID]; c.Match != LostMatchExact || !c.Selected || c.NewPath != filepath.Join(newRoot, "sub", "a.mp4") {
		t.Fatalf("同相对路径同大小应为 exact 且默认勾选: %+v", c)
	}
	if c := byID[mismatch.ID]; c.Match != LostMatchSizeMismatch || c.Selectable {
		t.Fatalf("大小不同应为 size_mismatch 且不可勾: %+v", c)
	}
	if c := byID[occupied.ID]; !c.Occupied || c.Selectable {
		t.Fatalf("新路径被活动记录占用时不可执行: %+v", c)
	}
	if _, listed := byID[offline.ID]; listed {
		t.Fatalf("卷未挂载的条目不给批量找回")
	}
	if preview.Unmatched != 1 || len(preview.Items) != 4 {
		t.Fatalf("应列出 4 条、1 条没找到: %+v", preview)
	}

	result, err := service.ApplyLostRelink(lostTestContext(), LostRelinkApplyRequest{Kind: LostMediaKindVideo, Pairs: lostSelectedPairs(preview)})
	if err != nil || result.Done != 2 || result.Skipped != 0 || result.Failed != 0 {
		t.Fatalf("执行结果不对: %+v err=%v", result, err)
	}
	var moved models.Video
	database.DB.First(&moved, exact.ID)
	if moved.Path != filepath.Join(newRoot, "sub", "a.mp4") || moved.IsStale || moved.StaleReason != "" {
		t.Fatalf("失效行找回后应改路径并清除失效: %+v", moved)
	}
	var restored models.Video
	if err := database.DB.First(&restored, trashed.ID).Error; err != nil || restored.Path != filepath.Join(newRoot, "c.mp4") || restored.IsStale {
		t.Fatalf("扫描器回收站行应原 id 恢复到新路径: %+v err=%v", restored, err)
	}
	var entries int64
	database.DB.Model(&models.VideoTrashEntry{}).Where("id = ?", entry.ID).Count(&entries)
	if entries != 0 {
		t.Fatalf("回收站条目应退役")
	}
}

// D-LD-03：新位置不在任何扫描根内时候选不可执行；执行时复核同样拒绝。
func TestLostMediaRelinkRejectsOutsideRoots(t *testing.T) {
	service, _ := setupLostMediaTest(t)
	oldRoot := lostTestRoot(t, LostMediaKindImage)
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	image := lostTestImage(t, filepath.Join(oldRoot, "p.jpg"), 4, true, true)
	lostTestWriteFile(t, filepath.Join(outside, "p.jpg"), 4)
	preview, err := service.PreviewLostRelinkMove(lostTestContext(), LostRelinkMoveRequest{Kind: LostMediaKindImage, OldDirectory: oldRoot, NewDirectory: outside})
	if err != nil {
		t.Fatal(err)
	}
	c := lostCandidatesByID(preview)[image.ID]
	if c.Match != LostMatchExact || !c.OutsideRoots || c.Selectable || c.Selected {
		t.Fatalf("新路径不在扫描根内应标 outside_roots 且不可执行: %+v", c)
	}
	result, err := service.ApplyLostRelink(lostTestContext(), LostRelinkApplyRequest{Kind: LostMediaKindImage,
		Pairs: []LostRelinkPair{{ID: image.ID, NewPath: c.NewPath}}})
	if err != nil || result.Skipped != 1 || result.Items[0].Reason != LostSkipOutsideRoots {
		t.Fatalf("执行时应复核并跳过: %+v err=%v", result, err)
	}
}

// D-LD-03 在文件夹里找：同名同大小唯一命中、仅大小命中、多候选、一个文件被两条命中，四种情况。
func TestLostMediaRelinkSearchFourCases(t *testing.T) {
	service, _ := setupLostMediaTest(t)
	oldRoot := lostTestRoot(t, LostMediaKindVideo)
	searchRoot := lostTestRoot(t, LostMediaKindVideo)
	nameSize := lostTestStaleVideo(t, filepath.Join(oldRoot, "movie.mp4"), 100, models.StaleReasonMissingFile)
	lostTestWriteFile(t, filepath.Join(searchRoot, "deep", "movie.mp4"), 100)
	lostTestWriteFile(t, filepath.Join(searchRoot, "movie.mp4"), 99) // 同名不同大小，不算
	sizeOnly := lostTestStaleVideo(t, filepath.Join(oldRoot, "old-name.mp4"), 200, models.StaleReasonMissingFile)
	lostTestWriteFile(t, filepath.Join(searchRoot, "renamed.mp4"), 200)
	ambiguous := lostTestStaleVideo(t, filepath.Join(oldRoot, "twin.mp4"), 300, "")
	lostTestWriteFile(t, filepath.Join(searchRoot, "x", "twin.mp4"), 300)
	lostTestWriteFile(t, filepath.Join(searchRoot, "y", "twin.mp4"), 300)
	shared1 := lostTestStaleVideo(t, filepath.Join(oldRoot, "one.mp4"), 400, models.StaleReasonMissingFile)
	shared2, _ := lostTestScannerTrashVideo(t, filepath.Join(oldRoot, "two.mp4"), 400)
	lostTestWriteFile(t, filepath.Join(searchRoot, "shared.mp4"), 400)
	lostTestStaleVideo(t, filepath.Join(oldRoot, "none.mp4"), 500, models.StaleReasonMissingFile)

	preview, err := service.PreviewLostRelinkSearch(lostTestContext(), LostRelinkSearchRequest{Kind: LostMediaKindVideo, Directory: searchRoot})
	if err != nil {
		t.Fatal(err)
	}
	byID := lostCandidatesByID(preview)
	if c := byID[nameSize.ID]; c.Match != LostMatchNameSize || !c.Selected || c.NewPath != filepath.Join(searchRoot, "deep", "movie.mp4") {
		t.Fatalf("同名同大小唯一命中应为 name_size 且默认勾选: %+v", c)
	}
	if c := byID[sizeOnly.ID]; c.Match != LostMatchSizeOnly || !c.Selectable || c.Selected {
		t.Fatalf("仅大小 + 扩展名唯一命中应为 size_only、可勾但默认不勾: %+v", c)
	}
	if c := byID[ambiguous.ID]; c.Match != LostMatchAmbiguous || c.NewPath != "" || len(c.Candidates) != 2 || c.Selectable {
		t.Fatalf("多个候选应为 ambiguous 并列出候选: %+v", c)
	}
	for _, id := range []uint{shared1.ID, shared2.ID} {
		if c := byID[id]; c.Match != LostMatchAmbiguous || c.Selectable {
			t.Fatalf("同一个文件被两条记录命中时都应降为 ambiguous: %+v", c)
		}
	}
	if preview.Unmatched != 1 {
		t.Fatalf("一个候选都没有的应计入 Unmatched: %+v", preview)
	}

	limited, err := service.PreviewLostRelinkSearch(lostTestContext(), LostRelinkSearchRequest{Kind: LostMediaKindVideo, Directory: searchRoot, ItemIDs: []uint{shared1.ID}})
	if err != nil || len(limited.Items) != 1 || limited.Items[0].Match != LostMatchSizeOnly {
		t.Fatalf("只匹配给定条目时不再与别的条目抢同一个文件: %+v err=%v", limited, err)
	}
}

// D-LD-03：图片扫描器软删的行在文件夹里找到后原 id 恢复、失效清除。
func TestLostMediaRelinkSearchRestoresHiddenImage(t *testing.T) {
	service, _ := setupLostMediaTest(t)
	root := lostTestRoot(t, LostMediaKindImage)
	hidden := lostTestImage(t, filepath.Join(root, "old", "pic.jpg"), 8, true, true)
	lostTestWriteFile(t, filepath.Join(root, "new", "pic.jpg"), 8)
	renamed := lostTestImage(t, filepath.Join(root, "old", "before.jpg"), 9, true, false)
	lostTestWriteFile(t, filepath.Join(root, "new", "after.jpg"), 9)
	preview, err := service.PreviewLostRelinkSearch(lostTestContext(), LostRelinkSearchRequest{Kind: LostMediaKindImage, Directory: root})
	if err != nil {
		t.Fatal(err)
	}
	pairs := lostSelectedPairs(preview)
	if c := lostCandidatesByID(preview)[renamed.ID]; c.Match == LostMatchSizeOnly && c.Selectable {
		pairs = append(pairs, LostRelinkPair{ID: renamed.ID, NewPath: c.NewPath}) // 「可能」由用户手动勾上
	}
	result, err := service.ApplyLostRelink(lostTestContext(), LostRelinkApplyRequest{Kind: LostMediaKindImage, Pairs: pairs})
	if err != nil || result.Done != 2 {
		t.Fatalf("应找回 2 条: %+v err=%v", result, err)
	}
	var moved models.Image
	if database.DB.First(&moved, renamed.ID); moved.Name != "after.jpg" || moved.IsStale {
		t.Fatalf("改了名的失效图片找回后名称应跟着新路径: %+v", moved)
	}
	var image models.Image
	if err := database.DB.First(&image, hidden.ID).Error; err != nil || image.IsStale || image.Path != filepath.Join(root, "new", "pic.jpg") {
		t.Fatalf("图片应原 id 恢复到新路径: %+v err=%v", image, err)
	}
}

// D-LD-03：执行时逐条复核，预览之后变了的跳过并说明原因，不影响其余条目。
func TestLostMediaRelinkApplyRechecksEachPair(t *testing.T) {
	service, _ := setupLostMediaTest(t)
	root := lostTestRoot(t, LostMediaKindVideo)
	ok := lostTestStaleVideo(t, filepath.Join(root, "old", "ok.mp4"), 4, models.StaleReasonMissingFile)
	lostTestWriteFile(t, filepath.Join(root, "new", "ok.mp4"), 4)
	resized := lostTestStaleVideo(t, filepath.Join(root, "old", "resized.mp4"), 4, models.StaleReasonMissingFile)
	lostTestWriteFile(t, filepath.Join(root, "new", "resized.mp4"), 5)
	vanished := lostTestStaleVideo(t, filepath.Join(root, "old", "vanished.mp4"), 4, models.StaleReasonMissingFile)
	taken := lostTestStaleVideo(t, filepath.Join(root, "old", "taken.mp4"), 4, models.StaleReasonMissingFile)
	lostTestWriteFile(t, filepath.Join(root, "new", "taken.mp4"), 4)
	lostMustCreate(t, &models.Video{Name: "taken.mp4", Path: filepath.Join(root, "new", "taken.mp4"), Directory: filepath.Join(root, "new"), Size: 4})
	healthy := models.Video{Name: "h.mp4", Path: filepath.Join(root, "h.mp4"), Directory: root, Size: 4}
	lostMustCreate(t, &healthy)
	lostTestWriteFile(t, filepath.Join(root, "new", "h.mp4"), 4)

	pairs := []LostRelinkPair{
		{ID: ok.ID, NewPath: filepath.Join(root, "new", "ok.mp4")},
		{ID: resized.ID, NewPath: filepath.Join(root, "new", "resized.mp4")},
		{ID: vanished.ID, NewPath: filepath.Join(root, "new", "vanished.mp4")},
		{ID: taken.ID, NewPath: filepath.Join(root, "new", "taken.mp4")},
		{ID: healthy.ID, NewPath: filepath.Join(root, "new", "h.mp4")},
	}
	result, err := service.ApplyLostRelink(lostTestContext(), LostRelinkApplyRequest{Kind: LostMediaKindVideo, Pairs: pairs})
	if err != nil || result.Done != 1 || result.Skipped != 4 {
		t.Fatalf("应找回 1 条、跳过 4 条: %+v err=%v", result, err)
	}
	want := map[uint]string{resized.ID: LostSkipSizeMismatch, vanished.ID: LostSkipTargetMissing, taken.ID: LostSkipOccupied, healthy.ID: LostSkipNoLongerLost}
	for _, item := range result.Items {
		if want[item.ID] != item.Reason {
			t.Fatalf("条目 %d 跳过原因应为 %s，实际 %s", item.ID, want[item.ID], item.Reason)
		}
	}
	var unchanged models.Video
	database.DB.First(&unchanged, resized.ID)
	if !unchanged.IsStale || unchanged.Path != resized.Path {
		t.Fatalf("跳过的条目不应被改: %+v", unchanged)
	}
}
