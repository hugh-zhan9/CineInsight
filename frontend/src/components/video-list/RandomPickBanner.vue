<template>
  <div v-if="randomPick.active" class="random-pick-banner" data-test="random-pick-banner">
    <div class="random-pick-banner__text">
      <strong>随机 {{ randomPickSize }} 部</strong>
      <span v-if="randomPick.reason" class="random-pick-banner__reason">{{ randomPick.reason }}</span>
      <span class="random-pick-banner__count">当前 {{ videoCount }} 条</span>
    </div>
    <div class="random-pick-banner__actions">
      <button type="button" class="btn-secondary btn-compact" :disabled="randomPick.loading" @click="$emit('reshuffle')">
        {{ randomPick.loading ? '抽取中...' : '换一批' }}
      </button>
      <button type="button" class="btn-secondary btn-compact" :disabled="randomPick.loading" @click="$emit('exit')">退出随机</button>
    </div>
  </div>
</template>

<script>
// 「随机 N 部」批次接管主列表时的状态条。批次本身归片库页维护，这里只呈现与转发动作。
export default {
  name: 'RandomPickBanner',
  props: {
    randomPick: { type: Object, required: true },
    randomPickSize: { type: Number, default: 10 },
    videoCount: { type: Number, default: 0 }
  },
  emits: ['reshuffle', 'exit']
};
</script>

<style scoped>
.random-pick-banner {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  margin: 0 0 10px;
  padding: 8px 12px;
  border: 1px solid var(--accent-border);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 13px;
}

.random-pick-banner__text {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  min-width: 0;
}

.random-pick-banner__reason,
.random-pick-banner__count {
  color: var(--text-muted);
}

.random-pick-banner__actions {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-left: auto;
}
.btn-compact {
  height: 28px;
  padding: 0 10px;
  font-size: 12px;
}
</style>
