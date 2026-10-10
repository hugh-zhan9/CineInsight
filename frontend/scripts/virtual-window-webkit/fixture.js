import { createApp, h, nextTick } from 'vue';
import VirtualVideoList from '../../src/components/VirtualVideoList.vue';
import VideoListRow from '../../src/components/VideoListRow.vue';
import AITagReviewDialog from '../../src/components/AITagReviewDialog.vue';
import ImageAITagReviewPanel from '../../src/components/ImageAITagReviewPanel.vue';
import { estimateVideoRowHeight, estimateVideoGridHeight } from '../../src/utils/virtualList.js';
import '../../src/styles/tokens.css';
import '../../src/styles/components.css';
const makeItems = count => Array.from({ length: count }, (_, i) => ({ id: i+1, name: `Synthetic ${i}.mp4`, path: `/synthetic/${i}.mp4`, tags: Array.from({ length: (i%4)*8 }, (_, j) => ({ id:j+1, name:`标签文字 ${j}`, color:'#9988ee' })) }));
let candidates=[];
window.go={main:{App:new Proxy({}, {get:(_, name)=>async()=> {
  if (/^(List|Search)(Image)?AITagCandidatePage$/.test(name)) return {items:candidates,next_id:0};
  if (name==='GetAITaggingStatusSummary') return {config_available:true,pending:candidates.length};
  if (name==='GetAIReviewApproval') return {};
  return [];
}})}};
const root=createApp({data:()=>({items:makeItems(20000),layout:'list',active:true,mode:'library',density:'compact',width:1100}),render(){
  if(this.mode==='video-review')return h(AITagReviewDialog,{ref:'panel',visible:true,qualityEnabled:false});
  if(this.mode==='image-review')return h(ImageAITagReviewPanel,{ref:'panel',visible:true});
  return h('div',{class:'main-view',style:{height:'680px',overflow:'auto',width:`${this.width}px`}},[
    h('div',{style:{display:this.active?'':'none'}},[h(VirtualVideoList,{ref:'list',items:this.items,active:this.active,layoutMode:this.layout,layoutKey:this.density,estimateHeight:(item,b,s,g)=>this.layout==='grid'?estimateVideoGridHeight(item,g.cardWidth):estimateVideoRowHeight(item,b,s,this.density),itemVersion:item=>JSON.stringify(item.tags)}, {default:({item})=>h(VideoListRow,{video:item,layoutMode:this.layout,density:this.density})})]),
    !this.active?h('div',{style:{height:'5000px'}},'other page'):null
  ]);
}}).mount('#app');
const settle=async()=>{await nextTick();await new Promise(resolve=>setTimeout(resolve,180));await nextTick();};
const assert=(value,message)=>{if(!value)throw new Error(message);};
const host=()=>document.querySelector(root.mode==='library'?'.main-view':root.mode==='video-review'?'.ai-review-workbench-content':'.image-ai-tag-review__content');
const snapshot=label=>{
  window.progress=label;
  const owner=host(),box=owner.getBoundingClientRect();
  const nodes=[...owner.querySelectorAll('[data-virtual-row-id]')];
  const visible=nodes.filter(n=>n.getBoundingClientRect().bottom>box.top && n.getBoundingClientRect().top<box.bottom);
  const first=visible[0],last=visible.at(-1);
  return {label,rendered:nodes.length,scrollTop:owner.scrollTop,scrollHeight:owner.scrollHeight,viewport:owner.clientHeight,anchor:first?{id:first.dataset.virtualRowId,offset:first.getBoundingClientRect().top-box.top}:null,last:last?.dataset.virtualRowId};
};
const compare=(before,after,{grid=false}={})=>{
  assert(before.anchor && after.anchor, `${after.label}: missing anchor before=${JSON.stringify(before)} after=${JSON.stringify(after)}`);
  assert(after.rendered<200,`${after.label}: DOM ${after.rendered}`);
  if(grid){const row=host().querySelector(`[data-virtual-row-id="${before.anchor.id}"]`);assert(row,`${after.label}: previous card missing`);const offset=row.getBoundingClientRect().top-host().getBoundingClientRect().top;assert(Math.abs(offset-before.anchor.offset)<1.5,`${after.label}: anchor offset ${offset} != ${before.anchor.offset}`);}
  else assert(before.anchor.id===after.anchor.id && Math.abs(before.anchor.offset-after.anchor.offset)<1.5,`${after.label}: anchor ${JSON.stringify(after.anchor)} != ${JSON.stringify(before.anchor)}`);
};
window.fixture={async run(){
  const results=[];
  for(const layout of ['list','grid']){
    root.layout=layout;await settle();host().scrollTop=300000;host().dispatchEvent(new Event('scroll'));await settle();
    let before=snapshot(`${layout}-deep`);results.push(before);
    for(const width of [660,1100]){root.width=width;await settle();const after=snapshot(`${layout}-width-${width}`);compare(before,after,{grid:layout==='grid'});results.push(after);before=after;}
    if(layout==='list'){root.density='comfortable';await settle();let changed=snapshot('list-density');compare(before,changed);results.push(changed);before=changed;const id=Number(before.anchor.id);for(const item of root.items.filter(item=>item.id>=id-5 && item.id<=id)){item.tags=item.tags.map(tag=>({...tag,name:tag.name+' 一段较长的标签文字'}));}await settle();changed=snapshot('list-tag-names');compare(before,changed);results.push(changed);before=changed;}
    root.items.push(...makeItems(100).map((item,i)=>({...item,id:30000+i})));await settle();let after=snapshot(`${layout}-append`);compare(before,after);results.push(after);before=after;
    root.items=root.items.filter(item=>item.id>3);await settle();after=snapshot(`${layout}-delete-before`);compare(before,after,{grid:layout==='grid'});results.push(after);before=after;
    root.active=false;await settle();host().scrollTop=800;host().dispatchEvent(new Event('scroll'));root.width=880;await settle();assert(host().scrollTop===800,`${layout}: hidden changed other page`);
    root.active=true;await settle();after=snapshot(`${layout}-return`);compare(before,after,{grid:layout==='grid'});results.push(after);
    root.items=makeItems(20000);root.width=1100;await settle();
    host().scrollTop=host().scrollHeight;host().dispatchEvent(new Event('scroll'));await settle();after=snapshot(`${layout}-end`);assert(after.last==='20000',`${layout}: final row missing ${after.last}`);results.push(after);
    if(layout==='grid'){
      root.items=makeItems(6);await settle();const nodes=host().querySelectorAll('[data-virtual-row-id]');const width=nodes[0].getBoundingClientRect().width;assert(Math.abs(nodes[5].getBoundingClientRect().width-width)<1,'grid: final card stretches');results.push(snapshot('grid-six-cards'));root.items=makeItems(20000);await settle();
    }
  }
  for(const kind of ['video','image'])for(const single of [false,true]){
    const count=single?100000:20000;
    candidates=Array.from({length:count},(_,i)=>{const id=single?1:i+1;return {id:i+1,video_id:id,image_id:id,video:{id,name:`Video ${id}`,path:`/synthetic/${id}`,tags:[]},image:{id,name:`Image ${id}`,path:`/synthetic/${id}`},suggested_name:`候选 ${i+1}`,confidence:'high',status:'pending',reasoning:'用于原生布局的合成理由。'.repeat((i%4+1)*3)};});
    root.mode='library';await settle();root.mode=`${kind}-review`;await settle();if(kind==='video')await root.$refs.panel.loadCandidates();await settle();
    let before=snapshot(`${kind}-${single?'one-media':'many-media'}-initial`);assert(before.rendered>0 && before.rendered<100,`${kind}: initial unbounded`);assert(before.viewport>100,`${kind}: collapsed viewport`);results.push(before);
    host().scrollTop=300000;host().dispatchEvent(new Event('scroll'));await settle();before=snapshot(`${kind}-${single?'one-media':'many-media'}-deep`);results.push(before);
    host().style.width='540px';await settle();let after=snapshot(`${kind}-${single?'one-media':'many-media'}-resize`);compare(before,after);results.push(after);before=after;
    root.$refs.panel.candidates=root.$refs.panel.candidates.filter(item=>item.id!==1);await settle();after=snapshot(`${kind}-${single?'one-media':'many-media'}-remove-first`);compare(before,after);results.push(after);
    host().scrollTop=host().scrollHeight;host().dispatchEvent(new Event('scroll'));await settle();after=snapshot(`${kind}-${single?'one-media':'many-media'}-end`);assert(after.last===`candidate:${count}`,`${kind}: final candidate missing ${after.last}`);results.push(after);
  }
  window.results={ua:navigator.userAgent,passed:true,results};return window.results;
}};
window.ready=true;
