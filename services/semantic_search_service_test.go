package services

import (
	"context"
	"testing"
	"video-master/database"
)

func TestSemanticSearchQueryEmbeddingIsStableAcrossPages(t *testing.T) {
	service := NewSemanticIndexService(nil, databaseCapabilityForQueryCacheTest(), nil)
	calls := 0
	service.embedderFactory = func(SemanticIndexConfig) SemanticEmbeddingClient {
		return semanticEmbeddingClientFunc(func(context.Context, string) ([]float64, error) {
			calls++
			return []float64{0.2, 0.8}, nil
		})
	}
	config := SemanticIndexConfig{Model: "embed-v1"}
	first, err := service.embedSearchQuery(context.Background(), config, "雨夜公路")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.embedSearchQuery(context.Background(), config, "雨夜公路")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(first) != 2 || len(second) != 2 {
		t.Fatalf("embedding calls=%d first=%v second=%v", calls, first, second)
	}
}

func databaseCapabilityForQueryCacheTest() database.SemanticVectorCapability {
	return database.SemanticVectorCapability{Available: true, Backend: "test"}
}
