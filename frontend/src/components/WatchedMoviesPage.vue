<template>
  <section class="page-content watched-movies-page">
    <header class="watched-movies-heading">
      <!-- 改名「观影记录」（D-PC52）：原来的「已看」与片库的「已看」视图同名，被当成同一回事。 -->
      <h2>观影记录</h2>
      <p>你在年度榜单上标过「已看」的电影，按影片上映年份从新到旧分组。</p>
    </header>

    <div class="watched-movies-toolbar">
      <span class="watched-movies-summary" data-test="watched-movies-summary">共 {{ total }} 部</span>
      <button class="btn-secondary btn-compact" type="button" :disabled="loading"
        data-test="watched-movies-reload" @click="load">重新读取</button>
    </div>

    <!-- 渲染的仍然只是 ListWatchedMovies 的返回值（榜单标记），一条不多。D-PC52 推翻了
         D-MC11 的「两边互不影响」：关联片库视频之后两边的已看状态双向同步，这句话跟着改。 -->
    <p class="watched-movies-note" data-test="watched-movies-scope-note">
      这里的「已看」是你在榜单上标记的。把它关联到片库里的视频后，两边的已看状态会互相同步：视频看完会记到这里，在这里取消已看，关联的视频也会改回未看。
    </p>

    <p v-if="error" class="watched-movies-error" role="alert" data-test="watched-movies-error">{{ error }}</p>
    <p v-if="actionError" class="watched-movies-error" role="alert" data-test="watched-movies-action-error">{{ actionError }}</p>

    <p v-if="loading && !groups.length" class="watched-movies-hint" role="status"
      data-test="watched-movies-loading">正在读取…</p>

    <!-- 读失败不落到空态：「还没标过」和「这次没读出来」是两件事，后者已经在上面报了。 -->
    <p v-else-if="!groups.length && !error" class="watched-movies-empty"
      data-test="watched-movies-empty">还没有标记过已看</p>

    <!-- 后端已按年份倒序分组、组内按标记时间倒序（services/movie_chart_query.go 的
         ListWatched），这里**原样渲染**：不重排、不重新分组、也不合并同年份的组。 -->
    <div v-for="group in groups" :key="group.year" class="watched-movies-group" data-test="watched-movies-group">
      <h3 class="watched-movies-group-title" data-test="watched-movies-group-title">{{ groupLabel(group) }}</h3>
      <ul class="watched-movies-list">
        <li v-for="movie in group.items" :key="movie.douban_id" class="watched-movies-item"
          data-test="watched-movies-item">
          <!-- 片名、海报都取自标记行的快照，所以榜单缓存被整年重建、条目从豆瓣下架之后
               这一页照样完整。海报地址只能是本地代理路由：豆瓣图床对本应用的 Origin 直接 403，
               而 has_poster 为假时那条路由必然 404，所以不发这个请求。 -->
          <img v-if="posterVisible(movie)" class="watched-movies-poster" :src="posterURL(movie)" alt=""
            loading="lazy" data-test="watched-movies-poster" @error="hidePoster(movie)" />
          <div class="watched-movies-info">
            <strong class="watched-movies-title" data-test="watched-movies-title">{{ movie.title }}</strong>
            <span v-if="markedAtText(movie)" class="watched-movies-marked-at"
              data-test="watched-movies-marked-at">{{ markedAtText(movie) }}</span>
            <!-- 没关联时给出「片库中可能已有」的只读建议，用户一键关联（D-PC52）。 -->
            <div v-if="!linkedIDs(movie).length && suggestionsOf(movie).length" class="watched-movies-library"
              :data-test="`watched-movies-suggestions-${movie.douban_id}`">
              <span class="watched-movies-library__label">片库中可能已有：</span>
              <span v-for="suggestion in suggestionsOf(movie)" :key="suggestion.video_id" class="watched-movies-library__match">
                {{ suggestion.display_title || suggestion.name }}
                <button class="btn-secondary btn-compact" type="button" :disabled="busy"
                  :data-test="`watched-movies-link-${movie.douban_id}-${suggestion.video_id}`"
                  @click="linkSuggestion(movie, suggestion)">关联</button>
              </span>
            </div>
          </div>
          <div class="watched-movies-actions">
            <button v-if="linkedIDs(movie).length" class="btn-secondary btn-compact" type="button" :disabled="busy"
              :data-test="`watched-movies-open-${movie.douban_id}`" @click="openInLibrary(movie)">在片库打开</button>
            <!-- 关联错了要能撤回（D-PC52）：只断开同步，两边已有的已看标记都不动。 -->
            <button v-if="linkedIDs(movie).length" class="btn-secondary btn-compact" type="button" :disabled="busy"
              :data-test="`watched-movies-unlink-${movie.douban_id}`" @click="unlink(movie)">取消关联</button>
            <button class="btn-secondary btn-compact" type="button" :disabled="busy"
              :data-test="`watched-movies-chart-${movie.douban_id}`" @click="$emit('navigate', 'movie-chart')">去榜单</button>
            <button class="btn-secondary btn-danger-outline btn-compact" type="button" :disabled="busy"
              :data-test="`watched-movies-unwatch-${movie.douban_id}`" @click="unwatch(movie)">取消已看</button>
          </div>
        </li>
      </ul>
    </div>
  </section>
</template>

<script>
// 本页的绑定只有这几个：读观影记录、撤销标记、关联 / 取消关联与建议、按 ID 取视频（在片库打开）。
// **不读榜单缓存**（ListMovieChart 等）：观影记录在缓存清空后仍要完整可用（D-MC05 快照列存在的全部理由）。
import {
  ClearMovieChartMark, GetVideosByIDs, LinkMovieToVideo, ListWatchedMovies, SuggestLibraryMatchesBatch, UnlinkMovieVideo
} from '../../wailsjs/go/main/App';
import { confirmAction, notifySuccess } from '../utils/feedback.js';

// 与后端 services.LibraryMatchKey 一致：「片名|年份」，年份未知为 0。
function libraryMatchKey(title, year) {
  return `${title}|${Number(year) || 0}`;
}

// 「去榜单」「在片库打开」要切页面，本页不认识 App 的页面状态，只发出意图：
// navigate(pageKey) 与 open-video(video)，由 App.vue 接（pageKey 与 currentPage 同一套取值）。
export default {
  name: 'WatchedMoviesPage',
  emits: ['navigate', 'open-video'],
  data() {
    return {
      groups: [],
      loading: false,
      error: '',
      actionError: '',
      busy: false,
      requestID: 0,
      suggestionRequestID: 0,
      suggestions: {},
      linkedOverrides: {},
      brokenPosters: []
    };
  },
  computed: {
    total() {
      return this.groups.reduce((sum, group) => sum + (group?.items?.length || 0), 0);
    }
  },
  mounted() {
    this.load();
  },
  beforeUnmount() {
    // 卸载后到达的响应一律丢弃：切走再切回会重新挂载，旧响应没有落点。
    this.requestID++;
    this.suggestionRequestID++;
  },
  methods: {
    async load() {
      const requestID = ++this.requestID;
      this.loading = true;
      this.error = '';
      try {
        const groups = (await ListWatchedMovies()) || [];
        // 后发先至的响应直接丢掉（连点两次「重新读取」就会出现）。
        if (requestID !== this.requestID) return;
        this.groups = groups;
        this.brokenPosters = [];
        this.linkedOverrides = {};
        this.loadSuggestions(groups);
      } catch (err) {
        if (requestID === this.requestID) this.error = `读取观影记录失败：${err}`;
      } finally {
        if (requestID === this.requestID) this.loading = false;
      }
    },
    // 只给还没关联视频的条目要建议，一次批量查询；失败只是不显示建议。
    async loadSuggestions(groups) {
      const requestID = ++this.suggestionRequestID;
      const pending = [];
      for (const group of groups) {
        for (const movie of group?.items || []) {
          if (!this.linkedIDs(movie).length) pending.push({ movie, year: Number(group.year) || 0 });
        }
      }
      if (!pending.length) {
        this.suggestions = {};
        return;
      }
      try {
        const result = (await SuggestLibraryMatchesBatch(pending.map(item => ({ title: item.movie.title, year: item.year })))) || {};
        if (requestID !== this.suggestionRequestID) return;
        const next = {};
        for (const item of pending) {
          const list = result[libraryMatchKey(item.movie.title, item.year)];
          next[item.movie.douban_id] = Array.isArray(list) ? list.slice(0, 3) : [];
        }
        this.suggestions = next;
      } catch {
        if (requestID === this.suggestionRequestID) this.suggestions = {};
      }
    },
    suggestionsOf(movie) {
      return this.suggestions[movie.douban_id] || [];
    },
    linkedIDs(movie) {
      const override = this.linkedOverrides[movie.douban_id];
      if (override) return override;
      return Array.isArray(movie?.linked_video_ids) ? movie.linked_video_ids : [];
    },
    async linkSuggestion(movie, suggestion) {
      if (this.busy) return;
      this.busy = true;
      this.actionError = '';
      try {
        await LinkMovieToVideo(movie.douban_id, suggestion.video_id);
        this.linkedOverrides = { ...this.linkedOverrides, [movie.douban_id]: [suggestion.video_id] };
        notifySuccess('已关联片库视频，之后两边的已看状态会同步');
      } catch (err) {
        this.actionError = `关联片库视频失败：${err}`;
      } finally {
        this.busy = false;
      }
    },
    // 取消关联：逐个解除这部片关联的视频，之后两边的已看不再同步；解除后重新给出「片库中可能已有」的建议。
    async unlink(movie) {
      const ids = this.linkedIDs(movie);
      if (!ids.length || this.busy) return;
      this.busy = true;
      this.actionError = '';
      try {
        for (const id of ids) await UnlinkMovieVideo(movie.douban_id, id);
        this.linkedOverrides = { ...this.linkedOverrides, [movie.douban_id]: [] };
        notifySuccess('已取消关联，之后两边的已看状态不再同步；已有的已看标记不变');
      } catch (err) {
        this.actionError = `取消关联失败：${err}`;
        return;
      } finally {
        this.busy = false;
      }
      await this.loadSuggestions(this.groups);
    },
    async openInLibrary(movie) {
      const ids = this.linkedIDs(movie);
      if (!ids.length || this.busy) return;
      this.busy = true;
      this.actionError = '';
      try {
        const videos = (await GetVideosByIDs(ids)) || [];
        const video = videos.find(item => item && item.id) || null;
        if (!video) {
          this.actionError = '关联的片库视频已不在片库里。';
          return;
        }
        this.$emit('open-video', video);
      } catch (err) {
        this.actionError = `打开片库视频失败：${err}`;
      } finally {
        this.busy = false;
      }
    },
    // 取消已看＝撤销榜单标记；关联的视频会跟着改回未看（D-PC52 双向同步）。
    async unwatch(movie) {
      if (this.busy) return;
      const linked = this.linkedIDs(movie).length > 0;
      const confirmed = await confirmAction({
        title: '取消已看',
        message: linked
          ? `取消后「${movie.title}」会从观影记录移除，关联的片库视频也会改回未看。继续吗？`
          : `取消后「${movie.title}」会从观影记录移除。继续吗？`,
        confirmText: '取消已看'
      });
      if (!confirmed) return;
      this.busy = true;
      this.actionError = '';
      try {
        await ClearMovieChartMark(movie.douban_id);
        notifySuccess('已取消已看');
      } catch (err) {
        this.actionError = `取消已看失败：${err}`;
        return;
      } finally {
        this.busy = false;
      }
      await this.load();
    },
    // 年份 0 是「详情还没补全就标了已看」，单列一组且不猜年份（需求设计文档 §7）；
    // 直接拼 `${year} 年` 会把它显示成「0 年」。
    groupLabel(group) {
      const year = Number(group?.year || 0);
      return year > 0 ? `${year} 年` : '年份未知';
    },
    markedAtText(movie) {
      const value = movie?.marked_at;
      if (!value) return '';
      const date = new Date(value);
      return Number.isNaN(date.getTime()) ? '' : `标记于 ${date.toLocaleString('zh-CN')}`;
    },
    posterURL(movie) {
      return `/preview/douban-chart-poster/${movie.douban_id}`;
    },
    posterVisible(movie) {
      return Boolean(movie.has_poster) && !this.brokenPosters.includes(movie.douban_id);
    },
    hidePoster(movie) {
      if (!this.brokenPosters.includes(movie.douban_id)) {
        this.brokenPosters = [...this.brokenPosters, movie.douban_id];
      }
    }
  }
};
</script>

<style scoped>
.watched-movies-page { max-width: 1000px; margin: 0 auto; padding: 24px 28px 40px; }
.watched-movies-heading h2 { margin: 0 0 6px; }
.watched-movies-heading p, .watched-movies-hint, .watched-movies-note { color: var(--text-secondary); font-size: 13px; }
.watched-movies-toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; margin: 20px 0 12px; }
.watched-movies-summary { color: var(--text-secondary); font-size: 13px; }
.watched-movies-toolbar .btn-secondary { margin-left: auto; }
.watched-movies-note { margin: 0 0 14px; line-height: 1.6; }
.watched-movies-error { color: var(--danger-color); overflow-wrap: anywhere; }
.watched-movies-empty { padding: 40px 16px; text-align: center; color: var(--text-secondary); }
.watched-movies-group { margin-bottom: 22px; }
.watched-movies-group-title { margin: 0 0 10px; font-size: 14px; color: var(--text-secondary); font-weight: 600; }
.watched-movies-list { list-style: none; padding: 0; margin: 0; display: grid; gap: 10px; }
.watched-movies-item { display: flex; flex-wrap: wrap; align-items: flex-start; gap: 14px; padding: 12px 16px; border: 1px solid var(--border-color); border-radius: var(--radius-md); background: var(--surface-faint); }
/* 与榜单页同形：按高度定尺寸、宽度随原图比例走，横版剧照不会被裁成一条。 */
.watched-movies-poster { flex: 0 0 auto; align-self: flex-start; height: 108px; width: auto; max-width: 90px; border-radius: var(--radius); object-fit: contain; background: var(--surface-muted, transparent); }
.watched-movies-info { flex: 1; min-width: 0; display: grid; gap: 6px; align-content: start; }
.watched-movies-title { font-size: 15px; overflow-wrap: anywhere; }
.watched-movies-marked-at { color: var(--text-secondary); font-size: 12px; }
.watched-movies-actions { display: flex; flex-shrink: 0; flex-wrap: wrap; align-items: center; gap: 8px; }
.watched-movies-library { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; padding: 8px 10px; border: 1px dashed var(--border-color); border-radius: var(--radius-sm); font-size: 12.5px; color: var(--text-secondary); }
.watched-movies-library__label { color: var(--text-primary); font-weight: 600; }
.watched-movies-library__match { display: inline-flex; align-items: center; gap: 6px; overflow-wrap: anywhere; }

@media (max-width: 560px) {
  .watched-movies-poster { height: 84px; max-width: 70px; }
  .watched-movies-toolbar .btn-secondary { margin-left: 0; }
}
</style>
