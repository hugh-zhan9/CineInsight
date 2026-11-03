<template>
 <section class="map-connection" aria-label="联网底图">
  <p v-if="status">OpenStreetMap · {{status.enabled ? '联网底图已开启' : '联网已关闭，仅可显示已有缓存'}} · {{status.cache_error ? '缓存状态不可用' : `缓存 ${(status.cache_bytes/1048576).toFixed(1)} / ${(status.budget_bytes/1048576).toFixed(0)} MiB`}}</p>
  <p class="help-text">照片和坐标在本机处理。开启底图后，OpenStreetMap 会知道请求的地图区域和网络连接信息；不会收到照片、人物、文件路径或完整坐标清单。不接地名查询。</p>
  <p v-if="status?.cache_full" role="status">缓存已达预算，已停止请求新瓦片；可清理底图缓存后继续。不会自动淘汰后重复下载。</p>
  <p v-if="status?.cache_error" role="alert">底图缓存不可用，可清理底图缓存；本地照片与回顾仍可使用。</p>
  <p v-if="error" role="alert">{{error}}</p>
  <div class="map-connection__actions"><button v-if="status" type="button" class="btn btn--secondary btn--sm" :disabled="busy" @click="toggle">{{status.enabled ? '关闭联网底图' : '开启联网底图…'}}</button><button type="button" class="btn btn--ghost btn--sm" :disabled="busy" @click="load">刷新状态</button><button type="button" class="btn btn--ghost btn--sm" :disabled="busy" @click="clear">清理底图缓存</button></div>
 </section>
</template>
<script>
import {photoMapAPI} from '../utils/photoMap.js';
import {confirmAction} from '../utils/feedback.js';
export default{
 name:'MapConnectionControls',props:{api:{type:Object,default:()=>photoMapAPI},pageActive:{type:Boolean,default:true}},emits:['status'],data:()=>({status:null,busy:false,error:'',generation:0,alive:true}),
 watch:{pageActive(active){this.generation++;if(active)this.load();}},mounted(){if(this.pageActive)this.load();},beforeUnmount(){this.alive=false;this.generation++;},
 methods:{
  async load(){if(!this.pageActive||this.busy)return;const ticket=this.generation;try{const status=await this.api.tileStatus();if(this.alive&&ticket===this.generation){this.status=status;this.$emit('status',status);}}catch(error){if(this.alive&&ticket===this.generation)this.error=error.message||String(error);}},
  async toggle(){if(!this.status||this.busy||!this.pageActive)return;const ticket=this.generation,enabled=!this.status.enabled,generation=this.status.generation;if(enabled&&!await confirmAction({title:'开启联网底图',message:'将请求当前视口的 OpenStreetMap 栅格地图。服务商会知道地图区域与网络连接信息；照片、人物及坐标清单留在本机，不查询在线地名。',confirmText:'确认开启'}))return;if(!this.alive||!this.pageActive||ticket!==this.generation)return;this.busy=true;this.error='';try{const status=await this.api.tilePolicy({enabled,confirmed:enabled,generation});if(this.alive){this.status=status;this.$emit('status',status);}}catch(error){if(this.alive)this.error=error.message||String(error);}finally{if(this.alive)this.busy=false;}},
  async clear(){if(this.busy||!this.pageActive)return;this.busy=true;this.error='';try{const status=await this.api.clearTiles();if(this.alive){this.status=status;this.$emit('status',status);}}catch(error){if(this.alive)this.error=error.message||String(error);}finally{if(this.alive)this.busy=false;}}
 }
};
</script>
<style scoped>
.map-connection { padding:var(--space-3);border:1px solid var(--hairline);border-radius:var(--radius); }
.map-connection__actions { display:flex;gap:var(--space-2);flex-wrap:wrap; }
</style>
