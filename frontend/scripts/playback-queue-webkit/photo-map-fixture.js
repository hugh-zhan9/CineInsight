import {createApp,h,nextTick} from 'vue';
import PhotoMapPanel from '../../src/components/viewer/PhotoMapPanel.vue';
import AppFeedback from '../../src/components/AppFeedback.vue';
import '../../src/styles/tokens.css';
import '../../src/styles/components.css';
let policy={enabled:false,provider:'osm',generation:'off',cache_bytes:0,budget_bytes:268435456,cache_full:false,cache_error:''};const calls=[],saved=[];
const images=[1,2].map(id=>({id,name:`合成照片 ${id}`,format:'png',width:256,height:256,taken_date:'2026-11-01',taken_at:'2026-11-01T12:00:00+08:00',tags:[],view_count:0}));
const ok=data=>({status:'ok',data});
window.go={main:{App:new Proxy({},{get:(_,name)=>async(...args)=>{
 calls.push({name,args});
 if(name==='GetMapTileStatus')return ok(policy);
 if(name==='SetMapTilesEnabled'){policy={...policy,enabled:args[0].enabled,generation:args[0].enabled?'on':'off',cache_bytes:1048576};return ok(policy);}
 if(name==='ClearMapTileCache'){policy={...policy,cache_bytes:0,generation:'cleared'};return ok(policy);}
 if(name==='QueryPhotoMap'){if(args[0].person_ids.includes(99))return {status:'error',error:{code:'photo_filter_invalid',message:'人物条件已失效'}};return ok({clusters:[{key:'fixture-cell',count:2,center_lat:10,center_lon:0,sample_image_id:1}],matched_count:2,missing_gps_count:1,undated_count:1,unprojectable_count:0,grid_level:1});}
 if(name==='ListPhotoMapCluster'||name==='ListPhotoRecapImages')return ok({items:images,next_cursor:'',total:2});
 if(name==='ListPhotoRecaps')return ok({items:saved,next_cursor:0});
 if(name==='SavePhotoRecap'){const input=args[0];const view={recap:{id:1,revision:1,name:input.name},selection:input.selection,people:[],invalid_person_ids:[]};saved.push(view);return ok(view);}
 if(name==='GetImageDetail')return {image:images.find(image=>image.id===args[0]),people:[]};
 if(name==='RecordImageView')return {recorded:true,view_count:1,last_viewed_at:'2026-11-04T00:00:00Z'};
 return [];
}})}};
const root=createApp({data:()=>({people:[]}),render(){return h('main',{style:'padding:16px;max-width:1000px;margin:auto'},[h('h2','合成照片地图验收（本机模拟瓦片）'),h(PhotoMapPanel,{ref:'panel',people:this.people,onSelectPeople:value=>{this.people=value;}}),h(AppFeedback)]);}}).mount('#app');
const assert=(value,label)=>{if(!value)throw new Error(label);};
const until=async(test,label)=>{window.progress=label;const end=Date.now()+8000;while(!test()&&Date.now()<end){await new Promise(resolve=>setTimeout(resolve,25));await nextTick();}assert(test(),label);};
const findButton=text=>[...document.querySelectorAll('button')].find(button=>button.textContent.trim()===text);
const click=async text=>{const button=findButton(text);assert(button,'missing '+text);button.click();await nextTick();};
const native=async element=>{element.scrollIntoView({block:'center'});await new Promise(resolve=>setTimeout(resolve,80));const rect=element.getBoundingClientRect();window.webkit.messageHandlers.nativeClick.postMessage({x:rect.left+rect.width/2,y:rect.top+rect.height/2});};
const serverCalls=async()=>await(await fetch('/calls')).json();
window.fixture={async run(){
 await until(()=>document.querySelector('.leaflet-marker-icon'),'local map points');assert(!(await serverCalls()).some(call=>call.kind==='tile'),'no network tiles before opt-in');assert(!calls.some(call=>call.name==='RecordImageView'),'map browsing is not an image view');
 await native(document.querySelector('.leaflet-marker-icon'));await until(()=>document.querySelector('.photo-map-photo'),'cluster drilldown');assert(!calls.some(call=>call.name==='RecordImageView'),'thumbnails are not image views');
 const dates=[...document.querySelectorAll('input[type=date]')];for(const [index,value] of ['2026-11-01','2026-11-02'].entries()){dates[index].value=value;dates[index].dispatchEvent(new Event('input',{bubbles:true}));}await nextTick();await click('旅行回顾');await until(()=>document.querySelector('.photo-map-photo'),'date recap');
 await native(document.querySelector('.photo-map-photo'));await until(()=>document.querySelector('[data-test=photo-lightbox-img]')?.naturalWidth===256,'real local image');await until(()=>calls.some(call=>call.name==='RecordImageView'),'lightbox records actual view');document.querySelector('[data-test=photo-lightbox-close]').click();await nextTick();
 await click('将当前条件保存为命名回顾');await until(()=>document.querySelector('[role=dialog] input'),'save conditions dialog');const name=document.querySelector('[role=dialog] input');name.value='本地测试回顾';name.dispatchEvent(new Event('input',{bubbles:true}));await nextTick();await click('保存条件');await until(()=>saved.length===1,'saved local conditions');assert(!('bounds' in saved[0].selection),'recap does not inherit viewport');
 await click('地图');await until(()=>findButton('开启联网底图…'),'map controls');await click('开启联网底图…');await until(()=>document.querySelector('[data-test=app-confirm-ok]'),'explicit map disclosure');await native(document.querySelector('[data-test=app-confirm-ok]'));
 await until(()=>document.querySelector('.leaflet-tile-loaded')?.naturalWidth===256,'synthetic tiles decoded');await until(()=>!document.querySelector('.leaflet-tile:not(.leaflet-tile-loaded)'),'tiles settled');
 const attribution=document.querySelector('.photo-map-canvas__attribution');attribution.scrollIntoView({block:'center'});await new Promise(resolve=>setTimeout(resolve,80));const rect=attribution.getBoundingClientRect();assert(document.elementFromPoint(rect.left+rect.width/2,rect.top+rect.height/2)?.closest('.photo-map-canvas__attribution'),'visible attribution above tiles');
 const before=(await serverCalls()).filter(call=>call.kind==='tile'&&call.generation==='on').length;await click('关闭联网底图');await native(document.querySelector('.leaflet-control-zoom-in'));await new Promise(resolve=>setTimeout(resolve,500));assert((await serverCalls()).filter(call=>call.kind==='tile'&&call.generation==='on').length===before,'no enabled requests after disable');
 root.people=[{id:99,name:'已删除人物'}];await until(()=>document.body.textContent.includes('人物条件已失效'),'failed current filter');assert(document.querySelectorAll('.leaflet-marker-icon').length===0,'old points cannot bypass failed filter');
 root.people=[];await until(()=>document.querySelector('.leaflet-marker-icon'),'local map restored');document.querySelector('.photo-map-canvas').scrollIntoView({block:'center'});
 window.results={passed:true,realDecodedImages:true,cases:['local-points-without-network','native-cluster-drilldown','date-recap','lightbox-view-boundary','save-conditions-only','explicit-map-opt-in','synthetic-tile-render','visible-attribution','off-stops-enabled-requests','failed-filter-clears-old-points']};
}};window.ready=true;
