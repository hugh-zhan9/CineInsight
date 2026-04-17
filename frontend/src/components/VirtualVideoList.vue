<template>
  <div class="virtual-video-list" ref="shell" :data-scroll-owner-fallback="scrollOwnerMissing ? 'true' : null">
    <div v-if="virtualizationEnabled && topSpacer > 0" :style="{ height: `${topSpacer}px` }" aria-hidden="true"></div>

    <template v-for="(item, visibleIndex) in renderedItems" :key="item.id">
      <div
        class="virtual-video-list__row"
        :data-virtual-row-id="item.id"
        :data-virtual-index="virtualizationEnabled ? startIndex + visibleIndex : visibleIndex"
      >
        <slot :item="item" :index="virtualizationEnabled ? startIndex + visibleIndex : visibleIndex"></slot>
      </div>
    </template>

    <div v-if="virtualizationEnabled && bottomSpacer > 0" :style="{ height: `${bottomSpacer}px` }" aria-hidden="true"></div>
  </div>
</template>

<script>
import {
  createHeightCacheKey,
  defaultRangeEngine,
  resolveScrollOwnerDescriptor
} from '../utils/virtualList.js';

export default {
  name: 'VirtualVideoList',
  props: {
    items: { type: Array, default: () => [] },
    loading: { type: Boolean, default: false },
    hasMore: { type: Boolean, default: false },
    virtualizationEnabled: { type: Boolean, default: true },
    subtitleMode: { type: Boolean, default: false },
    previewOpen: { type: Boolean, default: false },
    queryKey: { type: String, default: '' },
    overscan: { type: Number, default: 8 },
    rangeEngine: {
      type: Object,
      default: () => defaultRangeEngine
    },
    estimateHeight: { type: Function, required: true },
    itemVersion: { type: Function, required: true }
  },
  emits: ['load-more'],
  data() {
    return {
      scrollOwnerEl: null,
      resizeObserver: null,
      startIndex: 0,
      endIndex: 0,
      topSpacer: 0,
      bottomSpacer: 0,
      widthBucket: 1,
      scrollOwnerMissing: false,
      heightCache: new Map(),
      itemVersionCache: new Map(),
      measureRaf: 0
    };
  },
  computed: {
    renderedItems() {
      if (!this.virtualizationEnabled) {
        return this.items;
      }
      return this.items.slice(this.startIndex, this.endIndex);
    }
  },
  watch: {
    queryKey() {
      this.resetWindow();
    },
    virtualizationEnabled() {
      this.resetWindow();
    },
    previewOpen() {
      this.handleWidthChange();
    },
    items() {
      this.$nextTick(() => {
        this.syncWindow();
        this.scheduleMeasure();
      });
    }
  },
  mounted() {
    this.resolveScrollOwner();
    this.attachResizeObserver();
    this.resetWindow();
  },
  beforeUnmount() {
    this.detachScrollOwner();
    if (this.resizeObserver) {
      this.resizeObserver.disconnect();
      this.resizeObserver = null;
    }
    if (this.measureRaf) {
      cancelAnimationFrame(this.measureRaf);
      this.measureRaf = 0;
    }
  },
  methods: {
    resolveScrollOwner() {
      const { nextOwner, sameOwner, missing } = resolveScrollOwnerDescriptor(this.$el, this.scrollOwnerEl);
      this.scrollOwnerMissing = missing;
      if (sameOwner) {
        if (missing && this.virtualizationEnabled) {
          console.error('[VirtualVideoList] missing .main-view scroll owner; falling back to full list rendering');
        }
        return;
      }
      this.detachScrollOwner();
      this.scrollOwnerEl = nextOwner;
      if (this.scrollOwnerEl) {
        this.scrollOwnerEl.addEventListener('scroll', this.handleOwnerScroll, { passive: true });
      } else if (this.virtualizationEnabled) {
        console.error('[VirtualVideoList] missing .main-view scroll owner; falling back to full list rendering');
      }
    },
    detachScrollOwner() {
      if (this.scrollOwnerEl) {
        this.scrollOwnerEl.removeEventListener('scroll', this.handleOwnerScroll);
        this.scrollOwnerEl = null;
      }
    },
    attachResizeObserver() {
      if (typeof ResizeObserver === 'undefined' || !this.$el) {
        return;
      }
      this.resizeObserver = new ResizeObserver(() => {
        this.handleWidthChange();
      });
      this.resizeObserver.observe(this.$el);
    },
    handleOwnerScroll() {
      this.syncWindow();
      this.scheduleMeasure();
    },
    handleWidthChange() {
      const nextBucket = this.rangeEngine.getWidthBucket(this.$el?.getBoundingClientRect().width || 0);
      if (nextBucket !== this.widthBucket) {
        this.widthBucket = nextBucket;
        this.syncWindow(true);
      }
      this.scheduleMeasure();
    },
    resetWindow() {
      this.$nextTick(() => {
        this.resolveScrollOwner();
        this.widthBucket = this.rangeEngine.getWidthBucket(this.$el?.getBoundingClientRect().width || 0);
        this.syncWindow(true);
        this.scheduleMeasure();
      });
    },
    getListTop() {
      if (!this.scrollOwnerEl || !this.$el) {
        return 0;
      }
      const ownerRect = this.scrollOwnerEl.getBoundingClientRect();
      const listRect = this.$el.getBoundingClientRect();
      return this.scrollOwnerEl.scrollTop + (listRect.top - ownerRect.top);
    },
    getItemHeight(item) {
      const key = createHeightCacheKey(item.id, this.widthBucket, this.subtitleMode);
      if (this.heightCache.has(key)) {
        return this.heightCache.get(key);
      }
      return this.estimateHeight(item, this.widthBucket, this.subtitleMode);
    },
    syncWindow(force = false) {
      if (!this.virtualizationEnabled || !this.scrollOwnerEl || this.scrollOwnerMissing) {
        this.startIndex = 0;
        this.endIndex = this.items.length;
        this.topSpacer = 0;
        this.bottomSpacer = 0;
        return;
      }

      if (!force) {
        const bucket = this.rangeEngine.getWidthBucket(this.$el?.getBoundingClientRect().width || 0);
        if (bucket !== this.widthBucket) {
          this.widthBucket = bucket;
        }
      }

      const windowState = this.rangeEngine.calculateVirtualWindow({
        items: this.items,
        scrollTop: this.scrollOwnerEl.scrollTop,
        viewportHeight: this.scrollOwnerEl.clientHeight,
        listTop: this.getListTop(),
        overscan: this.overscan,
        getItemHeight: (item) => this.getItemHeight(item)
      });

      this.startIndex = windowState.startIndex;
      this.endIndex = windowState.endIndex;
      this.topSpacer = windowState.topSpacer;
      this.bottomSpacer = windowState.bottomSpacer;

      if (this.hasMore && !this.loading) {
        const viewportBottom = this.scrollOwnerEl.scrollTop + this.scrollOwnerEl.clientHeight;
        const listBottom = this.getListTop() + windowState.totalHeight;
        if (viewportBottom >= listBottom - 400) {
          this.$emit('load-more');
        }
      }
    },
    scheduleMeasure() {
      if (!this.virtualizationEnabled) {
        return;
      }
      if (this.measureRaf) {
        cancelAnimationFrame(this.measureRaf);
      }
      this.measureRaf = requestAnimationFrame(() => {
        this.measureRaf = 0;
        this.measureVisibleRows();
      });
    },
    measureVisibleRows() {
      if (!this.virtualizationEnabled || !this.scrollOwnerEl || !this.$el) {
        return;
      }

      const anchorIndex = this.startIndex;
      const anchorOffsetWithin = Math.max(0, this.scrollOwnerEl.scrollTop - this.getListTop() - this.topSpacer);
      let hasHeightChange = false;

      const nodes = this.$el.querySelectorAll('[data-virtual-row-id]');
      nodes.forEach((node) => {
        const itemId = Number(node.getAttribute('data-virtual-row-id'));
        const itemIndex = Number(node.getAttribute('data-virtual-index'));
        const item = this.items[itemIndex];
        if (!item || item.id !== itemId) {
          return;
        }

        const version = this.itemVersion(item);
        const versionKey = `${itemId}:${this.subtitleMode ? 1 : 0}`;
        if (this.itemVersionCache.get(versionKey) !== version) {
          this.heightCache.delete(createHeightCacheKey(itemId, this.widthBucket, this.subtitleMode));
          this.itemVersionCache.set(versionKey, version);
        }

        const measuredHeight = Math.ceil(node.getBoundingClientRect().height);
        if (!measuredHeight) {
          return;
        }
        const cacheKey = createHeightCacheKey(itemId, this.widthBucket, this.subtitleMode);
        if (this.heightCache.get(cacheKey) !== measuredHeight) {
          this.heightCache.set(cacheKey, measuredHeight);
          hasHeightChange = true;
        }
      });

      if (!hasHeightChange) {
        return;
      }

      this.syncWindow();
      const nextScrollTop = this.rangeEngine.calculateAnchorScrollTop({
        items: this.items,
        listTop: this.getListTop(),
        anchorIndex,
        anchorOffsetWithin,
        getItemHeight: (item) => this.getItemHeight(item)
      });
      this.scrollOwnerEl.scrollTop = nextScrollTop;
      this.syncWindow();
    }
  }
};
</script>

<style scoped>
.virtual-video-list {
  display: flex;
  flex-direction: column;
  gap: 0;
}

.virtual-video-list__row {
  display: block;
}
</style>
