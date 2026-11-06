import {createApp,h,nextTick} from 'vue';
import HomePage from '../../src/components/viewer/HomePage.vue';
import DiscoverPage from '../../src/components/viewer/DiscoverPage.vue';
import PlaybackEndScreen from '../../src/components/player/PlaybackEndScreen.vue';
import InlineVideoPlayer from '../../src/components/player/InlineVideoPlayer.vue';
import {createDiscoverySession} from '../../src/utils/discoverySession.js';
import {selectPlaybackEnding} from '../../src/utils/playbackEndingNavigation.js';
import '../../src/styles/tokens.css';
import '../../src/styles/components.css';
const catalog=Array.from({length:40},(_,i)=>({video:{id:i+1,name:`影片 ${i+1}`,duration:120},reasons:['本机片库']}));
const calls=[];let retireCount=0,navigationCount=0;
window.go={main:{App:new Proxy({}, {get:(_,method)=>async(...args)=>{
 calls.push({method,args});
 if(method==='SuggestTonightVideos')return catalog.filter(row=>!args[0].exclude_ids?.includes(row.video.id)).slice(0,args[1]);
 if(method==='GetVideoDetails')return {has_fanart:false};
 if(method==='SearchLibraryVideoPage')return {items:[],total:0};
 if(method==='GetRecap')return {};
 if(method==='GetPlaybackSubtitle')return {status:'absent',cues:[]};
 if(method==='GetPlaybackProxyStatus')return {running:false,queued:0,results:[]};
 if(method==='GetPlaybackProxy')return null;
 return [];
}})}};
const discovery=createDiscoverySession();let root;
const openEndedVideo=async(id,options)=>selectPlaybackEnding(id,{...options,resolveSource:async value=>({video_id:value,source_version:`source-${value}`}),navigate:async(value,source)=>{navigationCount++;root.selected={video_id:value,mode:'inline',source_version:source,inline_source:{locator_value:'./clip.mp4',mime:'video/mp4'}};root.page='detail';await nextTick();return true;}});
root=createApp({provide:()=>({discoverySession:discovery,openEndedVideo}),data:()=>({page:'home',selected:null,homeActive:true,discoverActive:false}),render(){return h('main',{class:'surface-cinema'},[
 h(HomePage,{ref:'home',pageActive:this.homeActive,style:this.page==='home'?'':'display:none',onNavigate:page=>{this.page=page;this.homeActive=false;this.discoverActive=true;}}),
 h(DiscoverPage,{ref:'discover',pageActive:this.discoverActive,style:this.page==='discover'?'':'display:none'}),
 this.page==='ending'?h(PlaybackEndScreen,{ending:{videoID:1,viewSessionID:'natural-end',cycle:1,sourceVersion:'source-1'},replay:async()=>{},retire:async()=>{retireCount++;}}):null,
 this.page==='detail'?h(InlineVideoPlayer,{ref:'player',videoId:this.selected.video_id,session:this.selected,autoplay:true}):null
]);}}).mount('#app');
const assert=(value,label)=>{if(!value)throw new Error(label+': '+JSON.stringify({tonight:root.$refs.home?.tonightBlock,calls:calls.slice(-12),body:document.body.innerText.slice(-500)}));};
const wait=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const until=async(test,label)=>{const deadline=Date.now()+8000;while(Date.now()<deadline){await nextTick();if(test())return;await wait(20);}throw new Error(label+': '+JSON.stringify({tonight:root.$refs.home?.tonightBlock,calls:calls.slice(-12),body:document.body.innerText.slice(-500)}));};
const ids=selector=>[...document.querySelectorAll(selector)].map(node=>Number(node.dataset.discoveryId));
window.fixture={async run(){
 await until(()=>document.querySelectorAll('[data-test="home-tonight-item"]').length===9,'nine home cards');
 const before=ids('[data-test="home-tonight-item"]');root.homeActive=false;await nextTick();root.homeActive=true;
 await until(()=>!root.$refs.home.tonightBlock.loading&&ids('[data-test="home-tonight-item"]').some((id,i)=>id!==before[i]),'return rotates cards');
 const after=ids('[data-test="home-tonight-item"]');assert(after.filter((id,i)=>id!==before[i]).length===3,'exactly three positions rotate');
 const played=after[4];document.querySelectorAll('[data-test="home-tonight-item"]')[4].click();await root.$refs.home.replacePlayedTonight(played);await until(()=>!ids('[data-test="home-tonight-item"]').includes(played),'playback replaces its card');
 document.querySelector('[data-test="home-discover"]').click();await until(()=>document.querySelectorAll('[data-test="discover-item"]').length===12,'twelve discover cards');
 const first=ids('[data-test="discover-item"]');root.discoverActive=false;await nextTick();root.discoverActive=true;await nextTick();assert(JSON.stringify(ids('[data-test="discover-item"]'))===JSON.stringify(first),'return preserves discover batch');
 document.querySelector('[data-test="discover-reroll"]').click();await until(()=>!root.$refs.discover.loading&&ids('[data-test="discover-item"]').some((id,i)=>id!==first[i]),'discover reroll');
 assert(ids('[data-test="discover-item"]').every(id=>!first.includes(id)),'new batch excludes current cards');
 root.page='ending';root.discoverActive=false;await until(()=>document.querySelectorAll('[data-test="ending-choice"]').length===3,'three ending choices');
 assert(!navigationCount&&!retireCount,'ending does not choose automatically');
 const choice=document.querySelector('[data-test="ending-choice"]');choice.click();choice.click();
 await until(()=>document.querySelector('video')&&!document.querySelector('video').paused,'selection autoplays detail media');
 assert(navigationCount===1&&retireCount===1,'single source-bound handoff');
 assert(root.selected.video_id!==1,'current video excluded');
 window.results={passed:true,cases:['home-nine','return-rotate-three','played-replacement','discover-twelve','discover-return-keeps-batch','reroll-no-duplicates','ending-three','no-auto-choice','single-handoff','selected-autoplay'],engine:navigator.userAgent};
}};window.ready=true;
