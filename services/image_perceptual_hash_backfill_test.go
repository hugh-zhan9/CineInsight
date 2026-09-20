package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// fakeThumbnailResolver 替掉真实的解码链路：记录被要求处理的 id，并按需把指纹
// 写进库（真实实现里这一步由 ImageThumbnailService.backfillImageMetadata 完成）。
type fakeThumbnailResolver struct {
	mu       sync.Mutex
	resolved []uint
	// failFor 里的 id 直接返回错误，模拟格式不支持或文件读不了。
	failFor map[uint]bool
	// silentFor 里的 id 返回成功但不写指纹，模拟"缩略图出来了、回填却失败了"。
	silentFor map[uint]bool
	block     chan struct{}
}

func (f *fakeThumbnailResolver) ResolveImageThumbnail(ctx context.Context, imageID uint) (*ImageMedia, error) {
	f.mu.Lock()
	f.resolved = append(f.resolved, imageID)
	block, fail, silent := f.block, f.failFor[imageID], f.silentFor[imageID]
	f.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if fail {
		return nil, errors.New("格式不支持")
	}
	if silent {
		return &ImageMedia{Path: "/tmp/fake.jpg", MIME: "image/jpeg"}, nil
	}

	var img models.Image
	if err := database.DB.First(&img, imageID).Error; err != nil {
		return nil, err
	}
	info, err := os.Stat(img.Path)
	if err != nil {
		return nil, err
	}
	if err := database.DB.Model(&models.Image{}).Where("id = ?", imageID).Updates(map[string]interface{}{
		"perceptual_hash":         fmt.Sprintf("%016x", uint64(imageID)),
		"hash_source_size":        info.Size(),
		"hash_source_mod_time_ns": info.ModTime().UnixNano(),
	}).Error; err != nil {
		return nil, err
	}
	return &ImageMedia{Path: "/tmp/fake.jpg", MIME: "image/jpeg"}, nil
}

func (f *fakeThumbnailResolver) resolvedIDs() []uint {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint(nil), f.resolved...)
}

func newFakeThumbnailResolver() *fakeThumbnailResolver {
	return &fakeThumbnailResolver{failFor: map[uint]bool{}, silentFor: map[uint]bool{}}
}

func waitImagePerceptualHashBackfill(t *testing.T, svc *ImagePerceptualHashBackfillService) ImagePerceptualHashBackfillStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := svc.GetImagePerceptualHashBackfillStatus()
		if !status.Running {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("补全任务超时未结束")
	return ImagePerceptualHashBackfillStatus{}
}

// 缺指纹的补、已经新鲜的跳过、取不到缩略图的记失败——三种记账各走一条。
func TestImagePerceptualHashBackfillAccounting(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	missing := imageCleanupCreateImage(t, filepath.Join(dir, "missing-hash.jpg"), bytes.Repeat([]byte("a"), 100), "", 100, 100)
	fresh := imageCleanupCreateImage(t, filepath.Join(dir, "fresh.jpg"), bytes.Repeat([]byte("b"), 101), "abcd000000000000", 100, 100)
	broken := imageCleanupCreateImage(t, filepath.Join(dir, "broken.jpg"), bytes.Repeat([]byte("c"), 102), "", 100, 100)

	resolver := newFakeThumbnailResolver()
	resolver.failFor[broken.ID] = true
	svc := NewImagePerceptualHashBackfillService(resolver)
	if _, err := svc.StartImagePerceptualHashBackfill(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	status := waitImagePerceptualHashBackfill(t, svc)

	if !status.Completed || status.Cancelled {
		t.Fatalf("任务应正常完成，实际 %+v", status)
	}
	if status.Total != 3 {
		t.Fatalf("Total 应为活跃图片数 3，实际 %d", status.Total)
	}
	if status.Succeeded != 1 || status.Skipped != 1 || status.Failed != 1 {
		t.Fatalf("记账不对: succeeded=%d skipped=%d failed=%d", status.Succeeded, status.Skipped, status.Failed)
	}
	if status.Processed != 3 {
		t.Fatalf("Processed 应为 3，实际 %d", status.Processed)
	}
	// 已经新鲜的那张不该被送去解码——这正是跳过它的意义。
	for _, id := range resolver.resolvedIDs() {
		if id == fresh.ID {
			t.Fatal("指纹新鲜的图片不该再取一次缩略图")
		}
	}
	if len(status.Failures) != 1 || status.Failures[0].ImageID != broken.ID {
		t.Fatalf("失败明细应指向 broken.jpg，实际 %+v", status.Failures)
	}

	var refreshed models.Image
	if err := database.DB.First(&refreshed, missing.ID).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if refreshed.PerceptualHash == "" {
		t.Fatal("缺指纹的图片应当被补上")
	}
}

// 缩略图出来了但指纹没落库，必须记失败而不是成功：回填是机会式的，失败只记日志，
// 不回读就会把"没补上"报成"已完成"，用户下一轮还会看到同样的待补全数。
func TestImagePerceptualHashBackfillDetectsSilentBackfillFailure(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	silent := imageCleanupCreateImage(t, filepath.Join(dir, "silent.jpg"), bytes.Repeat([]byte("a"), 100), "", 100, 100)

	resolver := newFakeThumbnailResolver()
	resolver.silentFor[silent.ID] = true
	svc := NewImagePerceptualHashBackfillService(resolver)
	if _, err := svc.StartImagePerceptualHashBackfill(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	status := waitImagePerceptualHashBackfill(t, svc)

	if status.Succeeded != 0 || status.Failed != 1 {
		t.Fatalf("指纹未落库应记失败，实际 succeeded=%d failed=%d", status.Succeeded, status.Failed)
	}
}

// 失效指纹（源文件变过）同样是目标集的一部分，不能只认"空指纹"。
func TestImagePerceptualHashBackfillRecomputesStaleFingerprints(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	stale := imageCleanupCreateImage(t, filepath.Join(dir, "stale.jpg"), bytes.Repeat([]byte("a"), 100), "abcd000000000000", 100, 100)
	if err := database.DB.Model(&models.Image{}).Where("id = ?", stale.ID).
		Update("hash_source_size", stale.HashSourceSize+1).Error; err != nil {
		t.Fatalf("制造失效指纹失败: %v", err)
	}

	resolver := newFakeThumbnailResolver()
	svc := NewImagePerceptualHashBackfillService(resolver)
	if _, err := svc.StartImagePerceptualHashBackfill(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	status := waitImagePerceptualHashBackfill(t, svc)
	if status.Succeeded != 1 || status.Skipped != 0 {
		t.Fatalf("失效指纹应被重算，实际 succeeded=%d skipped=%d", status.Succeeded, status.Skipped)
	}
}

// is_stale 的图片路径已经失效，取缩略图必然失败，不该进目标集白刷一轮失败。
func TestImagePerceptualHashBackfillSkipsStaleImages(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	hidden := imageCleanupCreateImage(t, filepath.Join(dir, "hidden.jpg"), bytes.Repeat([]byte("a"), 100), "", 100, 100)
	if err := database.DB.Model(&models.Image{}).Where("id = ?", hidden.ID).
		Update("is_stale", true).Error; err != nil {
		t.Fatalf("标记 is_stale 失败: %v", err)
	}

	resolver := newFakeThumbnailResolver()
	svc := NewImagePerceptualHashBackfillService(resolver)
	if _, err := svc.StartImagePerceptualHashBackfill(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	status := waitImagePerceptualHashBackfill(t, svc)
	if status.Total != 0 || status.Processed != 0 {
		t.Fatalf("失效图片不该进目标集，实际 total=%d processed=%d", status.Total, status.Processed)
	}
	if len(resolver.resolvedIDs()) != 0 {
		t.Fatalf("失效图片不该被解码，实际 %v", resolver.resolvedIDs())
	}
}

// 运行中重复启动返回当前状态并报 busy，不起第二个 worker。
func TestImagePerceptualHashBackfillRejectsConcurrentStart(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	imageCleanupCreateImage(t, filepath.Join(dir, "a.jpg"), bytes.Repeat([]byte("a"), 100), "", 100, 100)

	resolver := newFakeThumbnailResolver()
	resolver.block = make(chan struct{})
	svc := NewImagePerceptualHashBackfillService(resolver)
	if _, err := svc.StartImagePerceptualHashBackfill(context.Background()); err != nil {
		t.Fatalf("首次启动失败: %v", err)
	}
	if _, err := svc.StartImagePerceptualHashBackfill(context.Background()); !errors.Is(err, ErrImagePerceptualHashBackfillBusy) {
		t.Fatalf("重复启动应报 busy，实际 %v", err)
	}
	close(resolver.block)
	waitImagePerceptualHashBackfill(t, svc)
}

// 取消要停下来并标 Cancelled，不能报"已完成"。
func TestImagePerceptualHashBackfillCancel(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		imageCleanupCreateImage(t, filepath.Join(dir, fmt.Sprintf("a-%d.jpg", i)),
			bytes.Repeat([]byte("a"), 100+i), "", 100, 100)
	}

	resolver := newFakeThumbnailResolver()
	resolver.block = make(chan struct{})
	svc := NewImagePerceptualHashBackfillService(resolver)
	if _, err := svc.StartImagePerceptualHashBackfill(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	// 等 worker 真的卡在第一张上，再取消。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(resolver.resolvedIDs()) == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	if err := svc.CancelImagePerceptualHashBackfill(); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	status := waitImagePerceptualHashBackfill(t, svc)
	if !status.Cancelled || status.Completed {
		t.Fatalf("取消后应标 Cancelled 且不报完成，实际 %+v", status)
	}
	close(resolver.block)

	if err := svc.CancelImagePerceptualHashBackfill(); err == nil {
		t.Fatal("未运行时取消应报错")
	}
}

// 失败的那张不能让游标卡住：下一轮批次必须继续往后推进。
func TestImagePerceptualHashBackfillCursorAdvancesPastFailures(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	first := imageCleanupCreateImage(t, filepath.Join(dir, "a.jpg"), bytes.Repeat([]byte("a"), 100), "", 100, 100)
	second := imageCleanupCreateImage(t, filepath.Join(dir, "b.jpg"), bytes.Repeat([]byte("b"), 101), "", 100, 100)

	resolver := newFakeThumbnailResolver()
	resolver.failFor[first.ID] = true
	svc := NewImagePerceptualHashBackfillService(resolver)
	if _, err := svc.StartImagePerceptualHashBackfill(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	status := waitImagePerceptualHashBackfill(t, svc)
	if status.Failed != 1 || status.Succeeded != 1 {
		t.Fatalf("失败一张后应继续处理下一张，实际 failed=%d succeeded=%d", status.Failed, status.Succeeded)
	}
	var refreshed models.Image
	if err := database.DB.First(&refreshed, second.ID).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if refreshed.PerceptualHash == "" {
		t.Fatal("失败项之后的图片应当被补上")
	}
}

// 补全判"新鲜"的口径必须和清理判"有没有可用指纹"一致。两边分叉的话，补全会报
// 全部完成，而清理面板继续显示同样的待补全数，用户点多少次都不动。
func TestImagePerceptualHashFreshnessMatchesCleanupCount(t *testing.T) {
	setupImageServiceTestDB(t)
	dir := t.TempDir()
	imageCleanupCreateImage(t, filepath.Join(dir, "a.jpg"), bytes.Repeat([]byte("a"), 100), "", 100, 100)
	imageCleanupCreateImage(t, filepath.Join(dir, "b.jpg"), bytes.Repeat([]byte("b"), 101), "", 100, 100)
	// 长度对、源文件也对得上，但不是十六进制：清理那边 ParseUint 失败按 stale 数，
	// 补全这边只比长度的话会记成 Skipped——两边口径一分叉，用户按多少次补全，
	// "还有 N 张没有指纹"都不会变。
	imageCleanupCreateImage(t, filepath.Join(dir, "c.jpg"), bytes.Repeat([]byte("c"), 102), "zzzzzzzzzzzzzzzz", 100, 100)

	before, err := NewImageCleanupService().AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if before.StaleHashCount != 3 {
		t.Fatalf("补全前应有 3 张待补全，实际 %d", before.StaleHashCount)
	}

	svc := NewImagePerceptualHashBackfillService(newFakeThumbnailResolver())
	if _, err := svc.StartImagePerceptualHashBackfill(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	waitImagePerceptualHashBackfill(t, svc)

	after, err := NewImageCleanupService().AnalyzeImageCleanupCandidates()
	if err != nil {
		t.Fatalf("二次分析失败: %v", err)
	}
	if after.StaleHashCount != 0 {
		t.Fatalf("补全之后待补全数应归零，实际 %d", after.StaleHashCount)
	}
}

// 黑名单目录不进目标集（用户裁决 2026-09-20）：唯一的消费方（清理审阅）排黑名单，
// 补全这边不排的话，面板 Total 比真要干的活大一截，任务还会去解码用户明确排除掉的
// 目录——白烧 CPU，补出来的指纹也没人看。
func TestImagePerceptualHashBackfillSkipsBlacklistedDirectories(t *testing.T) {
	setupImageServiceTestDB(t)
	root := t.TempDir()
	excluded := filepath.Join(root, "backup")
	included := filepath.Join(root, "photos")
	hidden := imageCleanupCreateImage(t, filepath.Join(excluded, "hidden.jpg"), bytes.Repeat([]byte("a"), 100), "", 100, 100)
	wanted := imageCleanupCreateImage(t, filepath.Join(included, "wanted.jpg"), bytes.Repeat([]byte("b"), 101), "", 100, 100)

	if err := database.DB.Model(&models.Settings{}).Where("1 = 1").
		Update("image_scan_exclude_paths", excluded).Error; err != nil {
		t.Fatalf("设置图片黑名单失败: %v", err)
	}

	resolver := newFakeThumbnailResolver()
	svc := NewImagePerceptualHashBackfillService(resolver)
	if _, err := svc.StartImagePerceptualHashBackfill(context.Background()); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	status := waitImagePerceptualHashBackfill(t, svc)

	if status.Total != 1 {
		t.Fatalf("Total 应只数黑名单之外的 1 张，实际 %d", status.Total)
	}
	if status.Succeeded != 1 || status.Failed != 0 {
		t.Fatalf("记账不对: succeeded=%d failed=%d skipped=%d", status.Succeeded, status.Failed, status.Skipped)
	}
	for _, id := range resolver.resolvedIDs() {
		if id == hidden.ID {
			t.Fatal("黑名单目录里的图片不该被解码")
		}
	}

	var hiddenRow models.Image
	if err := database.DB.First(&hiddenRow, hidden.ID).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if hiddenRow.PerceptualHash != "" {
		t.Fatalf("黑名单目录里的图片不该被补上指纹，实际 %q", hiddenRow.PerceptualHash)
	}
	var wantedRow models.Image
	if err := database.DB.First(&wantedRow, wanted.ID).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if wantedRow.PerceptualHash == "" {
		t.Fatal("黑名单之外的图片应当被补上指纹")
	}
}
