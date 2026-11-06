import { createApp, h, nextTick } from 'vue';
import {runOwnerFixture} from './owner-fixture.js';
import PlaybackControls from '../../src/components/player/PlaybackControls.vue';
import '../../src/styles/tokens.css';
import '../../src/styles/components.css';
const rpc = () => window.go.main.ProbeApp;
class NativeProbeMedia extends HTMLElement {
  constructor() { super(); this.snapshot = { position: 0, duration: NaN, paused: true, muted: true, volume: 1, rate: 1 }; }
  get currentTime() { return this.snapshot.position; } set currentTime(value) { rpc().Command('seek', value); }
  get duration() { return this.snapshot.duration; }
  get paused() { return this.snapshot.paused; }
  get ended() { return this.snapshot.ended; }
  get muted() { return this.snapshot.muted; } set muted(value) { rpc().Command('muted', Number(value)); }
  get volume() { return this.snapshot.volume; } set volume(value) { rpc().Command('volume', Number(value)); }
  get playbackRate() { return this.snapshot.rate; } set playbackRate(value) { rpc().Command('rate', Number(value)); }
  get readyState() { return this.snapshot.duration > 0 ? 4 : 0; }
  get videoWidth() { return this.snapshot.width || 0; } get videoHeight() { return this.snapshot.height || 0; }
  get buffered() { return { length: 0 }; } get seekable() { return { length: Number(this.duration > 0), start: () => 0, end: () => this.duration }; }
  get textTracks() { return []; }
  get seeking() { return !!this.snapshot.seeking; }
  async play() { await rpc().Command('play', 0); this.dispatchEvent(new Event('play')); }
  pause() { rpc().Command('pause', 0); }
  async sync() {
    const old = this.snapshot; this.snapshot = await rpc().Snapshot();
    if (!old.duration && this.duration) this.dispatchEvent(new Event('loadedmetadata'));
    if (old.paused !== this.paused) this.dispatchEvent(new Event(this.paused ? 'pause' : 'playing'));
    if (old.muted !== this.muted || old.volume !== this.volume) this.dispatchEvent(new Event('volumechange'));
    this.dispatchEvent(new Event('timeupdate'));
  }
}
customElements.define('native-probe-media', NativeProbeMedia);
const root = createApp({ data: () => ({ overlay: false }), render() {
  return h('main', { class: 'surface-cinema', style: 'padding:24px;height:100vh;box-sizing:border-box' }, [
    h('h2', {}, '原生播放 · Wails 集成验证'),
    h('dialog', { open: true, ref: 'shell', style: 'position:relative;margin:0;width:100%;padding:0;background:var(--player-bg);border:0' }, [
      h('media-controller', { ref: 'surface', id: 'probe-media', nohotkeys: true, style: 'display:block;position:relative;width:100%' }, [
        h('native-probe-media', { ref: 'media', slot: 'media', style: 'display:block;height:380px;width:100%;background:var(--player-bg)' }),
        h(PlaybackControls, { ref: 'controls', videoElement: () => this.$refs.media, dialogElement: () => this.$refs.shell, surfaceElement: () => this.$refs.surface })
      ])
    ]),
    this.overlay ? h('div', { id: 'probe-overlay', style: 'position:fixed;inset:160px 80px auto;padding:50px;background:var(--panel-bg);z-index:9999' }, '遮挡检查：原生画面已隐藏，HTML弹窗可交互') : null
  ]);
} }).mount('#app');
const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
const assert = (value, label) => { if (!value) throw new Error(label); };
async function until(test,label,timeout=10000){const end=performance.now()+timeout;while(performance.now()<end){await root.$refs.media.sync();if(test())return;await wait(30);}throw new Error(label);}
async function viewport(visible=true){await nextTick();const r=root.$refs.media.getBoundingClientRect();await rpc().Viewport(r.x,r.y,r.width,r.height,visible);}
async function run(){
 await rpc().Create();await viewport();
 await until(()=>root.$refs.media.duration>0,'metadata');
 const start=performance.now();await root.$refs.media.play();
 await until(()=>root.$refs.media.snapshot.frames>2&&root.$refs.media.currentTime>0,'first rendered frames');
 const firstFrameMS=performance.now()-start;
 const occlusionStart=performance.now(),beforeOcclusion=root.$refs.media.currentTime;await viewport(false);await until(()=>root.$refs.media.currentTime>beforeOcclusion+.1,'occluded playback keeps its clock moving',2000);assert(root.$refs.media.currentTime-beforeOcclusion<(performance.now()-occlusionStart)/1000+.3,'occluded playback preserves playback speed');await viewport();
 assert(await rpc().Pixels()>10,'GPU framebuffer contains the synthetic image');
 let pictureClick=false;root.$refs.media.addEventListener('click',event=>{pictureClick=event.isTrusted},{once:true});
 const picture=root.$refs.media.getBoundingClientRect();await rpc().Click(picture.x+picture.width/2,picture.y+picture.height/2);await until(()=>pictureClick,'native image passes trusted input to the WebView');
 const actual=root.$refs.media.snapshot, expected=root.$refs.media.getBoundingClientRect();
 assert(Math.abs(actual.x-expected.x)<1&&Math.abs(actual.y-expected.y)<1&&Math.abs(actual.render_width-expected.width)<1,'native surface matches DOM coordinates');
 await rpc().Viewport(expected.x,-40,expected.width,expected.height,true);await root.$refs.media.sync();
 assert(Math.abs(root.$refs.media.snapshot.render_height-expected.height)<1&&Math.abs(root.$refs.media.snapshot.y+40)<1,'clipping preserves the full video scale');await viewport();
 await rpc().Command('pause',0);await until(()=>root.$refs.media.paused,'pause');
 await rpc().Command('seek',1);await until(()=>Math.abs(root.$refs.media.currentTime-1)<.2,'seek');
 await rpc().Subtitles([{start_ms:500,end_ms:1800,text:'第一行\n<literal>'},{start_ms:1500,end_ms:2500,text:'重叠'}],-500);
 await until(()=>root.$refs.media.snapshot.subtitle_text==='第一行\n<literal>\n重叠','native overlapping subtitles and negative offset');
 await rpc().Command('seek',2.1);await until(()=>root.$refs.media.currentTime>=2.05&&!root.$refs.media.snapshot.subtitle_text,'subtitle disappears at end');
 await rpc().Subtitles(Array.from({length:10000},(_,i)=>({start_ms:i*100,end_ms:i*100+200,text:`cue ${i}`})),0);
 await rpc().Command('seek',1);await until(()=>root.$refs.media.snapshot.subtitle_text.includes('cue 10'),'long subtitle index after seek');
 await rpc().Subtitles([],0);await until(()=>!root.$refs.media.snapshot.subtitle_text,'subtitle clear');
 root.overlay=true;await viewport(false);await root.$refs.media.sync();assert(root.$refs.media.snapshot.hidden,'overlay hides native surface');
 root.overlay=false;await viewport();
 await root.$refs.controls.enterAppFullscreen();await viewport();assert(root.$refs.shell.matches(':modal'),'application fullscreen');
 await root.$refs.controls.leaveFullscreen();await viewport();
 root.$refs.controls.menuOpen=true;await nextTick();await viewport(false);
 const fullButton=document.querySelector('[data-test="player-system-fullscreen"]'), rect=fullButton.getBoundingClientRect();
 await rpc().Click(rect.x+rect.width/2,rect.y+rect.height/2);
 await until(()=>document.fullscreenElement===root.$refs.surface,'system fullscreen');await viewport();await root.$refs.media.sync();
 await until(()=>root.$refs.media.snapshot.front_window&&root.$refs.media.snapshot.window_visible,'native surface belongs to visible system fullscreen window');
 assert(!root.$refs.media.snapshot.hidden&&await rpc().Pixels()>10,'system fullscreen preserves native image');
 await root.$refs.controls.leaveFullscreen();await viewport();
 await rpc().Command('seek',Math.max(0,root.$refs.media.duration-2));await root.$refs.media.play();await until(()=>root.$refs.media.ended,'real EOF');
 await rpc().Stress();await rpc().RejectReferences();
 const report={passed:true,first_frame_ms:firstFrameMS,cases:['wails-window','gpu-frames','gpu-pixels','dom-coordinates','clipped-scale','real-controls','pause','seek','modal-occlusion','app-fullscreen','system-fullscreen','native-subtitles','eof','close'],runtime:'pinned runtime; probe only'};
 sessionStorage.setItem('native-probe-result',JSON.stringify(report));sessionStorage.setItem('native-probe-epoch',String(await rpc().Epoch()));window.location.reload();
}
async function performanceRun(mode){
 const samples=[];const iterations=mode==='warm'?20:1;const sampleIndex=await rpc().SampleIndex();
 for(let i=0;i<iterations;i++){
  await rpc().Close();const start=performance.now();await rpc().Create();await viewport();
  await until(()=>root.$refs.media.duration>0&&root.$refs.media.videoWidth>0,'performance metadata');
  await root.$refs.media.play();await until(()=>root.$refs.media.snapshot.frames>=2&&root.$refs.media.currentTime>.05,'performance first frame');
  const first=performance.now()-start;await rpc().Command('pause',0);await until(()=>root.$refs.media.paused,'performance pause');
  const target=root.$refs.media.duration*(mode==='cold37'?.75:(.1+(((i+sampleIndex)*37)%70)/100)),frames=root.$refs.media.snapshot.frames,seekStart=performance.now();
  await rpc().Command('seek',target);await until(()=>!root.$refs.media.seeking&&Math.abs(root.$refs.media.currentTime-target)<.5&&root.$refs.media.snapshot.frames>frames,'performance seek').catch(error=>{throw new Error(`${error.message}: ${JSON.stringify({i,target,frames,state:root.$refs.media.snapshot})}`)});
  samples.push({first_frame_ms:first,seek_ms:performance.now()-seekStart,duration:root.$refs.media.duration,width:root.$refs.media.videoWidth,height:root.$refs.media.videoHeight});
 }
 await rpc().Finish(JSON.stringify({passed:true,mode,samples}));
}
async function start(){while(!window.go?.main?.ProbeApp)await wait(20);try{const mode=await rpc().Mode();if(mode==='owner'){root.$.appContext.app.unmount();const result=await runOwnerFixture();await rpc().Finish(JSON.stringify(result));return;}if(mode.startsWith('cold')||mode==='warm'){await performanceRun(mode);return;}const previous=sessionStorage.getItem('native-probe-result');if(previous){const result=JSON.parse(previous);assert(await rpc().Epoch()>Number(sessionStorage.getItem('native-probe-epoch')),'reload advances native window epoch');result.cases.push('window-reload-epoch','two-players-20-cycles','long-subtitles','trusted-image-input','reference-network-denied');sessionStorage.clear();await rpc().Finish(JSON.stringify(result));}else await run();}catch(error){await rpc().Finish(JSON.stringify({passed:false,error:String(error),stack:error.stack}));}}
start();
