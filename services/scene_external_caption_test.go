package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"video-master/database"
	"video-master/models"
)

func sceneChatResponse(content string) []byte {
	data, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": content}}}})
	return data
}

func TestParseSceneCaptionResponse(t *testing.T) {
	fenced := "```json\n{\"captions\":[\"  海边   日落 \",\"" + strings.Repeat("长", 40) + "\"]}\n```"
	captions, err := parseSceneCaptionResponse(sceneChatResponse(fenced), 2)
	if err != nil || captions[0] != "海边 日落" || len([]rune(captions[1])) != sceneCaptionMaxRunes {
		t.Fatalf("应去掉围栏、合并空白并截到 30 字: %q %v", captions, err)
	}
	if _, err := parseSceneCaptionResponse(sceneChatResponse(`{"captions":["只有一条"]}`), 2); err == nil {
		t.Fatal("条数对不上必须报错")
	}
	if _, err := parseSceneCaptionResponse([]byte(`not json`), 1); err == nil {
		t.Fatal("非 JSON 应报错")
	}
	if got := sceneExternalModelID(" gpt-vision "); got != "external-caption:gpt-vision@1" {
		t.Fatalf("模型标识不对: %s", got)
	}
}

// 外部客户端：未配置时拒绝；HTTP 失败只回代码；日志不含接口地址、Key 与描述文本。
func TestSceneCaptionClientConfigAndPrivacy(t *testing.T) {
	setupSceneDB(t, SceneProviderExternal)
	for _, key := range []string{envAITaggingBaseURL, envAITaggingModel, envAITaggingAPIKey} {
		t.Setenv(key, "")
	}
	if _, err := defaultSceneCaptionClientFactory(); !errors.Is(err, ErrSceneExternalNotConfigured) {
		t.Fatalf("未配置时应返回 scene_external_not_configured: %v", err)
	}
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(sceneChatResponse(`{"captions":["秘密的描述文本"]}`))
	}))
	defer server.Close()
	if err := database.DB.Model(&models.Settings{}).Where("id = ?", 1).Updates(map[string]any{
		"ai_tagging_base_url": server.URL, "ai_tagging_model": "vision-x", "ai_tagging_api_key": "sk-secret-key",
	}).Error; err != nil {
		t.Fatal(err)
	}
	client, err := defaultSceneCaptionClientFactory()
	if err != nil || client.ModelName() != "vision-x" {
		t.Fatalf("配置后应能构造客户端: %v", err)
	}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	captions, err := client.CaptionFrames(context.Background(), [][]byte{[]byte("jpeg")})
	if err != nil || len(captions) != 1 {
		t.Fatalf("描述请求失败: %v", err)
	}
	status = http.StatusUnauthorized
	if _, err := client.CaptionFrames(context.Background(), [][]byte{[]byte("jpeg")}); err == nil || err.Error() != "scene_external_http_401" {
		t.Fatalf("HTTP 失败应只回代码: %v", err)
	}
	for _, secret := range []string{server.URL, "sk-secret-key", "秘密的描述文本"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("日志不得包含 %q: %s", secret, logs.String())
		}
	}
	if _, err := client.CaptionFrames(context.Background(), make([][]byte, 9)); err == nil {
		t.Fatal("一次最多 8 帧")
	}
}
