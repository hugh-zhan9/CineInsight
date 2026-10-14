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

// 吸底的操作条从窗口底边往上占多高（自身高度 + 吸底的下边距），写在根节点的 --selection-bar-inset 上：
// 浮在右下 / 左下角的全局提示据此抬到它上面（AppFeedback），操作条折成几行都跟着变。片库、图片管理、清理中心
// 切走时只隐藏（v-show）不卸载，各自的操作条还挂着：藏起来的尺寸是 0（ResizeObserver 也会报这一下），不算；
// 看得见的有几条取最高的。没有看得见的操作条时去掉这个变量。
const barInsets = new Map();
function publishBarInset() {
  const inset = Math.max(0, ...barInsets.values());
  const style = document.documentElement.style;
  if (inset > 0) style.setProperty('--selection-bar-inset', `${inset}px`);
  else style.removeProperty('--selection-bar-inset');
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
    // 操作条节点随 v-if 出现 / 消失（组件卸载时也会以 null 再调一次）：出现时开始量它的高度，消失时撤掉它占的位置。
    setBarElement(element) {
      if (element === this._barElement) return;
      this._barResize?.disconnect();
      this._barResize = null;
      this._barElement = element;
      barInsets.delete(this);
      if (element && typeof ResizeObserver === 'function') {
        this._barResize = new ResizeObserver(() => this.measureBar());
        this._barResize.observe(element);
      }
      publishBarInset();
    },
    measureBar() {
      const element = this._barElement;
      const height = element ? element.getBoundingClientRect().height : 0;
      if (height > 0) barInsets.set(this, height + (parseFloat(getComputedStyle(element).bottom) || 0));
      else barInsets.delete(this);
      publishBarInset();
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
