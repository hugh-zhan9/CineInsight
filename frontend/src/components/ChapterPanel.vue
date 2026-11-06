<template>
  <section class="chapter-panel" data-test="chapter-panel">
    <div class="chapter-toolbar"><h3>{{sets?.title ? `${sets.title} · 章节` : '章节'}}</h3><button type="button" class="btn btn--secondary btn--sm" :disabled="busy" @click="load">刷新</button><button type="button" class="btn btn--secondary btn--sm" :disabled="busy || !sets?.source_version" @click="probe">读取原文件章节 / 补全技术信息</button></div>
    <p v-if="error" role="alert">{{error}}</p><p v-if="notice" role="status">{{notice}}</p>
    <div v-if="sets" class="chapter-sources">
      <section v-for="kind in ['embedded','manual']" :key="kind"><h4>{{kind==='embedded' ? '原文件章节' : '人工章节'}}</h4>
        <p class="help-text">{{sets[kind] ? sourceLabels[sets[kind].source_status] : '尚未创建人工章节'}}</p>
        <ol v-if="sets[kind]"><li v-for="chapter in sets[kind].chapters" :key="chapter.id"><button type="button" class="btn btn--ghost btn--sm" :disabled="busy || sets[kind].source_status!=='ready'" @click="resolve(sets[kind],chapter)">{{chapter.title}} · {{time(chapter.start_ms)}}</button><details v-if="chapter.summary"><summary>查看摘要</summary><p class="chapter-summary">{{chapter.summary}}</p></details></li></ol>
        <button v-if="kind==='manual'" type="button" class="btn btn--secondary btn--sm" :disabled="busy || !sets.source_version || !sets.duration_ms" @click="edit(false)">编辑人工章节</button>
        <button v-if="kind==='embedded' && sets.embedded?.chapters.length" type="button" class="btn btn--secondary btn--sm" :disabled="busy || sets.embedded.source_status!=='ready' || !sets.duration_ms" @click="edit(true)">复制到人工编辑稿</button>
      </section>
    </div>
    <AutomaticStructurePanel :video-id="videoId" :page-active="pageActive" @seek="$emit('seek',$event)" @copy="copyAutomatic" />
    <details class="chapter-generation"><summary>生成字幕章节摘要</summary><p class="help-text">需要有效的同名 SRT 和显式启用的外部文本 AI。不会自动生成字幕，也不发送视频、图片或文件路径。摘要仅依据字幕，可能遗漏无对白画面。</p>
      <label><input v-model="whole" type="checkbox" :disabled="busy" />全片字幕</label>
      <div v-if="!whole" class="chapter-toolbar"><label>开始（秒）<input v-model="scopeStart" type="number" class="number-input" min="0" step="0.001" :disabled="busy" /></label><label>结束（秒）<input v-model="scopeEnd" type="number" class="number-input" min="0" step="0.001" :disabled="busy" /></label></div>
      <div class="chapter-toolbar"><button type="button" class="btn btn--primary" :disabled="busy || !sets?.source_version || !sets?.duration_ms" @click="prepare">核对将发送的字幕范围</button><button type="button" class="btn btn--secondary" @click="$emit('subtitle-tools',videoId)">打开字幕工作台</button><button type="button" class="btn btn--ghost" @click="$emit('settings')">文本 AI 设置</button></div>
    </details>
    <h4>生成草稿与任务</h4><p v-if="jobError" role="alert">{{jobError}}</p><p v-if="!jobs.length" class="help-text">还没有生成任务。</p>
    <ul><li v-for="job in jobs" :key="job.id"><button type="button" class="btn btn--ghost" :disabled="busy" @click="openJob(job.id)">{{labels[job.status] || job.status}} · {{job.whole ? '全片' : '局部'}} · {{time(job.scope_start_ms)}} — {{time(job.scope_end_ms)}} · {{job.completed_chunks}}/{{job.total_chunks}} 段</button></li></ul>
    <button v-if="jobCursor" type="button" class="btn btn--secondary" :disabled="jobsLoading" @click="loadJobs(true)">更早任务</button>
    <ChapterEditorDialog v-if="editor" :draft="editor" :api="api" :page-active="pageActive" @close="editor=null" @saved="saved" />
    <ChapterDraftDialog v-if="draft" :key="draft.job.id" :draft="draft" :api="api" :page-active="pageActive" @close="closeDraft" @refresh="openJob(draft.job.id)" @changed="changed" @seek="$emit('seek',$event)" />
    <DialogShell v-if="preparation && pageActive" title="确认发送字幕" :busy="busy" @close="preparation=null">
      <p>视频：{{preparation.title || sets?.title}}</p><p>将向 {{preparation.provider}} 的 {{preparation.model}} 发送以下字幕：</p><p>{{preparation.scope.whole ? '全片字幕摘要' : '局部字幕摘要'}} · {{time(preparation.scope.start_ms)}} — {{time(preparation.scope.end_ms)}}</p><p>{{preparation.cue_count}} 条字幕，{{preparation.character_count}} 字，分 {{preparation.chunk_count}} 段处理。范围已扩展至完整字幕边界。</p>
      <p class="help-text">不上传音视频、图片、文件路径或完整片库。任一分段失败都不会替换人工章节；不会自动重试。整任务最多 15 分钟（含排队），超时会明确失败。取消将停止后续请求，已发送的字幕无法撤回。</p><label><input v-model="confirmed" type="checkbox" :disabled="busy" />我确认以上发送范围</label><p v-if="startError" role="alert">{{startError}}</p>
      <template #footer><button type="button" class="btn btn--primary" :disabled="busy || !confirmed" @click="start">发送字幕并生成草稿</button></template>
    </DialogShell>
  </section>
</template>
<script>
import AutomaticStructurePanel from './AutomaticStructurePanel.vue';
import DialogShell from './ui/DialogShell.vue';
import ChapterEditorDialog from './ChapterEditorDialog.vue';
import ChapterDraftDialog from './ChapterDraftDialog.vue';
import {chapterAPI,chapterTime,chapterEditorInputs,CHAPTER_JOB_LABELS,CHAPTER_SOURCE_LABELS} from '../utils/chapters.js';
import {newRequestID} from '../utils/requestID.js';
export default {
  name:'ChapterPanel',components:{AutomaticStructurePanel,DialogShell,ChapterEditorDialog,ChapterDraftDialog},props:{videoId:{type:Number,required:true},pageActive:{type:Boolean,default:true},initialJobId:{type:Number,default:0},api:{type:Object,default:()=>chapterAPI}},emits:['seek','subtitle-tools','settings'],
  data:()=>({sets:null,error:'',notice:'',busy:false,generation:0,setsRequest:0,jobRequest:0,draftRequest:0,alive:true,editor:null,draft:null,jobs:[],jobCursor:0,jobsLoading:false,jobsDirty:false,jobError:'',labels:CHAPTER_JOB_LABELS,sourceLabels:CHAPTER_SOURCE_LABELS,whole:true,scopeStart:'0',scopeEnd:'',preparation:null,confirmed:false,startError:'',requestID:'',poll:null}),
  watch:{videoId:{immediate:true,handler(){this.generation++;this.jobRequest++;this.jobsLoading=false;this.jobsDirty=false;this.sets=null;this.editor=null;this.draft=null;this.preparation=null;this.jobs=[];this.jobCursor=0;this.load();this.loadJobs(false);if(this.initialJobId)this.openJob(this.initialJobId);}},initialJobId(id){if(id)this.openJob(id);},pageActive(active){if(!active){this.generation++;this.draftRequest++;this.preparation=null;}else{this.loadJobs(false);}}},
  mounted(){this._off=window.runtime?.EventsOn?.('chapter-draft-changed',()=>{if(this.pageActive){this.loadJobs(false);if(this.draft&&['queued','running'].includes(this.draft.job.status))this.openJob(this.draft.job.id,true);}});},
  beforeUnmount(){this.alive=false;this.generation++;this.draftRequest++;clearInterval(this.poll);this._off?.();},
  methods:{
    time:chapterTime,
    async load(){const request=++this.setsRequest,id=this.videoId;this.error='';try{const sets=await this.api.sets(id);if(this.alive&&request===this.setsRequest&&id===this.videoId)this.sets=sets;}catch(error){if(this.alive&&request===this.setsRequest)this.error=error.message||String(error);}},
    async loadJobs(more){if(this.jobsLoading){if(!more)this.jobsDirty=true;return;}if(!this.videoId)return;const ticket=++this.jobRequest,id=this.videoId;this.jobsLoading=true;this.jobError='';try{const page=await this.api.jobs({video_id:id,cursor_id:more?this.jobCursor:0,limit:20});if(!this.alive||ticket!==this.jobRequest||id!==this.videoId)return;const byID=new Map(this.jobs.map(job=>[job.id,job]));for(const job of page.items)byID.set(job.id,job);this.jobs=[...byID.values()].sort((a,b)=>b.id-a.id);if(more||!this.jobCursor)this.jobCursor=page.next_cursor;}catch(error){if(this.alive&&ticket===this.jobRequest)this.jobError=error.message||String(error);}finally{if(this.alive&&ticket===this.jobRequest){this.jobsLoading=false;if(this.jobsDirty&&this.pageActive){this.jobsDirty=false;this.loadJobs(false);}}}},
    closeDraft(){this.draft=null;this.draftRequest++;},
    async openJob(id,refresh=false){const ticket=++this.draftRequest,generation=this.generation;if(!this.pageActive)return;try{const draft=await this.api.draft(id);if(this.alive&&ticket===this.draftRequest&&generation===this.generation&&(!refresh||this.draft?.job.id===id)){this.draft=draft;this.jobs=this.jobs.map(job=>job.id===id?draft.job:job);}}catch(error){if(this.alive&&ticket===this.draftRequest)this.jobError=error.message||String(error);}},
    copyAutomatic(view){if(!this.pageActive||this.busy||!this.sets?.source_version||view.source_status!=='ready'||view.source_version!==this.sets.source_version)return;this.editor={videoID:this.videoId,revision:this.sets.manual?.revision||0,chapters:view.segments.map(row=>({id:0,start_ms:row.start_ms,end_ms:row.end_ms,title:row.title,summary:''})),sourceVersion:view.source_version,currentSource:this.sets.source_version,sourceStatus:'ready'};},
    edit(copy){if(!this.pageActive||this.busy||!this.sets?.source_version)return;const manual=this.sets.manual,source=copy?this.sets.embedded:manual;this.editor={videoID:this.videoId,revision:manual?.revision||0,chapters:(source?.chapters||[]).map(row=>({...row,id:copy?0:row.id})),sourceVersion:source?.source_version||this.sets.source_version,currentSource:this.sets.source_version,sourceStatus:source?.source_status||'ready'};},
    saved(){this.editor=null;this.notice='人工章节已保存';this.load();},changed(){this.load();this.loadJobs(false);},
    async resolve(set,chapter){const generation=this.generation;if(!this.pageActive||this.busy)return;this.busy=true;this.error='';try{const result=await this.api.resolve({set_id:set.set_id,chapter_id:chapter.id,revision:set.revision});if(this.alive&&generation===this.generation)this.$emit('seek',result);}catch(error){if(this.alive&&generation===this.generation)this.error=error.message||String(error);}finally{if(this.alive)this.busy=false;}},
    async probe(){if(!this.pageActive||this.busy)return;this.busy=true;this.error='';try{await this.api.probe(this.videoId);if(this.alive)await this.load();}catch(error){if(this.alive)this.error=error.message||String(error);}finally{if(this.alive)this.busy=false;}},
    async prepare(){if(!this.pageActive||this.busy)return;this.busy=true;this.error='';const generation=this.generation;try{let scope={whole:true,start_ms:0,end_ms:0};if(!this.whole){const [range]=chapterEditorInputs([{startSeconds:this.scopeStart,endSeconds:this.scopeEnd,title:'范围'}]);scope={whole:false,start_ms:range.start_ms,end_ms:range.end_ms};}const prep=await this.api.prepare({video_id:this.videoId,scope});if(this.alive&&generation===this.generation){this.preparation=prep;this.confirmed=false;this.startError='';this.requestID=newRequestID();}}catch(error){if(this.alive&&generation===this.generation)this.error=error.message||String(error);}finally{if(this.alive)this.busy=false;}},
    async start(){if(!this.pageActive||this.busy||!this.preparation||!this.confirmed)return;this.busy=true;this.startError='';const generation=this.generation,p=this.preparation;try{const result=await this.api.start({request_id:this.requestID,video_id:p.video_id,source_version:p.source_version,subtitle_fingerprint:p.subtitle_fingerprint,expected_chapter_revision:p.expected_chapter_revision,provider_revision:p.provider_revision,scope:p.scope,confirmed_scope:true});if(this.alive&&generation===this.generation){this.preparation=null;this.notice=result.status==='job_compacted'?'这次请求已处理，请重新核对范围后开始新任务。':'生成任务已提交，可在此处或任务中心查看。';await this.loadJobs(false);await this.openJob(result.job_id);}}catch(error){if(this.alive&&generation===this.generation)this.startError=error.message||String(error);}finally{if(this.alive)this.busy=false;}}
  }
};
</script>
<style scoped>
.chapter-panel { display: grid; gap: var(--space-3); }
.chapter-toolbar { display: flex; flex-wrap: wrap; gap: var(--space-2); align-items: center; }
.chapter-sources { display: grid; grid-template-columns: repeat(auto-fit,minmax(260px,1fr)); gap: var(--space-4); }
.chapter-summary { white-space: pre-wrap; overflow-wrap: anywhere; }
.chapter-generation { padding-block: var(--space-3); border-block: 1px solid var(--hairline); }
</style>
