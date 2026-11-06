import {createApp,h,nextTick} from 'vue';
import InlineVideoPlayer from '../../src/components/player/InlineVideoPlayer.vue';
import QueueInlinePlayer from '../../src/components/QueueInlinePlayer.vue';
import PlaybackEndScreen from '../../src/components/player/PlaybackEndScreen.vue';
import {selectPlaybackEnding} from '../../src/utils/playbackEndingNavigation.js';
const wait=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const assert=(condition,label)=>{if(!condition)throw new Error(label);};
async function until(test,label){const end=Date.now()+12000;while(Date.now()<end){await nextTick();if(test())return;await wait(20);}throw new Error(`${label}: ${JSON.stringify({body:document.body.innerText.slice(-600),media:document.querySelector('cine-native-media')?._state,error:document.querySelector('cine-native-media')?.error,viewport:document.querySelector('cine-native-media')?._lastViewport,player:window.ownerFixtureApp?.$refs.player?{ending:window.ownerFixtureApp.$refs.player.ending,endingSeek:window.ownerFixtureApp.$refs.player.endingSeek,hasPlaybackStarted:window.ownerFixtureApp.$refs.player.hasPlaybackStarted,viewSession:window.ownerFixtureApp.$refs.player.viewSession}:null})}`);}
export async function runOwnerFixture(){
 const bridge=window.go.main.ProbeApp;const facts=[];
 window.go.main.App=new Proxy(bridge,{get:(target,key)=>{
  if(target[key])return target[key];
  if(key==='GetPlaybackProxy')return async()=>null;
  if(key==='GetPlaybackProxyStatus')return async()=>({running:false,queued:0,results:[]});
  if(key==='SuggestTonightVideos')return async()=>[2,3,4].map(id=>({video:{id,name:`测试影片 ${id}`,duration:4.05}}));
  if(key==='RecordViewEvent')return async(...args)=>{facts.push(args);};
  return async()=>[];
 }});
 let app,navigations=0;
 const openEndedVideo=(id,options)=>selectPlaybackEnding(id,{...options,resolveSource:value=>bridge.ResolveVisibleVideoForPlayback(value),navigate:async value=>{navigations++;app.session=await bridge.GetPlaybackSession(value);app.id=value;app.autoplay=true;return true;}});
 app=createApp({provide:()=>({openEndedVideo}),data:()=>({id:1,session:null,autoplay:false,queue:false,queueSnapshot:null}),render(){if(this.queue)return h('main',{class:'surface-cinema',style:'width:700px;margin:24px auto'},[this.queueSnapshot?.session?h(QueueInlinePlayer,{ref:'queuePlayer',key:this.queueSnapshot.session.token,session:this.queueSnapshot.session}):null,this.queueSnapshot?.ending?h('p',{'data-test':'native-queue-ending'},'队列已结束'):null]);return h('main',{class:'surface-cinema',style:'width:800px;max-width:100%;margin:24px auto'},[
 h(InlineVideoPlayer,{ref:'player',key:this.id,videoId:this.id,session:this.session,durationSeconds:4.05,autoplay:this.autoplay,onPreviewSessionStale:async()=>{this.session=await bridge.GetPlaybackSession(this.id);}}, {ending:({ending,replay,retire})=>h(PlaybackEndScreen,{ending,replay,retire})})
 ]);}}).mount('#app');
 window.ownerFixtureApp=app;app.session=await bridge.GetPlaybackSession(1);assert(app.session.mode==='native','formal AVI chooses native');
 await until(()=>document.querySelector('cine-native-media')?.readyState===4,'real native media metadata and frame');
 let media=document.querySelector('cine-native-media');assert(media.videoWidth===640&&media.videoHeight===360,'real decoder dimensions');assert(!document.querySelector('video'),'no browser decoder or proxy');
 await app.$refs.player.play();await until(()=>!media.paused&&media.currentTime>.15,'native element plays through real controls');
 await media.pause();const paused=media.currentTime;await wait(150);assert(Math.abs(media.currentTime-paused)<.15,'pause preserves actual native position');
 media.currentTime=1;await media.play();await until(()=>media.currentTime>1&&!media.seeking,'native seek completes before play');
 document.querySelector('[data-test="playback-volume-toggle"]').click();await nextTick();const before=media.currentTime;await until(()=>media.currentTime>before+.15,'popover does not stall playback');document.querySelector('[data-test="playback-volume-toggle"]').click();
 assert(!document.querySelector('track'),'native subtitles do not create a DOM track');
 document.querySelector('[data-test="playback-subtitle-toggle"]').click();await until(()=>document.querySelector('[aria-label="字幕偏移（秒）"]'),'controlled SRT loads through the native renderer');
 const offset=document.querySelector('[aria-label="字幕偏移（秒）"]');offset.value='-0.1';offset.dispatchEvent(new Event('input',{bubbles:true}));await wait(100);
 document.querySelector('[data-test="playback-subtitle-toggle"]').click();assert(!document.querySelector('track'),'subtitle configuration stays native');
 await until(()=>document.querySelectorAll('[data-test="ending-choice"]').length===3,'natural native EOF displays three recommendations');
 assert(facts.filter(row=>row[0]===1).length===1,'one effective-view owner');
 document.querySelector('[data-test="ending-choice"]').click();await until(()=>app.id===2&&document.querySelector('cine-native-media')?.currentTime>.1,'native ending selection opens and autoplays another native source');
 assert(navigations===1,'one navigation after physical native retirement');
 media=document.querySelector('cine-native-media');await media.pause();await bridge.RenameFixture(2);await until(()=>document.querySelector('[data-test="native-playback-retry"]'),'source mutation drains the decoder before rename');
 assert(document.body.innerText.includes('无法内嵌播放'),'source retirement is visible');
 app.queue=true;await nextTick();
 const refreshQueue=async()=>{app.queueSnapshot=await bridge.GetPlaybackQueue({limit:10});};
 const offQueue=window.runtime.EventsOn('owner-queue-change',()=>refreshQueue().catch(()=>{}));
 await bridge.PrepareNativeQueueFixture();await refreshQueue();
 await until(()=>app.queueSnapshot?.session?.video_id===3&&document.querySelector('cine-native-media')?.currentTime>.1,'whole native queue starts through its authoritative token');
 await until(()=>app.queueSnapshot?.session?.video_id===4&&document.querySelector('cine-native-media')?.currentTime>.1,'native queue advances after real EOF');
 await until(()=>document.querySelector('[data-test="native-queue-ending"]'),'last native queue item reaches authoritative ending');
 offQueue();app.$.appContext.app.unmount();await wait(100);
 return {passed:true,cases:['real-manager-source-fd','native-media-element','real-frame-dimensions','play-pause-seek','popover-clock','controlled-native-srt','no-dom-track','natural-eof-three','one-view-fact','physical-retire-before-navigation','selected-native-autoplay','rename-closes-source','native-queue-token','native-queue-advance','native-queue-authoritative-end']};
}
