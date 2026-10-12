#import <Foundation/Foundation.h>
#import <Vision/Vision.h>
#import <ImageIO/ImageIO.h>
#import <CoreImage/CoreImage.h>
#import "vision.h"
#include <math.h>

static NSString * const FaceModel = @"apple-faceprint-cpu-r3737841669-pad25-v1";
static NSString * const ImageModel = @"apple-image-featureprint-r2-768-v1";
static void Fail(NSString *code) { @throw [NSException exceptionWithName:code reason:nil userInfo:nil]; }
static void ValidateFloats(NSData *data, NSUInteger count) {
    if (![data isKindOfClass:NSData.class] || data.length != count * sizeof(float)) Fail(@"apple_vision_shape");
    const float *values = data.bytes;
    for (NSUInteger i = 0; i < count; i++) if (!isfinite(values[i])) Fail(@"apple_vision_shape");
}
static CGImageRef LoadImage(NSString *path) {
    if (![path isKindOfClass:NSString.class] || !path.isAbsolutePath) Fail(@"apple_vision_input");
    NSURL *url = [NSURL fileURLWithPath:path];
    CGImageSourceRef source = CGImageSourceCreateWithURL((__bridge CFURLRef)url, NULL);
    if (!source) Fail(@"apple_vision_decode");
    NSDictionary *properties = CFBridgingRelease(CGImageSourceCopyPropertiesAtIndex(source, 0, NULL));
    double width = [properties[(__bridge NSString *)kCGImagePropertyPixelWidth] doubleValue];
    double height = [properties[(__bridge NSString *)kCGImagePropertyPixelHeight] doubleValue];
    if (width < 1 || height < 1 || width * height > 100000000) { CFRelease(source); Fail(@"apple_vision_image_size"); }
    // ImageIO applies EXIF orientation; all downstream boxes refer to upright pixels.
    NSDictionary *options = @{(__bridge NSString *)kCGImageSourceCreateThumbnailFromImageAlways:@YES,
        (__bridge NSString *)kCGImageSourceCreateThumbnailWithTransform:@YES,
        (__bridge NSString *)kCGImageSourceThumbnailMaxPixelSize:@(MAX(width, height))};
    CGImageRef image = CGImageSourceCreateThumbnailAtIndex(source, 0, (__bridge CFDictionaryRef)options);
    CFRelease(source);
    if (!image) Fail(@"apple_vision_decode");
    return image;
}
static VNRequest *FaceRequest(void) {
    Class cls = NSClassFromString(@"VNCreateFaceprintRequest");
    if (!cls || ![cls isSubclassOfClass:VNRequest.class]) Fail(@"apple_face_unsupported");
    VNRequest *request = [[cls alloc] init];
    // Apple internal request uses a private revision. Pin the validated revision,
    // reject OS changes before comparing persisted vectors.
    if (request.revision != 3737841669UL) Fail(@"apple_face_version_changed");
    request.usesCPUOnly = YES;
    return request;
}
static NSArray *Faces(CGImageRef image) {
    size_t w = CGImageGetWidth(image), h = CGImageGetHeight(image);
    size_t border = (size_t)lrint(MIN(w, h) * .25), pw = w + 2 * border, ph = h + 2 * border;
    CGColorSpaceRef color = CGColorSpaceCreateDeviceRGB();
    CGContextRef context = CGBitmapContextCreate(NULL, pw, ph, 8, 0, color, (CGBitmapInfo)kCGImageAlphaPremultipliedLast);
    CGColorSpaceRelease(color);
    if (!context) Fail(@"apple_vision_image_size");
    CGContextSetRGBFillColor(context, 128.0/255, 128.0/255, 128.0/255, 1);
    CGContextFillRect(context, CGRectMake(0, 0, pw, ph));
    CGContextDrawImage(context, CGRectMake(border, border, w, h), image);
    CGImageRef padded = CGBitmapContextCreateImage(context);
    CGContextRelease(context);
    VNRequest *request = FaceRequest();
    VNImageRequestHandler *handler = [[VNImageRequestHandler alloc] initWithCGImage:padded options:@{}];
    CGImageRelease(padded);
    NSError *error = nil;
    if (![handler performRequests:@[request] error:&error]) Fail(@"apple_face_failed");
    NSMutableArray *faces = [NSMutableArray array];
    for (VNFaceObservation *face in request.results) {
        id print = [face valueForKey:@"faceprint"];
        if (!print) Fail(@"apple_face_feature_missing");
        NSData *data = [print valueForKey:@"VNEntityIdentificationModelPrintData"];
        if ([[print valueForKey:@"VNEntityIdentificationModelPrintElementCount"] unsignedIntegerValue] != 128) Fail(@"apple_vision_shape");
        ValidateFloats(data, 128);
        CGRect b = face.boundingBox;
        double x0 = MAX(0, (b.origin.x * pw - border) / w);
        double y0 = MAX(0, ((1 - CGRectGetMaxY(b)) * ph - border) / h);
        double x1 = MIN(1, (CGRectGetMaxX(b) * pw - border) / w);
        double y1 = MIN(1, ((1 - b.origin.y) * ph - border) / h);
        if (x1 <= x0 || y1 <= y0) continue;
        [faces addObject:@{@"bbox":@[@(x0),@(y0),@(x1-x0),@(y1-y0)],
            @"quality":@(face.confidence), @"embedding":[data base64EncodedStringWithOptions:0]}];
    }
    return faces;
}
static NSString *ImageFeatures(CGImageRef image) {
    VNGenerateImageFeaturePrintRequest *request = [VNGenerateImageFeaturePrintRequest new];
    request.revision = VNGenerateImageFeaturePrintRequestRevision2;
    request.usesCPUOnly = YES;
    VNImageRequestHandler *handler = [[VNImageRequestHandler alloc] initWithCGImage:image options:@{}];
    if (![handler performRequests:@[request] error:nil]) Fail(@"apple_image_feature_failed");
    VNFeaturePrintObservation *print = request.results.firstObject;
    if (!print || print.elementCount != 768 || print.elementType != VNElementTypeFloat) Fail(@"apple_vision_shape");
    ValidateFloats(print.data, 768);
    return [print.data base64EncodedStringWithOptions:0];
}
char *CineVisionRequest(const char *json) {
    @autoreleasepool {
        NSMutableDictionary *response = [NSMutableDictionary dictionary];
        @try {
            NSData *input = [NSData dataWithBytes:json length:strlen(json)];
            NSDictionary *request = [NSJSONSerialization JSONObjectWithData:input options:0 error:nil];
            if (![request isKindOfClass:NSDictionary.class]) Fail(@"apple_vision_input");
            if ([request[@"id"] isKindOfClass:NSString.class]) response[@"id"] = request[@"id"];
            NSString *operation = request[@"operation"];
            if ([operation isEqual:@"face_status"]) {
                (void)FaceRequest(); response[@"supported"] = @YES; response[@"model_id"] = FaceModel;
            } else if ([operation isEqual:@"image_status"]) {
                if (![VNGenerateImageFeaturePrintRequest.supportedRevisions containsIndex:2]) Fail(@"apple_image_unsupported");
                response[@"supported"] = @YES; response[@"model_id"] = ImageModel;
            } else if ([operation isEqual:@"face"] || !operation) {
                CGImageRef image = LoadImage(request[@"path"]);
                @try { response[@"faces"] = Faces(image); response[@"model_id"] = FaceModel; }
                @finally { CGImageRelease(image); }
            } else if ([operation isEqual:@"image_features"]) {
                NSArray *paths = request[@"paths"];
                if (![paths isKindOfClass:NSArray.class] || paths.count < 1 || paths.count > 32) Fail(@"apple_vision_input");
                NSMutableArray *vectors = [NSMutableArray array];
                for (NSString *path in paths) { @autoreleasepool {
                    CGImageRef image = LoadImage(path);
                    @try { [vectors addObject:ImageFeatures(image)]; }
                    @finally { CGImageRelease(image); }
                }}
                response[@"vectors"] = vectors; response[@"model_id"] = ImageModel;
            } else Fail(@"apple_vision_input");
        } @catch (NSException *exception) {
            // Never forward private API exception descriptions or input paths.
            response[@"error"] = [exception.name hasPrefix:@"apple_"] ? exception.name : @"apple_vision_failed";
        }
        NSData *output = [NSJSONSerialization dataWithJSONObject:response options:0 error:nil];
        NSString *text = [[NSString alloc] initWithData:output encoding:NSUTF8StringEncoding];
        return strdup(text.UTF8String ?: "{\"error\":\"apple_vision_failed\"}");
    }
}
int CineFaceWorker(void) {
    setbuf(stdout, NULL);
    char *status = CineVisionRequest("{\"operation\":\"face_status\"}");
    BOOL failed = strstr(status, "\"error\"") != NULL;
    if (failed) { puts(status); free(status); return 1; }
    free(status);
    puts("{\"ready\":true,\"model_id\":\"apple-faceprint-cpu-r3737841669-pad25-v1\"}");
    char buffer[65536];
    while (fgets(buffer, sizeof(buffer), stdin)) {
        if (!strchr(buffer, '\n')) return 2;
        char *response = CineVisionRequest(buffer); puts(response); free(response);
    }
    return 0;
}
