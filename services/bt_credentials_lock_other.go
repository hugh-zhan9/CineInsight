//go:build !darwin && !linux

package services

type BTFileCredentialLock struct{ Path string }

func (BTFileCredentialLock) TryLock() (func(), error) { return nil, ErrBTCredentials }
