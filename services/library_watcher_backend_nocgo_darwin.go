//go:build darwin && !cgo

package services

import "errors"

func newLibraryWatchBackend() (libraryWatchBackend, error) {
	return nil, errors.New("此构建未启用 macOS FSEvents，请使用启用 CGO 的构建")
}
