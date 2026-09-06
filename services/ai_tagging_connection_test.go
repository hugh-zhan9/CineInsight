package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeAITaggingConnectionReportsSuccessAndFailures(t *testing.T) {
	setupVideoServiceTestDB(t)

	var seenAuth, seenModel string
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer ok.Close()

	result := ProbeAITaggingConnection(context.Background(), AITaggingConnectionTestInput{BaseURL: ok.URL + "/v1", APIKey: "secret", Model: "vision-x"})
	if !result.OK || result.Reply != "OK" || result.Model != "vision-x" || !strings.Contains(result.Message, "连接正常") {
		t.Fatalf("可达接口应报成功: %+v", result)
	}
	if seenAuth != "Bearer secret" || seenModel != "vision-x" {
		t.Fatalf("测试请求应带上表单里的 Key 与模型: auth=%q model=%q", seenAuth, seenModel)
	}

	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer unauthorized.Close()
	result = ProbeAITaggingConnection(context.Background(), AITaggingConnectionTestInput{BaseURL: unauthorized.URL, Model: "m"})
	if result.OK || !strings.Contains(result.Message, "API Key") || !strings.Contains(result.Message, "401") {
		t.Fatalf("401 应翻成 Key 问题: %+v", result)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	address := closed.URL
	closed.Close()
	result = ProbeAITaggingConnection(context.Background(), AITaggingConnectionTestInput{BaseURL: address, Model: "m"})
	if result.OK || !strings.HasPrefix(result.Message, "无法连接") {
		t.Fatalf("连不上的地址应报无法连接: %+v", result)
	}

	result = ProbeAITaggingConnection(context.Background(), AITaggingConnectionTestInput{BaseURL: "not a url", Model: "m"})
	if result.OK || !strings.Contains(result.Message, "合法") {
		t.Fatalf("非法地址应在发请求前拦下: %+v", result)
	}

	// 表单与已保存配置都没有模型：直说缺什么，而不是发一个必然失败的请求。
	result = ProbeAITaggingConnection(context.Background(), AITaggingConnectionTestInput{BaseURL: ok.URL})
	if result.OK || !strings.Contains(result.Message, "不能为空") {
		t.Fatalf("缺模型应直接报缺项: %+v", result)
	}
}
