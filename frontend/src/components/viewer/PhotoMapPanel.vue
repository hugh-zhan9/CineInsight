<template>
 <section class="photo-map-panel" data-test="photo-map-panel">
  <div class="photo-map-toolbar"><label>拍摄日期从<input v-model="dateFrom" type="date" class="text-input" /></label><label>到<input v-model="dateTo" type="date" class="text-input" /></label><button type="button" class="btn btn--secondary" :aria-pressed="mode==='map'" @click="setMode('map')">地图</button><button type="button" class="btn btn--secondary" :disabled="!dateFrom || !dateTo" :aria-pressed="mode==='recap'" @click="setMode('recap')">旅行回顾</button><button type="button" class="btn btn--ghost" @click="refresh">刷新当前结果</button></div>
  <p class="help-text">人物沿用上方选择，日期按本机已有 EXIF 墙钟时间。旅行回顾查看整个日期与人物范围，包含没有 GPS 的照片；不会按地图视口裁剪、推断旅程或命名城市。</p>
  <p v-if="invalidPeople.length" role="alert">这个回顾包含已删除的人物条件，请先从上方人物选择中移除或修改。不会自动扩大为全部人物。</p>
  <p v-if="error" role="alert">{{error}}</p><p v-if="notice" role="status">{{notice}}</p>
  <template v-if="mode==='map'">
   <MapConnectionControls ref="connection" :api="api" :page-active="pageActive" @status="tileStatus=$event" />
   <p v-if="tileError && tileStatus?.enabled" role="alert">部分底图暂不可用，照片点和本地回顾仍可使用。{{tileStatus?.cache_full ? '缓存已达预算，可清理缓存后继续。' : ''}}</p>
  </template>
  <PhotoMapCanvas ref="canvas" v-show="mode==='map'" :clusters="mapResult?.clusters || []" :tile-status="tileStatus" :page-active="pageActive && mode==='map'" :busy="mapLoading" @bounds="viewportChanged" @select="selectCluster" @tile-error="onTileError" @tiles-loaded="tilesLoaded" />
  <p v-if="mode==='map' && mapResult" class="help-text">视口内 {{mapResult.matched_count}} 张 · 当前人物/日期条件下没有有效 GPS {{mapResult.missing_gps_count}} 张、未标日期 {{mapResult.undated_count}} 张、极区不可投影 {{mapResult.unprojectable_count}} 张。分类可能重叠；照片和 EXIF 原样保留。聚合层级 {{mapResult.grid_level}}。</p>
  <p v-if="mapLoading && mode==='map'" role="status">正在读取本地照片坐标…</p>
  <div class="photo-map-toolbar"><button type="button" class="btn btn--primary" :disabled="!dateFrom || !dateTo || invalidPeople.length" @click="editSaved(false)">将当前条件保存为命名回顾</button><button v-if="activeRecap" type="button" class="btn btn--secondary" :disabled="invalidPeople.length" @click="editSaved(true)">更新“{{activeRecap.recap.name}}”</button></div>
  <section v-if="gallery" class="photo-map-gallery">
   <h3>{{mode==='recap' ? '旅行回顾' : '这一组照片'}} · {{gallery.total}} 张</h3><p v-if="gallery.error" role="alert">{{gallery.error}}</p>
   <section v-for="group in groups" :key="group.date"><h4>{{group.label}}</h4><div class="photo-map-grid"><button v-for="item in group.items" :key="item.id" type="button" class="btn-plain photo-map-photo" :aria-label="item.name" @click="opened=gallery.items.findIndex(image=>image.id===item.id)"><img :src="`/preview/image-thumbnail/${item.id}`" :alt="item.name" loading="lazy" /><span>{{item.name}}</span></button></div></section>
   <p v-if="!gallery.items.length && !galleryLoading && !gallery.error">当前条件没有照片；无拍摄日期的照片不会进入旅行回顾。</p><button v-if="gallery.cursor" type="button" class="btn btn--secondary" :disabled="galleryLoading" @click="loadGallery(false)">加载更多</button>
  </section>
  <details class="photo-map-saved"><summary>已保存的命名回顾</summary><p v-if="savedError" role="alert">{{savedError}}</p><ul><li v-for="row in saved" :key="row.recap.id"><button type="button" class="btn btn--ghost" @click="openSaved(row)">{{row.recap.name}} · {{row.selection.date_from}} — {{row.selection.date_to}}{{row.invalid_person_ids.length ? ' · 人物条件失效' : ''}}</button><button type="button" class="btn btn--ghost btn--sm" @click="removeSaved(row)">删除回顾</button></li></ul><button v-if="savedCursor" type="button" class="btn btn--secondary" @click="loadSaved(false)">更早回顾</button></details>
  <DialogShell v-if="editor && pageActive" title="保存本地回顾条件" :busy="saving" @close="editor=null"><label>回顾名称<input v-model="editor.name" class="text-input" maxlength="100" :disabled="saving" /></label><p>{{editor.selection.date_from}} — {{editor.selection.date_to}} · {{editor.peopleLabel}} · 全部可见照片</p><p class="help-text">只保存这些条件，不复制照片、不推断城市；同名回顾也可以保留。</p><p v-if="saveError" role="alert">{{saveError}}</p><template #footer><button type="button" class="btn btn--primary" :disabled="saving || !editor.name.trim()" @click="save">保存条件</button></template></DialogShell>
  <PhotoLightbox v-if="pageActive && opened>=0 && gallery?.items[opened]" :image="gallery.items[opened]" :neighbors="gallery.items.slice(Math.max(0, opened-4), opened+5)" :has-prev="opened>0" :has-next="opened+1<gallery.items.length || Boolean(gallery.cursor)" :loading-next="galleryLoading" :next-error="gallery.error" @close="opened=-1" @prev="opened--" @next="nextPhoto" @select-neighbor="opened=gallery.items.findIndex(item=>Number(item.id)===$event)" @patch="patch" @changed="$emit('image-changed',$event)" @open-in-manage="$emit('open-in-manage',$event)" @open-cleanup="$emit('open-cleanup')" @open-person="$emit('open-person',$event)" @delete-image="$emit('delete-image',$event)" />
 </section>
</template>
<script>
import PhotoMapCanvas from './PhotoMapCanvas.vue';
import PhotoLightbox from './PhotoLightbox.vue';
import MapConnectionControls from '../MapConnectionControls.vue';
import DialogShell from '../ui/DialogShell.vue';
import {photoMapAPI} from '../../utils/photoMap.js';
import {appendPhotoMapItems,photoRecapGroups} from '../../utils/photoMapGeometry.js';
import {confirmAction} from '../../utils/feedback.js';
export default{
 name:'PhotoMapPanel',components:{PhotoMapCanvas,PhotoLightbox,MapConnectionControls,DialogShell},props:{people:{type:Array,default:()=>[]},pageActive:{type:Boolean,default:true},api:{type:Object,default:()=>photoMapAPI}},emits:['select-people','open-in-manage','open-person','delete-image','image-changed','open-cleanup'],
 data:()=>({dateFrom:'',dateTo:'',mode:'map',viewport:null,mapResult:null,mapSnapshot:null,mapLoading:false,mapWanted:false,mapGeneration:0,mapRequest:0,tileStatus:null,tileError:false,tileTimer:null,error:'',notice:'',gallery:null,galleryLoading:false,galleryGeneration:0,opened:-1,saved:[],savedCursor:0,savedError:'',activeRecap:null,editor:null,saving:false,saveError:'',alive:true}),
 computed:{selection(){return {person_ids:this.people.map(person=>person.id),date_from:this.dateFrom,date_to:this.dateTo};},invalidPeople(){return (this.activeRecap?.invalid_person_ids||[]).filter(id=>this.selection.person_ids.includes(id));},groups(){return photoRecapGroups(this.gallery?.items||[]);}},
 watch:{selection:{deep:true,handler(){this.criteriaChanged();}},pageActive(active){this.mapGeneration++;this.galleryGeneration++;this.opened=-1;if(active)this.refresh();else this.mapWanted=false;}},
 mounted(){this.loadSaved(true);},beforeUnmount(){this.alive=false;this.mapGeneration++;this.galleryGeneration++;clearTimeout(this.tileTimer);},
 methods:{
  invalidateData(){this.notice='照片资料已有更新，可刷新当前地图或回顾。';},
  removeDeletedImage(id){this.opened=-1;if(this.gallery){this.gallery.items=this.gallery.items.filter(image=>image.id!==id);this.gallery.total=Math.max(0,this.gallery.total-1);}if(this.mode==='map')this.loadMap();},
  criteriaChanged(){this.mapGeneration++;this.mapResult=null;this.mapSnapshot=null;this.galleryGeneration++;this.galleryLoading=false;this.gallery=null;this.opened=-1;this.error='';if(!this.pageActive||this.invalidPeople.length)return;if(this.mode==='map')this.loadMap();else if(this.dateFrom&&this.dateTo)this.startTimeline();},
  setMode(mode){this.mode=mode;this.criteriaChanged();},refresh(){this.tileError=false;this.$refs.canvas?.refreshTiles();this.criteriaChanged();this.loadSaved(true);},viewportChanged(value){this.viewport=value;if(this.mode==='map')this.loadMap();},
  async loadMap(){this.mapGeneration++;this.mapResult=null;this.mapSnapshot=null;if(!this.pageActive||!this.viewport||this.mode!=='map'||this.invalidPeople.length)return;if(this.mapLoading){this.mapWanted=true;return;}const ticket=this.mapGeneration,request=++this.mapRequest,snapshot={...this.viewport,...this.selection};this.mapLoading=true;this.error='';this.galleryGeneration++;this.galleryLoading=false;this.gallery=null;this.opened=-1;try{const result=await this.api.query(snapshot);if(this.alive&&ticket===this.mapGeneration){this.mapResult=result;this.mapSnapshot=snapshot;}}catch(error){if(this.alive&&ticket===this.mapGeneration)this.error=error.message||String(error);}finally{if(this.alive&&request===this.mapRequest){this.mapLoading=false;if(this.mapWanted&&this.pageActive&&this.mode==='map'){this.mapWanted=false;this.loadMap();}}}},
  selectCluster(cluster){if(this.mapLoading||!this.mapSnapshot||!this.mapResult||this.invalidPeople.length||!this.pageActive||!this.mapResult.clusters.some(item=>item.key===cluster.key))return;this.galleryGeneration++;this.galleryLoading=false;this.opened=-1;this.gallery={kind:'cluster',query:this.mapSnapshot,key:cluster.key,items:[],cursor:'',total:cluster.count,error:''};this.loadGallery(true);},
  startTimeline(){this.galleryGeneration++;this.galleryLoading=false;this.opened=-1;this.gallery={kind:'recap',selection:{...this.selection,person_ids:[...this.selection.person_ids]},items:[],cursor:'',total:0,error:''};this.loadGallery(true);},
  async loadGallery(reset){if(this.galleryLoading||!this.gallery||!this.pageActive)return;const ticket=this.galleryGeneration,gallery=this.gallery;this.galleryLoading=true;gallery.error='';try{const page=gallery.kind==='cluster'?await this.api.cluster({query:gallery.query,cell_key:gallery.key,cursor:reset?'':gallery.cursor,limit:50}):await this.api.timeline({selection:gallery.selection,cursor:reset?'':gallery.cursor,limit:50});if(!this.alive||ticket!==this.galleryGeneration||gallery!==this.gallery)return;gallery.items=reset?page.items:appendPhotoMapItems(gallery.items,page.items);gallery.cursor=page.next_cursor;gallery.total=page.total;}catch(error){if(this.alive&&ticket===this.galleryGeneration)gallery.error=error.message||String(error);}finally{if(this.alive&&ticket===this.galleryGeneration)this.galleryLoading=false;}},
  async nextPhoto(){const ticket=this.galleryGeneration;if(this.opened+1>=this.gallery.items.length&&this.gallery.cursor)await this.loadGallery(false);if(ticket===this.galleryGeneration&&this.opened+1<this.gallery.items.length)this.opened++;},patch(fields){if(this.gallery)this.gallery.items=this.gallery.items.map(item=>item.id===fields.id?{...item,...fields}:item);},
  tilesLoaded(result){this.tileError=Boolean(result?.failed);this.refreshTileStatus();},
  refreshTileStatus(){clearTimeout(this.tileTimer);this.tileTimer=setTimeout(()=>this.$refs.connection?.load(),500);},onTileError(){this.tileError=true;this.refreshTileStatus();},
  async loadSaved(reset){try{const page=await this.api.list({cursor_id:reset?0:this.savedCursor,limit:50});if(this.alive){this.saved=reset?page.items:[...this.saved,...page.items];this.savedCursor=page.next_cursor;}}catch(error){if(this.alive)this.savedError=error.message||String(error);}},
  openSaved(row){this.activeRecap=row;this.dateFrom=row.selection.date_from;this.dateTo=row.selection.date_to;this.mode='recap';const names=new Map(row.people.map(person=>[person.id,person.name]));this.$emit('select-people',row.selection.person_ids.map(id=>({id,name:names.get(id)||`已删除人物 #${id}`})));this.$nextTick(()=>this.criteriaChanged());},
  editSaved(update){this.saveError='';this.editor={id:update?this.activeRecap.recap.id:0,revision:update?this.activeRecap.recap.revision:0,name:update?this.activeRecap.recap.name:'',selection:{...this.selection,person_ids:[...this.selection.person_ids]},peopleLabel:this.people.length?this.people.map(person=>person.name).join('、'):'全部人物'};},
  async save(){if(!this.editor||this.saving||!this.pageActive)return;const editor=this.editor;this.saving=true;this.saveError='';try{const saved=await this.api.save({id:editor.id,expected_revision:editor.revision,name:editor.name,selection:editor.selection});if(this.alive){if(this.editor===editor)this.editor=null;this.activeRecap=saved;this.notice='回顾条件已保存';this.loadSaved(true);}}catch(error){if(this.alive)this.saveError=error.message||String(error);}finally{if(this.alive)this.saving=false;}},
  async removeSaved(row){if(!this.pageActive||!await confirmAction({title:'删除命名回顾',message:`只删除“${row.recap.name}”的日期和人物条件，照片保留。`,confirmText:'删除回顾',danger:true})||!this.alive||!this.pageActive)return;try{await this.api.remove(row.recap.id,row.recap.revision);if(this.alive){if(this.activeRecap?.recap.id===row.recap.id)this.activeRecap=null;this.loadSaved(true);}}catch(error){if(this.alive)this.savedError=error.message||String(error);}}
 }
};
</script>
<style scoped>
.photo-map-panel { display:grid;gap:var(--space-3); }
.photo-map-toolbar { display:flex;gap:var(--space-2);flex-wrap:wrap;align-items:end; }
.photo-map-toolbar label { display:grid;gap:var(--space-1); }
.photo-map-grid { display:grid;grid-template-columns:repeat(auto-fill,minmax(130px,1fr));gap:var(--space-3); }
.photo-map-photo img { width:100%;aspect-ratio:1;object-fit:cover;border-radius:var(--radius); }
.photo-map-photo span { display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap; }
.photo-map-saved { padding-block:var(--space-3);border-top:1px solid var(--hairline); }
</style>
