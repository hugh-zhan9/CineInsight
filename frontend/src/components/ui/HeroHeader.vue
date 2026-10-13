<template>
  <header :class="['ui-hero-header', { 'ui-hero-header--quiet': quiet }]">
    <p v-if="eyebrow || $slots.eyebrow" class="ui-hero-header__eyebrow"><slot name="eyebrow">{{ eyebrow }}</slot></p>
    <component :is="`h${headingLevel}`" class="ui-hero-header__title" :data-test="titleDataTest || undefined"><slot name="title">{{ title }}</slot></component>
    <p v-if="meta" class="ui-hero-header__meta" :data-test="metaDataTest || undefined">{{ meta }}</p>
    <slot />
    <div v-if="$slots.actions" class="ui-hero-header__actions"><slot name="actions" /></div>
  </header>
</template>

<script>
// 观看侧大图页的页头（D-UD-06）：首页与整页详情的大图区文字层，算作该页的页头（守卫 6）。
// 只管文字排版，背景图仍由宿主放（RecapBackdrop 之类），本组件放在背景里面。标题 --fs-hero（40），
// quiet 时降到 --fs-display（28，引导与空态）。文字颜色用大图压暗层上的 --recap-hero-* 变量，两主题都是白字。
// eyebrow 插槽可以带图标；默认插槽放标题与动作之间的附加信息（评分、标签行等）。
// 标题默认是 h1（headingLevel 改级别：大图区不是页面标题、页面另有 h1 时用 2 或 3）；title 插槽可换成带标记的标题。
// titleDataTest / metaDataTest 落在标题与 meta 行上，给宿主保留原有的测试钩子（home-hero-title 之类）。
export default {
  name: 'HeroHeader',
  props: {
    title: { type: String, default: '' },
    eyebrow: { type: String, default: '' },
    meta: { type: String, default: '' },
    quiet: { type: Boolean, default: false },
    headingLevel: { type: Number, default: 1, validator: level => Number.isInteger(level) && level >= 1 && level <= 6 },
    titleDataTest: { type: String, default: '' },
    metaDataTest: { type: String, default: '' }
  }
};
</script>

<style scoped>
.ui-hero-header { display: grid; gap: var(--space-3); max-width: 760px; min-width: 0; color: var(--recap-hero-fg); }
.ui-hero-header__eyebrow {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  margin: 0;
  color: var(--recap-hero-muted);
  font-size: var(--fs-secondary);
  font-weight: var(--fw-semibold);
  letter-spacing: 0.06em;
}
.ui-hero-header__title {
  display: -webkit-box;
  margin: 0;
  overflow: hidden;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  font-size: var(--fs-hero);
  font-weight: var(--fw-bold);
  line-height: 1.12;
  letter-spacing: -0.02em;
  text-shadow: 0 2px 24px var(--recap-scrim);
}
.ui-hero-header--quiet .ui-hero-header__title { font-size: var(--fs-display); }
.ui-hero-header__meta { margin: 0; color: var(--recap-hero-muted); font-size: var(--fs-body); }
.ui-hero-header__actions { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-3); margin-top: var(--space-1); }
</style>
