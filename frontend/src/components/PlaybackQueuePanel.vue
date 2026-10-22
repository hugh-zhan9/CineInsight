<template>
  <section v-if="snapshot?.current || error || open" class="queue-dock" data-test="queue-dock">
    <div v-if="snapshot?.current" class="queue-current">
      <button type="button" class="btn-plain queue-open queue-open--dock" :title="`打开「${snapshot.current.title}」的详情`" data-test="queue-current-open" @click="$emit('open-video', snapshot.current.video_id)">
        <span class="queue-thumb queue-thumb--dock" aria-hidden="true"><img v-if="!thumbFailed[snapshot.current.video_id]" :src="thumb(snapshot.current)" alt="" decoding="async" data-test="queue-current-thumb" @error="markThumbFailed(snapshot.current)" /></span>
        <strong>{{ snapshot.current.title }}</strong>
      </button><span>{{ unavailable ? '队列暂不可用' : statusLabel(snapshot.state.status) }} · {{ playerLabel(snapshot.state.player) }}</span>
      <button v-if="snapshot.state.player !== 'system' && snapshot.state.active_token" type="button" class="btn btn--secondary btn--sm" :disabled="busy || unavailable" @click="control">{{ snapshot.state.status === 'paused' ? '继续' : '暂停' }}</button>
      <button type="button" class="btn btn--primary btn--sm" :disabled="busy || unavailable || !snapshot.state.active_token" data-test="queue-next" @click="next">下一项</button>
      <button type="button" class="btn btn--secondary btn--sm" :disabled="busy || unavailable || !snapshot.state.active_token" data-test="queue-stop" @click="stop">停止队列</button>
      <button type="button" class="btn btn--secondary btn--sm" @click="$emit('open')">查看队列</button>
      <button type="button" class="btn btn--secondary btn--sm" @click="$emit('open-video', snapshot.current.video_id)">原片详情</button>
    </div>
    <p v-if="error" role="alert" data-test="queue-error">{{ error }} <button type="button" class="btn btn--secondary btn--sm" @click="load(cursor, history)">刷新</button></p>
    <button v-if="cleanupPending" type="button" class="btn btn--secondary btn--sm" data-test="queue-cleanup-retry" :disabled="busy" @click="stop">重试停止队列</button>
    <p v-if="snapshot?.warning" role="alert">{{ snapshot.warning }}</p>
    <p v-if="snapshot?.state.last_error_message" role="alert">{{ snapshot.state.last_error_message }}</p>
    <QueueInlinePlayer v-if="snapshot?.session" :key="snapshot.session.token" :session="snapshot.session" @error="error = $event" />
  </section>
  <DialogShell v-if="open" :title="`待播队列（${snapshot?.total ?? '—'} 项）`" size="xl" @close="$emit('close')">
    <section class="queue-panel" data-test="queue-panel">
      <p v-if="loading" role="status">正在读取…</p>
      <p v-if="error" role="alert">{{ error }}</p>
      <template v-if="snapshot">
        <div class="queue-settings">
          <label class="queue-field">播放器 <select class="select-input queue-player" :value="snapshot.state.player" :disabled="busy || unavailable || loading" data-test="queue-player" @change="configure($event.target.value, snapshot.state.autoplay)"><option v-for="cap in snapshot.capabilities" :key="cap.player" :value="cap.player" :disabled="!cap.available">{{ playerLabel(cap.player) }}{{ !cap.available ? ' · 不可用' : '' }}</option></select></label>
          <label class="queue-field"><input type="checkbox" :checked="snapshot.state.player !== 'system' && snapshot.state.autoplay" :disabled="busy || unavailable || loading || snapshot.state.player === 'system'" data-test="queue-autoplay" @change="configure(snapshot.state.player, $event.target.checked)" /> 自动连播</label>
          <button type="button" class="btn btn--danger btn--sm" :disabled="busy || unavailable || loading || !snapshot.total" data-test="queue-clear" @click="clear">清空队列…</button>
        </div>
        <p v-if="snapshot.state.player === 'system'">系统默认播放器仅支持手动下一项。停止队列后，请在系统播放器中停止当前视频。</p>
        <p v-else-if="snapshot.state.player === 'iina'">使用本次专用 IINA 窗口，停止队列会关闭它。请在此队列调整观看顺序。</p>
        <p v-else>关闭此列表仍会继续播放；播放器和停止按钮留在主窗口。</p>
        <p v-for="cap in snapshot.capabilities.filter(item => !item.available)" :key="cap.player">{{ playerLabel(cap.player) }}：{{ cap.reason }}</p>
        <ol class="queue-items">
          <li v-for="(item, index) in snapshot.items" :key="item.id" :class="{ current: item.id === snapshot.current?.id }" :data-test="`queue-item-${item.id}`">
            <!-- 缩略图与片名是一个按钮：进这部片的详情，并关掉队列列表（否则详情页压在对话框下面）。 -->
            <button type="button" class="btn-plain queue-open" :title="`打开「${item.title}」的详情`" :data-test="`queue-item-open-${item.id}`" @click="openItem(item)">
              <span class="queue-thumb" aria-hidden="true"><img v-if="!thumbFailed[item.video_id]" :src="thumb(item)" alt="" loading="lazy" decoding="async" :data-test="`queue-item-thumb-${item.id}`" @error="markThumbFailed(item)" /></span>
              <span class="queue-item__title">{{ item.title }}<small v-if="!item.source_available"> · 原片不可用</small></span>
            </button>
            <button type="button" class="btn btn--primary btn--sm" :disabled="busy || unavailable || loading" @click="play(item)">播放</button>
            <button type="button" class="btn btn--secondary btn--sm" :disabled="busy || unavailable || loading || (index === 0 && !history.length)" @click="moveAdjacent(item, index, -1)">上移</button>
            <button type="button" class="btn btn--secondary btn--sm" :disabled="busy || unavailable || loading || (index === snapshot.items.length - 1 && !snapshot.has_more)" @click="moveAdjacent(item, index, 1)">下移</button>
            <button type="button" class="btn btn--secondary btn--sm" :disabled="busy || unavailable || loading" @click="move(item, 0)">移至末尾</button>
            <button type="button" class="btn btn--secondary btn--sm" :disabled="busy || unavailable || loading" @click="remove(item)">移除</button>
          </li>
        </ol>
        <p v-if="!loading && !snapshot.total">从视频行、已选操作或作品集加入想看的视频。</p>
        <footer><button type="button" class="btn btn--secondary btn--sm" :disabled="loading || !history.length" data-test="queue-previous-page" @click="previousPage">上一页</button><button type="button" class="btn btn--secondary btn--sm" :disabled="loading || !snapshot.has_more" data-test="queue-next-page" @click="nextPage">下一页</button></footer>
      </template>
    </section>
  </DialogShell>
</template>
<script>
import { GetPlaybackQueue, EditPlaybackQueue, PlayPlaybackQueue, NextPlaybackQueue, StopPlaybackQueue, ControlPlaybackQueue } from '../../wailsjs/go/main/App';
import DialogShell from './ui/DialogShell.vue';
import QueueInlinePlayer from './QueueInlinePlayer.vue';
import { runtimeEventsMixin } from './video-list/runtimeEvents.js';
import { confirmAction } from '../utils/feedback.js';
import { queueStatusLabel, queuePlayerLabel } from '../utils/playbackQueue.js';
import { videoThumbnail } from '../utils/recap.js';
const firstCursor = () => ({ revision: 0, cursor_position: 0, cursor_id: 0 });
export default {
  name: 'PlaybackQueuePanel',
  components: { DialogShell, QueueInlinePlayer }, mixins: [runtimeEventsMixin],
  props: { open: Boolean }, emits: ['open', 'close', 'open-video'],
  data: () => ({ snapshot: null, error: '', unavailable: false, cleanupPending: false, cleanupToken: '', loading: false, busy: false, generation: 0, cursor: firstCursor(), history: [], thumbFailed: {} }),
  mounted() { this.registerRuntimeEvent('playback-queue-state', () => this.load(this.cursor, this.history)); this.load(); },
  beforeUnmount() { this.generation++; },
  watch: { open(value) { if (value) this.load(this.cursor, this.history); } },
  methods: {
    statusLabel: queueStatusLabel, playerLabel: queuePlayerLabel,
    // 缩略图按视频取（同一部片可以排进队列好几次）；取不到（还没生成、视频已删）就留一块底色占位。
    thumb(item) { return videoThumbnail(Number(item?.video_id || 0)); },
    markThumbFailed(item) { this.thumbFailed = { ...this.thumbFailed, [item.video_id]: true }; },
    openItem(item) { this.$emit('open-video', item.video_id); this.$emit('close'); },
    async load(cursor = firstCursor(), history = []) {
      const generation = ++this.generation; this.loading = true;
      try {
        const result = await GetPlaybackQueue({ ...cursor, limit: 50 });
        if (generation !== this.generation) return;
        this.snapshot = result; this.cursor = { ...cursor }; this.history = [...history]; this.error = ''; this.unavailable = false; this.cleanupPending = false; this.cleanupToken = '';
      } catch (error) {
        if (generation !== this.generation) return;
        if (String(error).includes('queue_changed') && cursor.cursor_id) return this.load();
        const message = String(error);
        const cleanup = message.match(/queue_cleanup_pending:([a-f0-9]{32}):(.+)$/);
        this.cleanupPending = Boolean(cleanup);
        this.cleanupToken = cleanup?.[1] || '';
        this.error = cleanup ? cleanup[2] : message;
        if (cleanup || /queue_unavailable|playback_quiesced/.test(message)) { this.unavailable = true; if (this.snapshot) this.snapshot = { ...this.snapshot, session: null }; }
      } finally { if (generation === this.generation) this.loading = false; }
    },
    async perform(action) {
      if (this.busy) return; this.busy = true; this.error = '';
      try { await action(); await this.load(); }
      catch (error) { const message = String(error); await this.load(); this.error = message; }
      finally { this.busy = false; }
    },
    edit(change) { const expected_revision = this.snapshot.state.revision; return this.perform(() => EditPlaybackQueue({ ...change, expected_revision })); },
    play(item) { const revision = this.snapshot.state.revision; return this.perform(() => PlayPlaybackQueue(item.id, revision)); },
    next() { const state = { ...this.snapshot.state }; return this.perform(() => NextPlaybackQueue(state.active_token, state.revision)); },
    stop() { const token = this.cleanupPending ? this.cleanupToken : this.snapshot.state.active_token; return this.perform(() => StopPlaybackQueue(token)); },
    control() { const state = { ...this.snapshot.state }; return this.perform(() => ControlPlaybackQueue(state.active_token, state.status === 'paused' ? 'resume' : 'pause')); },
    async configure(player, autoplay) {
      const revision = this.snapshot.state.revision;
      if (this.snapshot.state.active_token && player !== this.snapshot.state.player && !await confirmAction({ title: '切换播放器', message: '切换会停止当前队列播放。', confirmText: '切换' })) return;
      return this.perform(() => EditPlaybackQueue({ expected_revision: revision, action: 'configure', player, autoplay: player === 'system' ? false : autoplay }));
    },
    async clear() { const revision = this.snapshot.state.revision; const count = this.snapshot.total; if (!await confirmAction({ title: '清空待播队列', message: `移除队列中的 ${count} 项并停止当前队列播放，原视频不会删除。`, confirmText: '清空' })) return; return this.perform(() => EditPlaybackQueue({ action: 'clear', expected_revision: revision })); },
    async remove(item) { const revision = this.snapshot.state.revision; if (item.id === this.snapshot.current?.id && !await confirmAction({ title: '移除当前项', message: '将停止当前队列播放，原视频不会删除。', confirmText: '移除' })) return; return this.perform(() => EditPlaybackQueue({ action: 'remove', entry_id: item.id, expected_revision: revision })); },
    move(item, beforeEntryID) { return this.edit({ action: 'move', entry_id: item.id, before_entry_id: beforeEntryID }); },
    moveAdjacent(item, index, direction) {
      const page = this.snapshot; const revision = page.state.revision;
      const previousCursor = this.history[this.history.length - 1];
      return this.perform(async () => {
        let entryID = item.id; let beforeID;
        if (direction < 0) {
          let previous = page.items[index - 1];
          if (!previous && previousCursor) {
            const result = await GetPlaybackQueue({ ...previousCursor, revision, limit: 50 }); previous = result.items[result.items.length - 1];
          }
          if (!previous) throw new Error('queue_changed: 已无上一项，请刷新队列');
          beforeID = previous.id;
        } else {
          let next = page.items[index + 1];
          if (!next && page.has_more) {
            const result = await GetPlaybackQueue({ revision, cursor_id: item.id, cursor_position: item.position, limit: 1 }); next = result.items[0];
          }
          if (!next) throw new Error('queue_changed: 已无下一项，请刷新队列');
          entryID = next.id; beforeID = item.id;
        }
        await EditPlaybackQueue({ action: 'move', entry_id: entryID, before_entry_id: beforeID, expected_revision: revision });
      });
    },
    nextPage() { return this.load({ revision: this.snapshot.state.revision, cursor_id: this.snapshot.cursor_id, cursor_position: this.snapshot.cursor_position }, [...this.history, this.cursor]); },
    previousPage() { const history = [...this.history]; const cursor = history.pop(); return this.load(cursor, history); }
  }
};
</script>
<style scoped>
.queue-dock { flex: 0 0 auto; margin: 6px 20px; max-height: 45vh; overflow: auto; }
.queue-current { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; padding: 8px 10px; border: 1px solid var(--hairline); background: var(--panel-bg); border-radius: var(--radius-md); font-size: var(--fs-caption); }
.queue-current strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 30vw; }
.queue-dock p { margin: 6px; font-size: var(--fs-caption); }
.queue-panel { display: grid; gap: var(--space-3); color: var(--text-primary); }
.queue-panel footer, .queue-settings { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.queue-panel p, .queue-panel small { margin: 0; font-size: var(--fs-caption); color: var(--text-secondary); }
.queue-panel [role="alert"], .queue-dock [role="alert"] { color: var(--danger-text); }
.queue-items { list-style: none; margin: 0; padding: 0; border-top: 1px solid var(--hairline); }
.queue-items li { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; padding: 8px 6px; border-bottom: 1px solid var(--hairline); }
.queue-items li.current { background: var(--panel-muted-bg); }
.queue-open { display: flex; align-items: center; gap: 8px; min-width: 0; border-radius: var(--radius-sm); }
.queue-items .queue-open { flex: 1 1 260px; }
.queue-open--dock { flex: 0 1 auto; }
.queue-item__title { flex: 1 1 auto; min-width: 0; overflow-wrap: anywhere; font-size: var(--fs-secondary); }
.queue-open:hover .queue-item__title, .queue-open:hover strong { color: var(--accent-text); text-decoration: underline; }
.queue-items li.current .queue-item__title { color: var(--accent-text); font-weight: var(--fw-semibold); }
.queue-thumb { flex: 0 0 auto; display: block; width: 96px; aspect-ratio: 16 / 9; overflow: hidden; border-radius: var(--radius-sm); background: var(--thumb-fallback-bg); }
.queue-thumb--dock { width: 64px; }
.queue-thumb img { display: block; width: 100%; height: 100%; object-fit: cover; }
.queue-current span { color: var(--text-secondary); }
.queue-current strong { font-size: var(--fs-secondary); }
.queue-field { display: inline-flex; align-items: center; gap: 8px; font-size: var(--fs-secondary); color: var(--text-secondary); white-space: nowrap; }
.queue-field input[type="checkbox"] { width: 16px; height: 16px; margin: 0; accent-color: var(--accent-color); }
.queue-player { width: auto; min-width: 160px; }
.queue-panel footer { justify-content: center; }
</style>
