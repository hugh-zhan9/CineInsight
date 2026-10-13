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
    <div :class="['command-palette', { 'command-palette--dropping': dropping }]" @keydown="handleKeydown">
      <!-- 输入行：粘贴或拖入图片后左边显示缩略图、右边「清除」（以图模式，D-P2-05）。 -->
      <div class="command-palette__query">
        <img
          v-if="image"
          :src="image.dataURL"
          alt="查询图片"
          class="command-palette__query-thumb"
          data-test="command-palette-image-thumb"
        />
        <input
          ref="queryInput"
          v-model="query"
          type="text"
          class="search-input command-palette__input"
          data-test="command-palette-input"
          :placeholder="image ? '按这张图找画面；也可以继续输入文字搜索…' : '搜索命令、视频、人物、作品集、台词、画面…'"
          aria-label="命令面板"
          autocomplete="off"
        />
        <AppSpinner v-if="imagePreparing" :size="16" label="正在读取图片" />
        <button
          v-if="image"
          type="button"
          class="btn btn--secondary btn--sm"
          data-test="command-palette-clear-image"
          @click="clearImage"
        >清除</button>
      </div>
      <p v-if="imageError" class="command-palette__image-error" role="alert" data-test="command-palette-image-error">{{ imageError }}</p>
      <p class="command-palette__legend">↑ ↓ 选择 · 回车执行 · Esc 关闭 · 粘贴或拖入图片可按图找画面</p>

      <div ref="list" class="command-palette__list" role="listbox" data-test="command-palette-list">
        <!-- 命令组在最前（导航 · 动作 · 任务），随输入即时过滤，行为同以前。 -->
        <template v-for="group in commandGroups" :key="group.key">
          <p class="command-palette__group">{{ group.label }}</p>
          <button
            v-for="entry in group.entries"
            :key="entry.key"
            type="button"
            :class="['btn', 'btn--ghost', 'command-palette__item', {
              'command-palette__item--active': entry.key === activeEntryKey,
              'command-palette__item--disabled': !entry.enabled
            }]"
            :data-test="`command-palette-item-${entry.command.id}`"
            :disabled="!entry.enabled"
            :aria-selected="entry.key === activeEntryKey"
            role="option"
            @mousemove="pointAt(entry)"
            @click="execute(entry)"
          >{{ entry.command.label }}</button>
        </template>

        <p v-if="showCommandEmpty" class="command-palette__empty" data-test="command-palette-empty">
          没有匹配的命令。
        </p>

        <!-- 统一搜索的各组（D-P2-05）：各自加载、各自出错、各自为空，一组失败不影响其他组。 -->
        <section
          v-for="group in searchGroups"
          :key="group.key"
          class="command-palette__section"
          :data-test="`command-palette-group-${group.key}`"
          :aria-busy="group.state.status === 'loading' ? 'true' : 'false'"
        >
          <div class="command-palette__section-head">
            <p class="command-palette__group">{{ group.label }}</p>
            <AppSpinner v-if="group.state.status === 'loading' && group.entries.length" :size="16" />
            <button
              v-if="group.viewAll"
              type="button"
              class="btn-link command-palette__view-all"
              :data-test="`command-palette-view-all-${group.key}`"
              @click="viewAll(group)"
            >查看全部</button>
          </div>

          <div v-if="group.state.status === 'loading' && !group.entries.length" class="command-palette__loading" :data-test="`command-palette-${group.key}-loading`">
            <SkeletonBlock :lines="2" />
          </div>
          <AppBanner
            v-else-if="group.state.status === 'error'"
            tone="danger"
            :title="`${group.label}搜索失败`"
            :message="group.state.error"
            class="command-palette__error"
            :data-test="`command-palette-${group.key}-error`"
          >
            <template #actions>
              <button type="button" class="btn btn--secondary btn--sm" :data-test="`command-palette-retry-${group.key}`" @click="retryGroup(group.key)">重试</button>
            </template>
          </AppBanner>
          <template v-else>
            <button
              v-for="entry in group.entries"
              :key="entry.key"
              type="button"
              :class="['btn', 'btn--ghost', 'command-palette__item', 'command-palette__result', {
                'command-palette__item--active': entry.key === activeEntryKey
              }]"
              :data-test="`command-palette-item-${entry.key}`"
              :aria-selected="entry.key === activeEntryKey"
              role="option"
              @mousemove="pointAt(entry)"
              @click="execute(entry)"
            >
              <span :class="['command-palette__media', `command-palette__media--${entry.item.media.shape}`]">
                <AppIcon :name="group.icon" :size="16" />
                <img
                  v-if="entry.item.media.url && !brokenMedia[entry.key]"
                  :src="entry.item.media.url"
                  alt=""
                  loading="lazy"
                  @error="markBrokenMedia(entry.key)"
                />
              </span>
              <span class="command-palette__result-text">
                <span class="command-palette__result-title">{{ entry.item.title }}</span>
                <span v-if="entry.item.subtitle" class="command-palette__result-meta">{{ entry.item.subtitle }}</span>
              </span>
            </button>
            <p v-if="group.notice" class="command-palette__notice" :data-test="`command-palette-${group.key}-notice`">{{ group.notice }}</p>
            <p v-else-if="group.state.status === 'done' && !group.entries.length" class="command-palette__none" :data-test="`command-palette-${group.key}-empty`">
              没有匹配的{{ group.noun }}。
            </p>
          </template>
        </section>
      </div>
    </div>
  </DialogShell>
</template>

<script>
import AppBanner from './ui/AppBanner.vue';
import AppIcon from './ui/AppIcon.vue';
import AppSpinner from './ui/AppSpinner.vue';
import DialogShell from './ui/DialogShell.vue';
import SkeletonBlock from './ui/SkeletonBlock.vue';
import { COMMAND_PALETTE_SOURCE } from '../utils/appCommands.js';
import { COMMAND_GROUPS, filterCommands, isCommandEnabled } from '../utils/commandRegistry.js';
import { notifyError, notifySuccess } from '../utils/feedback.js';
import {
  TEXT_GROUP_KEYS,
  UNIFIED_SEARCH_DEBOUNCE_MS,
  UNIFIED_SEARCH_GROUPS,
  createGroupSequencer,
  firstImageFile,
  groupErrorText,
  groupNoticeText,
  prepareSearchImage,
  searchGroup,
  searchableKeyword,
  viewAllTarget
} from '../utils/unifiedSearch.js';

// 各组标题旁与结果缩略图没取到时用的图标。
const GROUP_ICONS = { video: 'videos', person: 'person', collection: 'collection', dialogue: 'diary', visual: 'image', image: 'image' };
const SCENE_GROUPS = new Set(['dialogue', 'visual', 'image']);
const IMAGE_PREPARE_KEY = 'image-prepare';

function emptyGroupStates() {
  return Object.fromEntries(UNIFIED_SEARCH_GROUPS.map(group => [group.key, null]));
}

// 命令面板（D-029）+ ⌘K 统一搜索（D-P2-05，详细设计 §3.4）。面板做四件事：过滤注册表里的命令；
// 输入 ≥ 2 字时按组并行搜视频、人物、作品集、台词、画面（编排在 utils/unifiedSearch.js）；粘贴或拖入图片时按图找画面；
// 执行选中项（命令、打开结果、「查看全部」）。所有业务动作都由各页面注册进 commandRegistry，或由 App 经 props 交进来，
// 面板不认识任何页面。
// 外壳是 DialogShell（标题、×、Esc 与焦点归还同所有对话框）；面板里有输入框，点遮罩照旧关闭，所以显式传 close-on-overlay。
// 面板只在没有别的对话框时打开（appCommands.js），执行前先关，不会和别的对话框叠在一起。
export default {
  name: 'CommandPalette',
  components: { AppBanner, AppIcon, AppSpinner, DialogShell, SkeletonBlock },
  props: {
    open: { type: Boolean, default: false },
    // 打开一部片（视频组结果）：App 的 openVideoFromCommand，按当前所在区域分流（观看 → 整页详情，管理 → 片库管理抽屉）。
    openVideo: { type: Function, required: true },
    // 执行前的准备（可选）：App 用它把宿主页的固定命令先切到宿主页再执行（详细设计 §6.1）。
    // 返回（或 resolve 为）false 表示放弃执行。面板自己不认识任何页面。
    beforeRun: { type: Function, default: null },
    // 切页并带参数（App 的 navigateTo(page, params)）：各组「查看全部」用。
    navigate: { type: Function, default: null },
    // 打开人物 / 作品集（App 的 openRecapEntity({ type, id, name })，与面板里「人物 · 某某」命令同一个入口）。
    openEntity: { type: Function, default: null },
    // 从某一刻打开一部片（App 的 openVideoAt({ videoID, startMs }, { from })）：台词、画面、以图的命中用，同样按区域分流。
    openVideoAt: { type: Function, default: null },
    // 搜索防抖毫秒数；测试里可以改小。
    searchDebounceMs: { type: Number, default: UNIFIED_SEARCH_DEBOUNCE_MS }
  },
  emits: ['close'],
  data() {
    return {
      query: '',
      // 用户用方向键或鼠标挑中的条目 key。按 key 记而不按序号：命令来源随时在变（任务组跟着事件刷新），
      // 搜索结果各组先后到达、插在中间，高亮不会因此跳到别的条目上。没挑过时高亮始终是第一条可执行项。
      activeKey: null,
      activeManual: false,
      groupStates: emptyGroupStates(),
      // 以图：{ dataURL, base64, name }（已缩到长边 1024 的 JPEG）。
      image: null,
      imageError: '',
      imagePreparing: false,
      dropping: false,
      brokenMedia: {}
    };
  },
  computed: {
    keyword() {
      return searchableKeyword(this.query);
    },
    // 只有图、没有文字时是「以图」模式：命令列表让位，图的结果排在最前。
    imageMode() {
      return Boolean(this.image) && !this.query.trim();
    },
    // 命令分组顺序固定，组内顺序来自 filterCommands 的权重排序（同以前）。
    commandGroups() {
      if (this.imageMode) return [];
      const byGroup = new Map(COMMAND_GROUPS.map(group => [group.key, []]));
      for (const command of filterCommands(this.query)) byGroup.get(command.group)?.push(command);
      const groups = [];
      for (const group of COMMAND_GROUPS) {
        const commands = byGroup.get(group.key) || [];
        if (!commands.length) continue;
        groups.push({
          key: group.key,
          label: group.label,
          entries: commands.map(command => ({ key: command.id, command, enabled: isCommandEnabled(command) }))
        });
      }
      return groups;
    },
    searchGroups() {
      const groups = [];
      for (const definition of UNIFIED_SEARCH_GROUPS) {
        const state = this.groupStates[definition.key];
        if (!state) continue;
        const entries = state.items.map(item => ({
          key: item.id,
          item,
          enabled: true,
          command: { id: item.id, label: item.label || item.title, run: () => this.openResult(item) }
        }));
        const notice = groupNoticeText(state.notices);
        groups.push({
          ...definition,
          icon: GROUP_ICONS[definition.key],
          state,
          entries,
          notice,
          // 有结果才给「查看全部」；场景三组在索引没建之类的说明下也给，去场景检索页建索引就是下一步。
          viewAll: state.status !== 'error' && (entries.length > 0 || (Boolean(notice) && SCENE_GROUPS.has(definition.key)))
        });
      }
      return groups;
    },
    flatEntries() {
      return [
        ...this.commandGroups.flatMap(group => group.entries),
        ...this.searchGroups.flatMap(group => group.entries)
      ];
    },
    // 高亮项：用户挑过且还在列表里就是它，否则是第一条可执行项（灰显项跳过）。
    activeEntry() {
      const entries = this.flatEntries.filter(entry => entry.enabled);
      if (this.activeManual) {
        const picked = entries.find(entry => entry.key === this.activeKey);
        if (picked) return picked;
      }
      return entries[0] || null;
    },
    activeEntryKey() {
      return this.activeEntry?.key ?? null;
    },
    showCommandEmpty() {
      return !this.commandGroups.length && !this.searchGroups.length;
    }
  },
  watch: {
    open: {
      immediate: true,
      handler(open) {
        if (!open) {
          this.stopListening();
          this.resetSearch();
          return;
        }
        this.resetSearch();
        this.query = '';
        this.activeKey = null;
        this.activeManual = false;
        this.startListening();
        this.$nextTick(() => this.$refs.queryInput?.focus());
      }
    },
    query() {
      this.activeKey = null;
      this.activeManual = false;
      this.scheduleSearch();
    }
  },
  beforeCreate() {
    this._sequencer = createGroupSequencer();
  },
  beforeUnmount() {
    this.stopListening();
    this.resetSearch();
  },
  methods: {
    handleKeydown(event) {
      // 输入法组字时的方向键与回车归输入法（选词、上屏），不当成面板操作。
      if (event.isComposing) return;
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        this.moveActive(1);
      } else if (event.key === 'ArrowUp') {
        event.preventDefault();
        this.moveActive(-1);
      } else if (event.key === 'Enter') {
        // 焦点在「查看全部」「重试」「清除」这些按钮上时回车按它们自己的；在输入框或列表项上时执行高亮项。
        const target = event.target;
        if (target !== this.$refs.queryInput && !target?.closest?.('[role="option"]')) return;
        event.preventDefault();
        this.execute(this.activeEntry);
      }
    },
    // 灰显项跳过：它们可见但不可执行，停在上面回车会什么都不发生。上下键跨组移动（命令 → 各搜索组）。
    moveActive(delta) {
      const entries = this.flatEntries.filter(entry => entry.enabled);
      if (!entries.length) return;
      const current = entries.findIndex(entry => entry.key === this.activeEntryKey);
      const next = current < 0
        ? (delta > 0 ? 0 : entries.length - 1)
        : Math.min(entries.length - 1, Math.max(0, current + delta));
      this.activeKey = entries[next].key;
      this.activeManual = true;
      this.$nextTick(() => this.$refs.list?.querySelector('.command-palette__item--active')?.scrollIntoView?.({ block: 'nearest' }));
    },
    pointAt(entry) {
      if (!entry.enabled) return;
      this.activeKey = entry.key;
      this.activeManual = true;
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
    // 打开一条搜索结果：视频走 openVideo，人物 / 作品集走实体入口，场景命中从命中前 2 秒起播。
    // 三者都由 App 按当前区域分流（观看 → 整页详情；管理 → 原有做法）。宿主没接上对应入口时什么也不做。
    openResult(item) {
      const target = item?.target || {};
      if (target.type === 'video') return this.openVideo(target.video);
      if (target.type === 'entity') {
        return this.openEntity ? this.openEntity({ type: target.entityType, id: target.id, name: target.name }) : false;
      }
      if (target.type === 'scene') {
        return this.openVideoAt
          ? this.openVideoAt({ videoID: target.videoID, startMs: target.startMs }, { from: COMMAND_PALETTE_SOURCE })
          : false;
      }
      return false;
    },
    // 组标题旁「查看全部」：关掉面板，带着关键词（或这张图）切到对应页面（utils/unifiedSearch.js 的 viewAllTarget）。
    async viewAll(group) {
      const target = viewAllTarget(group.key, { keyword: this.keyword, image: this.image });
      if (!target || !this.navigate) return;
      this.$emit('close');
      try {
        await this.navigate(target.page, target.params);
      } catch (err) {
        notifyError(`打开「${group.label}」的全部结果失败：${err}`);
      }
    },
    // 输入变化：不够 2 个字时收起各组并作废路上的请求；够了就先把各组放进「加载中」（已有结果的保留、标题旁转圈），
    // 停 250 毫秒再并行发请求。路上旧输入的响应一律作废。
    scheduleSearch() {
      const keyword = this.keyword;
      // 只多敲或删掉首尾空格，关键词没变，不重搜。
      if (keyword && keyword === this._searchKeyword) return;
      this._searchKeyword = keyword;
      clearTimeout(this._searchTimer);
      this._sequencer.invalidate(TEXT_GROUP_KEYS);
      if (!keyword) {
        const states = { ...this.groupStates };
        for (const key of TEXT_GROUP_KEYS) states[key] = null;
        this.groupStates = states;
        return;
      }
      const states = { ...this.groupStates };
      for (const key of TEXT_GROUP_KEYS) states[key] = loadingState(states[key]);
      this.groupStates = states;
      this._searchTimer = setTimeout(() => {
        for (const key of TEXT_GROUP_KEYS) this.runGroup(key, { keyword });
      }, this.searchDebounceMs);
    },
    async runGroup(key, input) {
      const sequence = this._sequencer.next(key);
      this.groupStates = { ...this.groupStates, [key]: loadingState(this.groupStates[key]) };
      try {
        const result = await searchGroup(key, input);
        if (!this._sequencer.isCurrent(key, sequence)) return;
        this.groupStates = { ...this.groupStates, [key]: { status: 'done', items: result.items, notices: result.notices, error: '' } };
      } catch (err) {
        if (!this._sequencer.isCurrent(key, sequence)) return;
        this.groupStates = { ...this.groupStates, [key]: { status: 'error', items: [], notices: [], error: groupErrorText(key, err) } };
      }
    },
    retryGroup(key) {
      if (key === 'image') {
        if (this.image) this.runGroup('image', { image: this.image });
        return;
      }
      if (this.keyword) this.runGroup(key, { keyword: this.keyword });
    },
    resetSearch() {
      clearTimeout(this._searchTimer);
      this._searchKeyword = '';
      this._sequencer.invalidate([...TEXT_GROUP_KEYS, 'image', IMAGE_PREPARE_KEY]);
      this.groupStates = emptyGroupStates();
      this.image = null;
      this.imageError = '';
      this.imagePreparing = false;
      this.dropping = false;
      this.brokenMedia = {};
    },
    markBrokenMedia(key) {
      this.brokenMedia = { ...this.brokenMedia, [key]: true };
    },
    // 以图：读图（校验、缩到长边 1024、编码）后切到以图模式并搜。读图期间又来一张，只认最后一张；
    // 读不出来或超过 20 MB 时就地说明原因，之前的图与结果不动。
    async useImageFile(file) {
      const sequence = this._sequencer.next(IMAGE_PREPARE_KEY);
      this.imageError = '';
      this.imagePreparing = true;
      let image;
      try {
        image = await prepareSearchImage(file);
      } catch (err) {
        if (!this._sequencer.isCurrent(IMAGE_PREPARE_KEY, sequence)) return;
        this.imagePreparing = false;
        this.imageError = String(err?.message || err || '这张图片读不出来，换一张再试。');
        return;
      }
      if (!this._sequencer.isCurrent(IMAGE_PREPARE_KEY, sequence)) return;
      this.imagePreparing = false;
      this.image = image;
      this.activeKey = null;
      this.activeManual = false;
      this.runGroup('image', { image });
      this.$nextTick(() => this.$refs.queryInput?.focus());
    },
    clearImage() {
      this._sequencer.invalidate(['image', IMAGE_PREPARE_KEY]);
      this.image = null;
      this.imageError = '';
      this.imagePreparing = false;
      this.groupStates = { ...this.groupStates, image: null };
      this.$nextTick(() => this.$refs.queryInput?.focus());
    },
    // 面板开着时接管图片的粘贴与拖放（窗口捕获阶段）：面板是模态的，图都归它。拦下之后底下的页面
    // （例如场景检索页自己的粘贴、拖放以图搜片）收不到。文字粘贴、拖动非文件内容照常。
    startListening() {
      if (this._listening) return;
      this._listening = true;
      window.addEventListener('paste', this.handlePaste, true);
      window.addEventListener('dragover', this.handleDragOver, true);
      window.addEventListener('drop', this.handleDrop, true);
    },
    stopListening() {
      if (!this._listening) return;
      this._listening = false;
      window.removeEventListener('paste', this.handlePaste, true);
      window.removeEventListener('dragover', this.handleDragOver, true);
      window.removeEventListener('drop', this.handleDrop, true);
      clearTimeout(this._dropTimer);
    },
    handlePaste(event) {
      const file = firstImageFile(event.clipboardData);
      if (!file) return;
      event.preventDefault();
      event.stopPropagation();
      this.useImageFile(file);
    },
    // 拖着文件经过时持续收到 dragover；停下 200 毫秒没再收到就当拖走了（WebKit 的 dragleave 不带 relatedTarget，
    // 靠它判断离开窗口会一闪一闪）。
    handleDragOver(event) {
      if (!hasFiles(event)) return;
      event.preventDefault();
      event.stopPropagation();
      this.dropping = true;
      clearTimeout(this._dropTimer);
      this._dropTimer = setTimeout(() => { this.dropping = false; }, 200);
    },
    handleDrop(event) {
      if (!hasFiles(event)) return;
      event.preventDefault();
      event.stopPropagation();
      clearTimeout(this._dropTimer);
      this.dropping = false;
      const file = firstImageFile(event.dataTransfer);
      if (file) this.useImageFile(file);
      else this.imageError = '拖进来的不是图片：只能用 JPEG、PNG、WebP、GIF、BMP 图片搜索。';
    }
  }
};

// 新一轮搜索开始：已有结果的组保留结果（标题旁转圈，避免每敲一个字整块闪一下），没有结果的组显示骨架。
function loadingState(previous) {
  const items = previous?.status === 'error' ? [] : (previous?.items || []);
  return { status: 'loading', items, notices: previous?.status === 'error' ? [] : (previous?.notices || []), error: '' };
}

function hasFiles(event) {
  return [...(event?.dataTransfer?.types || [])].includes('Files');
}
</script>

<style scoped>
/* 尺寸由 DialogShell 的 md（560）给出；正文左右收窄一点，列表项与输入框对齐。 */
:deep(.command-palette-modal .ui-dialog__body) { padding: 0 var(--space-4) var(--space-3); }
.command-palette--dropping {
  outline: 2px dashed var(--accent-color);
  outline-offset: var(--space-1);
  border-radius: var(--radius);
}
.command-palette__query {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.command-palette .command-palette__input { flex: 1; min-width: 0; }
.command-palette__query-thumb {
  flex: none;
  width: 56px;
  height: 34px;
  border-radius: var(--radius-sm);
  object-fit: cover;
  background: var(--thumb-bg);
}
.command-palette__image-error {
  margin: var(--space-2) 2px 0;
  color: var(--danger-text);
  font-size: var(--fs-caption);
}
.command-palette__legend {
  margin: var(--space-2) 2px var(--space-1);
  color: var(--text-muted);
  font-size: var(--fs-caption);
}
/* 命令之外还有六组结果，列表比只有命令时高一些（原 52vh）。 */
.command-palette__list {
  max-height: 60vh;
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
.command-palette__empty {
  padding: 10px 2px 6px;
  color: var(--text-muted);
  font-size: var(--fs-secondary);
}

/* 统一搜索的分组 */
.command-palette__section { margin-top: var(--space-1); }
.command-palette__section-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  color: var(--text-muted);
}
.command-palette__section-head .command-palette__group { flex: 1; min-width: 0; }
.command-palette__view-all { margin: 10px 2px 4px; font-size: var(--fs-caption); }
.command-palette__loading { padding: var(--space-1) 10px var(--space-2); }
.command-palette__error { margin: var(--space-1) 0 var(--space-2); }
.command-palette__notice,
.command-palette__none {
  margin: 0;
  padding: 4px 10px 8px;
  color: var(--text-muted);
  font-size: var(--fs-caption);
  line-height: 1.5;
}
.command-palette .command-palette__result {
  display: flex;
  align-items: center;
  justify-content: flex-start;
  gap: var(--space-3);
  padding: 6px 10px;
}
.command-palette__media {
  position: relative;
  flex: none;
  display: inline-grid;
  place-items: center;
  overflow: hidden;
  border-radius: var(--radius-sm);
  background: var(--thumb-bg);
  color: var(--text-muted);
}
.command-palette__media img {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.command-palette__media--poster { width: 28px; height: 42px; }
.command-palette__media--avatar { width: 34px; height: 34px; border-radius: var(--radius-pill); }
.command-palette__media--frame { width: 64px; height: 36px; }
.command-palette__result-text {
  display: grid;
  gap: 2px;
  min-width: 0;
}
.command-palette__result-title,
.command-palette__result-meta {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.command-palette__result-meta {
  color: var(--text-muted);
  font-size: var(--fs-caption);
}
.command-palette .command-palette__item--active .command-palette__result-meta { color: var(--accent-text); }
</style>
