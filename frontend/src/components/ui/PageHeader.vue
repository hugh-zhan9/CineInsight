<template>
  <header :class="['ui-page-header', `ui-page-header--${level}`]">
    <div class="ui-page-header__row">
      <div class="ui-page-header__text">
        <component :is="level === 'page' ? 'h1' : 'h2'" class="ui-page-header__title">{{ title }}</component>
        <p v-if="subtitle" class="ui-page-header__subtitle">{{ subtitle }}</p>
      </div>
      <div v-if="$slots.actions" class="ui-page-header__actions"><slot name="actions" /></div>
    </div>
    <div v-if="$slots.below" class="ui-page-header__below"><slot name="below" /></div>
  </header>
</template>

<script>
export const PAGE_HEADER_LEVELS = ['page', 'section'];

// 页头（D-UD-06）：每个顶层页面根部只放一个（守卫 6）。标题 --fs-title（h1）；actions 插槽是右侧的页级动作，
// below 插槽放页签（AppTabs）或筛选条。观看侧大图页（首页、整页详情）用 HeroHeader。
// level="section" 只给页面内需要同样排版的大分区用（h2、--fs-section），不算页面的页头。
export default {
  name: 'PageHeader',
  props: {
    title: { type: String, required: true },
    subtitle: { type: String, default: '' },
    level: { type: String, default: 'page', validator: level => PAGE_HEADER_LEVELS.includes(level) }
  }
};
</script>

<style scoped>
.ui-page-header { display: grid; gap: var(--space-3); margin-bottom: var(--space-4); }
.ui-page-header__row { display: flex; flex-wrap: wrap; align-items: flex-end; justify-content: space-between; gap: var(--space-3) var(--space-4); }
.ui-page-header__text { display: grid; gap: var(--space-1); min-width: 0; }
.ui-page-header__title { margin: 0; color: var(--text-primary); font-size: var(--fs-title); font-weight: var(--fw-bold); line-height: 1.25; }
.ui-page-header--section .ui-page-header__title { font-size: var(--fs-section); font-weight: var(--fw-semibold); }
.ui-page-header__subtitle { margin: 0; color: var(--text-secondary); font-size: var(--fs-secondary); line-height: 1.5; }
.ui-page-header__actions { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); }
.ui-page-header__below { min-width: 0; }
</style>
