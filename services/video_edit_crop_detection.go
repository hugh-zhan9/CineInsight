package services

import (
	"encoding/json"
	"gorm.io/gorm"
	"time"
	"video-master/models"
)

// createDetectedCropDraftTx joins the discovery transaction; it never queues an export.
func (s *VideoEditService) createDetectedCropDraftTx(tx *gorm.DB, v models.Video, r EditCropResult) (uint, error) {
	if r.Status != "detected" || !editCropValid(r.Rect, r.DisplayWidth, r.DisplayHeight) {
		return 0, editError("crop_invalid", "自动识别裁剪框无效")
	}
	recipe, err := defaultEditRecipe(models.VideoEditKindCrop, []uint{v.ID})
	if err != nil {
		return 0, err
	}
	recipe.Crop.Items[0].Rect = r.Rect
	recipe.Crop.Items[0].SourceVersion = r.SourceVersion
	raw, err := encodeEditRecipe(recipe)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	analysis, _ := json.Marshal(EditAnalysisResult{Kind: "crop", Status: "completed", Crops: []EditCropResult{r}, FinishedAt: &now})
	title := []rune("黑边识别 · " + editVideoDisplayName(v))
	if len(title) > 200 {
		title = title[:200]
	}
	p := models.VideoEditProject{Kind: "crop", Title: string(title), Status: models.VideoEditStatusDraft, Revision: 1, Mode: models.VideoEditModePrecise, RecipeJSON: raw, AnalysisJSON: string(analysis), AcknowledgedJSON: "[]"}
	if err = tx.Create(&p).Error; err != nil {
		return 0, err
	}
	return p.ID, nil
}

// notifyDetectedCropDraft publishes the existing edit-owner event after the discovery transaction commits.
func (s *VideoEditService) notifyDetectedCropDraft(projectID uint) {
	s.emit(VideoEditStateEvent{ProjectID: projectID, Status: models.VideoEditStatusDraft})
}
