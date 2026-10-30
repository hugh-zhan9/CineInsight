package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"gorm.io/gorm"
	"video-master/database"
	"video-master/services"
)

// appBTState is assembly and lifecycle state only. Durable task truth stays in BTService.
type appBTState struct {
	mu             sync.Mutex
	service        *services.BTService
	duplicates     *services.BTDuplicateService
	fence          *services.BTRestoreFence
	ready          bool
	initErr        error
	emit           func(string, interface{})
	runtimeContext context.Context
}

func (a *App) wireBT(dataDir string) {
	state := &appBTState{fence: services.NewBTRestoreFence(dataDir)}
	changed := func(id string, revision uint64) {
		state.mu.Lock()
		emit := state.emit
		state.mu.Unlock()
		if emit != nil {
			emit("bt-task-changed", struct {
				ID       string `json:"id"`
				Revision uint64 `json:"revision"`
			}{id, revision})
			a.notifyTaskCenterChanged()
		}
	}
	checker := &services.DownloadDuplicateChecker{PerceptualHash: a.perceptualHash}
	state.duplicates = services.NewBTDuplicateService(checker.Check, changed)
	var importer *services.BTImporter
	state.service = services.NewBTService(dataDir, services.BTServiceDeps{
		EnsureNamespace: services.WriteBTNamespaceMarker,
		ValidateRoot:    services.ValidateBTDownloadRoot,
		Changed:         changed,
		ImportFile: func(ctx context.Context, taskID, fileID string) error {
			_, err := importer.Import(ctx, taskID, fileID)
			state.duplicates.Enqueue(taskID, fileID)
			return err
		},
	})
	importer = services.NewBTImporter(a.videoService, services.NewBTPathPolicy(nil), state.service)
	// Construction is local and does not inspect a database or credentials.
	_ = state.service.Quiesce(context.Background())
	a.bt = state
	if a.databaseSwitchService != nil {
		a.databaseSwitchService.SetConfigTargetPreparation(func(ctx context.Context, db *gorm.DB) (func(bool) error, error) {
			ticket, err := state.fence.Prepare(ctx, db)
			if err != nil {
				return nil, err
			}
			return func(unchanged bool) error {
				if unchanged {
					return state.fence.ClearOwned(ticket)
				}
				return nil
			}, nil
		})
	}
}

// Called after schema initialization and before Wails can expose any binding.
func (a *App) initializeBTLocal() error {
	if a.bt == nil {
		return nil
	}
	// The imported-video publication path must already have all observers installed.
	if a.settingsService != nil {
		settings, err := a.settingsService.GetSettings()
		if err != nil {
			return a.setBTInitialization(err)
		}
		a.configureLocalMetadata(settings.LocalMetadataEnabled)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := a.bt.service.InitializeLocal(ctx, a.bt.fence)
	return a.setBTInitialization(err)
}
func (a *App) setBTInitialization(err error) error {
	a.bt.mu.Lock()
	a.bt.ready = err == nil
	a.bt.initErr = err
	a.bt.mu.Unlock()
	if err == nil {
		a.bt.service.Resume()
		a.bt.duplicates.Resume()
	}
	return err
}
func (a *App) startBT(ctx context.Context, emit func(string, interface{})) {
	if a.bt == nil {
		return
	}
	a.bt.mu.Lock()
	a.bt.emit = emit
	a.bt.runtimeContext = ctx
	ready := a.bt.ready
	a.bt.mu.Unlock()
	if ready {
		a.bt.service.Start(ctx)
		a.bt.duplicates.Start(ctx)
	}
}
func (a *App) quiesceBT(ctx context.Context) error {
	if a.bt == nil {
		return nil
	}
	// Both pauses are established even if one wait times out.
	return errors.Join(a.bt.service.Quiesce(ctx), a.bt.duplicates.Quiesce(ctx))
}
func (a *App) stopBT(ctx context.Context) error {
	if a.bt == nil {
		return nil
	}
	return errors.Join(a.bt.service.Stop(ctx), a.bt.duplicates.Stop(ctx))
}
func (a *App) resumeBTAfterMaintenance() {
	if a.bt == nil {
		return
	}
	if a.initializeBTLocal() != nil {
		return
	}
	a.bt.mu.Lock()
	ctx := a.bt.runtimeContext
	a.bt.mu.Unlock()
	if ctx != nil {
		a.bt.service.Start(ctx)
		a.bt.duplicates.Start(ctx)
	}
}
func (a *App) prepareBTRestore(ctx context.Context) (services.BTRestoreTicket, error) {
	if a.bt == nil {
		return services.BTRestoreTicket{}, nil
	}
	var ticket services.BTRestoreTicket
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var err error
		ticket, err = a.bt.fence.PrepareRestore(ctx, db)
		if err != nil {
			return errors.Join(err, a.bt.fence.ClearOwned(ticket))
		}
		return nil
	})
	return ticket, err
}
func (a *App) btContext() (context.Context, context.CancelFunc, error) {
	if a.bt == nil {
		return nil, nil, services.ErrBTConnection
	}
	a.bt.mu.Lock()
	ready := a.bt.ready
	ctx := a.bt.runtimeContext
	a.bt.mu.Unlock()
	if !ready {
		return nil, nil, services.ErrBTRestorePending
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	return ctx, cancel, nil
}

func (a *App) GetBTConnection() (services.BTConnectionView, error) {
	ctx, cancel, err := a.btContext()
	if err != nil {
		return services.BTConnectionView{}, err
	}
	defer cancel()
	return a.bt.service.Connection().Get(ctx)
}
func (a *App) ConfigureBTConnection(request services.BTConnectionRequest) (services.BTConnectionView, error) {
	ctx, cancel, err := a.btContext()
	if err != nil {
		return services.BTConnectionView{}, err
	}
	defer cancel()
	// Configuration owns its own pause and must never enter the ordinary command gate.
	return a.bt.service.Connection().Configure(ctx, request)
}
func (a *App) TestBTConnection() (services.BTConnectionTestResult, error) {
	ctx, cancel, err := a.btContext()
	if err != nil {
		return services.BTConnectionTestResult{}, err
	}
	defer cancel()
	return a.bt.service.TestConnection(ctx)
}
func (a *App) ListBTIndexers() ([]services.BTIndexerView, error) {
	ctx, cancel, err := a.btContext()
	if err != nil {
		return nil, err
	}
	defer cancel()
	return a.bt.service.Indexers(ctx)
}
func (a *App) SearchBTResources(query string, indexerIDs []int) (*services.BTSearchResult, error) {
	ctx, cancel, err := a.btContext()
	if err != nil {
		return nil, err
	}
	defer cancel()
	return a.bt.service.Search(ctx, query, indexerIDs)
}
func (a *App) PreviewBTResource(token string) (services.BTTaskView, error) {
	ctx, cancel, err := a.btContext()
	if err != nil {
		return services.BTTaskView{}, err
	}
	defer cancel()
	return a.bt.service.Preview(ctx, token)
}
func (a *App) SubmitBTDownload(request services.BTSubmitRequest) (services.BTTaskView, error) {
	ctx, cancel, err := a.btContext()
	if err != nil {
		return services.BTTaskView{}, err
	}
	defer cancel()
	return a.bt.service.Submit(ctx, request)
}
func (a *App) ListBTTasks(beforeID string, limit int) ([]services.BTTaskView, error) {
	ctx, cancel, err := a.btContext()
	if err != nil {
		return nil, err
	}
	defer cancel()
	return a.bt.service.List(ctx, beforeID, limit)
}
func (a *App) GetBTTask(id string) (services.BTTaskView, error) {
	ctx, cancel, err := a.btContext()
	if err != nil {
		return services.BTTaskView{}, err
	}
	defer cancel()
	return a.bt.service.Get(ctx, id)
}
func (a *App) ControlBTTask(request services.BTControlRequest) (services.BTTaskView, error) {
	ctx, cancel, err := a.btContext()
	if err != nil {
		return services.BTTaskView{}, err
	}
	defer cancel()
	return a.bt.service.Control(ctx, request)
}
