<template>
  <BaseModal class="suggestion-modal" @close="$emit('close')">
    <div class="suggestion-header">
      <h3>建议作品集</h3>
      <span class="suggestion-header__meta">
        <template v-if="suggestions.length">共 {{ suggestions.length }} 组候选</template>
        <template v-else>暂无候选</template>
      </span>
      <div class="suggestion-header__spacer"></div>
      <button
        v-if="status.running"
        type="button"
        class="btn-secondary btn-compact"
        data-test="collection-suggestion-cancel"
        @click="cancelAnalysis"
      >取消分析</button>
      <button
        v-else
        type="button"
        class="btn-secondary btn-compact"
        data-test="collection-suggestion-analyze"
        :disabled="loading"
        @click="startAnalysis"
      >分析剧集</button>
      <button type="button" class="suggestion-header__close" aria-label="关闭" @click="$emit('close')">✕</button>
    </div>

    <div v-if="status.running" class="suggestion-progress" data-test="collection-suggestion-progress">
      正在按文件名分析剧集…… 已扫 {{ status.scanned }} / {{ status.total }}
      <span v-if="status.matched"> · 命中 {{ status.matched }} 个文件</span>
    </div>
    <div v-else-if="status.last_error" class="suggestion-progress suggestion-progress--error" data-test="collection-suggestion-error">
      上次分析失败：{{ status.last_error }}
    </div>

    <div class="suggestion-body">
      <div v-if="loading" class="suggestion-placeholder">正在读取候选……</div>
      <div v-else-if="!suggestions.length" class="suggestion-placeholder" data-test="collection-suggestion-empty">
        <p>还没有可确认的剧集候选。</p>
        <p class="help-text">
          候选按文件名认剧集：「S01E02」、「第 3 集」、「EP04」、「[05]」、「片名 - 06」这几种写法各算一种。
          同一扫描根下同名系列凑够两集才成一组，已经在作品集里的视频不会再被提。
        </p>
      </div>
      <template v-else>
        <nav class="suggestion-list" aria-label="剧集候选">
          <button
            v-for="suggestion in suggestions"
            :key="suggestion.id"
            type="button"
            :class="['suggestion-list__item', { active: suggestion.id === activeID }]"
            data-test="collection-suggestion-item"
            @click="activeID = suggestion.id"
          >
            <span class="suggestion-list__name">{{ suggestion.series_name }}</span>
            <span class="suggestion-list__meta">{{ suggestion.member_count }} 集 · {{ suggestion.scan_root }}</span>
          </button>
        </nav>

        <div v-if="activeSuggestion" class="suggestion-detail">
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
                  <span v-if="member.multiple_versions" class="suggestion-member__badge">同集多版本</span>
                </span>
              </div>
              <button
                type="button"
                class="btn-secondary btn-compact"
                data-test="collection-suggestion-remove-member"
                @click="toggleMember(member.video_id)"
              >{{ isExcluded(member.video_id) ? '放回' : '去掉' }}</button>
            </div>
          </div>
        </div>
      </template>
    </div>

    <div v-if="activeSuggestion" class="suggestion-footer">
      <span class="suggestion-footer__meta" data-test="collection-suggestion-selection">
        将写入 {{ keptVideoIDs.length }} / {{ activeSuggestion.members.length }} 集
      </span>
      <div class="suggestion-header__spacer"></div>
      <button
        type="button"
        class="btn-secondary"
        data-test="collection-suggestion-dismiss"
        :disabled="processing"
        @click="dismiss"
      >忽略这一组</button>
      <button
        type="button"
        class="btn-primary"
        data-test="collection-suggestion-confirm"
        :disabled="processing || keptVideoIDs.length < 2 || !activeDraft.name.trim()"
        @click="confirm"
      >确认建作品集</button>
    </div>
  </BaseModal>
</template>

<script>
import {
  CancelCollectionSuggestionAnalysis,
  ConfirmCollectionSuggestion,
  DismissCollectionSuggestion,
  GetCollectionSuggestionStatus,
  ListCollectionSuggestions,
  StartCollectionSuggestionAnalysis
} from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';
import { confirmAction, notifyError, notifySuccess } from '../utils/feedback.js';
import { formatBytes } from '../utils/mediaDetails.js';
import { runtimeEventsMixin } from './video-list/runtimeEvents.js';

// 建议作品集面板（D-023..D-025）。
//
// 只做三件事：显示候选、让用户改名与去掉成员、把确认/忽略发回后端。分析本身是
// 后台任务，进度经 collection-suggestion-state 事件回来；确认后由后端复用作品集
// 写入，标题与文件名一律不动。
export default {
  name: 'CollectionSuggestionPanel',
  components: { BaseModal },
  mixins: [runtimeEventsMixin],
  emits: ['close', 'confirmed'],
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
    }
  },
  async mounted() {
    this.registerRuntimeEvent('collection-suggestion-state', (data) => {
      this.applyStatus(data);
      // 跑完就把候选拉一遍：分析是后台任务，用户不该再点一次刷新。
      if (data && !data.running) this.refresh();
    });
    await this.refreshStatus();
    await this.refresh();
  },
  methods: {
    formatBytes,
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
      if (!suggestion) return;
      const name = this.activeDraft.name.trim();
      const videoIDs = this.keptVideoIDs;
      if (videoIDs.length < 2 || !name) return;
      this.processing = true;
      try {
        const detail = await ConfirmCollectionSuggestion(suggestion.id, name, videoIDs);
        notifySuccess(`已把 ${videoIDs.length} 集写入作品集「${name}」`);
        this.$emit('confirmed', detail);
        this.dropSuggestion(suggestion.id);
      } catch (err) {
        notifyError('确认作品集失败：' + err);
      } finally {
        this.processing = false;
      }
    },
    async dismiss() {
      const suggestion = this.activeSuggestion;
      if (!suggestion) return;
      const confirmed = await confirmAction({
        title: '忽略这一组',
        message: `「${suggestion.series_name}」的 ${suggestion.members.length} 集不再作为建议出现。成员有增减时会重新提醒。`,
        confirmText: '忽略'
      });
      if (!confirmed) return;
      this.processing = true;
      try {
        await DismissCollectionSuggestion(suggestion.id);
        this.dropSuggestion(suggestion.id);
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
:deep(.suggestion-modal) {
  width: min(880px, calc(100vw - 32px));
  max-width: calc(100vw - 32px);
  max-height: calc(100vh - 48px);
  overflow: hidden;
  padding: 0;
  display: flex;
  flex-direction: column;
}

.suggestion-header {
  display: flex;
  align-items: center;
  gap: 10px;
  flex: none;
  padding: 12px 18px;
  border-bottom: 1px solid var(--hairline-soft);
}
.suggestion-header h3 { margin: 0; font-size: 15px; }
.suggestion-header__meta { color: var(--text-secondary); font-size: 12px; }
.suggestion-header__spacer { flex: 1; }
.suggestion-header__close { border: 0; background: transparent; color: var(--text-muted); font-size: 16px; cursor: pointer; }

.suggestion-progress {
  flex: none;
  max-height: 96px;
  overflow-y: auto;
  overflow-wrap: anywhere;
  padding: 8px 18px;
  border-bottom: 1px solid var(--hairline-faint);
  background: var(--panel-subtle-bg);
  color: var(--text-secondary);
  font-size: 12px;
}
.suggestion-progress--error { background: var(--warning-soft); border-bottom-color: var(--warning-border); color: var(--warning-text); }

.suggestion-body {
  display: flex;
  min-height: 0;
  flex: 1;
  overflow: hidden;
}

.suggestion-placeholder { padding: 28px 18px; color: var(--text-secondary); font-size: 13px; }
.suggestion-placeholder p { margin: 0 0 8px; }

.suggestion-list {
  width: 240px;
  flex: none;
  overflow-y: auto;
  padding: 8px;
  border-right: 1px solid var(--hairline-soft);
  background: var(--panel-subtle-bg);
}
.suggestion-list__item {
  display: grid;
  gap: 2px;
  width: 100%;
  padding: 8px 10px;
  border: 1px solid transparent;
  border-radius: var(--radius);
  background: transparent;
  text-align: left;
  cursor: pointer;
}
.suggestion-list__item.active { border-color: var(--accent-border); background: var(--accent-soft); }
.suggestion-list__name { color: var(--text-primary); font-size: 13px; }
.suggestion-list__meta { overflow: hidden; color: var(--text-muted); font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }

.suggestion-detail { min-width: 0; flex: 1; overflow-y: auto; padding: 14px 18px; }
.suggestion-name { display: grid; gap: 6px; }
.suggestion-name span { color: var(--text-secondary); font-size: 12px; }

.suggestion-members { display: grid; gap: 8px; margin-top: 14px; }
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
.suggestion-member__thumb { width: 64px; height: 36px; flex: none; border-radius: 4px; background: var(--neutral-softer); object-fit: cover; }
.suggestion-member__info { min-width: 0; flex: 1; display: grid; gap: 2px; }
.suggestion-member__name { overflow: hidden; color: var(--text-primary); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.suggestion-member__meta { color: var(--text-muted); font-family: var(--font-mono); font-size: 11px; }
.suggestion-member__badge { margin-left: 6px; padding: 1px 6px; border-radius: 999px; background: var(--warning-soft); color: var(--warning-text); font-family: inherit; }

.suggestion-footer {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: none;
  padding: 10px 18px;
  border-top: 1px solid var(--hairline-soft);
  background: var(--panel-subtle-bg);
}
.suggestion-footer__meta { color: var(--text-secondary); font-size: 12px; }
</style>
