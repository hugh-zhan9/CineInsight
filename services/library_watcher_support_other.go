//go:build !darwin && !linux && !windows

package services

func classifyLibraryWatchRoot(string) (bool, string, error) {
	return true, "", nil
}
