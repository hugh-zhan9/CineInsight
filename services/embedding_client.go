package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type TextEmbeddingClient interface {
	EmbedText(ctx context.Context, text string) (*LocalMLEmbeddingResult, error)
	EmbedTexts(ctx context.Context, texts []string) (*LocalMLEmbeddingResult, error)
}

type OpenAICompatibleEmbeddingClient struct {
	config AITaggingConfig
	client *http.Client
}

const aiEmbeddingRequestTimeout = 2 * time.Minute

func NewOpenAICompatibleEmbeddingClient(config AITaggingConfig) TextEmbeddingClient {
	return &OpenAICompatibleEmbeddingClient{
		config: config,
		client: &http.Client{Timeout: aiEmbeddingRequestTimeout},
	}
}

func (c *OpenAICompatibleEmbeddingClient) EmbedTexts(ctx context.Context, texts []string) (*LocalMLEmbeddingResult, error) {
	texts = filterNonEmptyStrings(texts)
	if len(texts) == 0 {
		return &LocalMLEmbeddingResult{Model: c.config.EmbeddingModel, Source: "api-embedding", Embeddings: [][]float32{}}, nil
	}
	model := strings.TrimSpace(c.config.EmbeddingModel)
	if model == "" {
		return nil, fmt.Errorf("AI embedding model unavailable")
	}
	body := map[string]any{
		"model": model,
		"input": texts,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openAIEmbeddingsURL(c.config.BaseURL), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(c.config.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("AI embedding API returned %d: %s", resp.StatusCode, truncateLogSnippet(string(respBody), 300))
	}
	var parsed struct {
		Model string `json:"model"`
		Data  []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, err
	}
	sort.Slice(parsed.Data, func(i, j int) bool {
		return parsed.Data[i].Index < parsed.Data[j].Index
	})
	embeddings := make([][]float32, 0, len(parsed.Data))
	for _, item := range parsed.Data {
		embeddings = append(embeddings, item.Embedding)
	}
	dimension := 0
	if len(embeddings) > 0 {
		dimension = len(embeddings[0])
	}
	if parsed.Model == "" {
		parsed.Model = model
	}
	return &LocalMLEmbeddingResult{
		Model:      parsed.Model,
		Source:     "api-embedding",
		Embeddings: embeddings,
		Dimension:  dimension,
	}, nil
}

func (c *OpenAICompatibleEmbeddingClient) EmbedText(ctx context.Context, text string) (*LocalMLEmbeddingResult, error) {
	result, err := c.EmbedTexts(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(result.Embeddings) == 0 {
		return nil, fmt.Errorf("AI embedding API returned no vector")
	}
	return &LocalMLEmbeddingResult{
		Model:     result.Model,
		Source:    result.Source,
		Embedding: result.Embeddings[0],
		Dimension: len(result.Embeddings[0]),
	}, nil
}

func openAIEmbeddingsURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if strings.HasSuffix(base, "/embeddings") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/embeddings"
	}
	return base + "/v1/embeddings"
}
