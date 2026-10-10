package services

import (
	"context"

	"video-master/models"
)

// DuplicateProject 以任意状态项目的配方新建一个草稿副本（视频编辑合同「执行与发布」补充）。
// 排队后的配方只读，失败/部分成功/取消/中断的项目在来源变化导致预检不再通过时无法原地修正，
// 复制出的草稿可以重新编辑、分析与预检。原项目、它的执行项与成品一律不动；分析结果与已确认的
// 警告不复制，副本必须重新预检。
func (s *VideoEditService) DuplicateProject(ctx context.Context, projectID uint) (*EditProjectView, error) {
	source, err := loadEditProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	recipe, err := parseEditRecipe(source.RecipeJSON)
	if err != nil {
		return nil, err
	}
	raw, err := encodeEditRecipe(recipe)
	if err != nil {
		return nil, err
	}
	mode := source.Mode
	if mode != models.VideoEditModeFast {
		mode = models.VideoEditModePrecise
	}
	project := models.VideoEditProject{
		Kind: source.Kind, Title: clampEditTitle(source.Title + "（副本）"), Status: models.VideoEditStatusDraft, Revision: 1,
		Mode: mode, RecipeJSON: raw, AnalysisJSON: "", AcknowledgedJSON: "[]",
	}
	db, err := editDB(ctx)
	if err != nil {
		return nil, err
	}
	if err := db.Create(&project).Error; err != nil {
		return nil, err
	}
	s.emit(VideoEditStateEvent{ProjectID: project.ID, Status: project.Status})
	return s.GetProject(ctx, project.ID)
}
