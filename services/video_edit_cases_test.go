package services

import (
	"context"
	"testing"

	"video-master/models"
)

// 配方 CAS：revision 谓词不符或已离开 draft 时零行更新，重读后报 edit_project_conflict。
func TestVideoEditUpdateRecipeCASConflict(t *testing.T) {
	service, media := newFakeEditService(t)
	dir := t.TempDir()
	a, b := media.addFakeSource(t, dir, "a.mkv", 4000), media.addFakeSource(t, dir, "b.mkv", 4000)
	view := mustEditProject(t, service, models.VideoEditKindMerge, a.ID, b.ID)
	if view.Revision != 1 || view.Mode != models.VideoEditModePrecise || view.Status != models.VideoEditStatusDraft {
		t.Fatalf("新项目应为 draft/revision 1/precise: %+v", view)
	}
	stale := view
	updated := mustUpdateRecipe(t, service, view, func(recipe *EditRecipe) { recipe.Merge.SpecSourceVideoID = b.ID })
	if updated.Revision != 2 || updated.Recipe.Merge.SpecSourceVideoID != b.ID {
		t.Fatalf("更新后 revision 应为 2: %+v", updated)
	}
	_, err := service.UpdateRecipe(context.Background(), EditRecipeUpdateRequest{ProjectID: view.ID, ExpectedRevision: stale.Revision, Recipe: stale.Recipe})
	if editErrorCodeOf(err) != "edit_project_conflict" {
		t.Fatalf("过期 revision 应报 edit_project_conflict，实际 %v", err)
	}
	current, _ := service.GetProject(context.Background(), view.ID)
	if current.Revision != 2 || current.Recipe.Merge.SpecSourceVideoID != b.ID {
		t.Fatalf("冲突的更新不能落库: %+v", current)
	}
	pauseEditWorker(service)
	queueAllAcknowledged(t, service, view.ID)
	_, err = service.UpdateRecipe(context.Background(), EditRecipeUpdateRequest{ProjectID: view.ID, ExpectedRevision: current.Revision, Recipe: current.Recipe})
	if editErrorCodeOf(err) != "edit_project_conflict" {
		t.Fatalf("排队后配方只读，应报 edit_project_conflict，实际 %v", err)
	}
	_, err = service.UpdateRecipe(context.Background(), EditRecipeUpdateRequest{ProjectID: view.ID, ExpectedRevision: 2,
		Recipe: EditRecipe{Merge: &EditMergeRecipe{Sources: []EditMergeSource{{VideoID: a.ID}}}}})
	if editErrorCodeOf(err) != "recipe_invalid" {
		t.Fatalf("少于两个来源应报 recipe_invalid，实际 %v", err)
	}
}

// 去片头导出前校验：区间倒置、越界、未确认都阻止导出；合法项照常生成输出摘要。
func TestVideoEditTrimPreflightValidatesRanges(t *testing.T) {
	service, media := newFakeEditService(t)
	dir := t.TempDir()
	videos := []models.Video{}
	for _, name := range []string{"1.mkv", "2.mkv", "3.mkv", "4.mkv"} {
		videos = append(videos, media.addFakeSource(t, dir, name, 10000))
	}
	view := mustEditProject(t, service, models.VideoEditKindTrimIntro, videos[0].ID, videos[1].ID, videos[2].ID, videos[3].ID)
	mustUpdateRecipe(t, service, view, func(recipe *EditRecipe) {
		recipe.TrimIntro.Items = []EditTrimItem{
			{VideoID: videos[0].ID, RemoveStartMS: 5000, RemoveEndMS: 3000, Origin: EditOriginManual, Confirmed: true},
			{VideoID: videos[1].ID, RemoveStartMS: 0, RemoveEndMS: 12000, Origin: EditOriginManual, Confirmed: true},
			{VideoID: videos[2].ID, RemoveStartMS: 0, RemoveEndMS: 2000, Origin: EditOriginDetected, DetectStatus: EditDetectDetected},
			{VideoID: videos[3].ID, RemoveStartMS: 0, RemoveEndMS: 2000, Origin: EditOriginUniform, Confirmed: true},
		}
	})
	pre, err := service.PreflightProject(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, issue := range pre.Errors {
		keys[issue.Key] = true
	}
	for _, want := range []string{"range_invalid:1", "range_invalid:2", "unconfirmed:3"} {
		if !keys[want] {
			t.Fatalf("缺少错误 %s: %+v", want, pre.Errors)
		}
	}
	if keys["range_invalid:4"] || keys["unconfirmed:4"] || pre.Ready {
		t.Fatalf("第 4 项合法、整体不可导出: ready=%v errors=%+v", pre.Ready, pre.Errors)
	}
	if len(pre.Outputs) != 2 || pre.Outputs[1].Seq != 4 || pre.Outputs[1].PlannedName != "4 (去片头).mkv" || pre.Outputs[1].DurationMS != 8000 {
		t.Fatalf("合法项应有输出摘要: %+v", pre.Outputs)
	}
}

// 高清替换导出前校验：长版区间重叠、长版/高清时长不等、未确认段；以及长版放大的警告须确认。
func TestVideoEditHDPreflightValidatesSegmentsAndWarnings(t *testing.T) {
	service, media := newFakeEditService(t)
	dir := t.TempDir()
	long := media.addFakeSource(t, dir, "long.mkv", 60000)
	hd := media.addFakeSource(t, dir, "hd.mkv", 30000)
	media.setProbe(hd.Path, fakeEditProbeJSON(640, 360, 30000, []string{"jpn", "eng"}, 0))
	view := mustEditProject(t, service, models.VideoEditKindHDReplace, long.ID, hd.ID)
	view = mustUpdateRecipe(t, service, view, func(recipe *EditRecipe) {
		recipe.HDReplace.Segments = []EditHDSegment{
			{LongStartMS: 1000, LongEndMS: 5000, HDStartMS: 0, HDEndMS: 4000, Confirmed: true},
			{LongStartMS: 4000, LongEndMS: 8000, HDStartMS: 4000, HDEndMS: 8000, Confirmed: true},
			{LongStartMS: 10000, LongEndMS: 12000, HDStartMS: 9000, HDEndMS: 12000, Confirmed: true},
			{LongStartMS: 20000, LongEndMS: 22000, HDStartMS: 20000, HDEndMS: 22000},
		}
	})
	pre, err := service.PreflightProject(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	codes := editIssueCodes(pre.Errors)
	for _, want := range []string{"range_overlap", "range_invalid", "unconfirmed"} {
		if !codes[want] {
			t.Fatalf("缺少错误码 %s: %+v", want, pre.Errors)
		}
	}
	view = mustUpdateRecipe(t, service, view, func(recipe *EditRecipe) {
		recipe.HDReplace.Segments = []EditHDSegment{{LongStartMS: 1000, LongEndMS: 5000, HDStartMS: 0, HDEndMS: 4000, Confirmed: true}}
	})
	pre, err = service.PreflightProject(context.Background(), view.ID)
	if err != nil || len(pre.Errors) != 0 || !editIssueCodes(pre.Warnings)["upscale"] {
		t.Fatalf("合法配方只应给出放大警告: %v errors=%+v warnings=%+v", err, pre.Errors, pre.Warnings)
	}
	pauseEditWorker(service)
	result, err := service.QueueProject(context.Background(), EditQueueRequest{ProjectID: view.ID, ExpectedRevision: view.Revision})
	if err != nil || result.Queued || len(result.Unacknowledged) != 1 {
		t.Fatalf("未确认警告时应拒绝排队并列出 key: %+v %v", result, err)
	}
	result, err = service.QueueProject(context.Background(), EditQueueRequest{ProjectID: view.ID, ExpectedRevision: view.Revision,
		AcknowledgedWarnings: result.Unacknowledged})
	if err != nil || !result.Queued || result.Project.Status != models.VideoEditStatusQueued || len(result.Project.Items) != 1 {
		t.Fatalf("确认后应排队: %+v %v", result, err)
	}
	if got := result.Project.AcknowledgedWarnings; len(got) != 1 || got[0] != result.Preflight.Warnings[0].Key {
		t.Fatalf("应记录已确认的警告: %+v", got)
	}
}

// Q9：无法按语言+序号对应的音轨/字幕列入冲突并阻止导出；配方给出明确选择（静音、指定流、整轨不导出）后放行。
func TestVideoEditMergeTrackConflictsNeedExplicitChoice(t *testing.T) {
	service, media := newFakeEditService(t)
	dir := t.TempDir()
	a := media.addFakeSource(t, dir, "a.mkv", 4000)
	b := media.addFakeSource(t, dir, "b.mkv", 4000)
	media.setProbe(b.Path, fakeEditProbeJSON(320, 180, 4000, []string{"kor"}, 0))
	view := mustEditProject(t, service, models.VideoEditKindMerge, a.ID, b.ID)
	pre, err := service.PreflightProject(context.Background(), view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pre.Conflicts) != 3 || !editIssueCodes(pre.Errors)["track_conflict"] {
		t.Fatalf("jpn/eng 两条音轨与一条字幕在 b 上都对不上，应有 3 处冲突: %+v", pre.Conflicts)
	}
	for _, conflict := range pre.Conflicts {
		if conflict.VideoID != b.ID || len(conflict.AllowedFills) == 0 {
			t.Fatalf("冲突应指向 b 并给出可选项: %+v", conflict)
		}
	}
	view = mustUpdateRecipe(t, service, view, func(recipe *EditRecipe) {
		recipe.Tracks.Audio = []EditTrackChoice{
			{Output: 0, VideoID: b.ID, Choice: EditTrackChoiceStream, StreamIndex: 1},
			{Output: 1, VideoID: b.ID, Choice: EditTrackChoiceSilence},
		}
		recipe.Tracks.Subtitle = []EditTrackChoice{{Output: 0, VideoID: 0, Choice: EditTrackChoiceDrop}}
	})
	pre, err = service.PreflightProject(context.Background(), view.ID)
	if err != nil || len(pre.Errors) != 0 || len(pre.Conflicts) != 0 {
		t.Fatalf("明确选择后冲突应解除: %v errors=%+v conflicts=%+v", err, pre.Errors, pre.Conflicts)
	}
	out := pre.Outputs[0]
	if len(out.AudioTracks) != 2 || out.AudioTracks[1].Mappings[1].Fill != EditTrackChoiceSilence || !out.SubtitleTracks[0].Dropped {
		t.Fatalf("映射摘要应反映选择: %+v", out)
	}
	if !editIssueCodes(pre.Warnings)["track_fill"] || out.Container != "mkv" {
		t.Fatalf("静音填充须警告确认，两条音轨输出 mkv: %+v %s", pre.Warnings, out.Container)
	}
	if pre.Fast.Available {
		t.Fatal("有静音填充时快速模式不可用")
	}
}
