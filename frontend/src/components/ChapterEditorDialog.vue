<template>
  <DialogShell v-if="pageActive" title="编辑人工章节" size="lg" :busy="busy" :close-on-overlay="false" @close="$emit('close')">
    <p class="help-text">只保存到本地片库，不改写视频容器。章节须按时间排列且不重叠，可以有间隙。</p>
    <fieldset :disabled="busy" class="chapter-fields">
      <p v-if="sourceChanged" role="alert">原片已变化，旧章节仍可阅读。请核对新原片的全部位置后再保存。</p>
      <label v-if="sourceChanged"><input v-model="acceptSource" type="checkbox" />我已核对以上章节在新原片中的位置</label>
      <div v-for="(row,index) in visibleRows" :key="page*25+index" class="chapter-edit-row">
        <label>标题<input v-model="row.title" class="text-input" maxlength="200" /></label>
        <label>开始（秒）<input v-model="row.startSeconds" class="number-input" type="number" min="0" step="0.001" /></label><label>结束（秒）<input v-model="row.endSeconds" class="number-input" type="number" min="0" step="0.001" /></label>
        <label>摘要<textarea v-model="row.summary" class="textarea-input" rows="3" maxlength="2000" /></label>
        <button type="button" class="btn btn--ghost btn--sm" @click="remove(page*25+index)">移除此章</button>
      </div>
      <div class="chapter-editor-actions"><button type="button" class="btn btn--secondary" :disabled="rows.length >= 500" @click="add">添加章节</button><button type="button" class="btn btn--secondary" @click="rows.sort((a,b) => Number(a.startSeconds)-Number(b.startSeconds))">按时间排列</button></div>
      <label v-if="!rows.length"><input v-model="clear" type="checkbox" />明确清空人工章节（原文件章节保留）</label>
    </fieldset>
    <nav v-if="rows.length > 25" aria-label="章节编辑分页"><button type="button" class="btn btn--ghost" :disabled="busy || page===0" @click="page--">上一页</button><span>{{page+1}} / {{Math.ceil(rows.length/25)}}</span><button type="button" class="btn btn--ghost" :disabled="busy || (page+1)*25>=rows.length" @click="page++">下一页</button></nav>
    <p v-if="error" role="alert">{{error}}</p>
    <template #footer><button type="button" class="btn btn--secondary" :disabled="busy" @click="$emit('close')">取消</button><button type="button" class="btn btn--primary" :disabled="busy || (!rows.length && !clear) || (sourceChanged && !acceptSource)" @click="save">{{rows.length ? '保存人工章节' : '清空人工章节'}}</button></template>
  </DialogShell>
</template>
<script>
import DialogShell from './ui/DialogShell.vue';
import {chapterAPI,chapterEditorInputs} from '../utils/chapters.js';
export default {
  name:'ChapterEditorDialog',components:{DialogShell},props:{draft:{type:Object,required:true},pageActive:{type:Boolean,default:true},api:{type:Object,default:()=>chapterAPI}},emits:['close','saved'],
  data(){return {rows:this.draft.chapters.map(row=>({...row,startSeconds:row.start_ms/1000,endSeconds:row.end_ms/1000})),page:0,busy:false,error:'',acceptSource:false,clear:false,alive:true};},
  computed:{visibleRows(){return this.rows.slice(this.page*25,(this.page+1)*25);},sourceChanged(){return this.draft.sourceStatus==='changed';}},beforeUnmount(){this.alive=false;},
  methods:{
    add(){this.rows.push({id:0,title:'',summary:'',startSeconds:'',endSeconds:''});this.page=Math.floor((this.rows.length-1)/25);this.clear=false;},
    remove(index){this.rows.splice(index,1);this.page=Math.min(this.page,Math.max(0,Math.ceil(this.rows.length/25)-1));},
    async save(){if(this.busy||!this.pageActive||(!this.rows.length&&!this.clear)||(this.sourceChanged&&!this.acceptSource))return;this.busy=true;this.error='';try{const saved=await this.api.save({video_id:this.draft.videoID,expected_revision:this.draft.revision,source_version:this.acceptSource?this.draft.currentSource:this.draft.sourceVersion,chapters:chapterEditorInputs(this.rows),clear:this.rows.length===0&&this.clear});if(this.alive)this.$emit('saved',saved);}catch(error){if(this.alive)this.error=error.message||String(error);}finally{if(this.alive)this.busy=false;}}
  }
};
</script>
<style scoped>
.chapter-fields { border: 0; padding: 0; min-width: 0; }
.chapter-edit-row { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-2); padding-block: var(--space-3); border-bottom: 1px solid var(--hairline); }
.chapter-edit-row label:first-child, .chapter-edit-row label:has(textarea) { grid-column: 1 / -1; }
.chapter-edit-row label { display: grid; gap: var(--space-1); }
.chapter-editor-actions { display: flex; gap: var(--space-2); margin-block: var(--space-3); }
</style>
