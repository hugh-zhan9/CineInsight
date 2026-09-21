<template>
  <section class="page-content movie-chart-page">
    <header class="movie-chart-heading">
      <h2>年度榜单</h2>
      <p>按年份浏览在内地公映的电影（含尚未上映的），看到合适的就直接标「想看 / 不想看 / 已看」。</p>
    </header>

    <div class="movie-chart-toolbar">
      <label class="movie-chart-field">
        <span>年份</span>
        <select v-model.number="year" class="select-input" aria-label="榜单年份" :disabled="busy || loading"
          data-test="movie-chart-year" @change="switchYear">
          <option v-for="option in years" :key="option" :value="option">{{ option }} 年</option>
        </select>
      </label>

      <div class="movie-chart-sort" role="group" aria-label="榜单排序">
        <button type="button" :class="['movie-chart-sort-btn', { active: sort === 'release' }]"
          :disabled="busy || loading" data-test="movie-chart-sort-release"
          @click="switchSort('release')">按上映时间</button>
        <button type="button" :class="['movie-chart-sort-btn', { active: sort === 'rating' }]"
          :disabled="busy || loading" data-test="movie-chart-sort-rating"
          @click="switchSort('rating')">按评分</button>
      </div>

      <label class="movie-chart-toggle">
        <input v-model="showMarked" type="checkbox" :disabled="busy || loading"
          data-test="movie-chart-show-marked" @change="switchShowMarked" />
        <span>不过滤</span>
      </label>

      <div class="movie-chart-toolbar-actions">
        <button class="btn-secondary btn-compact" type="button" :disabled="busy || loading || cache.refreshing"
          data-test="movie-chart-refresh" @click="refresh">刷新</button>
        <!-- 取消只在真的有一轮在跑时出现：抓一整年要二十多分钟，没有它用户只能等。 -->
        <button v-if="cache.refreshing" class="btn-secondary btn-compact" type="button" :disabled="busy"
          data-test="movie-chart-cancel" @click="cancelRefresh">取消刷新</button>
      </div>
    </div>

    <!-- 状态条是「豆瓣不可达时不显示空榜单」这条要求的落点：三种形态互斥，
         失败永远带上原因，绝不退化成一句「查无数据」。 -->
    <p :class="['movie-chart-status', `movie-chart-status--${cacheStage}`]" role="status"
      data-test="movie-chart-status">{{ statusText }}</p>

    <p v-if="backfillText" class="movie-chart-backfill" role="status" data-test="movie-chart-backfill">
      {{ backfillText }}
    </p>

    <p class="movie-chart-note" data-test="movie-chart-scope-note">
      榜单按年份从豆瓣多个排序合并抓取，<strong>非全量收录</strong>；口径是「在内地公映（含引进片）」。
      「不过滤」只让被「不想看 / 已看」隐藏的条目重新出现，不会放宽这个口径。
    </p>

    <p v-if="error" class="movie-chart-error" role="alert" data-test="movie-chart-error">{{ error }}</p>
    <p v-if="actionError" class="movie-chart-error" role="alert" data-test="movie-chart-action-error">{{ actionError }}</p>
    <!-- 撞名不是失败：标记已经记下了，所以这条用中性样式，和上面的报错分开。 -->
    <p v-if="notice" class="movie-chart-notice" role="status" data-test="movie-chart-notice">{{ notice }}</p>

    <p v-if="loading && !items.length" class="movie-chart-hint" role="status" data-test="movie-chart-loading">正在读取…</p>

    <div v-else-if="!items.length" class="movie-chart-empty" data-test="movie-chart-empty">
      <p>{{ emptyText }}</p>
      <button v-if="emptyRefreshVisible" class="btn-primary btn-compact" type="button" :disabled="busy || loading"
        data-test="movie-chart-empty-refresh" @click="refresh">点此刷新</button>
    </div>

    <ul v-else class="movie-chart-list">
      <li v-for="item in items" :key="item.douban_id" class="movie-chart-item" data-test="movie-chart-item">
        <!-- 只有 has_poster 为真才发请求：没有海报地址的条目走代理路由必然 404。
             地址永远是本地代理路由，不拼远程图床——豆瓣图床对本应用的 Origin 直接 403。 -->
        <img v-if="posterVisible(item)" class="movie-chart-poster" :src="posterURL(item)" alt=""
          loading="lazy" data-test="movie-chart-poster" @error="hidePoster(item)" />
        <div class="movie-chart-info">
          <strong class="movie-chart-title" data-test="movie-chart-title">{{ item.title }}</strong>
          <span v-if="originalTitleOf(item)" class="movie-chart-original">{{ originalTitleOf(item) }}</span>
          <div class="movie-chart-meta">
            <span :class="['movie-chart-release', { 'movie-chart-release--pending': !item.release_scope }]"
              data-test="movie-chart-release">{{ releaseLabel(item) }}</span>
            <span v-if="item.rating" data-test="movie-chart-rating">
              {{ Number(item.rating).toFixed(1) }} 分<template v-if="item.rating_count">（{{ item.rating_count }} 人）</template>
            </span>
            <span v-if="item.mark" class="movie-chart-mark-tag" data-test="movie-chart-mark-tag">{{ markLabel(item.mark) }}</span>
          </div>
          <p v-if="item.card_subtitle" class="movie-chart-subtitle">{{ item.card_subtitle }}</p>
          <!-- 补全失败单独一行：上面的「上映信息待确认」只说这条还没判定，
               不该让它自己看起来像一条错误。 -->
          <p v-if="detailErrorText(item)" class="movie-chart-detail-error" data-test="movie-chart-detail-error">
            {{ detailErrorText(item) }}
          </p>
        </div>
        <div class="movie-chart-actions">
          <button v-for="option in markOptions" :key="option.value" type="button"
            :class="['btn-secondary', 'btn-compact', { 'movie-chart-mark--on': item.mark === option.value }]"
            :aria-pressed="item.mark === option.value ? 'true' : 'false'"
            :title="item.mark === option.value ? '再次点击撤销' : ''"
            :disabled="busy || loading" :data-test="`movie-chart-mark-${option.value}`"
            @click="toggleMark(item, option.value)">{{ option.label }}</button>
        </div>
      </li>
    </ul>

    <div v-if="totalPages > 1" class="movie-chart-pager">
      <button class="btn-secondary btn-compact" type="button" :disabled="busy || loading || page <= 1"
        data-test="movie-chart-prev" @click="goPage(page - 1)">上一页</button>
      <span class="movie-chart-page-info" data-test="movie-chart-page-info">
        第 {{ page }} / {{ totalPages }} 页 · 共 {{ total }} 条
      </span>
      <button class="btn-secondary btn-compact" type="button" :disabled="busy || loading || page >= totalPages"
        data-test="movie-chart-next" @click="goPage(page + 1)">下一页</button>
    </div>
  </section>
</template>

<script>
import {
  CancelMovieChartRefresh, ClearMovieChartMark, ListMovieChart, ListMovieChartYears,
  MarkMovieChartEntry, OpenMovieChartYear, RefreshMovieChart
} from '../../wailsjs/go/main/App';
import { notifySuccess } from '../utils/feedback.js';

// 六类失败分类码各有各的一句话，**不合并**（D-MC07）。把代理连不上和「豆瓣没收录」
// 写成同一句，用户会照着换年份的方向排查，而真正要做的是去设置页改代理地址。
// credential_* 两类在本源上永不产生（豆瓣榜单端点无凭证），但枚举值保留、文案也
// 保留：分类码是后端的契约，界面不能因为「用不到」就把它折进别的分支。
const FAILURE_TEXTS = {
  credential_missing: '未配置豆瓣访问凭证',
  credential_invalid: '豆瓣访问凭证无效或已过期',
  proxy_unreachable: '资料源出网代理不可用，请检查设置里的代理地址',
  network_unreachable: '无法连接豆瓣，请检查网络',
  not_found: '豆瓣没有收录该条目',
  source_error: '豆瓣返回异常（可能被反爬拦截），稍后再试'
};

// 三个标记按钮，顺序即界面顺序。值与 models.MovieChartMark* 一致。
const MARK_OPTIONS = [
  { value: 'want', label: '想看' },
  { value: 'skip', label: '不想看' },
  { value: 'watched', label: '已看' }
];
const MARK_LABELS = Object.fromEntries(MARK_OPTIONS.map(option => [option.value, option.label]));

// 抓一整年要二十多分钟，状态条与「补全中 N/M」不轮询就会一直停在用户打开页面的
// 那一刻：刷新跑完了取消按钮还在，补全进度永远是同一个数字。
// 轮询只调 ListMovieChart（纯读、无副作用），**绝不**调 OpenMovieChartYear。
const STATUS_POLL_MS = 5000;

const EMPTY_CACHE = { last_refreshed_at: null, last_failure: '', refreshing: false };
const EMPTY_BACKFILL = { pending: 0, total: 0 };

export default {
  name: 'MovieChartPage',
  data() {
    const currentYear = new Date().getFullYear();
    return {
      years: [currentYear],
      year: currentYear,
      loadedYear: currentYear,
      sort: 'release',
      page: 1,
      pageSize: 20,
      total: 0,
      showMarked: false,
      items: [],
      cache: { ...EMPTY_CACHE },
      backfill: { ...EMPTY_BACKFILL },
      loading: false,
      busy: false,
      error: '',
      actionError: '',
      notice: '',
      requestID: 0,
      brokenPosters: [],
      pollTimer: null,
      markOptions: MARK_OPTIONS
    };
  },
  computed: {
    totalPages() {
      return Math.max(1, Math.ceil(this.total / (this.pageSize || 20)));
    },
    // 三形态（概要设计 §4.2）加上「从未抓过也没失败」这第四种：后者不是失败，
    // 不能和「无缓存 + 失败」合并成同一句（D-MC07）。
    cacheStage() {
      const refreshedAt = this.cache.last_refreshed_at;
      const failure = this.cache.last_failure;
      if (refreshedAt && !failure) return 'fresh';
      if (refreshedAt && failure) return 'stale';
      if (failure) return 'failed';
      return 'never';
    },
    failureText() {
      return this.failureTextOf(this.cache.last_failure);
    },
    statusText() {
      const time = this.formatTime(this.cache.last_refreshed_at);
      switch (this.cacheStage) {
        case 'fresh':
          return `数据更新于 ${time}`;
        case 'stale':
          return `数据来自缓存（${time}），最近一次更新失败：${this.failureText}`;
        case 'failed':
          return `暂无该年数据，更新失败：${this.failureText}`;
        default:
          return this.cache.refreshing ? '该年数据正在首次抓取…' : '该年还没有抓取过';
      }
    },
    backfillText() {
      const pending = Number(this.backfill.pending || 0);
      const total = Number(this.backfill.total || 0);
      if (pending <= 0 || total <= 0) return '';
      return `补全中 ${pending}/${total}`;
    },
    emptyText() {
      if (this.cacheStage === 'failed') return `暂无该年数据，更新失败：${this.failureText}`;
      if (this.cache.refreshing) return '正在抓取该年榜单，稍后条目会陆续出现。';
      if (this.cacheStage === 'never') return '该年还没有抓取过，点刷新获取。';
      if (!this.showMarked) return '该年榜单里没有未标记的条目，可以打开「不过滤」看看已标记的。';
      return '该年榜单暂时没有可显示的条目。';
    },
    emptyRefreshVisible() {
      return !this.cache.refreshing && (this.cacheStage === 'failed' || this.cacheStage === 'never');
    },
    needsStatusPoll() {
      return Boolean(this.cache.refreshing) || Number(this.backfill.pending || 0) > 0;
    }
  },
  mounted() {
    this.enter();
  },
  beforeUnmount() {
    // 卸载后到达的响应一律丢弃，定时器也一并停掉。
    this.requestID++;
    this.clearStatusPoll();
  },
  methods: {
    // enter 是「打开榜单页」这个动作：拉年份下拉、报一次 OpenMovieChartYear、再读第一页。
    async enter() {
      await this.loadYears();
      await this.openYear();
      await this.load();
    },
    async loadYears() {
      try {
        const years = (await ListMovieChartYears()) || [];
        if (!years.length) return;
        this.years = years;
        // 后端保证当前年一定在列表里；万一不在，别让下拉框显示成空。
        if (!years.includes(this.year)) this.year = years[0];
      } catch (err) {
        // 年份列表取不到不该挡住榜单本身：下拉里至少还有当前年。
        this.actionError = `读取榜单年份失败：${err}`;
      }
    },
    // openYear 是 D-MC12 自动刷新的**唯一触发点**，只在挂载与切年份时调。
    //
    // 不能挂到 load() 上：翻页、切排序、轮询状态条都会走 load()，没有一个是「打开了
    // 页面」。挂上去之后，用户点了取消，下一次为刷新状态条而发的读就会把那一轮原样
    // 重启（取消既不写失败码也不写成功时间，后端仍判到期），取消按钮等于没有；
    // 而一个持续失败的源会按每次翻页约 100 次豆瓣请求的速度重试。
    async openYear() {
      try {
        await OpenMovieChartYear(this.year);
      } catch (err) {
        this.actionError = `检查该年榜单失败：${err}`;
      }
    },
    // load 只读本地缓存，没有任何副作用。silent 用于轮询：不亮加载态，避免列表闪。
    async load({ silent = false } = {}) {
      const requestID = ++this.requestID;
      const year = this.year;
      const sort = this.sort;
      this.clearStatusPoll();
      if (!silent) {
        this.loading = true;
        this.error = '';
      }
      try {
        const result = await ListMovieChart(year, sort, this.page, this.showMarked);
        // 两道过时响应守卫：序号挡住「后发先至」，回显挡住一个明确不属于当前视图的
        // 响应（切年份 / 切排序时上一次请求可能后到）。
        if (requestID !== this.requestID) return;
        if (!result || result.year !== year || result.sort !== sort) return;
        this.items = result.items || [];
        this.total = Number(result.total || 0);
        this.pageSize = Number(result.page_size || 20) || 20;
        this.loadedYear = result.year;
        this.cache = result.cache || { ...EMPTY_CACHE };
        this.backfill = result.backfill || { ...EMPTY_BACKFILL };
        this.brokenPosters = [];
        this.error = '';
      } catch (err) {
        if (requestID === this.requestID) this.error = `读取榜单失败：${err}`;
      } finally {
        if (requestID === this.requestID) {
          this.loading = false;
          this.scheduleStatusPoll();
        }
      }
    },
    clearStatusPoll() {
      if (this.pollTimer === null) return;
      clearTimeout(this.pollTimer);
      this.pollTimer = null;
    },
    scheduleStatusPoll() {
      this.clearStatusPoll();
      if (!this.needsStatusPoll) return;
      this.pollTimer = setTimeout(() => {
        this.pollTimer = null;
        if (this.busy || this.loading) {
          this.scheduleStatusPoll();
          return;
        }
        this.load({ silent: true });
      }, STATUS_POLL_MS);
    },
    // 切年份是第二个、也是最后一个 OpenMovieChartYear 的调用点。
    async switchYear() {
      this.page = 1;
      this.notice = '';
      this.actionError = '';
      await this.openYear();
      await this.load();
    },
    // 切排序、切「不过滤」、翻页都只是重新读一页，**不报打开**。
    switchSort(sort) {
      if (this.sort === sort || this.busy || this.loading) return;
      this.sort = sort;
      this.page = 1;
      return this.load();
    },
    switchShowMarked() {
      this.page = 1;
      return this.load();
    },
    goPage(page) {
      if (this.busy || this.loading) return;
      const next = Math.min(Math.max(1, page), this.totalPages);
      if (next === this.page) return;
      this.page = next;
      return this.load();
    },
    async refresh() {
      if (this.busy || this.loading) return;
      this.busy = true;
      this.actionError = '';
      this.notice = '';
      try {
        await RefreshMovieChart(this.year);
        notifySuccess('已开始刷新该年榜单');
      } catch (err) {
        this.actionError = String(err);
      } finally {
        this.busy = false;
      }
      await this.load();
    },
    async cancelRefresh() {
      if (this.busy) return;
      this.busy = true;
      this.actionError = '';
      try {
        await CancelMovieChartRefresh();
        notifySuccess('已取消本轮刷新');
      } catch (err) {
        this.actionError = String(err);
      } finally {
        this.busy = false;
      }
      await this.load();
    },
    // 点当前已生效的那个标记＝撤销，其余＝改标记。撤销入口因此在「不过滤」视图里
    // 也一直在：被「不想看 / 已看」隐藏的条目露出来之后，原地再点一次就取消了。
    async toggleMark(item, mark) {
      if (this.busy || this.loading) return;
      const undo = item.mark === mark;
      this.busy = true;
      this.actionError = '';
      this.notice = '';
      let conflict = false;
      try {
        if (undo) {
          await ClearMovieChartMark(item.douban_id);
        } else {
          const result = await MarkMovieChartEntry(item.douban_id, mark);
          conflict = Boolean(result && result.watchlist_conflict);
        }
      } catch (err) {
        this.actionError = String(err);
        this.busy = false;
        return;
      }
      this.busy = false;
      await this.load();
      // 当页被标空但后端还有内容时退一页，别把用户留在一个空白页上。
      if (!this.items.length && this.page > 1) {
        this.page = Math.min(this.page - 1, this.totalPages);
        await this.load();
      }
      if (undo) {
        notifySuccess('已撤销标记');
      } else if (conflict) {
        // 撞名不是失败：标记照样记下了，只是没有新建片单条目，撤销时也不会去动
        // 用户自己手输的那一条（D-MC13）。
        this.notice = '已标记想看。该片名已在想看片单中，未重复添加。';
      } else {
        notifySuccess(`已标记${MARK_LABELS[mark] || mark}`);
      }
    },
    failureTextOf(code) {
      if (!code) return '';
      // 认不出来的分类码原样带出去，别让一次失败在界面上消失得无影无踪。
      return FAILURE_TEXTS[code] || `未知失败（${code}）`;
    },
    // 需求设计文档 §3 的展示表：四种 release_scope 各有各的说法，空串是
    // 「详情还没补全」而不是「不上映」，所以照常显示、只标待确认。
    releaseLabel(item) {
      const scope = item.release_scope;
      if (scope === 'theatrical') {
        return item.release_date ? `${item.release_date} 上映` : `${this.loadedYear}年内地上映·未定档`;
      }
      if (scope === 'undetermined') return '档期未定';
      if (!scope) return '上映信息待确认';
      return scope;
    },
    detailErrorText(item) {
      if (item.detail_status !== 'failed' || !item.detail_error) return '';
      return `补全失败：${this.failureTextOf(item.detail_error)}`;
    },
    markLabel(mark) {
      return MARK_LABELS[mark] || mark;
    },
    originalTitleOf(item) {
      const original = (item.original_title || '').trim();
      if (!original) return '';
      return original === (item.title || '').trim() ? '' : original;
    },
    posterURL(item) {
      return `/preview/douban-chart-poster/${item.douban_id}`;
    },
    posterVisible(item) {
      return Boolean(item.has_poster) && !this.brokenPosters.includes(item.douban_id);
    },
    hidePoster(item) {
      if (!this.brokenPosters.includes(item.douban_id)) {
        this.brokenPosters = [...this.brokenPosters, item.douban_id];
      }
    },
    formatTime(value) {
      if (!value) return '';
      const date = new Date(value);
      return Number.isNaN(date.getTime()) ? '' : date.toLocaleString('zh-CN');
    }
  }
};
</script>

<style scoped>
.movie-chart-page { max-width: 1000px; margin: 0 auto; padding: 24px 28px 40px; }
.movie-chart-heading h2 { margin: 0 0 6px; }
.movie-chart-heading p, .movie-chart-hint, .movie-chart-note { color: var(--text-secondary); font-size: 13px; }
.movie-chart-toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; margin: 20px 0 12px; }
.movie-chart-field { display: flex; align-items: center; gap: 8px; font-size: 13px; }
.movie-chart-field .select-input { min-width: 108px; }
.movie-chart-sort { display: flex; padding: 3px; border: 1px solid var(--border-color); border-radius: 9px; background: var(--control-bg); }
.movie-chart-sort-btn { min-height: 30px; padding: 0 12px; border: 0; border-radius: 6px; background: transparent; color: var(--text-secondary); cursor: pointer; font-size: 12px; }
.movie-chart-sort-btn.active { background: var(--accent-soft); color: var(--accent-color); font-weight: 600; }
.movie-chart-sort-btn:disabled { opacity: 0.5; cursor: not-allowed; }
.movie-chart-toggle { display: flex; align-items: center; gap: 6px; font-size: 13px; }
.movie-chart-toggle input { width: 16px; height: 16px; margin: 0; accent-color: var(--accent-color); }
.movie-chart-toolbar-actions { display: flex; align-items: center; gap: 8px; margin-left: auto; }
.movie-chart-status { margin: 0 0 8px; font-size: 13px; color: var(--text-secondary); }
.movie-chart-status--stale, .movie-chart-status--failed { color: var(--danger-color); }
.movie-chart-backfill { margin: 0 0 8px; font-size: 12px; color: var(--text-secondary); }
.movie-chart-note { margin: 0 0 14px; line-height: 1.6; }
.movie-chart-error { color: var(--danger-color); overflow-wrap: anywhere; }
.movie-chart-notice { color: var(--text-secondary); font-size: 13px; overflow-wrap: anywhere; }
.movie-chart-empty { padding: 40px 16px; text-align: center; color: var(--text-secondary); display: grid; justify-items: center; gap: 12px; }
.movie-chart-list { list-style: none; padding: 0; margin: 0; display: grid; gap: 10px; }
.movie-chart-item { display: flex; flex-wrap: wrap; align-items: flex-start; gap: 14px; padding: 14px 16px; border: 1px solid var(--border-color); border-radius: var(--radius-md); background: var(--surface-faint); }
/* 海报按高度定尺寸、宽度随原图比例走，与想看片单同形：豆瓣海报基本是 2:3，
   但个别条目是横版剧照，写死 aspect-ratio 会把它裁成一条。 */
.movie-chart-poster { flex: 0 0 auto; align-self: flex-start; height: 132px; width: auto; max-width: 110px; border-radius: var(--radius); object-fit: contain; background: var(--surface-muted, transparent); }
.movie-chart-info { flex: 1; min-width: 0; display: grid; gap: 6px; align-content: start; }
.movie-chart-title { font-size: 15px; overflow-wrap: anywhere; }
.movie-chart-original { color: var(--text-secondary); font-size: 13px; overflow-wrap: anywhere; }
.movie-chart-meta { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; color: var(--text-secondary); font-size: 12px; }
.movie-chart-release { padding: 1px 8px; border: 1px solid var(--border-color); border-radius: 999px; }
/* 待确认用中性描边，不用告警色：它只是还没补全，不是出错了。 */
.movie-chart-release--pending { border-style: dashed; }
.movie-chart-mark-tag { padding: 1px 8px; border-radius: 999px; background: var(--accent-soft); color: var(--accent-color); }
.movie-chart-subtitle { margin: 0; color: var(--text-secondary); font-size: 12px; overflow-wrap: anywhere; }
.movie-chart-detail-error { margin: 0; color: var(--danger-color); font-size: 12px; overflow-wrap: anywhere; }
.movie-chart-actions { display: flex; flex-shrink: 0; align-items: center; gap: 8px; }
.movie-chart-mark--on { border-color: var(--accent-color); color: var(--accent-color); font-weight: 600; }
.movie-chart-pager { display: flex; align-items: center; justify-content: center; gap: 14px; margin-top: 20px; }
.movie-chart-page-info { color: var(--text-secondary); font-size: 12px; }

@media (max-width: 560px) {
  .movie-chart-poster { height: 96px; max-width: 80px; }
  .movie-chart-toolbar-actions { margin-left: 0; }
  .movie-chart-actions { flex-wrap: wrap; }
}
</style>
