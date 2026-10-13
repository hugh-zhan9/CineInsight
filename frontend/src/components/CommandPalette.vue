<template>
  <DialogShell
    v-if="open"
    title="命令面板"
    size="md"
    close-on-overlay
    class="command-palette-modal"
    data-test="command-palette"
    @close="$emit('close')"
  >
    <div class="command-palette" @keydown="handleKeydown">
      <input
        ref="queryInput"
        v-model="query"
        type="text"
        class="search-input"
        data-test="command-palette-input"
        placeholder="搜索命令、视图、视频…"
        aria-label="命令面板"
        autocomplete="off"
      />
      <p class="command-palette__legend">↑ ↓ 选择 · 回车执行 · Esc 关闭</p>

      <div class="command-palette__list" role="listbox" data-test="command-palette-list">
        <template v-for="group in groupedEntries" :key="group.key">
          <p class="command-palette__group">{{ group.label }}</p>
          <button
            v-for="entry in group.entries"
            :key="entry.command.id"
            type="button"
            :class="['btn', 'btn--ghost', 'command-palette__item', {
              'command-palette__item--active': entry.index === activeIndex,
              'command-palette__item--disabled': !entry.enabled
            }]"
            :data-test="`command-palette-item-${entry.command.id}`"
            :disabled="!entry.enabled"
            :aria-selected="entry.index === activeIndex"
            role="option"
            @mousemove="activeIndex = entry.index"
            @click="execute(entry)"
          >{{ entry.command.label }}</button>
        </template>

        <p v-if="!flatEntries.length" class="command-palette__empty" data-test="command-palette-empty">
          没有匹配的命令。
        </p>
        <p v-if="videoError" class="command-palette__error" role="alert" data-test="command-palette-video-error">
          {{ videoError }}
        </p>
      </div>
    </div>
  </DialogShell>
</template>

<script>
import { SearchLibraryVideoPage } from '../../wailsjs/go/main/App';
import DialogShell from './ui/DialogShell.vue';
import { COMMAND_GROUPS, filterCommands, isCommandEnabled } from '../utils/commandRegistry.js';
import { notifyError, notifySuccess } from '../utils/feedback.js';

// 视频组一次最多 8 条（D-029）：面板是"快速跳过去"，不是第二个片库列表。
const VIDEO_RESULT_LIMIT = 8;
// 输入 ≥ 2 字才搜视频，且防抖一次，免得每敲一个字都打一次后端。
const VIDEO_QUERY_MIN_LENGTH = 2;

// 命令面板（D-029）。面板自己只做三件事：过滤注册表、按 ≥2 字搜视频、执行选中项。
// 所有业务动作都由各页面注册进 commandRegistry，面板不认识它们。
// 外壳是 DialogShell（标题、×、Esc 与焦点归还同所有对话框）；面板里有输入框，点遮罩照旧关闭，所以显式传 close-on-overlay。
// 面板只在没有别的对话框时打开（appCommands.js），执行前先关，不会和别的对话框叠在一起。
export default {
  name: 'CommandPalette',
  components: { DialogShell },
  props: {
    open: { type: Boolean, default: false },
    // 打开视频详情抽屉：走片库页既有的 openPreview，面板不新建打开路径。
    openVideo: { type: Function, required: true },
    // 执行前的准备（可选）：App 用它把宿主页的固定命令先切到宿主页再执行（详细设计 §6.1）。
    // 返回（或 resolve 为）false 表示放弃执行。面板自己不认识任何页面。
    beforeRun: { type: Function, default: null },
    // 视频搜索防抖毫秒数；测试里设 0 直接同步走。
    searchDebounceMs: { type: Number, default: 180 }
  },
  emits: ['close'],
  data() {
    return {
      query: '',
      activeIndex: 0,
      videoResults: [],
      videoError: ''
    };
  },
  computed: {
    // 视频组不进注册表：它随输入变化、只在面板打开时存在。
    videoCommands() {
      return this.videoResults.map(video => ({
        id: `video:${video.id}`,
        group: 'video',
        label: video.display_title || video.name,
        keywords: [video.name],
        run: () => this.openVideo(video)
      }));
    },
    // 分组顺序固定，组内顺序来自 filterCommands 的权重排序；index 按渲染顺序连续编号，
    // 方向键的上下移动才和眼睛看到的顺序一致。
    groupedEntries() {
      const matched = [...filterCommands(this.query), ...this.videoCommands];
      const byGroup = new Map(COMMAND_GROUPS.map(group => [group.key, []]));
      for (const command of matched) byGroup.get(command.group)?.push(command);
      const groups = [];
      let index = 0;
      for (const group of COMMAND_GROUPS) {
        const commands = byGroup.get(group.key) || [];
        if (!commands.length) continue;
        groups.push({
          key: group.key,
          label: group.label,
          entries: commands.map(command => ({ command, index: index++, enabled: isCommandEnabled(command) }))
        });
      }
      return groups;
    },
    flatEntries() {
      return this.groupedEntries.flatMap(group => group.entries);
    },
    activeEntry() {
      return this.flatEntries.find(entry => entry.index === this.activeIndex) || null;
    }
  },
  watch: {
    open: {
      immediate: true,
      handler(open) {
        if (!open) {
          clearTimeout(this._videoTimer);
          return;
        }
        this.query = '';
        this.activeIndex = 0;
        this.videoResults = [];
        this.videoError = '';
        this.$nextTick(() => this.$refs.queryInput?.focus());
      }
    },
    query() {
      this.activeIndex = 0;
      this.scheduleVideoSearch();
    },
    // 命令来源随时在变（任务组跟着事件刷新、视频结果异步到达），
    // 高亮项要留在列表内，否则回车会打在空处。
    flatEntries() {
      this.ensureActiveEntry();
    }
  },
  beforeUnmount() {
    clearTimeout(this._videoTimer);
  },
  methods: {
    handleKeydown(event) {
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        this.moveActive(1);
      } else if (event.key === 'ArrowUp') {
        event.preventDefault();
        this.moveActive(-1);
      } else if (event.key === 'Enter') {
        event.preventDefault();
        this.execute(this.activeEntry);
      }
    },
    // 灰显项跳过：它们可见但不可执行，停在上面回车会什么都不发生。
    moveActive(delta) {
      const entries = this.flatEntries.filter(entry => entry.enabled);
      if (!entries.length) return;
      const current = entries.findIndex(entry => entry.index === this.activeIndex);
      const next = current < 0
        ? (delta > 0 ? 0 : entries.length - 1)
        : Math.min(entries.length - 1, Math.max(0, current + delta));
      this.activeIndex = entries[next].index;
    },
    ensureActiveEntry() {
      const entries = this.flatEntries.filter(entry => entry.enabled);
      if (!entries.length) return;
      if (entries.some(entry => entry.index === this.activeIndex)) return;
      this.activeIndex = entries[0].index;
    },
    // 先关面板再执行：run() 往往切页或开抽屉，面板留在最上层会挡住结果。
    // 成功统一提示「已执行：<label>」（D-PC60、APP-12）；run() 返回 false 表示用户中途放弃
    // （例如离开设置页时选了继续编辑），不算执行，也不提示。
    async execute(entry) {
      if (!entry || !entry.enabled) return;
      const command = entry.command;
      this.$emit('close');
      try {
        if (this.beforeRun && await this.beforeRun(command) === false) return;
        const result = await command.run();
        if (result === false) return;
        notifySuccess(`已执行：${command.label}`);
      } catch (err) {
        notifyError(`执行「${command.label}」失败：${err}`);
      }
    },
    scheduleVideoSearch() {
      clearTimeout(this._videoTimer);
      const keyword = this.query.trim();
      if (keyword.length < VIDEO_QUERY_MIN_LENGTH) {
        this.videoResults = [];
        this.videoError = '';
        return;
      }
      this._videoTimer = setTimeout(() => this.searchVideos(keyword), this.searchDebounceMs);
    },
    async searchVideos(keyword) {
      const token = Symbol('command-palette-video');
      this._videoToken = token;
      try {
        const page = await SearchLibraryVideoPage({
          filter: {
            search_mode: 'file',
            keyword,
            smart_view: '',
            tag_ids: [],
            min_size: 0,
            max_size: 0,
            min_height: 0,
            max_height: 0,
            min_rating: null,
            max_rating: null,
            sort_mode: 'balanced'
          },
          limit: VIDEO_RESULT_LIMIT
        });
        if (this._videoToken !== token) return;
        this.videoResults = (page?.videos || []).slice(0, VIDEO_RESULT_LIMIT);
        this.videoError = '';
      } catch (err) {
        // 视频搜索失败只让视频组报错，其余三组照常可用（4.7.4 边界）。
        if (this._videoToken !== token) return;
        this.videoResults = [];
        this.videoError = `视频搜索失败：${err}`;
      }
    }
  }
};
</script>

<style scoped>
/* 尺寸由 DialogShell 的 md（560）给出；正文左右收窄一点，列表项与输入框对齐。 */
:deep(.command-palette-modal .ui-dialog__body) { padding: 0 var(--space-4) var(--space-3); }
.command-palette__legend {
  margin: var(--space-2) 2px var(--space-1);
  color: var(--text-muted);
  font-size: var(--fs-caption);
}
.command-palette__list {
  max-height: 52vh;
  overflow-y: auto;
  overscroll-behavior: contain;
}
.command-palette__group {
  margin: 10px 2px 4px;
  color: var(--text-muted);
  font-size: var(--fs-caption);
  font-weight: 600;
  letter-spacing: 0.04em;
}
/* 列表项是 .btn .btn--ghost：这里改成整行、左对齐、可省略，字重回到常规；高亮项（键盘或鼠标移到的那一项）
   压过 .btn--ghost 的悬停底色（components.css 在构建产物里排在后面，选择器要多一层）。 */
.command-palette .command-palette__item {
  width: 100%;
  height: auto;
  display: block;
  padding: 7px 10px;
  border: 0;
  color: var(--text-primary);
  font-size: var(--fs-secondary);
  font-weight: var(--fw-regular);
  text-align: left;
  overflow: hidden;
  text-overflow: ellipsis;
}
.command-palette .command-palette__item--active,
.command-palette .command-palette__item--active:hover:not(:disabled) {
  background: var(--accent-soft);
  color: var(--accent-text);
}
/* 灰显项保持原来的样子：浅字、不变淡、不显示禁止光标（.btn:disabled 的半透明在这里会淡得看不清）。 */
.command-palette .command-palette__item--disabled:disabled {
  color: var(--text-muted);
  opacity: 1;
  cursor: default;
}
.command-palette__empty,
.command-palette__error {
  padding: 10px 2px 6px;
  font-size: var(--fs-secondary);
}
.command-palette__empty { color: var(--text-muted); }
.command-palette__error { color: var(--danger-color); }
</style>
