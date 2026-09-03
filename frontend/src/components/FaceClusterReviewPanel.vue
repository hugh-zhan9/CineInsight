<template>
  <section class="face-review" data-test="face-cluster-review">
    <div class="face-review__toolbar">
      <p class="help-text">
        人脸只产出候选：只有你在这里命名、关联或确认追加，才会真正建立人物关系。
      </p>
      <button
        type="button"
        class="btn-secondary btn-compact"
        data-test="face-cluster-refresh"
        :disabled="!!busy"
        @click="load()"
      >刷新</button>
    </div>

    <p v-if="error" class="face-review__error" role="alert" data-test="face-cluster-review-error">{{ error }}</p>
    <p v-if="notice" class="face-review__notice" role="status" data-test="face-cluster-review-notice">{{ notice }}</p>

    <p v-if="initialLoading" class="face-review__status" role="status">正在加载人物候选…</p>
    <p
      v-else-if="!cards.length"
      class="face-review__status"
      role="status"
      data-test="face-cluster-review-empty"
    >暂无人物候选。人脸分析跑完后，认得出的面孔会在这里等你命名。</p>

    <article
      v-for="card in cards"
      :key="card.id"
      class="face-review__card glass-surface"
      :data-test="`face-cluster-card-${card.id}`"
    >
      <div class="face-review__card-main">
        <div class="face-review__crop">
          <img
            v-if="card.representative_observation_id && !cropFailed[card.id]"
            :src="cropURL(card)"
            :alt="`${cardTitle(card)} 的人脸截图`"
            loading="lazy"
            @error="markCropFailed(card.id)"
          />
          <span v-else aria-hidden="true">?</span>
        </div>
        <div class="face-review__summary">
          <strong>{{ cardTitle(card) }}</strong>
          <p class="face-review__meta">{{ mediaSummary(card) }}</p>
          <p v-if="card.candidates.length" class="face-review__candidates">
            <span
              v-for="candidate in card.candidates"
              :key="candidate.person_id"
              class="face-review__candidate"
              :data-test="`face-cluster-candidate-${card.id}-${candidate.person_id}`"
            >可能是 {{ candidate.display_name }}（相似度 {{ similarityText(candidate.similarity) }}）</span>
          </p>
          <p
            v-if="card.status === 'named' && card.append_pending_count"
            class="face-review__meta"
            :data-test="`face-cluster-append-summary-${card.id}`"
          >
            新出现 {{ card.append_pending_count }} 处：{{ appendMediaText(card) }}
          </p>
        </div>
      </div>

      <div v-if="card.status === 'unnamed'" class="face-review__actions">
        <button
          type="button"
          class="btn-primary btn-compact"
          :disabled="!!busy"
          :data-test="`face-cluster-name-open-${card.id}`"
          @click="openNameForm(card)"
        >命名为新人物</button>
        <button
          type="button"
          class="btn-secondary btn-compact"
          :disabled="!!busy"
          :data-test="`face-cluster-link-open-${card.id}`"
          @click="openLinkForm(card)"
        >关联到现有人物</button>
        <button
          type="button"
          class="btn-secondary btn-compact"
          :disabled="!!busy"
          :data-test="`face-cluster-ignore-${card.id}`"
          @click="ignore(card)"
        >忽略</button>
      </div>
      <div v-else class="face-review__actions">
        <button
          type="button"
          class="btn-primary btn-compact"
          :disabled="!!busy"
          :data-test="`face-cluster-append-confirm-${card.id}`"
          @click="confirmAppend(card)"
        >确认追加</button>
        <button
          type="button"
          class="btn-secondary btn-compact"
          :disabled="!!busy"
          :data-test="`face-cluster-append-dismiss-${card.id}`"
          @click="dismissAppend(card)"
        >忽略追加</button>
      </div>

      <div v-if="nameForm.clusterID === card.id" class="face-review__form" data-test="face-cluster-name-form">
        <input
          v-model="nameForm.displayName"
          type="text"
          class="text-input"
          placeholder="显示名，例如 周迅"
          data-test="face-cluster-name-display"
          @keyup.enter="submitName(card)"
        />
        <input
          v-model="nameForm.originalName"
          type="text"
          class="text-input"
          placeholder="原始名（可留空），例如 Zhou Xun"
          data-test="face-cluster-name-original"
          @keyup.enter="submitName(card)"
        />
        <button
          type="button"
          class="btn-primary btn-compact"
          :disabled="!!busy"
          data-test="face-cluster-name-submit"
          @click="submitName(card)"
        >确认命名</button>
        <button
          type="button"
          class="btn-secondary btn-compact"
          data-test="face-cluster-name-cancel"
          @click="closeForms"
        >取消</button>
      </div>

      <div v-if="linkForm.clusterID === card.id" class="face-review__form" data-test="face-cluster-link-form">
        <div class="face-review__combobox">
          <input
            v-model="linkForm.keyword"
            type="search"
            class="search-input"
            placeholder="搜索人物，回车关联"
            role="combobox"
            autocomplete="off"
            aria-controls="face-cluster-link-options"
            :aria-expanded="linkForm.menuOpen"
            data-test="face-cluster-link-search"
            @focus="linkForm.menuOpen = true"
            @input="handleLinkInput"
            @keydown="handleLinkKeydown"
          />
          <div
            v-if="linkForm.menuOpen"
            id="face-cluster-link-options"
            class="face-review__options"
            role="listbox"
            data-test="face-cluster-link-options"
          >
            <button
              v-for="(option, index) in linkOptions"
              :key="option.personID"
              type="button"
              role="option"
              :aria-selected="index === linkForm.activeIndex"
              :class="['face-review__option', { active: index === linkForm.activeIndex }]"
              :data-test="`face-cluster-link-option-${option.personID}`"
              @mouseenter="linkForm.activeIndex = index"
              @mousedown.prevent="submitLink(card, option)"
            >
              {{ option.name }}
              <span v-if="option.similarity" class="face-review__option-hint">候选 · {{ similarityText(option.similarity) }}</span>
            </button>
            <span v-if="!linkOptions.length" class="face-review__options-empty">没有匹配人物</span>
          </div>
        </div>
        <button
          type="button"
          class="btn-secondary btn-compact"
          data-test="face-cluster-link-cancel"
          @click="closeForms"
        >取消</button>
      </div>
    </article>
  </section>
</template>

<script>
import {
  ConfirmFaceClusterAppend, DismissFaceClusterAppend, IgnoreFaceCluster,
  LinkFaceCluster, ListFaceClusters, ListPeople, NameFaceCluster
} from '../../wailsjs/go/main/App';

// 后端错误码（设计 7.3.1）到人话的映射。cluster_not_unnamed / cluster_not_found 说明
// 这一簇已经被别处处理过了，除了报错还要顺手刷新，否则用户对着过期的卡片再点一次
// 还是同一个错。
const ERROR_TEXT = {
  cluster_not_unnamed: '这一簇刚刚已经被命名或忽略过了，列表已刷新。',
  cluster_not_found: '这一簇已经不在了，列表已刷新。',
  cluster_not_named: '这一簇还没有命名，无法确认追加。',
  person_name_invalid: '显示名不能为空，且不超过 200 个字。',
  person_not_found: '选中的人物已经不存在了。'
};
const STALE_CODES = ['cluster_not_unnamed', 'cluster_not_found'];

// 人物候选（D-019）：未命名簇给三个动作，已命名簇的追加候选给两个动作。
// 视频侧待审工作台与图片侧审阅面板共用这一个组件——两边看到的是同一批簇，
// 一个簇可能同时含视频与图片观测，因此这里不按媒体类型筛。
export default {
  name: 'FaceClusterReviewPanel',
  emits: ['changed'],
  data() {
    return {
      unnamedClusters: [],
      appendClusters: [],
      people: [],
      cropFailed: {},
      // initialLoading 只在第一次加载时为真：事件驱动的刷新不该把用户正在看的
      // 卡片换成"加载中"。
      initialLoading: true,
      busy: '',
      error: '',
      notice: '',
      analysisRunning: false,
      nameForm: { clusterID: 0, displayName: '', originalName: '' },
      linkForm: { clusterID: 0, keyword: '', menuOpen: false, activeIndex: 0 },
      reviewOff: null,
      analysisOff: null
    };
  },
  computed: {
    // 未命名簇在前（要动作的），已命名簇的追加候选在后。
    cards() {
      return [...this.unnamedClusters, ...this.appendClusters];
    },
    // 候选人物置顶：算出来的"可能是谁"排在搜索结果之前，用户不用自己打字。
    linkOptions() {
      const card = this.cards.find(item => item.id === this.linkForm.clusterID);
      if (!card) return [];
      const options = [];
      const seen = new Set();
      for (const candidate of card.candidates || []) {
        options.push({ personID: candidate.person_id, name: candidate.display_name || `人物 #${candidate.person_id}`, similarity: candidate.similarity });
        seen.add(candidate.person_id);
      }
      for (const item of this.people) {
        const id = Number(item?.person?.id);
        if (!id || seen.has(id)) continue;
        options.push({ personID: id, name: item.person.display_name || `人物 #${id}`, similarity: 0 });
      }
      const keyword = this.linkForm.keyword.trim().toLowerCase();
      if (!keyword) return options;
      return options.filter(option => option.name.toLowerCase().includes(keyword));
    }
  },
  mounted() {
    this.load({ initial: true });
    if (window.runtime?.EventsOn) {
      const reviewOff = window.runtime.EventsOn('face-review-changed', () => this.load());
      if (typeof reviewOff === 'function') this.reviewOff = reviewOff;
      const analysisOff = window.runtime.EventsOn('face-analysis-state', (status) => {
        if (!status) return;
        if (status.running) {
          this.analysisRunning = true;
          return;
        }
        // 进度事件每件媒体来一次，只有"跑完"才值得重新拉簇。
        const finished = this.analysisRunning || status.completed;
        this.analysisRunning = false;
        if (finished) this.load();
      });
      if (typeof analysisOff === 'function') this.analysisOff = analysisOff;
    }
  },
  beforeUnmount() {
    this.reviewOff?.();
    this.analysisOff?.();
  },
  methods: {
    cropURL(card) {
      return `/preview/face-crop/${card.representative_observation_id}`;
    },
    markCropFailed(clusterID) {
      this.cropFailed = { ...this.cropFailed, [clusterID]: true };
    },
    cardTitle(card) {
      if (card.status === 'named') return card.person_name || `人物 #${card.person_id}`;
      return '未命名的面孔';
    },
    similarityText(similarity) {
      return `${Math.round(Number(similarity || 0) * 100)}%`;
    },
    mediaSummary(card) {
      const parts = [`${card.observation_count} 次出现`];
      if (card.video_count) parts.push(`${card.video_count} 个视频`);
      if (card.image_count) parts.push(`${card.image_count} 张图片`);
      return parts.join(' · ');
    },
    appendMediaText(card) {
      const names = (card.append_pending_media || []).map(item => item.name || `${item.media_kind === 'video' ? '视频' : '图片'} #${item.media_id}`);
      return names.join('、');
    },
    async load({ initial = false } = {}) {
      if (initial) this.initialLoading = true;
      try {
        const [unnamed, named] = await Promise.all([
          ListFaceClusters({ status: 'unnamed', media_kind: '' }),
          ListFaceClusters({ status: 'named', media_kind: '' })
        ]);
        this.unnamedClusters = this.normalize(unnamed);
        // 已命名的簇只有在有待确认的追加候选时才值得占面板位置。
        this.appendClusters = this.normalize(named).filter(card => card.append_pending_count > 0);
        if (!this.cards.some(card => card.id === this.nameForm.clusterID)) this.nameForm.clusterID = 0;
        if (!this.cards.some(card => card.id === this.linkForm.clusterID)) this.linkForm.clusterID = 0;
      } catch (err) {
        // 刷新失败时保留已有卡片：把面板清空只会让用户以为候选没了。
        this.error = `加载人物候选失败：${err}`;
      } finally {
        this.initialLoading = false;
      }
    },
    normalize(list) {
      return (list || []).map(card => ({
        ...card,
        candidates: card.candidates || [],
        append_pending_media: card.append_pending_media || [],
        append_pending_count: Number(card.append_pending_count || 0)
      }));
    },
    closeForms() {
      this.nameForm = { clusterID: 0, displayName: '', originalName: '' };
      this.linkForm = { clusterID: 0, keyword: '', menuOpen: false, activeIndex: 0 };
    },
    openNameForm(card) {
      this.closeForms();
      this.error = '';
      this.notice = '';
      this.nameForm.clusterID = card.id;
    },
    openLinkForm(card) {
      this.closeForms();
      this.error = '';
      this.notice = '';
      this.linkForm.clusterID = card.id;
      this.linkForm.menuOpen = true;
      this.searchPeople();
    },
    handleLinkInput() {
      this.linkForm.menuOpen = true;
      this.linkForm.activeIndex = 0;
      this.searchPeople();
    },
    // 人物候选来自库里，输入即查；用 token 丢弃过期响应，避免慢的那一次覆盖新的。
    async searchPeople() {
      const token = Symbol('face-person-search');
      this._peopleToken = token;
      try {
        const results = await ListPeople(this.linkForm.keyword.trim(), '', 0, 20);
        if (this._peopleToken !== token) return;
        this.people = results || [];
      } catch (err) {
        if (this._peopleToken === token) this.error = `搜索人物失败：${err}`;
      }
    },
    handleLinkKeydown(event) {
      if (event.key === 'Escape') {
        event.preventDefault();
        this.linkForm.menuOpen = false;
        return;
      }
      const options = this.linkOptions;
      if (!options.length) return;
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        this.linkForm.menuOpen = true;
        this.linkForm.activeIndex = (this.linkForm.activeIndex + 1) % options.length;
      } else if (event.key === 'ArrowUp') {
        event.preventDefault();
        this.linkForm.menuOpen = true;
        this.linkForm.activeIndex = (this.linkForm.activeIndex - 1 + options.length) % options.length;
      } else if (event.key === 'Enter') {
        event.preventDefault();
        const card = this.cards.find(item => item.id === this.linkForm.clusterID);
        if (card) this.submitLink(card, options[this.linkForm.activeIndex] || options[0]);
      }
    },
    async runAction(key, action, successNotice) {
      if (this.busy) return;
      this.busy = key;
      this.error = '';
      this.notice = '';
      try {
        await action();
        this.closeForms();
        this.notice = successNotice;
        this.$emit('changed');
        await this.load();
      } catch (err) {
        const raw = String(err?.message || err || '');
        const code = Object.keys(ERROR_TEXT).find(item => raw.includes(item));
        this.error = code ? ERROR_TEXT[code] : `操作失败：${raw}`;
        if (code && STALE_CODES.includes(code)) await this.load();
      } finally {
        this.busy = '';
      }
    },
    submitName(card) {
      const displayName = this.nameForm.displayName;
      const originalName = this.nameForm.originalName;
      return this.runAction(
        `name-${card.id}`,
        () => NameFaceCluster(card.id, displayName, originalName),
        `已建立人物「${displayName.trim()}」并关联了这一簇里的媒体。`
      );
    },
    submitLink(card, option) {
      if (!option) return Promise.resolve();
      return this.runAction(
        `link-${card.id}`,
        () => LinkFaceCluster(card.id, option.personID),
        `已把这一簇关联到「${option.name}」。`
      );
    },
    ignore(card) {
      return this.runAction(
        `ignore-${card.id}`,
        () => IgnoreFaceCluster(card.id),
        '已忽略这一簇，源文件没变就不会再提示。'
      );
    },
    confirmAppend(card) {
      return this.runAction(
        `append-confirm-${card.id}`,
        () => ConfirmFaceClusterAppend(card.id),
        `已把新出现的媒体关联到「${this.cardTitle(card)}」。`
      );
    },
    dismissAppend(card) {
      return this.runAction(
        `append-dismiss-${card.id}`,
        () => DismissFaceClusterAppend(card.id),
        '已忽略这批追加候选，同一处不会再提示。'
      );
    }
  }
};
</script>

<style scoped>
.face-review {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.face-review__toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.face-review__toolbar .help-text {
  margin: 0;
}

.face-review__error {
  margin: 0;
  color: var(--danger-text);
  font-size: 13px;
}

.face-review__notice {
  margin: 0;
  color: var(--text-secondary);
  font-size: 13px;
}

.face-review__status {
  margin: 24px 0;
  text-align: center;
  color: var(--text-secondary);
  font-size: 13px;
}

.face-review__card {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 12px;
  border-radius: var(--radius-md);
}

.face-review__card-main {
  display: flex;
  align-items: flex-start;
  gap: 12px;
}

.face-review__crop {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 72px;
  height: 72px;
  flex: 0 0 auto;
  overflow: hidden;
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--panel-muted-bg);
  color: var(--text-muted);
}

.face-review__crop img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.face-review__summary {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.face-review__meta {
  margin: 0;
  color: var(--text-secondary);
  font-size: 12px;
}

.face-review__candidates {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin: 0;
}

.face-review__candidate {
  padding: 2px 8px;
  border: 1px solid var(--accent-border);
  border-radius: 999px;
  background: var(--accent-soft);
  color: var(--accent-text);
  font-size: 12px;
}

.face-review__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.face-review__form {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  padding-top: 8px;
  border-top: 1px solid var(--hairline-faint);
}

.face-review__form .text-input {
  min-width: 160px;
}

.face-review__combobox {
  position: relative;
  min-width: 220px;
}

.face-review__combobox > .search-input {
  width: 100%;
}

.face-review__options {
  position: absolute;
  top: calc(100% + 4px);
  right: 0;
  left: 0;
  z-index: 20;
  max-height: 220px;
  overflow-y: auto;
  padding: 4px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--panel-bg);
  box-shadow: var(--shadow-popover);
}

.face-review__option {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
  padding: 7px 8px;
  border: 0;
  border-radius: 4px;
  background: transparent;
  color: var(--text-primary);
  text-align: left;
  cursor: pointer;
}

.face-review__option:hover,
.face-review__option.active {
  background: var(--control-hover-bg);
  color: var(--accent-color);
}

.face-review__option-hint {
  color: var(--text-muted);
  font-size: 11px;
}

.face-review__options-empty {
  display: block;
  padding: 8px;
  color: var(--text-muted);
  font-size: 12px;
}
</style>
