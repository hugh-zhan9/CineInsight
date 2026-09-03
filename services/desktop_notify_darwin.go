//go:build darwin

package services

// darwin 的桌面通知与 Dock 角标（D-012）。这是仓库里唯一的 cgo 文件，
// 由 build tag 与 desktop_notify_other.go 互斥：其他平台连编译都不会碰到它。
//
// 两个刻意的选择：
//   - 用 NSUserNotificationCenter 而不是 UNUserNotificationCenter。后者要弹授权框、
//     要签名 entitlement，设计里明确推迟（D-012）。前者自 macOS 11 起标记废弃，
//     所以下面关掉了 deprecated 警告——不是没看见，是有意为之。
//   - 所有 AppKit 调用都 dispatch 到主队列。Go 的 goroutine 跑在任意线程上，
//     NSApp / dockTile 只能在主线程碰。
//
// 非 .app bundle 运行（wails dev、go test）时 defaultUserNotificationCenter 与 NSApp
// 都可能是 nil，两个入口都判空后直接返回，不崩。

/*
#cgo CFLAGS: -x objective-c -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Cocoa

#include <stdlib.h>
#include <string.h>
#import <Cocoa/Cocoa.h>

static void cineinsightDeliverNotification(const char *title, const char *body) {
	// C 字符串归 Go 侧所有，函数返回后就会被 free；block 是异步跑的，先拷一份。
	char *titleCopy = strdup(title != NULL ? title : "");
	char *bodyCopy = strdup(body != NULL ? body : "");
	dispatch_async(dispatch_get_main_queue(), ^{
		@autoreleasepool {
			NSUserNotificationCenter *center = [NSUserNotificationCenter defaultUserNotificationCenter];
			if (center != nil) {
				NSUserNotification *note = [[NSUserNotification alloc] init];
				note.title = [NSString stringWithUTF8String:titleCopy];
				note.informativeText = [NSString stringWithUTF8String:bodyCopy];
				[center deliverNotification:note];
				[note release];
			}
		}
		free(titleCopy);
		free(bodyCopy);
	});
}

// cineinsightHasBundleIdentifier 报告本进程是否有 bundle id。没有（wails dev、
// go test 直接跑二进制）时 NSUserNotification 会静默不投递，值得日志里留一行。
// NSBundle 的 mainBundle / bundleIdentifier 可以在任意线程读，不需要 dispatch。
static int cineinsightHasBundleIdentifier(void) {
	@autoreleasepool {
		NSString *identifier = [[NSBundle mainBundle] bundleIdentifier];
		return identifier != nil && identifier.length > 0 ? 1 : 0;
	}
}

static void cineinsightSetDockBadge(const char *label) {
	char *labelCopy = strdup(label != NULL ? label : "");
	dispatch_async(dispatch_get_main_queue(), ^{
		@autoreleasepool {
			NSApplication *application = NSApp;
			if (application != nil) {
				NSDockTile *tile = [application dockTile];
				if (tile != nil) {
					NSString *value = [NSString stringWithUTF8String:labelCopy];
					tile.badgeLabel = value.length > 0 ? value : nil;
					[tile display];
				}
			}
		}
		free(labelCopy);
	});
}
*/
import "C"

import (
	"log"
	"sync"
	"unsafe"
)

type darwinDesktopNotifier struct{}

// bundleWarnOnce 保证"没有 bundle id"这句话每个进程只说一次：
// 构造函数在启动与数据库恢复路径上都会被调用。
var bundleWarnOnce sync.Once

func newPlatformDesktopNotifier() DesktopNotifier {
	if C.cineinsightHasBundleIdentifier() == 0 {
		bundleWarnOnce.Do(func() {
			log.Printf("桌面通知：当前进程没有 bundle id（wails dev / 直接跑二进制），系统通知可能静默不显示；Dock 角标不受影响")
		})
	}
	return darwinDesktopNotifier{}
}

func (darwinDesktopNotifier) Notify(title, body string) {
	cTitle := C.CString(title)
	defer C.free(unsafe.Pointer(cTitle))
	cBody := C.CString(body)
	defer C.free(unsafe.Pointer(cBody))
	C.cineinsightDeliverNotification(cTitle, cBody)
}

func (darwinDesktopNotifier) SetBadge(label string) {
	cLabel := C.CString(label)
	defer C.free(unsafe.Pointer(cLabel))
	C.cineinsightSetDockBadge(cLabel)
}
