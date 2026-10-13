<template>
  <section :class="['watchlist-page', embedded ? 'watchlist-page--embedded' : 'page-content']">
    <!-- embedded：嵌在片单页的「想看」标签里（D-P2-04），页头由片单页提供。 -->
    <PageHeader v-if="!embedded" title="想看" subtitle="记下想看但还没收进片库的影片，选好类型后自动去对应的资料源补全信息。" />

    <form class="watchlist-form" data-test="watchlist-form" @submit.prevent="save">
      <label for="watchlist-title">{{ editID ? '修改片名' : '添加想看的影片' }}</label>
      <div class="watchlist-form-row">
        <select v-if="!editID" id="watchlist-kind" v-model="kind" class="text-input watchlist-kind-select"
          aria-label="影片类型" :disabled="busy" data-test="watchlist-kind" @change="kindTouched = true">
          <option v-for="option in kindOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
        </select>
        <input id="watchlist-title" ref="titleInput" v-model="title" class="text-input" type="text"
          placeholder="输入片名或番号，回车添加" :disabled="busy" data-test="watchlist-title" />
        <button class="btn btn--primary" type="submit" :disabled="!canSave" data-test="watchlist-save">
          {{ busy ? '处理中…' : editID ? '保存修改' : '添加' }}
        </button>
        <button v-if="editID" class="btn btn--secondary" type="button" :disabled="busy"
          data-test="watchlist-cancel" @click="resetDraft">取消</button>
      </div>
    </form>

    <div class="watchlist-search-row">
      <input v-model="keyword" class="text-input" type="search" placeholder="搜索片名" aria-label="搜索想看片名"
        :disabled="busy" data-test="watchlist-search" @input="search" />
      <button class="btn btn--secondary" type="button" :disabled="loading || busy" @click="load()">刷新</button>
    </div>
    <p v-if="error" class="watchlist-error" role="alert" data-test="watchlist-error">{{ error }}</p>
    <p v-if="loading && entries.length" class="watchlist-hint" role="status">正在读取…</p>
    <SkeletonBlock v-else-if="loading" class="watchlist-skeleton" :lines="4" data-test="watchlist-loading" />
    <EmptyState
      v-else-if="loaded && !entries.length && !nextID && !error"
      class="watchlist-empty"
      icon="inbox"
      :title="keyword.trim() ? '没有匹配的片名' : '还没有想看的影片，先记下一部吧。'"
      :message="keyword.trim() ? '换个关键词再搜，或者清空搜索看全部。' : '在上面输入片名或番号，回车就能添加。'"
      data-test="watchlist-empty"
    >
      <template v-if="keyword.trim()" #actions>
        <button class="btn btn--secondary btn--sm" type="button" data-test="watchlist-clear-search" @click="clearSearch">清空搜索</button>
      </template>
    </EmptyState>

    <!-- 片库里的对应情况（D-P2-03）读失败：片单本身照常显示，这里说明并给重试。 -->
    <AppBanner v-if="libraryError" class="watchlist-library-banner" tone="warning" title="没能读出片库里的对应情况"
      :message="libraryError" data-test="watchlist-library-error">
      <template #actions>
        <button class="btn btn--secondary btn--sm" type="button" :disabled="loading"
          data-test="watchlist-library-retry" @click="retryLibrary">重试</button>
      </template>
    </AppBanner>

    <ul class="watchlist-list">
      <li v-for="entry in entries" :key="entry.id" class="watchlist-entry" data-test="watchlist-entry">
        <div class="watchlist-entry-main">
          <img v-if="posterVisible(entry)" class="watchlist-poster" :src="posterURL(entry)" alt=""
            :data-test="`watchlist-poster-${entry.id}`" @error="hidePoster(entry)" />
          <div class="watchlist-entry-info">
            <strong>{{ entry.title }}</strong>
            <!-- 源站片名是只读补充：用户手输的番号永远是主标题，补全不覆盖它。
                 取不到时整段不渲染，不留分隔符也不占位。 -->
            <span v-if="sourceTitleOf(entry)" class="watchlist-source-title"
              :data-test="`watchlist-source-title-${entry.id}`">{{ sourceTitleOf(entry) }}</span>
            <div class="watchlist-entry-meta">
              <span class="watchlist-kind-tag" :data-test="`watchlist-kind-${entry.id}`">{{ kindLabel(entry.kind) }}</span>
              <!-- 条目来源（D-PC52）：榜单「想看」建立或认领的是「榜单」，其余是「手动」。 -->
              <span class="watchlist-origin-tag" :data-test="`watchlist-origin-${entry.id}`">{{ originLabel(entry) }}</span>
              <span class="watchlist-status" :data-test="`watchlist-status-${entry.id}`">{{ statusLabel(entry.enrichment_status) }}</span>
              <span v-if="entry.year">{{ entry.year }}</span>
              <span v-if="entry.rating">{{ Number(entry.rating).toFixed(1) }} 分</span>
              <time :datetime="entry.created_at">{{ formatDate(entry.created_at) }} 添加</time>
              <AppBadge v-if="notInLibrary(entry)" :data-test="`watchlist-not-in-library-${entry.id}`">不在库</AppBadge>
            </div>
            <p v-if="failureText(entry)" class="watchlist-failure" :data-test="`watchlist-failure-${entry.id}`">
              {{ failureText(entry) }}
            </p>
            <p v-if="genresOf(entry).length" class="watchlist-detail-line">{{ genresOf(entry).join(' · ') }}</p>
            <p v-if="creditsText(entry)" class="watchlist-detail-line">{{ creditsText(entry) }}</p>
            <p v-if="entry.overview" class="watchlist-overview">{{ entry.overview }}</p>
            <!-- 片库里的对应情况（D-P2-03，所有来源）：已在库（播放 / 解除，自动连上的标「自动匹配」）、
                 可能已有（确认后才关联）、不在库（只是 meta 行里的一个标记）。片单不会因入库自动删条目，
                 已在库与可能已有都给一键「移除（片单）」。 -->
            <div v-if="linkedOf(entry).length" class="watchlist-library watchlist-library--linked" :data-test="`watchlist-linked-${entry.id}`">
              <AppBadge tone="success">已在库</AppBadge>
              <span v-for="video in linkedOf(entry)" :key="video.video_id" class="watchlist-library__match"
                :data-test="`watchlist-linked-video-${entry.id}-${video.video_id}`">
                <span class="watchlist-library__title">{{ video.title }}</span>
                <AppBadge v-if="video.origin === 'auto'" :data-test="`watchlist-auto-${entry.id}-${video.video_id}`">自动匹配</AppBadge>
                <AppBadge v-if="video.is_watched">看过</AppBadge>
                <button class="btn btn--primary btn--sm" type="button" :data-test="`watchlist-play-${entry.id}-${video.video_id}`"
                  @click="$emit('open-video-id', video.video_id)">播放</button>
                <!-- 解除只去掉这一条关联、能再确认回来，所以不弹确认框，做完给带「撤销」的提示（D-UD-07 第 1 级）。 -->
                <button class="btn btn--ghost btn--sm" type="button" :disabled="busy || loading"
                  :data-test="`watchlist-unlink-${entry.id}-${video.video_id}`" @click="unlinkVideo(entry, video)">解除</button>
              </span>
              <button class="btn btn--danger btn--sm" type="button" :disabled="busy || loading"
                :data-test="`watchlist-remove-after-link-${entry.id}`" @click="remove(entry)">移除（片单）</button>
            </div>
            <div v-else-if="suggestionsOf(entry).length" class="watchlist-library" :data-test="`watchlist-suggestions-${entry.id}`">
              <span class="watchlist-library__label">片库中可能已有：</span>
              <span v-for="suggestion in suggestionsOf(entry)" :key="suggestion.video_id" class="watchlist-library__match"
                :data-test="`watchlist-suggestion-${entry.id}-${suggestion.video_id}`">
                {{ suggestion.display_title || suggestion.name }}
                <button class="btn btn--secondary btn--sm" type="button" :disabled="busy || loading" title="确认是同一部，关联到这个片库视频"
                  :data-test="`watchlist-link-${entry.id}-${suggestion.video_id}`" @click="confirmSuggestion(entry, suggestion)">确认</button>
              </span>
              <button class="btn btn--danger btn--sm" type="button" :disabled="busy || loading"
                :data-test="`watchlist-remove-suggested-${entry.id}`" @click="remove(entry)">移除（片单）</button>
            </div>
            <SkeletonBlock v-else-if="libraryLoadingOf(entry)" class="watchlist-library-skeleton" :lines="1"
              :data-test="`watchlist-library-loading-${entry.id}`" />
          </div>
        </div>
        <div class="watchlist-actions">
          <button v-if="entry.enrichment_status === 'failed'" class="btn btn--secondary btn--sm" type="button"
            :disabled="busy || loading" :data-test="`watchlist-retry-${entry.id}`" @click="retry(entry)">重试补全</button>
          <button class="btn btn--secondary btn--sm" type="button" :disabled="busy || loading"
            :data-test="`watchlist-candidates-${entry.id}`" @click="openCandidates(entry)">重选结果</button>
          <button class="btn btn--secondary btn--sm" type="button" :disabled="busy || loading"
            :data-test="`watchlist-edit-${entry.id}`" @click="startEdit(entry)">改名</button>
          <button class="btn btn--danger btn--sm" type="button" :disabled="busy || loading"
            :data-test="`watchlist-remove-${entry.id}`" @click="remove(entry)">移除</button>
        </div>

        <div v-if="candidateEntryID === entry.id" class="watchlist-candidates" data-test="watchlist-candidate-panel">
          <div class="watchlist-candidates-head">
            <strong>为「{{ entry.title }}」重选匹配结果</strong>
            <button class="btn btn--secondary btn--sm" type="button" data-test="watchlist-candidate-close"
              @click="closeCandidates">关闭</button>
          </div>
          <p v-if="candidateLoading" class="watchlist-hint" role="status">正在查询候选…</p>
          <p v-else-if="candidateError" class="watchlist-error" role="alert" data-test="watchlist-candidate-error">
            {{ candidateError }}
          </p>
          <p v-else-if="!candidates.length" class="watchlist-hint" data-test="watchlist-candidate-empty">资料源没有给出候选。</p>
          <ul v-else class="watchlist-candidate-list">
            <li v-for="(candidate, index) in candidates" :key="`${candidate.source_name}-${candidate.source_item_id}`"
              class="watchlist-candidate" data-test="watchlist-candidate">
              <div class="watchlist-candidate-info">
                <strong>{{ candidate.title || candidate.original_title || candidate.source_item_id }}</strong>
                <span class="watchlist-candidate-meta">
                  {{ sourceLabelOf(candidate.source_name) }}
                  <template v-if="candidate.year"> · {{ candidate.year }}</template>
                  <template v-if="candidate.rating"> · {{ Number(candidate.rating).toFixed(1) }} 分</template>
                  · {{ candidate.source_item_id }}
                </span>
                <span v-if="candidate.overview" class="watchlist-candidate-overview">{{ candidate.overview }}</span>
              </div>
              <button class="btn btn--primary btn--sm" type="button" :disabled="busy"
                :data-test="`watchlist-candidate-apply-${index}`" @click="applyCandidate(candidate)">用这个</button>
            </li>
          </ul>
        </div>
      </li>
    </ul>
    <button v-if="nextID" class="btn btn--secondary watchlist-more" type="button" :disabled="loading || busy"
      data-test="watchlist-more" @click="load(true)">加载更多</button>
  </section>
</template>

<script>
import {
  ApplyWatchlistCandidate, CreateWatchlistEntry, DeleteWatchlistEntry, LinkWatchlistEntryVideo, ListWatchlist,
  ListWatchlistCandidates, ListWatchlistLibraryState, RetryWatchlistEnrichment, SuggestLibraryMatchesBatch,
  UnlinkWatchlistEntryVideo, UpdateWatchlistEntry
} from '../../wailsjs/go/main/App';
import AppBadge from './ui/AppBadge.vue';
import AppBanner from './ui/AppBanner.vue';
import EmptyState from './ui/EmptyState.vue';
import PageHeader from './ui/PageHeader.vue';
import SkeletonBlock from './ui/SkeletonBlock.vue';
import { confirmAction, notifySuccess, notifyUndo } from '../utils/feedback.js';

// D-WM02 的五个类型，顺序即下拉框顺序，默认落在第一个（电影）。
const KIND_OPTIONS = [
  { value: 'movie', label: '电影' },
  { value: 'tv', label: '剧集' },
  { value: 'anime', label: '动画' },
  { value: 'show', label: '综艺' },
  { value: 'av', label: 'AV' }
];
const DEFAULT_KIND = KIND_OPTIONS[0].value;
const KIND_LABELS = Object.fromEntries(KIND_OPTIONS.map(option => [option.value, option.label]));

// 番号形态：可选的数字厂牌前缀 + 2~6 位字母 + 连字符 + 3~5 位数字（SSIS-001、259LUXU-1234）。
//
// 数字段要求 3 位以上是有意的：放到 2 位就会把「Catch-22」「COVID-19」这类正常片名
// 一并算成番号。这条判定**只改下拉框的值**，提交时以下拉框当前值为准，后端不会按
// 片名再判一次类型——用户改回去就是改回去了。
const AV_CODE_PATTERN = /^[0-9]{0,4}[A-Za-z]{2,6}-[0-9]{3,5}$/;

// FC2 番号是另一套形态：FC2-PPV-4976527 / FC2PPV-4976527 / FC2-4976527。
// 商品号 6~10 位，与片商番号的 3~5 位区分得开。
//
// **只认带 FC2 前缀的写法**：后端适配器也接受光秃秃的商品号（4976527），
// 但那在界面上无从判断——一串纯数字同样可能是用户随手写的片名。
// 自动识别是便利，不是判据；用户手选 AV 之后照样能补全。
const FC2_CODE_PATTERN = /^FC2-?(?:PPV-?)?[0-9]{6,10}$/i;

const STATUS_LABELS = {
  pending: '待补全',
  running: '补全中…',
  succeeded: '已补全',
  failed: '补全失败',
  manual: '手动维护'
};

// 资料源的展示名。条目只在补全成功后才有 source_name，失败时退回按类型推断该问谁。
// aggregate 是多源合并的保留源名——逐字段择优之后，一条记录本就不归属任何单一源。
// fanza 保留着：FANZA 已经退场，但存量条目的 source_name 仍是它，删掉键会让它们显示成裸字符串。
const SOURCE_LABELS = { douban: '豆瓣', tmdb: 'TMDB', bangumi: 'Bangumi', fanza: 'FANZA', javbus: 'JavBus', jav321: 'jav321', airav: 'airav', fc2: 'FC2', aggregate: '多源合并' };
const KIND_SOURCE_LABELS = { movie: '豆瓣 / TMDB', tv: 'TMDB', show: 'TMDB', anime: 'Bangumi', av: 'AV 资料源' };

// D-WM14 的六个失败分类码各有各的文案，**不合并**：把凭证没配和「查无此片」写成
// 同一句，用户会照着换片名的方向排查，而真正要做的是去填凭证。
const FAILURE_TEXTS = {
  credential_missing: source => `未配置 ${source} 的凭证`,
  credential_invalid: source => `${source} 凭证无效或已过期`,
  proxy_unreachable: () => '资料源出网代理不可用',
  network_unreachable: source => `无法连接 ${source}`,
  not_found: source => `${source} 没有收录`,
  source_error: source => `${source} 返回异常`
};

// WatchlistPage.origins 的取值（services.WatchlistOrigin*）。
const ORIGIN_LABELS = { chart: '来自榜单', manual: '手动添加' };

// 与后端 services.LibraryMatchKey 一致：「片名|年份」，年份未知为 0。
function libraryMatchKey(title, year) {
  return `${title}|${Number(year) || 0}`;
}

// ListWatchlistLibraryState 一次最多查 200 条（services.watchlistLibraryStateMaxEntries）。
const LIBRARY_STATE_BATCH = 200;
// 「片库中可能已有」每条最多列几部。
const SUGGESTION_LIMIT = 3;

function chunks(list, size) {
  const result = [];
  for (let index = 0; index < list.length; index += size) result.push(list.slice(index, index + size));
  return result;
}

// 调一个接口，成败都落成 { ok, value | error }，同步抛出的也算失败。
function settle(call) {
  return Promise.resolve().then(call).then(value => ({ ok: true, value }), error => ({ ok: false, error }));
}

export default {
  name: 'WatchlistPage',
  components: { AppBadge, AppBanner, EmptyState, PageHeader, SkeletonBlock },
  props: {
    // 嵌在片单页里时不显示自己的页头，也不再套一层页面留白（片单页提供）。
    embedded: { type: Boolean, default: false }
  },
  // open-video-id(videoID)：「已在库 · 播放」，由宿主进这部片的整页详情。
  emits: ['open-video-id'],
  data() {
    return {
      entries: [], nextID: 0, keyword: '', title: '', kind: DEFAULT_KIND, kindTouched: false,
      editID: 0, error: '', loading: false, loaded: false, busy: false, requestID: 0,
      candidateEntryID: 0, candidates: [], candidateLoading: false, candidateError: '', candidateRequestID: 0,
      brokenPosters: [], kindOptions: KIND_OPTIONS,
      origins: {},
      // 片库里的对应情况（D-P2-03），按条目 ID：linked = ListWatchlistLibraryState 的已关联视频，
      // dismissed = 同一接口给的解除过的视频 ID（含整部作品的全部版本），suggestions = SuggestLibraryMatchesBatch 的建议；
      // 没有这个键表示还没读到（或第一次就没读出来）。
      // libraryLoading 是正在读的条目 ID；libraryError 是最近一次读失败的原因（横幅 + 重试）。
      linked: {}, dismissed: {}, suggestions: {}, libraryLoading: {}, libraryError: ''
    };
  },
  computed: {
    canSave() {
      return Boolean(this.title.trim()) && !this.busy && !this.loading;
    }
  },
  watch: {
    title(value) {
      // 输入辅助：用户没动过下拉框时，类型跟着输入走；动过之后就完全交给用户，
      // 再怎么改片名也不再自动覆盖他选的那个值。
      if (this.editID || this.kindTouched) return;
      const code = value.trim();
      this.kind = (AV_CODE_PATTERN.test(code) || FC2_CODE_PATTERN.test(code)) ? 'av' : DEFAULT_KIND;
    }
  },
  mounted() {
    this.load();
    // 后端推送是补全结果的唯一刷新来源；这里不做轮询。
    if (window.runtime?.EventsOn) {
      const off = window.runtime.EventsOn('watchlist-enrich-progress', progress => this.applyProgress(progress));
      if (typeof off === 'function') this.progressOff = off;
    }
  },
  beforeUnmount() {
    this.requestID++;
    this._disposed = true;
    if (typeof this.progressOff === 'function') this.progressOff();
  },
  methods: {
    async load(more = false) {
      if (more && (this.loading || !this.nextID)) return;
      const requestID = ++this.requestID;
      this.loading = true;
      this.error = '';
      try {
        const page = await ListWatchlist(this.keyword.trim(), more ? this.nextID : 0, 50);
        if (requestID !== this.requestID) return;
        this.entries = more ? [...this.entries, ...page.entries] : page.entries;
        this.origins = more ? { ...this.origins, ...(page.origins || {}) } : { ...(page.origins || {}) };
        this.nextID = page.next_id;
        this.loaded = true;
        if (!more) this.resetLibrary();
        this.loadLibrary(page.entries || []);
      } catch (err) {
        if (requestID === this.requestID) this.error = `读取想看片单失败：${err}`;
      } finally {
        if (requestID === this.requestID) this.loading = false;
      }
    },
    // 空结果里的「清空搜索」：回到全部条目。
    clearSearch() {
      this.keyword = '';
      this.search();
    },
    search() {
      this.entries = [];
      this.nextID = 0;
      this.loaded = false;
      this.closeCandidates();
      this.load();
    },
    resetDraft() {
      this.title = '';
      this.kind = DEFAULT_KIND;
      this.kindTouched = false;
      this.editID = 0;
    },
    async startEdit(entry) {
      this.editID = entry.id;
      this.title = entry.title;
      await this.$nextTick();
      this.$refs.titleInput?.focus();
    },
    async save() {
      if (!this.canSave) return;
      const title = this.title.trim();
      if (Array.from(title).length > 200) {
        this.error = '片名不能超过 200 个字符';
        return;
      }
      this.busy = true;
      this.error = '';
      try {
        if (this.editID) {
          await UpdateWatchlistEntry(this.editID, title);
          notifySuccess('片名已修改');
        } else {
          // 类型取下拉框当前值，不再看片名长什么样。
          await CreateWatchlistEntry(title, this.kind);
          notifySuccess('已添加到想看');
          this.keyword = '';
        }
        this.resetDraft();
        await this.load();
      } catch (err) {
        this.error = String(err);
      } finally {
        this.busy = false;
      }
    },
    async remove(entry) {
      if (this.busy || this.loading) return;
      this.busy = true;
      try {
        // 删除片单条目会同步撤销榜单上的「想看」（D-PC52 双向同步），关联到豆瓣条目的要先说清楚。
        const chartLinked = this.origins[entry.id] === 'chart' || Boolean(this.doubanIDOf(entry));
        const message = chartLinked
          ? `从想看片单移除「${entry.title}」？年度榜单上这部片的「想看」标记也会一并撤销。`
          : `从想看片单移除「${entry.title}」？`;
        if (!await confirmAction({ title: '移除想看记录', message, confirmText: '移除', danger: true })) return;
        await DeleteWatchlistEntry(entry.id);
        this.entries = this.entries.filter(item => item.id !== entry.id);
        if (this.editID === entry.id) this.resetDraft();
        if (this.candidateEntryID === entry.id) this.closeCandidates();
        this.error = '';
        if (!this.entries.length && this.nextID) await this.load(true);
      } catch (err) {
        this.error = String(err);
      } finally {
        this.busy = false;
      }
    },
    async retry(entry) {
      if (this.busy || this.loading) return;
      this.busy = true;
      try {
        await RetryWatchlistEnrichment(entry.id);
        notifySuccess('已重新排入补全队列');
        this.error = '';
        await this.refreshEntry(entry.id);
      } catch (err) {
        this.error = String(err);
      } finally {
        this.busy = false;
      }
    },
    // 连点两条的重选时，先发的那次回来得晚就会把结果盖到后选的条目上，所以按
    // 请求序号丢弃过期响应，形态与 load() 一致。
    async openCandidates(entry) {
      if (this.busy || this.loading) return;
      const requestID = ++this.candidateRequestID;
      this.candidateEntryID = entry.id;
      this.candidates = [];
      this.candidateError = '';
      this.candidateLoading = true;
      try {
        const candidates = (await ListWatchlistCandidates(entry.id)) || [];
        if (requestID !== this.candidateRequestID) return;
        this.candidates = candidates;
      } catch (err) {
        if (requestID === this.candidateRequestID) this.candidateError = String(err);
      } finally {
        if (requestID === this.candidateRequestID) this.candidateLoading = false;
      }
    },
    closeCandidates() {
      this.candidateRequestID++;
      this.candidateEntryID = 0;
      this.candidates = [];
      this.candidateError = '';
      this.candidateLoading = false;
    },
    async applyCandidate(candidate) {
      if (this.busy) return;
      const entryID = this.candidateEntryID;
      this.busy = true;
      try {
        await ApplyWatchlistCandidate(entryID, candidate.source_item_id);
        notifySuccess('已应用所选结果');
        this.closeCandidates();
        this.error = '';
        await this.refreshEntry(entryID);
      } catch (err) {
        // 失败保留候选面板与错误，用户可以换一条再试。
        this.candidateError = String(err);
      } finally {
        this.busy = false;
      }
    },
    // applyProgress 处理 watchlist-enrich-progress：事件只带状态、分类码与源名，
    // 先就地把这一行改过来，用户立刻看到「补全中…」变成「已补全」。
    applyProgress(progress) {
      if (!progress || !progress.entry_id) return;
      const index = this.entries.findIndex(item => item.id === progress.entry_id);
      if (index < 0) return;
      const current = this.entries[index];
      this.entries.splice(index, 1, {
        ...current,
        enrichment_status: progress.status || current.enrichment_status,
        enrichment_error: progress.failure || '',
        source_name: progress.source_name || current.source_name
      });
      // 年份、简介、海报这些不在事件载荷里，成功之后要重新读回来，否则用户还得
      // 手动刷新才看得见补全结果（AC-03）。
      if (progress.status === 'succeeded') this.refreshEntry(progress.entry_id);
    },
    // refreshEntry 只换一行，不重拉整页——用户翻到第几页就停在第几页。
    // 游标语义是 id < cursorID，所以 id+1 配 limit 1 正好落在这一条上。
    async refreshEntry(id) {
      try {
        const page = await ListWatchlist(this.keyword.trim(), id + 1, 1);
        const fresh = page?.entries?.[0];
        if (!fresh || fresh.id !== id) return;
        const index = this.entries.findIndex(item => item.id === id);
        if (index < 0) return;
        this.brokenPosters = this.brokenPosters.filter(item => item !== id);
        if (page.origins && page.origins[id]) this.origins = { ...this.origins, [id]: page.origins[id] };
        this.entries.splice(index, 1, fresh);
      } catch (err) {
        this.error = `刷新想看条目失败：${err}`;
        return;
      }
      // 补全成功、选定候选之后后端已同步跑过这一条的自动关联，片名与年份也可能变了：这一行的片库情况重读。
      await this.loadLibrary(this.entries.filter(item => item.id === id));
    },
    // 片库里的对应情况（D-P2-03）：一批条目一次批量查（已关联 ListWatchlistLibraryState，建议 SuggestLibraryMatchesBatch），
    // 两者各自成败。每个条目记一个请求序号，后发的请求作废先发的：翻页、单行刷新、确认 / 解除之后的重读互不覆盖；
    // 重新读第一页（搜索、刷新）时 resetLibrary 清空旧状态。读失败只影响这一块：横幅说明并给重试，片单本身照常。
    resetLibrary() {
      this._libraryTokens = new Map();
      this.linked = {};
      this.dismissed = {};
      this.suggestions = {};
      this.libraryLoading = {};
      this.libraryError = '';
    },
    async loadLibrary(entries) {
      const targets = (entries || []).filter(entry => entry && entry.id);
      if (!targets.length) return;
      if (!this._libraryTokens) this._libraryTokens = new Map();
      const token = (this._librarySeq = (this._librarySeq || 0) + 1);
      for (const entry of targets) this._libraryTokens.set(entry.id, token);
      this.libraryLoading = { ...this.libraryLoading, ...Object.fromEntries(targets.map(entry => [entry.id, true])) };
      const current = entry => this._libraryTokens.get(entry.id) === token;
      const [states, suggestions] = await Promise.all([
        settle(async () => Object.assign({}, ...(await Promise.all(
          chunks(targets.map(entry => entry.id), LIBRARY_STATE_BATCH).map(ids => ListWatchlistLibraryState(ids))
        )).map(result => result || {}))),
        settle(async () => (await SuggestLibraryMatchesBatch(targets.map(entry => ({ title: entry.title, year: Number(entry.year) || 0 })))) || {})
      ]);
      if (this._disposed) return;
      const mine = targets.filter(current);
      if (!mine.length) return;
      const linked = { ...this.linked };
      const dismissed = { ...this.dismissed };
      const suggested = { ...this.suggestions };
      const loading = { ...this.libraryLoading };
      // 读失败的那一样保留上一次读到的（第一次就失败则仍是「没读到」，这一行不显示片库情况）。
      for (const entry of mine) {
        delete loading[entry.id];
        // 没在结果里的条目（刚被删掉）按没有关联算。
        if (states.ok) {
          linked[entry.id] = Array.isArray(states.value[entry.id]?.linked) ? states.value[entry.id].linked : [];
          dismissed[entry.id] = Array.isArray(states.value[entry.id]?.dismissed) ? states.value[entry.id].dismissed.map(Number) : [];
        }
        if (suggestions.ok) {
          const list = suggestions.value[libraryMatchKey(entry.title, entry.year)];
          suggested[entry.id] = Array.isArray(list) ? list : [];
        }
      }
      this.linked = linked;
      this.dismissed = dismissed;
      this.suggestions = suggested;
      this.libraryLoading = loading;
      const failure = !states.ok ? states.error : (!suggestions.ok ? suggestions.error : null);
      if (failure !== null) this.libraryError = String(failure);
      else if (this.entries.every(entry => this.libraryKnown(entry))) this.libraryError = '';
    },
    retryLibrary() {
      this.libraryError = '';
      return this.loadLibrary(this.entries);
    },
    libraryKnown(entry) {
      return Array.isArray(this.linked[entry.id]) && Array.isArray(this.suggestions[entry.id]);
    },
    libraryLoadingOf(entry) {
      return Boolean(this.libraryLoading[entry.id]);
    },
    linkedOf(entry) {
      return this.linked[entry.id] || [];
    },
    // 每条最多列 SUGGESTION_LIMIT 部。只在没有任何关联时才显示（已在库优先，见模板），所以已关联的不会出现在这里；
    // 这个条目解除过的视频（dismissed，含整部作品的各个版本）不再建议——解除就是「不是这部」；要关联回来用解除提示里的「撤销」。
    suggestionsOf(entry) {
      const dismissed = new Set(this.dismissed[entry.id] || []);
      return (this.suggestions[entry.id] || []).filter(item => !dismissed.has(Number(item.video_id))).slice(0, SUGGESTION_LIMIT);
    },
    // 「不在库」只在两样都读到了、既没有关联也没有建议时才说；没读出来时什么都不说。
    notInLibrary(entry) {
      return this.libraryKnown(entry) && !this.linkedOf(entry).length && !this.suggestionsOf(entry).length;
    },
    // 「确认」：把建议的这部关联上（豆瓣条目由后端写榜单那张关联表，已看同步照旧），再重读这一行。
    async confirmSuggestion(entry, suggestion) {
      if (this.busy) return;
      this.busy = true;
      try {
        await LinkWatchlistEntryVideo(entry.id, suggestion.video_id);
        this.linked = {
          ...this.linked,
          [entry.id]: [...this.linkedOf(entry), { video_id: suggestion.video_id, title: suggestion.display_title || suggestion.name, origin: 'manual', is_watched: false }]
        };
        notifySuccess('已关联片库视频');
        this.error = '';
      } catch (err) {
        this.error = `关联片库视频失败：${err}`;
        return;
      } finally {
        this.busy = false;
      }
      await this.loadLibrary([entry]);
    },
    // 「解除」：只去掉这一条关联（后端记下「解除过」，之后不再自动连这一对），不弹确认；提示里的「撤销」把它确认回来。
    async unlinkVideo(entry, video) {
      if (this.busy) return;
      this.busy = true;
      try {
        await UnlinkWatchlistEntryVideo(entry.id, video.video_id);
        this.linked = { ...this.linked, [entry.id]: this.linkedOf(entry).filter(item => item.video_id !== video.video_id) };
        // 重读回来之前先记下这一部解除过，免得它在「可能已有」里闪一下（重读会带回整部作品的各个版本）。
        this.dismissed = { ...this.dismissed, [entry.id]: [...new Set([...(this.dismissed[entry.id] || []), Number(video.video_id)])] };
        notifyUndo(`已解除「${entry.title}」与「${video.title}」的关联`, () => this.relinkVideo(entry.id, video.video_id));
        this.error = '';
      } catch (err) {
        this.error = `解除关联失败：${err}`;
        return;
      } finally {
        this.busy = false;
      }
      await this.loadLibrary([entry]);
    },
    // 撤销解除：重新确认这一对。失败时抛出，由提示的动作报「撤销失败」。页面已关掉也照样关联，只是不再刷新。
    async relinkVideo(entryID, videoID) {
      await LinkWatchlistEntryVideo(entryID, videoID);
      if (this._disposed) return;
      await this.loadLibrary(this.entries.filter(entry => entry.id === entryID));
    },
    // 补全到豆瓣条目的片单项：与年度榜单的「想看」双向同步（移除时要说明）。
    doubanIDOf(entry) {
      return entry.source_name === 'douban' && entry.source_item_id ? String(entry.source_item_id) : '';
    },
    originLabel(entry) {
      return ORIGIN_LABELS[this.origins[entry.id]] || ORIGIN_LABELS.manual;
    },
    kindLabel(kind) {
      return KIND_LABELS[kind] || KIND_LABELS[DEFAULT_KIND];
    },
    statusLabel(status) {
      return STATUS_LABELS[status] || '待补全';
    },
    sourceLabelOf(sourceName) {
      return SOURCE_LABELS[sourceName] || sourceName || '资料源';
    },
    // 源站片名只在它确实带来新信息时才显示：与用户手输的标题相同就没必要重复一遍。
    sourceTitleOf(entry) {
      const sourceTitle = (entry.source_title || '').trim();
      if (!sourceTitle) return '';
      return sourceTitle === (entry.title || '').trim() ? '' : sourceTitle;
    },
    failureText(entry) {
      const code = entry.enrichment_error;
      if (!code || entry.enrichment_status !== 'failed') return '';
      // 失败行的 source_name 可能仍是上次成功的源，不能据此归因这次失败。
      // 电影链里只有 TMDB 使用凭证，其余失败可能来自链上的任一家。
      const source = entry.kind === 'movie'
        ? (code === 'credential_missing' || code === 'credential_invalid' ? 'TMDB' : KIND_SOURCE_LABELS.movie)
        : (SOURCE_LABELS[entry.source_name] || KIND_SOURCE_LABELS[entry.kind] || '资料源');
      const build = FAILURE_TEXTS[code];
      // 认不出来的分类码原样带出去，别让一条失败在界面上消失得无影无踪。
      return build ? build(source) : `补全失败（${code}）`;
    },
    // Genres 与 Credits 在库里是 JSON 字符串，空值是空串。解析失败只让这一处的
    // 展示落空，不能把整页带崩。
    parseJSON(raw, fallback) {
      if (typeof raw !== 'string' || !raw) return fallback;
      try {
        const parsed = JSON.parse(raw);
        return parsed === null || parsed === undefined ? fallback : parsed;
      } catch {
        return fallback;
      }
    },
    genresOf(entry) {
      const parsed = this.parseJSON(entry.genres, []);
      return Array.isArray(parsed) ? parsed.filter(item => typeof item === 'string' && item) : [];
    },
    creditsText(entry) {
      const parsed = this.parseJSON(entry.credits, {});
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return '';
      const names = list => (Array.isArray(list) ? list.filter(item => typeof item === 'string' && item) : []);
      const directors = names(parsed.directors);
      const cast = names(parsed.cast);
      const parts = [];
      if (directors.length) parts.push(`导演：${directors.join('、')}`);
      if (cast.length) parts.push(`主演：${cast.slice(0, 5).join('、')}`);
      return parts.join('　');
    },
    // 条目结构里没有「有没有海报」这个字段，所以一律先取，取不到再收起来。
    posterURL(entry) {
      return `/preview/watchlist-poster/${entry.id}`;
    },
    posterVisible(entry) {
      return !this.brokenPosters.includes(entry.id);
    },
    hidePoster(entry) {
      if (!this.brokenPosters.includes(entry.id)) this.brokenPosters = [...this.brokenPosters, entry.id];
    },
    formatDate(value) {
      return new Date(value).toLocaleDateString('zh-CN');
    }
  }
};
</script>

<style scoped>
.watchlist-page { max-width: 960px; margin: 0 auto; padding: 24px 28px 40px; }
.watchlist-page--embedded { max-width: none; margin: 0; padding: 0; }
.watchlist-hint { color: var(--text-secondary); font-size: var(--fs-secondary); }
.watchlist-skeleton { margin-top: var(--space-2); }
.watchlist-form { margin: 0 0 24px; padding: 18px; border: 1px solid var(--border-color); border-radius: var(--radius-md); background: var(--surface-faint); }
.watchlist-form label { display: block; margin-bottom: 10px; font-weight: var(--fw-semibold); font-size: var(--fs-secondary); }
.watchlist-form-row, .watchlist-search-row, .watchlist-actions { display: flex; align-items: center; gap: 10px; }
.watchlist-form-row { flex-wrap: wrap; }
.watchlist-form-row .text-input, .watchlist-search-row .text-input { flex: 1; min-width: 0; }
.watchlist-kind-select { flex: 0 0 auto; min-width: 96px; }
.watchlist-form-row button, .watchlist-actions { flex-shrink: 0; }
.watchlist-search-row { margin-bottom: 16px; }
.watchlist-list { list-style: none; padding: 0; margin: 0; display: grid; gap: 10px; }
.watchlist-entry { display: flex; flex-wrap: wrap; align-items: flex-start; gap: 16px; padding: 14px 16px; border: 1px solid var(--border-color); border-radius: var(--radius-md); background: var(--surface-faint); }
.watchlist-entry-main { flex: 1; min-width: 0; display: flex; gap: 14px; }
/* align-self 不能省：父级是 flex 且默认 stretch，海报会被拉到跟简介一样高，
   再配 object-fit: cover 就只剩中间一条。aspect-ratio 给它海报该有的 2:3。 */
/* 封面按**高度**定尺寸，宽度随图片自身比例走。
   不能写死 aspect-ratio：TMDB 的电影海报是 2:3 竖版，而 AV 封面是 800×538 的
   横版整幅封套（正面+背面）。此前固定成 2/3 再 object-fit: cover，等于把横版
   封套从中间裁出一条窄竖条——放大也只是更大的窄竖条。
   contain + 自动宽度让两种比例都完整显示，且行高统一。

   按 168px 高算：竖版海报 112×168，横版封套 250×168（max-width 260 兜住更宽的）。
   高度定得比一行文字高不少，是因为横版封套按高度缩放后本来就矮——要让竖版
   海报也够看，只能把共同的高度抬上去。 */
.watchlist-poster {
  flex: 0 0 auto;
  align-self: flex-start;
  height: 168px;
  width: auto;
  max-width: 260px;
  border-radius: var(--radius-sm);
  object-fit: contain;
  background: var(--surface-muted, transparent);
}

/* 窄屏上封面让位给文字：横版封套在 400px 宽的屏幕上会挤掉标题。 */
@media (max-width: 560px) {
  .watchlist-poster { height: 120px; max-width: 190px; }
}
.watchlist-entry-info { flex: 1; min-width: 0; display: grid; gap: 6px; align-content: start; }
.watchlist-entry-info strong { overflow-wrap: anywhere; font-size: var(--fs-body); }
/* 源站片名：只读补充，视觉上明确弱于用户手输的主标题。
   长片名在窄屏上会占掉整行，所以允许换行而不是省略号——它本身就是信息。 */
.watchlist-source-title { color: var(--text-secondary); font-size: var(--fs-secondary); line-height: 1.4; word-break: break-word; }

.watchlist-entry-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; color: var(--text-secondary); font-size: var(--fs-caption); }
.watchlist-kind-tag, .watchlist-status, .watchlist-origin-tag { padding: 1px 8px; border: 1px solid var(--border-color); border-radius: var(--radius-pill); }
.watchlist-library { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; padding: 8px 10px; border: 1px dashed var(--border-color); border-radius: var(--radius-sm); font-size: var(--fs-secondary); color: var(--text-secondary); }
.watchlist-library__label { color: var(--text-primary); font-weight: var(--fw-semibold); }
.watchlist-library__match { display: inline-flex; align-items: center; gap: 6px; overflow-wrap: anywhere; }
.watchlist-library__title { color: var(--text-primary); }
.watchlist-library-skeleton { max-width: 240px; }
.watchlist-library-banner { margin-bottom: var(--space-3); }
.watchlist-failure { margin: 0; color: var(--danger-text); font-size: var(--fs-caption); overflow-wrap: anywhere; }
.watchlist-detail-line { margin: 0; color: var(--text-secondary); font-size: var(--fs-caption); overflow-wrap: anywhere; }
.watchlist-overview { margin: 0; font-size: var(--fs-secondary); line-height: 1.5; overflow-wrap: anywhere; }
.watchlist-candidates { flex: 1 0 100%; margin-top: 4px; padding: 12px; border: 1px solid var(--border-color); border-radius: var(--radius-md); background: var(--surface-color, transparent); }
.watchlist-candidates-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 10px; }
.watchlist-candidate-list { list-style: none; padding: 0; margin: 0; display: grid; gap: 8px; }
.watchlist-candidate { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; padding: 8px 10px; border: 1px solid var(--border-color); border-radius: var(--radius-sm); }
.watchlist-candidate-info { display: grid; gap: 4px; min-width: 0; }
.watchlist-candidate-meta { color: var(--text-secondary); font-size: var(--fs-caption); overflow-wrap: anywhere; }
.watchlist-candidate-overview { font-size: var(--fs-caption); line-height: 1.5; overflow-wrap: anywhere; }
.watchlist-empty { margin-top: var(--space-2); }
.watchlist-error { color: var(--danger-text); overflow-wrap: anywhere; }
.watchlist-more { display: block; margin: 20px auto 0; }
</style>
