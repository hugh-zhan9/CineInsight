<template>
  <main class="page-content explore-page" data-test="explore-page" :aria-busy="busy || loading">
    <PageHeader title="探索" subtitle="在自己的收藏里，遇见还没留意的画面">
      <template #actions>
        <div class="explore-toolbar">
          <select class="select-input" aria-label="探索媒体类型" :value="scope" :disabled="locked" data-test="explore-scope" @change="changeScope($event.target.value)">
            <option value="all">全部媒体</option><option value="video">仅视频</option><option value="image">仅图片</option>
          </select>
          <SegmentedControl label="探索视图" :model-value="view.mode" :options="modeOptions" @update:model-value="changeMode" />
          <button type="button" class="btn btn--primary" :disabled="locked || !snapshot" data-test="explore-next" @click="next">{{ snapshot?.status !== 'ready' ? '查看新内容' : (view.mode === 'wall' ? '换一墙' : '换一批') }}</button>
        </div>
      </template>
    </PageHeader>
    <AppBanner v-if="error" tone="danger" :title="error" data-test="explore-error">
      <template #actions><button type="button" class="btn btn--secondary btn--sm" :disabled="busy" @click="recover">核对并重试</button><button v-if="snapshot" type="button" class="btn btn--ghost btn--sm" :disabled="busy" @click="reload">刷新场景</button></template>
    </AppBanner>
    <AppBanner v-if="failedCount" tone="info" :title="`有 ${failedCount} 项画面暂时无法显示`" data-test="explore-image-failures">
      <template #actions><button type="button" class="btn btn--secondary btn--sm" :disabled="locked" data-test="explore-retry-images" @click="retrySignal++">重试画面</button><button v-if="remainingFailed" type="button" class="btn btn--secondary btn--sm" :disabled="locked" @click="reshuffle">重新洗牌</button></template>
    </AppBanner>
    <AppBanner v-if="libraryChanged" tone="info" title="片库有变化，正在核对并补充内容">
      <template #actions><button type="button" class="btn btn--secondary btn--sm" :disabled="locked" @click="refresh">核对当前内容</button></template>
    </AppBanner>
    <p v-if="loading && !snapshot" class="explore-status" role="status">正在准备这一轮的画面…</p>
    <EmptyState v-else-if="snapshot?.status === 'empty'" icon="image" title="还没有可探索的内容" message="这个范围内没有可见的图片或视频，可换一个媒体类型，或先添加扫描目录。" />
    <template v-else-if="snapshot">
      <div class="explore-summary">
        <span>本轮已展示 {{ snapshot.presented_count }} / {{ snapshot.available_count }} 项</span>
        <span>各媒体类型分别记忆进度 · 画廊与墙共用记录</span>
      </div>
      <AppBanner v-if="snapshot.status === 'round_complete'" tone="info" title="这一轮已浏览完" data-test="explore-complete">
        <template #actions><button type="button" class="btn btn--secondary btn--sm" :disabled="locked" @click="reshuffle">重新洗牌</button></template>
      </AppBanner>
      <div v-if="pageActive && !frozen" class="explore-content" :inert="frozen || busy || !!preview" :class="{ 'is-busy': busy }">
        <MediaWall v-if="view.mode === 'wall'" ref="wall" :entries="snapshot.entries" :cards="cards" :loader="loader" :retry-signal="retrySignal" @failed="setImageFailure" :view="view" :selected-id="view.selected_id" @visible="loadCards" @view="cameraChanged" @select="select" @exposure="exposure" @open="openPreview" />
        <RandomGallery v-else :entries="galleryEntries" :cards="cards" :loader="loader" :retry-signal="retrySignal" @failed="setImageFailure" :selected-id="view.selected_id" @visible="loadCards" @exposure="exposure" @open="openPreview" />
      </div>
      <p v-if="notice" class="explore-status" role="status">{{ notice }}</p>
      <p v-if="remainingFailed" class="explore-status">剩余内容暂时无法预览，可重试当前内容或重新洗牌。</p>
    </template>

    <PhotoLightbox v-if="pageActive && preview?.entry.kind === 'image'" :key="preview.entry.entry_id" :image="preview.image" :asset-url="photoAssetURL" :allow-remove="false" mixed-media :has-prev="previewIndex > 0" :has-next="previewIndex < snapshot.entries.length - 1" @close="closePreview" @prev="stepPreview(-1)" @next="stepPreview(1)" @patch="patchImage" @open-person="openPerson" @open-in-manage="openImageManage" @open-cleanup="closePreview(); $emit('open-cleanup')" />
    <Teleport v-if="pageActive && preview?.entry.kind === 'video'" to="body">
      <DialogShell :title="preview.card.title || '视频预览'" size="xl" data-test="explore-preview" @close="closePreview">
        <AppBanner v-if="previewError" tone="danger" :title="previewError" />
        <div v-if="previewBusy" class="explore-status" role="status">正在准备画面…</div>
        <InlineVideoPlayer v-if="previewSession?.mode === 'inline' && previewSession.inline_source && previewRange" :key="`${preview.entry.entry_id}:${previewRange.requestID}`" ref="previewPlayer" usage="browse-preview" :video-id="preview.entry.media_id" :session="previewSession" :bookmark-range="previewRange" :duration-seconds="(preview.card.duration_ms || 0) / 1000" :autoplay="true" />
        <img v-else-if="previewFrameURL" :key="previewFrameURL" class="explore-still" :src="previewFrameURL" alt="当前视频预览画面" @error="previewImageFailed" />
        <p v-if="previewSession && previewSession.mode !== 'inline'" class="explore-status">在详情中准备播放；浏览不会自动生成播放代理。</p>
        <template #footer>
          <button type="button" class="btn btn--secondary" :disabled="previewBusy || previewIndex <= 0" @click="stepPreview(-1)">上一项</button>
          <button type="button" class="btn btn--secondary" :disabled="previewBusy || previewIndex >= snapshot.entries.length - 1" @click="stepPreview(1)">下一项</button>
          <button type="button" class="btn btn--secondary" :disabled="previewBusy || !previewSession?.source_version || !preview.card.duration_ms" data-test="explore-sample" @click="samplePreview">换个画面</button>
          <button type="button" class="btn btn--primary" :disabled="previewBusy" data-test="explore-detail" @click="openDetail">打开详情</button>
        </template>
      </DialogShell>
    </Teleport>
  </main>
</template>

<script>
import { markRaw } from 'vue';
import { GetPreviewSession } from '../../../wailsjs/go/main/App';
import SegmentedControl from '../ui/SegmentedControl.vue';
import PageHeader from '../ui/PageHeader.vue';
import AppBanner from '../ui/AppBanner.vue';
import EmptyState from '../ui/EmptyState.vue';
import DialogShell from '../ui/DialogShell.vue';
import MediaWall from '../explore/MediaWall.vue';
import RandomGallery from '../explore/RandomGallery.vue';
import PhotoLightbox from './PhotoPreview.vue';
import InlineVideoPlayer from '../player/InlineVideoPlayer.vue';
import { createExplorationController, createExplorationImageLoader, explorationAssetURL, explorationContext, mediaExplorationAPI } from '../../utils/mediaExploration.js';
import { createMediaExposureTracker } from '../../utils/mediaExposure.js';
import { hasOpenModalLayer, MODAL_OPEN_EVENT } from '../ui/modalSignals.js';
import { confirmAction } from '../../utils/feedback.js';

export default {
  name: 'ExplorePage', components: { SegmentedControl, PageHeader, AppBanner, EmptyState, DialogShell, MediaWall, RandomGallery, PhotoLightbox, InlineVideoPlayer },
  props: { pageActive: { type: Boolean, default: true }, imagesVersion: { type: Number, default: 0 }, api: { type: Object, default: () => mediaExplorationAPI } },
  emits: ['open-video', 'open-person', 'open-in-manage', 'open-cleanup'],
  data() { return { snapshot: null, scope: 'all', view: { mode: 'wall', scale: .35, x: 0, y: 0, selected_id: 0, gallery_id: 0, gallery_entry_ids: [] }, cards: {}, failedCards: {}, retrySignal: 0, loader: markRaw(createExplorationImageLoader()), loading: false, busy: false, frozen: false, error: '', notice: '', libraryChanged: false, focused: true, preview: null, previewSession: null, previewError: '', previewBusy: false, generation: 0, previewGeneration: 0 }; },
  computed: {
    modeOptions() { return [{ value: 'gallery', label: '随机画廊', disabled: this.locked, dataTest: 'explore-gallery' }, { value: 'wall', label: '媒体墙', disabled: this.locked, dataTest: 'explore-wall' }]; },
    locked() { return this.busy || this.loading || this.frozen || !!this.preview; },
    galleryEntries() { const ids = this.view.gallery_entry_ids || []; return ids.length ? ids.map(id => this.snapshot?.entries.find(e => e.entry_id === id)).filter(Boolean) : (this.snapshot?.entries || []).slice(0, 24); },
    previewIndex() { return this.snapshot?.entries.findIndex(e => e.entry_id === this.preview?.entry.entry_id) ?? -1; },
    previewRange() { const row = this.snapshot?.entries.find(e => e.entry_id === this.preview?.entry.entry_id); const point = row?.samples?.at(-1); if (!point || point.source_version !== this.previewSession?.source_version) return null; return { videoID: row.media_id, startMS: point.start_ms, endMS: point.end_ms, sourceToken: point.source_version, requestID: `${row.entry_id}:${point.start_ms}:${point.source_version}` }; },
    photoAssetURL() { return this.preview ? explorationAssetURL(`/preview/image/${this.preview.entry.media_id}`, this.snapshot, this.preview.entry.entry_id) : ''; },
    previewFrameURL() { if (!this.preview) return ''; if (!this.previewRange) return this.preview.card.thumbnail_url ? `${this.preview.card.thumbnail_url}&edge=256` : ''; return explorationAssetURL(`/preview/frame/${this.preview.entry.media_id}?ms=${this.previewRange.startMS}&w=960`, this.snapshot, this.preview.entry.entry_id, this.previewRange.sourceToken); },
    failedCount() { return Object.values(this.failedCards).filter(Boolean).length; },
    remainingFailed() { const unseen = (this.snapshot?.entries || []).filter(e => !e.presented); return this.snapshot?.remaining_count > 0 && this.snapshot.remaining_count <= unseen.length && unseen.length > 0 && unseen.every(e => this.cards[e.entry_id]?.unavailable_reason || this.failedCards[e.entry_id]); }
  },
  watch: {
    imagesVersion() { this.markLibraryChanged(); },
    pageActive(active) { if (!active) { this.suspend(); this.flush().catch(error => { this.error = error.message; }); } else { this.focused = document.hasFocus(); if (!this.snapshot) this.start(); else { this.$nextTick(() => this.$refs.wall?.focus()); this.scheduleRefill(); } } }
  },
  created() {
    this._openedEntries = new Set(); this._libraryRevision = 0; this._disposed = false;
    this._tracker = createMediaExposureTracker(); this._wanted = new Set(); this._reading = null;
    this._controller = markRaw(createExplorationController(this.api, (state, pendingView) => this.acceptSnapshot(state, pendingView)));
  },
  mounted() {
    this._focus = () => { this.focused = document.hasFocus() && document.visibilityState !== 'hidden'; if (!this.focused) this._tracker.update([], { active: false }); };
    window.addEventListener('focus', this._focus); window.addEventListener('blur', this._focus); document.addEventListener('visibilitychange', this._focus);
    this._focus();
    this._modalOpen = () => this._tracker.update([], { active: false });
    window.addEventListener(MODAL_OPEN_EVENT, this._modalOpen);
    this._saveTimer = setInterval(() => { if (!this.frozen && this._controller.pending && !this._controller.blocked && !this.busy) this.flush().catch(error => { this.error = error.message; }); }, 1000);
    this._offEvents = [];
    for (const name of ['videos-changed', 'video-updated', 'video-deleted', 'image-library-changed', 'image-updated', 'image-deleted', 'scan-completed', 'library-changed', 'version-groups-changed', 'library-scan-summary', 'library-watcher-reconciled', 'image-scan-completed']) {
      const off = window.runtime?.EventsOn?.(name, () => this.markLibraryChanged()); if (typeof off === 'function') this._offEvents.push(off);
    }
    for (const [name, handler] of [
      ['exploration-quiesced', () => { this.frozen = true; this.suspend(); this.notice = '数据库维护中，探索已暂停。'; }],
      ['exploration-resumed', () => { this.frozen = false; this.notice = '维护已结束，可核对并恢复浏览。'; if (this._controller.blocked) this.error = '浏览记录尚未确认，请核对并重试'; }]
    ]) { const off = window.runtime?.EventsOn?.(name, handler); if (typeof off === 'function') this._offEvents.push(off); }
    if (this.pageActive) this.start();
  },
  beforeUnmount() { this._disposed = true; clearTimeout(this._refillTimer); this.suspend(); clearInterval(this._saveTimer); window.removeEventListener(MODAL_OPEN_EVENT, this._modalOpen); window.removeEventListener('focus', this._focus); window.removeEventListener('blur', this._focus); document.removeEventListener('visibilitychange', this._focus); for (const off of this._offEvents || []) off(); },
  methods: {
    markLibraryChanged() { this.libraryChanged = true; this._libraryRevision++; this.scheduleRefill(); },
    scheduleRefill() {
      clearTimeout(this._refillTimer);
      if (this._disposed || !this.pageActive || this.locked || this._controller.blocked || this.error || (!this.libraryChanged && !this._openedEntries.size)) return;
      this._refillTimer = setTimeout(() => this.refill(), 0);
    },
    async refill() {
      clearTimeout(this._refillTimer);
      if (this._disposed || !this.pageActive || this.locked || this._controller.blocked || this.error) return;
      const revision = this._libraryRevision;
      await this.run(async () => {
        if (this.libraryChanged) {
          await this._controller.change('replenish');
          this.libraryChanged = revision !== this._libraryRevision;
          const ids = (this.view.mode === 'wall' ? this.$refs.wall?.visibleEntries || [] : this.galleryEntries).map(entry => entry.entry_id);
          const ticket = this.generation;
          for (let offset = 0; offset < ids.length && this.pageActive; offset += 256) {
            const result = await this.api.items({ ...explorationContext(this.snapshot), entry_ids: ids.slice(offset, offset + 256) });
            if (ticket !== this.generation || !this.pageActive) break;
            for (const card of result.items) this.cards[card.entry_id] = card;
          }
          if (Object.values(this.cards).some(card => card.unavailable_reason)) this.notice = '部分内容已不可用，暂时没有墙外内容可补充。';
        }
        while (this._openedEntries.size && this.pageActive && !this.frozen) {
          const current = new Set(this.snapshot.entries.map(entry => entry.entry_id));
          for (const id of this._openedEntries) if (!current.has(id)) this._openedEntries.delete(id);
          const ids = [...this._openedEntries].slice(0, 256);
          if (!ids.length) break;
          await this._controller.change('replace', { entry_ids: ids });
          for (const id of ids) this._openedEntries.delete(id);
          this.notice = this.snapshot.entries.some(entry => ids.includes(entry.entry_id)) ? '暂时没有墙外的新内容可补充，已保留原位置。' : '';
        }
      });
    },
    async start() { this.loading = true; this.error = ''; try { await this._controller.open(this.scope); } catch (error) { this.error = error.message; } finally { this.loading = false; } },
    acceptSnapshot(state, pendingView) {
      const changed = this.snapshot?.scene_id !== state.scene_id || this.snapshot?.database_epoch !== state.database_epoch || this.snapshot?.scope !== state.scope;
      if (changed) { this.generation++; this.loader.cancelAll(); this.cards = {}; this.failedCards = {}; this._wanted.clear(); this._reading = null; this._tracker.reset(); this._openedEntries.clear(); this.closePreview(false); }
      else {
        if (this.snapshot.round_id !== state.round_id) this._tracker.reset();
        const previous = new Map(this.snapshot.entries.map(entry => [entry.entry_id, entry.media_id]));
        const retained = new Set(state.entries.filter(entry => previous.get(entry.entry_id) === entry.media_id).map(entry => entry.entry_id));
        if (retained.size !== this.snapshot.entries.length || retained.size !== state.entries.length) {
          this.generation++; this._reading = null; this._wanted.clear();
          this._tracker.forget(this.snapshot.entries.filter(entry => !retained.has(entry.entry_id)).map(entry => entry.entry_id));
          this.$nextTick(() => this.loadCards((this.view.mode === 'wall' ? this.$refs.wall?.visibleEntries || [] : this.galleryEntries).map(entry => entry.entry_id)));
        }
        for (const id of Object.keys(this.cards)) if (!retained.has(Number(id))) { delete this.cards[id]; delete this.failedCards[id]; }
      }
      this.snapshot = state; this.scope = state.scope;
      this.view = { ...state.view_state, ...(pendingView || {}), gallery_entry_ids: pendingView?.gallery_entry_ids || state.view_state.gallery_entry_ids || [] };
    },
    setView(patch) { this.view = { ...this.view, ...patch }; this._controller.view({ ...this.view }); },
    setImageFailure(id, failed) { this.failedCards[id] = failed; },
    cameraChanged(camera) { this.setView(camera); },
    select(id) { this.setView({ selected_id: id }); },
    changeMode(mode) { if (this.locked) return; this.setView({ mode }); this._tracker.update([], { active: false }); },
    exposure(items) {
      const ids = this._tracker.update(items.map(item => ({ ...item, loaded: item.loaded && item.unobscured !== false })), { active: this.pageActive && this.focused && !this.preview && !this.frozen && !this.busy && !hasOpenModalLayer() });
      if (ids.length) this._controller.presented(ids);
    },
    loadCards(ids) {
      if (!this.pageActive || !this.snapshot) return Promise.resolve();
      const current = new Set(this.snapshot.entries.map(entry => entry.entry_id));
      this._wanted = new Set(ids.filter(id => current.has(id) && !this.cards[id]));
      if (this._reading) return this._reading;
      const ticket = this.generation;
      const run = this.drainCards(ticket).catch(error => { if (ticket === this.generation) this.error = error.message; }).finally(() => { if (this._reading === run) this._reading = null; });
      this._reading = run; return run;
    },
    async drainCards(ticket) {
      while (this._wanted.size && ticket === this.generation && this.pageActive) {
        const ids = [...this._wanted].slice(0, 256); for (const id of ids) this._wanted.delete(id);
        const result = await this.api.items({ ...explorationContext(this.snapshot), entry_ids: ids });
        if (ticket !== this.generation || !this.pageActive) return;
        const current = new Set(this.snapshot.entries.map(entry => entry.entry_id));
        for (const card of result.items) if (current.has(card.entry_id)) this.cards[card.entry_id] = card;
      }
    },
    invalidateCards() {
      this.generation++; this.loader.cancelAll(); this.cards = {}; this.failedCards = {}; this._wanted.clear(); this._reading = null;
      this.$nextTick(() => this.loadCards((this.view.mode === 'wall' ? this.$refs.wall?.visibleEntries || [] : this.galleryEntries).map(e => e.entry_id)));
    },
    async flush() { return this._controller.flush(); },
    async run(action) { if (this.busy) return; this.busy = true; this.error = ''; try { return await action(); } catch (error) { this.error = error.message; } finally { this.busy = false; this.scheduleRefill(); } },
    async changeScope(scope) { if (this.locked) return; await this.run(() => this._controller.open(scope)); },
    async next() {
      if (this.locked) return;
      await this.run(async () => {
        await this.flush();
        if (this.view.mode === 'gallery') {
          const current = new Set(this.galleryEntries.map(e => e.entry_id));
          const remaining = this.snapshot.entries.filter(e => !e.presented && !current.has(e.entry_id)).slice(0, 24);
          if (remaining.length) { this.setView({ gallery_id: remaining[0].entry_id, gallery_entry_ids: remaining.map(e => e.entry_id) }); await this.flush(); return; }
        }
        await this._controller.change('next');
        this.notice = this.snapshot.status === 'round_complete' ? '没有新增可见内容；这一轮已浏览完，可重新洗牌。' : '';
      });
    },
    async reshuffle() { if (!this.locked) await this.run(() => this._controller.change('reshuffle')); },
    async refresh() { if (this.locked) return; await this.run(async () => { await this._controller.change('replenish'); this.libraryChanged = false; this.invalidateCards(); }); },
    async recover() { await this.run(async () => { if (!this.snapshot) return this._controller.open(this.scope); await this._controller.recover(); this.invalidateCards(); }); },
    async reload() {
      if (!await confirmAction({ title: '刷新探索场景', message: '将放弃尚未确认保存的浏览记录和视口，读取已保存的场景。', confirmLabel: '刷新场景' })) return;
      await this.run(async () => { await this._controller.reloadDiscardingPending(); this._tracker.reset(); this.invalidateCards(); this.notice = '已读取保存的场景；未确认的浏览记录已放弃。'; });
    },
    async openPreview(entry) {
      if (this.locked || !this.pageActive) return;
      const ticket = ++this.previewGeneration; this.select(entry.entry_id);
      let card;
      try {
        const result = await this.api.items({ ...explorationContext(this.snapshot), entry_ids: [entry.entry_id] });
        if (ticket !== this.previewGeneration || !this.pageActive) return;
        card = result.items[0]; if (card) this.cards[entry.entry_id] = card;
      } catch (error) { if (ticket === this.previewGeneration) this.error = error.message || String(error); return; }
      if (ticket !== this.previewGeneration || !this.pageActive) return;
      if (!card) { this.error = '内容尚未读取，请重试'; return; }
      if (card.unavailable_reason) { this.markLibraryChanged(); return; }
      this._openedEntries.add(entry.entry_id);
      this.preview = { entry, card, image: { id: entry.media_id, name: card.title } }; this.previewSession = null; this.previewError = ''; this._tracker.update([], { active: false });
      if (entry.kind !== 'video') return;
      this.previewBusy = true;
      try {
        const session = await GetPreviewSession(entry.media_id);
        if (ticket !== this.previewGeneration || !this.pageActive) return;
        if (session.inline_source) session.inline_source = { ...session.inline_source, locator_value: explorationAssetURL(session.inline_source.locator_value, this.snapshot, entry.entry_id, session.source_version) };
        this.previewSession = session;
        if (!card.duration_ms) { this.previewError = '片长未知，暂时不能随机取样；可在详情中补全技术信息。'; return; }
        const previous = this.snapshot.entries.find(e => e.entry_id === entry.entry_id)?.samples?.at(-1);
        if (previous && previous.source_version !== session.source_version) this.previewError = '原文件已变化，旧位置已停用；可点“换个画面”重新取样。';
        else if (!previous && card.duration_ms && session.source_version) await this.samplePreview(true);
      } catch (error) { if (ticket === this.previewGeneration) this.previewError = error.message || String(error); }
      finally { if (ticket === this.previewGeneration) this.previewBusy = false; }
    },
    async samplePreview(initial = false) {
      if (!this.preview || (!initial && this.previewBusy)) return;
      const ticket = this.previewGeneration, entryID = this.preview.entry.entry_id, source = this.previewSession?.source_version;
      this.previewBusy = true; this.previewError = ''; this.$refs.previewPlayer?.pause();
      try { await this._controller.change('sample', { entry_id: entryID, source_version: source }); }
      catch (error) { if (ticket === this.previewGeneration) this.previewError = error.message || String(error); }
      finally { if (ticket === this.previewGeneration) this.previewBusy = false; }
    },
    closePreview(refill = true) { this.previewGeneration++; this.$refs.previewPlayer?.pause(); this.preview = null; this.previewSession = null; this.previewBusy = false; this.$nextTick(() => { if (this.pageActive && !this.frozen) this.$refs.wall?.focus(); }); if (refill) this.scheduleRefill(); },
    previewImageFailed(event) { if (this.preview && event.target.getAttribute('src') === this.previewFrameURL) this.previewError = '画面暂时无法读取，可换个画面或打开详情。'; },
    async stepPreview(delta) { const entry = this.snapshot.entries[this.previewIndex + delta]; if (!entry) return; this.closePreview(false); await this.openPreview(entry); },
    async openDetail() { const id = this.preview?.entry.media_id; if (!id) return; this.closePreview(false); await this.run(async () => { await this.flush(); if (this.pageActive) this.$emit('open-video', id); }); },
    patchImage(fields) { if (this.preview?.entry.kind === 'image' && fields.id === this.preview.entry.media_id) this.preview.image = { ...this.preview.image, ...fields }; },
    openPerson(person) { this.closePreview(); this.$emit('open-person', person); },
    openImageManage(image) { this.closePreview(); this.$emit('open-in-manage', image); },
    suspend() { this.generation++; this.closePreview(false); this.loader.cancelAll(); this._wanted.clear(); this._reading = null; this._tracker.update([], { active: false }); },
    async prepareClose() { this.frozen = true; this.suspend(); if (this._controller.blocked) await this._controller.recover(); await this.flush(); },
    resumeClose() { this.frozen = false; },
  }
};
</script>

<style scoped>
.explore-page { display: flex; flex-direction: column; gap: 16px; }
.explore-toolbar, .explore-summary { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.explore-summary { justify-content: space-between; font-size: var(--fs-caption); color: var(--text-secondary); font-variant-numeric: tabular-nums; }
.explore-content.is-busy { opacity: .75; }
.explore-status { color: var(--text-secondary); font-size: var(--fs-body); padding-block: 12px; }
.explore-still { max-height: 65vh; max-width: 100%; display: block; margin-inline: auto; object-fit: contain; }
</style>
