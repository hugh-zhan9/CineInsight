#ifndef CINE_NATIVE_PLAYBACK_H
#define CINE_NATIVE_PLAYBACK_H
#include <stdint.h>
typedef struct CineNativePlayer CineNativePlayer;
int cine_native_register_window(void);
void cine_native_unregister_window(void);
uint64_t cine_native_window_epoch(void);
int cine_native_create(const char *library, CineNativePlayer **out, char *error, int size);
void cine_native_probe_diagnostics(CineNativePlayer *player);
int cine_native_load_at(CineNativePlayer *player, const char *path, double start);
int cine_native_load(CineNativePlayer *player, const char *path);
int cine_native_command(CineNativePlayer *player, const char *name, double value);
int cine_native_viewport(CineNativePlayer *player, double x, double y, double width, double height, int visible);
int cine_native_subtitles(CineNativePlayer *player, const char *json);
void cine_native_probe_click(CineNativePlayer *player, double x, double y);
int cine_native_probe_pixels(CineNativePlayer *player);
char *cine_native_snapshot(CineNativePlayer *player);
void cine_native_destroy(CineNativePlayer *player);
void cine_native_free(void *value);
#endif
