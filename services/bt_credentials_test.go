package services

import (
	"context"
	"errors"
	"sync"
	"testing"
)

var btTestSecretLock sync.Mutex

type btMemorySecrets map[string][]byte

func (s btMemorySecrets) Put(k string, v []byte) error {
	if _, ok := s[k]; ok {
		return errors.New("exists")
	}
	s[k] = append([]byte{}, v...)
	return nil
}
func (s btMemorySecrets) Get(k string) ([]byte, error) {
	v, ok := s[k]
	if !ok {
		return nil, errors.New("missing")
	}
	return v, nil
}
func (s btMemorySecrets) Delete(k string) error { delete(s, k); return nil }
func (s btMemorySecrets) List() ([]string, error) {
	out := []string{}
	for k := range s {
		out = append(out, k)
	}
	return out, nil
}
func TestBTCredentialCAS(t *testing.T) {
	store := btMemorySecrets{}
	m := NewBTCredentialManager(store)
	ctx := context.Background()
	replace := BTSecretChange{Operation: "replace", Value: "first"}
	ref, err := m.Save(ctx, "", replace, BTSecretChange{}, func(next string) (bool, error) {
		if _, ok := store[next]; !ok {
			t.Fatal("CAS before secret write")
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Save(ctx, ref, BTSecretChange{Operation: "replace", Value: "loser"}, BTSecretChange{}, func(string) (bool, error) { return false, nil })
	if !errors.Is(err, ErrBTConflict) || len(store) != 1 {
		t.Fatalf("conflict: %v %d", err, len(store))
	}
	old, _ := m.Load(ref)
	if old.ProwlarrKey != "first" {
		t.Fatal("old credential mutated")
	}
	_, err = m.Save(ctx, ref, replace, BTSecretChange{}, func(string) (bool, error) { return false, errors.New("ambiguous commit") })
	if err == nil || len(store) != 2 {
		t.Fatal("uncertain CAS lost potential live secret")
	}
	if err = m.Cleanup(func() (map[string]bool, error) { return map[string]bool{ref: true}, nil }); err != nil || len(store) != 1 {
		t.Fatal("orphan cleanup")
	}
	cleared, err := m.Save(ctx, ref, BTSecretChange{Operation: "clear"}, BTSecretChange{}, func(next string) (bool, error) {
		if next != "" {
			t.Fatal(next)
		}
		if _, ok := store[ref]; !ok {
			t.Fatal("deleted before CAS")
		}
		return true, nil
	})
	if err != nil || cleared != "" || len(store) != 0 {
		t.Fatal("clear failed")
	}
}

func TestBTCredentialReconnectMissingReference(t *testing.T) {
	m := NewBTCredentialManager(btMemorySecrets{})
	_, err := m.Save(context.Background(), "missing", BTSecretChange{Operation: "replace", Value: "key"}, BTSecretChange{Operation: "clear"}, func(string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal("explicit replacement cannot recover lost secrets", err)
	}
}

func (btMemorySecrets) TryLock() (func(), error) {
	if !btTestSecretLock.TryLock() {
		return nil, ErrBTCredentialsBusy
	}
	return btTestSecretLock.Unlock, nil
}
