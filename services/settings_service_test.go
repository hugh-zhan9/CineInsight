package services

import (
	"testing"

	"video-master/database"
	"video-master/models"
)

// UpdateSettings 是逐字段显式赋值的白名单：新列漏加一行，前端发过来也存不下，
// 表现成「拨了开关、提示保存成功、重开设置页又变回去」（见 settings_service.go
// 文件内那条事故记录）。本用例把在线资料源那五列的保存往返钉住。
func TestUpdateSettingsPersistsOnlineMetadataSourceColumns(t *testing.T) {
	setupVideoServiceTestDB(t)
	service := &SettingsService{}

	if err := service.UpdateSettings(models.Settings{
		MetadataProxyURL:   "socks5://127.0.0.1:1080",
		TMDBAPIKey:         "tmdb-key",
		BangumiAccessToken: "bangumi-token",
		FANZAAPIID:         "fanza-api-id",
		FANZAAffiliateID:   "fanza-affiliate-id",
	}); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}

	saved, err := service.GetSettings()
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if saved.MetadataProxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("资料源出网代理未保存，实际 %q", saved.MetadataProxyURL)
	}
	if saved.TMDBAPIKey != "tmdb-key" {
		t.Fatalf("TMDB 凭证未保存，实际 %q", saved.TMDBAPIKey)
	}
	if saved.BangumiAccessToken != "bangumi-token" {
		t.Fatalf("Bangumi 凭证未保存，实际 %q", saved.BangumiAccessToken)
	}
	if saved.FANZAAPIID != "fanza-api-id" {
		t.Fatalf("FANZA API ID 未保存，实际 %q", saved.FANZAAPIID)
	}
	if saved.FANZAAffiliateID != "fanza-affiliate-id" {
		t.Fatalf("FANZA 联盟 ID 未保存，实际 %q", saved.FANZAAffiliateID)
	}

	// 保存链路与读取链路要对得上：存进去的就是 LoadWatchlistMetadataConfig 拿到的。
	config := LoadWatchlistMetadataConfig()
	want := WatchlistMetadataConfig{
		ProxyURL:           "socks5://127.0.0.1:1080",
		TMDBAPIKey:         "tmdb-key",
		BangumiAccessToken: "bangumi-token",
		FANZAAPIID:         "fanza-api-id",
		FANZAAffiliateID:   "fanza-affiliate-id",
	}
	if config != want {
		t.Fatalf("保存后读取到的配置不一致，实际: %+v", config)
	}
}

// 清空是合法操作：用户把代理或凭证删掉，保存后就该是空的，不能留着旧值。
func TestUpdateSettingsClearsOnlineMetadataSourceColumns(t *testing.T) {
	setupVideoServiceTestDB(t)
	service := &SettingsService{}

	if err := service.UpdateSettings(models.Settings{
		MetadataProxyURL: "http://127.0.0.1:8080",
		TMDBAPIKey:       "tmdb-key",
	}); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	if err := service.UpdateSettings(models.Settings{}); err != nil {
		t.Fatalf("清空设置失败: %v", err)
	}

	saved, err := service.GetSettings()
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if saved.MetadataProxyURL != "" || saved.TMDBAPIKey != "" {
		t.Fatalf("期望清空后为空，实际 proxy=%q tmdb=%q", saved.MetadataProxyURL, saved.TMDBAPIKey)
	}
}

// 粘贴凭证时很容易带上首尾空格。存进去的应该是去过空白的值，否则请求头里会多出
// 一个看不见的空格，排查起来毫无线索。
func TestUpdateSettingsTrimsOnlineMetadataSourceColumns(t *testing.T) {
	setupVideoServiceTestDB(t)
	service := &SettingsService{}

	if err := service.UpdateSettings(models.Settings{
		MetadataProxyURL:   "  http://127.0.0.1:8080  ",
		TMDBAPIKey:         "  tmdb-key\n",
		BangumiAccessToken: "\tbangumi-token ",
		FANZAAPIID:         " fanza-api-id ",
		FANZAAffiliateID:   " fanza-affiliate-id ",
	}); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}

	var saved models.Settings
	if err := database.DB.First(&saved).Error; err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if saved.MetadataProxyURL != "http://127.0.0.1:8080" ||
		saved.TMDBAPIKey != "tmdb-key" ||
		saved.BangumiAccessToken != "bangumi-token" ||
		saved.FANZAAPIID != "fanza-api-id" ||
		saved.FANZAAffiliateID != "fanza-affiliate-id" {
		t.Fatalf("期望存入去空白后的值，实际: %+v", saved)
	}
}
