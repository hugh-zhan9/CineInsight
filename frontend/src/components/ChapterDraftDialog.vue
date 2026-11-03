<template>
  <DialogShell v-if="pageActive" title="核对章节摘要" size="lg" :busy="busy" @close="$emit('close')">
    <p>{{labels[draft.job.status] || draft.job.status}} · {{draft.job.whole ? '全片字幕摘要' : '局部字幕摘要'}} · {{time(draft.job.scope_start_ms)}} — {{time(draft.job.scope_end_ms)}}</p>
    <p class="help-text">摘要仅依据字幕，可能遗漏无对白画面；请核对证据后采纳。展开摘要不会改变已看状态。取消将停止后续请求，已发送的字幕无法撤回。</p>
    <p v-if="draft.job.error_code" role="alert">{{errors[draft.job.error_code] || '这次生成未能完成，原人工章节保留。'}}</p>
    <ol><li v-for="(chapter,index) in draft.chapters" :key="index"><strong>{{chapter.title}}</strong> · {{time(chapter.start_ms)}} — {{time(chapter.end_ms)}}<details><summary>查看摘要与证据</summary><p class="chapter-summary">{{chapter.summary}}</p><button v-for="cue in chapter.evidence" :key="cue.cue_id" type="button" class="btn btn--ghost btn--sm" :disabled="busy || draft.job.status!=='draft_ready'" @click="$emit('seek',{video_id:draft.job.video_id,start_ms:cue.start_ms,source_version:draft.source_version})">字幕 {{cue.cue_id}} · {{time(cue.start_ms)}}</button></details></li></ol>
    <section v-if="application" class="chapter-application">
      <h3>应用范围核对</h3><p>移除 {{application.remove_ids.length}} 章，新增 {{application.additions.length}} 章，保留 {{application.retained}} 章。</p>
      <p v-if="application.conflicts.length" role="alert">以下原章节跨越本次范围。请先编辑边界，或明确扩大范围重新生成；本次不会拆分或删除它们。</p>
      <ul><li v-for="chapter in application.conflicts" :key="chapter.id">{{chapter.title}} · {{time(chapter.start_ms)}} — {{time(chapter.end_ms)}}</li></ul>
      <ol><li v-for="(chapter,index) in application.additions" :key="index">{{chapter.title}} · {{time(chapter.start_ms)}} — {{time(chapter.end_ms)}}</li></ol>
      <p v-if="application.apply_token && expired" role="alert">应用预览已过期，请重新核对。</p>
      <button v-if="application.apply_token" type="button" class="btn btn--primary" :disabled="busy || expired" @click="apply">按以上范围采纳为人工章节</button>
    </section>
    <p v-if="error" role="alert">{{error}}</p>
    <template #footer>
      <button type="button" class="btn btn--secondary" :disabled="busy" @click="$emit('refresh')">刷新状态</button>
      <button v-if="['queued','running'].includes(draft.job.status)" type="button" class="btn btn--secondary" :disabled="busy" @click="mutate('cancel')">取消生成</button>
      <template v-if="draft.job.status==='draft_ready'"><button type="button" class="btn btn--danger" :disabled="busy" @click="mutate('discard')">放弃草稿</button><button type="button" class="btn btn--primary" :disabled="busy" @click="prepareApplication">预览应用范围</button></template>
    </template>
  </DialogShell>
</template>
<script>
import DialogShell from './ui/DialogShell.vue';
import {chapterAPI,chapterTime,CHAPTER_JOB_LABELS,CHAPTER_ERROR_LABELS} from '../utils/chapters.js';
import {confirmAction} from '../utils/feedback.js';
export default {
  name:'ChapterDraftDialog',components:{DialogShell},props:{draft:{type:Object,required:true},pageActive:{type:Boolean,default:true},api:{type:Object,default:()=>chapterAPI}},emits:['close','refresh','changed','seek'],
  data:()=>({labels:CHAPTER_JOB_LABELS,errors:CHAPTER_ERROR_LABELS,busy:false,applying:false,error:'',application:null,now:Date.now(),timer:null,generation:0,alive:true}),
  computed:{expired(){return !(Date.parse(this.application?.expires_at)>this.now);}},
  watch:{pageActive(active){if(!active){this.generation++;this.dropPreview();}},'draft.job.revision'(){this.generation++;this.dropPreview();}},
  beforeUnmount(){this.alive=false;this.generation++;this.dropPreview();},
  methods:{
    time:chapterTime,
    dropPreview(){clearTimeout(this.timer);const token=this.application?.apply_token;this.application=null;if(token&&!this.applying)this.api.discardPreview(token).catch(()=>{});},
    async prepareApplication(){if(this.busy||!this.pageActive)return;this.dropPreview();const ticket=++this.generation;this.busy=true;this.error='';try{const preview=await this.api.preview({job_id:this.draft.job.id,expected_job_revision:this.draft.job.revision,expected_chapter_revision:this.draft.expected_chapter_revision});if(!this.alive||ticket!==this.generation){if(preview.apply_token)this.api.discardPreview(preview.apply_token).catch(()=>{});return;}this.application=preview;this.now=Date.now();const delay=Date.parse(preview.expires_at)-this.now;if(delay>0)this.timer=setTimeout(()=>{this.now=Date.now();},Math.min(delay,2147483647));}catch(error){if(this.alive&&ticket===this.generation)this.error=error.message||String(error);}finally{if(this.alive)this.busy=false;}},
    async apply(){this.now=Date.now();if(this.busy||!this.pageActive||!this.application?.apply_token||this.expired)return;this.busy=true;this.applying=true;this.error='';try{await this.api.apply(this.application.apply_token);if(this.alive){this.$emit('changed');this.$emit('close');}}catch(error){if(this.alive)this.error=error.message||String(error);}finally{this.applying=false;if(this.alive)this.busy=false;}},
    async mutate(action){if(this.busy||!this.pageActive)return;const ticket=this.generation,id=this.draft.job.id,revision=this.draft.job.revision;if(action==='discard'&&!await confirmAction({title:'放弃章节草稿',message:'删除这份未采纳的摘要正文？已有人工章节不变。',confirmText:'放弃草稿',danger:true}))return;if(!this.alive||!this.pageActive||ticket!==this.generation)return;this.busy=true;this.error='';try{await this.api[action](id,revision);if(this.alive){this.$emit('changed');this.$emit('refresh');}}catch(error){if(this.alive)this.error=error.message||String(error);}finally{if(this.alive)this.busy=false;}}
  }
};
</script>
<style scoped>
.chapter-summary { white-space: pre-wrap; overflow-wrap: anywhere; }
.chapter-application { border-top: 1px solid var(--hairline); padding-top: var(--space-3); }
li { margin-block: var(--space-2); }
</style>
