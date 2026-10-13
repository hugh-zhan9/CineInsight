<template>
  <section class="page-content viewer-home" data-test="viewer-home">
    <!-- 观看首页（D-UI-04）：各区块并行取数、各自加载与失败；没有数据的区块不显示。 -->
    <RecapBackdrop v-if="hero.state === 'ready'" :key="hero.key" :frames="hero.frames" :fallback="hero.fallback" data-test="home-hero">
      <div class="recap-hero__body">
        <p class="recap-hero__eyebrow"><RecapIcon :name="hero.icon" /> {{ hero.eyebrow }}</p>
        <h2 class="recap-hero__title" data-test="home-hero-title">{{ hero.title }}</h2>
        <p v-if="hero.meta" class="recap-hero__meta" data-test="home-hero-meta">{{ hero.meta }}</p>
        <div class="recap-hero__actions">
          <button type="button" class="btn btn--primary" data-test="home-hero-open" @click="openVideo(hero.video)"><AppIcon name="play" /> 打开详情</button>
        </div>
      </div>
    </RecapBackdrop>
    <RecapBackdrop v-else-if="hero.state === 'pending'" size="small" data-test="home-hero-loading">
      <div class="recap-hero__body"><p class="recap-hero__meta">正在挑一部…</p></div>
    </RecapBackdrop>
    <RecapBackdrop v-else-if="hero.state === 'guide'" size="small" data-test="home-guide">
      <div class="recap-hero__body">
        <h2 class="recap-hero__title recap-hero__title--quiet">还没有可以接着看或推荐的视频</h2>
        <p class="recap-hero__meta">先添加扫描目录把视频收进片库，或者直接去片库里挑一部。</p>
        <div class="recap-hero__actions">
          <button type="button" class="btn btn--primary" data-test="home-guide-scan" @click="$emit('navigate', 'settings')">去添加扫描目录</button>
          <button type="button" class="btn btn--secondary" data-test="home-guide-library" @click="$emit('navigate', 'videos')">去片库</button>
        </div>
      </div>
    </RecapBackdrop>

    <section v-if="blockVisible(continueBlock)" class="recap-section" data-test="home-continue">
      <header class="recap-section__head"><h3><RecapIcon name="play" /> 继续观看</h3><small>从上次停下的地方接着看</small></header>
      <p v-if="continueBlock.error" class="viewer-home__error" role="alert" data-test="home-continue-error">{{ continueBlock.error }}</p>
      <div v-else-if="continueBlock.loading && !continueBlock.items.length" class="viewer-home__skeletons" aria-busy="true" data-test="home-continue-loading">
        <SkeletonBlock v-for="n in 4" :key="n" shape="card" :lines="2" />
      </div>
      <div v-if="continueBlock.items.length" class="recap-strip">
        <RecapMediaCard
          v-for="video in continueBlock.items"
          :key="video.id"
          class="recap-strip__item"
          :id="video.id"
          :title="videoTitle(video)"
          :subtitle="progressText(video)"
          @open="openVideo(video)"
        />
      </div>
    </section>

    <section v-if="blockVisible(tonightBlock)" class="recap-section" data-test="home-tonight">
      <header class="recap-section__head">
        <h3><RecapIcon name="spark" /> 今晚看什么</h3>
        <small>和随机播放同一套均衡抽取，播放越少越容易被抽到</small>
        <button type="button" class="btn btn--secondary btn--sm viewer-home__reroll" :disabled="tonightBlock.loading" data-test="home-tonight-reroll" @click="rerollTonight"><AppIcon name="refresh" /> {{ tonightBlock.loading ? '正在抽…' : '换一批' }}</button>
      </header>
      <p v-if="tonightBlock.error" class="viewer-home__error" role="alert" data-test="home-tonight-error">{{ tonightBlock.error }}</p>
      <div v-else-if="tonightBlock.loading && !tonightBlock.items.length" class="viewer-home__skeletons" aria-busy="true" data-test="home-tonight-loading">
        <SkeletonBlock v-for="n in 3" :key="n" shape="card" :lines="2" />
      </div>
      <p v-else-if="tonightBlock.notice" class="recap-muted" role="status" data-test="home-tonight-notice">{{ tonightBlock.notice }}</p>
      <div v-if="tonightBlock.items.length" class="viewer-home__tonight">
        <RecapMediaCard
          v-for="item in tonightBlock.items"
          :key="item.video.id"
          :id="item.video.id"
          :title="videoTitle(item.video)"
          :subtitle="(item.reasons || []).join(' · ')"
          data-test="home-tonight-item"
          @open="openVideo(item.video)"
        />
      </div>
    </section>

    <section v-if="blockVisible(recentBlock)" class="recap-section" data-test="home-recent">
      <header class="recap-section__head"><h3><RecapIcon name="videos" /> 最近添加</h3><small>最近 30 天收进片库的视频</small></header>
      <p v-if="recentBlock.error" class="viewer-home__error" role="alert" data-test="home-recent-error">{{ recentBlock.error }}</p>
      <div v-else-if="recentBlock.loading && !recentBlock.items.length" class="viewer-home__posters" aria-busy="true" data-test="home-recent-loading">
        <SkeletonBlock v-for="n in 7" :key="n" shape="poster" :lines="1" />
      </div>
      <div v-if="recentBlock.items.length" class="viewer-home__posters">
        <RecapMediaCard
          v-for="video in recentBlock.items"
          :key="video.id"
          variant="poster"
          :id="video.id"
          :cover="posterURL(video.id)"
          :title="videoTitle(video)"
          data-test="home-recent-item"
          @open="openVideo(video)"
        />
      </div>
    </section>

    <section v-if="blockVisible(recapBlock, topPeople)" class="recap-section" data-test="home-people">
      <header class="recap-section__head"><h3><RecapIcon name="person" /> 本月常看的人物</h3><small>按本月播放次数</small></header>
      <p v-if="recapBlock.error" class="viewer-home__error" role="alert" data-test="home-people-error">{{ recapBlock.error }}</p>
      <SkeletonBlock v-else-if="recapBlock.loading && !topPeople.length" class="viewer-home__skeleton-lines" :lines="2" data-test="home-people-loading" />
      <div v-if="topPeople.length" class="recap-people">
        <button
          v-for="item in topPeople"
          :key="item.entity.id"
          type="button"
          class="recap-person"
          :title="`${item.weight} 次播放 · ${item.videos} 部`"
          data-test="home-person"
          @click="openPerson(item.entity)"
        >
          <span class="recap-person__avatar"><img v-if="item.entity.image_url" :src="item.entity.image_url" alt="" loading="lazy" /><b v-else>{{ initial(item.entity.name) }}</b></span>
          <strong>{{ item.entity.name }}</strong><small>{{ item.weight }} 次</small>
        </button>
      </div>
    </section>

    <section v-if="blockVisible(recapBlock, highlight ? [highlight] : [])" class="recap-section" data-test="home-highlight">
      <header class="recap-section__head">
        <h3><RecapIcon name="heart" /> 本月回顾亮点</h3>
        <small v-if="highlight">{{ highlight.label }}{{ highlight.inProgress ? '（进行中）' : '' }}</small>
        <button type="button" class="btn btn--secondary btn--sm viewer-home__reroll" data-test="home-open-recap" @click="$emit('navigate', 'recap')">看完整回顾 <AppIcon name="arrow" /></button>
      </header>
      <p v-if="recapBlock.error" class="viewer-home__error" role="alert" data-test="home-highlight-error">{{ recapBlock.error }}</p>
      <SkeletonBlock v-else-if="recapBlock.loading && !highlight" class="viewer-home__skeleton-lines" :lines="3" data-test="home-highlight-loading" />
      <div v-if="highlight" class="viewer-home__highlight">
        <RecapMediaCard
          v-if="highlight.favorite"
          class="viewer-home__favorite"
          :id="highlight.favorite.video.id"
          :title="highlight.favorite.video.title || '已删除的视频'"
          :subtitle="`本月最爱 · 看了 ${highlight.favorite.count} 次`"
          :available="highlight.favorite.video.available"
          data-test="home-highlight-favorite"
          @open="openVideo(highlight.favorite.video)"
        />
        <div class="viewer-home__stats">
          <StatTile v-for="stat in highlight.stats" :key="stat.key" size="lg" :unit="stat.unit" :label="stat.label" :data-test="`home-highlight-${stat.key}`">
            <template #icon><RecapIcon :name="stat.icon" /></template>
            <template #value><RecapCountUp :value="stat.value" /></template>
          </StatTile>
        </div>
      </div>
    </section>
  </section>
</template>

<script>
// 观看首页（D-UI-04，详细设计「观看首页」）：一期只用现有接口——
//   继续观看 ListContinueWatchingWithFilter({}, '', 0, 10)；今晚看什么 SuggestTonightVideos(默认条件, 3)；
//   最近添加 SearchLibraryVideoPage 的 recently_added 智能视图（14 部，海报走 /preview/video-artwork）；
//   本月常看的人物与本月回顾亮点共用一次 GetRecap（本月）。
// 每块各自的代次，晚到的旧响应丢掉；一块失败只在那一块写一行。卡片点开发 open-video（宿主进整页详情）。
// 大图的背景：有背景图（详情里的 has_fanart）用背景图，否则用画面帧；减少动态效果由 RecapBackdrop 处理。
import {
  GetRecap, GetVideoDetails, ListContinueWatchingWithFilter, SearchLibraryVideoPage, SuggestTonightVideos
} from '../../../wailsjs/go/main/App';
import RecapBackdrop from '../recap/RecapBackdrop.vue';
import RecapCountUp from '../recap/RecapCountUp.vue';
import RecapIcon from '../recap/RecapIcon.vue';
import RecapMediaCard from '../recap/RecapMediaCard.vue';
import AppIcon from '../ui/AppIcon.vue';
import SkeletonBlock from '../ui/SkeletonBlock.vue';
import StatTile from '../ui/StatTile.vue';
import { translateBackendError } from '../../utils/feedback.js';
import { formatDuration } from '../../utils/mediaDetails.js';
import { DEFAULT_RANDOM_CONDITIONS, randomConditionFields } from '../../utils/randomPlayback.js';
import { currentCursor, heroFrameURLs, recapRequest, videoArtwork, videoThumbnail } from '../../utils/recap.js';

export const HOME_CONTINUE_LIMIT = 10;
export const HOME_TONIGHT_LIMIT = 3;
export const HOME_RECENT_LIMIT = 14;
export const HOME_PEOPLE_LIMIT = 8;

// 大图选哪一部：继续观看的第一条优先（它还在读时先等它），没有再取今晚看什么的第一条。
// 两块都读成功又都为空时给一句引导；有一块失败又挑不出时不出大图，失败写在各自区块里。
export function pickHomeHero(continueBlock, tonightBlock) {
  const resumed = continueBlock?.items?.[0];
  if (resumed) return { state: 'ready', from: 'continue', video: resumed, reasons: [] };
  if (continueBlock?.loading) return { state: 'pending' };
  const tonight = tonightBlock?.first;
  if (tonight) return { state: 'ready', from: 'tonight', video: tonight.video, reasons: tonight.reasons || [] };
  if (tonightBlock?.loading) return { state: 'pending' };
  if (continueBlock?.error || tonightBlock?.error) return { state: 'none' };
  return { state: 'guide' };
}

export function homeVideoTitle(video) {
  return String(video?.display_title || video?.title || video?.name || '未命名视频');
}

export function homeProgressText(video) {
  const position = formatDuration(video?.watch_position_seconds);
  const duration = formatDuration(video?.duration);
  if (!position) return duration ? `共 ${duration}` : '';
  return duration ? `看到 ${position} / ${duration}` : `看到 ${position}`;
}

export default {
  name: 'HomePage',
  components: { AppIcon, RecapBackdrop, RecapCountUp, RecapIcon, RecapMediaCard, SkeletonBlock, StatTile },
  // pageActive：宿主把首页切回来时为 true，这里重取会变的区块（继续观看、最近添加、本月回顾；期间可能看过片），
  // 取回之前保留现有内容。今晚看什么只在「换一批」时重抽，大图定下后不再换。
  props: { pageActive: { type: Boolean, default: true } },
  // open-video(id)：进整页详情；open-person({ id, name })：进人物页；navigate(pageKey)：回顾页、片库、设置。
  emits: ['open-video', 'navigate', 'open-person'],
  data() {
    return {
      continueBlock: { loading: true, error: '', items: [] },
      tonightBlock: { loading: true, error: '', items: [], first: null, notice: '' },
      recentBlock: { loading: true, error: '', items: [] },
      recapBlock: { loading: true, error: '', result: null },
      heroArtwork: { id: 0, hasFanart: null },
      // 第一次挑出来的大图（ready 的那一份）：之后回到首页、换一批都不换它。
      heroLock: null
    };
  },
  computed: {
    livePick() { return pickHomeHero(this.continueBlock, this.tonightBlock); },
    // 大图定下后固定是那一部；它来自继续观看且还在新读到的列表里时，用新的那条（进度是新的）。
    heroPick() {
      const lock = this.heroLock;
      if (!lock) return this.livePick;
      const fresh = lock.from === 'continue' ? this.continueBlock.items.find(item => Number(item.id) === Number(lock.video.id)) : null;
      return fresh ? { ...lock, video: fresh } : lock;
    },
    heroVideoID() { return this.heroPick.state === 'ready' ? Number(this.heroPick.video?.id || 0) : 0; },
    hero() {
      const pick = this.heroPick;
      if (pick.state !== 'ready') return pick;
      const video = pick.video;
      const id = this.heroVideoID;
      // 背景图状态还没回来时先不给帧，免得先拉一轮画面帧又换成背景图。
      const hasFanart = this.heroArtwork.id === id ? this.heroArtwork.hasFanart : null;
      const settled = hasFanart !== null;
      const resumed = pick.from === 'continue';
      return {
        ...pick,
        key: `${pick.from}-${id}`,
        icon: resumed ? 'play' : 'spark',
        eyebrow: resumed ? '继续观看' : '今晚看什么',
        title: homeVideoTitle(video),
        meta: resumed ? homeProgressText(video) : pick.reasons.join(' · '),
        frames: !settled ? [] : hasFanart ? [videoArtwork(id, 'fanart')] : heroFrameURLs(video),
        fallback: settled ? videoThumbnail(id) : ''
      };
    },
    topPeople() { return (this.recapBlock.result?.top_people || []).slice(0, HOME_PEOPLE_LIMIT); },
    // 本月没有任何记录（empty）时整块不显示。
    highlight() {
      const result = this.recapBlock.result;
      if (!result || result.empty) return null;
      const overview = result.overview || {};
      return {
        label: result.label || '本月', inProgress: Boolean(result.in_progress), favorite: result.favorite || null,
        stats: [
          { key: 'plays', icon: 'plays', label: '播放次数', unit: '次', value: Number(overview.plays || 0) },
          { key: 'videos', icon: 'videos', label: '看过的视频', unit: '部', value: Number(overview.videos || 0) },
          { key: 'days', icon: 'days', label: '活跃天数', unit: '天', value: Number(overview.active_days || 0) }
        ]
      };
    }
  },
  watch: {
    livePick(pick) { if (!this.heroLock && pick.state === 'ready') this.heroLock = pick; },
    heroVideoID(id) { this.loadHeroArtwork(id); },
    pageActive(active) { if (active) this.refresh(); }
  },
  created() {
    this._tokens = {};
  },
  mounted() {
    this.load();
  },
  beforeUnmount() {
    for (const key of Object.keys(this._tokens)) this._tokens[key]++;
  },
  methods: {
    videoTitle: homeVideoTitle,
    progressText: homeProgressText,
    posterURL(id) { return videoArtwork(id, 'poster'); },
    blockVisible(block, items = block.items) {
      return Boolean(block.loading || block.error || items?.length);
    },
    // 挂载时四个请求同时发出，互不等待。
    load() {
      return Promise.all([this.loadContinue(), this.loadTonight(), this.loadRecent(), this.loadRecap()]);
    },
    // 回到首页：只重取会变的三块，今晚看什么这一批留着（换一批才重抽）。
    refresh() {
      return Promise.all([this.loadContinue(), this.loadRecent(), this.loadRecap()]);
    },
    loadContinue() {
      return this.runBlock('continueBlock', '继续观看', async () => ({
        items: (await ListContinueWatchingWithFilter({}, '', 0, HOME_CONTINUE_LIMIT))?.videos || []
      }));
    },
    loadRecent() {
      return this.runBlock('recentBlock', '最近添加', async () => ({
        items: (await SearchLibraryVideoPage({ filter: { smart_view: 'recently_added' }, limit: HOME_RECENT_LIMIT }))?.videos || []
      }));
    },
    loadRecap() {
      return this.runBlock('recapBlock', '本月回顾', async () => ({ result: await GetRecap(recapRequest('month', currentCursor())) }));
    },
    // 一块的取数：只接受这块最新一次请求的结果；失败只写这一块的错误，已有内容留着。
    async runBlock(name, label, fetch) {
      const token = (this._tokens[name] = (this._tokens[name] || 0) + 1);
      this[name] = { ...this[name], loading: true, error: '' };
      try {
        const patch = await fetch();
        if (token === this._tokens[name]) this[name] = { ...this[name], ...patch, loading: false };
      } catch (err) {
        if (token === this._tokens[name]) this[name] = { ...this[name], loading: false, error: `读取${label}失败：${translateBackendError(err?.message || err)}` };
      }
    },
    // 默认条件（不限观看状态、不限收藏与时长、不带片库筛选）。换一批在原条件上排除当前这一批，
    // 抽不出新的就留着这一批并说明（与「今晚看什么」弹窗同一口径）。大图只认首次取到的第一条，换一批不换大图。
    loadTonight({ reroll = false } = {}) {
      const previous = this.tonightBlock.items;
      const exclude = reroll ? previous.map(item => Number(item.video.id)) : [];
      return this.runBlock('tonightBlock', '今晚看什么', async () => {
        const request = { filter: {}, ...randomConditionFields(DEFAULT_RANDOM_CONDITIONS), exclude_ids: exclude };
        const items = (await SuggestTonightVideos(request, HOME_TONIGHT_LIMIT)) || [];
        if (!reroll) return { items, first: items[0] || null, notice: '' };
        if (!items.length) return { items: previous, notice: '符合条件的候选都在这里了，没有更多可换的。' };
        return { items, notice: '' };
      });
    },
    rerollTonight() {
      if (this.tonightBlock.loading) return null;
      return this.loadTonight({ reroll: true });
    },
    // 大图的背景图状态来自详情（artwork.has_fanart）；读不到详情就当没有背景图，用画面帧。
    async loadHeroArtwork(id) {
      const token = (this._tokens.heroArtwork = (this._tokens.heroArtwork || 0) + 1);
      if (!id) return;
      this.heroArtwork = { id, hasFanart: null };
      let hasFanart = false;
      try {
        hasFanart = Boolean((await GetVideoDetails(id))?.artwork?.has_fanart);
      } catch (_err) {
        hasFanart = false;
      }
      if (token === this._tokens.heroArtwork) this.heroArtwork = { id, hasFanart };
    },
    openVideo(video) {
      if (!video?.id || video.available === false) return;
      this.$emit('open-video', Number(video.id));
    },
    openPerson(entity) {
      if (entity?.id) this.$emit('open-person', { id: Number(entity.id), name: entity.name || '' });
    },
    initial(name) {
      return String(name || '?').trim().slice(0, 1) || '?';
    }
  }
};
</script>

<style scoped>
.viewer-home {
  display: grid;
  gap: 26px;
  align-content: start;
  padding-bottom: 56px;
  background:
    radial-gradient(1100px 360px at 88% -80px, color-mix(in srgb, var(--accent-color) 13%, transparent), transparent 70%),
    radial-gradient(800px 300px at 2% -60px, color-mix(in srgb, var(--warning-color) 9%, transparent), transparent 70%);
}
.viewer-home__error { margin: 0; color: var(--danger-text); font-size: var(--fs-secondary); }
.viewer-home__skeletons { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 16px; }
.viewer-home__skeleton-lines { max-width: 520px; }
.viewer-home__reroll { margin-left: auto; align-self: center; }
.viewer-home__tonight { display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 16px; }
.viewer-home__posters { display: grid; grid-template-columns: repeat(auto-fill, minmax(132px, 1fr)); gap: 16px 14px; }
.viewer-home__highlight { display: grid; grid-template-columns: minmax(220px, 300px) minmax(0, 1fr); gap: 16px; align-items: start; }
.viewer-home__stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 14px; align-content: start; }
@media (max-width: 900px) {
  .viewer-home__highlight { grid-template-columns: minmax(0, 1fr); }
}
</style>
