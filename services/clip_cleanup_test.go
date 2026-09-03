package services

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// clipFixtureVideo 建一个真实文件 + 一条视频记录。
// 名字要避开 mockFFProbe 里 short.mp4 / small.mp4 两个特例，否则时长与分辨率会被改写。
func clipFixtureVideo(t *testing.T, root, name, content string) models.Video {
	t.Helper()
	path := filepath.Join(root, name)
	mustWriteSizedFile(t, path, []byte(content))
	video := models.Video{
		Name: name, Path: path, Directory: root,
		Size: int64(len(content)), Duration: 12, Width: 1920, Height: 1080,
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	return video
}

// seedFrameHashSequence 直接写一条有效序列（指纹取自磁盘上的文件）。
// 回填链路由 frame_hash_service_test.go 覆盖，这里要测的是匹配与清理类别。
func seedFrameHashSequence(t *testing.T, video models.Video, hashes []uint64) {
	t.Helper()
	info, err := os.Stat(video.Path)
	if err != nil {
		t.Fatalf("读取文件信息失败: %v", err)
	}
	row := models.VideoFrameHashSequence{
		VideoID:         video.ID,
		IntervalMS:      clipFrameIntervalMS,
		Hashes:          encodeFrameHashes(hashes),
		FrameCount:      len(hashes),
		SourceSize:      info.Size(),
		SourceModTimeNS: info.ModTime().UnixNano(),
		ComputedAt:      time.Now(),
	}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatalf("写入帧哈希序列失败(%s): %v", dbtest.Backend(), err)
	}
}

func randomFrameHashes(seed int64, count int) []uint64 {
	source := rand.New(rand.NewSource(seed))
	hashes := make([]uint64, count)
	for index := range hashes {
		hashes[index] = source.Uint64()
	}
	return hashes
}

// 截取片段成候选（AC-17）：完整片 40 帧（80 秒），片段是它第 10 帧起的 15 帧。
func TestCleanupAnalysisReportsClipCandidate(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	full := clipFixtureVideo(t, root, "feature.mp4", "feature-content-original")
	clip := clipFixtureVideo(t, root, "excerpt.mp4", "excerpt")
	unrelated := clipFixtureVideo(t, root, "other.mp4", "other-content")

	fullHashes := randomFrameHashes(1, 40)
	seedFrameHashSequence(t, full, fullHashes)
	seedFrameHashSequence(t, clip, fullHashes[10:25])
	seedFrameHashSequence(t, unrelated, randomFrameHashes(2, 15))

	result, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(result.ClipGroups) != 1 {
		t.Fatalf("应产出 1 个截取候选，实际 %d: %#v", len(result.ClipGroups), result.ClipGroups)
	}
	group := result.ClipGroups[0]
	if group.Full.ID != full.ID || group.Clip.ID != clip.ID {
		t.Fatalf("完整片与片段的角色不对: full=%d clip=%d", group.Full.ID, group.Clip.ID)
	}
	if group.OffsetSeconds != 20 {
		t.Fatalf("偏移应为 20 秒（第 10 帧 × 2 秒），实际 %.1f", group.OffsetSeconds)
	}
	if group.MatchRate != 1 {
		t.Fatalf("逐帧相同时命中率应为 1，实际 %.3f", group.MatchRate)
	}
	if group.EstimatedSavings != clip.Size {
		t.Fatalf("可释放空间应等于片段体积: %d != %d", group.EstimatedSavings, clip.Size)
	}
	// 有序列的视频不再计入待补全数；这三个都补过了。
	if result.StaleFrameHashCount != 0 {
		t.Fatalf("三个视频都有有效序列时待补全数应为 0，实际 %d", result.StaleFrameHashCount)
	}
}

// 没有序列与序列失效都算"待补全"，面板据此提示可以补全帧哈希（设计 4.6.4）。
func TestCleanupAnalysisCountsPendingAndStaleFrameHashes(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	fresh := clipFixtureVideo(t, root, "fresh.mp4", "fresh-content")
	stale := clipFixtureVideo(t, root, "stale.mp4", "stale-content")
	clipFixtureVideo(t, root, "never.mp4", "never-computed")

	seedFrameHashSequence(t, fresh, randomFrameHashes(3, 30))
	seedFrameHashSequence(t, stale, randomFrameHashes(4, 30))
	// 源文件重编码：这一行的指纹失效。
	mustWriteSizedFile(t, stale.Path, []byte("stale-content-re-encoded"))

	result, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if result.StaleFrameHashCount != 2 {
		t.Fatalf("失效 1 个 + 从未回填 1 个 = 2，实际 %d", result.StaleFrameHashCount)
	}
	if len(result.ClipGroups) != 0 {
		t.Fatalf("只剩一条有效序列时不该有截取候选: %#v", result.ClipGroups)
	}
}

// 已经被精确重复认领的对不再作为截取出现（设计 4.6.4）。
//
// 两个文件内容一致（因此是精确重复），序列却按"完整片 + 片段"手工写入——
// 现实里不会这样，但这正是这道排除线唯一能被独立验证的形态。
func TestCleanupAnalysisExcludesExactDuplicatePairsFromClips(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	payload := "identical-payload-for-duplicate-detection"
	left := clipFixtureVideo(t, root, "left.mp4", payload)
	right := clipFixtureVideo(t, root, "right.mp4", payload)

	fullHashes := randomFrameHashes(5, 40)
	seedFrameHashSequence(t, left, fullHashes)
	seedFrameHashSequence(t, right, fullHashes[5:25])

	result, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(result.DuplicateGroups) != 1 {
		t.Fatalf("同内容文件应先被判为精确重复，实际 %d 组", len(result.DuplicateGroups))
	}
	if len(result.ClipGroups) != 0 {
		t.Fatalf("精确重复对不该再报成截取候选: %#v", result.ClipGroups)
	}
}

// 忽略之后不再出现；任一侧重编码（指纹变了）则重新出现（D-028）。
func TestDismissClipCandidateHidesPairUntilFingerprintChanges(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	full := clipFixtureVideo(t, root, "movie.mp4", "movie-content-original")
	clip := clipFixtureVideo(t, root, "cut.mp4", "cut")

	fullHashes := randomFrameHashes(6, 40)
	seedFrameHashSequence(t, full, fullHashes)
	seedFrameHashSequence(t, clip, fullHashes[8:24])

	result, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(result.ClipGroups) != 1 {
		t.Fatalf("忽略之前应有 1 个候选，实际 %d", len(result.ClipGroups))
	}

	if err := DismissClipCandidate(full.ID, clip.ID); err != nil {
		t.Fatalf("忽略失败: %v", err)
	}
	// 重复忽略同一对是幂等的：唯一键 + 刷新指纹。
	if err := DismissClipCandidate(full.ID, clip.ID); err != nil {
		t.Fatalf("重复忽略应当幂等: %v", err)
	}
	var dismissals int64
	if err := database.DB.Model(&models.ClipDismissal{}).Count(&dismissals).Error; err != nil {
		t.Fatalf("统计忽略记录失败: %v", err)
	}
	if dismissals != 1 {
		t.Fatalf("同一对只该有一条忽略记录，实际 %d", dismissals)
	}

	result, err = (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(result.ClipGroups) != 0 {
		t.Fatalf("忽略之后不该再报出: %#v", result.ClipGroups)
	}

	// 片段重编码：指纹变了，这是一份新素材，候选重新出现。
	mustWriteSizedFile(t, clip.Path, []byte("cut-re-encoded"))
	seedFrameHashSequenceReplacing(t, clip, fullHashes[8:24])
	result, err = (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(result.ClipGroups) != 1 {
		t.Fatalf("重编码之后忽略应失效、候选重新出现，实际 %d", len(result.ClipGroups))
	}

	// 没有序列的视频无法记录指纹，忽略要明确失败而不是悄悄存一条空指纹。
	other := clipFixtureVideo(t, root, "nosequence.mp4", "no-sequence-content")
	if err := DismissClipCandidate(full.ID, other.ID); err == nil {
		t.Fatal("缺少帧哈希序列时忽略应当报错")
	}
	if err := DismissClipCandidate(full.ID, full.ID); err == nil {
		t.Fatal("完整片与片段相同时应当报错")
	}
	if err := DismissClipCandidate(0, clip.ID); err == nil {
		t.Fatal("缺少视频 ID 时应当报错")
	}
}

func seedFrameHashSequenceReplacing(t *testing.T, video models.Video, hashes []uint64) {
	t.Helper()
	if err := database.DB.Where("video_id = ?", video.ID).Delete(&models.VideoFrameHashSequence{}).Error; err != nil {
		t.Fatalf("删除旧序列失败: %v", err)
	}
	seedFrameHashSequence(t, video, hashes)
}

// 一个片段只报一条候选：同一段素材同时对上两个完整片时，报命中率最高的那一条，
// 否则"可释放空间"会把同一个文件算好几遍。
func TestCleanupAnalysisReportsOneCandidatePerClip(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	first := clipFixtureVideo(t, root, "version-a.mp4", "version-a-content-long")
	second := clipFixtureVideo(t, root, "version-b.mp4", "version-b-content-longer")
	clip := clipFixtureVideo(t, root, "shared-cut.mp4", "shared")

	fullHashes := randomFrameHashes(7, 40)
	seedFrameHashSequence(t, first, fullHashes)
	seedFrameHashSequence(t, second, fullHashes)
	seedFrameHashSequence(t, clip, fullHashes[12:30])

	result, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(result.ClipGroups) != 1 {
		t.Fatalf("同一片段只该报一条候选，实际 %d: %#v", len(result.ClipGroups), result.ClipGroups)
	}
	if result.ClipGroups[0].Clip.ID != clip.ID {
		t.Fatalf("被报出的片段应是 shared-cut.mp4，实际 %d", result.ClipGroups[0].Clip.ID)
	}
}

// 旧五类的计算路径与结果快照（AC-18）：截取片段是加在分析末尾的独立一步，
// 精确重复 / 近似重复 / 同源 / 短视频 / 低清的结果一个字节都不许变。
//
// 这条用例在加入截取片段之前就该是绿的——把 analyzeCleanupCandidates 末尾那一段
// 摘掉重跑仍然通过，才算证明了"路径没被碰过"。
//
// **扫描根裁剪是既有行为**（本批开工前工作树里就有的未提交改动：`library_service.go`
// 的 applyScanRootScope 已经把旧五类的输入集限制在扫描根之内），本快照钉住的是
// **含裁剪的现状**：夹具在扫描根内外各放一整套五类素材，断言只有根内那一套出现。
// 没有这一层，`loadScanRootScope` 为空时裁剪是 no-op，快照对这道筛是结构性失明的。
func TestCleanupAnalysisLegacyCategoriesSnapshot(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	mockFFProbe(t, root)
	insideDir := filepath.Join(root, "inside")
	outsideDir := filepath.Join(root, "outside")
	for _, dir := range []string{insideDir, outsideDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
	}
	// 扫描根只覆盖 inside：outside 那一套素材应当整体不出现在任何类别里。
	if err := database.DB.Create(&models.ScanDirectory{Path: insideDir}).Error; err != nil {
		t.Fatalf("创建扫描目录失败: %v", err)
	}

	// ---- 根内：五类各一份 ----
	duplicatePayload := "exact-duplicate-payload"
	duplicateA := clipFixtureVideo(t, insideDir, "dup-a.mp4", duplicatePayload)
	duplicateB := clipFixtureVideo(t, insideDir, "dup-b.mp4", duplicatePayload)
	nearA := clipFixtureVideo(t, insideDir, "near-a.mp4", "near-a-content")
	nearB := clipFixtureVideo(t, insideDir, "near-b.mp4", "near-b-content-longer")
	sameA := clipFixtureVideo(t, insideDir, "same-a.mp4", "same-a-content")
	sameB := clipFixtureVideo(t, insideDir, "same-b.mp4", "same-b-content-longer")
	short := clipFixtureVideo(t, insideDir, "short.mp4", "short-content")
	small := clipFixtureVideo(t, insideDir, "small.mp4", "small-content")

	seedPerceptualHashRow(t, nearA, "0000000000000000")
	seedPerceptualHashRow(t, nearB, "0000000000000001")
	if err := database.DB.Create(&models.VideoSameSourceRelation{
		VideoAID: sameA.ID, VideoBID: sameB.ID,
		VideoAFingerprint: "fa", VideoBFingerprint: "fb",
		Status: models.VideoSameSourceStatusDetected, Confidence: "high",
		Reasoning: "画面指纹一致", DetectionVersion: "test",
	}).Error; err != nil {
		t.Fatalf("创建同源关系失败: %v", err)
	}

	// ---- 根外：同样五类各一份，必须全部被裁掉 ----
	outsidePayload := "outside-duplicate-payload"
	outDuplicateA := clipFixtureVideo(t, outsideDir, "out-dup-a.mp4", outsidePayload)
	outDuplicateB := clipFixtureVideo(t, outsideDir, "out-dup-b.mp4", outsidePayload)
	outNearA := clipFixtureVideo(t, outsideDir, "out-near-a.mp4", "out-near-a-content")
	outNearB := clipFixtureVideo(t, outsideDir, "out-near-b.mp4", "out-near-b-content-longer")
	outSameA := clipFixtureVideo(t, outsideDir, "out-same-a.mp4", "out-same-a-content")
	outSameB := clipFixtureVideo(t, outsideDir, "out-same-b.mp4", "out-same-b-content-longer")
	outShort := clipFixtureVideo(t, outsideDir, "short.mp4", "outside-short-content")
	outSmall := clipFixtureVideo(t, outsideDir, "small.mp4", "outside-small-content")

	// 根外这一对的哈希彼此相邻、与根内那一对相距很远：裁剪一旦失效，
	// 近似重复就会变成两组而不是一组，下面的断言当场炸。
	seedPerceptualHashRow(t, outNearA, "ffffffffffffff00")
	seedPerceptualHashRow(t, outNearB, "ffffffffffffff01")
	if err := database.DB.Create(&models.VideoSameSourceRelation{
		VideoAID: outSameA.ID, VideoBID: outSameB.ID,
		VideoAFingerprint: "ofa", VideoBFingerprint: "ofb",
		Status: models.VideoSameSourceStatusDetected, Confidence: "high",
		Reasoning: "根外同源", DetectionVersion: "test",
	}).Error; err != nil {
		t.Fatalf("创建根外同源关系失败: %v", err)
	}

	result, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{
		MinDuration: 5 * time.Second, MinWidth: 480, MinHeight: 320,
	})
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}

	if len(result.DuplicateGroups) != 1 {
		t.Fatalf("精确重复应为 1 组（根外那一组被裁掉），实际 %d", len(result.DuplicateGroups))
	}
	duplicateIDs := append([]uint{result.DuplicateGroups[0].Original.ID}, videoIDs(result.DuplicateGroups[0].Candidates)...)
	if !sameUintSet(duplicateIDs, []uint{duplicateA.ID, duplicateB.ID}) {
		t.Fatalf("精确重复组成员不对: %v", duplicateIDs)
	}
	if result.DuplicateGroups[0].Reason != "文件大小和采样哈希一致" {
		t.Fatalf("精确重复的说明文案变了: %q", result.DuplicateGroups[0].Reason)
	}

	if len(result.NearDuplicateGroups) != 1 {
		t.Fatalf("近似重复应为 1 组（根外那一组被裁掉），实际 %d", len(result.NearDuplicateGroups))
	}
	nearIDs := append([]uint{result.NearDuplicateGroups[0].Original.ID}, videoIDs(result.NearDuplicateGroups[0].Candidates)...)
	if !sameUintSet(nearIDs, []uint{nearA.ID, nearB.ID}) {
		t.Fatalf("近似重复组成员不对: %v", nearIDs)
	}
	if result.NearDuplicateGroups[0].Reason != "三帧感知哈希接近，可能是同片不同转码（不会默认选中）" {
		t.Fatalf("近似重复的说明文案变了: %q", result.NearDuplicateGroups[0].Reason)
	}

	if len(result.SameSourceGroups) != 1 {
		t.Fatalf("同源应为 1 组（根外那一组被裁掉），实际 %d", len(result.SameSourceGroups))
	}
	sameGroup := result.SameSourceGroups[0]
	if !sameUintSet([]uint{sameGroup.Preferred.ID, sameGroup.Alternative.ID}, []uint{sameA.ID, sameB.ID}) {
		t.Fatalf("同源组成员不对: preferred=%d alternative=%d", sameGroup.Preferred.ID, sameGroup.Alternative.ID)
	}
	if sameGroup.Confidence != "high" || sameGroup.Reason != "画面指纹一致" {
		t.Fatalf("同源组的判断说明变了: %+v", sameGroup)
	}
	if sameGroup.EstimatedSavings != sameGroup.Alternative.Size {
		t.Fatalf("同源可释放空间应等于可替代版本体积: %d != %d", sameGroup.EstimatedSavings, sameGroup.Alternative.Size)
	}

	if !sameUintSet(videoIDs(result.LowDuration), []uint{short.ID}) {
		t.Fatalf("短视频候选不对（根外的 short.mp4 应被裁掉）: %v", videoIDs(result.LowDuration))
	}
	if !sameUintSet(videoIDs(result.LowResolution), []uint{small.ID}) {
		t.Fatalf("低清候选不对（根外的 small.mp4 应被裁掉）: %v", videoIDs(result.LowResolution))
	}
	if result.StaleHashCount != 0 {
		t.Fatalf("感知哈希待重算数应为 0，实际 %d", result.StaleHashCount)
	}

	// 逐个点名：根外的八个视频一个都不许出现在任何类别里。
	outsideIDs := map[uint]string{
		outDuplicateA.ID: "out-dup-a.mp4", outDuplicateB.ID: "out-dup-b.mp4",
		outNearA.ID: "out-near-a.mp4", outNearB.ID: "out-near-b.mp4",
		outSameA.ID: "out-same-a.mp4", outSameB.ID: "out-same-b.mp4",
		outShort.ID: "outside/short.mp4", outSmall.ID: "outside/small.mp4",
	}
	appeared := make([]uint, 0)
	for _, group := range append(append([]CleanupDuplicateGroup(nil), result.DuplicateGroups...), result.NearDuplicateGroups...) {
		appeared = append(appeared, group.Original.ID)
		appeared = append(appeared, videoIDs(group.Candidates)...)
	}
	for _, group := range result.SameSourceGroups {
		appeared = append(appeared, group.Preferred.ID, group.Alternative.ID)
	}
	appeared = append(appeared, videoIDs(result.LowDuration)...)
	appeared = append(appeared, videoIDs(result.LowResolution)...)
	for _, group := range result.ClipGroups {
		appeared = append(appeared, group.Full.ID, group.Clip.ID)
	}
	for _, id := range appeared {
		if name, outside := outsideIDs[id]; outside {
			t.Fatalf("扫描根之外的 %s（id=%d）不该出现在清理候选里", name, id)
		}
	}

	// 新增的两项同样按扫描根裁剪：根内 8 个视频都还没有帧哈希序列。
	if result.StaleFrameHashCount != 8 {
		t.Fatalf("待补全帧哈希数应只数根内的 8 个视频，实际 %d", result.StaleFrameHashCount)
	}
	if len(result.ClipGroups) != 0 {
		t.Fatalf("没有任何序列时不该有截取候选: %#v", result.ClipGroups)
	}
}

func seedPerceptualHashRow(t *testing.T, video models.Video, hash string) {
	t.Helper()
	info, err := os.Stat(video.Path)
	if err != nil {
		t.Fatalf("读取文件信息失败: %v", err)
	}
	row := models.VideoPerceptualHash{
		VideoID: video.ID, SourceSize: info.Size(), SourceModTimeNS: info.ModTime().UnixNano(),
		HashEarly: hash, HashMiddle: hash, HashLate: hash, ComputedAt: time.Now(),
	}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatalf("写入感知哈希失败: %v", err)
	}
}

func videoIDs(videos []models.Video) []uint {
	ids := make([]uint, 0, len(videos))
	for _, video := range videos {
		ids = append(ids, video.ID)
	}
	return ids
}

func sameUintSet(left, right []uint) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[uint]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}
