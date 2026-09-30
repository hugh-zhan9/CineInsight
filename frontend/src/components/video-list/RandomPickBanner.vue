<template>
  <!-- 随机播放的结果条（D-PC44、PLAY-08）：说清抽中了哪一部、为什么，30 秒内「换一个」不计入统计。 -->
  <div v-if="randomPlay && randomPlay.active" class="random-pick-banner" data-test="random-play-banner">
    <div class="random-pick-banner__text">
      <strong>正在随机播放</strong>
      <span class="random-pick-banner__name" data-test="random-play-name">「{{ randomPlay.videoName }}」</span>
      <span v-if="randomPlay.reason" class="random-pick-banner__reason">{{ randomPlay.reason }}</span>
      <span class="random-pick-banner__count" data-test="random-play-hint">
        {{ randomPlay.rerollable ? '30 秒内「换一个」不计入这次播放' : '这次随机已计入播放记录，「换一个」会重新随机一部' }}
      </span>
    </div>
    <div class="random-pick-banner__actions">
      <button type="button" class="btn-secondary btn-compact" :disabled="randomPlay.loading" data-test="random-play-reroll" @click="$emit('reroll')">
        {{ randomPlay.loading ? '换一个中...' : '换一个' }}
      </button>
      <button type="button" class="btn-secondary btn-compact" :disabled="randomPlay.loading" data-test="random-play-dismiss" @click="$emit('dismiss-play')">关闭</button>
    </div>
  </div>

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
// 「随机 N 部」批次接管主列表时的状态条，以及随机播放之后的结果条。
// 批次、随机播放的令牌与 30 秒窗口都归片库页维护，这里只呈现与转发动作。
export default {
  name: 'RandomPickBanner',
  props: {
    randomPick: { type: Object, required: true },
    randomPickSize: { type: Number, default: 10 },
    videoCount: { type: Number, default: 0 },
    // { active, videoName, reason, rerollable, loading }；rerollable 表示仍在 30 秒窗口内。
    randomPlay: { type: Object, default: null }
  },
  emits: ['reshuffle', 'exit', 'reroll', 'dismiss-play']
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

.random-pick-banner__name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-primary);
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
