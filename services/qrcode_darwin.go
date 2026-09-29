//go:build darwin && cgo

package services

// 手机端访问二维码（D-PC47，详细设计 §8.8）：用系统自带的 CoreImage CIQRCodeGenerator 生成，
// 不引入第三方库。cgo 只在这里，构建约束与 qrcode_other.go 互斥：无 cgo 的 darwin 构建与其他平台
// 走 qrcode_other.go，编译不受影响，只是不显示二维码。

/*
#cgo CFLAGS: -x objective-c -Wno-deprecated-declarations
#cgo LDFLAGS: -framework CoreImage -framework CoreGraphics -framework ImageIO -framework Foundation

#include <stdlib.h>
#include <string.h>
#import <Foundation/Foundation.h>
#import <CoreImage/CoreImage.h>
#import <CoreGraphics/CoreGraphics.h>
#import <ImageIO/ImageIO.h>

// cineinsightQRCodePNG 把文本渲染成带白边（quiet zone）的 PNG，返回 malloc 出来的字节，
// 调用方负责 free。失败返回 NULL。scale 是每个模块的像素数，margin 是白边的像素数。
static unsigned char *cineinsightQRCodePNG(const char *text, int scale, int margin, size_t *outLen) {
	@autoreleasepool {
		if (text == NULL || outLen == NULL) {
			return NULL;
		}
		NSString *string = [NSString stringWithUTF8String:text];
		NSData *message = [string dataUsingEncoding:NSUTF8StringEncoding];
		if (message == nil || message.length == 0) {
			return NULL;
		}
		CIFilter *filter = [CIFilter filterWithName:@"CIQRCodeGenerator"];
		if (filter == nil) {
			return NULL;
		}
		[filter setValue:message forKey:@"inputMessage"];
		[filter setValue:@"M" forKey:@"inputCorrectionLevel"];
		CIImage *qr = filter.outputImage;
		if (qr == nil) {
			return NULL;
		}
		CIContext *context = [CIContext contextWithOptions:nil];
		CGImageRef base = [context createCGImage:qr fromRect:qr.extent];
		if (base == NULL) {
			return NULL;
		}
		size_t modules = CGImageGetWidth(base);
		size_t side = modules * (size_t)scale + 2 * (size_t)margin;

		CGColorSpaceRef space = CGColorSpaceCreateDeviceRGB();
		CGContextRef bitmap = CGBitmapContextCreate(NULL, side, side, 8, 0, space, (CGBitmapInfo)kCGImageAlphaPremultipliedLast);
		CGColorSpaceRelease(space);
		if (bitmap == NULL) {
			CGImageRelease(base);
			return NULL;
		}
		CGContextSetRGBFillColor(bitmap, 1, 1, 1, 1);
		CGContextFillRect(bitmap, CGRectMake(0, 0, side, side));
		// 最近邻放大：二维码的每个模块必须是清晰的方块，插值会糊掉边缘。
		CGContextSetInterpolationQuality(bitmap, kCGInterpolationNone);
		CGContextDrawImage(bitmap, CGRectMake(margin, margin, modules * scale, modules * scale), base);
		CGImageRelease(base);
		CGImageRef finalImage = CGBitmapContextCreateImage(bitmap);
		CGContextRelease(bitmap);
		if (finalImage == NULL) {
			return NULL;
		}

		CFMutableDataRef data = CFDataCreateMutable(NULL, 0);
		CGImageDestinationRef destination = CGImageDestinationCreateWithData(data, CFSTR("public.png"), 1, NULL);
		if (destination == NULL) {
			CFRelease(data);
			CGImageRelease(finalImage);
			return NULL;
		}
		CGImageDestinationAddImage(destination, finalImage, NULL);
		bool ok = CGImageDestinationFinalize(destination);
		CFRelease(destination);
		CGImageRelease(finalImage);
		if (!ok) {
			CFRelease(data);
			return NULL;
		}
		size_t length = (size_t)CFDataGetLength(data);
		unsigned char *result = (unsigned char *)malloc(length);
		if (result != NULL) {
			memcpy(result, CFDataGetBytePtr(data), length);
			*outLen = length;
		}
		CFRelease(data);
		return result;
	}
}
*/
import "C"

import (
	"errors"
	"unsafe"
)

// generateQRCodePNG 返回文本对应的二维码 PNG 字节。
func generateQRCodePNG(text string) ([]byte, error) {
	if text == "" {
		return nil, errors.New("二维码内容为空")
	}
	cText := C.CString(text)
	defer C.free(unsafe.Pointer(cText))
	var length C.size_t
	raw := C.cineinsightQRCodePNG(cText, 8, 32, &length)
	if raw == nil {
		return nil, errors.New("生成二维码失败")
	}
	defer C.free(unsafe.Pointer(raw))
	return C.GoBytes(unsafe.Pointer(raw), C.int(length)), nil
}
