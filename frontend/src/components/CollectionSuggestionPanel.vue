<template>
  <component :is="embedded ? 'InlineShell' : 'DialogShell'" v-bind="shellProps">
    <div class="suggestion-toolbar">
      <span class="suggestion-toolbar__meta">
        <template v-if="suggestions.length">共 {{ suggestions.length }} 组候选</template>
        <template v-else>暂无候选</template>
      </span>
      <button
        v-if="status.running"
        type="button"
        class="btn btn--secondary btn--sm"
        data-test="collection-suggestion-cancel"
        @click="cancelAnalysis"
      >取消分析</button>
      <button
        v-else
        type="button"
        class="btn btn--secondary btn--sm"
        data-test="collection-suggestion-analyze"
        :disabled="loading"
        @click="startAnalysis"
      >分析剧集</button>
    </div>

    <div v-if="status.running" class="suggestion-progress" data-test="collection-suggestion-progress">
      <ProgressBar
        :value="status.total > 0 ? status.scanned / status.total : null"
        :label="`正在按文件名分析剧集… 已扫 ${status.scanned} / ${status.total}${status.matched ? ` · 命中 ${status.matched} 个文件` : ''}`"
      />
    </div>
    <p v-else-if="status.last_error" class="suggestion-progress suggestion-progress--error" role="alert" data-test="collection-suggestion-error">
      上次分析失败：{{ status.last_error }}
    </p>

    <div class="suggestion-body">
      <SkeletonBlock v-if="loading" class="suggestion-placeholder" :lines="4" />
      <EmptyState
        v-else-if="!suggestions.length"
        class="suggestion-empty"
        title="还没有可确认的剧集候选"
        message="候选按文件名认剧集：「S01E02」、「第 3 集」、「EP04」、「[05]」、「片名 - 06」这几种写法各算一种。同一扫描根下同名系列凑够两集才成一组，已经在作品集里的视频不会再被提；已建作品集的系列出现新集时，会提示追加到那个作品集。"
        data-test="collection-suggestion-empty"
      />
      <template v-else>
        <nav class="suggestion-list" aria-label="剧集候选">
          <button
            v-for="suggestion in suggestions"
            :key="suggestion.id"
            type="button"
            :class="['btn', 'btn--ghost', 'suggestion-list__item', { active: suggestion.id === activeID }]"
            :aria-current="suggestion.id === activeID ? 'true' : undefined"
            data-test="collection-suggestion-item"
            @click="activeID = suggestion.id"
          >
            <span class="suggestion-list__name">{{ suggestion.series_name }}<AppBadge v-if="isAppend(suggestion)" tone="accent" class="suggestion-list__badge" :data-test="`collection-suggestion-append-badge-${suggestion.id}`">追加</AppBadge></span>
            <span class="suggestion-list__meta">{{ isAppend(suggestion) ? `新增 ${suggestion.member_count} 集 → 「${suggestion.target_collection_name}」` : `${suggestion.member_count} 集` }} · {{ suggestion.scan_root }}</span>
          </button>
        </nav>

        <div v-if="activeSuggestion" class="suggestion-detail">
          <!-- D-PC38：已确认作品集的系列又出现新集，候选是「追加到现有作品集」，不再新建（META-15）。 -->
          <template v-if="isAppend(activeSuggestion)">
            <p class="suggestion-append-target" data-test="collection-suggestion-append-target">
              追加到现有作品集「{{ activeSuggestion.target_collection_name || `作品集 #${activeSuggestion.target_collection_id}` }}」
            </p>
            <p class="help-text">
              这些是该系列新出现、还不在任何作品集里的集，确认后按集号插入到合适的位置。视频标题与文件名不会改动。
            </p>
          </template>
          <template v-else>
            <label class="suggestion-name">
              <span>作品集名称</span>
              <input
                v-model="activeDraft.name"
                type="text"
                maxlength="200"
                class="text-input"
                data-test="collection-suggestion-name"
                placeholder="作品集名称"
              />
            </label>
            <p class="help-text">
              已有同名作品集时会加入那一个，不会新建。确认只写作品集关系，视频标题与文件名不会改动。
            </p>
          </template>

          <div class="suggestion-members">
            <div
              v-for="member in activeSuggestion.members"
              :key="member.video_id"
              :class="['suggestion-member', { excluded: isExcluded(member.video_id) }]"
              data-test="collection-suggestion-member"
            >
              <img class="suggestion-member__thumb" :src="member.thumbnail_url" alt="" loading="lazy" />
              <div class="suggestion-member__info">
                <span class="suggestion-member__name" :title="member.path">{{ member.name }}</span>
                <span class="suggestion-member__meta">
                  {{ episodeLabel(member) }}<template v-if="member.size > 0"> · {{ formatBytes(member.size) }}</template>
                  <AppBadge v-if="member.multiple_versions" tone="warning" class="suggestion-member__badge">同集多版本</AppBadge>
                </span>
              </div>
              <button
                type="button"
                class="btn btn--secondary btn--sm"
                data-test="collection-suggestion-remove-member"
                @click="toggleMember(member.video_id)"
              >{{ isExcluded(member.video_id) ? '放回' : '去掉' }}</button>
            </div>
          </div>
        </div>
      </template>
    </div>

    <template v-if="activeSuggestion" #footer>
      <span class="suggestion-footer__meta" data-test="collection-suggestion-selection">
        将写入 {{ keptVideoIDs.length }} / {{ activeSuggestion.members.length }} 集
      </span>
      <button
        type="button"
        class="btn btn--secondary"
        data-test="collection-suggestion-dismiss"
        :disabled="processing"
        @click="dismiss"
      >忽略这一组</button>
      <button
        type="button"
        class="btn btn--primary"
        data-test="collection-suggestion-confirm"
        :disabled="!canConfirm"
        @click="confirm"
      >{{ isAppend(activeSuggestion) ? '追加到作品集' : '确认建作品集' }}</button>
    </template>
  </component>
</template>

<script>
import { h } from 'vue';
import {
  CancelCollectionSuggestionAnalysis,
  ConfirmCollectionSuggestion,
  DismissCollectionSuggestion,
  GetCollectionSuggestionStatus,
  ListCollectionSuggestions,
  StartCollectionSuggestionAnalysis
} from '../../wailsjs/go/main/App';
import AppBadge from './ui/AppBadge.vue';
import DialogShell from './ui/DialogShell.vue';
import EmptyState from './ui/EmptyState.vue';
import ProgressBar from './ui/ProgressBar.vue';
import SkeletonBlock from './ui/SkeletonBlock.vue';
import { confirmAction, notifyError, notifySuccess } from '../utils/feedback.js';
import { formatBytes } from '../utils/mediaDetails.js';
import { runtimeEventsMixin } from './video-list/runtimeEvents.js';

// 嵌入模式的外壳：与 DialogShell 收同样的正文与 footer 两个插槽，但只是页面里的一块——不弹层、没有标题栏
// （标题由宿主的区块给），footer 接在两栏下面当这一块的操作行。
const InlineShell = {
  name: 'CollectionSuggestionInline',
  setup(_, { slots }) {
    return () => h('div', { class: 'suggestion-inline' }, [
      slots.default?.(),
      slots.footer ? h('div', { class: 'suggestion-inline__footer' }, slots.footer()) : null
    ]);
  }
};

// 建议作品集面板（D-023..D-025）。
//
// 只做三件事：显示候选、让用户改名与去掉成员、把确认/忽略发回后端。分析本身是
// 后台任务，进度经 collection-suggestion-state 事件回来；确认后由后端复用作品集
// 写入，标题与文件名一律不动。
//
// 两种用法：默认是对话框，自己读候选（片库管理「管理」菜单）；embedded 嵌在待处理页的「建议作品集」区块里
// （D-P3-01），候选由宿主读好经 items 传进来——这一块的数量、加载、出错与清空都跟宿主的同一份来源走，面板不另读。
// 候选在后端变了（确认、忽略、分析跑完）都发 changed：嵌入时宿主据此重读这一块与摘要。
export default {
  name: 'CollectionSuggestionPanel',
  components: { AppBadge, DialogShell, EmptyState, InlineShell, ProgressBar, SkeletonBlock },
  mixins: [runtimeEventsMixin],
  props: {
    embedded: { type: Boolean, default: false },
    items: { type: Array, default: null }
  },
  emits: ['close', 'confirmed', 'changed'],
  data() {
    return {
      suggestions: [],
      // drafts 按候选 id 存改名与被去掉的成员，切换候选时不丢用户的编辑。
      drafts: {},
      activeID: 0,
      loading: false,
      processing: false,
      status: { running: false, scanned: 0, total: 0, matched: 0, pending: 0, last_error: '' }
    };
  },
  computed: {
    shellProps() {
      if (this.embedded) return {};
      return { title: '建议作品集', size: 'xl', class: 'suggestion-modal', onClose: () => this.$emit('close') };
    },
    activeSuggestion() {
      return this.suggestions.find(item => item.id === this.activeID) || null;
    },
    activeDraft() {
      return this.drafts[this.activeID] || { name: '', excluded: [] };
    },
    keptVideoIDs() {
      if (!this.activeSuggestion) return [];
      const excluded = this.activeDraft.excluded || [];
      return this.activeSuggestion.members
        .map(member => member.video_id)
        .filter(videoID => !excluded.includes(videoID));
    },
    // 新建至少两集且要有名称；追加到既有作品集一集就够，名称沿用目标作品集（后端同一口径）。
    canConfirm() {
      if (this.processing || !this.activeSuggestion) return false;
      if (this.isAppend(this.activeSuggestion)) return this.keptVideoIDs.length >= 1;
      return this.keptVideoIDs.length >= 2 && Boolean(this.activeDraft.name.trim());
    }
  },
  watch: {
    // 嵌入模式：宿主每次重读都换上新的候选；改过的名称与去掉的成员按候选 id 留着（syncDrafts）。
    items: {
      immediate: true,
      handler(items) {
        if (!this.embedded) return;
        this.suggestions = Array.isArray(items) ? [...items] : [];
        this.syncDrafts();
      }
    }
  },
  async mounted() {
    this.registerRuntimeEvent('collection-suggestion-state', (data) => {
      this.applyStatus(data);
      // 跑完就把候选拉一遍：分析是后台任务，用户不该再点一次刷新。嵌入时候选归宿主读，由 changed 让宿主重读。
      if (data && !data.running) {
        this.$emit('changed');
        if (!this.embedded) this.refresh();
      }
    });
    await this.refreshStatus();
    if (!this.embedded) await this.refresh();
  },
  methods: {
    formatBytes,
    // kind=append 等价于 target_collection_id 非空（详细设计 §1.2a）。
    isAppend(suggestion) {
      return suggestion?.kind === 'append' || Boolean(suggestion?.target_collection_id);
    },
    applyStatus(data) {
      this.status = {
        running: Boolean(data?.running),
        scanned: Number(data?.scanned || 0),
        total: Number(data?.total || 0),
        matched: Number(data?.matched || 0),
        pending: Number(data?.pending || 0),
        last_error: data?.last_error || ''
      };
    },
    async refreshStatus() {
      try {
        this.applyStatus(await GetCollectionSuggestionStatus());
      } catch (err) {
        notifyError('读取剧集分析状态失败：' + err);
      }
    },
    async refresh() {
      this.loading = true;
      try {
        const views = await ListCollectionSuggestions();
        this.suggestions = Array.isArray(views) ? views : [];
        this.syncDrafts();
      } catch (err) {
        notifyError('读取剧集候选失败：' + err);
      } finally {
        this.loading = false;
      }
    },
    syncDrafts() {
      const drafts = {};
      for (const suggestion of this.suggestions) {
        const previous = this.drafts[suggestion.id];
        drafts[suggestion.id] = {
          name: previous ? previous.name : suggestion.series_name,
          excluded: previous ? previous.excluded.filter(id => suggestion.members.some(member => member.video_id === id)) : []
        };
      }
      this.drafts = drafts;
      if (!this.suggestions.some(item => item.id === this.activeID)) {
        this.activeID = this.suggestions.length ? this.suggestions[0].id : 0;
      }
    },
    isExcluded(videoID) {
      return (this.activeDraft.excluded || []).includes(videoID);
    },
    toggleMember(videoID) {
      const draft = this.drafts[this.activeID];
      if (!draft) return;
      draft.excluded = draft.excluded.includes(videoID)
        ? draft.excluded.filter(id => id !== videoID)
        : [...draft.excluded, videoID];
    },
    episodeLabel(member) {
      const season = member.season == null ? '' : `S${String(member.season).padStart(2, '0')}`;
      const episode = member.episode == null ? '' : `E${String(member.episode).padStart(2, '0')}`;
      return `${season}${episode}` || '未识别集号';
    },
    async startAnalysis() {
      try {
        this.applyStatus(await StartCollectionSuggestionAnalysis());
      } catch (err) {
        notifyError('启动剧集分析失败：' + err);
      }
    },
    async cancelAnalysis() {
      try {
        await CancelCollectionSuggestionAnalysis();
      } catch (err) {
        notifyError('取消剧集分析失败：' + err);
      }
      await this.refreshStatus();
    },
    async confirm() {
      const suggestion = this.activeSuggestion;
      if (!suggestion || !this.canConfirm) return;
      const append = this.isAppend(suggestion);
      const name = append ? (suggestion.target_collection_name || '') : this.activeDraft.name.trim();
      const videoIDs = this.keptVideoIDs;
      this.processing = true;
      try {
        const detail = await ConfirmCollectionSuggestion(suggestion.id, name, videoIDs);
        notifySuccess(append ? `已把 ${videoIDs.length} 集追加到作品集「${name}」` : `已把 ${videoIDs.length} 集写入作品集「${name}」`);
        this.$emit('confirmed', detail);
        this.dropSuggestion(suggestion.id);
        this.$emit('changed');
      } catch (err) {
        const raw = String(err?.message || err || '');
        notifyError(raw.includes('target_collection_gone') ? '目标作品集已被删除，请重新分析剧集。' : '确认作品集失败：' + raw);
      } finally {
        this.processing = false;
      }
    },
    // D-PC38：忽略按（扫描根，规范化系列名）记忆，之后这个系列新增集数也不会再提醒。
    async dismiss() {
      const suggestion = this.activeSuggestion;
      if (!suggestion) return;
      const confirmed = await confirmAction({
        title: '忽略这一组',
        message: `「${suggestion.series_name}」不再作为建议出现：同一扫描目录下的这个系列之后新增集数也不会再提醒。`,
        confirmText: '忽略'
      });
      if (!confirmed) return;
      this.processing = true;
      try {
        await DismissCollectionSuggestion(suggestion.id);
        this.dropSuggestion(suggestion.id);
        this.$emit('changed');
      } catch (err) {
        notifyError('忽略剧集候选失败：' + err);
      } finally {
        this.processing = false;
      }
    },
    dropSuggestion(suggestionID) {
      this.suggestions = this.suggestions.filter(item => item.id !== suggestionID);
      this.syncDrafts();
    }
  }
};
</script>

<style scoped>
.suggestion-toolbar { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: var(--space-3); }
.suggestion-toolbar__meta { color: var(--text-secondary); font-size: var(--fs-caption); }

.suggestion-progress {
  max-height: 96px;
  margin: 0 0 var(--space-3);
  overflow-y: auto;
  overflow-wrap: anywhere;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--hairline-faint);
  border-radius: var(--radius-sm);
  background: var(--panel-subtle-bg);
  color: var(--text-secondary);
  font-size: var(--fs-caption);
}
.suggestion-progress--error { background: var(--warning-soft); border-color: var(--warning-border); color: var(--warning-text); }

/* 左列表右详情各自滚动：两栏占定高，对话框正文不跟着一起滚。 */
.suggestion-body {
  display: flex;
  height: min(520px, calc(100vh - 300px));
  min-height: 240px;
  overflow: hidden;
  border: 1px solid var(--hairline-soft);
  border-radius: var(--radius);
}

.suggestion-placeholder { flex: 1; padding: 28px 18px; }
.suggestion-empty { flex: 1; border: 0; border-radius: 0; }

.suggestion-list {
  width: 240px;
  flex: none;
  overflow-y: auto;
  padding: var(--space-2);
  border-right: 1px solid var(--hairline-soft);
  background: var(--panel-subtle-bg);
}
.suggestion-list__item {
  display: grid;
  gap: 2px;
  width: 100%;
  height: auto;
  padding: var(--space-2) 10px;
  border: 1px solid transparent;
  border-radius: var(--radius-sm);
  font-weight: var(--fw-regular);
  text-align: left;
  white-space: normal;
  justify-content: stretch;
}
.suggestion-list__item.btn.active { border-color: var(--accent-border); background: var(--accent-soft); }
.suggestion-list__name { display: flex; align-items: center; gap: 6px; color: var(--text-primary); font-size: var(--fs-secondary); }
.suggestion-append-target { margin: 0 0 6px; color: var(--text-primary); font-size: var(--fs-secondary); font-weight: var(--fw-semibold); overflow-wrap: anywhere; }
.suggestion-list__meta { overflow: hidden; color: var(--text-muted); font-size: var(--fs-caption); text-overflow: ellipsis; white-space: nowrap; }

.suggestion-detail { min-width: 0; flex: 1; overflow-y: auto; padding: 14px 18px; }
.suggestion-name { display: grid; gap: 6px; }
.suggestion-name span { color: var(--text-secondary); font-size: var(--fs-caption); }

.suggestion-members { display: grid; gap: var(--space-2); margin-top: 14px; }
.suggestion-member {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 7px 9px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
}
.suggestion-member.excluded { opacity: 0.5; }
.suggestion-member__thumb { width: 64px; height: 36px; flex: none; border-radius: var(--radius-sm); background: var(--neutral-softer); object-fit: cover; }
.suggestion-member__info { min-width: 0; flex: 1; display: grid; gap: 2px; }
.suggestion-member__name { overflow: hidden; color: var(--text-primary); font-size: var(--fs-caption); text-overflow: ellipsis; white-space: nowrap; }
.suggestion-member__meta { display: flex; align-items: center; gap: 6px; color: var(--text-muted); font-family: var(--font-mono); font-size: var(--fs-caption); }
.suggestion-member__badge { font-family: inherit; }

.suggestion-footer__meta { color: var(--text-secondary); font-size: var(--fs-caption); }

/* 嵌入模式（待处理页的一块）：两栏按内容高、封顶后各自滚，不跟窗口高度走（页面本身会滚）；
   操作行与对话框 footer 同一排法——第一项（将写入几集）靠左，按钮靠右。
   操作行由 InlineShell 生成，不带本组件的 scope 属性，要用 :deep 才命中；外壳根节点带。 */
.suggestion-inline .suggestion-body { height: auto; max-height: 480px; }
.suggestion-inline :deep(.suggestion-inline__footer) {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: var(--space-2);
  margin-top: var(--space-3);
}
.suggestion-inline :deep(.suggestion-inline__footer > :first-child:not(:last-child)) { margin-right: auto; }
</style>
