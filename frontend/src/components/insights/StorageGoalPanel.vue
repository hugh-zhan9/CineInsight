<template>
  <section class="storage-goal" data-test="storage-goal">
    <h3>按目标释放空间</h3>
    <p class="help-text">指定希望额外腾出的空间，先核对候选，再到原清理流程处理。这里不会删除文件。</p>
    <form class="storage-goal__form" @submit.prevent="generate">
      <label>磁盘<select v-model="volumeID" class="select-input" :disabled="loading || busy || disabled" data-test="storage-goal-volume"><option value="" disabled>请选择磁盘</option><option v-for="volume in volumes" :key="volume.id" :value="volume.id">{{ volume.label }}</option></select></label>
      <label>希望额外释放<input v-model.number="targetGB" class="number-input" type="number" min="0.001" max="1000000" step="0.1" :disabled="busy || disabled" data-test="storage-goal-gb" />GB</label>
      <button type="submit" class="btn btn--primary" :disabled="loading || busy || disabled || !volumeID" data-test="storage-goal-generate">{{ busy ? '正在生成方案…' : '生成审阅方案' }}</button>
      <button type="button" class="btn btn--secondary" :disabled="busy || disabled" @click="refreshVolumes">刷新磁盘空间</button>
    </form>
    <p v-if="selectedVolume" class="help-text" data-test="storage-goal-free">当前可用 {{ formatBytes(selectedVolume.available_bytes) }} · 可见媒体占用 {{ formatBytes(selectedVolume.media_bytes) }} · {{ sampledAt }}</p>
    <p v-if="!loading && !volumes.length && !error" role="status">没有找到含可见媒体的在线磁盘。</p>
    <AppBanner v-if="error" tone="danger" :title="error" />
    <p v-if="notice" class="help-text" role="status">{{ notice }}</p>
    <div class="storage-goal__links">
      <button type="button" class="btn btn--ghost btn--sm" :disabled="disabled" @click="$emit('run-command', 'library.openCleanup', '视频清理审阅')">核对视频保留项</button>
      <button type="button" class="btn btn--ghost btn--sm" :disabled="disabled" @click="$emit('run-command', 'photos.openCleanup', '图片清理审阅')">核对图片保留项</button>
    </div>
    <template v-if="plan">
      <p data-test="storage-goal-summary">共 {{ plan.count }} 项候选，逻辑估计 {{ formatBytes(plan.potential_bytes) }}<template v-if="plan.shortfall_bytes > 0">，距目标仍差 {{ formatBytes(plan.shortfall_bytes) }}</template>。</p>
      <p class="help-text">当前勾选 {{ selection.size }} 项，估计 {{ formatBytes(selectedBytes) }}；仅精确重复按目标优先勾选，其他项目需手动核对。</p>
      <ul class="storage-goal__notices"><li v-for="text in plan.notices" :key="text">{{ text }}</li></ul>
      <div v-for="item in plan.items.slice(0, visibleCount)" :key="`${item.kind}:${item.id}`" class="storage-goal__row" data-test="storage-goal-row">
        <input type="checkbox" :checked="selection.has(key(item))" :disabled="disabled" :aria-label="`选择 ${item.title}`" @change="toggle(item)" />
        <img :src="item.kind === 'image' ? `/preview/image-thumbnail/${item.id}` : `/preview/thumbnail/${item.id}`" alt="" loading="lazy" />
        <div><strong>{{ item.title }}</strong><small :title="item.keeper_title">{{ item.reason }}<template v-if="item.keeper_title"> · 保留：{{ item.keeper_title }}</template><template v-if="item.is_favorite"> · 已收藏</template><template v-if="item.personal_rating != null"> · 评分 {{ item.personal_rating }}</template></small></div>
        <span>{{ formatBytes(item.bytes) }}</span>
        <button v-if="item.kind === 'video'" type="button" class="btn btn--secondary btn--sm" @click="$emit('open-video-id', item.id)">预览再决定</button>
      </div>
      <button v-if="visibleCount < plan.items.length" type="button" class="btn btn--secondary btn--sm" @click="visibleCount += 50">显示更多候选</button>
      <p v-if="plan.truncated" class="help-text">这里只列出前 {{ plan.items.length }} 项；其余候选请到原清理审阅中核对。</p>
      <div class="storage-goal__links">
        <button type="button" class="btn btn--secondary" :disabled="disabled || !selectedDuplicates('video').length" data-test="storage-goal-review-video" @click="review('video')">审阅勾选的视频重复项</button>
        <button type="button" class="btn btn--secondary" :disabled="disabled || !selectedDuplicates('image').length" data-test="storage-goal-review-image" @click="review('image')">审阅勾选的图片重复项</button>
      </div>
      <p class="help-text">长期未看的大文件可逐一预览，或在下方「久未观看的大文件」中继续审阅。移到同卷废纸篓通常不会立刻增加可用空间；处理后刷新查看实测结果。</p>
    </template>
  </section>
</template>
<script>
import { GetStorageGoalPlan, GetStorageGoalVolumes } from '../../../wailsjs/go/main/App';
import AppBanner from '../ui/AppBanner.vue';
import { formatGoalBytes as formatBytes } from '../../utils/storageGoal.js';
import { mediaProcessingError } from '../../utils/mediaProcessing.js';
import { handoffCleanupReview, readCleanupReview } from '../../utils/cleanupReviewAccess.js';
export default {
  name: 'StorageGoalPanel', components: { AppBanner }, props: { disabled: Boolean }, emits: ['run-command', 'open-video-id'],
  data: () => ({ volumes: [], epoch: '', sampledAt: '', volumeID: '', targetGB: 100, loading: false, busy: false, error: '', notice: '', plan: null, selection: new Set(), visibleCount: 50, generation: 0 }),
  computed: {
    selectedVolume() { return this.volumes.find(volume => volume.id === this.volumeID); },
    reviews() { return { video: readCleanupReview('video'), image: readCleanupReview('image') }; },
    reviewRevision() { return JSON.stringify([this.reviews.video?.revision, this.reviews.image?.revision]); },
    selectedBytes() { return (this.plan?.items || []).filter(item => this.selection.has(this.key(item))).reduce((sum, item) => sum + Number(item.bytes), 0); }
  },
  watch: {
    volumeID() { this.invalidate(); }, targetGB() { this.invalidate(); },
    reviewRevision() { if (this.plan) { this.invalidate(); this.notice = '保留项或分析条件已变化，请重新生成方案。'; } }
  },
  mounted() { this.refreshVolumes(); }, beforeUnmount() { this.generation++; this._closed = true; },
  methods: {
    formatBytes,
    key(item) { return `${item.kind}:${item.id}`; },
    invalidate() { this.generation++; this.plan = null; this.selection = new Set(); this.busy = false; this.notice = ''; },
    async refreshVolumes() {
      if (this.busy) return; const ticket = ++this.generation; this.loading = true; this.error = ''; this.plan = null;
      try { const response = await GetStorageGoalVolumes(); if (this._closed || ticket !== this.generation) return; this.volumes = response.items || []; this.epoch = response.epoch; this.sampledAt = new Date(response.sampled_at).toLocaleString(); if (!this.volumes.some(volume => volume.id === this.volumeID)) this.volumeID = this.volumes[0]?.id || ''; }
      catch (error) { if (!this._closed && ticket === this.generation) this.error = '读取磁盘空间失败：' + error; }
      finally { if (!this._closed) this.loading = false; }
    },
    async generate() {
      if (this.busy || this.loading || this.disabled) return;
      if (!this.volumeID) { this.error = '请先选择在线磁盘'; return; }
      const target = Number(this.targetGB) * 1e9; if (!Number.isSafeInteger(target) || target <= 0 || Number(this.targetGB) > 1000000) { this.error = '请输入有效的目标GB数，支持最多100万GB'; return; }
      const ticket = ++this.generation, revision = this.reviewRevision, reviews = this.reviews; this.busy = true; this.error = ''; this.notice = ''; this.plan = null;
      const wire = snapshot => snapshot ? { analysis_key: snapshot.analysis_key, locked_ids: snapshot.locked_ids, candidates: snapshot.candidates } : null;
      try { const plan = await GetStorageGoalPlan({ epoch: this.epoch, volume_id: this.volumeID, target_bytes: target, video_review: wire(reviews.video), image_review: wire(reviews.image) }); if (this._closed || ticket !== this.generation) return; if (revision !== this.reviewRevision) { this.notice = '保留项或分析条件已变化，请重新生成方案。'; return; } this.plan = plan; this._planReviews = reviews; this.selection = new Set((plan.items || []).filter(item => item.suggested).map(this.key)); this.visibleCount = 50; }
      catch (error) { if (!this._closed && ticket === this.generation) this.error = '生成方案失败：' + error; }
      finally { if (ticket === this.generation) this.busy = false; }
    },
    toggle(item) { const next = new Set(this.selection); if (next.has(this.key(item))) next.delete(this.key(item)); else next.add(this.key(item)); this.selection = next; },
    selectedDuplicates(kind) { return (this.plan?.items || []).filter(item => item.kind === kind && item.reason_kind !== 'old' && this.selection.has(this.key(item))); },
    review(kind) {
      const snapshot = this._planReviews?.[kind], current = readCleanupReview(kind);
      if (!snapshot || !current || snapshot.revision !== current.revision) { this.invalidate(); this.notice = '审阅条件已变化，请重新生成方案。'; return; }
      handoffCleanupReview(kind, current.revision, this.selectedDuplicates(kind).map(item => item.id));
      this.$emit('run-command', kind === 'video' ? 'library.openCleanup' : 'photos.openCleanup', kind === 'video' ? '视频清理审阅' : '图片清理审阅');
    }
  }
};
</script>
<style scoped>
.storage-goal { margin-block: 20px; padding: 18px; border: 1px solid var(--hairline); border-radius: var(--radius); background: var(--panel-bg); }
.storage-goal h3 { margin-top: 0; }
.storage-goal__form, .storage-goal__links { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; }
.storage-goal__form label { display: flex; align-items: center; gap: 6px; }
.storage-goal__form input { width: 110px; }
.storage-goal__links { margin-block: 12px; }
.storage-goal__notices { padding-left: 20px; font-size: var(--fs-caption); color: var(--text-muted); }
.storage-goal__row { display: flex; align-items: center; gap: 10px; padding: 10px 0; border-top: 1px solid var(--hairline-faint); }
.storage-goal__row > img { width: 72px; height: 48px; object-fit: cover; }
.storage-goal__row > div { flex: 1; min-width: 0; }
.storage-goal__row strong, .storage-goal__row small { display: block; overflow: hidden; text-overflow: ellipsis; }
.storage-goal__row small { color: var(--text-muted); font-size: var(--fs-caption); }
.storage-goal__row > span { white-space: nowrap; }
</style>
