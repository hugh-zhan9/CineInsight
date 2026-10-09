#!/usr/bin/env bash
# 在隐藏窗口中验证真正的 AppKit/AVFoundation 生命周期，不改变系统壁纸。
set -euo pipefail
if [[ "$(uname -s)" != Darwin ]]; then
  echo 'SKIP: requires macOS and a graphical session'
  exit 77
fi
if ! command -v ffmpeg >/dev/null; then
  echo 'SKIP: ffmpeg is required to generate a synthetic video'
  exit 77
fi
wallpaper_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
wallpaper_tmp="$(mktemp -d)"
trap 'rm -rf -- "$wallpaper_tmp"' EXIT
ffmpeg -v error -f lavfi -i color=c=blue:s=1920x800:r=24 -t 1 -c:v libx264 -pix_fmt yuv420p "$wallpaper_tmp/sample.mp4"
cat > "$wallpaper_tmp/check.m" <<'OBJC'
#import "wallpaper_native_darwin.m"
#include <stdio.h>
#include <stdlib.h>

// 仅截断呈现，原生窗口、AVPlayer 和观察者仍是生产实现。
@interface CineWallpaperWindow (HiddenTest)
@end
@implementation CineWallpaperWindow (HiddenTest)
- (void)orderFront:(id)sender {}
@end

static void require(BOOL condition, const char *message) {
    if (!condition) { fprintf(stderr, "FAIL: %s\n", message); exit(1); }
}
static void drain(NSTimeInterval duration) {
    [[NSRunLoop mainRunLoop] runUntilDate:[NSDate dateWithTimeIntervalSinceNow:duration]];
}
int main(int argc, char **argv) {
    @autoreleasepool {
        require(argc == 2, "synthetic video path required");
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyProhibited];
        [NSApp finishLaunching];
        require([NSScreen screens].count > 0, "graphical session required");
        CineWallpaperController *owner = cineWallpaperNew();
        int width = 0, height = 0;
        require(cineWallpaperInspect(argv[1], 1, &width, &height) == 0 && width == 1920 && height == 800, "real asset inspection");
        require(cineWallpaperStartVideo(owner, argv[1]) == 0, "start native video");
        require(owner->outputs.count == [NSScreen screens].count, "one output per display");
        for (CineWallpaperOutput *output in owner->outputs) {
            require(!output->window.visible && output->window.ignoresMouseEvents, "hidden fixture and mouse passthrough");
            require(!output->window.canBecomeKeyWindow && !output->window.canBecomeMainWindow, "no focus stealing");
            require(output->window.level == CGWindowLevelForKey(kCGDesktopIconWindowLevelKey)-1, "window below icons");
            require(output->window.collectionBehavior & NSWindowCollectionBehaviorCanJoinAllSpaces, "all Spaces flag");
            require(output->player.muted, "video muted");
        }
        NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:12];
        BOOL looped = NO;
        while (!looped && [deadline timeIntervalSinceNow] > 0) {
            drain(0.1); looped = cineWallpaperStatus(owner) == 2;
            for (CineWallpaperOutput *output in owner->outputs) looped &= output->looper.loopCount >= 2;
        }
        require(looped, "real video loops at least twice");
        [owner powerChanged:[NSNotification notificationWithName:NSWorkspaceScreensDidSleepNotification object:nil]];
        require(cineWallpaperStatus(owner) == 3, "display sleep pauses");
        for (CineWallpaperOutput *output in owner->outputs) require(output->player.rate == 0, "all players paused");
        [owner powerChanged:[NSNotification notificationWithName:NSWorkspaceScreensDidWakeNotification object:nil]];
        require(cineWallpaperStatus(owner) == 2, "wake resumes");
        [owner screenChanged:nil];
        require(owner->outputs.count == [NSScreen screens].count, "display changes rebuild outputs");
        require(cineWallpaperStop(owner) == 0 && cineWallpaperStop(owner) == 0, "stop is idempotent");
        require(owner->outputs == nil && owner->timer == nil && owner->sourceURL == nil, "stop releases native resources");
        [owner powerChanged:[NSNotification notificationWithName:NSWorkspaceScreensDidWakeNotification object:nil]];
        [owner tick:nil];
        require(cineWallpaperStatus(owner) == 0, "late wake/timer events cannot revive stopped playback");

        NSString *missingPath = [[NSString stringWithUTF8String:argv[1]] stringByAppendingString:@".missing"];
        if (cineWallpaperStartVideo(owner, missingPath.UTF8String) == 0) {
            NSDate *failureDeadline = [NSDate dateWithTimeIntervalSinceNow:12];
            while (cineWallpaperStatus(owner) != 4 && [failureDeadline timeIntervalSinceNow] > 0) drain(0.1);
            require(cineWallpaperStatus(owner) == 4, "asynchronous media failure is reported");
        }
        require(owner->outputs == nil && owner->timer == nil, "failed playback releases native resources");
        require(cineWallpaperStop(owner) == 0, "failed state can be dismissed");

        // 主线程不处理队列：后台请求须超时；之后排队的取消请求不能复活播放器。
        NSCondition *condition = [[NSCondition alloc] init];
        __block BOOL finished = NO;
        __block int result = 0;
        const char *path = argv[1];
        dispatch_async(dispatch_get_global_queue(QOS_CLASS_USER_INITIATED, 0), ^{
            result = cineWallpaperStartVideo(owner, path);
            [condition lock]; finished = YES; [condition signal]; [condition unlock];
        });
        [condition lock];
        NSDate *waitDeadline = [NSDate dateWithTimeIntervalSinceNow:5];
        while (!finished && [condition waitUntilDate:waitDeadline]) {}
        require(finished && result != 0, "queued native request cancels on main-thread stall");
        [condition unlock]; [condition release];
        drain(0.2);
        require(cineWallpaperStatus(owner) == 0 && owner->outputs == nil, "cancelled request has no late side effects");
        cineWallpaperClose(owner);
        drain(0.1);
        puts("native wallpaper checks passed (hidden windows; real decode/loop, pause/wake, rebuild, stop, queue cancellation)");
    }
    return 0;
}
OBJC
clang -fno-objc-arc -fsanitize=address -Wno-deprecated-declarations -I "$wallpaper_root/services" \
  "$wallpaper_tmp/check.m" -o "$wallpaper_tmp/check" \
  -framework Cocoa -framework AVFoundation -framework QuartzCore -framework ImageIO -framework CoreMedia
ASAN_OPTIONS=detect_leaks=0 "$wallpaper_tmp/check" "$wallpaper_tmp/sample.mp4"
