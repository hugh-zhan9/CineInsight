#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
static id evaluate(WKWebView *view, NSString *js) {
 __block BOOL finished=NO; __block id value=nil;
 [view evaluateJavaScript:js completionHandler:^(id result,NSError *error){value=result;finished=YES;if(error){fprintf(stderr,"JS: %s\n",error.localizedDescription.UTF8String);}}];
 NSDate *limit=[NSDate dateWithTimeIntervalSinceNow:15];
 while(!finished && limit.timeIntervalSinceNow>0) [[NSRunLoop mainRunLoop] runUntilDate:[NSDate dateWithTimeIntervalSinceNow:0.01]];
 if(!finished) {fprintf(stderr,"evaluation timed out\n");exit(1);}return value;
}
int main(int argc,char **argv) {@autoreleasepool {
 if(argc!=2)return 2;
 [NSApplication sharedApplication];[NSApp setActivationPolicy:NSApplicationActivationPolicyProhibited];[NSApp finishLaunching];
 NSWindow *window=[[NSWindow alloc]initWithContentRect:NSMakeRect(30,30,1160,760) styleMask:NSWindowStyleMaskBorderless backing:NSBackingStoreBuffered defer:NO];
 WKWebView *view=[[WKWebView alloc]initWithFrame:window.contentView.bounds];window.contentView=view;
 window.alphaValue=1; [window orderFront:nil];
 NSURL *url=[NSURL fileURLWithPath:[NSString stringWithUTF8String:argv[1]]];[view loadFileURL:url allowingReadAccessToURL:url.URLByDeletingLastPathComponent];
 NSDate *limit=[NSDate dateWithTimeIntervalSinceNow:55];BOOL ready=NO;
 while(!ready && limit.timeIntervalSinceNow>0) {[[NSRunLoop mainRunLoop]runUntilDate:[NSDate dateWithTimeIntervalSinceNow:0.1]];ready=[evaluate(view,@"Boolean(window.ready)") boolValue];}
 if(!ready){fprintf(stderr,"fixture not ready\n");return 1;}
 evaluate(view,@"fixture.run().catch(e=>window.resultJSON=JSON.stringify({error:String(e)})); void 0");
 NSString *result=nil;limit=[NSDate dateWithTimeIntervalSinceNow:55];
 while(!result && limit.timeIntervalSinceNow>0){[[NSRunLoop mainRunLoop]runUntilDate:[NSDate dateWithTimeIntervalSinceNow:0.1]];id v=evaluate(view,@"window.results ? JSON.stringify(window.results) : (window.resultJSON || null)");if([v isKindOfClass:NSString.class])result=v;}
 [window orderOut:nil];if(!result){fprintf(stderr,"fixture did not finish: %s\n", [[evaluate(view,@"window.progress") description] UTF8String]);return 1;} puts(result.UTF8String);id report=[NSJSONSerialization JSONObjectWithData:[result dataUsingEncoding:NSUTF8StringEncoding] options:0 error:nil]; return [report isKindOfClass:NSDictionary.class] && [report[@"passed"] boolValue] ? 0 : 1;
}}
