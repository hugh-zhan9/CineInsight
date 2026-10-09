package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"video-master/database"
	"video-master/models"
)

// fixSMoveAway 模拟「移到系统废纸篓」：文件离开原路径。
func fixSMoveAway(t *testing.T, path string) {
	t.Helper()
	if err := os.Rename(path, filepath.Join(t.TempDir(), filepath.Base(path))); err != nil {
		t.Fatalf("移走文件失败: %v", err)
	}
}

// 修复 S：组里有已移到废纸篓的成员时，「移出本组」照常为 memberID 与它写忽略。
func TestCleanupIMG07DismissMemberAcceptsTrashedMemberFixS(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	a := p016Video(t, root, "a.mp4", "fixs-a", 1920, 1080)
	b := p016Video(t, root, "b.mp4", "fixs-b-x", 1920, 1080)
	c := p016Video(t, root, "c.mp4", "fixs-c-xx", 1920, 1080)
	for _, video := range []models.Video{a, b, c} {
		seedPerceptualHashRow(t, video, "0000000000000000")
	}
	analysis := mustAnalyzeCleanup(t)
	if len(analysis.NearDuplicateGroups) != 1 || len(p016GroupIDs(analysis.NearDuplicateGroups[0])) != 3 {
		t.Fatalf("三个视频应成一组: %+v", analysis.NearDuplicateGroups)
	}
	fixSMoveAway(t, b.Path)
	if err := database.DB.Delete(&b).Error; err != nil {
		t.Fatal(err)
	}
	if err := DismissNearDuplicateMember([]uint{a.ID, b.ID, c.ID}, a.ID); err != nil {
		t.Fatalf("组里有已软删成员时移出应成功: %v", err)
	}
	var rows []models.NearDuplicateDismissal
	database.DB.Order("video_low_id, video_high_id").Find(&rows)
	if len(rows) != 2 || rows[0].VideoLowID != a.ID || rows[0].VideoHighID != b.ID || rows[1].VideoHighID != c.ID {
		t.Fatalf("应写 a-b、a-c 两对: %+v", rows)
	}
	// b 的文件已不在原处：它这一侧记空指纹；a、c 记真实指纹。
	if rows[0].FingerprintA == "" || rows[0].FingerprintB != "" || rows[1].FingerprintA == "" || rows[1].FingerprintB == "" {
		t.Fatalf("指纹记录不符: %+v", rows)
	}
	cached := &CleanupService{status: CleanupStatus{Completed: true, Analysis: analysis}}
	for _, got := range []*CleanupAnalysis{cached.Status().Analysis, mustAnalyzeCleanup(t)} {
		for _, group := range got.NearDuplicateGroups {
			if containsUintID(p016GroupIDs(group), a.ID) {
				t.Fatalf("a 不应再与 b、c 成组: %+v", got.NearDuplicateGroups)
			}
		}
	}
}

// 修复 S：要移出的这一份本身已软删或已硬删时报错，不写任何记录。
func TestCleanupIMG07DismissMemberRejectsDeletedTargetFixS(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	a := p016Video(t, root, "a.mp4", "fixs-a", 1920, 1080)
	b := p016Video(t, root, "b.mp4", "fixs-b-x", 1920, 1080)
	c := p016Video(t, root, "c.mp4", "fixs-c-xx", 1920, 1080)
	if err := database.DB.Delete(&c).Error; err != nil {
		t.Fatal(err)
	}
	ids := []uint{a.ID, b.ID, c.ID}
	if err := DismissNearDuplicateMember(ids, c.ID); err == nil || !strings.Contains(err.Error(), "已不在片库中") {
		t.Fatalf("移出已软删的一份应报错: %v", err)
	}
	if err := database.DB.Unscoped().Delete(&c).Error; err != nil {
		t.Fatal(err)
	}
	if err := DismissNearDuplicateMember(ids, c.ID); err == nil || !strings.Contains(err.Error(), "已不在片库中") {
		t.Fatalf("移出已硬删的一份应报错: %v", err)
	}
	var count int64
	database.DB.Model(&models.NearDuplicateDismissal{}).Count(&count)
	if count != 0 {
		t.Fatalf("报错时不应写忽略记录: %d", count)
	}
}

// 修复 S：组里某个 ID 的记录已硬删时跳过这一对，其余配对照常写；跳完没有可写的配对时报错。
func TestCleanupIMG07DismissMemberSkipsHardDeletedFixS(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	a := p016Video(t, root, "a.mp4", "fixs-a", 1920, 1080)
	b := p016Video(t, root, "b.mp4", "fixs-b-x", 1920, 1080)
	c := p016Video(t, root, "c.mp4", "fixs-c-xx", 1920, 1080)
	if err := database.DB.Unscoped().Delete(&b).Error; err != nil {
		t.Fatal(err)
	}
	ids := []uint{a.ID, b.ID, c.ID}
	if err := DismissNearDuplicateMember(ids, a.ID); err != nil {
		t.Fatalf("组里有已硬删成员时应跳过它: %v", err)
	}
	var rows []models.NearDuplicateDismissal
	database.DB.Find(&rows)
	if len(rows) != 1 || rows[0].VideoLowID != a.ID || rows[0].VideoHighID != c.ID ||
		rows[0].FingerprintA == "" || rows[0].FingerprintB == "" {
		t.Fatalf("只应写 a-c 一对且带指纹: %+v", rows)
	}
	if err := database.DB.Unscoped().Delete(&c).Error; err != nil {
		t.Fatal(err)
	}
	if err := DismissNearDuplicateMember(ids, a.ID); err == nil || !strings.Contains(err.Error(), "记录都已不存在") {
		t.Fatalf("没有可写的配对应报错: %v", err)
	}
	var count int64
	database.DB.Model(&models.NearDuplicateDismissal{}).Count(&count)
	if count != 1 {
		t.Fatalf("报错时不应多写记录: %d", count)
	}
}

func fixSImages(t *testing.T) (*models.Image, *models.Image, *models.Image) {
	t.Helper()
	dir := t.TempDir()
	a := imageCleanupCreateImage(t, filepath.Join(dir, "a.jpg"), []byte(strings.Repeat("a", 300)), "abcd000000000000", 100, 100)
	b := imageCleanupCreateImage(t, filepath.Join(dir, "b.jpg"), []byte(strings.Repeat("b", 301)), "abcd000000000000", 100, 100)
	c := imageCleanupCreateImage(t, filepath.Join(dir, "c.jpg"), []byte(strings.Repeat("c", 302)), "abcd000000000000", 100, 100)
	return a, b, c
}

// 修复 S（图片）：组里有已移到废纸篓的成员时，「移出本组」照常为 memberID 与它写忽略。
func TestImageCleanupIMG07DismissMemberAcceptsTrashedMemberFixS(t *testing.T) {
	setupImageServiceTestDB(t)
	a, b, c := fixSImages(t)
	svc := newImageCleanupTestService()
	analysis, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil || len(analysis.NearDuplicateGroups) != 1 || len(imageCleanupGroupIDs(analysis.NearDuplicateGroups[0])) != 3 {
		t.Fatalf("三张图应成一组: %+v err=%v", analysis, err)
	}
	fixSMoveAway(t, b.Path)
	if err := database.DB.Delete(&models.Image{}, b.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := DismissImageNearDuplicateMember([]uint{a.ID, b.ID, c.ID}, a.ID); err != nil {
		t.Fatalf("组里有已软删成员时移出应成功: %v", err)
	}
	var rows []models.ImageNearDuplicateDismissal
	database.DB.Order("image_low_id, image_high_id").Find(&rows)
	if len(rows) != 2 || rows[0].ImageLowID != a.ID || rows[0].ImageHighID != b.ID || rows[1].ImageHighID != c.ID {
		t.Fatalf("应写 a-b、a-c 两对: %+v", rows)
	}
	if rows[0].FingerprintA == "" || rows[0].FingerprintB != "" || rows[1].FingerprintA == "" || rows[1].FingerprintB == "" {
		t.Fatalf("指纹记录不符: %+v", rows)
	}
	analysis, err = svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range analysis.NearDuplicateGroups {
		if containsUintID(imageCleanupGroupIDs(group), a.ID) {
			t.Fatalf("a 不应再与 b、c 成组: %+v", analysis.NearDuplicateGroups)
		}
	}
}

// 修复 S（图片）：要移出的这一份本身已软删或已硬删时报错，不写任何记录。
func TestImageCleanupIMG07DismissMemberRejectsDeletedTargetFixS(t *testing.T) {
	setupImageServiceTestDB(t)
	a, b, c := fixSImages(t)
	if err := database.DB.Delete(&models.Image{}, c.ID).Error; err != nil {
		t.Fatal(err)
	}
	ids := []uint{a.ID, b.ID, c.ID}
	if err := DismissImageNearDuplicateMember(ids, c.ID); err == nil || !strings.Contains(err.Error(), "已不在图片库中") {
		t.Fatalf("移出已软删的一张应报错: %v", err)
	}
	if err := database.DB.Unscoped().Delete(&models.Image{}, c.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := DismissImageNearDuplicateMember(ids, c.ID); err == nil || !strings.Contains(err.Error(), "已不在图片库中") {
		t.Fatalf("移出已硬删的一张应报错: %v", err)
	}
	var count int64
	database.DB.Model(&models.ImageNearDuplicateDismissal{}).Count(&count)
	if count != 0 {
		t.Fatalf("报错时不应写忽略记录: %d", count)
	}
}

// 修复 S（图片）：组里某个 ID 的记录已硬删时跳过这一对；跳完没有可写的配对时报错。
func TestImageCleanupIMG07DismissMemberSkipsHardDeletedFixS(t *testing.T) {
	setupImageServiceTestDB(t)
	a, b, c := fixSImages(t)
	if err := database.DB.Unscoped().Delete(&models.Image{}, b.ID).Error; err != nil {
		t.Fatal(err)
	}
	ids := []uint{a.ID, b.ID, c.ID}
	if err := DismissImageNearDuplicateMember(ids, a.ID); err != nil {
		t.Fatalf("组里有已硬删成员时应跳过它: %v", err)
	}
	var rows []models.ImageNearDuplicateDismissal
	database.DB.Find(&rows)
	if len(rows) != 1 || rows[0].ImageLowID != a.ID || rows[0].ImageHighID != c.ID ||
		rows[0].FingerprintA == "" || rows[0].FingerprintB == "" {
		t.Fatalf("只应写 a-c 一对且带指纹: %+v", rows)
	}
	if err := database.DB.Unscoped().Delete(&models.Image{}, c.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := DismissImageNearDuplicateMember(ids, a.ID); err == nil || !strings.Contains(err.Error(), "记录都已不存在") {
		t.Fatalf("没有可写的配对应报错: %v", err)
	}
	var count int64
	database.DB.Model(&models.ImageNearDuplicateDismissal{}).Count(&count)
	if count != 1 {
		t.Fatalf("报错时不应多写记录: %d", count)
	}
}

// 修复 S（整组）：含软删成员的整组忽略成功，只写至少一侧在库的配对；硬删的 ID 跳过。
func TestCleanupIMG07DismissGroupWithTrashedMembersFixS(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	a := p016Video(t, root, "a.mp4", "fixs-a", 1920, 1080)
	b := p016Video(t, root, "b.mp4", "fixs-b-x", 1920, 1080)
	c := p016Video(t, root, "c.mp4", "fixs-c-xx", 1920, 1080)
	d := p016Video(t, root, "d.mp4", "fixs-d-xxx", 1920, 1080)
	fixSMoveAway(t, b.Path) // b 已移到废纸篓；c 只从库里移除，文件还在原处。
	for _, video := range []models.Video{b, c} {
		if err := database.DB.Delete(&video).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Unscoped().Delete(&d).Error; err != nil {
		t.Fatal(err)
	}
	if err := DismissNearDuplicateGroup([]uint{a.ID, b.ID, c.ID, d.ID}); err != nil {
		t.Fatalf("含软删成员的整组忽略应成功: %v", err)
	}
	var rows []models.NearDuplicateDismissal
	database.DB.Order("video_low_id, video_high_id").Find(&rows)
	if len(rows) != 2 || rows[0].VideoLowID != a.ID || rows[0].VideoHighID != b.ID ||
		rows[1].VideoLowID != a.ID || rows[1].VideoHighID != c.ID {
		t.Fatalf("只应写 a-b、a-c（b-c 两侧都已软删、d 已硬删）: %+v", rows)
	}
	if rows[0].FingerprintA == "" || rows[0].FingerprintB != "" || rows[1].FingerprintA == "" || rows[1].FingerprintB == "" {
		t.Fatalf("指纹记录不符（b 文件已移走记空，c 文件还在记真实指纹）: %+v", rows)
	}
}

// 修复 S（整组）：组里一个在库成员都没有，或跳过硬删后没有可写的配对时报错，不写记录。
func TestCleanupIMG07DismissGroupNothingInLibraryFixS(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	a := p016Video(t, root, "a.mp4", "fixs-a", 1920, 1080)
	b := p016Video(t, root, "b.mp4", "fixs-b-x", 1920, 1080)
	c := p016Video(t, root, "c.mp4", "fixs-c-xx", 1920, 1080)
	e := p016Video(t, root, "e.mp4", "fixs-e-xxxx", 1920, 1080)
	for _, video := range []models.Video{a, b} {
		if err := database.DB.Delete(&video).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Unscoped().Delete(&c).Error; err != nil {
		t.Fatal(err)
	}
	if err := DismissNearDuplicateGroup([]uint{a.ID, b.ID, c.ID}); err == nil || !strings.Contains(err.Error(), "都已不在片库中") {
		t.Fatalf("全员不在库应报错: %v", err)
	}
	if err := DismissNearDuplicateGroup([]uint{e.ID, c.ID}); err == nil || !strings.Contains(err.Error(), "记录都已不存在") {
		t.Fatalf("跳过硬删后没有可写配对应报错: %v", err)
	}
	var count int64
	database.DB.Model(&models.NearDuplicateDismissal{}).Count(&count)
	if count != 0 {
		t.Fatalf("报错时不应写忽略记录: %d", count)
	}
}

// 修复 S（图片整组）：含软删成员的整组忽略成功，只写至少一侧在库的配对；硬删的 ID 跳过。
func TestImageCleanupIMG07DismissGroupWithTrashedMembersFixS(t *testing.T) {
	setupImageServiceTestDB(t)
	a, b, c := fixSImages(t)
	d := imageCleanupCreateImage(t, filepath.Join(t.TempDir(), "d.jpg"), []byte(strings.Repeat("d", 303)), "abcd000000000000", 100, 100)
	fixSMoveAway(t, b.Path) // b 已移到废纸篓；c 只从库里移除，文件还在原处。
	for _, id := range []uint{b.ID, c.ID} {
		if err := database.DB.Delete(&models.Image{}, id).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Unscoped().Delete(&models.Image{}, d.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := DismissImageNearDuplicateGroup([]uint{a.ID, b.ID, c.ID, d.ID}); err != nil {
		t.Fatalf("含软删成员的整组忽略应成功: %v", err)
	}
	var rows []models.ImageNearDuplicateDismissal
	database.DB.Order("image_low_id, image_high_id").Find(&rows)
	if len(rows) != 2 || rows[0].ImageLowID != a.ID || rows[0].ImageHighID != b.ID ||
		rows[1].ImageLowID != a.ID || rows[1].ImageHighID != c.ID {
		t.Fatalf("只应写 a-b、a-c（b-c 两侧都已软删、d 已硬删）: %+v", rows)
	}
	if rows[0].FingerprintA == "" || rows[0].FingerprintB != "" || rows[1].FingerprintA == "" || rows[1].FingerprintB == "" {
		t.Fatalf("指纹记录不符（b 文件已移走记空，c 文件还在记真实指纹）: %+v", rows)
	}
}

// 修复 S（图片整组）：组里一个在库成员都没有，或跳过硬删后没有可写的配对时报错，不写记录。
func TestImageCleanupIMG07DismissGroupNothingInLibraryFixS(t *testing.T) {
	setupImageServiceTestDB(t)
	a, b, c := fixSImages(t)
	e := imageCleanupCreateImage(t, filepath.Join(t.TempDir(), "e.jpg"), []byte(strings.Repeat("e", 304)), "abcd000000000000", 100, 100)
	for _, id := range []uint{a.ID, b.ID} {
		if err := database.DB.Delete(&models.Image{}, id).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Unscoped().Delete(&models.Image{}, c.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := DismissImageNearDuplicateGroup([]uint{a.ID, b.ID, c.ID}); err == nil || !strings.Contains(err.Error(), "都已不在图片库中") {
		t.Fatalf("全员不在库应报错: %v", err)
	}
	if err := DismissImageNearDuplicateGroup([]uint{e.ID, c.ID}); err == nil || !strings.Contains(err.Error(), "记录都已不存在") {
		t.Fatalf("跳过硬删后没有可写配对应报错: %v", err)
	}
	var count int64
	database.DB.Model(&models.ImageNearDuplicateDismissal{}).Count(&count)
	if count != 0 {
		t.Fatalf("报错时不应写忽略记录: %d", count)
	}
}
