package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
)

// recordingFFProbe 装一个会把每次调用的目标路径追加到日志文件的 ffprobe stub，
// 返回读取调用次数的闭包。用来证明"库里元数据新鲜时一次都不探测"。
func recordingFFProbe(t *testing.T, root string, fail bool) func() []string {
	t.Helper()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("创建 ffprobe 目录失败: %v", err)
	}
	logPath := filepath.Join(root, "ffprobe-calls.log")
	body := `cat <<JSON
{"streams":[{"width":1920,"height":1080,"duration":"12.0"}],"format":{"duration":"12.0"}}
JSON`
	if fail {
		body = `exit 1`
	}
	script := "#!/bin/bash\ntarget=\"${@: -1}\"\necho \"$target\" >> " + logPath + "\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "ffprobe"), []byte(script), 0755); err != nil {
		t.Fatalf("写入 ffprobe stub 失败: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			return nil
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) == 1 && lines[0] == "" {
			return nil
		}
		return lines
	}
}

// probedFixtureVideo 建一条"已经被成功探测过"的视频记录：四项元数据齐全且 size 与
// 磁盘一致。clipFixtureVideo 不写 Resolution，走不到复用分支。
func probedFixtureVideo(t *testing.T, root, name, content string) models.Video {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("写入文件失败: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 失败: %v", err)
	}
	video := models.Video{
		Name: name, Path: path, Directory: root, Size: info.Size(),
		Duration: 12, Resolution: "1920x1080", Width: 1920, Height: 1080,
	}
	if err := database.DB.Create(&video).Error; err != nil {
		t.Fatalf("创建视频失败: %v", err)
	}
	return video
}

// 库里的时长分辨率还新鲜时不该再探测一遍。早先是每轮对整库无条件跑 ffprobe：
// 本机 1439 个视频、大半在外置盘上，一轮分析要为此等上很久。
func TestCleanupAnalysisSkipsProbeWhenStoredMetadataIsFresh(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	calls := recordingFFProbe(t, root, false)

	probedFixtureVideo(t, root, "a.mp4", "content-a")
	probedFixtureVideo(t, root, "b.mp4", "content-b-longer")

	result, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{
		MinDuration: 5 * time.Second, MinWidth: 480, MinHeight: 320,
	})
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("库内元数据新鲜时不应调用 ffprobe，实际调用 %d 次: %v", len(got), got)
	}
	if result.SkippedMetadata != 0 {
		t.Fatalf("不应有元数据缺失计数，实际 %d", result.SkippedMetadata)
	}
}

// 文件大小和库里对不上就得重探一次——这是扫描侧 needsTechnicalRefreshDuringScan
// 的同一条判据，两处口径必须一致。
func TestCleanupAnalysisProbesWhenStoredSizeIsStale(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	calls := recordingFFProbe(t, root, false)

	video := probedFixtureVideo(t, root, "grown.mp4", "content")
	if err := database.DB.Model(&models.Video{}).Where("id = ?", video.ID).
		Update("size", video.Size+999).Error; err != nil {
		t.Fatalf("制造大小失配失败: %v", err)
	}

	if _, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{}); err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if got := calls(); len(got) != 1 {
		t.Fatalf("大小失配应触发一次探测，实际 %d 次: %v", len(got), got)
	}
}

// 一次探测失败不该把一个真视频从所有类别里抹掉。精确重复只看文件大小与采样哈希，
// 它必须照常出现；只有低清/短视频这两类因为缺元数据才退出。
func TestCleanupAnalysisKeepsProbeFailuresInExactDuplicates(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	calls := recordingFFProbe(t, root, true) // 探测一律失败

	payload := "identical-bytes-for-both"
	first := probedFixtureVideo(t, root, "dup-a.mp4", payload)
	second := probedFixtureVideo(t, root, "dup-b.mp4", payload)
	// 两条记录的 size 都写歪，强制走探测路径，再让探测失败。
	if err := database.DB.Model(&models.Video{}).Where("id IN ?", []uint{first.ID, second.ID}).
		Update("size", 1).Error; err != nil {
		t.Fatalf("制造大小失配失败: %v", err)
	}

	result, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{
		MinDuration: 5 * time.Second, MinWidth: 480, MinHeight: 320,
	})
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if len(calls()) != 2 {
		t.Fatalf("两个视频都该被探测一次，实际 %v", calls())
	}
	if len(result.DuplicateGroups) != 1 {
		t.Fatalf("探测失败的真视频仍应成精确重复组，实际 %d 组", len(result.DuplicateGroups))
	}
	if result.SkippedMetadata != 2 {
		t.Fatalf("两个视频都应计入元数据缺失，实际 %d", result.SkippedMetadata)
	}
	// 拿不到宽高时不能瞎猜成低清，也不能算成短视频。
	if len(result.LowResolution) != 0 || len(result.LowDuration) != 0 {
		t.Fatalf("缺元数据的视频不该进低清/短视频，实际 low_res=%d low_dur=%d",
			len(result.LowResolution), len(result.LowDuration))
	}
}

// 文件读不到（外置盘没挂载是常态）要有一个可见的计数。在有这个字段之前，
// 插着盘和不插盘跑出来的界面长得一模一样，用户只会觉得"检测不准"。
func TestCleanupAnalysisCountsUnavailableFiles(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	recordingFFProbe(t, root, false)

	kept := probedFixtureVideo(t, root, "kept.mp4", "kept-content")
	gone := probedFixtureVideo(t, root, "gone.mp4", "gone-content")
	if err := os.Remove(gone.Path); err != nil {
		t.Fatalf("删除测试文件失败: %v", err)
	}

	result, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if result.SkippedUnavailable != 1 {
		t.Fatalf("缺失文件应计入跳过数，实际 %d", result.SkippedUnavailable)
	}
	// 文件不可访问不是指纹问题，不该混进待补全计数里。
	if result.StaleHashCount != 1 {
		t.Fatalf("只有仍在的那个视频算待补全指纹，实际 %d", result.StaleHashCount)
	}
	_ = kept
}

// 从没回填过感知哈希的视频必须计入待补全。早先只数"有行但失效"的，于是一个从没
// 跑过补全的库显示 0，提示与「重算感知哈希」按钮永不出现，近似重复恒为空。
func TestCleanupAnalysisCountsVideosWithoutPerceptualHashRow(t *testing.T) {
	setupCleanupServiceTestDB(t)
	root := t.TempDir()
	recordingFFProbe(t, root, false)

	hashed := probedFixtureVideo(t, root, "hashed.mp4", "hashed-content")
	seedPerceptualHashRow(t, hashed, "0000000000000000")
	probedFixtureVideo(t, root, "bare-a.mp4", "bare-a-content")
	probedFixtureVideo(t, root, "bare-b.mp4", "bare-b-content-longer")

	result, err := (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("分析失败: %v", err)
	}
	if result.StaleHashCount != 2 {
		t.Fatalf("两个没有哈希行的视频应计入待补全，实际 %d", result.StaleHashCount)
	}

	// 有行但源文件变过的同样算，且不与上面那条重复计数。
	if err := database.DB.Model(&models.VideoPerceptualHash{}).Where("video_id = ?", hashed.ID).
		Update("source_size", 999999).Error; err != nil {
		t.Fatalf("制造失效指纹失败: %v", err)
	}
	result, err = (&CleanupService{}).AnalyzeCleanupCandidates(CleanupCriteria{})
	if err != nil {
		t.Fatalf("二次分析失败: %v", err)
	}
	if result.StaleHashCount != 3 {
		t.Fatalf("未回填 2 + 失效 1 应为 3，实际 %d", result.StaleHashCount)
	}
}
