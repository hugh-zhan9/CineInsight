<script>
import { Fragment, Comment, Text, h } from 'vue';
import AppIcon from './AppIcon.vue';

function cards(nodes) {
  return nodes.flatMap(node => node.type === Fragment ? cards(node.children || []) : [Comment, Text].includes(node.type) ? [] : [node]);
}

// One responsive group at a time. Keep the row's natural height, including captions.
export default {
  name: 'MediaRail',
  inheritAttrs: false,
  data: () => ({ start: 0, capacity: 6 }),
  mounted() {
    if (typeof ResizeObserver === 'undefined') return;
    this._observer = new ResizeObserver(entries => {
      const width = entries[0]?.contentRect.width || 0;
      if (!width) return;
      const minimum = parseFloat(getComputedStyle(this.$el).getPropertyValue('--rail-card-width')) || 220;
      const capacity = Math.max(1, Math.floor((width - 72 + 14) / (minimum + 14)));
      if (capacity !== this.capacity) { this.capacity = capacity; this.start = 0; }
    });
    this._observer.observe(this.$el);
  },
  beforeUnmount() { this._observer?.disconnect(); },
  render() {
    const items = cards(this.$slots.default?.() || []);
    const start = Math.min(this.start, Math.max(0, Math.ceil(items.length / this.capacity) - 1) * this.capacity);
    const multiple = items.length > this.capacity;
    const arrow = (direction, disabled) => h('button', {
      type: 'button', class: 'media-rail__arrow btn btn--ghost', disabled,
      'aria-label': direction < 0 ? '上一组' : '下一组',
      onClick: () => { this.start = start + direction * this.capacity; }
    }, [h(AppIcon, { name: direction < 0 ? 'chevron-left' : 'chevron-right' })]);
    return h('div', { ...this.$attrs, class: [this.$attrs.class, 'media-rail', { 'media-rail--paged': multiple }] }, [
      multiple ? arrow(-1, start === 0) : null,
      h('div', { class: 'media-rail__items', style: { gridTemplateColumns: `repeat(${this.capacity}, minmax(0, 1fr))` } }, items.slice(start, start + this.capacity)),
      multiple ? arrow(1, start + this.capacity >= items.length) : null
    ]);
  }
};
</script>
<style scoped>
.media-rail { display: grid; min-width: 0; gap: 10px; align-items: center; padding: 2px; }
.media-rail--paged { grid-template-columns: 26px minmax(0, 1fr) 26px; }
.media-rail__items { display: grid; align-items: start; min-width: 0; gap: 14px; }
.media-rail__arrow { padding: 0; width: 26px; height: 44px; display: grid; place-items: center; }
</style>
