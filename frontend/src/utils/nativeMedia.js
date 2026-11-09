import { readPlaybackAudio } from './playbackAudio.js';
import {
 AcquireNativePlaybackOwner, ReleaseNativePlaybackOwner, OpenNativePlayback,
 CommandNativePlayback, SetNativePlaybackViewport, GetNativePlaybackSnapshot,
 CloseNativePlayback, ConfigureNativePlaybackSubtitles, GetNativePlaybackCapability
} from '../../wailsjs/go/main/App';
import { prepareActivePlayer, releaseActivePlayer, retireActivePlayer } from './activePlayer.js';
const api={
 capability:()=>GetNativePlaybackCapability(),acquire:r=>AcquireNativePlaybackOwner(r),release:r=>ReleaseNativePlaybackOwner(r),open:r=>OpenNativePlayback(r),
 command:r=>CommandNativePlayback(r),viewport:r=>SetNativePlaybackViewport(r),snapshot:r=>GetNativePlaybackSnapshot(r),close:r=>CloseNativePlayback(r),subtitles:r=>ConfigureNativePlaybackSubtitles(r)
};
const abort=()=>new DOMException('播放已取消','AbortError');
const tracks=()=>{const target=new EventTarget();return Object.assign([],{addEventListener:target.addEventListener.bind(target),removeEventListener:target.removeEventListener.bind(target),getTrackById:()=>null});};
const range=(duration,enabled)=>({length:enabled?1:0,start(index){if(index!==0||!enabled)throw new DOMException('时间范围无效','IndexSizeError');return 0;},end(index){if(index!==0||!enabled)throw new DOMException('时间范围无效','IndexSizeError');return duration;}});
export class NativeMediaElement extends HTMLElement {
 constructor(){super();this.nativePlayback=true;this._state={seq:-1,state:'opening',position_ms:0,duration_ms:0,paused:true,muted:true,volume:1,rate:1};this._generation=0;this._commands=Promise.resolve();this._playVersion=0;this._waiters=[];this._tracks=tracks();this._seq=0;this._revision=0;this._subtitleRevision=0;this._closed=true;}
 get playbackSource(){return this._source;}set playbackSource(value){const key=JSON.stringify(value?{video_id:value.video_id,source_version:value.source_version,queue_token:value.queue_token}:null);if(key===this._sourceKey){this._source=value;return;}this._sourceKey=key;this._source=value;if(this.isConnected){if(value)this._start();else this.close('replaced').catch(()=>{});}}
 connectedCallback(){if(!this.hasAttribute('tabindex'))this.tabIndex=0;this.style.display='block';this.style.aspectRatio='16 / 9';this._geometry=()=>this.scheduleViewport();window.addEventListener('resize',this._geometry);window.addEventListener('scroll',this._geometry,true);document.addEventListener('visibilitychange',this._geometry);this._resize=new ResizeObserver(this._geometry);this._resize.observe(this);this._mutations=new MutationObserver(this._geometry);this._mutations.observe(document.body,{subtree:true,attributes:true,childList:true,attributeFilter:['style','class','open','hidden']});if(this._source)this._start();}
 disconnectedCallback(){this._resize?.disconnect();this._mutations?.disconnect();window.removeEventListener('resize',this._geometry);window.removeEventListener('scroll',this._geometry,true);document.removeEventListener('visibilitychange',this._geometry);cancelAnimationFrame(this._raf);clearTimeout(this._viewportTimer);this.close('navigate').catch(()=>{});}
 async _start(){
 const audio=readPlaybackAudio();this._initialMuted=audio.muted;this._initialVolume=audio.volume;
 const previous=this.close('replaced');const generation=++this._generation;this._closed=false;this._retiring=false;this._error=null;this._state={seq:-1,state:'opening',position_ms:0,duration_ms:0,paused:true,muted:true,volume:1,rate:1};this._seq=0;this._revision=0;this._subtitleRevision=0;this._commands=Promise.resolve();this._lastViewport='';
 this._opened=(async()=>{
  await previous;if(!this._current(generation))throw abort();
  let capability=await api.capability();
  if(capability.available&&!capability.window_ready){await this._windowReady(generation);capability=await api.capability();}
  if(!capability.available||!capability.window_ready)throw new Error(capability.reason_message||'本机播放窗口尚未就绪');
  const owner=await api.acquire({component_nonce:crypto.randomUUID(),window_epoch:capability.window_epoch});
  if(!this._current(generation)){await api.release(owner);throw abort();}this._owner=owner;
  this._off=window.runtime?.EventsOn?.('native-playback-state',state=>{if(this._current(generation)&&state.session_id===this._session?.session_id)this._apply(state);});
  const session=await api.open({...owner,request_id:crypto.randomUUID(),video_id:this._source.video_id,source_version:this._source.source_version,queue_token:this._source.queue_token||'',start_ms:this._source.start_ms||0,paused:true,muted:this._initialMuted,volume:this._initialVolume});
  if(!this._current(generation)){await api.release(owner);throw abort();}this._session=session;
  this._apply(await api.snapshot(this._ref()));this.scheduleViewport();return session;
 })();this._opened.catch(error=>{if(this._current(generation))this._fail(error);});
 }
 _current(generation){return this.isConnected&&!this._closed&&generation===this._generation;}
 _ref(){return {...this._owner,session_id:this._session?.session_id};}
 _windowReady(generation){return new Promise((resolve,reject)=>{
 let timer,off,settled=false;const cancelReady=()=>finish(abort());
 const finish=error=>{if(settled)return;settled=true;clearTimeout(timer);off?.();if(this._cancelReady===cancelReady)this._cancelReady=null;if(!this._current(generation))reject(abort());else if(error)reject(error);else resolve();};
 this._cancelReady=cancelReady;timer=setTimeout(()=>finish(new Error('本机播放窗口未在限定时间内就绪')),10000);off=window.runtime?.EventsOn?.('native-playback-ready',()=>finish());if(settled)off?.();
 api.capability().then(capability=>{if(capability.available&&capability.window_ready)finish();},finish);
 });}

 _event(name){this.dispatchEvent(new Event(name));}
 _apply(state){
 if(!this._session||state.session_id!==this._session.session_id||state.seq<=this._state.seq)return;
 const old=this._state,wasReady=this.readyState;this._state=state;
 if(state.state==='error'||(state.state==='closed'&&!this._retiring)){this._fail(new Error(state.error_message||'播放会话已结束'),state.error_code);return;}
 if(state.video_width&&state.video_height)this.style.aspectRatio=`${state.video_width} / ${state.video_height}`;
 if(!wasReady&&this.readyState){this._event('loadedmetadata');this._event('durationchange');this._event('canplay');}
 if(old.state!=='seeking'&&this.seeking)this._event('seeking');if(old.state==='seeking'&&!this.seeking)this._event('seeked');
 if(old.paused!==state.paused){this._event(state.paused?'pause':'play');if(state.paused)releaseActivePlayer(this.activePlayerOwner||this);}
 if(state.state==='playing'&&old.state!=='playing')this._event('playing');
 if(old.volume!==state.volume||old.muted!==state.muted)this._event('volumechange');if(old.rate!==state.rate)this._event('ratechange');
 this._event('timeupdate');
 if(state.state==='ended'&&old.state!=='ended'){releaseActivePlayer(this.activePlayerOwner||this);this._event('ended');}

 this._settle();this.scheduleViewport();
 }
 _reportError(error,code,generation){if(this._current(generation)&&error?.name!=='AbortError')this._fail(error,code);}
 _fail(error,code='native_decode_failed'){if(this._closed||error?.name==='AbortError')return;this._error={code:3,message:String(error?.message||error),nativeCode:code};this._event('error');this._settle();}
 _wait(test,kind='state'){if(this._error)return Promise.reject(new Error(this._error.message));if(test())return Promise.resolve();return new Promise((resolve,reject)=>this._waiters.push({test,resolve,reject,kind}));}
 _settle(){this._waiters=this._waiters.filter(waiter=>{if(this._error||this._closed){waiter.reject(this._error?new Error(this._error.message):abort());return false;}if(waiter.test()){waiter.resolve();return false;}return true;});}
 _command(kind,value,intent){const generation=this._generation;const operation=this._commands.catch(()=>{}).then(async()=>{await this._opened;if(!this._current(generation)||(kind==='play'&&intent!==this._playVersion))throw abort();const request={...this._ref(),seq:++this._seq,kind};if(value!==undefined)request.value=value;await api.command(request);if(!this._current(generation))throw abort();this._apply(await api.snapshot(this._ref()));});this._commands=operation;return operation;}
 async play(){
 const generation=this._generation,intent=++this._playVersion;const verify=()=>{if(!this._current(generation)||intent!==this._playVersion)throw abort();};
 await this._opened;verify();await this._wait(()=>this.readyState>0,'play');verify();await this._commands;verify();if(!this.getClientRects().length||document.visibilityState==='hidden')throw abort();
 if(this.beforePlay)await this.beforePlay();else await prepareActivePlayer(this.activePlayerOwner||this);verify();await this._command('play',undefined,intent);verify();await this._wait(()=>this._state.state==='playing','play');verify();
 }

 _cancelPlay(){++this._playVersion;this._waiters=this._waiters.filter(waiter=>{if(waiter.kind==='play'){waiter.reject(abort());return false;}return true;});}
 async pause(){const generation=this._generation;this._cancelPlay();if(this._closed)return this._release;if(this._error)return this.close('user');await this._opened;if(!this._current(generation))throw abort();await this._command('pause');await this._wait(()=>this.paused);}

 load(){if(!this._source)return this.close('replaced');return this._start();}
 async retire(){const generation=this._generation;await this.pause();if(!this._current(generation))throw abort();this._retiring=true;if(this._session)await api.close({...this._ref(),reason:'navigate'});if(!this._current(generation))throw abort();return this.close('navigate');}
 close(reason='user'){
 if(this._closed)return this._release||Promise.resolve();this._cancelPlay();this._closed=true;++this._generation;this._cancelReady?.();this._off?.();this._off=null;this._settle();
 const owner=this._owner;this._owner=null;this._session=null;
 this._release=owner?retireActivePlayer(this.activePlayerOwner||this,()=>api.release(owner)):Promise.resolve();return this._release;
 }
 async configureSubtitles(configuration){const generation=this._generation;await this._opened;if(!this._current(generation))throw abort();return api.subtitles({...this._ref(),revision:++this._subtitleRevision,...configuration});}
 scheduleViewport(){if(this._closed||!this._session||this._raf||this._viewportTimer)return;const delay=Math.max(0,34-(performance.now()-(this._viewportAt||0)));this._viewportTimer=setTimeout(()=>{this._viewportTimer=null;this._raf=requestAnimationFrame(()=>{this._raf=null;this._syncViewport();});},delay);}
 _syncViewport(){
 if(this._closed||!this._session)return;this._viewportAt=performance.now();const original=this.getBoundingClientRect();const rect={x:original.x,y:original.y,left:original.left,top:original.top,right:original.right,bottom:original.bottom,width:original.width,height:original.height};
 const controls=this.parentElement?.querySelector('[data-test="playback-controls"]');if(controls){const bar=controls.getBoundingClientRect();if(bar.height>0&&bar.top>rect.top&&bar.top<rect.bottom&&bar.left<rect.right&&bar.right>rect.left){rect.bottom=bar.top;rect.height=rect.bottom-rect.top;}}
 const visible=document.visibilityState!=='hidden'&&rect.width>0&&rect.height>0&&rect.right>0&&rect.bottom>0&&rect.left<innerWidth&&rect.top<innerHeight&&this.getClientRects().length>0;
 const intersects=other=>{const r=other.getBoundingClientRect();return r.width>0&&r.height>0&&r.left<rect.right&&r.right>rect.left&&r.top<rect.bottom&&r.bottom>rect.top&&getComputedStyle(other).visibility!=='hidden';};
 const occluded=visible&&[...document.querySelectorAll('dialog[open],[role="dialog"],[role="menu"],[data-test="playback-ending"],[data-test="playback-transcript"]')].some(node=>!node.contains(this)&&intersects(node));
 if(!visible)this._cancelPlay();
 const box=visible?{x:rect.x,y:rect.y,width:rect.width,height:rect.height,visible,occluded}:{x:0,y:0,width:0,height:0,visible:false,occluded:false};const key=JSON.stringify(box);if(key===this._lastViewport)return;this._lastViewport=key;const generation=this._generation;
 api.viewport({...this._ref(),revision:++this._revision,...box}).catch(error=>{if(this._current(generation))this._fail(error);});
 }
 get currentTime(){return this._state.position_ms/1000;}set currentTime(value){if(!Number.isFinite(value)||value<0)throw new TypeError('播放位置无效');const generation=this._generation;this._command('seek',Math.round(value*1000)).catch(error=>this._reportError(error,'native_seek_failed',generation));}
 get duration(){return this._state.duration_ms>0?this._state.duration_ms/1000:NaN;}
 get paused(){return this._state.paused;}get ended(){return this._state.state==='ended';}get seeking(){return this._state.state==='seeking';}
 get readyState(){return this._state.video_width>0&&!['opening','error','closed'].includes(this._state.state)?4:0;}
 get videoWidth(){return this._state.video_width||0;}get videoHeight(){return this._state.video_height||0;}
 get volume(){return this._state.volume;}set volume(value){if(!this._opened||this._closed){this._initialVolume=value;return;}const generation=this._generation;this._command('set_volume',value).catch(error=>this._reportError(error,'native_decode_failed',generation));}
 get muted(){return this._state.muted;}set muted(value){if(!this._opened||this._closed){this._initialMuted=Boolean(value);return;}const generation=this._generation;this._command('set_muted',Boolean(value)).catch(error=>this._reportError(error,'native_decode_failed',generation));}
 get playbackRate(){return this._state.rate;}set playbackRate(value){if(!this._opened||this._closed)return;const generation=this._generation;this._command('set_rate',value).catch(error=>this._reportError(error,'native_decode_failed',generation));}
 get buffered(){return range(0,false);}get seekable(){return range(this.duration,this._state.seekable&&this.duration>0);}
 get textTracks(){return this._tracks;}get audioTracks(){return this._tracks;}get videoTracks(){return this._tracks;}
 get error(){return this._error;}get sourceAvailable(){return !['source_changed','source_unavailable','native_owner_revoked'].includes(this._error?.nativeCode);}
 get currentSrc(){return this._source?`native:${this._source.video_id}:${this._source.source_version}`:'';}
}
if(!customElements.get('cine-native-media'))customElements.define('cine-native-media',NativeMediaElement);
