package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"video-master/models"
)

// 评审 3：路径里带空格时，失败原因不能漏出路径片段。
func TestVideoEditErrorMessageScrubsPathsWithSpaces(t *testing.T) {
	service, media := newFakeEditService(t)
	dir := filepath.Join(t.TempDir(), "My Library", "Season 1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	a, b := media.addFakeSource(t, dir, "Episode One.mkv", 3000), media.addFakeSource(t, dir, "Episode Two.mkv", 3000)
	media.failSource = b.Path
	view := mustEditProject(t, service, models.VideoEditKindMerge, a.ID, b.ID)
	queueAllAcknowledged(t, service, view.ID)
	done := waitEditProject(t, service, view.ID, 10*time.Second, models.VideoEditStatusFailed, models.VideoEditStatusCompleted)
	message := done.Items[0].ErrorMessage
	for _, leak := range []string{"Library", "Season", "Episode"} {
		if strings.Contains(message, leak) {
			t.Fatalf("失败原因漏出路径片段 %q: %q", leak, message)
		}
	}
	if !strings.Contains(message, "<path>") {
		t.Fatalf("路径应擦成 <path>: %q", message)
	}
}

// 评审 4：高清替换精确模式按片段重写长版旁挂字幕（片段时长量化到整帧、<50 ms 间隙被丢弃）。
func TestVideoEditHDPreciseRetimesLongSidecar(t *testing.T) {
	service, media := newFakeEditService(t)
	dir := t.TempDir()
	long, hd := media.addFakeSource(t, dir, "long.mkv", 10000), media.addFakeSource(t, dir, "hd.mkv", 9000)
	writeEditTestFile(t, filepath.Join(dir, "long.srt"), "1\n00:00:06,000 --> 00:00:07,000\n长版\n")
	view := mustEditProject(t, service, models.VideoEditKindHDReplace, long.ID, hd.ID)
	mustUpdateRecipe(t, service, view, func(recipe *EditRecipe) {
		recipe.HDReplace.Segments = []EditHDSegment{
			{LongStartMS: 1000, LongEndMS: 5000, HDStartMS: 0, HDEndMS: 4000, Confirmed: true},
			{LongStartMS: 5030, LongEndMS: 8000, HDStartMS: 4030, HDEndMS: 7000, Confirmed: true},
		}
	})
	project, _ := loadEditProject(context.Background(), view.ID)
	recipe, _ := parseEditRecipe(project.RecipeJSON)
	_, plans, err := service.preflight(context.Background(), project, recipe, nil)
	if err != nil || len(plans) != 1 || plans[0].Sidecar.Mode != "retime" {
		t.Fatalf("高清替换应按片段重写旁挂字幕: %v %+v", err, plans)
	}
	content, err := retimeEditSidecar(plans[0])
	if err != nil || !strings.Contains(string(content), "00:00:05,970 --> 00:00:06,970\n长版") {
		t.Fatalf("丢弃 30 ms 间隙后字幕应前移 30 ms: %v\n%s", err, content)
	}
}

// 评审 5：快速模式切点前移到不晚于移除区间开始处时什么也没移除，预检必须报错。
func TestVideoEditFastCutBeforeRemoveStartIsError(t *testing.T) {
	service, media := newFakeEditService(t)
	video := media.addFakeSource(t, t.TempDir(), "ep.mkv", 10000)
	view := mustEditProject(t, service, models.VideoEditKindTrimIntro, video.ID)
	view = mustUpdateRecipe(t, service, view, func(recipe *EditRecipe) {
		recipe.TrimIntro.Items[0] = EditTrimItem{VideoID: video.ID, RemoveStartMS: 2000, RemoveEndMS: 4300, Origin: EditOriginManual, Confirmed: true}
	})
	if _, err := service.UpdateRecipe(context.Background(), EditRecipeUpdateRequest{ProjectID: view.ID, ExpectedRevision: view.Revision,
		Recipe: view.Recipe, Mode: models.VideoEditModeFast}); err != nil {
		t.Fatal(err)
	}
	pre, err := service.PreflightProject(context.Background(), view.ID)
	if err != nil || !editIssueCodes(pre.Errors)["fast_cut_invalid"] {
		t.Fatalf("关键帧在移除区间开始之前时应报 fast_cut_invalid: %v %+v", err, pre.Errors)
	}
}

// 评审 8：检查输出名时的权限等错误不能报成「均已被占用」；超长名字按字节截断，不触发 ENAMETOOLONG。
func TestVideoEditOutputCheckErrorIsNotConflict(t *testing.T) {
	service, media := newFakeEditService(t)
	dir := t.TempDir()
	a, b := media.addFakeSource(t, dir, "a.mkv", 3000), media.addFakeSource(t, dir, "b.mkv", 3000)
	view := mustEditProject(t, service, models.VideoEditKindMerge, a.ID, b.ID)
	editOutputLstat = func(path string) (os.FileInfo, error) {
		if strings.Contains(path, "(合并)") {
			return nil, &os.PathError{Op: "lstat", Path: path, Err: syscall.EACCES}
		}
		return os.Lstat(path)
	}
	t.Cleanup(func() { editOutputLstat = os.Lstat })
	pre, err := service.PreflightProject(context.Background(), view.ID)
	codes := editIssueCodes(pre.Errors)
	if err != nil || codes["output_conflict"] || !codes["output_unavailable"] {
		t.Fatalf("权限错误应报 output_unavailable: %v %+v", err, pre.Errors)
	}
	if name := sanitizeEditBaseName(strings.Repeat("长", 150)); len(name) > editMaxBaseNameBytes {
		t.Fatalf("文件基本名应按字节截断到 %d 以内，实际 %d 字节", editMaxBaseNameBytes, len(name))
	}
}

// 评审 9a：只在排队、没有运行项时取消的项目不能在 cancelRequested 里留下记录。
func TestVideoEditCancelQueuedLeavesNoCancelFlag(t *testing.T) {
	service, media := newFakeEditService(t)
	pauseEditWorker(service)
	view, _ := queuedMerge(t, service, media)
	if err := service.CancelProject(context.Background(), view.ID); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	_, leaked := service.cancelRequested[view.ID]
	service.mu.Unlock()
	if leaked {
		t.Fatal("排队项目取消后不应留下 cancelRequested")
	}
}

// 评审 9b：片库来源是指向普通文件的符号链接时照常预检（指纹取解析后的文件）。
func TestVideoEditAcceptsSymlinkedSource(t *testing.T) {
	service, media := newFakeEditService(t)
	dir := t.TempDir()
	real := media.addFakeSource(t, dir, "real.mkv", 3000)
	other := media.addFakeSource(t, dir, "other.mkv", 3000)
	link := filepath.Join(dir, "link.mkv")
	if err := os.Symlink(real.Path, link); err != nil {
		t.Skip("无法创建符号链接")
	}
	media.setProbe(link, fakeEditProbeJSON(320, 180, 3000, []string{"jpn", "eng"}, 1))
	linked := insertEditVideo(t, link)
	view := mustEditProject(t, service, models.VideoEditKindMerge, linked.ID, other.ID)
	pre, err := service.PreflightProject(context.Background(), view.ID)
	if err != nil || len(pre.Errors) != 0 || pre.Sources[0].Error != "" {
		t.Fatalf("指向普通文件的符号链接来源应可用: %v %+v", err, pre.Errors)
	}
}

// 评审 9c：AnalyzeEditProject 校验 expected_revision。
func TestVideoEditAnalyzeChecksRevision(t *testing.T) {
	service, media := newFakeEditService(t)
	fakeEditAnalysisReaders(service)
	dir := t.TempDir()
	long, hd := media.addFakeSource(t, dir, "long.mkv", 6000), media.addFakeSource(t, dir, "hd.mkv", 6000)
	view := mustEditProject(t, service, models.VideoEditKindHDReplace, long.ID, hd.ID)
	_, err := service.AnalyzeProject(context.Background(), EditAnalyzeRequest{ProjectID: view.ID, ExpectedRevision: view.Revision + 1})
	if editErrorCodeOf(err) != "edit_project_conflict" {
		t.Fatalf("revision 不符应报 edit_project_conflict: %v", err)
	}
	if current, _ := service.GetProject(context.Background(), view.ID); current.Status != models.VideoEditStatusDraft {
		t.Fatalf("冲突时不能进入 analyzing: %s", current.Status)
	}
}

// 评审 10：选好名字到 rename 之间目标被别人占用时不能覆盖（排他 rename），顺延到下一个空闲名字。
func TestVideoEditPublishNeverOverwritesRacingFile(t *testing.T) {
	service, media := newFakeEditService(t)
	raced := ""
	service.beforePublishRename = func(target string) {
		if raced == "" {
			raced = target
			writeEditTestFile(t, target, "someone else")
		}
	}
	view, _ := queuedMerge(t, service, media)
	done := waitEditProject(t, service, view.ID, 10*time.Second, models.VideoEditStatusCompleted, models.VideoEditStatusFailed)
	if got := readFileString(t, raced); got != "someone else" {
		t.Fatalf("抢先出现的同名文件被覆盖: %q", got)
	}
	_, path := outputPathOf(t, done, 1)
	if path == raced || filepath.Base(path) != "a (合并) (2).mkv" {
		t.Fatalf("应顺延到 (2)，实际 %s", path)
	}
}
