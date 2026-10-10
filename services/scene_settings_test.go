package services

import (
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
)

func TestNormalizeSceneSettings(t *testing.T) {
	cases := map[int]int{-3: 5, 0: 5, 1: 2, 2: 2, 7: 7, 30: 30, 31: 30}
	for input, want := range cases {
		if got := NormalizeSceneVisualIntervalSeconds(input); got != want {
			t.Fatalf("间隔 %d 应归一化为 %d，实际 %d", input, want, got)
		}
	}
	for input, want := range map[string]string{"": "local", "local": "local", " External ": "external", "cloud": "local"} {
		if got := NormalizeSceneVisualProvider(input); got != want {
			t.Fatalf("提供方 %q 应归一化为 %q，实际 %q", input, want, got)
		}
	}
}

// 通用设置保存必须带上三项场景检索设置，存进去的就是生效值。
func TestUpdateSettingsSavesSceneFields(t *testing.T) {
	setupSceneDB(t, SceneProviderLocal)
	service := &SettingsService{}
	input := models.Settings{VideoExtensions: ".mp4", SceneVisualProvider: "external", SceneVisualIntervalSeconds: 99, SceneModelMirrorURL: "  https://hf-mirror.com  "}
	if err := service.UpdateSettings(input); err != nil {
		t.Fatalf("保存设置失败(%s): %v", dbtest.Backend(), err)
	}
	var saved models.Settings
	if err := database.DB.First(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if saved.SceneVisualProvider != "external" || saved.SceneVisualIntervalSeconds != 30 || saved.SceneModelMirrorURL != "https://hf-mirror.com" {
		t.Fatalf("场景检索设置未按生效值保存: %+v", saved)
	}
	if mirror := SceneModelMirrorURL(); mirror != "https://hf-mirror.com" {
		t.Fatalf("运行时应读到镜像主机: %q", mirror)
	}
}
