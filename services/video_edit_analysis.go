package services

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"video-master/models"
	"video-master/services/editalign"

	"gorm.io/gorm"
)

// 分析（合同「分析：片头识别与分段对齐」）：同一时间一轮、可取消，状态 analyzing，进度经 video-edit-state。
// 帧读取与算法都在 editalign（经函数字段注入）；本文件只负责取媒体槽、组装 Source、把结果以
// confirmed=false 写回配方。分析失败或取消不改配方；已确认的配方项永远不被分析覆盖。

const editAnalysisStepMS = 250

// EditAnalysisResult 是项目最近一轮分析的结果（存于 analysis_json）。
type EditAnalysisResult struct {
	Kind       string               `json:"kind"`
	Status     string               `json:"status"`
	Error      string               `json:"error"`
	WindowMS   int64                `json:"window_ms"`
	StartedAt  *time.Time           `json:"started_at" ts_type:"string"`
	FinishedAt *time.Time           `json:"finished_at" ts_type:"string"`
	Intros     []EditIntroResult    `json:"intros"`
	Segments   []EditAlignedSegment `json:"segments"`
}

// EditIntroResult 是一个来源的片头识别结果；applied=false 表示该项已确认、没有被覆盖。
type EditIntroResult struct {
	VideoID     uint    `json:"video_id"`
	Status      string  `json:"status"`
	StartMS     int64   `json:"start_ms"`
	EndMS       int64   `json:"end_ms"`
	MatchRate   float64 `json:"match_rate"`
	MatchedWith int     `json:"matched_with"`
	Applied     bool    `json:"applied"`
}

// EditAlignedSegment 是一段高清对齐结果；applied=false 表示与已确认段重叠而未写入配方。
type EditAlignedSegment struct {
	LongStartMS int64   `json:"long_start_ms"`
	LongEndMS   int64   `json:"long_end_ms"`
	HDStartMS   int64   `json:"hd_start_ms"`
	HDEndMS     int64   `json:"hd_end_ms"`
	MatchRate   float64 `json:"match_rate"`
	Status      string  `json:"status"`
	Applied     bool    `json:"applied"`
}

// EditAnalyzeRequest 启动一轮分析；window_ms 只用于片头识别（0 表示用配方里的窗口或默认 10 分钟）。
type EditAnalyzeRequest struct {
	ProjectID        uint   `json:"project_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
	WindowMS         int64  `json:"window_ms"`
}

func parseEditAnalysis(raw string) *EditAnalysisResult {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var result EditAnalysisResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil
	}
	if result.Intros == nil {
		result.Intros = []EditIntroResult{}
	}
	if result.Segments == nil {
		result.Segments = []EditAlignedSegment{}
	}
	return &result
}

func clampEditRate(rate float64) float64 {
	return min(max(rate, 0), 1)
}

// AnalyzeProject 启动一轮分析：trim_intro（≥2 项）识别重复片头，hd_replace 做分段对齐；merge 不适用。
func (s *VideoEditService) AnalyzeProject(ctx context.Context, request EditAnalyzeRequest) (*EditProjectView, error) {
	project, err := loadEditProject(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	recipe, err := parseEditRecipe(project.RecipeJSON)
	if err != nil {
		return nil, err
	}
	window := int64(0)
	switch project.Kind {
	case models.VideoEditKindTrimIntro:
		if recipe.TrimIntro == nil || len(recipe.TrimIntro.Items) < 2 {
			return nil, editError("analysis_not_applicable", "自动识别片头至少需要两项")
		}
		window = request.WindowMS
		if window == 0 {
			window = recipe.TrimIntro.AnalysisWindowMS
		}
		if window == 0 {
			window = videoEditDefaultIntroWindowMS
		}
		if window < videoEditMinIntroWindowMS || window > videoEditMaxIntroWindowMS {
			return nil, editError("recipe_invalid", "片头分析窗口必须在 1–20 分钟之间")
		}
	case models.VideoEditKindHDReplace:
	default:
		return nil, editError("analysis_not_applicable", "顺序合并不需要分析")
	}
	if _, err := loadEditVideos(ctx, editRecipeVideoIDs(recipe)); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return nil, editError("edit_service_stopping", "应用正在退出或维护，暂时不能分析")
	}
	if s.analysisProject != 0 {
		s.mu.Unlock()
		return nil, editError("edit_analysis_busy", "已有一轮分析在进行，请等它结束或取消")
	}
	s.analysisProject = project.ID
	s.analysis.Add(1)
	s.mu.Unlock()
	db, err := editDB(ctx)
	if err == nil {
		result := db.Model(&models.VideoEditProject{}).
			Where("id = ? AND revision = ? AND status = ?", project.ID, request.ExpectedRevision, models.VideoEditStatusDraft).
			Updates(map[string]any{"status": models.VideoEditStatusAnalyzing, "updated_at": s.now()})
		err = result.Error
		if err == nil && result.RowsAffected == 0 {
			err = s.conflictError(ctx, project.ID)
		}
	}
	if err != nil {
		s.mu.Lock()
		s.analysisProject = 0
		s.mu.Unlock()
		s.analysis.Done()
		return nil, err
	}
	parent := s.parentCtx
	if parent == nil {
		parent = context.Background()
	}
	analysisCtx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	s.mu.Lock()
	s.analysisCancel, s.analysisDone, s.analysisFinished = cancel, 0, done
	if s.stopping {
		cancel()
	}
	s.mu.Unlock()
	go s.runAnalysis(analysisCtx, cancel, done, project, recipe, window)
	s.emit(VideoEditStateEvent{ProjectID: project.ID, Status: models.VideoEditStatusAnalyzing})
	return s.GetProject(ctx, project.ID)
}

// CancelAnalysis 取消进行中的分析并等它写回 draft（最多 15 秒）；配方不变。
func (s *VideoEditService) CancelAnalysis(ctx context.Context, projectID uint) error {
	s.mu.Lock()
	running := s.analysisProject == projectID
	cancel, done := s.analysisCancel, s.analysisFinished
	s.mu.Unlock()
	if !running || cancel == nil {
		project, err := loadEditProject(ctx, projectID)
		if err != nil {
			return err
		}
		if project.Status != models.VideoEditStatusAnalyzing {
			return editError("edit_project_conflict", "项目没有进行中的分析")
		}
		// 没有分析在跑却停在 analyzing（不应出现）：直接回到 draft，配方不变。
		db, err := editDB(ctx)
		if err != nil {
			return err
		}
		return db.Model(&models.VideoEditProject{}).Where("id = ? AND status = ?", projectID, models.VideoEditStatusAnalyzing).
			Updates(map[string]any{"status": models.VideoEditStatusDraft, "updated_at": s.now()}).Error
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
	case <-ctx.Done():
	}
	return nil
}

func (s *VideoEditService) setAnalysisProgress(projectID uint, progress float64) {
	s.mu.Lock()
	s.analysisDone = progress
	s.mu.Unlock()
	s.emit(VideoEditStateEvent{ProjectID: projectID, Status: models.VideoEditStatusAnalyzing, Progress: progress})
}

// runAnalysis 在后台跑一轮分析并把结果写回（project 回到 draft）。
func (s *VideoEditService) runAnalysis(ctx context.Context, cancel context.CancelFunc, done chan struct{}, project models.VideoEditProject, recipe EditRecipe, window int64) {
	defer s.analysis.Done()
	defer close(done)
	defer cancel()
	defer func() {
		s.mu.Lock()
		s.analysisProject, s.analysisCancel, s.analysisFinished, s.analysisDone = 0, nil, nil, 0
		s.mu.Unlock()
	}()
	started := s.now()
	result := EditAnalysisResult{Kind: project.Kind, Status: "completed", WindowMS: window, StartedAt: &started,
		Intros: []EditIntroResult{}, Segments: []EditAlignedSegment{}}
	err := s.slot.Acquire(ctx)
	if err == nil {
		func() {
			defer s.slot.Release()
			switch project.Kind {
			case models.VideoEditKindTrimIntro:
				err = s.analyzeIntros(ctx, project.ID, &recipe, window, &result)
			case models.VideoEditKindHDReplace:
				err = s.analyzeAlignment(ctx, project.ID, &recipe, &result)
			}
		}()
	}
	finished := s.now()
	result.FinishedAt = &finished
	writeRecipe := err == nil && ctx.Err() == nil
	if !writeRecipe {
		result.Intros, result.Segments = []EditIntroResult{}, []EditAlignedSegment{}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			result.Status, result.Error = "cancelled", ""
		} else {
			result.Status, result.Error = "failed", scrubEditMessage(err.Error())
		}
	}
	s.finishAnalysis(ctx, project.ID, recipe, result, writeRecipe)
}

// finishAnalysis 把项目从 analyzing 写回 draft；成功时连同配方一起写入并 revision+1。
func (s *VideoEditService) finishAnalysis(ctx context.Context, projectID uint, recipe EditRecipe, result EditAnalysisResult, writeRecipe bool) {
	bg, cancel := editBackground(ctx)
	defer cancel()
	analysisRaw, _ := json.Marshal(result)
	fields := map[string]any{"status": models.VideoEditStatusDraft, "analysis_json": string(analysisRaw), "updated_at": s.now()}
	if writeRecipe {
		raw, err := encodeEditRecipe(recipe)
		if err == nil {
			fields["recipe_json"] = raw
			fields["revision"] = gorm.Expr("revision + 1")
		}
	}
	db, err := editDB(bg)
	if err != nil {
		return
	}
	if err := db.Model(&models.VideoEditProject{}).Where("id = ? AND status = ?", projectID, models.VideoEditStatusAnalyzing).
		Updates(fields).Error; err != nil {
		logVideoEdit("project=%d analysis write failed: %s", projectID, scrubEditMessage(err.Error()))
	}
	logVideoEdit("project=%d analysis %s", projectID, result.Status)
	s.emit(VideoEditStateEvent{ProjectID: projectID, Status: models.VideoEditStatusDraft, Progress: 1})
}

// editAlignSource 读一个视频前 windowMS（≤0 为全片）的 4fps dHash 序列并组装 editalign.Source。
func (s *VideoEditService) editAlignSource(ctx context.Context, ffmpeg string, video models.Video, windowMS int64) (editalign.Source, error) {
	probe, err := s.probeEditSource(ctx, video.Path)
	if err != nil {
		return editalign.Source{}, editError("probe_failed", "《%s》探测失败", video.Name)
	}
	readMS := probe.DurationMS
	if windowMS > 0 && windowMS < readMS {
		readMS = windowMS
	}
	sequence, err := s.readHashes(ctx, ffmpeg, video.Path, 0, readMS, editAnalysisStepMS)
	if err != nil {
		if ctx.Err() != nil {
			return editalign.Source{}, ctx.Err()
		}
		return editalign.Source{}, editError("analysis_failed", "《%s》读取画面指纹失败：%s", video.Name, scrubEditPaths(err.Error(), video.Path))
	}
	return editalign.Source{ID: video.ID, DurationMS: probe.DurationMS, Hashes: sequence, Frames: s.grayReader(ffmpeg, video.Path, 32)}, nil
}

// analyzeIntros 识别重复片头，只改写未确认项：detected 写区间与置信度，undetected/ambiguous 只标状态。
func (s *VideoEditService) analyzeIntros(ctx context.Context, projectID uint, recipe *EditRecipe, window int64, result *EditAnalysisResult) error {
	ffmpeg, err := s.findFFmpeg()
	if err != nil {
		return editError("analysis_failed", "找不到 ffmpeg")
	}
	items := recipe.TrimIntro.Items
	ids := make([]uint, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.VideoID)
	}
	videos, err := loadEditVideos(ctx, ids)
	if err != nil {
		return err
	}
	sources := make([]editalign.Source, 0, len(items))
	for index, item := range items {
		source, err := s.editAlignSource(ctx, ffmpeg, videos[item.VideoID], window)
		if err != nil {
			return err
		}
		sources = append(sources, source)
		s.setAnalysisProgress(projectID, 0.8*float64(index+1)/float64(len(items)))
	}
	intros, err := s.detectIntros(ctx, sources, editalign.IntroOptions{})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return editError("analysis_failed", "片头识别失败：%s", scrubEditMessage(err.Error()))
	}
	byVideo := map[uint]editalign.IntroResult{}
	for _, intro := range intros {
		byVideo[intro.SourceID] = intro
	}
	for index := range items {
		item := &recipe.TrimIntro.Items[index]
		intro, ok := byVideo[item.VideoID]
		if !ok {
			continue
		}
		entry := EditIntroResult{VideoID: item.VideoID, Status: string(intro.Status), StartMS: intro.StartMS, EndMS: intro.EndMS,
			MatchRate: clampEditRate(intro.MatchRate), MatchedWith: intro.MatchedWith}
		if !item.Confirmed {
			entry.Applied = true
			item.Confirmed = false
			if intro.Status == editalign.IntroDetected && intro.EndMS > intro.StartMS && intro.StartMS >= 0 {
				item.RemoveStartMS, item.RemoveEndMS = intro.StartMS, intro.EndMS
				item.Origin, item.Confidence, item.DetectStatus = EditOriginDetected, entry.MatchRate, EditDetectDetected
			} else {
				item.Confidence = 0
				item.DetectStatus = EditDetectUndetected
				if intro.Status == editalign.IntroAmbiguous {
					item.DetectStatus = EditDetectAmbiguous
				}
			}
		}
		result.Intros = append(result.Intros, entry)
	}
	s.setAnalysisProgress(projectID, 1)
	return nil
}

// analyzeAlignment 做高清分段对齐：保留已确认段，未确认段整体换成新结果（与已确认段重叠的结果不写入）。
func (s *VideoEditService) analyzeAlignment(ctx context.Context, projectID uint, recipe *EditRecipe, result *EditAnalysisResult) error {
	ffmpeg, err := s.findFFmpeg()
	if err != nil {
		return editError("analysis_failed", "找不到 ffmpeg")
	}
	hd := recipe.HDReplace
	videos, err := loadEditVideos(ctx, []uint{hd.LongVideoID, hd.HDVideoID})
	if err != nil {
		return err
	}
	long, err := s.editAlignSource(ctx, ffmpeg, videos[hd.LongVideoID], 0)
	if err != nil {
		return err
	}
	s.setAnalysisProgress(projectID, 0.3)
	high, err := s.editAlignSource(ctx, ffmpeg, videos[hd.HDVideoID], 0)
	if err != nil {
		return err
	}
	s.setAnalysisProgress(projectID, 0.6)
	aligned, err := s.alignSegments(ctx, long, high, editalign.AlignOptions{}, func(done, total int) {
		if total > 0 {
			s.setAnalysisProgress(projectID, 0.6+0.4*float64(done)/float64(total))
		}
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return editError("analysis_failed", "分段对齐失败：%s", scrubEditMessage(err.Error()))
	}
	kept := []EditHDSegment{}
	for _, segment := range hd.Segments {
		if segment.Confirmed {
			kept = append(kept, segment)
		}
	}
	confirmed := len(kept)
	for _, segment := range aligned {
		entry := EditAlignedSegment{LongStartMS: segment.LongStartMS, LongEndMS: segment.LongEndMS, HDStartMS: segment.HDStartMS,
			HDEndMS: segment.HDEndMS, MatchRate: clampEditRate(segment.MatchRate), Status: string(segment.Status)}
		overlaps := false
		for _, existing := range kept[:confirmed] {
			if segment.LongStartMS < existing.LongEndMS && existing.LongStartMS < segment.LongEndMS {
				overlaps = true
			}
		}
		if !overlaps && len(kept) < videoEditMaxSegments && segment.LongEndMS > segment.LongStartMS {
			entry.Applied = true
			status := string(segment.Status)
			if !oneOf(status, "matched", "conflict") {
				status = ""
			}
			kept = append(kept, EditHDSegment{LongStartMS: segment.LongStartMS, LongEndMS: segment.LongEndMS,
				HDStartMS: segment.HDStartMS, HDEndMS: segment.HDEndMS, AudioSource: EditAudioSourceLong,
				Origin: EditOriginDetected, MatchRate: entry.MatchRate, Status: status, Confirmed: false})
		}
		result.Segments = append(result.Segments, entry)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].LongStartMS < kept[j].LongStartMS })
	hd.Segments = kept
	s.setAnalysisProgress(projectID, 1)
	return nil
}
