<template>
  <Teleport to="body"><DialogShell title="确认区间预览" size="lg" :busy="busy" @close="$emit('close')">
    <p>{{ preview.topic_title }} · {{ preview.items.length }} 个片段；按此顺序预览，最后一段结束即停止。预览不会写入观看次数、断点或日记。</p>
    <p v-if="preview.will_interrupt" class="help-text">确认后会切换当前队列播放，原队列条目仍保留。</p><p v-if="preview.notice" role="status">{{ preview.notice }}</p>
    <ol :start="page * 50 + 1"><li v-for="item in shownItems" :key="`${item.bookmark_id}-${item.video_id}-${item.start_ms}`">{{ item.title }} · {{ time(item.start_ms) }} — {{ item.end_ms > item.start_ms ? time(item.end_ms) : '结束时间待补充' }}<p v-if="item.reason" role="alert">{{ item.reason }}</p></li></ol>
    <div v-if="preview.items.length > 50" class="clip-preview-pages"><button type="button" class="btn btn--ghost btn--sm" :disabled="page === 0" @click="page--">上一页</button><span>第 {{ page + 1 }} / {{ Math.ceil(preview.items.length / 50) }} 页</span><button type="button" class="btn btn--ghost btn--sm" :disabled="(page + 1) * 50 >= preview.items.length" @click="page++">下一页</button></div>
    <p v-if="preview.conflicts" role="alert">有 {{ preview.conflicts }} 项需要处理，本次不会跳过它们或部分加入。</p>
    <label v-if="preview.requires_inline"><input v-model="switchToInline" type="checkbox" :disabled="busy" data-test="clip-switch-inline" />明确切换为应用内播放器并预览</label>
    <p v-if="expired && !preview.conflicts" role="alert">预览已过期，请返回重新预览。</p><p v-if="error" role="alert">{{ error }}</p>
    <template #footer><button type="button" class="btn btn--secondary" :disabled="busy" @click="$emit('close')">返回核对</button><button type="button" class="btn btn--primary" :disabled="!canSubmit" data-test="clip-preview-confirm" @click="submit">加入队列并预览</button></template>
  </DialogShell></Teleport>
</template>
<script>
import DialogShell from './ui/DialogShell.vue';
import { clipTopicAPI, clipError, clipTime } from '../utils/clipTopics.js';
export default {
  name: 'ClipQueuePreviewDialog', components: { DialogShell }, props: { preview: { type: Object, required: true }, api: { type: Object, default: () => clipTopicAPI } }, emits: ['close', 'queued'],
  data: () => ({ switchToInline: false, page: 0, busy: false, error: '', now: Date.now(), timer: null, alive: true }),
  computed: { shownItems() { return this.preview.items.slice(this.page * 50, (this.page + 1) * 50); }, expired() { return !(Date.parse(this.preview.expires_at) > this.now); }, canSubmit() { return !this.busy && !this.expired && !this.preview.conflicts && Boolean(this.preview.preview_token) && (!this.preview.requires_inline || this.switchToInline); } },
  mounted() { const delay = Date.parse(this.preview.expires_at) - Date.now(); if (delay > 0) this.timer = setTimeout(() => { this.now = Date.now(); }, Math.min(delay, 2147483647)); },
  beforeUnmount() { this.alive = false; clearTimeout(this.timer); if (!this.busy && this.preview.preview_token) this.api.discard(this.preview.preview_token).catch(() => {}); },
  methods: { time: clipTime, async submit() {
    this.now = Date.now(); if (!this.canSubmit) return;
    this.busy = true; this.error = '';
    try { const result = await this.api.append({ preview_token: this.preview.preview_token, switch_to_inline: this.switchToInline }); if (this.alive) this.$emit('queued', result); }
    catch (error) { if (this.alive) this.error = clipError(error); }
    finally { if (this.alive) this.busy = false; }
  } }
};
</script>
<style scoped>
.clip-preview-pages { display: flex; align-items: center; gap: var(--space-3); }
</style>
