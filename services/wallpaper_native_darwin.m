//go:build darwin && cgo

#import <Cocoa/Cocoa.h>
#import <AVFoundation/AVFoundation.h>
#import <QuartzCore/QuartzCore.h>
#import <ImageIO/ImageIO.h>
#include <math.h>
#include "wallpaper_native_darwin.h"

// 所有窗口、播放器、观察者和定时器只在主线程使用；Go 侧串行化公开操作。
static BOOL cineWallpaperOnMain(dispatch_block_t block) {
    @autoreleasepool {
        if ([NSThread isMainThread]) { block(); return YES; }
        // Wails 退出主循环后在主线程调用 shutdown。未开始的调用两秒后取消，
        // 使持 Go 操作锁的查询不会与 shutdown 互等。已经开始的操作须等系统结果。
        NSCondition *condition = [[NSCondition alloc] init];
        __block BOOL started = NO, finished = NO, cancelled = NO;
        dispatch_async(dispatch_get_main_queue(), ^{
            [condition lock];
            if (cancelled) { [condition unlock]; return; }
            started = YES; [condition unlock];
            block();
            [condition lock]; finished = YES; [condition signal]; [condition unlock];
        });
        [condition lock];
        NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:2];
        while (!finished && [condition waitUntilDate:deadline]) {}
        if (!finished && !started) cancelled = YES;
        while (started && !finished) [condition wait];
        BOOL completed = finished;
        [condition unlock]; [condition release];
        return completed;
    }
}

@interface CineWallpaperWindow : NSWindow
@end
@implementation CineWallpaperWindow
- (BOOL)canBecomeKeyWindow { return NO; }
- (BOOL)canBecomeMainWindow { return NO; }
@end

@interface CineWallpaperOutput : NSObject {
@public
    NSWindow *window;
    AVQueuePlayer *player;
    AVPlayerLooper *looper;
}
@end
@implementation CineWallpaperOutput
- (void)dealloc {
    [looper disableLooping];
    [player pause];
    [player removeAllItems];
    [window orderOut:nil];
    [window close];
    [looper release];
    [player release];
    [window release];
    [super dealloc];
}
@end

@interface CineWallpaperController : NSObject {
@public
    int state; // idle=0, starting=1, playing=2, paused=3, failed=4
    NSMutableArray *outputs;
    NSURL *sourceURL;
    NSTimer *timer;
    NSDate *startedAt;
    BOOL systemSleeping, screensSleeping, sessionInactive;
}
- (void)stop;
- (BOOL)start:(NSURL *)url;
- (void)tick:(NSTimer *)sender;
- (void)screenChanged:(NSNotification *)note;
- (void)powerChanged:(NSNotification *)note;
@end

@implementation CineWallpaperController
- (void)stop {
    if (timer == nil && outputs == nil && sourceURL == nil) { state = 0; return; }
    [timer invalidate]; [timer release]; timer = nil;
    [[NSNotificationCenter defaultCenter] removeObserver:self];
    [[[NSWorkspace sharedWorkspace] notificationCenter] removeObserver:self];
    [outputs release]; outputs = nil;
    [sourceURL release]; sourceURL = nil;
    [startedAt release]; startedAt = nil;
    systemSleeping = screensSleeping = sessionInactive = NO;
    state = 0;
}
- (NSMutableArray *)makeOutputs:(NSURL *)url {
    NSMutableArray *result = [NSMutableArray array];
    for (NSScreen *screen in [NSScreen screens]) {
        CineWallpaperOutput *output = [[[CineWallpaperOutput alloc] init] autorelease];
        output->window = [[CineWallpaperWindow alloc] initWithContentRect:screen.frame
            styleMask:NSWindowStyleMaskBorderless backing:NSBackingStoreBuffered defer:NO];
        output->window.releasedWhenClosed = NO;
        output->window.level = CGWindowLevelForKey(kCGDesktopIconWindowLevelKey) - 1;
        output->window.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces |
            NSWindowCollectionBehaviorStationary | NSWindowCollectionBehaviorIgnoresCycle;
        output->window.ignoresMouseEvents = YES;
        output->window.hidesOnDeactivate = NO;
        output->window.hasShadow = NO;
        output->window.backgroundColor = [NSColor blackColor];
        output->window.contentView.wantsLayer = YES;
        output->player = [[AVQueuePlayer alloc] init];
        output->player.muted = YES;
        AVPlayerItem *item = [AVPlayerItem playerItemWithURL:url];
        output->looper = [[AVPlayerLooper playerLooperWithPlayer:output->player templateItem:item] retain];
        if (output->looper == nil) return nil;
        AVPlayerLayer *layer = [AVPlayerLayer playerLayerWithPlayer:output->player];
        layer.frame = output->window.contentView.bounds;
        layer.autoresizingMask = kCALayerWidthSizable | kCALayerHeightSizable;
        layer.videoGravity = AVLayerVideoGravityResizeAspectFill;
        [output->window.contentView.layer addSublayer:layer];
        [result addObject:output];
    }
    return result.count > 0 ? result : nil;
}
- (BOOL)start:(NSURL *)url {
    NSMutableArray *prepared = [self makeOutputs:url];
    if (prepared == nil) return NO;
    [prepared retain]; // stop 释放旧一轮；新窗口准备失败前旧播放不变。
    [self stop];
    outputs = prepared;
    sourceURL = [url retain];
    startedAt = [[NSDate date] retain];
    state = 1;
    for (CineWallpaperOutput *output in outputs) {
        [output->window orderFront:nil];
        [output->player play];
    }
    [[NSNotificationCenter defaultCenter] addObserver:self selector:@selector(screenChanged:)
        name:NSApplicationDidChangeScreenParametersNotification object:nil];
    NSNotificationCenter *workspace = [[NSWorkspace sharedWorkspace] notificationCenter];
    for (NSString *name in @[NSWorkspaceWillSleepNotification, NSWorkspaceDidWakeNotification,
            NSWorkspaceScreensDidSleepNotification, NSWorkspaceScreensDidWakeNotification,
            NSWorkspaceSessionDidResignActiveNotification, NSWorkspaceSessionDidBecomeActiveNotification]) {
        [workspace addObserver:self selector:@selector(powerChanged:) name:name object:nil];
    }
    timer = [[NSTimer timerWithTimeInterval:0.5 target:self selector:@selector(tick:) userInfo:nil repeats:YES] retain];
    [[NSRunLoop mainRunLoop] addTimer:timer forMode:NSRunLoopCommonModes];
    return YES;
}
- (void)tick:(NSTimer *)sender {
    if (outputs.count == 0) return;
    BOOL ready = YES, failed = NO;
    for (CineWallpaperOutput *output in outputs) {
        failed |= output->player.status == AVPlayerStatusFailed ||
            output->player.currentItem.status == AVPlayerItemStatusFailed ||
            output->looper.status == AVPlayerLooperStatusFailed;
        ready &= output->player.currentItem != nil && output->player.currentItem.status == AVPlayerItemStatusReadyToPlay;
    }
    BOOL paused = systemSleeping || screensSleeping || sessionInactive;
    if (failed || (!paused && !ready && -[startedAt timeIntervalSinceNow] > 15)) {
        [self stop]; state = 4;
        return;
    }
    state = paused ? 3 : (ready ? 2 : 1);
}
- (void)powerChanged:(NSNotification *)note {
    // NSWorkspace 通知通常来自主线程，显式转交以保证所有对象同一所有者。
    cineWallpaperOnMain(^{
        if (sourceURL == nil || outputs.count == 0) return;
        NSString *name = note.name;
        if ([name isEqualToString:NSWorkspaceWillSleepNotification]) systemSleeping = YES;
        if ([name isEqualToString:NSWorkspaceDidWakeNotification]) systemSleeping = NO;
        if ([name isEqualToString:NSWorkspaceScreensDidSleepNotification]) screensSleeping = YES;
        if ([name isEqualToString:NSWorkspaceScreensDidWakeNotification]) screensSleeping = NO;
        if ([name isEqualToString:NSWorkspaceSessionDidResignActiveNotification]) sessionInactive = YES;
        if ([name isEqualToString:NSWorkspaceSessionDidBecomeActiveNotification]) sessionInactive = NO;
        BOOL paused = systemSleeping || screensSleeping || sessionInactive;
        if (!paused) { [startedAt release]; startedAt = [[NSDate date] retain]; }
        for (CineWallpaperOutput *output in outputs) {
            if (paused) [output->player pause]; else [output->player play];
        }
        [self tick:nil];
    });
}
- (void)screenChanged:(NSNotification *)note {
    cineWallpaperOnMain(^{
        if (sourceURL == nil) return;
        NSMutableArray *prepared = [[self makeOutputs:sourceURL] retain];
        [outputs release]; outputs = prepared;
        if (prepared == nil) { [self stop]; state = 4; return; }
        [startedAt release]; startedAt = [[NSDate date] retain];
        for (CineWallpaperOutput *output in outputs) {
            [output->window orderFront:nil];
            if (!systemSleeping && !screensSleeping && !sessionInactive) [output->player play];
        }
        [self tick:nil];
    });
}
- (void)dealloc { [self stop]; [super dealloc]; }
@end

int cineWallpaperAvailable(void) {
    @autoreleasepool {
        return [[NSProcessInfo processInfo] isOperatingSystemAtLeastVersion:(NSOperatingSystemVersion){14,0,0}];
    }
}
void *cineWallpaperNew(void) { return [[CineWallpaperController alloc] init]; }

int cineWallpaperInspect(const char *path, int video, int *width, int *height) {
    @autoreleasepool {
        NSString *value = [NSString stringWithUTF8String:path];
        if (value == nil) return 1;
        NSURL *url = [NSURL fileURLWithPath:value];
        if (!video) {
            CGImageSourceRef source = CGImageSourceCreateWithURL((CFURLRef)url, NULL);
            if (source == NULL) return 1;
            NSDictionary *properties = (NSDictionary *)CGImageSourceCopyPropertiesAtIndex(source, 0, NULL);
            *width = [properties[(NSString *)kCGImagePropertyPixelWidth] intValue];
            *height = [properties[(NSString *)kCGImagePropertyPixelHeight] intValue];
            [properties release]; CFRelease(source);
            return *width > 0 && *height > 0 ? 0 : 1;
        }
        AVURLAsset *asset = [AVURLAsset URLAssetWithURL:url options:nil];
        NSCondition *condition = [[NSCondition alloc] init];
        __block BOOL loaded = NO;
        [asset loadValuesAsynchronouslyForKeys:@[@"playable", @"tracks", @"duration"] completionHandler:^{
            [condition lock]; loaded = YES; [condition signal]; [condition unlock];
        }];
        [condition lock];
        NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:15];
        while (!loaded && [condition waitUntilDate:deadline]) {}
        BOOL completed = loaded;
        [condition unlock]; [condition release];
        if (!completed) { [asset cancelLoading]; return 1; }
        for (NSString *key in @[@"playable", @"tracks", @"duration"]) {
            if ([asset statusOfValueForKey:key error:nil] != AVKeyValueStatusLoaded) return 1;
        }
        double duration = CMTimeGetSeconds(asset.duration);
        if (!asset.playable || !isfinite(duration) || duration <= 0) return 1;
        AVAssetTrack *track = [[asset tracksWithMediaType:AVMediaTypeVideo] firstObject];
        if (track == nil) return 1;
        CGSize size = CGSizeApplyAffineTransform(track.naturalSize, track.preferredTransform);
        *width = (int)llround(fabs(size.width)); *height = (int)llround(fabs(size.height));
        return *width > 0 && *height > 0 ? 0 : 1;
    }
}

int cineWallpaperSetImage(void *controller, const char *path) {
    @autoreleasepool {
        if (NSApp == nil || controller == NULL) return 1;
        CineWallpaperController *owner = (CineWallpaperController *)controller;
        __block int result = 1;
        NSString *value = [NSString stringWithUTF8String:path];
        if (value == nil) return 1;
        NSURL *url = [NSURL fileURLWithPath:value];
        cineWallpaperOnMain(^{ @autoreleasepool {
            result = 0;
            NSArray *screens = [NSScreen screens];
            NSWorkspace *workspace = [NSWorkspace sharedWorkspace];
            NSMutableArray *oldURLs = [NSMutableArray array], *oldOptions = [NSMutableArray array];
            for (NSScreen *screen in screens) {
                [oldURLs addObject:[workspace desktopImageURLForScreen:screen] ?: [NSNull null]];
                [oldOptions addObject:[workspace desktopImageOptionsForScreen:screen] ?: @{}];
            }
            if (screens.count == 0) { result = 1; return; }
            NSDictionary *options = @{NSWorkspaceDesktopImageScalingKey:@(NSImageScaleProportionallyUpOrDown),
                NSWorkspaceDesktopImageAllowClippingKey:@YES};
            for (NSUInteger index = 0; index < screens.count; index++) {
                if (![workspace setDesktopImageURL:url forScreen:screens[index] options:options error:nil]) {
                    result = 1;
                    // 连失败屏幕也尝试恢复：系统 API 失败不代表绝无副作用。
                    for (NSUInteger changed = 0; changed <= index; changed++) {
                        if (oldURLs[changed] == [NSNull null] || ![workspace setDesktopImageURL:oldURLs[changed]
                            forScreen:screens[changed] options:oldOptions[changed] error:nil]) result = 2;
                    }
                    break;
                }
            }
            if (result == 0) [owner stop];
        }});
        return result;
    }
}

int cineWallpaperStartVideo(void *controller, const char *path) {
    @autoreleasepool {
        if (NSApp == nil || controller == NULL) return 1;
        __block int result = 1;
        // 排队的 block 拷贝会持有对象，不能捕获可能已被 Go 释放的 C 字符串。
        CineWallpaperController *owner = (CineWallpaperController *)controller;
        NSString *value = [NSString stringWithUTF8String:path];
        if (value == nil) return 1;
        NSURL *url = [NSURL fileURLWithPath:value];
        cineWallpaperOnMain(^{ @autoreleasepool {
            if ([owner start:url]) result = 0;
        }});
        return result;
    }
}
int cineWallpaperStatus(void *controller) {
    if (NSApp == nil || controller == NULL) return 0;
    CineWallpaperController *owner = (CineWallpaperController *)controller;
    __block int result = 5;
    cineWallpaperOnMain(^{ result = owner->state; });
    return result;
}
int cineWallpaperStop(void *controller) {
    if (controller == NULL) return 0;
    CineWallpaperController *owner = (CineWallpaperController *)controller;
    if (NSApp == nil) { [owner stop]; return 0; }
    return cineWallpaperOnMain(^{ @autoreleasepool { [owner stop]; }}) ? 0 : 1;
}
void cineWallpaperClose(void *controller) {
    if (controller == NULL) return;
    if (NSApp == nil) { [(CineWallpaperController *)controller release]; return; }
    CineWallpaperController *owner = (CineWallpaperController *)controller;
    dispatch_block_t close = ^{ @autoreleasepool { [owner stop]; [owner release]; }};
    if ([NSThread isMainThread]) close(); else dispatch_async(dispatch_get_main_queue(), close);
}
