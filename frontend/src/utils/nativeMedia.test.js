import {afterEach,beforeEach,expect,it,vi} from 'vitest';
import {flushPromises,mount} from '@vue/test-utils';
const api=vi.hoisted(()=>Object.fromEntries(['AcquireNativePlaybackOwner','ReleaseNativePlaybackOwner','OpenNativePlayback','CommandNativePlayback','SetNativePlaybackViewport','GetNativePlaybackSnapshot','CloseNativePlayback','ConfigureNativePlaybackSubtitles','GetNativePlaybackCapability','NotifyPlaybackActivity','GetPlaybackProxy','GetPlaybackProxyStatus','GetPlaybackSubtitle','RecordViewEvent','CancelPreviewProxy','GetPlaybackSession','RequestPreviewProxy'].map(name=>[name,vi.fn()])));
vi.mock('../../wailsjs/go/main/App',()=>api);
import './nativeMedia.js';
import InlineVideoPlayer from '../components/player/InlineVideoPlayer.vue';
let listeners,state;
const owner={owner_id:'owner',owner_token:'secret'};
const snapshot=(extra={})=>({session_id:'session',seq:1,video_id:1,source_version:'source',state:'paused',position_ms:0,duration_ms:10000,video_width:640,video_height:360,paused:true,muted:true,volume:1,rate:1,seekable:true,...extra});
beforeEach(()=>{
 vi.clearAllMocks();listeners=new Map();window.runtime={EventsOn:(name,callback)=>{listeners.set(name,callback);return()=>listeners.delete(name);}};
 globalThis.ResizeObserver=class{observe(){}disconnect(){}};
 api.GetPlaybackProxy.mockResolvedValue(null);api.GetPlaybackProxyStatus.mockResolvedValue({running:false,queued:0,results:[]});api.GetPlaybackSubtitle.mockResolvedValue({status:'absent',cues:[]});
 api.GetNativePlaybackCapability.mockResolvedValue({available:true,window_ready:true,window_epoch:7});api.AcquireNativePlaybackOwner.mockResolvedValue(owner);api.OpenNativePlayback.mockResolvedValue(snapshot({seq:0,state:'opening'}));state=snapshot();api.GetNativePlaybackSnapshot.mockImplementation(async()=>state);api.ReleaseNativePlaybackOwner.mockResolvedValue();api.SetNativePlaybackViewport.mockResolvedValue({applied_revision:1});api.CommandNativePlayback.mockResolvedValue({accepted_seq:1});api.ConfigureNativePlaybackSubtitles.mockResolvedValue({status:'ready',applied_revision:1});
});
afterEach(async()=>{document.querySelectorAll('cine-native-media').forEach(node=>node.remove());await flushPromises();delete window.runtime;});
async function media(){const element=document.createElement('cine-native-media');element.playbackSource={video_id:1,source_version:'source'};element.getBoundingClientRect=()=>new DOMRect(0,0,640,360);element.getClientRects=()=>[element.getBoundingClientRect()];document.body.appendChild(element);await flushPromises();return element;}
it('opens source identity without paths and provides honest native media ranges',async()=>{
 const element=await media();expect(api.AcquireNativePlaybackOwner).toHaveBeenCalledWith(expect.objectContaining({window_epoch:7}));expect(api.OpenNativePlayback).toHaveBeenCalledWith(expect.objectContaining({...owner,video_id:1,source_version:'source',paused:true,muted:true}));expect(api.OpenNativePlayback.mock.calls[0][0]).not.toHaveProperty('path');expect(element.videoWidth).toBe(640);expect(element.buffered.length).toBe(0);expect(element.seekable.end(0)).toBe(10);expect(element.querySelector('track')).toBeNull();
});
it('discards stale and foreign events while reflecting actual playback facts',async()=>{
 const element=await media();const apply=listeners.get('native-playback-state');const ended=vi.fn();element.addEventListener('ended',ended);
 apply(snapshot({session_id:'other',seq:99,position_ms:9000}));apply(snapshot({seq:1,position_ms:8000}));expect(element.currentTime).toBe(0);
 apply(snapshot({seq:2,state:'playing',paused:false,position_ms:2000}));expect(element.currentTime).toBe(2);expect(element.paused).toBe(false);
 apply(snapshot({seq:3,state:'ended',position_ms:10000}));apply(snapshot({seq:4,state:'ended',position_ms:10000}));expect(ended).toHaveBeenCalledTimes(1);
});
it('releases an owner when its Open result arrives after removal',async()=>{
 let finish;api.OpenNativePlayback.mockImplementation(()=>new Promise(resolve=>{finish=resolve;}));const element=await media();element.remove();await flushPromises();expect(api.ReleaseNativePlaybackOwner).toHaveBeenCalledWith(owner);finish(snapshot({seq:0}));await flushPromises();expect(element.readyState).toBe(0);
});
it('sends only the controlled subtitle fingerprint and display configuration',async()=>{
 const element=await media();await element.configureSubtitles({subtitle_fingerprint:'opaque-fingerprint',enabled:true,offset_ms:-100,preferences:{size:'large',background:'solid',position:'top'}});
 expect(api.ConfigureNativePlaybackSubtitles).toHaveBeenCalledWith(expect.objectContaining({...owner,session_id:'session',subtitle_fingerprint:'opaque-fingerprint',offset_ms:-100}));expect(api.ConfigureNativePlaybackSubtitles.mock.calls[0][0]).not.toHaveProperty('cues');expect(element.querySelector('track')).toBeNull();
});

it('marks a changed source before any progress-producing callback',async()=>{
 const element=await media(),apply=listeners.get('native-playback-state');apply(snapshot({seq:2,state:'playing',paused:false,position_ms:1000}));
 const ordinary=vi.fn(),error=vi.fn(()=>expect(element.sourceAvailable).toBe(false));element.addEventListener('pause',ordinary);element.addEventListener('timeupdate',ordinary);element.addEventListener('error',error);
 apply(snapshot({seq:3,state:'closed',paused:true,position_ms:9000,error_code:'source_changed',error_message:'原片已变化'}));expect(ordinary).not.toHaveBeenCalled();expect(error).toHaveBeenCalledOnce();
});
it.each(['seek','volume'])('does not project a late %s failure into a replacement source',async kind=>{
 const element=await media();let reject;api.CommandNativePlayback.mockImplementationOnce(()=>new Promise((_,fail)=>{reject=fail;}));
 if(kind==='seek')element.currentTime=2;else element.volume=.5;await flushPromises();
 state=snapshot({session_id:'replacement',video_id:2,source_version:'second'});api.OpenNativePlayback.mockResolvedValue({...state,seq:0});element.playbackSource={video_id:2,source_version:'second'};await flushPromises();
 const error=vi.fn();element.addEventListener('error',error);reject(new Error('late old command failure'));await flushPromises();expect(error).not.toHaveBeenCalled();expect(element.error).toBeNull();expect(element.currentSrc).toContain('native:2:second');
});
it('cancels play waiting for handoff when its source is replaced',async()=>{
 const element=await media();let resume;element.beforePlay=()=>new Promise(resolve=>{resume=resolve;});const pending=element.play().catch(error=>error);await flushPromises();
 state=snapshot({session_id:'replacement',video_id:2,source_version:'second'});api.OpenNativePlayback.mockResolvedValue({...state,seq:0});element.playbackSource={video_id:2,source_version:'second'};await flushPromises();resume();const result=await pending;expect(result.name).toBe('AbortError');expect(api.CommandNativePlayback.mock.calls.filter(([request])=>request.kind==='play')).toHaveLength(0);
});

it('does not write host progress or view facts after source invalidation',async()=>{
 const wrapper=mount(InlineVideoPlayer,{attachTo:document.body,props:{videoId:1,session:{video_id:1,mode:'native',source_version:'source',native_source:{video_id:1,source_version:'source'}},durationSeconds:10}});await flushPromises();
 const apply=listeners.get('native-playback-state');apply(snapshot({seq:2,state:'playing',paused:false,position_ms:1000}));await flushPromises();const before=wrapper.emitted('watch-progress')?.length||0;
 apply(snapshot({seq:3,state:'closed',paused:true,position_ms:9900,error_code:'source_changed',error_message:'原片已变化'}));await flushPromises();expect(wrapper.emitted('watch-progress')?.length||0).toBe(before);expect(api.RecordViewEvent).not.toHaveBeenCalled();wrapper.unmount();
});

it('does not retire a replacement session when an old Close reply arrives',async()=>{
 const element=await media();let finish;api.CloseNativePlayback.mockImplementationOnce(()=>new Promise(resolve=>{finish=resolve;}));const pending=element.retire().catch(error=>error);await flushPromises();
 state=snapshot({session_id:'replacement',video_id:2,source_version:'second'});api.OpenNativePlayback.mockResolvedValue({...state,seq:0});element.playbackSource={video_id:2,source_version:'second'};await flushPromises();const releases=api.ReleaseNativePlaybackOwner.mock.calls.length;
 finish();const error=await pending;expect(error.name).toBe('AbortError');expect(api.ReleaseNativePlaybackOwner).toHaveBeenCalledTimes(releases);expect(element._session.session_id).toBe('replacement');expect(element._closed).toBe(false);
});
it('uses the saved audio preference on each native source open',async()=>{
 let saved=JSON.stringify({volume:.37,muted:false});
 vi.stubGlobal('localStorage',{getItem:()=>saved,setItem:(_key,value)=>{saved=value;}});
 try {
  const element=await media();
  expect(api.OpenNativePlayback).toHaveBeenLastCalledWith(expect.objectContaining({volume:.37,muted:false}));
  saved=JSON.stringify({volume:.61,muted:false});
  element.playbackSource={video_id:2,source_version:'next'};await flushPromises();
  expect(api.OpenNativePlayback).toHaveBeenLastCalledWith(expect.objectContaining({video_id:2,volume:.61,muted:false}));
 } finally {vi.unstubAllGlobals();}
});
