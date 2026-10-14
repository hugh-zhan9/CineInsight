package main

import (
	"log"
	"video-master/models"
	"video-master/services"
)

// recapActivityEvent 在一条播放或图片查看事实提交后发给前端，回顾页可见时据此刷新（D-RECAP-CONTENT）。
const recapActivityEvent = "recap-activity"

// GetRecap 返回一期观看回顾（实时 / 月度 / 年度）。只读，按本机时区计算时间窗。
func (a *App) GetRecap(request services.RecapRequest) (*services.RecapResult, error) {
	ctx, cancel, err := a.viewingNotesContext()
	if err != nil {
		return nil, err
	}
	defer cancel()
	return services.NewRecapService().GetRecap(ctx, request)
}

// ListRecapItems 返回一期回顾里某一类（新发现、看过的视频、今日流水、标签排行……）完整列表的一页，
// 与页面上的计数同口径，含已不在库中的项（available=false）。只读。
func (a *App) ListRecapItems(request services.RecapListRequest) (*services.RecapListPage, error) {
	ctx, cancel, err := a.viewingNotesContext()
	if err != nil {
		return nil, err
	}
	defer cancel()
	return services.NewRecapService().ListRecapItems(ctx, request)
}

// RecordImageView 记录一次桌面查看器里的有效查看（同一张图连续显示满 1.5 秒，由前端计时）。
// 同一图片 30 分钟内只记一次；返回是否新记了事件与累计查看次数。
func (a *App) RecordImageView(imageID uint) (*services.ImageViewResult, error) {
	ctx, cancel, err := a.viewingNotesContext()
	if err != nil {
		return nil, err
	}
	defer cancel()
	result, err := a.imageService.RecordImageView(ctx, imageID, models.ImageViewSourceDesktopViewer)
	if err != nil {
		log.Printf("API RecordImageView id=%d err=%v", imageID, err)
	}
	return result, err
}
