<template>
  <section data-test="chapter-library">
    <template v-if="!selectedVideo">
      <div class="chapter-library-toolbar"><h3>保留的人工章节</h3><input v-model="keyword" class="search-input" placeholder="搜索片名" aria-label="搜索人工章节" /><button type="button" class="btn btn--secondary" @click="load(true)">搜索 / 刷新</button></div>
      <p class="help-text">原视频删除后，人工章节仍可在这里阅读；原片不可用时不能回跳或覆盖时间位置。</p>
      <p v-if="error" role="alert">{{error}}</p><p v-if="!items.length && !loading" class="help-text">还没有人工章节，可在视频详情中创建。</p>
      <ul><li v-for="item in items" :key="item.id"><button type="button" class="btn btn--ghost" @click="open(item.video_id)">{{item.title_snapshot}}</button></li></ul>
      <button v-if="cursor" type="button" class="btn btn--secondary" :disabled="loading" @click="load(false)">加载更多</button>
    </template>
    <template v-else><button type="button" class="btn btn--ghost" @click="selectedVideo=0">返回章节列表</button><ChapterPanel :key="selectedVideo" :video-id="selectedVideo" :initial-job-id="selectedJob" :page-active="pageActive" :api="api" @seek="$emit('seek',$event)" @subtitle-tools="$emit('subtitle-tools',$event)" @settings="$emit('settings')" /></template>
  </section>
</template>
<script>
import ChapterPanel from './ChapterPanel.vue';
import {chapterAPI} from '../utils/chapters.js';
export default {
  name:'ChapterLibraryPanel',components:{ChapterPanel},props:{pageActive:{type:Boolean,default:true},request:{type:Object,default:null},api:{type:Object,default:()=>chapterAPI}},emits:['seek','subtitle-tools','settings'],
  data:()=>({items:[],cursor:0,keyword:'',error:'',loading:false,selectedVideo:0,selectedJob:0,generation:0,alive:true}),
  watch:{request:{immediate:true,handler(value){if(value?.videoID)this.open(value.videoID,value.jobID);}}},mounted(){this.load(true);},beforeUnmount(){this.alive=false;this.generation++;},
  methods:{open(id,jobID=0){this.selectedVideo=id;this.selectedJob=jobID;},async load(reset){if(this.loading)return;const ticket=++this.generation;this.loading=true;this.error='';try{const page=await this.api.library({keyword:this.keyword,cursor_id:reset?0:this.cursor,limit:50});if(this.alive&&ticket===this.generation){this.items=reset?page.items:[...this.items,...page.items];this.cursor=page.next_cursor;}}catch(error){if(this.alive&&ticket===this.generation)this.error=error.message||String(error);}finally{if(this.alive&&ticket===this.generation)this.loading=false;}}}
};
</script>
<style scoped>
.chapter-library-toolbar { display:flex;flex-wrap:wrap;gap:var(--space-2);align-items:center; }
</style>
