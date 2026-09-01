<template>
  <BasePopover
    :anchor="anchor"
    :position="position"
    :align="align"
    :min-width="minWidth"
    panel-class="base-menu"
    @close="$emit('close')"
  >
    <div class="base-menu__list" role="menu" :aria-label="label" @keydown="onKeydown">
      <template v-for="(item, index) in items" :key="item.id || item.heading || `sep-${index}`">
        <div v-if="item.divider" class="base-menu__divider" role="separator"></div>
        <div v-else-if="item.heading" class="base-menu__heading" role="presentation">{{ item.heading }}</div>
        <button
          v-else
          type="button"
          role="menuitem"
          :ref="el => registerItem(el, index)"
          :class="['base-menu__item', { 'base-menu__item--danger': item.danger, 'base-menu__item--active': index === activeIndex }]"
          :disabled="item.disabled"
          :data-menu-id="item.id"
          @click="select(item)"
          @mouseenter="activeIndex = index"
        >
          <span class="base-menu__label">{{ item.label }}</span>
          <span v-if="item.shortcut" class="base-menu__shortcut">{{ item.shortcut }}</span>
        </button>
      </template>
    </div>
  </BasePopover>
</template>

<script>
import BasePopover from './BasePopover.vue';

// 下拉菜单：管理 / 视图 / 行内 ⋯ / 随机拆分按钮的 ▾ 半边共用同一份实现，
// 行内 ⋯ 与右键菜单也因此能共用同一份菜单项定义。
export default {
  name: 'BaseMenu',
  components: { BasePopover },
  props: {
    // 每项是 { id, label, shortcut?, danger?, disabled? }，
    // 或 { heading } 分组标题，或 { divider: true } 分隔线。
    items: { type: Array, default: () => [] },
    anchor: { type: Object, default: null },
    position: { type: Object, default: null },
    align: { type: String, default: 'start' },
    minWidth: { type: Number, default: 200 },
    label: { type: String, default: '菜单' }
  },
  emits: ['select', 'close'],
  data() {
    return { activeIndex: -1, itemEls: {} };
  },
  computed: {
    selectableIndexes() {
      return this.items
        .map((item, index) => ({ item, index }))
        .filter(({ item }) => !item.divider && !item.heading && !item.disabled)
        .map(({ index }) => index);
    }
  },
  methods: {
    registerItem(el, index) {
      if (el) this.itemEls[index] = el;
      else delete this.itemEls[index];
    },
    select(item) {
      if (item.disabled) return;
      this.$emit('select', item);
      this.$emit('close');
    },
    move(step) {
      const order = this.selectableIndexes;
      if (order.length === 0) return;
      const current = order.indexOf(this.activeIndex);
      const next = current === -1
        ? (step > 0 ? 0 : order.length - 1)
        : (current + step + order.length) % order.length;
      this.activeIndex = order[next];
      this.itemEls[this.activeIndex]?.focus({ preventScroll: true });
    },
    onKeydown(event) {
      // 菜单打开期间把方向键和回车吃掉，列表的 J/K/F/W/T 才不会同时触发。
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        event.stopPropagation();
        this.move(1);
      } else if (event.key === 'ArrowUp') {
        event.preventDefault();
        event.stopPropagation();
        this.move(-1);
      } else if (event.key === 'Enter' || event.key === ' ') {
        const item = this.items[this.activeIndex];
        if (!item) return;
        event.preventDefault();
        event.stopPropagation();
        this.select(item);
      }
    }
  }
};
</script>

<style>
.base-menu {
  padding: 6px;
}

.base-menu__list {
  display: flex;
  flex-direction: column;
}

.base-menu__heading {
  padding: 9px 10px 4px;
  color: var(--text-muted);
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.08em;
}

.base-menu__divider {
  height: 1px;
  margin: 5px 4px;
  background: var(--hairline-faint);
}

.base-menu__item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 7px 10px;
  border: 0;
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-primary);
  font-size: 12.5px;
  text-align: left;
  cursor: pointer;
  outline: none;
}

.base-menu__item--active:not(:disabled) {
  background: var(--accent-soft);
  color: var(--accent-text);
}

.base-menu__item--danger {
  color: var(--danger-text);
}

.base-menu__item:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.base-menu__label {
  flex: 1;
  min-width: 0;
}

.base-menu__shortcut {
  color: var(--text-muted);
  font-family: var(--font-mono);
  font-size: 11px;
}
</style>
