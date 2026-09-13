<template>
  <Teleport to="body">
    <BaseModal v-if="!videoSource && !imageSource" class="face-source-dialog" aria-label="人脸来源详情" @close="$emit('close')">
      <div class="face-source-dialog__header">
        <h3>{{ cluster.person_name || '待命名人脸' }} · 来源详情</h3>
        <button type="button" class="btn-secondary btn-compact" @click="$emit('close')">关闭</button>
      </div>
      <p>查看这张脸出现的原图或视频，确认身份后再命名。视频会跳到人脸出现的位置。</p>
      <p v-if="cluster.status === 'unnamed'">识别有误的来源可逐条移除，原图片和视频会保留。</p>
      <p v-if="error" role="alert">{{ error }}</p>
      <p v-if="removeError" role="alert">{{ removeError }}</p>
      <div class="face-source-dialog__list">
        <article v-for="source in sources" :key="source.observation_id" class="face-source-dialog__row">
          <img v-if="!cropFailed[source.observation_id]" :src="`/preview/face-crop/${source.observation_id}`" alt="此处识别到的人脸" loading="lazy" @error="cropFailed[source.observation_id] = true" />
          <span v-else class="face-source-dialog__fallback">人脸截图不可用</span>
          <div>
            <strong>{{ source.name || '原媒体不可用' }}</strong>
            <p>{{ source.media_kind === 'video' ? '视频' : '图片' }} · {{ formatBytes(source.size) }}<template v-if="source.media_kind === 'video' && source.frame_ms >= 0"> · {{ timestamp(source.frame_ms) }}</template></p>
            <p class="face-source-dialog__path">{{ source.path }}</p>
            <span v-if="source.unavailable">{{ source.unavailable }}</span>
            <button v-else type="button" class="btn-secondary btn-compact" :data-test="`face-source-open-${source.observation_id}`" @click="openSource(source)">{{ source.media_kind === 'video' ? '查看视频片段' : '查看原图' }}</button>
            <button v-if="cluster.status === 'unnamed'" type="button" class="btn-secondary btn-compact face-source-dialog__remove" :disabled="loading || removingID !== null" :data-test="`face-source-remove-${source.observation_id}`" @click="removeSource(source)">{{ removingID === source.observation_id ? '移除中…' : '移除' }}</button>
          </div>
        </article>
        <p v-if="!loading && !sources.length && !error">暂无可查看的图片或视频来源。</p>
      </div>
      <button v-if="nextID || error" type="button" class="btn-secondary" :disabled="loading || removingID !== null" data-test="face-source-more" @click="loadMore">{{ error ? '重试' : '加载更多来源' }}</button>
      <p v-if="loading" role="status">正在加载来源…</p>
      <div v-if="cluster.status === 'unnamed'" class="face-source-dialog__actions">
        <button type="button" class="btn-primary" :disabled="removingID !== null" @click="$emit('name', cluster)">命名为新人物</button>
        <button type="button" class="btn-secondary" :disabled="removingID !== null" @click="$emit('link', cluster)">关联到现有人物</button>
      </div>
    </BaseModal>
    <div v-if="videoSource" class="face-source-video">
      <PreviewDrawer :initial-entity="{ type: 'video', id: videoSource.media_id }" :start-time-ms="videoSource.frame_ms >= 0 ? videoSource.frame_ms : null" @close="videoSource = null" @preview-externally="previewExternally" />
      <p v-if="videoError" class="face-source-video__error" role="alert">{{ videoError }}</p>
    </div>
  </Teleport>
  <ImageSourceDialog v-if="imageSource" :image="imageSource" @close="imageSource = null" />
</template>
<script>
import BaseModal from './ui/BaseModal.vue';
import PreviewDrawer from './PreviewDrawer.vue';
import ImageSourceDialog from './ImageSourceDialog.vue';
import { GetFaceClusterObservations, RemoveFaceClusterObservation, PreviewExternally } from '../../wailsjs/go/main/App';
import { formatBytes } from '../utils/mediaDetails.js';
import { feedbackState, resolveConfirm } from '../utils/feedback.js';
export default {
  name: 'FaceClusterDetailDialog', components: { BaseModal, PreviewDrawer, ImageSourceDialog },
  props: { cluster: { type: Object, required: true } }, emits: ['close', 'name', 'link', 'removed'],
  data: () => ({ sources: [], nextID: 0, loading: false, error: '', removeError: '', removingID: null, videoError: '', cropFailed: {}, videoSource: null, imageSource: null }),
  watch: { 'cluster.id': { immediate: true, handler() { this.reset(); } } },
  mounted() { window.addEventListener('keydown', this.escape, true); },
  beforeUnmount() { this._request = null; this._sourceSession = null; window.removeEventListener('keydown', this.escape, true); },
  methods: {
    formatBytes,
    timestamp(ms) { const seconds = Math.floor(ms / 1000); return `${Math.floor(seconds / 3600).toString().padStart(2, '0')}:${Math.floor(seconds / 60 % 60).toString().padStart(2, '0')}:${(seconds % 60).toString().padStart(2, '0')}`; },
    reset() { this._request = null; this._sourceSession = Symbol(); this.sources = []; this.nextID = 0; this.loading = false; this.error = ''; this.removeError = ''; this.removingID = null; this.cropFailed = {}; this.videoSource = null; this.imageSource = null; this.loadMore(); },
    async loadMore() {
      if (this.loading || this.removingID !== null) return;
      const request = Symbol(); this._request = request; this.loading = true; this.error = '';
      try {
        const page = await GetFaceClusterObservations(Number(this.cluster.id), this.nextID, 30);
        if (this._request !== request) return;
        this.sources.push(...(page?.observations || [])); this.nextID = Number(page?.next_id || 0);
      } catch (err) { if (this._request === request) this.error = `加载来源失败：${err}`; }
      finally { if (this._request === request) this.loading = false; }
    },
    async removeSource(source) {
      if (this.loading || this.removingID !== null || this.cluster.status !== 'unnamed') return;
      const session = this._sourceSession;
      this.removingID = source.observation_id; this.removeError = '';
      try {
        const clusterRemoved = await RemoveFaceClusterObservation(Number(this.cluster.id), Number(source.observation_id));
        if (this._sourceSession !== session) return;
        this.sources = this.sources.filter(item => item.observation_id !== source.observation_id);
        this.$emit('removed');
        if (clusterRemoved) { this.$emit('close'); return; }
        this.removingID = null;
        if (!this.sources.length && this.nextID) await this.loadMore();
      } catch (err) { if (this._sourceSession === session) this.removeError = `移除来源失败：${err}`; }
      finally { if (this._sourceSession === session) this.removingID = null; }
    },
    openSource(source) {
      if (source.unavailable) return;
      if (source.media_kind === 'video') { this.videoError = ''; this.videoSource = source; }
      else this.imageSource = { ...source, id: source.media_id };
    },
    escape(event) {
      if (event.key !== 'Escape' || this.imageSource) return;
      if (document.querySelector('[data-test="person-media-delete-dialog"]')) return;
      event.preventDefault(); event.stopImmediatePropagation();
      if (feedbackState.confirm) { resolveConfirm(false); return; }
      if (this.videoSource) this.videoSource = null; else this.$emit('close');
    },
    async previewExternally(video) {
      try { await PreviewExternally(Number(video.id)); }
      catch (err) { this.videoError = `外部预览失败：${err}`; }
    }
  }
};
</script>
<style scoped>
:deep(.face-source-dialog) { width: min(900px, 92vw); max-width: 92vw; max-height: 90vh; display: flex; flex-direction: column; gap: 12px; }
.face-source-dialog__header,.face-source-dialog__actions { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.face-source-dialog__header h3 { margin: 0; }
.face-source-dialog__list { overflow-y: auto; min-height: 0; }
.face-source-dialog__row { display: grid; grid-template-columns: 88px minmax(0, 1fr); gap: 14px; padding: 12px 0; border-bottom: 1px solid var(--hairline); }
.face-source-dialog__row img,.face-source-dialog__fallback { width: 88px; height: 88px; object-fit: contain; border-radius: 8px; }
.face-source-dialog__path { overflow-wrap: anywhere; color: var(--text-muted); font-size: 12px; }
.face-source-dialog__remove { margin-left: 8px; color: var(--danger-color); }
.face-source-video { position: fixed; inset: 0; z-index: 1200; background: rgba(0,0,0,.65); }
.face-source-video :deep(.preview-drawer) { width: min(900px, 94vw); max-width: 94vw; }
.face-source-video__error { position: absolute; bottom: 15px; right: 20px; z-index: 1; padding: 10px; background: var(--panel-bg); }
</style>
