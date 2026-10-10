<template>
  <div v-show="visible" class="ai-review-batch-status">
    <p v-if="preparing" role="status">正在预览本次批准范围…</p>
    <p v-if="state.token" role="status" data-test="review-batch-summary">
      {{ stateLabel }} · 已处理 {{ state.processed || 0 }} / {{ state.total || 0 }} · 成功 {{ state.succeeded || 0 }} · 跳过 {{ state.skipped || 0 }} · 失败 {{ state.failed || 0 }}
      <span v-if="state.state !== 'running' && state.remaining"> · 未处理 {{ state.remaining }}，需要重新预览确认</span>
      <button v-if="state.state === 'running'" type="button" class="btn-secondary btn-compact" data-test="review-batch-cancel" @click="cancelBatch">取消剩余</button>
    </p>
    <p v-if="state.skipped" class="help-text">已变化或失效的候选未被批准；可刷新候选列表重新审阅。</p>
    <p v-if="notice" role="status" data-test="review-batch-notice">{{ notice }}</p>
    <p v-if="error" class="error-text" role="alert" data-test="review-batch-error">{{ error }} <button type="button" class="btn-secondary btn-compact" @click="refresh">刷新批次状态</button></p>
    <details v-if="failures.length"><summary>未批准的候选（展示前 {{ failures.length }} 项）</summary><ul><li v-for="item in failures" :key="item.id">候选 #{{ item.id }}：{{ reasonLabel(item.code) }}</li></ul></details>
  </div>
</template>

<script>
import { PreviewAIReviewApproval, StartAIReviewApproval, GetAIReviewApproval, CancelAIReviewApproval } from '../../wailsjs/go/main/App';
import { confirmAction } from '../utils/feedback.js';

export default {
  name: 'AIReviewBatchControls',
  props: { kind: { type: String, required: true }, visible: { type: Boolean, default: true }, query: { type: Object, default: () => ({}) } },
  emits: ['busy', 'outcomes', 'settled'],
  data: () => ({ state: {}, preparing: false, draining: false, error: '', notice: '', failures: [], after: 0, generation: 0, pollSequence: 0, timer: null, disposed: false, settledToken: '' }),
  computed: {
    busy() { return this.preparing || this.draining || this.state.state === 'running'; },
    stateLabel() { return { running: '正在批准', completed: '本批已处理完', cancelled: '已取消剩余操作', failed: '批次已中止' }[this.state.state] || '审阅批次'; }
  },
  watch: {
    busy: { immediate: true, handler(value) { this.$emit('busy', value); } },
    visible(value) { if (value) this.refresh(); else this.stopPolling(); }
  },
  mounted() { if (this.visible) this.refresh(); },
  beforeUnmount() { this.disposed = true; this.stopPolling(); },
  methods: {
    reasonLabel(code) { return { changed: '候选已变化，请刷新后重新审阅', unavailable: '媒体或标签已不可用', not_pending: '已不是可批准的待审候选', approval_failed: '批准失败，候选仍需检查' }[code] || '未能批准，请重新审阅'; },
    stopPolling() { clearTimeout(this.timer); ++this.generation; },
    refresh() {
      this.stopPolling();
      this.after = 0; this.state = {}; this.failures = []; this.error = ''; this.settledToken = '';
      return this.poll(this.generation);
    },
    async poll(generation) {
      if (this.disposed || !this.visible || generation !== this.generation) return;
      clearTimeout(this.timer);
      const sequence = ++this.pollSequence;
      try {
        const result = await GetAIReviewApproval(this.kind, this.state.token || '', this.after, 200);
        if (this.disposed || !this.visible || generation !== this.generation || sequence !== this.pollSequence) return;
        if (!result || !result.token) { this.state = {}; this.draining = false; return; }
        this.state = result;
        const outcomes = Array.isArray(result.results) ? result.results : [];
        if (outcomes.length) {
          this.$emit('outcomes', outcomes);
          this.failures.push(...outcomes.filter(item => item.state !== 'approved').slice(0, Math.max(0, 50 - this.failures.length)));
        }
        this.after = Number(result.next_after || 0);
        this.draining = !!result.has_more;
        if (result.state === 'failed') this.error = '批次已中止，请检查运行状态；未处理项需要重新预览。';
        if (result.has_more || result.state === 'running') {
          this.timer = setTimeout(() => this.poll(generation), result.has_more ? 0 : 1000);
        } else if (this.settledToken !== result.token) {
          this.settledToken = result.token;
          this.$emit('settled', result);
        }
      } catch (err) {
        if (generation === this.generation && sequence === this.pollSequence && !this.disposed) { this.draining = false; this.error = '读取批次状态失败：' + err; }
      }
    },
    previewLoaded(ids, title = '') { return this.preview('loaded', [...ids], title); },
    previewFiltered() { return this.preview('filtered', []); },
    async releasePreview(token) {
      if (!token) return;
      try { await CancelAIReviewApproval(this.kind, token); }
      catch (_err) { if (!this.disposed) this.error = '未能释放预览，请稍后重试；预览将在 10 分钟后失效。'; }
    },
    async preview(scope, ids, title = '') {
      if (this.busy || this.disposed || !this.visible) return;
      this.stopPolling();
      const generation = this.generation;
      const query = { ...this.query };
      this.preparing = true; this.error = ''; this.notice = '';
      let preview;
      try {
        preview = await PreviewAIReviewApproval(this.kind, { scope, ids, filter: query });
        if (this.disposed || !this.visible || generation !== this.generation) { await this.releasePreview(preview?.token); return; }
        if (!preview?.eligible || !preview.token) { this.notice = `本次匹配 ${preview?.matched || 0} 条，没有可批准的待审候选。`; return; }
        const range = scope === 'loaded' ? `已加载${title ? `「${title}」` : ''}的结果` : '全部筛选结果（包括未加载的候选）';
        const confirmed = await confirmAction({ title: '确认批量批准', message: `范围：${range}。本次匹配 ${preview.matched} 条，其中可批准 ${preview.eligible} 条，涉及 ${preview.media_count} 个文件、${preview.link_count} 个标签关联；排除 ${preview.excluded} 条不可批准项。只处理本次预览集合，新增或变化的候选不会作为本批新成员。`, confirmText: `批准 ${preview.eligible} 条` });
        if (!confirmed || this.disposed || !this.visible || generation !== this.generation) { await this.releasePreview(preview.token); return; }
        const state = await StartAIReviewApproval(this.kind, preview.token);
        if (this.disposed || generation !== this.generation) return;
        this.state = state; this.after = 0; this.failures = []; this.settledToken = '';
        await this.poll(generation);
      } catch (err) {
        if (!this.disposed && generation === this.generation) this.error = '批量批准未能开始：' + err;
      } finally {
        this.preparing = false;
      }
    },
    async cancelBatch() {
      try { await CancelAIReviewApproval(this.kind, this.state.token); await this.poll(this.generation); }
      catch (err) { this.error = '取消剩余操作失败：' + err; }
    }
  }
};
</script>

<style scoped>
.ai-review-batch-status { flex: 0 0 auto; font-size: 12px; }
.ai-review-batch-status p { margin: 6px 0; }
.ai-review-batch-status details { max-height: 160px; overflow: auto; }
.error-text { color: var(--danger-color); }
</style>
