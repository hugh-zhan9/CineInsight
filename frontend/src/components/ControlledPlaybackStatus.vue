<template>
  <div v-if="visible" class="controlled-playback-status" role="status">
    <span>IINA 精确同步 · {{ labels[state.status] || state.status }}</span>
    <span v-if="state.message">{{ state.message }}</span>
    <button type="button" class="btn btn--secondary btn--sm" @click="stop">{{ state.status === 'failed' ? '重试停止' : '停止播放' }}</button>
    <button v-if="state.status === 'failed'" type="button" class="btn btn--ghost btn--sm" @click="dismissed = true">关闭</button>
    <span v-if="error" role="alert">{{ error }}</span>
  </div>
</template>
<script>
import { GetControlledPlaybackState, StopControlledPlayback } from '../../wailsjs/go/main/App';
export default {
  name: 'ControlledPlaybackStatus',
  data: () => ({
    state: {}, dismissed: false, error: '', generation: 0, retired: false,
    labels: { starting: '正在启动', playing: '播放中', paused: '已暂停', ended: '已播完', failed: '操作未完成' }
  }),
  computed: {
    visible() { return !this.dismissed && ['starting', 'playing', 'paused', 'ended', 'failed'].includes(this.state.status); }
  },
  mounted() {
    this._off = window.runtime?.EventsOn?.('controlled-playback-state', state => {
      this.generation++;
      this.state = state;
      this.dismissed = false;
    });
    this.refresh();
  },
  beforeUnmount() {
    this.retired = true;
    this.generation++;
    this._off?.();
  },
  methods: {
    async refresh() {
      const generation = ++this.generation;
      try {
        const state = await GetControlledPlaybackState();
        if (!this.retired && generation === this.generation) this.state = state;
      } catch (error) {
        if (!this.retired && generation === this.generation) this.error = '读取精确同步状态失败，请稍后重试。';
      }
    },
    async stop() {
      this.error = '';
      try {
        await StopControlledPlayback();
        if (!this.retired) await this.refresh();
      } catch (error) {
        if (!this.retired) this.error = '停止精确同步播放失败，请重试。';
      }
    }
  }
};
</script>
<style scoped>
.controlled-playback-status { display: flex; align-items: center; gap: var(--space-2); flex-wrap: wrap; padding: var(--space-2); background: var(--bg-secondary); }
</style>
