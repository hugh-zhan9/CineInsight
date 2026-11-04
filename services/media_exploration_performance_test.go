package services

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Opt-in synthetic scale measurement; ordinary regression runs remain small.
func TestMediaExplorationHundredThousandCandidates(t *testing.T) {
	if os.Getenv("CINEINSIGHT_EXPLORATION_PERF") != "1" {
		t.Skip("explicit synthetic 100000-item measurement")
	}
	setupImageServiceTestDB(t)
	db := database.DB.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	for base := 0; base < 100000; base += 500 {
		rows := make([]models.Image, 500)
		for i := range rows {
			rows[i] = models.Image{Name: fmt.Sprintf("synthetic-%d", base+i), Path: fmt.Sprintf("/synthetic/%d.jpg", base+i), Directory: "/synthetic"}
		}
		if err := db.CreateInBatches(&rows, 100).Error; err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	s := NewMediaExplorationService()
	state, err := s.Open(context.Background(), "image")
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	if err != nil || state.AvailableCount != 100000 || len(state.Entries) != 1024 {
		t.Fatalf("full pool unavailable: %v", err)
	}
	start = time.Now()
	_, err = s.Open(context.Background(), "image")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("backend=%s candidates=100000 scene=1024 first_scene_ms=%d resumed_scene_ms=%d heap_alloc_mib=%.2f allocation_delta_mib=%.2f", db.Dialector.Name(), elapsed.Milliseconds(), time.Since(start).Milliseconds(), float64(after.HeapAlloc)/(1<<20), float64(after.TotalAlloc-before.TotalAlloc)/(1<<20))
}
