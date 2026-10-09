#ifndef CINE_WALLPAPER_H
#define CINE_WALLPAPER_H
int cineWallpaperAvailable(void);
void *cineWallpaperNew(void);
int cineWallpaperInspect(const char *path, int video, int *width, int *height);
int cineWallpaperSetImage(void *controller, const char *path);
int cineWallpaperStartVideo(void *controller, const char *path);
int cineWallpaperStatus(void *controller);
int cineWallpaperStop(void *controller);
void cineWallpaperClose(void *controller);
#endif
