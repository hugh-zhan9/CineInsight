<template>
  <section class="page-content watchlist-page">
    <header class="watchlist-heading">
      <h2>想看</h2>
      <p>记下想看但还没收进片库的影片，选好类型后自动去对应的资料源补全信息。</p>
    </header>

    <form class="watchlist-form" data-test="watchlist-form" @submit.prevent="save">
      <label for="watchlist-title">{{ editID ? '修改片名' : '添加想看的影片' }}</label>
      <div class="watchlist-form-row">
        <select v-if="!editID" id="watchlist-kind" v-model="kind" class="text-input watchlist-kind-select"
          aria-label="影片类型" :disabled="busy" data-test="watchlist-kind" @change="kindTouched = true">
          <option v-for="option in kindOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
        </select>
        <input id="watchlist-title" ref="titleInput" v-model="title" class="text-input" type="text"
          placeholder="输入片名或番号，回车添加" :disabled="busy" data-test="watchlist-title" />
        <button class="btn-primary" type="submit" :disabled="!canSave" data-test="watchlist-save">
          {{ busy ? '处理中…' : editID ? '保存修改' : '添加' }}
        </button>
        <button v-if="editID" class="btn-secondary" type="button" :disabled="busy"
          data-test="watchlist-cancel" @click="resetDraft">取消</button>
      </div>
    </form>

    <div class="watchlist-search-row">
      <input v-model="keyword" class="text-input" type="search" placeholder="搜索片名" aria-label="搜索想看片名"
        :disabled="busy" data-test="watchlist-search" @input="search" />
      <button class="btn-secondary" type="button" :disabled="loading || busy" @click="load()">刷新</button>
    </div>
    <p v-if="error" class="watchlist-error" role="alert" data-test="watchlist-error">{{ error }}</p>
    <p v-if="loading" class="watchlist-hint" role="status">正在读取…</p>
    <p v-else-if="loaded && !entries.length && !nextID && !error" class="watchlist-empty" data-test="watchlist-empty">
      {{ keyword.trim() ? '没有匹配的片名' : '还没有想看的影片，先记下一部吧。' }}
    </p>

    <ul class="watchlist-list">
      <li v-for="entry in entries" :key="entry.id" class="watchlist-entry" data-test="watchlist-entry">
        <div class="watchlist-entry-main">
          <img v-if="posterVisible(entry)" class="watchlist-poster" :src="posterURL(entry)" alt=""
            :data-test="`watchlist-poster-${entry.id}`" @error="hidePoster(entry)" />
          <div class="watchlist-entry-info">
            <strong>{{ entry.title }}</strong>
            <div class="watchlist-entry-meta">
              <span class="watchlist-kind-tag" :data-test="`watchlist-kind-${entry.id}`">{{ kindLabel(entry.kind) }}</span>
              <span class="watchlist-status" :data-test="`watchlist-status-${entry.id}`">{{ statusLabel(entry.enrichment_status) }}</span>
              <span v-if="entry.year">{{ entry.year }}</span>
              <span v-if="entry.rating">{{ Number(entry.rating).toFixed(1) }} 分</span>
              <time :datetime="entry.created_at">{{ formatDate(entry.created_at) }} 添加</time>
            </div>
            <p v-if="failureText(entry)" class="watchlist-failure" :data-test="`watchlist-failure-${entry.id}`">
              {{ failureText(entry) }}
            </p>
            <p v-if="genresOf(entry).length" class="watchlist-detail-line">{{ genresOf(entry).join(' · ') }}</p>
            <p v-if="creditsText(entry)" class="watchlist-detail-line">{{ creditsText(entry) }}</p>
            <p v-if="entry.overview" class="watchlist-overview">{{ entry.overview }}</p>
          </div>
        </div>
        <div class="watchlist-actions">
          <button v-if="entry.enrichment_status === 'failed'" class="btn-secondary btn-compact" type="button"
            :disabled="busy || loading" :data-test="`watchlist-retry-${entry.id}`" @click="retry(entry)">重试补全</button>
          <button class="btn-secondary btn-compact" type="button" :disabled="busy || loading"
            :data-test="`watchlist-candidates-${entry.id}`" @click="openCandidates(entry)">重选结果</button>
          <button class="btn-secondary btn-compact" type="button" :disabled="busy || loading"
            :data-test="`watchlist-edit-${entry.id}`" @click="startEdit(entry)">改名</button>
          <button class="btn-secondary btn-danger-outline btn-compact" type="button" :disabled="busy || loading"
            :data-test="`watchlist-remove-${entry.id}`" @click="remove(entry)">移除</button>
        </div>

        <div v-if="candidateEntryID === entry.id" class="watchlist-candidates" data-test="watchlist-candidate-panel">
          <div class="watchlist-candidates-head">
            <strong>为「{{ entry.title }}」重选匹配结果</strong>
            <button class="btn-secondary btn-compact" type="button" data-test="watchlist-candidate-close"
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
              <button class="btn-primary btn-compact" type="button" :disabled="busy"
                :data-test="`watchlist-candidate-apply-${index}`" @click="applyCandidate(candidate)">用这个</button>
            </li>
          </ul>
        </div>
      </li>
    </ul>
    <button v-if="nextID" class="btn-secondary watchlist-more" type="button" :disabled="loading || busy"
      data-test="watchlist-more" @click="load(true)">加载更多</button>
  </section>
</template>

<script>
import {
  ApplyWatchlistCandidate, CreateWatchlistEntry, DeleteWatchlistEntry, ListWatchlist,
  ListWatchlistCandidates, RetryWatchlistEnrichment, UpdateWatchlistEntry
} from '../../wailsjs/go/main/App';
import { confirmAction, notifySuccess } from '../utils/feedback.js';

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

const STATUS_LABELS = {
  pending: '待补全',
  running: '补全中…',
  succeeded: '已补全',
  failed: '补全失败',
  manual: '手动维护'
};

// 资料源的展示名。条目只在补全成功后才有 source_name，失败时退回按类型推断该问谁。
const SOURCE_LABELS = { tmdb: 'TMDB', bangumi: 'Bangumi', fanza: 'FANZA', javbus: 'JavBus' };
const KIND_SOURCE_LABELS = { movie: 'TMDB', tv: 'TMDB', show: 'TMDB', anime: 'Bangumi', av: 'AV 资料源' };

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

export default {
  name: 'WatchlistPage',
  data() {
    return {
      entries: [], nextID: 0, keyword: '', title: '', kind: DEFAULT_KIND, kindTouched: false,
      editID: 0, error: '', loading: false, loaded: false, busy: false, requestID: 0,
      candidateEntryID: 0, candidates: [], candidateLoading: false, candidateError: '', candidateRequestID: 0,
      brokenPosters: [], kindOptions: KIND_OPTIONS
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
      this.kind = AV_CODE_PATTERN.test(value.trim()) ? 'av' : DEFAULT_KIND;
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
        this.nextID = page.next_id;
        this.loaded = true;
      } catch (err) {
        if (requestID === this.requestID) this.error = `读取想看片单失败：${err}`;
      } finally {
        if (requestID === this.requestID) this.loading = false;
      }
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
        if (!await confirmAction({ title: '移除想看记录', message: `从想看片单移除「${entry.title}」？`, confirmText: '移除', danger: true })) return;
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
        this.entries.splice(index, 1, fresh);
      } catch (err) {
        this.error = `刷新想看条目失败：${err}`;
      }
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
    failureText(entry) {
      const code = entry.enrichment_error;
      if (!code || entry.enrichment_status !== 'failed') return '';
      const source = SOURCE_LABELS[entry.source_name] || KIND_SOURCE_LABELS[entry.kind] || '资料源';
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
.watchlist-heading h2 { margin: 0 0 6px; }
.watchlist-heading p, .watchlist-hint { color: var(--text-secondary); font-size: 13px; }
.watchlist-form { margin: 24px 0; padding: 18px; border: 1px solid var(--border-color); border-radius: var(--radius-md); background: var(--surface-faint); }
.watchlist-form label { display: block; margin-bottom: 10px; font-weight: 600; font-size: 13px; }
.watchlist-form-row, .watchlist-search-row, .watchlist-actions { display: flex; align-items: center; gap: 10px; }
.watchlist-form-row { flex-wrap: wrap; }
.watchlist-form-row .text-input, .watchlist-search-row .text-input { flex: 1; min-width: 0; }
.watchlist-kind-select { flex: 0 0 auto; min-width: 96px; }
.watchlist-form-row button, .watchlist-actions { flex-shrink: 0; }
.watchlist-search-row { margin-bottom: 16px; }
.watchlist-list { list-style: none; padding: 0; margin: 0; display: grid; gap: 10px; }
.watchlist-entry { display: flex; flex-wrap: wrap; align-items: flex-start; gap: 16px; padding: 14px 16px; border: 1px solid var(--border-color); border-radius: var(--radius-md); background: var(--surface-faint); }
.watchlist-entry-main { flex: 1; min-width: 0; display: flex; gap: 14px; }
.watchlist-poster { flex: 0 0 auto; width: 64px; border-radius: var(--radius-sm); object-fit: cover; background: var(--surface-muted, transparent); }
.watchlist-entry-info { flex: 1; min-width: 0; display: grid; gap: 6px; align-content: start; }
.watchlist-entry-info strong { overflow-wrap: anywhere; font-size: 15px; }
.watchlist-entry-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; color: var(--text-secondary); font-size: 12px; }
.watchlist-kind-tag, .watchlist-status { padding: 1px 8px; border: 1px solid var(--border-color); border-radius: 999px; }
.watchlist-failure { margin: 0; color: var(--danger-color); font-size: 12px; overflow-wrap: anywhere; }
.watchlist-detail-line { margin: 0; color: var(--text-secondary); font-size: 12px; overflow-wrap: anywhere; }
.watchlist-overview { margin: 0; font-size: 13px; line-height: 1.5; overflow-wrap: anywhere; }
.watchlist-candidates { flex: 1 0 100%; margin-top: 4px; padding: 12px; border: 1px solid var(--border-color); border-radius: var(--radius-md); background: var(--surface-color, transparent); }
.watchlist-candidates-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 10px; }
.watchlist-candidate-list { list-style: none; padding: 0; margin: 0; display: grid; gap: 8px; }
.watchlist-candidate { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; padding: 8px 10px; border: 1px solid var(--border-color); border-radius: var(--radius-sm); }
.watchlist-candidate-info { display: grid; gap: 4px; min-width: 0; }
.watchlist-candidate-meta { color: var(--text-secondary); font-size: 12px; overflow-wrap: anywhere; }
.watchlist-candidate-overview { font-size: 12px; line-height: 1.5; overflow-wrap: anywhere; }
.watchlist-empty { padding: 40px 16px; text-align: center; color: var(--text-secondary); }
.watchlist-error { color: var(--danger-color); overflow-wrap: anywhere; }
.watchlist-more { display: block; margin: 20px auto 0; }
</style>
