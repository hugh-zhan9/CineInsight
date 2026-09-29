//go:build !darwin

package services

// Other platforms still validate the configured root's existence and identity.
func scanVolumeAvailable(string) error { return nil }
