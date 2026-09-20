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
	if len(analysis.NearDuplicateGroups) != 0 {
		t.Fatalf("无哈希图片不应产出近似组，实际 %+v", analysis.NearDuplicateGroups)
	}
	// 四张夹具都没有指纹，四张都计入待补全（D-CD04）。早先"无哈希不计"使一个
	// 从没回填过的图库显示"指纹过期 0"，用户看不出近似重复为什么是空的。
	if analysis.StaleHashCount != 4 {
		t.Fatalf("四张无指纹图片都应计入待补全，实际 %d", analysis.StaleHashCount)
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
	if !strings.Contains(group.Reason, "感知哈希") {
		t.Fatalf("近似重复 Reason 不符: %q", group.Reason)
	}
	ids := imageCleanupGroupIDs(group)
	// closeOtherBand 与 big 的距离只有 2，本来就该是近似重复。单段前缀分桶时它因为
	// 高 16 位不同而永远进不了比对——那正是 D-CD03 要修的漏检。改成 8 段之后
	// 它们共享低 6 段，能比上了。
	if !imageCleanupContainsID(ids, smallVariant.ID) || !imageCleanupContainsID(ids, closeOtherBand.ID) {
		t.Fatalf("距离 ≤8 的两张都应进组（含跨段命中的 close-other-band）: %v", ids)
	}
	if len(group.Candidates) != 2 {
		t.Fatalf("近似组候选应为 2 张，实际 %+v", group.Candidates)
	}
	// farSameBand 同段但距离 32，仍然必须被阈值挡住：放宽分桶不等于放宽判定。
	if imageCleanupContainsID(ids, farSameBand.ID) {
		t.Fatalf("汉明距离超阈值的图片不应进组: %v", ids)
	}
}

// 审阅界面要靠标签、AI 描述、实测大小和修改时间判断该留哪一份；分析的主查询不做
// 全库 Preload，这些字段必须在成组之后单独补回来。
func TestImageCleanupMembersCarryFactsAndTags(t *testing.T) {
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
	if other.ID != copyImage.ID {
		t.Fatalf("另一名成员应是副本 %d，实际 %d", copyImage.ID, other.ID)
	}
	if len(other.Tags) != 0 {
		t.Fatalf("未打标签的成员不应凭空带出标签，实际 %+v", other.Tags)
	}
	if group.MaxHammingDistance != 0 {
		t.Fatalf("精确重复组的汉明距离应为 0，实际 %d", group.MaxHammingDistance)
	}
}

// 回归：连通分量是链式的，链两端可以毫不相似。若报组内两两最大距离，
// 一个刚判定为近似重复的组会被界面标成相似度 0%。距离只对推荐保留项算。
func TestImageCleanupNearDuplicateDistanceIsRelativeToKeeper(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	// a—b 距离 3，b—c 距离 3，a—c 距离 6：三者连成一条链。a 像素最大，是推荐保留项。
	imageCleanupCreateImage(t, filepath.Join(dir, "a.jpg"), bytes.Repeat([]byte("a"), 300), "abcd000000000000", 400, 400)
	imageCleanupCreateImage(t, filepath.Join(dir, "b.jpg"), bytes.Repeat([]byte("b"), 301), "abcd000000000007", 200, 200)
	imageCleanupCreateImage(t, filepath.Join(dir, "c.jpg"), bytes.Repeat([]byte("c"), 302), "abcd000000000038", 100, 100)

	svc := NewImageCleanupService()
	analysis, err := svc.AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(analysis.NearDuplicateGroups) != 1 {
		t.Fatalf("应产出 1 个近似重复组，实际 %d", len(analysis.NearDuplicateGroups))
	}
	group := analysis.NearDuplicateGroups[0]
	if len(group.Candidates) != 2 {
		t.Fatalf("组内应有 2 个候选，实际 %d", len(group.Candidates))
	}
	// 相对保留项 a：b 距离 3、c 距离 3（0x38 是三位），最大 3；两两最大值会是 6。
	if group.MaxHammingDistance != 3 {
		t.Fatalf("距离应相对推荐保留项计算（期望 3），实际 %d", group.MaxHammingDistance)
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
	// 失效的 stale.jpg 与从未回填的 no-hash.jpg 都算"没有可用指纹"（D-CD04）。
	if analysis.StaleHashCount != 2 {
		t.Fatalf("StaleHashCount 应为 2（失效 1 + 未回填 1），实际 %d", analysis.StaleHashCount)
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

// 巨型桶的邻居上限仍然生效，但多段分桶让它不再吞掉本该命中的对（D-CD03）。
//
// A 先入桶，随后 65 个 filler 把 A 挤出"高两段"那几个桶的邻居窗口。单段分桶时
// 这就意味着与 A 哈希完全相同的 Z 永远看不到 A——一条纯粹由实现上限造成的漏检。
// 改成 8 段之后 A 与 Z 还共享 filler 不在的那几段，桶里只有它们俩，从不溢出，
// 于是照样成组。这正是多段冗余要买的东西。
func TestImageCleanupGiantBandBucketKeepsPairsFoundByOtherBands(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
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
	// filler 彼此距离 0 成一组；A 与 Z 距离 0 另成一组。
	if len(analysis.NearDuplicateGroups) != 2 {
		t.Fatalf("应产出 2 个近似重复组（filler 一组、A/Z 一组），实际 %d", len(analysis.NearDuplicateGroups))
	}
	var fillerGroup, victimGroup *ImageCleanupDuplicateGroup
	for i := range analysis.NearDuplicateGroups {
		group := &analysis.NearDuplicateGroups[i]
		if imageCleanupContainsID(imageCleanupGroupIDs(*group), first.ID) {
			victimGroup = group
			continue
		}
		fillerGroup = group
	}
	if fillerGroup == nil || victimGroup == nil {
		t.Fatalf("两组应当一组是 filler、一组是 A/Z，实际 %+v", analysis.NearDuplicateGroups)
	}
	if len(fillerGroup.Candidates)+1 != fillerCount {
		t.Fatalf("filler 组应含 %d 张图片，实际 %d", fillerCount, len(fillerGroup.Candidates)+1)
	}
	victimIDs := imageCleanupGroupIDs(*victimGroup)
	if len(victimIDs) != 2 || !imageCleanupContainsID(victimIDs, last.ID) {
		t.Fatalf("A/Z 应当各自成对（距离 0，靠 filler 不占的那几段命中）: %v", victimIDs)
	}
	// 距离 32 的 filler 不许混进 A/Z 那一组：放宽分桶不等于放宽判定。
	if len(fillerGroup.Candidates)+1+len(victimIDs) != fillerCount+2 {
		t.Fatalf("两组成员总数不对: filler=%d victim=%d", len(fillerGroup.Candidates)+1, len(victimIDs))
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

// imageCleanupHashState 造一个只为走分桶/成组的内存条目：不落盘、不进库，
// 因为 buildNearDuplicateGroups 只读 image 的哈希三件套与 state 的实测大小。
func imageCleanupHashState(id uint, name string, hash string) imageCleanupFileState {
	return imageCleanupFileState{
		image: models.Image{
			ID: id, Name: name, Path: "/tmp/" + name, Directory: "/tmp",
			Size: 100, PerceptualHash: hash,
			HashSourceSize: 100, HashSourceModTimeNS: 1,
		},
		size:      100,
		modTimeNS: 1,
	}
}

// imageCleanupBandFiller 造一张"只在第 band 段与全零哈希相同、其余段全是 ff"的图：
// 会跟全零哈希落进同一个桶，但汉明距离 56，永远不成边。
func imageCleanupBandFiller(band int) string {
	pairs := make([]string, imageCleanupBandCount)
	for i := range pairs {
		pairs[i] = "ff"
	}
	pairs[band] = "00"
	return strings.Join(pairs, "")
}

// 候选上限（256）仍然生效：前几段把名额占满之后，后面几段根本不扫，真正的近似
// 重复因此被漏掉。这是上限换来的代价，也是 TC-05 要钉住的东西——分桶从 1 段放宽到
// 8 段之后，一张图最多能攒 8×64 个候选，上限第一次真的够得着了。
func TestImageCleanupCandidateCapStopsScanningLaterBands(t *testing.T) {
	// 前 4 段各塞满一桶（64 张），刚好把 256 个候选名额占光。
	states := make([]imageCleanupFileState, 0, imageCleanupMaxCandidates+2)
	var id uint = 1
	for band := 0; band < imageCleanupMaxCandidates/imageCleanupMaxBandNeighbors; band++ {
		for i := 0; i < imageCleanupMaxBandNeighbors; i++ {
			states = append(states, imageCleanupHashState(id, fmt.Sprintf("filler-%d-%02d.jpg", band, i), imageCleanupBandFiller(band)))
			id++
		}
	}
	// 只在最后一段与受害者相同、距离 7（阈值内）的真近似重复。
	const nearHash = "0101010101010100"
	near := imageCleanupHashState(id, "near.jpg", nearHash)
	id++
	victim := imageCleanupHashState(id, "victim.jpg", "0000000000000000")
	states = append(states, near, victim)

	svc := NewImageCleanupService()
	groups, stale := svc.buildNearDuplicateGroups(states, nil)
	if stale != 0 {
		t.Fatalf("全部条目都有可用指纹，stale 应为 0，实际 %d", stale)
	}
	for _, group := range groups {
		if imageCleanupContainsID(imageCleanupGroupIDs(group), victim.image.ID) {
			t.Fatalf("候选上限没生效：受害者本不该扫到第 8 段的邻居，却成了组 %v", imageCleanupGroupIDs(group))
		}
	}

	// 对照组：把占名额的 filler 去掉，同一对必须能成组——证明上面漏掉那一对确实是
	// 上限造成的，而不是哈希或阈值构造错了。
	controlGroups, _ := svc.buildNearDuplicateGroups([]imageCleanupFileState{near, victim}, nil)
	if len(controlGroups) != 1 || len(imageCleanupGroupIDs(controlGroups[0])) != 2 {
		t.Fatalf("对照组应当成一对（距离 7，在阈值 %d 内），实际 %+v", imageCleanupHammingThreshold, controlGroups)
	}
}
