<template>
  <section class="page-content watched-movies-page">
    <header class="watched-movies-heading">
      <h2>已看</h2>
      <p>你在年度榜单上标过「已看」的电影，按影片上映年份从新到旧分组。</p>
    </header>

    <div class="watched-movies-toolbar">
      <span class="watched-movies-summary" data-test="watched-movies-summary">共 {{ total }} 部</span>
      <button class="btn-secondary btn-compact" type="button" :disabled="loading"
        data-test="watched-movies-reload" @click="load">重新读取</button>
    </div>

    <!-- 这一页**只有**榜单标记，不掺主片库的播放状态（D-MC11 的「不合并 videos.is_watched」）。
         两者是不同的事实：「我在榜单上确认自己看过这部片」与「这个文件被播放过」。
         后端也没有给出能把两者混起来的绑定，所以真正的风险不是谁去混，而是后来的人
         以为它们本来就是混的——这句话和 WatchedMoviesPage.test.js 里那条断言一起，
         把「渲染的就是 ListWatchedMovies 的返回值，一条不多」钉住。 -->
    <p class="watched-movies-note" data-test="watched-movies-scope-note">
      这里的「已看」是你在榜单上亲手标记的，与主片库里视频文件的播放状态是两回事：两边互不影响，也不合并显示。
    </p>

    <p v-if="error" class="watched-movies-error" role="alert" data-test="watched-movies-error">{{ error }}</p>

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
          </div>
        </li>
      </ul>
    </div>
  </section>
</template>

<script>
// 本页只调这一个绑定。多导入一个榜单侧的读接口，就等于把「缓存清空后仍可用」
// 这条（D-MC05 快照列存在的全部理由）交还给缓存表。
import { ListWatchedMovies } from '../../wailsjs/go/main/App';

export default {
  name: 'WatchedMoviesPage',
  data() {
    return {
      groups: [],
      loading: false,
      error: '',
      requestID: 0,
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
      } catch (err) {
        if (requestID === this.requestID) this.error = `读取已看影片失败：${err}`;
      } finally {
        if (requestID === this.requestID) this.loading = false;
      }
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

@media (max-width: 560px) {
  .watched-movies-poster { height: 84px; max-width: 70px; }
  .watched-movies-toolbar .btn-secondary { margin-left: 0; }
}
</style>
