//go:build !darwin || !cgo

package services

import "fmt"

type unavailableWallpaperPlatform struct{}

func newWallpaperPlatform() wallpaperPlatform        { return unavailableWallpaperPlatform{} }
func (unavailableWallpaperPlatform) Available() bool { return false }
func (unavailableWallpaperPlatform) Inspect(string, string) (int, int, error) {
	return 0, 0, fmt.Errorf("当前平台不支持桌面壁纸。")
}
func (unavailableWallpaperPlatform) SetImage(string) error {
	return fmt.Errorf("当前平台不支持桌面壁纸。")
}
func (unavailableWallpaperPlatform) StartVideo(string) error {
	return fmt.Errorf("当前平台不支持动态壁纸。")
}
func (unavailableWallpaperPlatform) Status() wallpaperNativeStatus {
	return wallpaperNativeStatus{state: "idle"}
}
func (unavailableWallpaperPlatform) Stop() error { return nil }
func (unavailableWallpaperPlatform) Close()      {}
