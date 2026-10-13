<template>
  <div class="ui-empty">
    <span v-if="icon" class="ui-empty__icon"><AppIcon :name="icon" :size="24" /></span>
    <p class="ui-empty__title">{{ title }}</p>
    <p v-if="message" class="ui-empty__message" :data-test="messageDataTest || undefined">{{ message }}</p>
    <div v-if="$slots.actions" class="ui-empty__actions"><slot name="actions" /></div>
  </div>
</template>

<script>
import AppIcon from './AppIcon.vue';

// 空状态（D-UD-06）：永远给下一步——actions 插槽放「添加扫描目录」「清除筛选」之类的按钮。
// icon 是图标集里的名字（可选）。只描述「这里没有东西」，加载中用 SkeletonBlock，出错用 AppBanner 或提示。
// data-test 等属性落在外层；messageDataTest 落在说明那一行上（宿主给说明文字单独挂测试钩子用）。
export default {
  name: 'EmptyState',
  components: { AppIcon },
  props: {
    title: { type: String, required: true },
    message: { type: String, default: '' },
    icon: { type: String, default: '' },
    messageDataTest: { type: String, default: '' }
  }
};
</script>

<style scoped>
.ui-empty {
  display: grid;
  justify-items: center;
  gap: var(--space-2);
  padding: var(--space-6) var(--space-5);
  border: 1px dashed var(--border-color);
  border-radius: var(--radius-lg);
  background: var(--surface-faint);
  color: var(--text-secondary);
  text-align: center;
}
.ui-empty__icon {
  display: inline-grid;
  place-items: center;
  width: 44px;
  height: 44px;
  margin-bottom: var(--space-1);
  border-radius: var(--radius-pill);
  background: var(--panel-muted-bg);
  color: var(--text-muted);
}
.ui-empty__title { margin: 0; color: var(--text-primary); font-size: var(--fs-body); font-weight: var(--fw-semibold); }
.ui-empty__message { max-width: 520px; margin: 0; font-size: var(--fs-secondary); line-height: 1.6; }
.ui-empty__actions { display: flex; flex-wrap: wrap; justify-content: center; gap: var(--space-2); margin-top: var(--space-2); }
</style>
