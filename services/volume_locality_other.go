//go:build !darwin

package services

import "strings"

func isObviouslyExternalPath(cleaned string) bool {
	if strings.HasPrefix(cleaned, `\\`) {
		return true
	}
	return strings.HasPrefix(cleaned, "/mnt/") || strings.HasPrefix(cleaned, "/media/")
}

func probeVolumeLocality(_ string) bool {
	return false
}
