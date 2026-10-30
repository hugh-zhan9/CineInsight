//go:build !darwin || !cgo

package services

type btUnavailableSecretStore struct{}

func NewBTSystemSecretStore(string) BTSecretStore           { return btUnavailableSecretStore{} }
func (btUnavailableSecretStore) Put(string, []byte) error   { return ErrBTCredentials }
func (btUnavailableSecretStore) Get(string) ([]byte, error) { return nil, ErrBTCredentials }
func (btUnavailableSecretStore) Delete(string) error        { return ErrBTCredentials }
func (btUnavailableSecretStore) List() ([]string, error)    { return nil, ErrBTCredentials }

func (btUnavailableSecretStore) TryLock() (func(), error) { return nil, ErrBTCredentials }
