<template>
  <Teleport to="body">
    <DialogShell title="先试一段，再决定是否增强整片" size="full" class="enhancement-preview" data-test="enhancement-preview" @close="close">
      <div class="enhancement-preview__controls">
        <strong>{{ video.name }}</strong>
        <label>起点（秒）<input v-model.number="start" class="number-input" type="number" min="0" step="0.1" :disabled="busy" /></label>
        <label>样片长度<select v-model.number="seconds" class="select-input" :disabled="busy"><option :value="5">5秒</option><option :value="10">10秒</option><option v-if="video.duration > 0 && video.duration < 5" :value="video.duration">整段 {{ video.duration }} 秒</option></select></label>
        <span>{{ profile === 'anime' ? '动漫' : '普通真人' }} · 2× · 仅比较画面</span>
        <button type="button" class="btn btn--primary" :disabled="busy || !source" data-test="enhancement-preview-start" @click="run">{{ busy ? result?.state === 'queued' ? '等待处理资源…' : '正在试处理…' : '生成对照样片' }}</button>
        <button v-if="busy" type="button" class="btn btn--secondary" @click="cancel">取消</button>
        <button type="button" class="btn btn--secondary" :disabled="busy || result?.state !== 'ready'" data-test="enhancement-preview-full" @click="createFull">按此模型增强整片</button>
      </div>
      <AppBanner v-if="error" tone="danger" :title="error" />
      <div v-if="result?.state === 'ready' && result.result_url" class="enhancement-preview__panes">
        <section><h3>原片样片</h3><video ref="original" :src="result.comparison_url" controls playsinline muted preload="auto" @play="play" @pause="onPause" @seeking="onSeeking('original')" @seeked="onSeeked('original')" @waiting="onWaiting('original')" @stalled="onWaiting('original')" @canplay="onReady('original')" @timeupdate="align" @ended="pause" @error="mediaError('原片对照')" /></section>
        <section><h3>增强样片</h3><video ref="enhanced" :src="result.result_url" playsinline muted preload="auto" @seeking="onSeeking('enhanced')" @seeked="onSeeked('enhanced')" @waiting="onWaiting('enhanced')" @stalled="onWaiting('enhanced')" @canplay="onReady('enhanced')" @error="mediaError('增强样片')" /></section>
      </div>
      <p v-else class="enhancement-preview__empty">选择想检查的片段，先比较人物、文字或纹理细节。试处理不会改动原片或写观看记录。</p>
    </DialogShell>
  </Teleport>
</template>
<script>
import { GetMediaProcessing } from '../../wailsjs/go/main/App';
import DialogShell from './ui/DialogShell.vue';
import AppBanner from './ui/AppBanner.vue';
import { createMediaProcessingController, mediaProcessingError } from '../utils/mediaProcessing.js';
import { compareDriftSeek, compareFollowerTarget } from '../utils/versionCompare.js';
export default {
  name: 'VideoEnhancementPreview', components: { DialogShell, AppBanner },
  props: { video: { type: Object, required: true }, profile: { type: String, required: true } }, emits: ['close', 'create-full'],
  data() { return { start: 0, seconds: this.video.duration > 0 ? Math.min(5, this.video.duration) : 5, source: null, result: null, starting: false, error: '', playing: false, failedPlayback: false, waiting: { original: false, enhanced: false }, seeking: { original: false, enhanced: false } }; },
  computed: { busy() { return this.starting || ['queued', 'running'].includes(this.result?.state); } },
  async mounted() {
    this._source = createMediaProcessingController(() => {}); this._controller = createMediaProcessingController(view => { this.result = view; if (view.state === 'failed') this.error = view.error; });
    try { this.source = await this._source.start({ kind: 'video', media_id: Number(this.video.id), operation: 'inspect' }); } catch (error) { this.error = mediaProcessingError(error); }
  },
  beforeUnmount() { this._closed = true; this.pause(); for (const video of [this.$refs.original, this.$refs.enhanced]) { video?.removeAttribute('src'); video?.load(); } this._controller?.close().catch(() => {}); this._source?.close().catch(() => {}); },
  methods: {
    pauseMedia() { for (const video of [this.$refs.original, this.$refs.enhanced]) if (video && !video.paused) video.pause(); },
    pause() { this.playing = false; this.pauseMedia(); },
    syncBusy() { return Object.values(this.waiting).some(Boolean) || Object.values(this.seeking).some(Boolean); },
    onPause() { if (!this.syncBusy()) this.pause(); },
    onWaiting(side) { this.waiting[side] = true; this.pauseMedia(); },
    onReady(side) { this.waiting[side] = false; this.resume(); },
    onSeeking(side) { this.seeking[side] = true; this.pauseMedia(); if (side === 'original') this.seek(); },
    onSeeked(side) { this.seeking[side] = false; this.resume(); },
    mediaError(label) { this.failedPlayback = true; this.pause(); this.error = `${label}未能加载，请重新试处理`; },
    seek() {
      const left = this.$refs.original, right = this.$refs.enhanced;
      if (!left || !right || right.readyState < 1) return;
      const target = compareFollowerTarget(left.currentTime, 0, right.duration).time;
      if (Math.abs(right.currentTime - target) > 0.001) { this.seeking.enhanced = true; this.pauseMedia(); right.currentTime = target; }
    },
    align() {
      const left = this.$refs.original, right = this.$refs.enhanced;
      if (this.syncBusy() || this.failedPlayback || !left || !right || right.readyState < 2) return;
      if (!left.seeking && !right.seeking && compareDriftSeek({ leaderTime: left.currentTime, followerTime: right.currentTime, followerDuration: right.duration }) != null) this.seek();
    },
    async resume() {
      if (!this.playing || this._closed || this.failedPlayback || this.syncBusy()) return;
      const videos = [this.$refs.original, this.$refs.enhanced];
      if (videos.some(video => !video || video.readyState < 2)) return;
      this.align(); if (this.syncBusy()) return;
      try { await Promise.all(videos.filter(video => video.paused && !video.ended).map(video => video.play())); }
      catch (error) { if (error?.name !== 'AbortError' && !this._closed) { this.pause(); this.error = '无法同步播放：' + error; } }
    },
    async play() { if (this.failedPlayback) { this.pause(); return; } this.playing = true; if (this.$refs.enhanced?.readyState < 2) this.onWaiting('enhanced'); await this.resume(); },
    async cancel() { this.pause(); this.$refs.original?.pause(); this._controller.invalidate(); try { if (this.result?.id) await this._controller.release(this.result.id); this.result = null; this.waiting = { original: false, enhanced: false }; this.seeking = { original: false, enhanced: false }; this.failedPlayback = false; } catch (error) { this.error = mediaProcessingError(error); } },
    async run() { if (!this.source || this.busy) return; if (!Number.isFinite(this.start) || this.start < 0 || !Number.isFinite(this.seconds) || this.seconds <= 0 || this.seconds > 10) { this.error = '请输入合法的起点和不超过10秒的长度'; return; } this.starting = true; this.error = ''; try { await this.cancel(); await this._controller.start({ kind: 'video', media_id: Number(this.video.id), parent_id: this.source.id, operation: 'sample', profile: this.profile, start_seconds: this.start, duration_seconds: this.seconds }); } catch (error) { this.error = mediaProcessingError(error); } finally { this.starting = false; } },
    async createFull() { if (this.busy || this.result?.state !== 'ready') return; try { await GetMediaProcessing(this.source.id); if (await this.close()) this.$emit('create-full'); } catch (error) { this.error = mediaProcessingError(error); } },
    async close() { this.pause(); this.$refs.original?.pause(); try { await this._controller.close(); await this._source.close(); this.$emit('close'); return true; } catch (error) { this.error = '试处理尚未停止，请重试关闭：' + error; return false; } }
  }
};
</script>
<style scoped>
:deep(.enhancement-preview .ui-dialog__body) { display: flex; flex-direction: column; min-height: 0; }
.enhancement-preview__controls { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; margin-bottom: 16px; }
.enhancement-preview__controls label { display: flex; align-items: center; gap: 6px; }
.enhancement-preview__controls input { width: 110px; }
.enhancement-preview__controls span { color: var(--text-secondary); font-size: var(--fs-caption); }
.enhancement-preview__panes { flex: 1; display: grid; grid-template-columns: 1fr 1fr; min-height: 0; gap: 12px; }
.enhancement-preview__panes section { display: flex; flex-direction: column; min-height: 0; min-width: 0; }
.enhancement-preview__panes h3 { font-size: var(--fs-secondary); }
.enhancement-preview__panes video { flex: 1; width: 100%; min-height: 0; background: var(--overlay-strong); object-fit: contain; }
.enhancement-preview__empty { margin: auto; color: var(--text-muted); }
</style>
