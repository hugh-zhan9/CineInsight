<template>
  <div :class="['ui-banner', `ui-banner--${tone}`]" role="status">
    <AppIcon class="ui-banner__icon" :name="toneIcon" :size="20" />
    <div class="ui-banner__text">
      <p class="ui-banner__title">{{ title }}</p>
      <p v-if="message" class="ui-banner__message">{{ message }}</p>
    </div>
    <div v-if="$slots.actions" class="ui-banner__actions"><slot name="actions" /></div>
  </div>
</template>

<script>
import AppIcon from './AppIcon.vue';

export const BANNER_TONES = ['neutral', 'accent', 'warning', 'danger', 'success'];
const TONE_ICONS = { neutral: 'info', accent: 'info', warning: 'warning', danger: 'warning', success: 'check' };

// 页级横幅（D-UD-12）：持续状态（离线、缺组件、维护中），不是一次性的提示——一次性的用 notify。
// actions 插槽放下一步（「重新连接」「去设置」）。
export default {
  name: 'AppBanner',
  components: { AppIcon },
  props: {
    tone: { type: String, default: 'neutral', validator: tone => BANNER_TONES.includes(tone) },
    title: { type: String, required: true },
    message: { type: String, default: '' }
  },
  computed: {
    toneIcon() { return TONE_ICONS[this.tone] || 'info'; }
  }
};
</script>

<style scoped>
.ui-banner {
  display: flex;
  align-items: flex-start;
  gap: var(--space-3);
  padding: var(--space-3) var(--space-4);
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--panel-subtle-bg);
  color: var(--text-primary);
}
.ui-banner__icon { margin-top: 1px; color: var(--text-secondary); }
.ui-banner__text { display: grid; flex: 1; gap: 2px; min-width: 0; }
.ui-banner__title { margin: 0; font-size: var(--fs-secondary); font-weight: var(--fw-semibold); }
.ui-banner__message { margin: 0; color: var(--text-secondary); font-size: var(--fs-caption); line-height: 1.6; overflow-wrap: anywhere; }
.ui-banner__actions { display: flex; flex: none; flex-wrap: wrap; align-items: center; gap: var(--space-2); }
.ui-banner--accent { border-color: var(--accent-border); background: var(--accent-soft); }
.ui-banner--accent .ui-banner__icon { color: var(--accent-text); }
.ui-banner--warning { border-color: var(--warning-border); background: var(--warning-soft); }
.ui-banner--warning .ui-banner__icon { color: var(--warning-text); }
.ui-banner--danger { border-color: var(--danger-border); background: var(--danger-soft); }
.ui-banner--danger .ui-banner__icon { color: var(--danger-text); }
.ui-banner--success { border-color: var(--accent-border); background: var(--accent-soft); }
.ui-banner--success .ui-banner__icon { color: var(--success-color); }
</style>
