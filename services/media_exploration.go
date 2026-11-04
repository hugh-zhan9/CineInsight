package services

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/rand"
	"sync"
	"time"
	"video-master/database"
	"video-master/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// MediaExplorationService owns durable exploration, never formal watching state.
type MediaExplorationService struct {
	mu    sync.Mutex
	pool  *sql.DB
	epoch string
}

func NewMediaExplorationService() *MediaExplorationService { return &MediaExplorationService{} }

func (s *MediaExplorationService) databaseEpoch(db *gorm.DB) (string, error) {
	pool, err := db.DB()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pool != pool || s.epoch == "" {
		s.pool, s.epoch = pool, uuid.NewString()
	}
	return s.epoch, nil
}

func explorationDB(db *gorm.DB) *gorm.DB {
	return db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
}

// Open resumes a persisted scene. A process/database change only changes epoch.
func (s *MediaExplorationService) Open(ctx context.Context, scope string) (*ExplorationSnapshot, error) {
	if !validExplorationScope(scope) {
		return nil, ErrExplorationInvalid
	}
	var result *ExplorationSnapshot
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		epoch, err := s.databaseEpoch(db)
		if err != nil {
			return err
		}
		db = explorationDB(db)
		err = db.Transaction(func(tx *gorm.DB) error {
			var state models.MediaExplorationState
			err := tx.Where("scope = ?", scope).First(&state).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				view, _ := json.Marshal(ExplorationView{Mode: "wall", Scale: .35})
				state = models.MediaExplorationState{Scope: scope, Revision: 1, RoundID: uuid.NewString(), SceneID: uuid.NewString(), SceneSequence: 1, ViewStateJSON: string(view)}
				created := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "scope"}}, DoNothing: true}).Create(&state)
				if created.Error != nil {
					return created.Error
				}
				if created.RowsAffected != 1 {
					return ErrExplorationConflict
				}
				if err := explorationSyncCandidates(tx, &state, true, nil); err != nil {
					return err
				}
				if err := explorationAssignScene(tx, &state); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			result, err = explorationSnapshot(tx, state, epoch)
			return err
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
		// A concurrent initializer won the unique scope. Read its committed state;
		// never retry initialization or create a second round.
		if errors.Is(err, ErrExplorationConflict) {
			return db.Transaction(func(tx *gorm.DB) error {
				var state models.MediaExplorationState
				if err := tx.Where("scope = ?", scope).First(&state).Error; err != nil {
					return err
				}
				result, err = explorationSnapshot(tx, state, epoch)
				return err
			}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		}
		return err
	})
	return result, err
}

// Update atomically changes one scope using its root revision, scene and receipt.
func (s *MediaExplorationService) Update(ctx context.Context, r ExplorationRequest) (*ExplorationSnapshot, error) {
	if err := validateExplorationRequest(r); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		return nil, ErrExplorationInvalid
	}
	hash := sha256.Sum256(encoded)
	digest := hex.EncodeToString(hash[:])
	var result *ExplorationSnapshot
	// Samples acquire the existing path coordinator before any database gate,
	// matching rename/maintenance order. All filesystem work precedes the tx.
	var samples []ExplorationSample
	if r.Action == "sample" {
		unlock, err := rLockLibraryPathsContext(ctx)
		if err != nil {
			return nil, err
		}
		defer unlock()
		samples, err = s.prepareSample(ctx, r)
		if err != nil {
			return nil, err
		}
	}
	err = database.WithOperationContext(ctx, func(db *gorm.DB) error {
		epoch, err := s.databaseEpoch(db)
		if err != nil {
			return err
		}
		if epoch != r.DatabaseEpoch {
			return ErrExplorationDatabase
		}
		return explorationDB(db).Transaction(func(tx *gorm.DB) error {
			var state models.MediaExplorationState
			if err := tx.Where("scope = ?", r.Scope).First(&state).Error; err != nil {
				return err
			}
			if state.LastRequestID == r.RequestID {
				if state.LastRequestHash != digest {
					return ErrExplorationInvalid
				}
				result, err = explorationSnapshot(tx, state, epoch)
				return err
			}
			if state.SceneID != r.SceneID || state.RoundID != r.RoundID {
				return ErrExplorationScene
			}
			changed := tx.Model(&models.MediaExplorationState{}).Where("id = ? AND revision = ? AND round_id = ? AND scene_id = ?", state.ID, r.ExpectedRevision, r.RoundID, r.SceneID).Update("revision", gorm.Expr("revision + 1"))
			if changed.Error != nil {
				return changed.Error
			}
			if changed.RowsAffected != 1 {
				return ErrExplorationConflict
			}
			state.Revision = r.ExpectedRevision + 1
			switch r.Action {
			case "present":
				var count int64
				q := tx.Model(&models.MediaExplorationEntry{}).Where("state_id = ? AND scene_id = ? AND id IN ?", state.ID, state.SceneID, r.EntryIDs)
				if err := q.Session(&gorm.Session{}).Count(&count).Error; err != nil {
					return err
				}
				if count != int64(len(r.EntryIDs)) {
					return ErrExplorationInvalid
				}
				if err := q.Where("presented_at IS NULL").Update("presented_at", time.Now()).Error; err != nil {
					return err
				}
			case "save_view":
				for _, id := range append([]uint{r.View.SelectedID, r.View.GalleryID}, r.View.GalleryIDs...) {
					if id == 0 {
						continue
					}
					var count int64
					if err := tx.Model(&models.MediaExplorationEntry{}).Where("id = ? AND state_id = ? AND scene_id = ?", id, state.ID, state.SceneID).Count(&count).Error; err != nil {
						return err
					}
					if count != 1 {
						return ErrExplorationInvalid
					}
				}
				view, _ := json.Marshal(r.View)
				state.ViewStateJSON = string(view)
			case "next":
				if err := explorationAdvance(tx, &state, false); err != nil {
					return err
				}
			case "reshuffle":
				if err := explorationAdvance(tx, &state, true); err != nil {
					return err
				}
			case "refresh":
				if err := explorationRefreshAvailability(tx, state); err != nil {
					return err
				}
			case "sample":
				value, _ := json.Marshal(samples)
				updated := tx.Model(&models.MediaExplorationEntry{}).Where("id = ? AND state_id = ? AND scene_id = ? AND media_kind = ?", r.EntryID, state.ID, state.SceneID, "video").Update("sample_points_json", string(value))
				if updated.Error != nil {
					return updated.Error
				}
				if updated.RowsAffected != 1 {
					return ErrExplorationScene
				}
			}
			state.LastRequestID, state.LastRequestHash, state.LastRequestKind = r.RequestID, digest, r.Action
			state.LastResultRevision, state.LastResultSceneID = state.Revision, state.SceneID
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := tx.Save(&state).Error; err != nil {
				return err
			}
			result, err = explorationSnapshot(tx, state, epoch)
			return err
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	})
	return result, err
}

func explorationSnapshot(db *gorm.DB, state models.MediaExplorationState, epoch string) (*ExplorationSnapshot, error) {
	result := &ExplorationSnapshot{ExplorationContext: ExplorationContext{DatabaseEpoch: epoch, Scope: state.Scope, SceneID: state.SceneID}, RoundID: state.RoundID, Revision: state.Revision, Status: "ready", Entries: []ExplorationSceneEntry{}, LastReceipt: ExplorationReceipt{RequestID: state.LastRequestID, Revision: state.LastResultRevision, SceneID: state.LastResultSceneID}}
	if err := json.Unmarshal([]byte(state.ViewStateJSON), &result.View); err != nil {
		return nil, err
	}
	q := db.Model(&models.MediaExplorationEntry{}).Where("state_id = ? AND available = ?", state.ID, true)
	if err := q.Session(&gorm.Session{}).Count(&result.AvailableCount).Error; err != nil {
		return nil, err
	}
	if err := q.Where("presented_at IS NOT NULL").Count(&result.PresentedCount).Error; err != nil {
		return nil, err
	}
	result.RemainingCount = result.AvailableCount - result.PresentedCount
	if result.AvailableCount == 0 {
		result.Status = "empty"
	} else if result.RemainingCount == 0 {
		result.Status = "round_complete"
	}
	var entries []models.MediaExplorationEntry
	if err := db.Where("state_id = ? AND scene_id = ?", state.ID, state.SceneID).Order("slot, id").Limit(explorationSceneLimit).Find(&entries).Error; err != nil {
		return nil, err
	}
	for _, row := range entries {
		item := ExplorationSceneEntry{EntryID: row.ID, Slot: row.Slot, Kind: row.MediaKind, MediaID: row.MediaID, Presented: row.PresentedAt != nil, Samples: []ExplorationSample{}}
		if err := json.Unmarshal([]byte(row.SamplePointsJSON), &item.Samples); err != nil {
			return nil, err
		}
		result.Entries = append(result.Entries, item)
	}
	return result, nil
}

func explorationAssignScene(db *gorm.DB, state *models.MediaExplorationState) error {
	var entries []models.MediaExplorationEntry
	if err := db.Where("state_id = ? AND available = ? AND presented_at IS NULL", state.ID, true).Order("order_position, id").Limit(explorationSceneLimit).Find(&entries).Error; err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	if err := db.Model(&models.MediaExplorationEntry{}).Where("state_id = ? AND scene_id <> ?", state.ID, "").Updates(map[string]any{"scene_id": "", "slot": -1}).Error; err != nil {
		return err
	}
	state.SceneID = uuid.NewString()
	state.SceneSequence++
	var view ExplorationView
	if err := json.Unmarshal([]byte(state.ViewStateJSON), &view); err != nil {
		return err
	}
	view.X, view.Y, view.SelectedID, view.GalleryID = 0, 0, 0, 0
	view.GalleryIDs = nil
	encoded, _ := json.Marshal(view)
	state.ViewStateJSON = string(encoded)
	// One bounded upsert per batch, rather than 1024 individual statements.
	for i := range entries {
		entries[i].SceneID, entries[i].Slot = state.SceneID, i
	}
	if err := db.Omit("State").Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"scene_id", "slot"})}).CreateInBatches(&entries, 100).Error; err != nil {
		return err
	}
	return db.Save(state).Error
}

func explorationAdvance(db *gorm.DB, state *models.MediaExplorationState, reset bool) error {
	var recent []models.MediaExplorationEntry
	if err := db.Where("state_id = ? AND scene_id = ?", state.ID, state.SceneID).Order("slot").Find(&recent).Error; err != nil {
		return err
	}
	avoid := map[string]bool{}
	for _, row := range recent {
		if row.PresentedAt != nil {
			avoid[row.UnitKey] = true
		}
	}
	if reset {
		if err := db.Where("state_id = ?", state.ID).Delete(&models.MediaExplorationEntry{}).Error; err != nil {
			return err
		}
		state.RoundID = uuid.NewString()
		if err := explorationSyncCandidates(db, state, true, avoid); err != nil {
			return err
		}
		// Even an empty reset invalidates all old scene requests.
		state.SceneID = uuid.NewString()
	} else {
		var maxOrder int64
		if err := db.Model(&models.MediaExplorationEntry{}).Where("state_id = ?", state.ID).Select("COALESCE(MAX(order_position), 0)").Scan(&maxOrder).Error; err != nil {
			return err
		}
		var released []models.MediaExplorationEntry
		for _, row := range recent {
			if row.PresentedAt == nil {
				maxOrder++
				row.OrderPosition = maxOrder
				released = append(released, row)
			}
		}
		if len(released) > 0 {
			if err := db.Omit("State").Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"order_position"})}).CreateInBatches(&released, 100).Error; err != nil {
				return err
			}
		}
		if err := explorationSyncCandidates(db, state, false, nil); err != nil {
			return err
		}
	}
	return explorationAssignScene(db, state)
}

func shuffleExploration(rows []models.MediaExplorationEntry) {
	rand.Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
}
