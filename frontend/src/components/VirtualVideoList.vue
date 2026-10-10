<template>
  <div
    :class="['virtual-video-list', `virtual-video-list--${layoutMode}`]"
    ref="shell"
    :data-scroll-owner-fallback="scrollOwnerMissing ? 'true' : null"
  >
    <div v-if="virtualizationEnabled && topSpacer > 0" :style="{ height: `${topSpacer}px` }" aria-hidden="true"></div>

    <div class="virtual-video-list__items" :style="gridStyle">
      <div
        v-for="(item, visibleIndex) in renderedItems"
        :key="item.id"
        class="virtual-video-list__row"
        :data-virtual-row-id="item.id"
        :data-virtual-index="windowed ? startIndex + visibleIndex : visibleIndex"
      >
        <slot :item="item" :index="windowed ? startIndex + visibleIndex : visibleIndex"></slot>
      </div>
    </div>

    <div v-if="virtualizationEnabled && bottomSpacer > 0" :style="{ height: `${bottomSpacer}px` }" aria-hidden="true"></div>
    <div
      v-if="showLoadMoreSentinel"
      ref="loadMoreSentinel"
      class="virtual-video-list__sentinel"
      aria-hidden="true"
    ></div>
  </div>
</template>

<script>
import { markRaw } from 'vue';
import { VirtualHeightIndex, defaultRangeEngine, resolveScrollOwnerDescriptor } from '../utils/virtualList.js';

export default {
  name: 'VirtualVideoList',
  props: {
    items: { type: Array, default: () => [] },
    loading: { type: Boolean, default: false },
    hasMore: { type: Boolean, default: false },
    active: { type: Boolean, default: true },
    scrollOwnerSelector: { type: String, default: '.main-view' },
    virtualizationEnabled: { type: Boolean, default: true },
    subtitleMode: { type: Boolean, default: false },
    previewOpen: { type: Boolean, default: false },
    queryKey: { type: String, default: '' },
    layoutMode: { type: String, default: 'list' },
    layoutKey: { type: String, default: '' },
    overscan: { type: Number, default: 8 },
    rangeEngine: { type: Object, default: () => defaultRangeEngine },
    estimateHeight: { type: Function, required: true },
    itemVersion: { type: Function, required: true }
  },
  emits: ['load-more'],
  data() {
    return {
      scrollOwnerEl: null,
      resizeObserver: null,
      loadMoreObserver: null,
      startIndex: 0, endIndex: 0, topSpacer: 0, bottomSpacer: 0,
      widthBucket: 1, layoutWidth: 0, columns: 1, listTop: 0,
      scrollOwnerMissing: false,
      layoutIndex: null,
      heightCache: markRaw(new Map()),
      anchorQueries: markRaw(new WeakMap()),
      rebuilding: false, pendingPosition: null, measureTicket: null, generation: 0, disposed: false, indexQuery: null, scrollRevision: 0,
      lastPosition: null, inactivePosition: null,
      loadMoreQueued: false
    };
  },
  computed: {
    geometryItems() { return this.items.map(item => [item.id, this.itemVersion(item)]); },
    windowed() { return this.virtualizationEnabled && !this.scrollOwnerMissing; },
    renderedItems() { return this.windowed ? this.items.slice(this.startIndex, this.endIndex) : this.items; },
    gridStyle() { return this.layoutMode === 'grid' ? { gridTemplateColumns: `repeat(${this.columns}, minmax(0, 1fr))` } : null; },
    showLoadMoreSentinel() { return !this.windowed && this.hasMore; }
  },
  watch: {
    queryKey() {
      this.inactivePosition = null;
      this.lastPosition = null;
      this.rebuild(false, true);
    },
    geometryItems() { this.rebuild(); },
    layoutMode() { this.rebuild(true, true); },
    layoutKey() { this.rebuild(true, true); },
    subtitleMode() { this.rebuild(true, true); },
    previewOpen() { this.rebuild(true, true); },
    virtualizationEnabled() { this.rebuild(true, true); },
    active: {
      flush: 'sync',
      handler(active) {
        this.generation++;
        if (!active) {
          // The parent may already be hidden: the last active geometry, never the
          // shared owner's new page position, is the return point.
          this.inactivePosition = this.rebuilding ? this.pendingPosition : this.lastPosition;
          this.rebuilding = false;
          this.stopObservers();
        } else {
          this.resolveScrollOwner();
          this.attachResizeObserver();
          this.rebuild(true, true, this.inactivePosition);
        }
      }
    },
    loading(value) {
      if (!value) this.loadMoreQueued = false;
      this.scheduleLoadMoreObserverRefresh();
    },
    hasMore() { this.loadMoreQueued = false; this.scheduleLoadMoreObserverRefresh(); }
  },
  mounted() {
    this.resolveScrollOwner();
    this.attachResizeObserver();
    this.rebuild(false, true);
  },
  beforeUnmount() {
    this.disposed = true;
    this.generation++;
    this.stopObservers();
    this.scrollOwnerEl = null;
  },
  methods: {
    live(token = this.generation) { return this.active && !this.disposed && token === this.generation; },
    requestLoadMore() {
      if (!this.active || this.loadMoreQueued || this.loading || !this.hasMore) return;
      this.loadMoreQueued = true;
      this.$emit('load-more');
    },
    maybeEmitLoadMore(totalHeight) {
      if (!this.scrollOwnerEl) return;
      if (this.scrollOwnerEl.scrollTop + this.scrollOwnerEl.clientHeight >= this.getListTop() + totalHeight - 400) this.requestLoadMore();
    },
    resolveScrollOwner() {
      const { nextOwner, missing } = resolveScrollOwnerDescriptor(this.$el, this.scrollOwnerEl, this.scrollOwnerSelector);
      this.scrollOwnerEl?.removeEventListener('scroll', this.handleOwnerScroll);
      this.scrollOwnerEl = nextOwner;
      this.scrollOwnerMissing = missing;
      if (nextOwner && this.active) nextOwner.addEventListener('scroll', this.handleOwnerScroll, { passive: true });
      if (missing && this.virtualizationEnabled) console.error(`[VirtualVideoList] missing ${this.scrollOwnerSelector} scroll owner; falling back to full list rendering`);
    },
    stopObservers() {
      this.scrollOwnerEl?.removeEventListener('scroll', this.handleOwnerScroll);
      this.resizeObserver?.disconnect();
      this.resizeObserver = null;
      this.detachLoadMoreObserver();
      this.measureTicket = null;
    },
    attachResizeObserver() {
      if (!this.active || typeof ResizeObserver === 'undefined' || !this.$el || this.resizeObserver) return;
      this.resizeObserver = markRaw(new ResizeObserver(() => {
        if (!this.live()) return;
        this.handleWidthChange();
        this.scheduleMeasure();
      }));
      this.resizeObserver.observe(this.$el);
    },
    detachLoadMoreObserver() {
      this.loadMoreObserver?.disconnect();
      this.loadMoreObserver = null;
    },
    scheduleLoadMoreObserverRefresh() {
      const token = this.generation;
      this.$nextTick(() => { if (this.live(token)) this.refreshLoadMoreObserver(); });
    },
    refreshLoadMoreObserver() {
      this.detachLoadMoreObserver();
      if (!this.active || this.windowed || !this.hasMore) return;
      if (typeof IntersectionObserver === 'undefined') { this.requestLoadMore(); return; }
      const sentinel = this.$refs.loadMoreSentinel;
      if (!sentinel) return;
      this.loadMoreObserver = markRaw(new IntersectionObserver(entries => {
        if (entries.some(entry => entry.isIntersecting)) this.requestLoadMore();
      }, { root: this.scrollOwnerEl || null, rootMargin: '0px 0px 600px 0px', threshold: 0.01 }));
      this.loadMoreObserver.observe(sentinel);
    },
    handleOwnerScroll() {
      if (!this.live()) return;
      this.scrollRevision++;
      this.syncWindow();
      this.lastPosition = this.capturePosition();
      this.scheduleMeasure();
    },
    handleWidthChange() {
      if (!this.live()) return;
      const width = this.$el?.getBoundingClientRect().width || 0;
      if (Math.abs(width - this.layoutWidth) > 0.1) this.rebuild(true, true);
    },
    async rebuild(preserve = true, clear = false, position = undefined) {
      let saved = position === undefined ? (preserve ? (this.rebuilding ? this.pendingPosition : this.capturePosition()) : null) : position;
      if (saved?.query !== this.queryKey) saved = null;
      const scrollRevision = this.scrollRevision;
      const token = ++this.generation;
      this.pendingPosition = saved;
      this.rebuilding = this.active;
      if (!this.active) return;
      await this.$nextTick();
      if (!this.live(token)) return;
      this.resolveScrollOwner();
      this.layoutWidth = this.$el?.getBoundingClientRect().width || 0;
      this.widthBucket = this.rangeEngine.getWidthBucket(this.layoutWidth);
      this.columns = this.layoutMode === 'grid' ? Math.max(1, Math.floor((this.layoutWidth + 12) / 212)) : 1;
      if (clear) this.heightCache.clear();
      const retained = new Map();
      const cardWidth = (this.layoutWidth - (this.columns - 1) * 12) / this.columns;
      this.layoutIndex = markRaw(new VirtualHeightIndex(this.items, item => {
        const cached = this.heightCache.get(item.id);
        if (cached && cached.version === this.itemVersion(item)) {
          retained.set(item.id, cached);
          return cached.height;
        }
        return this.estimateHeight(item, this.widthBucket, this.subtitleMode, { columns: this.columns, cardWidth });
      }, { columns: this.columns, gap: this.layoutMode === 'grid' ? 12 : 0 }));
      this.heightCache = markRaw(retained);
      this.indexQuery = this.queryKey;
      this.listTop = this.getListTop();
      this.syncWindow(saved ? this.positionTop(saved) : undefined);
      await this.$nextTick();
      if (!this.live(token)) return;
      if (saved && this.scrollRevision === scrollRevision) this.restorePosition(saved);
      this.lastPosition = this.capturePosition();
      this.rebuilding = false;
      this.pendingPosition = null;
      if (this.windowed) this.maybeEmitLoadMore(this.layoutIndex.totalHeight);
      this.scheduleMeasure();
      this.refreshLoadMoreObserver();
    },
    getListTop() {
      if (!this.scrollOwnerEl || !this.$el) return 0;
      return this.scrollOwnerEl.scrollTop + this.$el.getBoundingClientRect().top - this.scrollOwnerEl.getBoundingClientRect().top;
    },
    capturePosition() {
      if (!this.active) return this.inactivePosition;
      if (!this.scrollOwnerEl) return null;
      return { anchor: this.captureScrollAnchor(), top: this.scrollOwnerEl.scrollTop, query: this.indexQuery ?? this.queryKey };
    },
    captureScrollAnchor() {
      if (!this.active) return this.inactivePosition?.anchor || null;
      if (!this.scrollOwnerEl || !this.$el) return null;
      let anchor = null;
      if (this.windowed && this.layoutIndex) {
        const index = this.layoutIndex;
        const row = index.rowAt(Math.max(0, this.scrollOwnerEl.scrollTop - this.listTop));
        const i = row * index.columns;
        if (i < index.count) anchor = { id: index.ids[i], offset: this.listTop + index.itemTop(i) - this.scrollOwnerEl.scrollTop };
      } else {
        const ownerTop = this.scrollOwnerEl.getBoundingClientRect().top;
        for (const row of this.$el.querySelectorAll('[data-virtual-row-id]')) {
          const rect = row.getBoundingClientRect();
          if (rect.bottom > ownerTop) {
            const i = this.layoutIndex?.indexOf(row.dataset.virtualRowId) ?? -1;
            anchor = { id: i >= 0 ? this.layoutIndex.ids[i] : row.dataset.virtualRowId, offset: rect.top - ownerTop };
            break;
          }
        }
      }
      if (anchor) this.anchorQueries.set(anchor, this.indexQuery ?? this.queryKey);
      return anchor;
    },
    positionTop(position) {
      const anchor = position?.anchor;
      const index = anchor && this.layoutIndex?.indexOf(anchor.id);
      if (index == null || index < 0) return position?.top ?? this.scrollOwnerEl?.scrollTop ?? 0;
      const row = Math.floor(index / this.columns);
      const offset = Math.max(anchor.offset, -Math.max(0, this.layoutIndex.heights[row] - 1));
      return this.getListTop() + this.layoutIndex.itemTop(index) - offset;
    },
    restorePosition(position) {
      if (!position || position.query !== this.queryKey || !this.live() || !this.scrollOwnerEl) return;
      if (this.windowed) this.scrollOwnerEl.scrollTop = this.positionTop(position);
      else if (!this.restoreDOMAnchor(position.anchor)) this.scrollOwnerEl.scrollTop = position.top;
      this.syncWindow();
    },
    restoreDOMAnchor(anchor) {
      if (!anchor) return false;
      const row = [...this.$el.querySelectorAll('[data-virtual-row-id]')].find(node => node.dataset.virtualRowId === String(anchor.id));
      if (!row) return false;
      this.scrollOwnerEl.scrollTop += row.getBoundingClientRect().top - this.scrollOwnerEl.getBoundingClientRect().top - anchor.offset;
      return true;
    },
    restoreScrollAnchor(anchor) {
      if (!anchor || this.anchorQueries.get(anchor) !== this.queryKey) return;
      if (!this.active) {
        this.inactivePosition = { anchor, top: this.inactivePosition?.top || 0, query: this.queryKey };
        return;
      }
      this.restorePosition({ anchor, top: this.scrollOwnerEl?.scrollTop || 0, query: this.queryKey });
      this.lastPosition = this.capturePosition();
      this.scheduleMeasure();
    },
    scrollToItem(itemId) {
      if (!this.live() || !this.scrollOwnerEl) return;
      const index = this.layoutIndex?.indexOf(itemId) ?? -1;
      if (index < 0) return;
      const row = [...this.$el.querySelectorAll('[data-virtual-row-id]')].find(node => node.dataset.virtualRowId === String(itemId));
      if (row?.scrollIntoView) row.scrollIntoView({ block: 'nearest' });
      else if (this.windowed) this.scrollOwnerEl.scrollTop = this.getListTop() + this.layoutIndex.itemTop(index);
      this.handleOwnerScroll();
    },
    syncWindow(targetTop) {
      if (!this.active) return;
      if (!this.windowed || !this.scrollOwnerEl || !this.layoutIndex) {
        this.startIndex = 0; this.endIndex = this.items.length; this.topSpacer = 0; this.bottomSpacer = 0;
        return;
      }
      this.listTop = this.getListTop();
      const state = this.rangeEngine.calculateVirtualWindow({
        items: this.items, layoutIndex: this.layoutIndex,
        scrollTop: targetTop ?? this.scrollOwnerEl.scrollTop, viewportHeight: this.scrollOwnerEl.clientHeight,
        listTop: this.listTop, overscan: this.overscan
      });
      this.startIndex = state.startIndex; this.endIndex = state.endIndex;
      this.topSpacer = state.topSpacer; this.bottomSpacer = state.bottomSpacer;
      if (targetTop === undefined && !this.rebuilding) this.maybeEmitLoadMore(state.totalHeight);
    },
    scheduleMeasure() {
      if (!this.live() || !this.windowed || this.rebuilding) return;
      const token = this.generation;
      if (this.measureTicket === token) return;
      this.measureTicket = token;
      // Vue batches window updates. Measure after that DOM commit, including in
      // WKWebView sessions where animation callbacks are suspended by occlusion.
      this.$nextTick(() => {
        if (this.measureTicket !== token) return;
        this.measureTicket = null;
        if (this.live(token)) this.measureVisibleRows();
      });
    },
    async measureVisibleRows() {
      if (!this.live() || !this.windowed || this.rebuilding || !this.scrollOwnerEl || !this.layoutIndex) return;
      const token = this.generation;
      const saved = this.capturePosition();
      const scrollRevision = this.scrollRevision;
      const measured = new Map();
      for (const node of this.$el.querySelectorAll('[data-virtual-row-id]')) {
        const i = Number(node.dataset.virtualIndex);
        const item = this.items[i];
        if (!item || String(item.id) !== node.dataset.virtualRowId) continue;
        const height = node.getBoundingClientRect().height;
        if (!height) continue;
        this.heightCache.set(item.id, { height, version: this.itemVersion(item) });
        const row = Math.floor(i / this.columns);
        measured.set(row, Math.max(measured.get(row) || 0, height));
      }
      let changed = false;
      for (const [row, height] of measured) changed = this.layoutIndex.setRowHeight(row, height) || changed;
      if (!changed) return;
      this.syncWindow(this.positionTop(saved));
      await this.$nextTick();
      if (!this.live(token) || this.scrollRevision !== scrollRevision) return;
      this.restorePosition(saved);
      this.lastPosition = this.capturePosition();
    }
  }
};
</script>

<style scoped>
.virtual-video-list { display: block; overflow-anchor: none; }
.virtual-video-list__items { display: block; }
.virtual-video-list--grid .virtual-video-list__items { display: grid; gap: 12px; align-items: stretch; }
.virtual-video-list__row { display: block; min-width: 0; }
.virtual-video-list__sentinel { width: 100%; height: 1px; }
</style>
