<template>
  <section id="settings-automatic-structure" class="settings-section">
    <h3>自动分段</h3>
    <label class="switch"><input v-model="form.auto_structure_enabled" type="checkbox" data-test="auto-structure-enabled" /><span class="slider"></span><span>空闲时自动整理全部视频</span></label>
    <p class="help-text">为已有和新增视频生成本机画面分段与代表帧，无需 AI Token，不上传内容、不生成语音字幕，也不会覆盖人工章节。</p>
    <p class="help-text">自动分段始终只在空闲时运行，开始播放或恢复操作时暂停。没有支持的空闲探测或 FFmpeg 时会等待，不影响正常浏览。</p>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-else role="status">已准备 {{ status.ready || 0 }} 部 · 待处理 {{ status.pending || 0 }} 部 · 失败 {{ status.failed || 0 }} 部 · 暂不可用 {{ status.unavailable || 0 }} 部</p>
    <p v-if="status.waiting_idle" class="help-text">{{ reason }}</p>
    <button type="button" class="btn btn--secondary btn--sm" @click="load">刷新状态</button>
  </section>
</template>
<script>
import { idleWaitReasonLabel } from '../../utils/idleScheduling.js';
import { GetVideoStructureStatus } from '../../../wailsjs/go/main/App';
import { runtimeEventsMixin } from '../video-list/runtimeEvents.js';
export default {
  name: 'AutomaticStructureSection', mixins: [runtimeEventsMixin], props: { form: { type: Object, required: true } },
  data: () => ({ status: {}, error: '' }),
  computed: { reason() { return idleWaitReasonLabel(this.status.reason); } },
  mounted() { this.load(); this.registerRuntimeEvent('video-structure-state', status => { this.status = status || {}; }); },
  beforeUnmount() { this._closed = true; },
  methods: { async load() { try { const status = await GetVideoStructureStatus(); if (!this._closed) { this.status = status || {}; this.error = ''; } } catch { if (!this._closed) this.error = '自动分段状态暂时没有读到'; } } }
};
</script>
