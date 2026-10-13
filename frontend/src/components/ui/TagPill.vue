<template>
  <button
    v-if="clickable"
    type="button"
    class="ui-tag-pill ui-tag-pill--clickable"
    @click="$emit('click', $event)"
  ><span v-if="$slots.leading" class="ui-tag-pill__leading"><slot name="leading" /></span><span class="ui-tag-pill__label">{{ label }}</span></button>
  <span v-else class="ui-tag-pill"><span v-if="$slots.leading" class="ui-tag-pill__leading"><slot name="leading" /></span><span class="ui-tag-pill__label">{{ label }}</span></span>
</template>

<script>
// 用户标签（D-UD-06）：用户自己的分类。clickable 时是按钮（跳到这个标签的结果），发 click；
// 否则是只读的文字胶囊。数量与状态用 AppBadge，可叠加的筛选条件用 FilterChip。
// leading 插槽放文字前面的小东西：颜色圆点、头像、图标（装饰性的图片写 alt=""）。
export default {
  name: 'TagPill',
  props: {
    label: { type: String, required: true },
    clickable: { type: Boolean, default: false }
  },
  emits: ['click']
};
</script>

<style scoped>
.ui-tag-pill {
  display: inline-flex;
  flex: none;
  align-items: center;
  gap: var(--space-1);
  max-width: 180px;
  height: 22px;
  padding: 0 var(--space-2);
  border: 1px solid var(--hairline);
  border-radius: var(--radius-pill);
  background: var(--panel-subtle-bg);
  color: var(--text-secondary);
  font: inherit;
  font-size: var(--fs-caption);
  font-weight: var(--fw-medium);
  line-height: 1;
  white-space: nowrap;
}
.ui-tag-pill__leading { display: inline-flex; flex: none; align-items: center; }
.ui-tag-pill__label { min-width: 0; overflow: hidden; text-overflow: ellipsis; }
.ui-tag-pill--clickable { cursor: pointer; transition: border-color var(--transition), color var(--transition), background var(--transition); }
.ui-tag-pill--clickable:hover { border-color: var(--accent-border); background: var(--accent-soft); color: var(--accent-text); }
.ui-tag-pill--clickable:focus-visible { outline: 2px solid var(--accent-color); outline-offset: 2px; }
</style>
