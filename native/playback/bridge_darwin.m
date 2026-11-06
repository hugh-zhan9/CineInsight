// The native surface is deliberately separate from playback ownership/statistics.
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <OpenGL/gl3.h>
#include <dlfcn.h>
#include <pthread.h>
#include <stdatomic.h>
#include "bridge.h"
#include "include/mpv/client.h"
#include "include/mpv/render.h"
#include "include/mpv/render_gl.h"
#define MPV_FN(name) static __typeof__(name) *p_##name
MPV_FN(mpv_create); MPV_FN(mpv_initialize); MPV_FN(mpv_set_option_string); MPV_FN(mpv_set_option);
MPV_FN(mpv_set_property); MPV_FN(mpv_get_property); MPV_FN(mpv_command);
MPV_FN(mpv_request_log_messages); MPV_FN(mpv_wait_event); MPV_FN(mpv_error_string); MPV_FN(mpv_terminate_destroy);
MPV_FN(mpv_render_context_create); MPV_FN(mpv_render_context_render); MPV_FN(mpv_render_context_update);
MPV_FN(mpv_render_context_set_update_callback); MPV_FN(mpv_render_context_free);
static void *libraryHandle;
static char *loadedLibraryPath;
static pthread_mutex_t libraryLock=PTHREAD_MUTEX_INITIALIZER;
static void onMain(void (^block)(void)) { if ([NSThread isMainThread]) block(); else dispatch_sync(dispatch_get_main_queue(), block); }
static WKWebView *findWebView(NSView *root) {
 if ([root isKindOfClass:WKWebView.class]) return (WKWebView*)root;
 for (NSView *child in root.subviews) { WKWebView *found=findWebView(child); if (found) return found; }
 return nil;
}
static WKWebView *registeredWebView;
static atomic_uint_fast64_t registeredEpoch=0;
@interface CineNativeWindowObserver : NSObject
@end
@implementation CineNativeWindowObserver
-(void)observeValueForKeyPath:(NSString*)key ofObject:(id)object change:(NSDictionary*)change context:(void*)context {
 if(object==registeredWebView&&[key isEqual:@"loading"]&&registeredWebView.loading){
  atomic_fetch_add(&registeredEpoch,1);
  for(NSView *child in registeredWebView.superview.subviews){if([child isKindOfClass:NSClassFromString(@"CineNativeContainer")])child.hidden=YES;}
 }
}
-(void)windowClosing:(NSNotification*)notification { atomic_fetch_add(&registeredEpoch,1); }
@end
static CineNativeWindowObserver *windowObserver;
uint64_t cine_native_window_epoch(void) { return atomic_load(&registeredEpoch); }
int cine_native_register_window(void) {
 __block int result=0;
 onMain(^{
  if(registeredWebView&&registeredWebView.window){if(registeredWebView.loading)result=-2;return;}
  WKWebView *found=nil;NSUInteger count=0;
  for(NSWindow *window in NSApp.windows){WKWebView *candidate=findWebView(window.contentView);if(candidate){found=candidate;count++;}}
  if(count!=1){result=-1;return;}if(found.loading){result=-2;return;}
  if(registeredWebView){[registeredWebView removeObserver:windowObserver forKeyPath:@"loading"];[[NSNotificationCenter defaultCenter]removeObserver:windowObserver];[windowObserver release];[registeredWebView release];}
  registeredWebView=[found retain];windowObserver=[CineNativeWindowObserver new];
  [registeredWebView addObserver:windowObserver forKeyPath:@"loading" options:NSKeyValueObservingOptionNew context:NULL];
  [[NSNotificationCenter defaultCenter]addObserver:windowObserver selector:@selector(windowClosing:) name:NSWindowWillCloseNotification object:registeredWebView.window];
  atomic_fetch_add(&registeredEpoch,1);
 });return result;
}
void cine_native_unregister_window(void) {
 onMain(^{atomic_fetch_add(&registeredEpoch,1);if(registeredWebView){[registeredWebView removeObserver:windowObserver forKeyPath:@"loading"];[[NSNotificationCenter defaultCenter]removeObserver:windowObserver];[windowObserver release];windowObserver=nil;[registeredWebView release];registeredWebView=nil;}});
}
@interface CineNativeContainer : NSView
@end
@implementation CineNativeContainer
-(NSView*)hitTest:(NSPoint)point { return nil; }
@end
@interface CineNativeCaption : NSView { NSLock *_pendingLock; NSString *_pendingText; BOOL _updatePending; }
-(void)queueText:(NSString*)text;
@property(nonatomic,copy) NSString *text;
@property(nonatomic,copy) NSDictionary *preferences;
@property(nonatomic,copy) NSArray *cues;
@property(nonatomic,assign) int64_t offset;
@end
@implementation CineNativeCaption
-(id)initWithFrame:(NSRect)frame { self=[super initWithFrame:frame];if(self)_pendingLock=[NSLock new];return self; }
-(void)queueText:(NSString*)text {
 [_pendingLock lock];if([_pendingText isEqualToString:text]){[_pendingLock unlock];return;}[_pendingText release];_pendingText=[text copy];BOOL enqueue=!_updatePending;_updatePending=YES;[_pendingLock unlock];
 if(enqueue)dispatch_async(dispatch_get_main_queue(),^{[self->_pendingLock lock];self.text=self->_pendingText;self->_updatePending=NO;[self->_pendingLock unlock];[self setNeedsDisplay:YES];});
}
-(BOOL)isOpaque { return NO; }
-(void)drawRect:(NSRect)dirty {
 if(!self.text.length)return;
 CGFloat ratio=.03;NSString *size=self.preferences[@"size"];
 if([size isEqual:@"small"])ratio=.025;else if([size isEqual:@"large"])ratio=.04;else if([size isEqual:@"xlarge"])ratio=.05;
 CGFloat fontSize=MAX(14,MIN(40,self.bounds.size.width*ratio));
 NSMutableParagraphStyle *paragraph=[[[NSMutableParagraphStyle alloc]init]autorelease];paragraph.alignment=NSTextAlignmentCenter;
 NSDictionary *attrs=@{NSFontAttributeName:[NSFont systemFontOfSize:fontSize weight:NSFontWeightSemibold],NSForegroundColorAttributeName:NSColor.whiteColor,NSParagraphStyleAttributeName:paragraph};
 NSRect measured=[self.text boundingRectWithSize:NSMakeSize(MAX(1,self.bounds.size.width-32),self.bounds.size.height) options:NSStringDrawingUsesLineFragmentOrigin attributes:attrs];
 CGFloat height=MIN(self.bounds.size.height,ceil(measured.size.height)+12), y=12;
 NSString *position=self.preferences[@"position"];
 if([position isEqual:@"top"])y=self.bounds.size.height-height-12;else if([position isEqual:@"middle"])y=(self.bounds.size.height-height)/2;
 NSRect rect=NSMakeRect(16,MAX(0,y),MAX(1,self.bounds.size.width-32),height);
 NSString *background=self.preferences[@"background"];
 if(![background isEqual:@"none"]){[[NSColor colorWithWhite:0 alpha:[background isEqual:@"solid"]?1:.65]setFill];[[NSBezierPath bezierPathWithRoundedRect:rect xRadius:4 yRadius:4]fill];}
 [self.text drawWithRect:NSInsetRect(rect,6,6) options:NSStringDrawingUsesLineFragmentOrigin attributes:attrs];
}
-(void)dealloc { [_text release];[_preferences release];[_cues release];[_pendingLock release];[_pendingText release];[super dealloc]; }
@end
@interface CineNativeView : NSOpenGLView {
 NSLock *_renderLock; dispatch_queue_t _renderQueue; BOOL _queued; BOOL _stopped;
 NSSize _pixels; BOOL _visible; GLuint _offscreenFBO; GLuint _offscreenTexture;
}
@property(nonatomic,assign) mpv_render_context *renderer;
@property(atomic,assign) NSUInteger frames;
@property(atomic,assign) NSUInteger decodedFrames;
@property(atomic,assign) int renderError;
@property(atomic,assign) BOOL probePixels;
@property(atomic,assign) int pixelRange;
-(void)requestRender;
-(void)updateGeometry;
-(void)stopRendering;
-(void)waitForRender;
@end
@implementation CineNativeView
-(id)initWithFrame:(NSRect)frame pixelFormat:(NSOpenGLPixelFormat*)format {
 self=[super initWithFrame:frame pixelFormat:format];if(self){_renderLock=[NSLock new];_renderQueue=dispatch_queue_create("CineInsight.native.render",DISPATCH_QUEUE_SERIAL);}return self;
}
-(void)updateGeometry { [_renderLock lock];_pixels=[self convertRectToBacking:self.bounds].size;_visible=!self.hidden;[_renderLock unlock];[self requestRender]; }
-(void)requestRender {
 [_renderLock lock];if(_queued||_stopped){[_renderLock unlock];return;}_queued=YES;[_renderLock unlock];
 dispatch_async(_renderQueue,^{ @autoreleasepool {
  [self->_renderLock lock];NSSize size=self->_pixels;BOOL visible=self->_visible&&!self->_stopped;self->_queued=NO;[self->_renderLock unlock];
  if(!self.renderer)return;if(visible&&(size.width<1||size.height<1))return;
  NSOpenGLContext *context=self.openGLContext;CGLLockContext(context.CGLContextObj);[context makeCurrentContext];
  if(!visible&&!self->_offscreenFBO){glGenFramebuffers(1,&self->_offscreenFBO);glGenTextures(1,&self->_offscreenTexture);glBindTexture(GL_TEXTURE_2D,self->_offscreenTexture);glTexImage2D(GL_TEXTURE_2D,0,GL_RGBA8,16,16,0,GL_RGBA,GL_UNSIGNED_BYTE,NULL);glBindFramebuffer(GL_FRAMEBUFFER,self->_offscreenFBO);glFramebufferTexture2D(GL_FRAMEBUFFER,GL_COLOR_ATTACHMENT0,GL_TEXTURE_2D,self->_offscreenTexture,0);}
  glBindFramebuffer(GL_FRAMEBUFFER,visible?0:self->_offscreenFBO);
  if(!visible&&glCheckFramebufferStatus(GL_FRAMEBUFFER)!=GL_FRAMEBUFFER_COMPLETE){self.renderError=MPV_ERROR_VO_INIT_FAILED;CGLUnlockContext(context.CGLContextObj);return;}
  mpv_opengl_fbo fbo={.fbo=visible?0:(int)self->_offscreenFBO,.w=visible?(int)size.width:16,.h=visible?(int)size.height:16,.internal_format=0};int flip=1,block=0;
  mpv_render_param params[]={{MPV_RENDER_PARAM_OPENGL_FBO,&fbo},{MPV_RENDER_PARAM_FLIP_Y,&flip},{MPV_RENDER_PARAM_BLOCK_FOR_TARGET_TIME,&block},{MPV_RENDER_PARAM_INVALID,NULL}};
  uint64_t updated=p_mpv_render_context_update(self.renderer);int rendered=p_mpv_render_context_render(self.renderer,params);
  if(self.probePixels){unsigned char pixels[32*32*4];glReadBuffer(GL_BACK);glReadPixels(MAX(0,(int)size.width/2-16),MAX(0,(int)size.height/2-16),32,32,GL_RGBA,GL_UNSIGNED_BYTE,pixels);int low=255,high=0;for(int i=0;i<32*32*4;i++){if(i%4==3)continue;low=MIN(low,pixels[i]);high=MAX(high,pixels[i]);}self.pixelRange=rendered<0?-1:high-low;self.probePixels=NO;}
  if(visible)[context flushBuffer];else glFlush();CGLUnlockContext(context.CGLContextObj);
  if(rendered<0)self.renderError=rendered;
  if(rendered>=0&&(updated&MPV_RENDER_UPDATE_FRAME)){self.decodedFrames++;if(visible)self.frames++;}
 } });
}
-(void)drawRect:(NSRect)dirty { [self requestRender]; }
-(void)waitForRender { dispatch_sync(_renderQueue,^{}); }
-(void)stopRendering { [_renderLock lock];_stopped=YES;[_renderLock unlock];[self waitForRender];[self.openGLContext makeCurrentContext];if(_offscreenFBO)glDeleteFramebuffers(1,&_offscreenFBO);if(_offscreenTexture)glDeleteTextures(1,&_offscreenTexture);_offscreenFBO=0;_offscreenTexture=0; }
-(BOOL)acceptsFirstResponder { return NO; }
-(void)dealloc { dispatch_release(_renderQueue);[_renderLock release];[super dealloc]; }
@end
struct CineNativePlayer { mpv_handle *core; CineNativeView *view; NSView *container; WKWebView *webview; CineNativeCaption *caption; NSArray *cues; NSArray *cueEnds; int64_t offset; dispatch_source_t clock; dispatch_queue_t clockQueue; int error; int eof; };
static void *glAddress(void *unused,const char *name) { return dlsym(RTLD_DEFAULT,name); }
static void updateFrame(void *context) {
 CineNativeView *view=(CineNativeView*)context;
 [view requestRender];
}
static int loadLibrary(const char *path,char *error,int size) {
 pthread_mutex_lock(&libraryLock);
 if(libraryHandle){int same=loadedLibraryPath&&!strcmp(loadedLibraryPath,path);if(!same)snprintf(error,size,"Native runtime identity differs from the loaded library");pthread_mutex_unlock(&libraryLock);return same?0:-1;}
 libraryHandle=dlopen(path,RTLD_NOW|RTLD_LOCAL);
 if (!libraryHandle) { snprintf(error,size,"%s",dlerror()); pthread_mutex_unlock(&libraryLock); return -1; }
#define LOAD(name) p_##name=dlsym(libraryHandle,#name); if(!p_##name){snprintf(error,size,"Missing %s",#name);dlclose(libraryHandle);libraryHandle=NULL;pthread_mutex_unlock(&libraryLock);return -1;}
 LOAD(mpv_create); LOAD(mpv_initialize); LOAD(mpv_set_option_string); LOAD(mpv_set_option); LOAD(mpv_set_property); LOAD(mpv_get_property); LOAD(mpv_command); LOAD(mpv_request_log_messages); LOAD(mpv_wait_event); LOAD(mpv_error_string); LOAD(mpv_terminate_destroy); LOAD(mpv_render_context_create); LOAD(mpv_render_context_render); LOAD(mpv_render_context_update); LOAD(mpv_render_context_set_update_callback); LOAD(mpv_render_context_free);
#undef LOAD
 loadedLibraryPath=strdup(path);
 pthread_mutex_unlock(&libraryLock); return 0;
}
int cine_native_create(const char *library,CineNativePlayer **out,char *error,int size) {
 if (loadLibrary(library,error,size)<0) return -1;
 CineNativePlayer *player=calloc(1,sizeof(*player)); player->core=p_mpv_create();
 if(!player->core){free(player);snprintf(error,size,"mpv_create failed");return -1;}
 const char *options[][2]={{"vo","libmpv"},{"config","no"},{"load-scripts","no"},{"input-default-bindings","no"},{"osc","no"},{"ytdl","no"},{"sub-auto","no"},{"sid","no"},{"audio-file-auto","no"},{"idle","yes"},{"keep-open","yes"},{"pause","yes"},{"mute","yes"},{"hwdec","auto-safe"},{"hr-seek-framedrop","no"},{"demuxer","lavf"},{"vd-lavc-threads","2"},{"ad-lavc-threads","2"},{"access-references","no"}};
 for (int i=0;i<sizeof(options)/sizeof(options[0]);i++) { int code=p_mpv_set_option_string(player->core,options[i][0],options[i][1]); if(code<0){snprintf(error,size,"Native option %s: %s",options[i][0],p_mpv_error_string(code));cine_native_destroy(player);return code;} }
 char *keys[]={"protocol_whitelist","format_whitelist"};
 mpv_node values[2]={{.format=MPV_FORMAT_STRING,.u.string="file"},{.format=MPV_FORMAT_STRING,.u.string="avi,rm,matroska,webm,mov,mpeg,mpegts,asf,flv,ogg,nut"}};
 mpv_node_list list={.num=2,.values=values,.keys=keys};mpv_node map={.format=MPV_FORMAT_NODE_MAP,.u.list=&list};
 int option=p_mpv_set_option(player->core,"demuxer-lavf-o",MPV_FORMAT_NODE,&map);if(option<0){snprintf(error,size,"Native demuxer restrictions: %s",p_mpv_error_string(option));cine_native_destroy(player);return option;}
 int result=p_mpv_initialize(player->core);
 if(result<0){snprintf(error,size,"mpv_initialize: %s",p_mpv_error_string(result));cine_native_destroy(player);return result;}
 __block int viewError=0;
 onMain(^{
  WKWebView *web=registeredWebView;
  if(!web){viewError=-1;return;}
  player->webview=[web retain];
  NSOpenGLPixelFormatAttribute attrs[]={NSOpenGLPFAOpenGLProfile,NSOpenGLProfileVersion3_2Core,NSOpenGLPFADoubleBuffer,NSOpenGLPFAAccelerated,NSOpenGLPFAColorSize,24,NSOpenGLPFAAlphaSize,8,0};
  NSOpenGLPixelFormat *format=[[NSOpenGLPixelFormat alloc]initWithAttributes:attrs];
  player->view=[[CineNativeView alloc]initWithFrame:NSMakeRect(0,0,1,1) pixelFormat:format]; [format release];
  player->view.wantsBestResolutionOpenGLSurface=YES; player->view.hidden=YES;
  player->container=[[CineNativeContainer alloc]initWithFrame:NSMakeRect(0,0,1,1)];player->container.wantsLayer=YES;player->container.layer.masksToBounds=YES;
  [web.superview addSubview:player->container positioned:NSWindowAbove relativeTo:web];[player->container addSubview:player->view];
  player->caption=[[CineNativeCaption alloc]initWithFrame:NSZeroRect];[player->container addSubview:player->caption positioned:NSWindowAbove relativeTo:player->view];
  [player->view.openGLContext makeCurrentContext];
  mpv_opengl_init_params gl={.get_proc_address=glAddress,.get_proc_address_ctx=NULL};
  mpv_render_param params[]={{MPV_RENDER_PARAM_API_TYPE,MPV_RENDER_API_TYPE_OPENGL},{MPV_RENDER_PARAM_OPENGL_INIT_PARAMS,&gl},{MPV_RENDER_PARAM_INVALID,NULL}};
  mpv_render_context *renderer=NULL; viewError=p_mpv_render_context_create(&renderer,player->core,params);
  player->view.renderer=renderer;
  if(renderer)p_mpv_render_context_set_update_callback(renderer,updateFrame,player->view);
 });
 if(viewError<0){snprintf(error,size,"Native view/render context unavailable: %d",viewError);cine_native_destroy(player);return viewError;}
 player->clockQueue=dispatch_queue_create("CineInsight.native.subtitle-clock",DISPATCH_QUEUE_SERIAL);
 player->clock=dispatch_source_create(DISPATCH_SOURCE_TYPE_TIMER,0,0,player->clockQueue);
 dispatch_source_set_timer(player->clock,dispatch_time(DISPATCH_TIME_NOW,0),NSEC_PER_SEC/30,NSEC_PER_MSEC*2);
 dispatch_source_set_event_handler(player->clock,^{ @autoreleasepool {
  double position=0;int seeking=0;p_mpv_get_property(player->core,"seeking",MPV_FORMAT_FLAG,&seeking);if(seeking||p_mpv_get_property(player->core,"time-pos",MPV_FORMAT_DOUBLE,&position)<0){[player->caption queueText:@""];return;}
  int64_t time=(int64_t)llround(position*1000)-player->offset;
  NSArray *cues=player->cues;NSUInteger low=0,high=cues.count;
  while(low<high){NSUInteger mid=(low+high)/2;if([cues[mid][@"start_ms"]longLongValue]<=time)low=mid+1;else high=mid;}
  NSMutableArray *active=[NSMutableArray array];
  for(NSInteger i=(NSInteger)low-1;i>=0&&[player->cueEnds[i]longLongValue]>time;i--){NSDictionary *cue=cues[i];if(time<[cue[@"end_ms"]longLongValue])[active insertObject:cue[@"text"] atIndex:0];}
  [player->caption queueText:[active componentsJoinedByString:@"\n"]];
 } });dispatch_resume(player->clock);
 *out=player;return 0;
}
void cine_native_probe_diagnostics(CineNativePlayer *player) { p_mpv_request_log_messages(player->core,"debug"); }
int cine_native_load_at(CineNativePlayer *player,const char *path,double start) {
 player->eof=0;player->error=0;char options[80];snprintf(options,sizeof(options),"start=%.3f",start);
 const char *args[]={"loadfile",path,"replace","-1",options,NULL};return p_mpv_command(player->core,args);
}
int cine_native_load(CineNativePlayer *player,const char *path) {
 player->eof=0;player->error=0;
 const char *args[]={"loadfile",path,"replace",NULL};return p_mpv_command(player->core,args);
}
int cine_native_command(CineNativePlayer *player,const char *name,double value) {
 if(!strcmp(name,"play")||!strcmp(name,"pause")){int paused=!strcmp(name,"pause");return p_mpv_set_property(player->core,"pause",MPV_FORMAT_FLAG,&paused);}
 if(!strcmp(name,"seek")){const char *args[]={"seek",NULL,"absolute+exact",NULL};char time[64];snprintf(time,sizeof(time),"%.6f",value);args[1]=time;player->eof=0;const char **argv=args;__block int result;dispatch_sync(player->clockQueue,^{result=p_mpv_command(player->core,argv);[player->caption queueText:@""];});return result;}
 if(!strcmp(name,"volume")){value*=100;return p_mpv_set_property(player->core,"volume",MPV_FORMAT_DOUBLE,&value);}
 if(!strcmp(name,"muted")){int flag=value!=0;return p_mpv_set_property(player->core,"mute",MPV_FORMAT_FLAG,&flag);}
 if(!strcmp(name,"rate"))return p_mpv_set_property(player->core,"speed",MPV_FORMAT_DOUBLE,&value);
 return -1;
}
int cine_native_viewport(CineNativePlayer *player,double x,double y,double width,double height,int visible) {
 __block int result=0;
 onMain(^{
  WKWebView *web=player->webview;
if(!isfinite(x)||!isfinite(y)||!isfinite(width)||!isfinite(height)||width<0||height<0||width>MAX(1,web.bounds.size.width)*2||height>MAX(1,web.bounds.size.height)*2){result=-1;return;}
  if(player->container.superview!=web.superview){[player->container removeFromSuperview];[web.superview addSubview:player->container positioned:NSWindowAbove relativeTo:web];}
  NSRect requested=NSMakeRect(x,web.isFlipped?y:web.bounds.size.height-y-height,width,height);
  NSRect full=[web convertRect:requested toView:web.superview];
  NSRect clipped=[web convertRect:NSIntersectionRect(requested,web.bounds) toView:web.superview];
  player->container.frame=clipped;player->container.hidden=!visible||NSIsEmptyRect(clipped);
  player->view.frame=NSMakeRect(full.origin.x-clipped.origin.x,full.origin.y-clipped.origin.y,full.size.width,full.size.height);player->view.hidden=player->container.hidden;
  player->caption.frame=player->view.frame;[player->caption setNeedsDisplay:YES];
  CGLLockContext(player->view.openGLContext.CGLContextObj);[player->view.openGLContext update];CGLUnlockContext(player->view.openGLContext.CGLContextObj);[player->view updateGeometry];
 });return result;
}
int cine_native_subtitles(CineNativePlayer *player,const char *json) { @autoreleasepool {
 NSData *data=[[NSString stringWithUTF8String:json]dataUsingEncoding:NSUTF8StringEncoding];
 NSDictionary *document=[NSJSONSerialization JSONObjectWithData:data options:0 error:nil];
 if(![document isKindOfClass:NSDictionary.class])return -1;
 NSArray *cues=[document[@"cues"]sortedArrayUsingComparator:^NSComparisonResult(NSDictionary *a,NSDictionary *b){return [a[@"start_ms"]compare:b[@"start_ms"]];}];
 NSMutableArray *ends=[NSMutableArray array];int64_t end=0;for(NSDictionary *cue in cues){end=MAX(end,[cue[@"end_ms"]longLongValue]);[ends addObject:@(end)];}
 dispatch_sync(player->clockQueue,^{[player->cues release];player->cues=[cues copy];[player->cueEnds release];player->cueEnds=[ends copy];player->offset=[document[@"offset_ms"]longLongValue];});
 onMain(^{player->caption.preferences=document[@"preferences"];player->caption.text=@"";[player->caption queueText:@""];[player->caption setNeedsDisplay:YES];});return 0;
} }
void cine_native_probe_click(CineNativePlayer *player,double x,double y) {
 onMain(^{WKWebView *web=player->webview;[web.window makeKeyAndOrderFront:nil];[NSApp activateIgnoringOtherApps:YES];
 NSPoint point=[web convertPoint:NSMakePoint(x,web.isFlipped?y:web.bounds.size.height-y) toView:nil];
 for(int i=0;i<2;i++){NSEvent *event=[NSEvent mouseEventWithType:i?NSEventTypeLeftMouseUp:NSEventTypeLeftMouseDown location:point modifierFlags:0 timestamp:NSProcessInfo.processInfo.systemUptime windowNumber:web.window.windowNumber context:nil eventNumber:i+1 clickCount:1 pressure:i?0:1];[web.window sendEvent:event];}
 });
}
int cine_native_probe_pixels(CineNativePlayer *player) { __block int result=0;onMain(^{player->view.probePixels=YES;[player->view setNeedsDisplay:YES];[player->view displayIfNeeded];[player->view waitForRender];result=player->view.pixelRange;});return result; }
char *cine_native_snapshot(CineNativePlayer *player) { @autoreleasepool {
 for(int i=0;i<64;i++){
  mpv_event *event=p_mpv_wait_event(player->core,0);
  if(!event||event->event_id==MPV_EVENT_NONE)break;
  if(event->event_id==MPV_EVENT_LOG_MESSAGE){mpv_event_log_message *log=event->data;fprintf(stderr,"[native-probe %s] %s",log->prefix,log->text);}
  if(event->event_id==MPV_EVENT_END_FILE){mpv_event_end_file *end=event->data;if(end&&end->reason==MPV_END_FILE_REASON_ERROR)player->error=end->error;}
 }
 double position=0,duration=0,volume=0,rate=1;int paused=1,eof=0,muted=1,seeking=0;int64_t width=0,height=0;
 p_mpv_get_property(player->core,"time-pos",MPV_FORMAT_DOUBLE,&position);p_mpv_get_property(player->core,"duration",MPV_FORMAT_DOUBLE,&duration);
 p_mpv_get_property(player->core,"pause",MPV_FORMAT_FLAG,&paused);p_mpv_get_property(player->core,"eof-reached",MPV_FORMAT_FLAG,&eof);
 p_mpv_get_property(player->core,"mute",MPV_FORMAT_FLAG,&muted);p_mpv_get_property(player->core,"volume",MPV_FORMAT_DOUBLE,&volume);p_mpv_get_property(player->core,"speed",MPV_FORMAT_DOUBLE,&rate);
 p_mpv_get_property(player->core,"seeking",MPV_FORMAT_FLAG,&seeking);
 p_mpv_get_property(player->core,"width",MPV_FORMAT_INT64,&width);p_mpv_get_property(player->core,"height",MPV_FORMAT_INT64,&height);
 __block BOOL front=NO,windowVisible=NO;__block NSString *captionText=nil;__block NSUInteger frames=0;__block BOOL hidden=YES;__block NSRect css=NSZeroRect;
 onMain(^{front=player->view.window==NSApp.keyWindow;windowVisible=player->view.window.visible&&!player->view.window.miniaturized;captionText=[player->caption.text copy];frames=player->view.frames;hidden=player->view.hidden;css=[player->webview convertRect:player->view.bounds fromView:player->view];if(!player->webview.isFlipped)css.origin.y=player->webview.bounds.size.height-css.origin.y-css.size.height;});
 NSDictionary *state=@{@"front_window":@(front),@"window_visible":@(windowVisible),@"subtitle_text":captionText?:@"",@"position":@(position),@"duration":@(duration),@"seeking":[NSNumber numberWithBool:seeking!=0],@"paused":[NSNumber numberWithBool:paused!=0],@"ended":[NSNumber numberWithBool:eof!=0],@"muted":[NSNumber numberWithBool:muted!=0],@"volume":@(volume/100),@"rate":@(rate),@"width":@(width),@"height":@(height),@"decoded_frames":@(player->view.decodedFrames),@"frames":@(frames),@"hidden":@(hidden),@"error":@(player->error?:player->view.renderError),@"x":@(css.origin.x),@"y":@(css.origin.y),@"render_width":@(css.size.width),@"render_height":@(css.size.height)};
 NSData *json=[NSJSONSerialization dataWithJSONObject:state options:0 error:nil];[captionText release];return strndup(json.bytes,json.length);
} }
void cine_native_destroy(CineNativePlayer *player) {
 if(!player)return;
 if(player->clock){dispatch_source_cancel(player->clock);dispatch_sync(player->clockQueue,^{});dispatch_release(player->clock);dispatch_release(player->clockQueue);}
 // Barrier drains already queued caption updates before freeing the C owner.
 onMain(^{
  if(player->view){if(player->view.renderer)p_mpv_render_context_set_update_callback(player->view.renderer,NULL,NULL);[player->view stopRendering];[player->view.openGLContext makeCurrentContext];if(player->view.renderer){p_mpv_render_context_free(player->view.renderer);player->view.renderer=NULL;}[player->view removeFromSuperview];[player->view release];}
  [player->caption removeFromSuperview];[player->caption release];player->caption=nil;[player->cues release];[player->cueEnds release];
  [player->container removeFromSuperview];[player->container release];
  [player->webview release];
 });
 if(player->core)p_mpv_terminate_destroy(player->core);free(player);
}
void cine_native_free(void *value){free(value);}
