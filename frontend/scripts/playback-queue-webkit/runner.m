#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
static void pump(double duration) {
 NSDate *end=[NSDate dateWithTimeIntervalSinceNow:duration];
 do {
  NSEvent *event=[NSApp nextEventMatchingMask:NSEventMaskAny untilDate:[NSDate dateWithTimeIntervalSinceNow:0.005] inMode:NSDefaultRunLoopMode dequeue:YES];
  if(event)[NSApp sendEvent:event];
  [NSApp updateWindows];
  [[NSRunLoop mainRunLoop]runUntilDate:[NSDate dateWithTimeIntervalSinceNow:0.005]];
 } while(end.timeIntervalSinceNow>0);
}
static id evaluate(WKWebView *view, NSString *js) {
 __block BOOL finished=NO; __block id value=nil;
 [view evaluateJavaScript:js completionHandler:^(id result,NSError *error){value=result;finished=YES;if(error){fprintf(stderr,"JS: %s\n",error.localizedDescription.UTF8String);}}];
 NSDate *limit=[NSDate dateWithTimeIntervalSinceNow:15];
 while(!finished && limit.timeIntervalSinceNow>0) pump(0.01);
 if(!finished) {fprintf(stderr,"evaluation timed out\n");exit(1);}return value;
}
@interface NativeClick : NSObject<WKScriptMessageHandler>
@property(nonatomic,weak) WKWebView *view;
@end
@implementation NativeClick
-(void)userContentController:(WKUserContentController*)controller didReceiveScriptMessage:(WKScriptMessage*)message {
 NSDictionary *data=message.body; WKWebView *view=self.view;
 CGFloat x=[data[@"x"] doubleValue], y=[data[@"y"] doubleValue];
 NSPoint point=[view convertPoint:NSMakePoint(x,view.isFlipped?y:view.bounds.size.height-y) toView:nil];
 NSEvent *down=[NSEvent mouseEventWithType:NSEventTypeLeftMouseDown location:point modifierFlags:0 timestamp:NSProcessInfo.processInfo.systemUptime windowNumber:view.window.windowNumber context:nil eventNumber:1 clickCount:1 pressure:1];
 NSEvent *up=[NSEvent mouseEventWithType:NSEventTypeLeftMouseUp location:point modifierFlags:0 timestamp:NSProcessInfo.processInfo.systemUptime windowNumber:view.window.windowNumber context:nil eventNumber:2 clickCount:1 pressure:0];
 [view.window sendEvent:down];[view.window sendEvent:up];
}
@end
int main(int argc,char **argv) {@autoreleasepool {
 if(argc<2 || argc>3)return 2;
 [NSApplication sharedApplication];[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];[NSApp finishLaunching];
 NSWindow *window=[[NSWindow alloc]initWithContentRect:NSMakeRect(30,30,1160,760) styleMask:(NSWindowStyleMaskTitled|NSWindowStyleMaskClosable|NSWindowStyleMaskResizable) backing:NSBackingStoreBuffered defer:NO];
 WKWebViewConfiguration *config=[WKWebViewConfiguration new];
 config.preferences.elementFullscreenEnabled=YES;
 NativeClick *clicks=[NativeClick new];[config.userContentController addScriptMessageHandler:clicks name:@"nativeClick"];
 WKWebView *view=[[WKWebView alloc]initWithFrame:window.contentView.bounds configuration:config];clicks.view=view;window.contentView=view;
 window.alphaValue=1; [window makeKeyAndOrderFront:nil]; [NSApp activateIgnoringOtherApps:YES];
 NSURL *url=[NSURL fileURLWithPath:[NSString stringWithUTF8String:argv[1]]];[view loadFileURL:url allowingReadAccessToURL:url.URLByDeletingLastPathComponent];
 NSDate *limit=[NSDate dateWithTimeIntervalSinceNow:55];BOOL ready=NO;
 while(!ready && limit.timeIntervalSinceNow>0) {pump(0.1);ready=[evaluate(view,@"Boolean(window.ready)") boolValue];}
 if(!ready){fprintf(stderr,"fixture not ready\n");return 1;}
 evaluate(view,@"fixture.run().catch(e=>window.resultJSON=JSON.stringify({error:String(e)})); void 0");
 NSString *result=nil;limit=[NSDate dateWithTimeIntervalSinceNow:90];
 while(!result && limit.timeIntervalSinceNow>0){pump(0.1);id v=evaluate(view,@"window.results ? JSON.stringify(window.results) : (window.resultJSON || null)");if([v isKindOfClass:NSString.class])result=v;}
 if(argc>2) {
  __block BOOL done=NO;
  [view takeSnapshotWithConfiguration:nil completionHandler:^(NSImage *img,NSError *err){
    if(img) {NSBitmapImageRep *rep=[[NSBitmapImageRep alloc]initWithData:img.TIFFRepresentation];[[rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}]writeToFile:[NSString stringWithUTF8String:argv[2]] atomically:YES];}
    done=YES;
  }];
  NSDate *end=[NSDate dateWithTimeIntervalSinceNow:5];while(!done && end.timeIntervalSinceNow>0)pump(0.05);
 }
 [window orderOut:nil];if(!result){fprintf(stderr,"fixture did not finish: %s\n", [[evaluate(view,@"window.progress") description] UTF8String]);return 1;} puts(result.UTF8String);id report=[NSJSONSerialization JSONObjectWithData:[result dataUsingEncoding:NSUTF8StringEncoding] options:0 error:nil]; return [report isKindOfClass:NSDictionary.class] && [report[@"passed"] boolValue] ? 0 : 1;
}}
