package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"video-master/database"
	"video-master/models"
)

// 场景检索设置的归一化与读取（D-MW-SCENES）。
const (
	SceneProviderLocal    = "local"
	SceneProviderExternal = "external"

	sceneIntervalMinSeconds = 2
	sceneIntervalMaxSeconds = 30
)

// NormalizeSceneVisualProvider 只认 external；其余（含空串、未知值）一律按默认 local。
func NormalizeSceneVisualProvider(value string) string {
	if strings.TrimSpace(strings.ToLower(value)) == SceneProviderExternal {
		return SceneProviderExternal
	}
	return SceneProviderLocal
}

// NormalizeSceneVisualIntervalSeconds：≤0 取默认 5，其余夹到 2–30。
func NormalizeSceneVisualIntervalSeconds(value int) int {
	switch {
	case value <= 0:
		return database.DefaultSceneVisualIntervalSeconds
	case value < sceneIntervalMinSeconds:
		return sceneIntervalMinSeconds
	case value > sceneIntervalMaxSeconds:
		return sceneIntervalMaxSeconds
	}
	return value
}

// sceneSettings 是建索引与检索用到的设置的生效值。
type sceneSettings struct {
	Provider   string
	IntervalMS int64
	Mirror     string
	// ExternalModel / ExternalConfigured：外部描述用的模型名与"接口地址和模型都已配置"。
	// 取值口径同 SettingsAITaggingConfigProvider（设置非空优先，环境变量兜底），但只读设置、
	// 带调用方 ctx，不构造客户端也不发请求——检索、覆盖率与健康面板只需要拼出 model_id。
	ExternalModel      string
	ExternalConfigured bool
	// ExternalFingerprint 是接口地址 + 模型 + Key 的摘要（不含明文）。外部建索引入队时记下它，
	// 每项与每批请求前重读比对：设置改回本地或换了接口/模型/Key，就不再发任何请求（I-1）。
	ExternalFingerprint string
}

// externalAllowed 报告当前设置是否仍允许用 fingerprint 对应的外部配置发请求。
func (s sceneSettings) externalAllowed(fingerprint string) bool {
	return s.Provider == SceneProviderExternal && s.ExternalConfigured && fingerprint != "" && s.ExternalFingerprint == fingerprint
}

// currentModelID 是当前提供方对应的模型标识；外部未配置时为空串。
func (s sceneSettings) currentModelID() string {
	if s.Provider != SceneProviderExternal {
		return SceneLocalModelID
	}
	if !s.ExternalConfigured {
		return ""
	}
	return sceneExternalModelID(s.ExternalModel)
}

func settingOrEnv(value, env string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv(env))
}

var errSceneDatabaseUnavailable = errors.New("database_unavailable")

func loadSceneSettings(ctx context.Context) (sceneSettings, error) {
	if database.DB == nil {
		return sceneSettings{}, errSceneDatabaseUnavailable
	}
	var row models.Settings
	if err := database.DB.WithContext(ctx).
		Select("id", "scene_visual_provider", "scene_visual_interval_seconds", "scene_model_mirror_url", "ai_tagging_base_url", "ai_tagging_model", "ai_tagging_api_key").
		First(&row).Error; err != nil {
		return sceneSettings{}, err
	}
	model := settingOrEnv(row.AITaggingModel, envAITaggingModel)
	baseURL := settingOrEnv(row.AITaggingBaseURL, envAITaggingBaseURL)
	digest := sha256.Sum256([]byte(baseURL + "\x00" + model + "\x00" + settingOrEnv(row.AITaggingAPIKey, envAITaggingAPIKey)))
	return sceneSettings{
		Provider:            NormalizeSceneVisualProvider(row.SceneVisualProvider),
		IntervalMS:          int64(NormalizeSceneVisualIntervalSeconds(row.SceneVisualIntervalSeconds)) * 1000,
		Mirror:              strings.TrimSpace(row.SceneModelMirrorURL),
		ExternalModel:       model,
		ExternalConfigured:  model != "" && baseURL != "",
		ExternalFingerprint: hex.EncodeToString(digest[:]),
	}, nil
}

// SceneModelMirrorURL 供运行时读镜像主机；读不到设置时按"没配镜像"处理。
func SceneModelMirrorURL() string {
	settings, err := loadSceneSettings(context.Background())
	if err != nil {
		return ""
	}
	return settings.Mirror
}
