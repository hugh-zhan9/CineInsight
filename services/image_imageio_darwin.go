//go:build darwin

package services

/*
#cgo LDFLAGS: -framework ImageIO -framework CoreFoundation -framework CoreGraphics
#include <CoreFoundation/CoreFoundation.h>
#include <ImageIO/ImageIO.h>
#include <CoreGraphics/CoreGraphics.h>
#include <stdlib.h>

static int ci_imageio_convert_jpeg(const char *src_path, const char *dst_path, int max_edge) {
    if (!src_path || !dst_path || max_edge <= 0) {
        return -1;
    }
    CFURLRef src_url = CFURLCreateFromFileSystemRepresentation(
        kCFAllocatorDefault, (const UInt8 *)src_path, strlen(src_path), false);
    if (!src_url) {
        return -2;
    }
    CGImageSourceRef source = CGImageSourceCreateWithURL(src_url, NULL);
    CFRelease(src_url);
    if (!source) {
        return -3;
    }

    CFNumberRef max_size_num = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &max_edge);
    const void *thumb_keys[4] = {
        kCGImageSourceCreateThumbnailFromImageAlways,
        kCGImageSourceCreateThumbnailWithTransform,
        kCGImageSourceShouldCacheImmediately,
        kCGImageSourceThumbnailMaxPixelSize
    };
    const void *thumb_vals[4] = {
        kCFBooleanTrue,
        kCFBooleanTrue,
        kCFBooleanTrue,
        max_size_num
    };
    CFDictionaryRef thumb_opts = CFDictionaryCreate(
        kCFAllocatorDefault, thumb_keys, thumb_vals, 4,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    CFRelease(max_size_num);

    CGImageRef image = CGImageSourceCreateThumbnailAtIndex(source, 0, thumb_opts);
    CFRelease(thumb_opts);
    CFRelease(source);
    if (!image) {
        return -4;
    }

    CFURLRef dst_url = CFURLCreateFromFileSystemRepresentation(
        kCFAllocatorDefault, (const UInt8 *)dst_path, strlen(dst_path), false);
    if (!dst_url) {
        CGImageRelease(image);
        return -5;
    }
    CFStringRef jpeg_uti = CFSTR("public.jpeg");
    CGImageDestinationRef dest = CGImageDestinationCreateWithURL(dst_url, jpeg_uti, 1, NULL);
    CFRelease(dst_url);
    if (!dest) {
        CGImageRelease(image);
        return -6;
    }

    float quality = 0.84f;
    CFNumberRef quality_num = CFNumberCreate(kCFAllocatorDefault, kCFNumberFloatType, &quality);
    const void *dst_keys[1] = { kCGImageDestinationLossyCompressionQuality };
    const void *dst_vals[1] = { quality_num };
    CFDictionaryRef dst_opts = CFDictionaryCreate(
        kCFAllocatorDefault, dst_keys, dst_vals, 1,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    CFRelease(quality_num);

    CGImageDestinationAddImage(dest, image, dst_opts);
    bool ok = CGImageDestinationFinalize(dest);
    CFRelease(dst_opts);
    CFRelease(dest);
    CGImageRelease(image);
    return ok ? 0 : -7;
}

static int ci_imageio_probe_dimensions(const char *src_path, int *out_width, int *out_height) {
    if (!src_path || !out_width || !out_height) {
        return -1;
    }
    CFURLRef src_url = CFURLCreateFromFileSystemRepresentation(
        kCFAllocatorDefault, (const UInt8 *)src_path, strlen(src_path), false);
    if (!src_url) {
        return -2;
    }
    CGImageSourceRef source = CGImageSourceCreateWithURL(src_url, NULL);
    CFRelease(src_url);
    if (!source) {
        return -3;
    }
    CFDictionaryRef props = CGImageSourceCopyPropertiesAtIndex(source, 0, NULL);
    CFRelease(source);
    if (!props) {
        return -4;
    }
    int w = 0, h = 0, orientation = 1;
    CFNumberRef w_ref = (CFNumberRef)CFDictionaryGetValue(props, kCGImagePropertyPixelWidth);
    CFNumberRef h_ref = (CFNumberRef)CFDictionaryGetValue(props, kCGImagePropertyPixelHeight);
    CFNumberRef o_ref = (CFNumberRef)CFDictionaryGetValue(props, kCGImagePropertyOrientation);
    if (w_ref) CFNumberGetValue(w_ref, kCFNumberIntType, &w);
    if (h_ref) CFNumberGetValue(h_ref, kCFNumberIntType, &h);
    if (o_ref) CFNumberGetValue(o_ref, kCFNumberIntType, &orientation);
    CFRelease(props);
    if (w <= 0 || h <= 0) {
        return -5;
    }
    if (orientation >= 5 && orientation <= 8) {
        int tmp = w;
        w = h;
        h = tmp;
    }
    *out_width = w;
    *out_height = h;
    return 0;
}
*/
import "C"

import (
	"context"
	"fmt"
	"unsafe"
)

func nativeFastImageThumbnail(ctx context.Context, sourcePath, destinationPath string, maxEdge int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cSrc := C.CString(sourcePath)
	defer C.free(unsafe.Pointer(cSrc))
	cDst := C.CString(destinationPath)
	defer C.free(unsafe.Pointer(cDst))
	if rc := C.ci_imageio_convert_jpeg(cSrc, cDst, C.int(maxEdge)); rc != 0 {
		return fmt.Errorf("imageio thumbnail failed code=%d", int(rc))
	}
	return nil
}

func nativeFastImageProbe(ctx context.Context, sourcePath string) (int, int, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	cSrc := C.CString(sourcePath)
	defer C.free(unsafe.Pointer(cSrc))
	var w, h C.int
	if rc := C.ci_imageio_probe_dimensions(cSrc, &w, &h); rc != 0 {
		return 0, 0, fmt.Errorf("imageio probe failed code=%d", int(rc))
	}
	return int(w), int(h), nil
}
