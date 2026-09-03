package services

import (
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

// 扫描后自动任务的四个开关必须真的存得下来。
//
// 这条以前是缺的：前端一直把这四个字段发过来，UpdateSettings 却没有赋值，
// tx.Save 保留旧值，用户拨了开关、看到"保存成功"、重开设置页又变回去。
// 两个后端都跑：赋值漏没漏与后端无关，但这一列的读回口径要在两边都成立。
func TestUpdateSettingsPersistsAutomationToggles(t *testing.T) {
	database.DB = dbtest.Open(t)
	if err := database.DB.Create(&models.Settings{VideoExtensions: ".mp4", PlayWeight: 2}).Error; err != nil {
		t.Fatalf("创建设置失败(%s): %v", dbtest.Backend(), err)
	}
	service := &SettingsService{}

	on := models.Settings{
		VideoExtensions: ".mp4", PlayWeight: 2,
		AutoTechnicalBackfill: true, AutoPerceptualHash: true,
		AutoCleanupAnalysis: true, AutoImageEXIFBackfill: true,
	}
	if err := service.UpdateSettings(on); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	saved, err := service.GetSettings()
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if !saved.AutoTechnicalBackfill || !saved.AutoPerceptualHash || !saved.AutoCleanupAnalysis || !saved.AutoImageEXIFBackfill {
		t.Fatalf("四个自动开关都应被保存为开(%s): technical=%v phash=%v cleanup=%v exif=%v",
			dbtest.Backend(), saved.AutoTechnicalBackfill, saved.AutoPerceptualHash,
			saved.AutoCleanupAnalysis, saved.AutoImageEXIFBackfill)
	}

	// 再关回去：只写不清同样是个坑，关掉必须也能存下来。
	off := models.Settings{VideoExtensions: ".mp4", PlayWeight: 2}
	if err := service.UpdateSettings(off); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	saved, err = service.GetSettings()
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if saved.AutoTechnicalBackfill || saved.AutoPerceptualHash || saved.AutoCleanupAnalysis || saved.AutoImageEXIFBackfill {
		t.Fatalf("四个自动开关都应被保存为关(%s): technical=%v phash=%v cleanup=%v exif=%v",
			dbtest.Backend(), saved.AutoTechnicalBackfill, saved.AutoPerceptualHash,
			saved.AutoCleanupAnalysis, saved.AutoImageEXIFBackfill)
	}
}
