package services

import (
	"context"
	"errors"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"

	"gorm.io/gorm"
)

func TestHealthCachedSubtitleStatusesNeverProbeOrWait(t *testing.T) {
	svc := &SubtitleService{engineStatusProbe: func() []SubtitleEngineStatus { t.Fatal("diagnostic read started a Python probe"); return nil }}
	if _, _, checked := svc.CachedEngineStatuses(); checked {
		t.Fatal("empty cache should be unknown")
	}
	checkedAt := time.Now().Add(-time.Minute)
	svc.engineStatusAt = checkedAt
	svc.engineStatusCache = []SubtitleEngineStatus{{Engine: SubtitleEngineWhisperX, Available: true}}
	statuses, observed, checked := svc.CachedEngineStatuses()
	if !checked || !observed.Equal(checkedAt) || len(statuses) != 1 || !statuses[0].Available {
		t.Fatalf("%+v %v %v", statuses, observed, checked)
	}
	statuses[0].Available = false
	if !svc.engineStatusCache[0].Available {
		t.Fatal("caller mutated the cached status")
	}
	svc.engineStatusMu.Lock()
	defer svc.engineStatusMu.Unlock()
	if _, _, checked := svc.CachedEngineStatuses(); checked {
		t.Fatal("in-flight check should not expose unlocked state")
	}
}

func TestHealthWaitingSnapshotDoesNotLoadSettingsOrProbe(t *testing.T) {
	gate := NewIdleGate()
	gate.loadSettings = func() (IdleSchedulerSettings, error) {
		t.Fatal("snapshot loaded settings")
		return IdleSchedulerSettings{}, nil
	}
	gate.probe = func(context.Context) (IdleSample, error) {
		t.Fatal("snapshot started a system probe")
		return IdleSample{}, nil
	}
	since := time.Now()
	waiter := &idleWaiter{taskKey: "phash", reason: "user_active", since: since}
	gate.waiters["phash"] = map[*idleWaiter]struct{}{waiter: {}}
	items, known := gate.WaitingSnapshot()
	if !known || len(items) != 1 || items[0].TaskKey != "phash" || items[0].Reason != "user_active" || !items[0].Since.Equal(since) {
		t.Fatalf("%+v %v", items, known)
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if _, known := gate.WaitingSnapshot(); known {
		t.Fatal("busy state must remain unknown")
	}
}

func TestHealthFaceCacheDoesNotReadConfigurationOrProbePython(t *testing.T) {
	r := newFaceRuntimeForTest(t)
	if _, _, checked := r.CachedStatus(); checked {
		t.Fatal("new runtime is unexamined")
	}
	r.supported = func() bool { return false }
	actual := r.Status()
	r.mirror = func() string { t.Fatal("cached read called mirror configuration"); return "" }
	r.acceptPy = func(string) bool { t.Fatal("cached read probed an interpreter"); return false }
	stored, observed, checked := r.CachedStatus()
	if !checked || observed.IsZero() || stored.State != actual.State {
		t.Fatalf("%+v %v %v", stored, observed, checked)
	}
}

func TestHealthSemanticReadsUseDeadlinesAndNeverLoadConfig(t *testing.T) {
	capability := setupSemanticIndexTestDB(t)
	if err := database.DB.AutoMigrate(&models.ImageSemanticIndex{}); err != nil {
		t.Fatal(err)
	}
	profile := models.SemanticIndexProfile{ID: 1, ActiveModel: "health-test", Dimension: 3, Generation: 1}
	if err := database.DB.Save(&profile).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.Video{Name: "v", Path: "/health-test-v"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.Image{Name: "i", Path: "/health-test-i"}).Error; err != nil {
		t.Fatal(err)
	}
	provider := SemanticIndexConfigProviderFunc(func() (SemanticIndexConfig, error) {
		t.Fatal("health snapshot called configuration provider")
		return SemanticIndexConfig{}, nil
	})
	video := &SemanticIndexService{db: database.DB, capability: capability, configProvider: provider, status: SemanticIndexStatus{Available: true}}
	photo := &ImageSemanticIndexService{db: database.DB, capability: capability, configProvider: provider, status: ImageSemanticIndexStatus{Available: true}}
	queries := 0
	if err := database.DB.Callback().Query().Before("gorm:query").Register("health-deadline-test", func(tx *gorm.DB) {
		// GORM builds IN subqueries through the same callback in DryRun mode;
		// only statements executed against the pool require a query deadline.
		if tx.DryRun {
			return
		}
		queries++
		if _, ok := tx.Statement.Context.Deadline(); !ok {
			tx.AddError(errors.New("health query has no deadline"))
			t.Error("query escaped its deadline")
		}
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for name, read := range map[string]func(context.Context) (SemanticHealthSnapshot, error){"video": video.HealthSnapshot, "image": photo.HealthSnapshot} {
		snapshot, err := read(ctx)
		if err != nil || !snapshot.Available || !snapshot.Built || snapshot.Coverage.Total != 1 || snapshot.Coverage.Indexed != 0 {
			t.Fatalf("%s: %+v %v", name, snapshot, err)
		}
	}
	if queries != 6 {
		t.Fatalf("expected profile and two coverage reads per media, got %d", queries)
	}
	sqlDB, err := database.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	connection, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	blocked, stop := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer stop()
	if _, err := video.HealthSnapshot(blocked); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("connection-pool wait ignored context: %v", err)
	}
}

func TestHealthRecordedProxyUsageDoesNotEnumerateFiles(t *testing.T) {
	setupVideoServiceTestDB(t)
	if err := database.DB.Model(&models.Settings{}).Where("id = ?", 1).Update("proxy_cache_limit_bytes", int64(4096)).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewPlaybackProxyService("/a/path/that/must/not/be/read", nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	usage, err := svc.RecordedHealthUsage(ctx)
	if err != nil || usage.Count != 0 || usage.TotalBytes != 0 || usage.LimitBytes != 4096 {
		t.Fatalf("%+v %v", usage, err)
	}
	cancel()
	if _, err := svc.RecordedHealthUsage(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
}
