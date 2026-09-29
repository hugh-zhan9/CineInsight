//go:build darwin && cgo

package services

// macOS 系统废纸篓（D-PC01）：NSFileManager trashItemAtURL:resultingItemURL:error:。
//
// 写法沿用 desktop_notify_darwin.go：cgo + Objective-C 序言。C 字符串的所有权规则很简单——
// 入参归 Go 侧（调用返回后释放），出参（resulting、message）由 C 函数 strdup，Go 侧读完就 free。
// NSError 的解读不在这里做：这里只把 domain 是否为 Cocoa、错误码、底层 POSIX 码和本地化文案
// 原样交出去，映射表在 mapSystemTrashError（平台无关、可单测）。

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation

#include <stdlib.h>
#include <string.h>
#import <Foundation/Foundation.h>

// cineinsightTrashItem 返回 0 表示成功（*resulting 为废纸篓里的实际路径），非 0 表示失败。
static int cineinsightTrashItem(const char *path, char **resulting, int *isCocoa, long *code, long *posix, char **message) {
	@autoreleasepool {
		*resulting = NULL;
		*isCocoa = 0;
		*code = 0;
		*posix = 0;
		*message = NULL;
		NSString *nsPath = [NSString stringWithUTF8String:path];
		if (nsPath == nil) {
			*message = strdup("路径不是有效的 UTF-8");
			return 1;
		}
		NSURL *url = [NSURL fileURLWithPath:nsPath];
		NSURL *resultURL = nil;
		NSError *error = nil;
		BOOL ok = [[NSFileManager defaultManager] trashItemAtURL:url resultingItemURL:&resultURL error:&error];
		if (ok) {
			if (resultURL != nil && resultURL.path != nil) {
				*resulting = strdup([resultURL.path UTF8String]);
			}
			return 0;
		}
		if (error != nil) {
			*isCocoa = [error.domain isEqualToString:NSCocoaErrorDomain] ? 1 : 0;
			*code = (long)error.code;
			NSError *underlying = error.userInfo[NSUnderlyingErrorKey];
			if (underlying != nil && [underlying.domain isEqualToString:NSPOSIXErrorDomain]) {
				*posix = (long)underlying.code;
			} else if ([error.domain isEqualToString:NSPOSIXErrorDomain]) {
				*posix = (long)error.code;
			}
			NSString *description = error.localizedDescription;
			if (description != nil) {
				*message = strdup([description UTF8String]);
			}
		}
		return 1;
	}
}
*/
import "C"

import (
	"errors"
	"unsafe"
)

// moveToSystemTrash 把文件移入当前用户的系统废纸篓，返回它在废纸篓里的实际路径。
func moveToSystemTrash(path string) (string, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))

	var (
		resulting *C.char
		isCocoa   C.int
		code      C.long
		posix     C.long
		message   *C.char
	)
	status := C.cineinsightTrashItem(cPath, &resulting, &isCocoa, &code, &posix, &message)
	defer func() {
		if resulting != nil {
			C.free(unsafe.Pointer(resulting))
		}
		if message != nil {
			C.free(unsafe.Pointer(message))
		}
	}()
	if status == 0 {
		if resulting == nil {
			// 系统没有回报实际路径：文件确实进了废纸篓，但后面恢复需要这个路径。宁可当作失败，
			// 让调用方走崩溃恢复分支按身份去找，也不能编一个路径。
			return "", errors.New("移到废纸篓失败: 系统没有返回废纸篓中的位置")
		}
		return C.GoString(resulting), nil
	}
	text := ""
	if message != nil {
		text = C.GoString(message)
	}
	return "", mapSystemTrashError(isCocoa != 0, int(code), int(posix), text, path)
}
