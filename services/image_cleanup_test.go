package services

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// imageCleanupCreateImage 写入真实文件并按当前 stat 建库记录；hash 非空时把
// hash_source_size/mod_time 设为与文件一致（即"非 stale"）。
func imageCleanupCreateImage(t *testing.T, path string, content []byte, hash string, width, height int) *models.Image {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("写入图片文件失败: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 图片文件失败: %v", err)
	}
	img := models.Image{
		Name:           filepath.Base(path),
		Path:           path,
		Directory:      filepath.Dir(path),
		Size:           info.Size(),
		Width:          width,
		Height:         height,
		Format:         "jpg",
		PerceptualHash: hash,
	}
	if hash != "" {
		img.HashSourceSize = info.Size()
		img.HashSourceModTimeNS = info.ModTime().UnixNano()
	}
	if err := database.DB.Create(&img).Error; err != nil {
		t.Fatalf("创建图片记录失败: %v", err)
	}
	return &img
}

func imageCleanupGroupIDs(group ImageCleanupDuplicateGroup) []uint {
	ids := []uint{group.Original.ID}
	for _, candidate := range group.Candidates {
		ids = append(ids, candidate.ID)
	}
	return ids
}

func imageCleanupContainsID(ids []uint, id uint) bool {
	for _, item := range ids {
		if item == id {
			return true
		}
	}
	return false
}

func TestImageCleanupEmptyLibraryProducesEmptyAnalysis(t *testing.T) {
	setupImageServiceTestDB(t)
	svc := NewImageCleanupService()
	analysis, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("空库分析失败: %v", err)
	}
	if len(analysis.DuplicateGroups) != 0 || len(analysis.NearDuplicateGroups) != 0 || analysis.StaleHashCount != 0 {
		t.Fatalf("空库应产出空分析结果，实际 %+v", analysis)
	}
}

func TestImageCleanupExcludesBlacklistedDirectories(t *testing.T) {
	setupImageServiceTestDB(t)
	included := t.TempDir()
	excluded := t.TempDir()
	same := bytes.Repeat([]byte("a"), 2048)

	// 黑名单目录内的一对精确重复；若黑名单生效，它们不应出现在候选里。
	imageCleanupCreateImage(t, filepath.Join(excluded, "a.jpg"), same, "", 100, 100)
	imageCleanupCreateImage(t, filepath.Join(excluded, "b.jpg"), same, "", 100, 100)
	// 非黑名单的一对精确重复，作为对照应当保留。
	imageCleanupCreateImage(t, filepath.Join(included, "c.jpg"), same, "", 4000, 3000)
	imageCleanupCreateImage(t, filepath.Join(included, "d.jpg"), same, "", 100, 100)

	// 通过图片专用黑名单排除 excluded 目录。
	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").
		Update("image_scan_exclude_paths", excluded).Error; err != nil {
		t.Fatalf("设置图片黑名单失败: %v", err)
	}

	svc := NewImageCleanupService()
	analysis, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(analysis.DuplicateGroups) != 1 {
		t.Fatalf("黑名单目录应被排除，仅剩 1 组精确重复，实际 %d 组", len(analysis.DuplicateGroups))
	}
	for _, id := range imageCleanupGroupIDs(analysis.DuplicateGroups[0]) {
		var img models.Image
		if err := database.DB.First(&img, id).Error; err != nil {
			t.Fatalf("读取候选图片失败: %v", err)
		}
		if strings.HasPrefix(img.Path, excluded) {
			t.Fatalf("黑名单目录 %s 的图片不应进入候选: %s", excluded, img.Path)
		}
	}
}

func TestImageCleanupExactDuplicateGroupAndOriginalSelection(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	same := bytes.Repeat([]byte("a"), 2048)
	differentSameSize := append(bytes.Repeat([]byte("a"), 2047), 'b')

	small := imageCleanupCreateImage(t, filepath.Join(dir, "dup-small.jpg"), same, "", 100, 100)
	large := imageCleanupCreateImage(t, filepath.Join(dir, "dup-large.jpg"), same, "", 4000, 3000)
	sizeOnly := imageCleanupCreateImage(t, filepath.Join(dir, "same-size-other-bytes.jpg"), differentSameSize, "", 100, 100)
	unrelated := imageCleanupCreateImage(t, filepath.Join(dir, "other.jpg"), bytes.Repeat([]byte("c"), 99), "", 100, 100)

	svc := NewImageCleanupService()
	analysis, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(analysis.DuplicateGroups) != 1 {
		t.Fatalf("应产出 1 个精确重复组，实际 %d", len(analysis.DuplicateGroups))
	}
	group := analysis.DuplicateGroups[0]
	if group.Original.ID != large.ID {
		t.Fatalf("Original 应按像素数优先选中 %d，实际 %d", large.ID, group.Original.ID)
	}
	if len(group.Candidates) != 1 || group.Candidates[0].ID != small.ID {
		t.Fatalf("候选应为 %d，实际 %+v", small.ID, group.Candidates)
	}
	if group.Reason != "文件大小和采样哈希一致" {
		t.Fatalf("精确重复 Reason 不符: %q", group.Reason)
	}
	ids := imageCleanupGroupIDs(group)
	if imageCleanupContainsID(ids, sizeOnly.ID) || imageCleanupContainsID(ids, unrelated.ID) {
		t.Fatalf("同大小不同内容/无关图片不应进组: %v", ids)
	}
	if len(analysis.NearDuplicateGroups) != 0 || analysis.StaleHashCount != 0 {
		t.Fatalf("无哈希图片不应产出近似组或 stale 计数，实际 %+v", analysis)
	}
}

func TestImageCleanupMissingFileSkipped(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	same := bytes.Repeat([]byte("a"), 512)
	imageCleanupCreateImage(t, filepath.Join(dir, "kept.jpg"), same, "abcd000000000000", 100, 100)
	missing := imageCleanupCreateImage(t, filepath.Join(dir, "missing.jpg"), same, "abcd000000000000", 100, 100)
	if err := os.Remove(missing.Path); err != nil {
		t.Fatalf("删除测试文件失败: %v", err)
	}

	svc := NewImageCleanupService()
	analysis, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(analysis.DuplicateGroups) != 0 || len(analysis.NearDuplicateGroups) != 0 {
		t.Fatalf("文件缺失的图片应被跳过，实际 %+v", analysis)
	}
	if analysis.StaleHashCount != 0 {
		t.Fatalf("文件缺失不应计入 StaleHashCount，实际 %d", analysis.StaleHashCount)
	}
}

func TestImageCleanupNearDuplicateGroupsByHammingDistance(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	// 各文件大小互不相同，避免落入精确重复分桶。
	big := imageCleanupCreateImage(t, filepath.Join(dir, "near-big.jpg"), bytes.Repeat([]byte("a"), 300), "abcd000000000000", 200, 200)
	smallVariant := imageCleanupCreateImage(t, filepath.Join(dir, "near-small.jpg"), bytes.Repeat([]byte("b"), 301), "abcd000000000001", 100, 100)
	farSameBand := imageCleanupCreateImage(t, filepath.Join(dir, "far-same-band.jpg"), bytes.Repeat([]byte("c"), 302), "abcd0000ffffffff", 100, 100)
	closeOtherBand := imageCleanupCreateImage(t, filepath.Join(dir, "close-other-band.jpg"), bytes.Repeat([]byte("d"), 303), "abce000000000000", 100, 100)

	svc := NewImageCleanupService()
	analysis, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(analysis.DuplicateGroups) != 0 {
		t.Fatalf("不应产出精确重复组，实际 %d", len(analysis.DuplicateGroups))
	}
	if len(analysis.NearDuplicateGroups) != 1 {
		t.Fatalf("应产出 1 个近似重复组，实际 %d", len(analysis.NearDuplicateGroups))
	}
	group := analysis.NearDuplicateGroups[0]
	if group.Original.ID != big.ID {
		t.Fatalf("近似组 Original 应按像素数选中 %d，实际 %d", big.ID, group.Original.ID)
	}
	if len(group.Candidates) != 1 || group.Candidates[0].ID != smallVariant.ID {
		t.Fatalf("近似组候选应为 %d，实际 %+v", smallVariant.ID, group.Candidates)
	}
	if !strings.Contains(group.Reason, "感知哈希") {
		t.Fatalf("近似重复 Reason 不符: %q", group.Reason)
	}
	ids := imageCleanupGroupIDs(group)
	if imageCleanupContainsID(ids, farSameBand.ID) || imageCleanupContainsID(ids, closeOtherBand.ID) {
		t.Fatalf("汉明距离超阈值或不同分桶的图片不应进组: %v", ids)
	}
}

// 审阅界面要靠标签、AI 描述、实测大小和修改时间判断该留哪一份；分析的主查询不做
// 全库 Preload，这些字段必须在成组之后单独补回来。
func TestImageCleanupMembersCarryFactsTagsAndDescription(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	content := bytes.Repeat([]byte("same"), 40)
	keep := imageCleanupCreateImage(t, filepath.Join(dir, "keep.jpg"), content, "", 400, 300)
	copyImage := imageCleanupCreateImage(t, filepath.Join(dir, "copy.jpg"), content, "", 400, 300)

	tag := models.Tag{Name: "旅行"}
	if err := database.DB.Create(&tag).Error; err != nil {
		t.Fatalf("创建标签失败: %v", err)
	}
	if err := database.DB.Model(keep).Association("Tags").Append(&tag); err != nil {
		t.Fatalf("关联标签失败: %v", err)
	}
	descriptions := []models.ImageAIDescription{
		{ImageID: keep.ID, Status: "completed", Description: "海边日落"},
		{ImageID: copyImage.ID, Status: "failed", Description: "不该下发"},
	}
	if err := database.DB.Create(&descriptions).Error; err != nil {
		t.Fatalf("创建 AI 描述失败: %v", err)
	}

	svc := NewImageCleanupService()
	analysis, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(analysis.DuplicateGroups) != 1 {
		t.Fatalf("应产出 1 个精确重复组，实际 %d", len(analysis.DuplicateGroups))
	}
	group := analysis.DuplicateGroups[0]
	original := group.Original
	if original.FileSize != int64(len(content)) {
		t.Fatalf("成员应带实测文件大小 %d，实际 %d", len(content), original.FileSize)
	}
	if original.ModTimeNS == 0 {
		t.Fatalf("成员应带实测修改时间，实际 %+v", original)
	}
	var keeper, other ImageCleanupMember
	for _, member := range append([]ImageCleanupMember{group.Original}, group.Candidates...) {
		if member.ID == keep.ID {
			keeper = member
		} else {
			other = member
		}
	}
	if len(keeper.Tags) != 1 || keeper.Tags[0].Name != "旅行" {
		t.Fatalf("已打标签的成员应带出标签，实际 %+v", keeper.Tags)
	}
	if keeper.Description != "海边日落" {
		t.Fatalf("已完成的 AI 描述应带出，实际 %q", keeper.Description)
	}
	if other.Description != "" {
		t.Fatalf("未完成的 AI 描述不应下发，实际 %q", other.Description)
	}
	if group.MaxHammingDistance != 0 {
		t.Fatalf("精确重复组的汉明距离应为 0，实际 %d", group.MaxHammingDistance)
	}
}

// 近似重复组要报出"有多像"，界面靠它给用户量化说法。
func TestImageCleanupNearDuplicateReportsMaxHammingDistance(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	imageCleanupCreateImage(t, filepath.Join(dir, "a.jpg"), bytes.Repeat([]byte("a"), 300), "abcd000000000000", 200, 200)
	// 与上一张相差 3 个 bit（0x7 = 三位）。
	imageCleanupCreateImage(t, filepath.Join(dir, "b.jpg"), bytes.Repeat([]byte("b"), 301), "abcd000000000007", 100, 100)

	svc := NewImageCleanupService()
	analysis, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(analysis.NearDuplicateGroups) != 1 {
		t.Fatalf("应产出 1 个近似重复组，实际 %d", len(analysis.NearDuplicateGroups))
	}
	if got := analysis.NearDuplicateGroups[0].MaxHammingDistance; got != 3 {
		t.Fatalf("组内最大汉明距离应为 3，实际 %d", got)
	}
}

func TestImageCleanupNearDuplicateOriginalFallsBackToFileSize(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	smallFile := imageCleanupCreateImage(t, filepath.Join(dir, "same-pixels-small.jpg"), bytes.Repeat([]byte("a"), 400), "1234000000000000", 100, 100)
	bigFile := imageCleanupCreateImage(t, filepath.Join(dir, "same-pixels-big.jpg"), bytes.Repeat([]byte("b"), 900), "1234000000000000", 100, 100)

	svc := NewImageCleanupService()
	analysis, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(analysis.NearDuplicateGroups) != 1 {
		t.Fatalf("应产出 1 个近似重复组，实际 %d", len(analysis.NearDuplicateGroups))
	}
	group := analysis.NearDuplicateGroups[0]
	if group.Original.ID != bigFile.ID {
		t.Fatalf("像素数相同时 Original 应按体积选中 %d，实际 %d", bigFile.ID, group.Original.ID)
	}
	if len(group.Candidates) != 1 || group.Candidates[0].ID != smallFile.ID {
		t.Fatalf("候选应为 %d，实际 %+v", smallFile.ID, group.Candidates)
	}
}

func TestImageCleanupStaleHashCountedAndSkipped(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	fresh := imageCleanupCreateImage(t, filepath.Join(dir, "fresh.jpg"), bytes.Repeat([]byte("a"), 500), "abcd000000000000", 100, 100)
	stale := imageCleanupCreateImage(t, filepath.Join(dir, "stale.jpg"), bytes.Repeat([]byte("b"), 501), "abcd000000000000", 100, 100)
	imageCleanupCreateImage(t, filepath.Join(dir, "no-hash.jpg"), bytes.Repeat([]byte("c"), 502), "", 100, 100)
	if err := database.DB.Model(&models.Image{}).Where("id = ?", stale.ID).
		Update("hash_source_size", stale.HashSourceSize+1).Error; err != nil {
		t.Fatalf("制造 stale 指纹失败: %v", err)
	}

	svc := NewImageCleanupService()
	analysis, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if analysis.StaleHashCount != 1 {
		t.Fatalf("StaleHashCount 应为 1（无哈希不计），实际 %d", analysis.StaleHashCount)
	}
	if len(analysis.NearDuplicateGroups) != 0 {
		t.Fatalf("stale 指纹图片不应参与近似检测，实际 %+v", analysis.NearDuplicateGroups)
	}
	_ = fresh
}

func TestImageCleanupExactPairsExcludedFromNearDuplicates(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	same := bytes.Repeat([]byte("a"), 2048)
	imageCleanupCreateImage(t, filepath.Join(dir, "exact-1.jpg"), same, "abcd000000000000", 100, 100)
	imageCleanupCreateImage(t, filepath.Join(dir, "exact-2.jpg"), same, "abcd000000000000", 100, 100)

	svc := NewImageCleanupService()
	analysis, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(analysis.DuplicateGroups) != 1 {
		t.Fatalf("应产出 1 个精确重复组，实际 %d", len(analysis.DuplicateGroups))
	}
	if len(analysis.NearDuplicateGroups) != 0 {
		t.Fatalf("已属精确重复的对不应再报近似重复，实际 %+v", analysis.NearDuplicateGroups)
	}
}

func TestImageCleanupDismissalExcludesGroupAndIsIdempotent(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	first := imageCleanupCreateImage(t, filepath.Join(dir, "pair-1.jpg"), bytes.Repeat([]byte("a"), 700), "beef000000000000", 100, 100)
	second := imageCleanupCreateImage(t, filepath.Join(dir, "pair-2.jpg"), bytes.Repeat([]byte("b"), 701), "beef000000000003", 100, 100)

	svc := NewImageCleanupService()
	analysis, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(analysis.NearDuplicateGroups) != 1 {
		t.Fatalf("忽略前应产出 1 个近似重复组，实际 %d", len(analysis.NearDuplicateGroups))
	}

	// 故意用高 ID 在前的顺序传入，校验低/高 ID 归一化。
	if err := DismissImageNearDuplicateGroup([]uint{second.ID, first.ID}); err != nil {
		t.Fatalf("忽略近似重复组失败: %v", err)
	}
	analysis, err = svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("忽略后分析失败: %v", err)
	}
	if len(analysis.NearDuplicateGroups) != 0 {
		t.Fatalf("忽略后不应再产出近似重复组，实际 %+v", analysis.NearDuplicateGroups)
	}

	if err := DismissImageNearDuplicateGroup([]uint{first.ID, second.ID}); err != nil {
		t.Fatalf("重复忽略应幂等无错误: %v", err)
	}
	var count int64
	if err := database.DB.Model(&models.ImageNearDuplicateDismissal{}).Count(&count).Error; err != nil {
		t.Fatalf("统计 dismissals 失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("重复忽略不应新增行，实际 %d 行", count)
	}
	var dismissal models.ImageNearDuplicateDismissal
	if err := database.DB.First(&dismissal).Error; err != nil {
		t.Fatalf("读取 dismissal 失败: %v", err)
	}
	if dismissal.ImageLowID != first.ID || dismissal.ImageHighID != second.ID {
		t.Fatalf("dismissal 应低 ID 在前，实际 low=%d high=%d", dismissal.ImageLowID, dismissal.ImageHighID)
	}

	if err := DismissImageNearDuplicateGroup([]uint{first.ID}); err == nil {
		t.Fatal("单张图片的忽略请求应报错")
	}
	if err := DismissImageNearDuplicateGroup([]uint{first.ID, first.ID}); err == nil {
		t.Fatal("重复同一 ID 的忽略请求应报错")
	}
}

func TestImageCleanupGiantBandBucketTruncatesNeighbors(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	// A 先入桶，随后 65 个同前缀但距离远（>8）的 filler 把 A 挤出邻居窗口，
	// 最后与 A 哈希完全相同的 Z 到达时已看不到 A。
	first := imageCleanupCreateImage(t, filepath.Join(dir, "victim-a.jpg"), bytes.Repeat([]byte("a"), 1000), "abcd000000000000", 100, 100)
	fillerCount := imageCleanupMaxBandNeighbors + 1
	for i := 0; i < fillerCount; i++ {
		imageCleanupCreateImage(t, filepath.Join(dir, fmt.Sprintf("filler-%02d.jpg", i)),
			bytes.Repeat([]byte("f"), 1100+i), "abcd0000ffffffff", 100, 100)
	}
	last := imageCleanupCreateImage(t, filepath.Join(dir, "victim-z.jpg"), bytes.Repeat([]byte("z"), 3000), "abcd000000000000", 100, 100)

	svc := NewImageCleanupService()
	analysis, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	// filler 之间距离 0，分组仍产出（边界表：巨型桶截断后分组仍产出）。
	if len(analysis.NearDuplicateGroups) != 1 {
		t.Fatalf("应产出 1 个 filler 近似重复组，实际 %d", len(analysis.NearDuplicateGroups))
	}
	group := analysis.NearDuplicateGroups[0]
	if len(group.Candidates)+1 != fillerCount {
		t.Fatalf("filler 组应含 %d 张图片，实际 %d", fillerCount, len(group.Candidates)+1)
	}
	ids := imageCleanupGroupIDs(group)
	if imageCleanupContainsID(ids, first.ID) || imageCleanupContainsID(ids, last.ID) {
		t.Fatalf("被邻居上限截断的 A/Z 不应进 filler 组: %v", ids)
	}
}

func TestImageCleanupSoftDeletedImagesExcluded(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	same := bytes.Repeat([]byte("a"), 2048)
	imageCleanupCreateImage(t, filepath.Join(dir, "active.jpg"), same, "abcd000000000000", 100, 100)
	deleted := imageCleanupCreateImage(t, filepath.Join(dir, "deleted.jpg"), same, "abcd000000000000", 100, 100)
	if err := database.DB.Delete(&models.Image{}, deleted.ID).Error; err != nil {
		t.Fatalf("软删图片失败: %v", err)
	}

	svc := NewImageCleanupService()
	analysis, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(analysis.DuplicateGroups) != 0 || len(analysis.NearDuplicateGroups) != 0 {
		t.Fatalf("软删图片不应参与分析，实际 %+v", analysis)
	}
}

func TestImageCleanupStartStatusProgressAndInvalidate(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	same := bytes.Repeat([]byte("a"), 2048)
	small := imageCleanupCreateImage(t, filepath.Join(dir, "dup-1.jpg"), same, "", 100, 100)
	imageCleanupCreateImage(t, filepath.Join(dir, "dup-2.jpg"), same, "", 4000, 3000)

	svc := NewImageCleanupService()
	var stagesMu sync.Mutex
	stages := make([]string, 0)
	svc.SetEventEmitter(func(progress ImageCleanupProgress) {
		stagesMu.Lock()
		stages = append(stages, progress.Stage)
		stagesMu.Unlock()
	})

	status, err := svc.StartImageCleanupAnalysis()
	if err != nil {
		t.Fatalf("启动分析失败: %v", err)
	}
	if !status.Running {
		t.Fatalf("启动后状态应为 Running，实际 %+v", status)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		status = svc.GetImageCleanupStatus()
		if !status.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("等待分析完成超时")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !status.Completed || status.Error != "" || status.Analysis == nil {
		t.Fatalf("分析应成功完成，实际 %+v", status)
	}
	if len(status.Analysis.DuplicateGroups) != 1 {
		t.Fatalf("应产出 1 个精确重复组，实际 %d", len(status.Analysis.DuplicateGroups))
	}
	stagesMu.Lock()
	sawDone := false
	for _, stage := range stages {
		if stage == "done" {
			sawDone = true
		}
	}
	stagesMu.Unlock()
	if !sawDone {
		t.Fatalf("进度事件应包含 done 阶段，实际 %v", stages)
	}

	if status.Stale {
		t.Fatalf("刚完成的分析不应是过期状态，实际 %+v", status)
	}
	// 与视频侧同一条不变量：done 阶段出现时结果必须已经可读。
	if status.Progress.Stage != "done" {
		t.Fatalf("完成后进度阶段应为 done，实际 %+v", status.Progress)
	}

	// 模拟删除后失效（app 层在 DeleteImage/BatchDeleteImages/Restore 后调用）：
	// 软删候选 → Invalidate 只标记过期（结果保留供继续审阅）→ 重新分析不再成组。
	imageService := NewImageService()
	result := imageService.BatchDeleteImages([]uint{small.ID}, false)
	if result.Failed != 0 {
		t.Fatalf("软删候选失败: %+v", result.Errors)
	}
	svc.InvalidateAnalysis()
	status = svc.GetImageCleanupStatus()
	if !status.Completed || status.Analysis == nil || status.Error != "" {
		t.Fatalf("Invalidate 后仍应保留结果供审阅，实际 %+v", status)
	}
	if !status.Stale {
		t.Fatalf("Invalidate 后应标记为过期，实际 %+v", status)
	}

	analysis, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("重新分析失败: %v", err)
	}
	if len(analysis.DuplicateGroups) != 0 {
		t.Fatalf("删除候选后重新分析不应再成组，实际 %+v", analysis.DuplicateGroups)
	}
}
