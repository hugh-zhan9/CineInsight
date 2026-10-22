package models

import (
	"gorm.io/gorm/schema"
	"sync"
	"testing"
)

func TestFunctionalPolishModelsRegistered(t *testing.T) {
	tables := map[string]bool{}
	cache := &sync.Map{}
	for _, model := range AllModels() {
		parsed, err := schema.Parse(model, cache, schema.NamingStrategy{})
		if err != nil {
			t.Fatal(err)
		}
		tables[parsed.Table] = true
	}
	for _, table := range []string{"recommendation_feedbacks", "task_failure_runs", "task_failure_items", "collection_episode_assignments", "collection_season_expectations"} {
		if !tables[table] {
			t.Errorf("functional-polish table %s missing from production registry", table)
		}
	}
}
