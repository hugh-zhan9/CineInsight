<template>
  <div v-if="visible" :ref="setBarElement" class="ui-selection-bar" role="toolbar" aria-label="已选条目的批量操作">
    <span class="ui-selection-bar__count" aria-live="polite" data-test="selection-bar-count">{{ countText }}</span>
    <button
      v-if="canSelectAllMatching"
      type="button"
      class="btn-link"
      :data-test="selectAllDataTest"
      :disabled="selectingAll"
      @click="$emit('select-all-matching')"
    >{{ selectAllLabel }}</button>
    <button type="button" class="btn-link" :data-test="clearDataTest" @click="$emit('clear')">
      取消选择<kbd v-if="clearOnEscape" class="ui-selection-bar__kbd">Esc</kbd>
    </button>
    <div v-if="$slots.actions" class="ui-selection-bar__actions"><slot name="actions" /></div>
  </div>
</template>

<script>
import { reactive } from 'vue';
import { isTextEntryTarget } from '../../utils/keyboardShortcuts.js';

// 「全选当前结果（N）」的文案（D-UD-10）：选择操作条与片库工具栏共用这一处，N 就是点了之后会选中的条数；
// 宿主正在补取剩余的页时是「正在选择…」。N 未知时不带括号（只有片库工具栏的按钮会这样用）。
export function selectAllMatchingLabel(total, selecting = false) {
  if (selecting) return '正在选择…';
  return Number.isFinite(total) ? `全选当前结果（${total}）` : '全选当前结果';
}

// 选择操作条（D-UD-10）：有选中时才浮在内容区底部，没选中时页面上没有批量动作。默认只作用于勾选的条目；
// 要作用于整个筛选结果必须点「全选当前结果（N）」（select-all-matching），宿主据此把 allMatchingSelected 置真。
// 文案全站固定；Esc 发 clear（有对话框开着、焦点在文本框里——判定见 utils/keyboardShortcuts.js 的 isTextEntryTarget、
// 按键已被别人处理、或宿主被 v-show 藏起来时不抢）。
// 清理审阅的勾选是审阅结果而不是临时选择，宿主传 clear-on-escape=false 关掉 Esc 清空（「取消选择」照旧）。
// actions 插槽放批量动作按钮（主操作放最后）。宿主负责把它放在滚动内容的末尾，吸底靠 position: sticky。
// 两个内置链接的 data-test 默认是 selection-bar-select-all-matching / selection-bar-clear；宿主要保留自己原有的
// 测试钩子时用 select-all-data-test / clear-data-test 换掉（例如图片库的 photo-select-all / photo-clear-selection）。
// 元素真的显示着：沿祖先查 display:none（v-show 写的就是它）。藏起来的页面里还挂着的抽屉、对话框不算「开着」，
// 否则切到别的页面后，那边的 Esc 会被一个看不见的抽屉挡住。
function isRendered(element) {
  for (let node = element; node; node = node.parentElement) {
    if (getComputedStyle(node).display === 'none') return false;
  }
  return true;
}

// 看得见的操作条在窗口里的竖直范围，按离窗口底边的距离记：top 是上沿、bottom 是下沿。全局提示据此决定照常待在
// 底部还是抬到操作条上面（AppFeedback）。吸底时下沿离底 16；列表短时它跟在最后一行后面，可能在半屏处；
// 操作条所在的区块后面还有别的内容时（体检、丢失媒体的分组），滚动会让它在吸底与随内容上移之间来回。
// 跟着变的时机：操作条尺寸变（折行；页面被 v-show 藏起来时变成 0）、所在滚动区域滚动、窗口改大小，每帧最多量一次。
// 片库、图片管理、清理中心切走时只隐藏不卸载，各自的操作条还挂着：量出来高度是 0 的不算；看得见的有几条时合成一个范围。
export const selectionBarZone = reactive({ visible: false, top: 0, bottom: 0 });
const barZones = new Map();
function publishBarZone() {
  const zones = [...barZones.values()];
  selectionBarZone.visible = zones.length > 0;
  selectionBarZone.top = zones.length ? Math.max(...zones.map(zone => zone.top)) : 0;
  selectionBarZone.bottom = zones.length ? Math.min(...zones.map(zone => zone.bottom)) : 0;
}

// 操作条随着滚动的那个祖先（片库是内容区 .main-view）；都不滚时是窗口。
function scrollOwnerOf(element) {
  for (let node = element.parentElement; node; node = node.parentElement) {
    if (/^(auto|scroll|overlay)$/.test(getComputedStyle(node).overflowY)) return node;
  }
  return window;
}

export default {
  name: 'SelectionBar',
  props: {
    count: { type: Number, required: true },
    totalMatching: { type: Number, default: null },
    allMatchingSelected: { type: Boolean, default: false },
    // 宿主正在为「全选当前结果」补取剩余的页：按钮禁用并写「正在选择…」。
    selectingAll: { type: Boolean, default: false },
    clearOnEscape: { type: Boolean, default: true },
    selectAllDataTest: { type: String, default: 'selection-bar-select-all-matching' },
    clearDataTest: { type: String, default: 'selection-bar-clear' }
  },
  emits: ['select-all-matching', 'clear'],
  computed: {
    visible() { return this.count > 0 || this.allMatchingSelected; },
    selectedTotal() {
      return this.allMatchingSelected && Number.isFinite(this.totalMatching) ? this.totalMatching : this.count;
    },
    countText() {
      return this.allMatchingSelected ? `已选当前结果全部 ${this.selectedTotal} 项` : `已选 ${this.selectedTotal} 项`;
    },
    canSelectAllMatching() {
      return !this.allMatchingSelected && Number.isFinite(this.totalMatching) && this.totalMatching > this.count;
    },
    selectAllLabel() {
      return selectAllMatchingLabel(this.totalMatching, this.selectingAll);
    }
  },
  mounted() { document.addEventListener('keydown', this.handleKeydown); },
  beforeUnmount() { document.removeEventListener('keydown', this.handleKeydown); },
  methods: {
    // 操作条节点随 v-if 出现 / 消失（组件卸载时也会以 null 再调一次）：出现时开始跟踪它的位置，消失时撤掉监听与它的范围。
    setBarElement(element) {
      if (element === this._barElement) return;
      this.stopTrackingBar();
      this._barElement = element;
      if (element) this.trackBar(element);
      publishBarZone();
    },
    // 滚动区域在第一次量的时候才找：这里被调用时节点可能还在一棵没挂进页面的子树里（整页一起挂载），往上找不到它。
    trackBar(element) {
      if (typeof ResizeObserver === 'function') {
        this._barResize = new ResizeObserver(this.scheduleBarMeasure);
        this._barResize.observe(element);
      }
      window.addEventListener('resize', this.scheduleBarMeasure, { passive: true });
      this.scheduleBarMeasure();
    },
    stopTrackingBar() {
      this._barResize?.disconnect();
      this._barResize = null;
      this._barScrollOwner?.removeEventListener('scroll', this.scheduleBarMeasure);
      this._barScrollOwner = null;
      window.removeEventListener('resize', this.scheduleBarMeasure);
      if (this._barFrame) cancelAnimationFrame(this._barFrame);
      this._barFrame = 0;
      barZones.delete(this);
    },
    // 滚动一帧里会来好多次：合到下一帧量一次。
    scheduleBarMeasure() {
      if (this._barFrame || !this._barElement) return;
      this._barFrame = requestAnimationFrame(() => {
        this._barFrame = 0;
        this.measureBar();
      });
    },
    measureBar() {
      const element = this._barElement;
      if (element && !this._barScrollOwner && element.isConnected) {
        this._barScrollOwner = scrollOwnerOf(element);
        this._barScrollOwner.addEventListener('scroll', this.scheduleBarMeasure, { passive: true });
      }
      const rect = element?.getBoundingClientRect();
      if (rect && rect.height > 0) barZones.set(this, { top: window.innerHeight - rect.top, bottom: window.innerHeight - rect.bottom });
      else barZones.delete(this);
      publishBarZone();
    },
    handleKeydown(event) {
      if (event.key !== 'Escape' || !this.clearOnEscape || !this.visible || event.defaultPrevented) return;
      if (!this.isShown() || [...document.querySelectorAll('[role="dialog"]')].some(dialog => isRendered(dialog))) return;
      if (isTextEntryTarget(event.target)) return;
      event.preventDefault();
      this.$emit('clear');
    },
    // 操作条真的显示在页面上：宿主页面被 v-show 藏起来（切到别的页面）时，它和它的勾选都还在，
    // 但 Esc 不能替一个看不见的页面清空选择。沿祖先查 display:none（v-show 写的就是它）。
    isShown() {
      return isRendered(this.$el);
    }
  }
};
</script>

<style scoped>
.ui-selection-bar {
  position: sticky;
  bottom: var(--space-4);
  z-index: var(--z-sticky);
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--space-2) var(--space-4);
  margin-top: var(--space-3);
  padding: var(--space-2) var(--space-2) var(--space-2) var(--space-4);
  border: 1px solid var(--accent-border);
  border-radius: var(--radius-lg);
  background: var(--panel-bg);
  box-shadow: var(--shadow-popover);
  font-size: var(--fs-secondary);
}
.ui-selection-bar__count { color: var(--text-primary); font-weight: var(--fw-semibold); font-variant-numeric: tabular-nums; }
.ui-selection-bar__kbd { margin-left: var(--space-1); padding: 0 4px; border: 1px solid var(--hairline); border-radius: 4px; color: var(--text-muted); font-family: var(--font-mono); font-size: var(--fs-caption); }
.ui-selection-bar__actions { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); margin-left: auto; }
</style>
