//go:build darwin && cgo

package services

/*
#cgo CFLAGS: -x objective-c -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Cocoa -framework AVFoundation -framework QuartzCore -framework ImageIO -framework CoreMedia
#include <stdlib.h>
#include "wallpaper_native_darwin.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

type darwinWallpaperPlatform struct{ controller unsafe.Pointer }

func newWallpaperPlatform() wallpaperPlatform {
	return &darwinWallpaperPlatform{controller: C.cineWallpaperNew()}
}

func (*darwinWallpaperPlatform) Available() bool { return C.cineWallpaperAvailable() != 0 }

func (*darwinWallpaperPlatform) Inspect(path, kind string) (int, int, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	var width, height C.int
	var video C.int
	if kind == "video" {
		video = 1
	}
	if C.cineWallpaperInspect(cPath, video, &width, &height) != 0 {
		return 0, 0, fmt.Errorf("系统无法解码该素材，请选择 macOS 支持的图片或视频格式。")
	}
	return int(width), int(height), nil
}

func (p *darwinWallpaperPlatform) SetImage(path string) error {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	switch C.cineWallpaperSetImage(p.controller, cPath) {
	case 0:
		return nil
	case 2:
		return fmt.Errorf("壁纸未全部设置成功，部分显示器可能已改变，请在系统设置中检查壁纸。")
	default:
		return fmt.Errorf("系统未能设置图片壁纸，请检查图片格式、权限和显示器状态。")
	}
}

func (p *darwinWallpaperPlatform) StartVideo(path string) error {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	if C.cineWallpaperStartVideo(p.controller, cPath) != 0 {
		return fmt.Errorf("无法启动动态壁纸，请检查应用和显示器状态。")
	}
	return nil
}

func (p *darwinWallpaperPlatform) Status() wallpaperNativeStatus {
	switch C.cineWallpaperStatus(p.controller) {
	case 1:
		return wallpaperNativeStatus{"starting", "正在启动动态壁纸…"}
	case 2:
		return wallpaperNativeStatus{"playing", "无声循环播放中"}
	case 3:
		return wallpaperNativeStatus{"paused", "动态壁纸已随系统暂停"}
	case 4:
		return wallpaperNativeStatus{"failed", "动态壁纸播放失败，已移除桌面播放层，请选择系统支持的视频。"}
	case 5:
		return wallpaperNativeStatus{"starting", "正在等待系统桌面响应…"}
	default:
		return wallpaperNativeStatus{state: "idle"}
	}
}

func (p *darwinWallpaperPlatform) Stop() error {
	if C.cineWallpaperStop(p.controller) != 0 {
		return fmt.Errorf("系统桌面暂时没有响应，动态壁纸尚未停止，请重试。")
	}
	return nil
}
func (p *darwinWallpaperPlatform) Close() {
	C.cineWallpaperClose(p.controller)
	p.controller = nil
}
