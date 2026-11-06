<template>
  <section class="automatic-structure" data-test="automatic-structure">
    <h4>自动分段</h4>
    <p class="help-text">本机画面分析生成，不是剧情摘要。人工章节和原文件章节保持不变。</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-else role="status">{{ statusText }}</p>
    <div class="automatic-structure__actions"><button type="button" class="btn btn--secondary btn--sm" :disabled="busy" @click="load">刷新</button><button v-if="view && ['failed','unavailable','waiting_idle'].includes(view.status)" type="button" class="btn btn--secondary btn--sm" :disabled="busy" @click="retry">重新排队</button><button v-if="view?.segments?.length" type="button" class="btn btn--secondary btn--sm" :disabled="busy || view.source_status !== 'ready'" @click="$emit('copy', view)">复制到人工编辑稿</button></div>
    <ol v-if="view?.segments?.length"><li v-for="segment in view.segments" :key="segment.position"><button type="button" class="btn btn--ghost btn--sm" :disabled="busy || view.source_status !== 'ready'" @click="seek(segment)"><img v-if="view.source_status === 'ready'" :src="`/preview/frame/${videoId}?ms=${segment.representative_ms}&w=160&source_version=${encodeURIComponent(view.source_version)}`" alt="" loading="lazy" decoding="async" />{{ segment.title }} · {{ time(segment.start_ms) }}</button></li></ol>
  </section>
</template>
<script>
import { GetVideoStructure, RetryVideoStructure, ResolveVideoStructure } from '../../wailsjs/go/main/App';
import { chapterTime } from '../utils/chapters.js';
import { runtimeEventsMixin } from './video-list/runtimeEvents.js';
export default {
  name: 'AutomaticStructurePanel', mixins: [runtimeEventsMixin], props: { videoId: Number, pageActive: { type: Boolean, default: true } }, emits: ['seek','copy'],
  data: () => ({ view: null, error: '', busy: false, generation: 0 }),
  computed: { statusText() { if (this.view?.source_status === 'unavailable') return '原片当前不可用，保留已生成分段'; if (this.view?.source_status === 'changed') return '原片已变化，旧分段不能定位，正在等待重新整理'; return ({pending:'等待空闲处理',running:'正在整理',waiting_idle:'等待空闲继续',ready:'自动分段已准备',failed:'分析失败，可重新排队',unavailable:'原片或本机组件暂不可用'})[this.view?.status] || '正在读取'; } },
  watch: { videoId() { this.generation++; this.view = null; this.load(); }, pageActive(active) { this.generation++; if (active) this.load(); } },
  mounted() { this.load(); this.registerRuntimeEvent('video-structure-state', () => { if (!this.pageActive || this._refresh) return; this._refresh = setTimeout(() => { this._refresh = null; this.load(); }, 1000); }); },
  beforeUnmount() { this.generation++; this._closed = true; clearTimeout(this._refresh); },
  methods: {
    time: chapterTime,
    async load() { if (!this.videoId || !this.pageActive || this._closed) return; if (this.busy) { this._refreshPending = true; return; } const generation = ++this.generation; try { const view = await GetVideoStructure(this.videoId); if (generation === this.generation && !this._closed) { this.view = view; this.error = ''; } } catch (error) { if (generation === this.generation && !this._closed) this.error = String(error?.message || error); } },
    finishAction() { this.busy = false; if (this._refreshPending) { this._refreshPending = false; this.load(); } },
    async retry() { if (this.busy || !this.view) return; this.busy = true; const generation = this.generation; try { await RetryVideoStructure(this.videoId,this.view.revision); this._refreshPending = true; } catch (error) { if (generation === this.generation && !this._closed) this.error = String(error?.message || error); } finally { this.finishAction(); } },
    async seek(segment) { if (this.busy || this.view?.source_status !== 'ready') return; this.busy = true; const generation = this.generation; try { const result = await ResolveVideoStructure(this.videoId,this.view.generation,segment.position,this.view.source_version); if (generation === this.generation && this.pageActive && !this._closed) this.$emit('seek',result); } catch (error) { if (generation === this.generation && !this._closed) this.error = String(error?.message || error); } finally { this.finishAction(); } }
  }
};
</script>
<style scoped>.automatic-structure img { width: 120px; aspect-ratio: 16 / 9; object-fit: contain; margin-right: var(--space-2); }.automatic-structure__actions { display: flex; flex-wrap: wrap; gap: var(--space-2); }</style>
