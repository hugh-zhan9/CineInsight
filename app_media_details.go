package main

import (
	"context"
	"log"
	"video-master/models"
	"video-master/services"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func (a *App) GetVideoDetails(videoID uint) (*services.VideoDetails, error) {
	return a.videoDetailService.GetVideoDetails(videoID)
}

func (a *App) UpdateVideoDetails(input services.VideoDetailsUpdate) (*services.VideoDetails, error) {
	return a.videoDetailService.UpdateVideoDetails(input)
}

func (a *App) RefreshVideoTechnicalMetadata(videoID uint) (*services.VideoDetails, error) {
	if err := a.videoService.RefreshVideoMetadata(videoID); err != nil {
		return nil, err
	}
	return a.videoDetailService.GetVideoDetails(videoID)
}

func (a *App) GetLocalMetadataDiff(videoID uint) (*services.LocalMetadataDiff, error) {
	return a.localMetadata.GetDiff(videoID)
}

func (a *App) ApplyLocalMetadata(request services.LocalMetadataApplyRequest) (*services.LocalMetadataApplyResult, error) {
	result, err := a.localMetadata.Apply(request)
	if err == nil && a.cleanupService != nil {
		a.cleanupService.InvalidateAnalysis()
	}
	return result, err
}

func (a *App) PreviewLocalMetadataBatch(videoIDs []uint) services.LocalMetadataBatchPreview {
	return a.localMetadata.PreviewBatch(videoIDs)
}

func (a *App) ApplyLocalMetadataBatch(request services.LocalMetadataBatchApplyRequest) services.LocalMetadataBatchResult {
	result := a.localMetadata.ApplyBatch(request)
	if result.Succeeded > 0 && a.cleanupService != nil {
		a.cleanupService.InvalidateAnalysis()
	}
	return result
}

func (a *App) ExportLocalMetadataNFO(videoID uint) (*services.LocalMetadataNFOExportResult, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return a.localMetadata.ExportVideoNFO(ctx, videoID)
}

func (a *App) ResolveVideoArtwork(videoID uint, kind string) (*services.VideoArtworkData, error) {
	return a.localMetadata.ResolveVideoArtwork(videoID, kind)
}

// ListPeople returns stable local person candidates and their active video and image counts.
func (a *App) ListPeople(keyword, cursorName string, cursorID uint, limit int) ([]services.PersonListItem, error) {
	return a.personService.ListPeople(keyword, cursorName, cursorID, limit)
}

func (a *App) GetPersonDetail(personID, cursorVideoID uint, limit int) (*services.PersonDetail, error) {
	return a.personService.GetPersonDetail(personID, cursorVideoID, limit)
}

func (a *App) CreatePerson(displayName, originalName string) (*models.Person, error) {
	return a.personService.CreatePerson(displayName, originalName)
}

func (a *App) UpdatePerson(personID uint, displayName, originalName string) (*models.Person, error) {
	return a.personService.UpdatePerson(personID, displayName, originalName)
}

func (a *App) AddPersonVideo(personID, videoID uint) error {
	return a.personService.AddPersonVideo(personID, videoID)
}

func (a *App) AddPersonVideos(personID uint, videoIDs []uint) error {
	return a.personService.AddPersonVideos(personID, videoIDs)
}

func (a *App) RemovePersonVideo(personID, videoID uint) (bool, error) {
	return a.personService.RemovePersonVideo(personID, videoID)
}

// GetPersonImages 翻页人物详情「图片」区块；首页已经在 GetPersonDetail 里返回。
func (a *App) GetPersonImages(personID, cursorImageID uint, limit int) (*services.PersonImagePage, error) {
	return a.personService.GetPersonImages(personID, cursorImageID, limit)
}

func (a *App) AddPersonImages(personID uint, imageIDs []uint) error {
	return a.personService.AddPersonImages(personID, imageIDs)
}

// RemovePersonImage 返回值与 RemovePersonVideo 同义：true 表示这是该人物跨视频与
// 图片的最后一条关系，人物已被连带清理。
func (a *App) RemovePersonImage(personID, imageID uint) (bool, error) {
	return a.personService.RemovePersonImage(personID, imageID)
}

func (a *App) SetImagePeople(imageID uint, personIDs []uint) error {
	return a.personService.SetImagePeople(imageID, personIDs)
}

func (a *App) SelectPersonAvatar() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择人物头像",
		Filters: []runtime.FileFilter{
			{DisplayName: "图片 (*.jpg;*.jpeg;*.png;*.webp)", Pattern: "*.jpg;*.jpeg;*.png;*.webp"},
		},
	})
}

func (a *App) SetPersonAvatar(personID uint, sourcePath string) (*models.Person, error) {
	return a.personService.SetPersonAvatar(personID, sourcePath)
}

func (a *App) RemovePersonAvatar(personID uint) error {
	return a.personService.RemovePersonAvatar(personID)
}

func (a *App) ListCollections(keyword, cursorName string, cursorID uint, limit int) ([]services.CollectionListItem, error) {
	return a.collectionService.ListCollections(keyword, cursorName, cursorID, limit)
}

func (a *App) GetCollectionDetail(collectionID uint) (*services.CollectionDetail, error) {
	return a.collectionService.GetCollectionDetail(collectionID)
}

func (a *App) CreateCollection(name, description string) (*models.MediaCollection, error) {
	return a.collectionService.CreateCollection(name, description)
}

func (a *App) UpdateCollection(collectionID uint, name, description string) (*models.MediaCollection, error) {
	return a.collectionService.UpdateCollection(collectionID, name, description)
}

func (a *App) DeleteCollection(collectionID uint) error {
	return a.collectionService.DeleteCollection(collectionID)
}

func (a *App) AddCollectionVideo(collectionID, videoID uint) error {
	return a.collectionService.AddCollectionVideo(collectionID, videoID)
}

func (a *App) AddCollectionVideos(collectionID uint, videoIDs []uint) error {
	return a.collectionService.AddCollectionVideos(collectionID, videoIDs)
}

func (a *App) RemoveCollectionVideo(collectionID, videoID uint) error {
	return a.collectionService.RemoveCollectionVideo(collectionID, videoID)
}

func (a *App) ReorderCollectionVideos(collectionID uint, activeVideoIDs []uint) error {
	return a.collectionService.ReorderCollectionVideos(collectionID, activeVideoIDs)
}

func (a *App) SelectCollectionCover() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择作品集封面",
		Filters: []runtime.FileFilter{
			{DisplayName: "图片 (*.jpg;*.jpeg;*.png;*.webp)", Pattern: "*.jpg;*.jpeg;*.png;*.webp"},
		},
	})
}

func (a *App) SetCollectionCover(collectionID uint, sourcePath string) (*models.MediaCollection, error) {
	return a.collectionService.SetCollectionCover(collectionID, sourcePath)
}

func (a *App) RemoveCollectionCover(collectionID uint) error {
	return a.collectionService.RemoveCollectionCover(collectionID)
}

// ===== Tag Methods =====

// GetAllTags 获取所有标签
func (a *App) GetAllTags() ([]models.Tag, error) {
	tags, err := a.tagService.GetAllTags()
	log.Printf("API GetAllTags result=%d err=%v sample=%s", len(tags), err, summarizeTags(tags, 5))
	return tags, err
}

// GetImageTags 仅返回被图片实际使用的标签，供图片库筛选栏展示。
func (a *App) GetImageTags() ([]models.Tag, error) {
	tags, err := a.tagService.GetImageTags()
	log.Printf("API GetImageTags result=%d err=%v", len(tags), err)
	return tags, err
}

// CreateTag 创建标签
func (a *App) CreateTag(name, color string) (*models.Tag, error) {
	tag, err := a.tagService.CreateTag(name, color)
	if tag != nil {
		log.Printf("API CreateTag name=%s color=%s id=%d err=%v", name, color, tag.ID, err)
	} else {
		log.Printf("API CreateTag name=%s color=%s id=0 err=%v", name, color, err)
	}
	return tag, err
}

// UpdateTag 更新标签
func (a *App) UpdateTag(id uint, name, color string) error {
	err := a.tagService.UpdateTag(id, name, color)
	log.Printf("API UpdateTag id=%d name=%s color=%s err=%v", id, name, color, err)
	return err
}

// DeleteTag 删除标签
func (a *App) DeleteTag(id uint) error {
	err := a.tagService.DeleteTag(id)
	log.Printf("API DeleteTag id=%d err=%v", id, err)
	return err
}

func (a *App) MergeTags(sourceTagIDs []uint, targetTagID uint) (*services.MergeTagsResult, error) {
	result, err := a.tagService.MergeTags(sourceTagIDs, targetTagID)
	log.Printf("API MergeTags sources=%v target=%d result=%+v err=%v", sourceTagIDs, targetTagID, result, err)
	return result, err
}

// ===== 建议作品集（P-007，D-023..D-025）=====

// StartCollectionSuggestionAnalysis 是用户显式发起的剧集分析，不经空闲门（D-030）。
func (a *App) StartCollectionSuggestionAnalysis() (services.CollectionSuggestionStatus, error) {
	status, err := a.collectionSuggestions.Analyze(a.backgroundContext())
	log.Printf("API StartCollectionSuggestionAnalysis running=%v total=%d err=%v", status.Running, status.Total, err)
	return status, err
}

func (a *App) GetCollectionSuggestionStatus() services.CollectionSuggestionStatus {
	return a.collectionSuggestions.Status()
}

func (a *App) CancelCollectionSuggestionAnalysis() error {
	err := a.collectionSuggestions.Cancel()
	log.Printf("API CancelCollectionSuggestionAnalysis err=%v", err)
	return err
}

// ListCollectionSuggestions 返回待审阅的剧集候选（含成员预览）。
func (a *App) ListCollectionSuggestions() ([]services.CollectionSuggestionView, error) {
	views, err := a.collectionSuggestions.List()
	log.Printf("API ListCollectionSuggestions count=%d err=%v", len(views), err)
	return views, err
}

// ConfirmCollectionSuggestion 确认候选：复用作品集写入，绝不改视频标题与文件名。
// orderedVideoIDs 必须是候选成员的子集，顺序即作品集顺序。
func (a *App) ConfirmCollectionSuggestion(suggestionID uint, name string, orderedVideoIDs []uint) (*services.CollectionDetail, error) {
	detail, err := a.collectionSuggestions.Confirm(suggestionID, name, orderedVideoIDs)
	log.Printf("API ConfirmCollectionSuggestion id=%d name=%q members=%d err=%v", suggestionID, name, len(orderedVideoIDs), err)
	return detail, err
}

// DismissCollectionSuggestion 忽略候选：成员不动，成员集合不变时不再出现。
func (a *App) DismissCollectionSuggestion(suggestionID uint) error {
	err := a.collectionSuggestions.Dismiss(suggestionID)
	log.Printf("API DismissCollectionSuggestion id=%d err=%v", suggestionID, err)
	return err
}
