#include <CoreServices/CoreServices.h>
#include <dispatch/dispatch.h>
#include <stdint.h>
#include <stdlib.h>

extern void cineLibraryFSEvent(uintptr_t token, char *path, uint32_t flags);

typedef struct {
    FSEventStreamRef stream;
    dispatch_queue_t queue;
} CineLibraryStream;

static void cineLibraryCallback(ConstFSEventStreamRef stream, void *info,
        size_t count, void *paths, const FSEventStreamEventFlags flags[],
        const FSEventStreamEventId ids[]) {
    char **names = (char **)paths;
    for (size_t i = 0; i < count; i++) {
        cineLibraryFSEvent((uintptr_t)info, names[i], flags[i]);
    }
}

static void cineLibraryQueueBarrier(void *context) {}

static void cineLibraryStop(CineLibraryStream *watch) {
    FSEventStreamStop(watch->stream);
    FSEventStreamInvalidate(watch->stream);
    // Wait for callbacks before Go deletes their cgo.Handle. Callbacks never block.
    dispatch_sync_f(watch->queue, NULL, cineLibraryQueueBarrier);
    FSEventStreamRelease(watch->stream);
    dispatch_release(watch->queue);
    free(watch);
}

static CineLibraryStream *cineLibraryStart(const char *path, uintptr_t token) {
    CFStringRef name = CFStringCreateWithCString(NULL, path, kCFStringEncodingUTF8);
    if (!name) return NULL;
    CFArrayRef paths = CFArrayCreate(NULL, (const void **)&name, 1, &kCFTypeArrayCallBacks);
    CFRelease(name);
    if (!paths) return NULL;
    FSEventStreamContext context = {0, (void *)token, NULL, NULL, NULL};
    FSEventStreamRef stream = FSEventStreamCreate(NULL, cineLibraryCallback,
        &context, paths, kFSEventStreamEventIdSinceNow, 0.1,
        kFSEventStreamCreateFlagFileEvents | kFSEventStreamCreateFlagWatchRoot |
        kFSEventStreamCreateFlagNoDefer);
    CFRelease(paths);
    if (!stream) return NULL;
    CineLibraryStream *watch = calloc(1, sizeof(CineLibraryStream));
    if (!watch) { FSEventStreamRelease(stream); return NULL; }
    watch->stream = stream;
    watch->queue = dispatch_queue_create("cineinsight.library.fsevents", NULL);
    if (!watch->queue) { FSEventStreamRelease(stream); free(watch); return NULL; }
    FSEventStreamSetDispatchQueue(stream, watch->queue);
    if (!FSEventStreamStart(stream)) {
        cineLibraryStop(watch);
        return NULL;
    }
    return watch;
}
