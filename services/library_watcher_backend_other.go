//go:build !darwin

package services

import "github.com/fsnotify/fsnotify"

func newLibraryWatchBackend() (libraryWatchBackend, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &fsnotifyLibraryWatchBackend{watcher: watcher}, nil
}
