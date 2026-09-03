package services

import (
	"path/filepath"
	"strings"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

type SettingsService struct{}

// GetSettings 获取设置
func (s *SettingsService) GetSettings() (*models.Settings, error) {
	var settings models.Settings
	err := database.DB.First(&settings).Error
	return &settings, err
}

// UpdateSettings 更新设置
func (s *SettingsService) UpdateSettings(input models.Settings) error {
	return database.Transaction(func(tx *gorm.DB) error {
		var settings models.Settings
		if err := tx.First(&settings).Error; err != nil {
			return err
		}

		settings.ConfirmBeforeDelete = input.ConfirmBeforeDelete
		settings.DeleteOriginalFile = input.DeleteOriginalFile
		settings.VideoExtensions = input.VideoExtensions
		settings.ScanExcludePaths = normalizeScanExcludePaths(input.ScanExcludePaths)
		settings.ImageScanExcludePaths = normalizeScanExcludePaths(input.ImageScanExcludePaths)
		settings.PlayWeight = input.PlayWeight
		settings.RandomHalfLifeDays = normalizeRandomHalfLifeDays(input.RandomHalfLifeDays)
		settings.AutoScanOnStartup = input.AutoScanOnStartup
		settings.LibraryWatchEnabled = input.LibraryWatchEnabled
		settings.LocalMetadataEnabled = input.LocalMetadataEnabled
		// 扫描后自动任务的四个开关：前端一直在发，这里此前漏了赋值，
		// tx.Save 于是保留旧值——用户拨了开关、提示保存成功、重开设置页又变回去。
		settings.AutoTechnicalBackfill = input.AutoTechnicalBackfill
		settings.AutoPerceptualHash = input.AutoPerceptualHash
		settings.AutoCleanupAnalysis = input.AutoCleanupAnalysis
		settings.AutoImageEXIFBackfill = input.AutoImageEXIFBackfill
		settings.AutoCollectionSuggestions = input.AutoCollectionSuggestions
		settings.AIQualityEnabled = input.AIQualityEnabled
		settings.ShortFeedMaxDurationMinutes = positiveOrDefault(input.ShortFeedMaxDurationMinutes, DefaultShortFeedMaxDurationMinutes)
		settings.ShortFeedFeedbackSyncEnabled = input.ShortFeedFeedbackSyncEnabled
		settings.Theme = input.Theme
		settings.LogEnabled = input.LogEnabled
		settings.BilingualEnabled = input.BilingualEnabled
		settings.BilingualLang = input.BilingualLang
		settings.DeepLApiKey = input.DeepLApiKey
		settings.SubtitleTranslationProvider = string(normalizeSubtitleTranslationProvider(input.SubtitleTranslationProvider))
		settings.SubtitleTranslationBaseURL = strings.TrimSpace(input.SubtitleTranslationBaseURL)
		settings.SubtitleTranslationAPIKey = strings.TrimSpace(input.SubtitleTranslationAPIKey)
		settings.SubtitleTranslationModel = strings.TrimSpace(input.SubtitleTranslationModel)
		settings.SubtitleWhisperXModel = normalizeSubtitleWhisperXModel(input.SubtitleWhisperXModel)
		settings.SubtitleWhisperXBatchSize = normalizeSubtitleWhisperXBatchSize(input.SubtitleWhisperXBatchSize)
		settings.AITaggingBaseURL = input.AITaggingBaseURL
		settings.AITaggingAPIKey = input.AITaggingAPIKey
		settings.AITaggingModel = input.AITaggingModel
		settings.SemanticEmbeddingModel = strings.TrimSpace(input.SemanticEmbeddingModel)
		settings.AITaggingFrameCount = 0
		settings.AITaggingImagesPerRequest = positiveOrDefault(input.AITaggingImagesPerRequest, defaultAITaggingImagesPerRequest)
		settings.AITaggingSubtitleCharLimit = positiveOrDefault(input.AITaggingSubtitleCharLimit, defaultAITaggingSubtitleCharLimit)
		settings.AITaggingStartupBatchSize = positiveOrDefault(input.AITaggingStartupBatchSize, defaultAITaggingStartupBatchSize)
		settings.AITaggingMaxExtraFrames = normalizeAITaggingMaxExtraFrames(input.AITaggingMaxExtraFrames)
		settings.BackupDirectory = strings.TrimSpace(input.BackupDirectory)
		// 与备份执行层共用同一套归一化，保证存储值等于生效值。
		settings.BackupRetentionCount = normalizedBackupRetention(input.BackupRetentionCount)
		settings.BackupIntervalHours = normalizedBackupInterval(input.BackupIntervalHours)
		// 空闲调度（D-032）：阈值收进 1–120 分钟，时间窗只认 HH:MM，
		// 存进去的就是生效值——门控读设置时不用再猜。
		settings.IdleSchedulingEnabled = input.IdleSchedulingEnabled
		settings.IdleThresholdMinutes = NormalizeIdleThresholdMinutes(input.IdleThresholdMinutes)
		settings.IdleRequireACPower = input.IdleRequireACPower
		settings.IdleWindowStart = NormalizeIdleWindowBound(input.IdleWindowStart)
		settings.IdleWindowEnd = NormalizeIdleWindowBound(input.IdleWindowEnd)
		// 桌面通知（D-013）：开关即时生效，通知中心每次投递前读一次这一列。
		settings.DesktopNotificationsEnabled = input.DesktopNotificationsEnabled
		// 播放代理（D-005、D-006）：上限 0 是"不限"，负数按默认 50 GiB 归一化，
		// 存进去的就是生效值。调低上限不立即淘汰，下一次写入时生效。
		settings.AutoCompatibilityProxy = input.AutoCompatibilityProxy
		settings.ProxyCacheLimitBytes = NormalizeProxyCacheLimitBytes(input.ProxyCacheLimitBytes)
		// 人脸识别（D-016、D-022）：镜像前缀存进去的就是生效值（去空白），
		// 自动开关即时生效——扫描后自动化每次读一次这一列。
		settings.AutoFaceAnalysis = input.AutoFaceAnalysis
		settings.FaceModelMirrorURL = strings.TrimSpace(input.FaceModelMirrorURL)
		// 帧哈希序列（D-026）：自动开关即时生效，扫描后自动化每次读一次这一列。
		settings.AutoFrameHashSequence = input.AutoFrameHashSequence
		// 浏览器插件桥接（D-B03、D-B05、D-B06）：目录与并发存进去的就是生效值。
		//
		// 令牌**有意不在这里赋值**：它是配对凭据，只由 RegenerateBrowserBridgeToken
		// 生成。走通用设置保存的话，前端任何一次漏带该字段的提交都会把令牌抹成空，
		// 已经配好的插件会在用户毫不知情的情况下断开。
		settings.BrowserBridgeEnabled = input.BrowserBridgeEnabled
		settings.BrowserDownloadDirectory = strings.TrimSpace(input.BrowserDownloadDirectory)
		settings.BrowserDownloadConcurrency = NormalizeBrowserDownloadConcurrency(input.BrowserDownloadConcurrency)

		if err := tx.Save(&settings).Error; err != nil {
			return err
		}
		return syncShortVideoTags(tx)
	})
}

func normalizeRandomHalfLifeDays(value int) int {
	if value < 0 {
		return 90
	}
	if value > 3650 {
		return 3650
	}
	return value
}

func normalizeScanExcludePaths(raw string) string {
	return strings.Join(parseScanExcludePaths(raw), "\n")
}

func parseScanExcludePaths(raw string) []string {
	paths := make([]string, 0)
	seen := make(map[string]struct{})
	for _, line := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '\r' }) {
		path := filepath.Clean(strings.TrimSpace(line))
		if path == "" || path == "." {
			continue
		}
		key := strings.ToLower(path)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		paths = append(paths, path)
	}
	return paths
}

func isScanPathExcluded(path string, excludedPaths []string) bool {
	for _, excluded := range excludedPaths {
		if pathIsEqualOrInside(path, excluded) {
			return true
		}
	}
	return false
}

func positiveOrDefault(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
