package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
)

var ErrBTCredentials = errors.New("bt_credentials_unavailable: 系统凭据不可用")
var ErrBTCredentialsBusy = errors.New("bt_credentials_busy: 凭据正在由另一实例更新")
var ErrBTConflict = errors.New("bt_conflict: 配置已变化")

// BTSecrets remains backend-only; it is never a response DTO.
type BTSecrets struct {
	ProwlarrKey string
	QBPassword  string
}
type BTSecretChange struct {
	Operation string `json:"operation"`
	Value     string `json:"value"`
}

// BTSecretStore creates immutable references; Put must reject an existing reference.
type BTSecretStore interface {
	Put(string, []byte) error
	Get(string) ([]byte, error)
	Delete(string) error
	List() ([]string, error)
}

// BTCredentialManager serializes saves and orphan cleanup across the DB CAS boundary.
// The caller drains its old client generation before Save and never replays CAS side effects.
type BTSecretLocker interface{ TryLock() (func(), error) }

type BTCredentialManager struct {
	mu     sync.Mutex
	store  BTSecretStore
	locker BTSecretLocker
}

// NewBTCredentialManager accepts an explicit test locker or a scoped system store
// implementing BTSecretLocker. Missing synchronization is an error, never a fallback.
func NewBTCredentialManager(store BTSecretStore, locks ...BTSecretLocker) *BTCredentialManager {
	m := &BTCredentialManager{store: store}
	if len(locks) == 1 {
		m.locker = locks[0]
	} else if len(locks) == 0 {
		m.locker, _ = store.(BTSecretLocker)
	}
	return m
}
func (m *BTCredentialManager) lockScope() (func(), error) {
	if m.locker == nil {
		return nil, ErrBTCredentials
	}
	return m.locker.TryLock()
}

func btSecretRef() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", ErrBTCredentials
	}
	return "ci-bt-" + hex.EncodeToString(b), nil
}
func (m *BTCredentialManager) load(ref string) (BTSecrets, error) {
	if ref == "" {
		return BTSecrets{}, nil
	}
	raw, err := m.store.Get(ref)
	if err != nil {
		return BTSecrets{}, ErrBTCredentials
	}
	var s BTSecrets
	if len(raw) > 32768 || json.Unmarshal(raw, &s) != nil {
		return BTSecrets{}, ErrBTCredentials
	}
	return s, nil
}
func (m *BTCredentialManager) Load(ref string) (BTSecrets, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := m.lockScope()
	if err != nil {
		return BTSecrets{}, err
	}
	defer unlock()
	return m.load(ref)
}
func btChange(old string, c BTSecretChange) (string, error) {
	switch c.Operation {
	case "", "keep":
		if c.Value != "" {
			return "", ErrBTCredentials
		}
		return old, nil
	case "clear":
		if c.Value != "" {
			return "", ErrBTCredentials
		}
		return "", nil
	case "replace":
		if len(c.Value) > 8192 || c.Value == "" {
			return "", ErrBTCredentials
		}
		return c.Value, nil
	}
	return "", ErrBTCredentials
}

// Save writes a new immutable bundle then invokes the injected DB CAS exactly once.
// false means conflict; an error means the DB outcome is unknown, so both refs remain.
func (m *BTCredentialManager) Save(ctx context.Context, oldRef string, key, password BTSecretChange, cas func(string) (bool, error)) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, lockErr := m.lockScope()
	if lockErr != nil {
		return "", lockErr
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var old BTSecrets
	var err error
	if key.Operation == "" || key.Operation == "keep" || password.Operation == "" || password.Operation == "keep" {
		old, err = m.load(oldRef)
		if err != nil {
			return "", err
		}
	}
	next := old
	next.ProwlarrKey, err = btChange(old.ProwlarrKey, key)
	if err != nil {
		return "", err
	}
	next.QBPassword, err = btChange(old.QBPassword, password)
	if err != nil {
		return "", err
	}
	ref := ""
	if next.ProwlarrKey != "" || next.QBPassword != "" {
		ref, err = btSecretRef()
		if err != nil {
			return "", err
		}
		raw, _ := json.Marshal(next)
		if m.store.Put(ref, raw) != nil {
			return "", ErrBTCredentials
		}
	}
	if err := ctx.Err(); err != nil {
		if ref != "" {
			_ = m.store.Delete(ref)
		}
		return "", err
	}
	ok, err := cas(ref)
	if err != nil {
		return "", ErrBTCredentials
	} // commit may have happened: orphan cleanup checks DB later
	if !ok {
		if ref != "" {
			_ = m.store.Delete(ref)
		}
		return "", ErrBTConflict
	}
	if oldRef != "" && oldRef != ref {
		_ = m.store.Delete(oldRef)
	}
	return ref, nil
}

// Cleanup obtains the authoritative references under the same serialization as Save.
func (m *BTCredentialManager) Cleanup(referenced func() (map[string]bool, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := m.lockScope()
	if err != nil {
		return err
	}
	defer unlock()
	live, err := referenced()
	if err != nil {
		return ErrBTCredentials
	}
	refs, err := m.store.List()
	if err != nil {
		return ErrBTCredentials
	}
	for _, ref := range refs {
		if !live[ref] {
			if m.store.Delete(ref) != nil {
				return ErrBTCredentials
			}
		}
	}
	return nil
}
