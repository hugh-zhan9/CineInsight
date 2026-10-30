package services

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"video-master/database"
	"video-master/models"
	"video-master/services/btclient"
)

var ErrBTConnection = errors.New("bt_connection_unavailable: 下载连接配置不可用")
var ErrBTConnectionActive = errors.New("bt_connection_active: 仍有受管理任务，不能替换连接身份或保存目录")

// BTConnectionHooks must be wired to the coordinator's admission/generation lifecycle.
// Resume must reload the current DB slot (also after an uncertain CAS), never reuse
// the old snapshot. Get/Clients do not take the configuration mutex, so Drain may
// wait for a worker that needs a final configuration read.
type BTConnectionHooks struct {
	Drain      func(context.Context) error
	Invalidate func(uint64)
	Resume     func()
}
type BTConnectionRequest struct {
	ExpectedRevision uint64         `json:"expected_revision"`
	ProwlarrBase     string         `json:"prowlarr_base"`
	QBBase           string         `json:"qb_base"`
	QBUsername       string         `json:"qb_username"`
	DownloadRoot     string         `json:"download_root"`
	Enabled          bool           `json:"enabled"`
	ProwlarrKey      BTSecretChange `json:"prowlarr_key"`
	QBPassword       BTSecretChange `json:"qb_password"`
}
type BTConnectionView struct {
	InstanceID            string `json:"instance_id"`
	Revision              uint64 `json:"revision"`
	ProwlarrBase          string `json:"prowlarr_base"`
	QBBase                string `json:"qb_base"`
	QBUsername            string `json:"qb_username"`
	DownloadRoot          string `json:"download_root"`
	Enabled               bool   `json:"enabled"`
	ProwlarrKeyConfigured bool   `json:"prowlarr_key_configured"`
	QBPasswordConfigured  bool   `json:"qb_password_configured"`
	CredentialStatus      string `json:"credential_status"`
}

// BTConnectionService resolves the live DB on every operation, including inside CAS.
type BTConnectionService struct {
	mu                sync.Mutex
	credentialFactory BTCredentialFactory
	hooks             BTConnectionHooks
	db                func() *gorm.DB
}

// NewBTConnectionService uses a dynamic physical-DB credential factory. nil selects
// the scoped system Keychain implementation; tests must inject their explicit scope.
func NewBTConnectionService(factory BTCredentialFactory, hooks BTConnectionHooks) *BTConnectionService {
	if factory == nil {
		factory = btSystemCredentialFactory
	}
	return &BTConnectionService{credentialFactory: factory, hooks: hooks, db: func() *gorm.DB { return database.DB }}
}

func (s *BTConnectionService) database(ctx context.Context) (*gorm.DB, error) {
	db := s.db()
	if db == nil || database.MaintenanceActive() {
		return nil, ErrBTConnection
	}
	return db.WithContext(ctx), nil
}
func (s *BTConnectionService) current(ctx context.Context) (models.BTConnection, error) {
	db, err := s.database(ctx)
	if err != nil {
		return models.BTConnection{}, err
	}
	var row models.BTConnection
	err = db.Where("slot = ?", "local").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.BTConnection{}, nil
	}
	if err != nil {
		return row, ErrBTConnection
	}
	return row, nil
}
func (s *BTConnectionService) view(row models.BTConnection, credentials *BTCredentialManager) (BTConnectionView, error) {
	v := BTConnectionView{InstanceID: row.ID, Revision: row.Revision, ProwlarrBase: row.ProwlarrBase, QBBase: row.QBBase, QBUsername: row.QBUsername, DownloadRoot: row.DownloadRoot, Enabled: row.Enabled, CredentialStatus: "unconfigured"}
	if row.ID == "" {
		v.ProwlarrBase = "http://127.0.0.1:9696"
		v.QBBase = "http://127.0.0.1:8081"
	}
	if row.CredentialRef != "" {
		if credentials == nil {
			v.CredentialStatus = "unavailable"
			return v, nil
		}
		secrets, err := credentials.Load(row.CredentialRef)
		if err != nil {
			v.CredentialStatus = "unavailable"
			if errors.Is(err, ErrBTCredentialsBusy) {
				v.CredentialStatus = "busy"
			}
			return v, nil
		}
		v.CredentialStatus = "configured"
		v.ProwlarrKeyConfigured = secrets.ProwlarrKey != ""
		v.QBPasswordConfigured = secrets.QBPassword != ""
	}
	return v, nil
}
func (s *BTConnectionService) Get(ctx context.Context) (BTConnectionView, error) {
	var result BTConnectionView
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var row models.BTConnection
		err := db.Where("slot = ?", "local").First(&row).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrBTConnection
		}
		if row.CredentialRef == "" {
			result, err = s.view(row, nil)
			return err
		}
		credentials, err := s.credentialFactory(ctx, db)
		if err != nil {
			result, _ = s.view(row, nil)
			result.CredentialStatus = "unavailable"
			if errors.Is(err, ErrBTCredentialsBusy) {
				result.CredentialStatus = "busy"
			}
			return nil
		}
		result, err = s.view(row, credentials)
		return err
	})
	if err != nil {
		return BTConnectionView{}, ErrBTConnection
	}
	return result, nil
}

func (s *BTConnectionService) Configure(ctx context.Context, r BTConnectionRequest) (BTConnectionView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.credentialFactory == nil || s.hooks.Drain == nil || s.hooks.Invalidate == nil || s.hooks.Resume == nil {
		return BTConnectionView{}, ErrBTConnection
	}
	r.ProwlarrBase = strings.TrimSpace(r.ProwlarrBase)
	r.QBBase = strings.TrimSpace(r.QBBase)
	r.QBUsername = strings.TrimSpace(r.QBUsername)
	for _, endpoint := range []string{r.ProwlarrBase, r.QBBase} {
		c, err := btclient.New(endpoint, "")
		if err != nil {
			return BTConnectionView{}, err
		}
		c.Close()
	}
	if len(r.QBUsername) > 200 || !filepath.IsAbs(r.DownloadRoot) || strings.TrimSpace(r.DownloadRoot) == "" {
		return BTConnectionView{}, ErrBTConnection
	}
	r.DownloadRoot = filepath.Clean(r.DownloadRoot)
	// Read the optimistic snapshot under a short lease, then drain without a DB
	// lease: a worker being drained may itself need to complete database work.
	var old models.BTConnection
	var originalDB *gorm.DB
	var maintenanceStarted <-chan struct{}
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		originalDB = database.DB
		maintenanceStarted = database.MaintenanceStarted()
		readErr := db.Where("slot = ?", "local").First(&old).Error
		if errors.Is(readErr, gorm.ErrRecordNotFound) {
			return nil
		}
		return readErr
	})
	if err != nil {
		return BTConnectionView{}, ErrBTConnection
	}
	if old.Revision != r.ExpectedRevision {
		return BTConnectionView{}, ErrBTConflict
	}
	replace := old.ID != "" && (old.ProwlarrBase != r.ProwlarrBase || old.QBBase != r.QBBase || old.DownloadRoot != r.DownloadRoot)
	// A failed drain may already have closed command admission. The coordinator
	// Resume hook owns generation/stop guards, including concurrent maintenance.
	defer s.hooks.Resume()
	if err = s.hooks.Drain(ctx); err != nil {
		return BTConnectionView{}, ErrBTConnection
	}
	var result BTConnectionView
	err = database.WithOperationContext(ctx, func(stable *gorm.DB) error {
		// Detect even a completed maintenance cycle that restored the same IDs,
		// revisions and epochs, including an in-place restore using the same handle.
		select {
		case <-maintenanceStarted:
			return ErrBTConflict
		default:
		}
		if database.DB != originalDB {
			return ErrBTConflict
		}
		credentials, factoryErr := s.credentialFactory(ctx, stable)
		if factoryErr != nil {
			return factoryErr
		}
		next := models.BTConnection{ID: old.ID, Slot: "local", Revision: old.Revision + 1, TaskEpoch: old.TaskEpoch, ProwlarrBase: r.ProwlarrBase, QBBase: r.QBBase, QBUsername: r.QBUsername, DownloadRoot: r.DownloadRoot, Enabled: r.Enabled}
		if old.ID == "" || replace {
			next.ID = uuid.NewString()
			next.TaskEpoch = 0
		}
		var casCause error
		_, err = credentials.Save(ctx, old.CredentialRef, r.ProwlarrKey, r.QBPassword, func(ref string) (bool, error) {
			next.CredentialRef = ref
			conflict := false
			e := stable.Transaction(func(tx *gorm.DB) error {
				if replace {
					var count int64
					if err := tx.Model(&models.BTTask{}).Where("connection_id = ? AND stage NOT IN ?", old.ID, []string{"cancelled", "released", "abandoned"}).Count(&count).Error; err != nil {
						return err
					}
					if count > 0 {
						return ErrBTConnectionActive
					}
				}
				if old.ID == "" {
					return tx.Create(&next).Error
				}
				if replace {
					result := tx.Model(&models.BTConnection{}).Where("id = ? AND revision = ? AND task_epoch = ? AND slot = ?", old.ID, old.Revision, old.TaskEpoch, "local").Updates(map[string]any{"slot": "retired:" + old.ID, "credential_ref": "", "enabled": false, "revision": old.Revision + 1})
					if result.Error != nil {
						return result.Error
					}
					if result.RowsAffected != 1 {
						conflict = true
						return ErrBTConflict
					}
					return tx.Create(&next).Error
				}
				result := tx.Model(&models.BTConnection{}).Where("id = ? AND revision = ? AND task_epoch = ? AND slot = ?", old.ID, old.Revision, old.TaskEpoch, "local").Updates(map[string]any{"prowlarr_base": next.ProwlarrBase, "qb_base": next.QBBase, "qb_username": next.QBUsername, "download_root": next.DownloadRoot, "enabled": next.Enabled, "credential_ref": ref, "revision": next.Revision})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					conflict = true
					return ErrBTConflict
				}
				return nil
			})
			if conflict {
				return false, nil
			}
			if errors.Is(e, ErrBTConnectionActive) {
				casCause = e
				return false, nil
			}
			if e != nil {
				casCause = e
				return false, e
			}
			return true, nil
		})
		if err != nil {
			s.hooks.Invalidate(old.Revision + 1)
			if errors.Is(casCause, ErrBTConnectionActive) {
				return ErrBTConnectionActive
			}
			if casCause != nil {
				return ErrBTConnection
			}
			return err
		}
		s.hooks.Invalidate(next.Revision)
		result, err = s.view(next, credentials)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrBTConflict) || errors.Is(err, ErrBTConnectionActive) || errors.Is(err, ErrBTCredentials) || errors.Is(err, ErrBTCredentialsBusy) {
			return BTConnectionView{}, err
		}
		return BTConnectionView{}, ErrBTConnection
	}
	return result, nil

}

// BTConnectionClients is backend-only; Close after a test or when a generation drains.
type BTConnectionClients struct {
	InstanceID string
	Revision   uint64
	Prowlarr   *btclient.Client
	QB         *btclient.Client
	QBUsername string
	qbPassword string
}

func (c *BTConnectionClients) Close() { c.Prowlarr.Close(); c.QB.Close() }
func (s *BTConnectionService) Clients(ctx context.Context) (*BTConnectionClients, error) {
	var clients *BTConnectionClients
	err := database.WithOperationContext(ctx, func(db *gorm.DB) error {
		var row models.BTConnection
		if err := db.Where("slot = ?", "local").First(&row).Error; err != nil || !row.Enabled {
			return ErrBTConnection
		}
		credentials, err := s.credentialFactory(ctx, db)
		if err != nil {
			return err
		}
		secret, err := credentials.Load(row.CredentialRef)
		if err != nil {
			return err
		}
		p, err := btclient.New(row.ProwlarrBase, secret.ProwlarrKey)
		if err != nil {
			return err
		}
		q, err := btclient.New(row.QBBase, "")
		if err != nil {
			p.Close()
			return err
		}
		clients = &BTConnectionClients{InstanceID: row.ID, Revision: row.Revision, Prowlarr: p, QB: q, QBUsername: row.QBUsername, qbPassword: secret.QBPassword}
		return nil
	})
	return clients, err
}

// AuthenticateQB allows passwordless loopback setups; no auth settings are modified.
func (c *BTConnectionClients) AuthenticateQB(ctx context.Context) error {
	if c.qbPassword != "" || c.QBUsername != "" {
		return c.QB.Login(ctx, c.QBUsername, c.qbPassword)
	}
	return nil
}

type BTConnectionTestResult struct {
	ProwlarrStatus        string `json:"prowlarr_status"`
	QBStatus              string `json:"qb_status"`
	ProwlarrVersion       string `json:"prowlarr_version"`
	QBVersion             string `json:"qb_version"`
	QBAPIVersion          string `json:"qb_api_version"`
	MetadataPreviewStatus string `json:"metadata_preview_status"`
}

func (s *BTConnectionService) Test(ctx context.Context) (BTConnectionTestResult, error) {
	c, err := s.Clients(ctx)
	if err != nil {
		return BTConnectionTestResult{}, err
	}
	defer c.Close()
	out := BTConnectionTestResult{ProwlarrStatus: "ok", QBStatus: "ok", MetadataPreviewStatus: "unavailable"}
	if _, err := c.Prowlarr.Indexers(ctx); err != nil {
		out.ProwlarrStatus = btConnectionProbeStatus(err)
	}
	if version, e := c.Prowlarr.ProwlarrVersion(ctx); e != nil {
		out.ProwlarrStatus = btConnectionProbeStatus(e)
	} else {
		out.ProwlarrVersion = version
	}
	if e := c.AuthenticateQB(ctx); e != nil {
		out.QBStatus = btConnectionProbeStatus(e)
	} else {
		version, api, e := c.QB.QBVersion(ctx)
		if e != nil {
			out.QBStatus = btConnectionProbeStatus(e)
		} else {
			out.QBVersion, out.QBAPIVersion = version, api
			if e = c.QB.CheckQBProfile(ctx); e != nil {
				out.QBStatus = btConnectionProbeStatus(e)
			}
			if e = c.QB.CheckQBMetadataProfile(ctx); e == nil {
				out.MetadataPreviewStatus = "supported"
			} else if errors.Is(e, btclient.ErrVersionUnsupported) {
				out.MetadataPreviewStatus = "unsupported"
			}
		}
	}
	return out, nil
}
func (s *BTConnectionService) CleanupCredentials(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return database.WithOperationContext(ctx, func(db *gorm.DB) error {
		credentials, err := s.credentialFactory(ctx, db)
		if err != nil {
			return err
		}
		return credentials.Cleanup(func() (map[string]bool, error) {
			var refs []string
			if err := db.Model(&models.BTConnection{}).Where("credential_ref <> ?", "").Pluck("credential_ref", &refs).Error; err != nil {
				return nil, ErrBTConnection
			}
			live := map[string]bool{}
			for _, ref := range refs {
				live[ref] = true
			}
			return live, nil
		})
	})
}

func btConnectionProbeStatus(err error) string {
	if errors.Is(err, btclient.ErrAuthentication) {
		return "auth_failed"
	}
	if errors.Is(err, btclient.ErrVersionUnsupported) {
		return "version_unsupported"
	}
	return "unavailable"
}
