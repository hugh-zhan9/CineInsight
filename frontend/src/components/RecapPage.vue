<template>
  <main class="page-content recap-page" data-test="recap-page">
    <PageHeader class="recap-head" title="观看回顾" subtitle="只统计真实的播放与看图记录，不推算观看时长">
      <template #actions>
        <div class="recap-head__controls">
          <!-- 回顾范围（D-UD-06 SegmentedControl），每项带 recap-tab-* 钩子。 -->
          <SegmentedControl label="回顾范围" :options="tabOptions" :model-value="tab" @update:model-value="setTab" />
          <div v-if="tab !== 'live'" class="recap-period">
            <button type="button" class="btn btn--secondary btn--sm" :disabled="!canBack" data-test="recap-prev" @click="shift(-1)"><AppIcon name="chevron-left" /> 上一{{ unit }}</button>
            <strong class="recap-period__label" data-test="recap-period-label">{{ periodLabel }}</strong>
            <span v-if="result && result.in_progress" class="recap-pill" data-test="recap-in-progress">进行中</span>
            <button type="button" class="btn btn--secondary btn--sm" :disabled="!canForward" data-test="recap-next" @click="shift(1)">下一{{ unit }} <AppIcon name="chevron-right" /></button>
          </div>
          <div v-else class="recap-period">
            <span class="recap-live-dot" aria-hidden="true"></span>
            <strong class="recap-period__label">今天</strong>
            <small v-if="result" class="recap-period__note">更新于 {{ formatClock(result.generated_at) }}</small>
          </div>
        </div>
      </template>
    </PageHeader>

    <AppBanner v-if="error" tone="danger" :title="error" data-test="recap-error">
      <template #actions><button type="button" class="btn btn--secondary btn--sm" data-test="recap-retry" @click="load()">重试</button></template>
    </AppBanner>
    <div v-if="loading && !result" class="recap-skeleton" data-test="recap-loading" aria-busy="true">
      <RecapBackdrop size="medium"><div class="recap-hero__body"><p class="recap-hero__meta">正在读取回顾…</p></div></RecapBackdrop>
      <div class="recap-skeleton__row"><SkeletonBlock v-for="n in 4" :key="n" class="recap-skeleton__tile" :lines="3" /></div>
    </div>

    <template v-if="result && result.period === 'live'">
      <RecapBackdrop v-if="liveHero" :key="`live-${liveHero.video.id}`" size="medium" :frames="framesFor(liveHero.video)" :fallback="videoThumbnail(liveHero.video.id)" data-test="recap-live-hero">
        <!-- 大图区的文字层是 HeroHeader（D-UD-06）；页面标题在上面的 PageHeader，这里用 h3。 -->
        <div class="recap-hero__body">
          <HeroHeader :title="liveHero.video.title || '已删除的视频'" :meta="liveHero.meta" :heading-level="3">
            <template #eyebrow><RecapIcon name="live" /> {{ liveHero.eyebrow }}</template>
            <template #actions>
              <button type="button" class="btn btn--primary" :disabled="!liveHero.video.available" data-test="recap-live-open" @click="openVideo(liveHero.video)"><AppIcon name="play" /> 打开详情</button>
            </template>
          </HeroHeader>
        </div>
      </RecapBackdrop>
      <RecapBackdrop v-else size="small" data-test="recap-live-idle">
        <div class="recap-hero__body">
          <HeroHeader quiet :title="result.empty ? '今天还没有观看记录' : '今天的记录都在下面'" meta="播放一部片或在图片页看图，这里会即时更新。" :heading-level="3">
            <template #eyebrow><RecapIcon name="live" /> 实时</template>
          </HeroHeader>
        </div>
      </RecapBackdrop>

      <section class="recap-stats" data-test="recap-stats">
        <StatTile v-for="(stat, index) in stats" :key="stat.key" v-reveal="index" size="lg" :unit="stat.unit" :label="stat.label" :note="stat.change ? '' : stat.note">
          <template #icon><RecapIcon :name="stat.icon" /></template>
          <template #value><RecapCountUp :value="stat.value" /></template>
          <span v-if="stat.change" :class="['recap-stat__delta', `is-${stat.tone}`]">{{ stat.change }} <small>较{{ result.overview.previous.label }}</small></span>
        </StatTile>
      </section>

      <div class="recap-split">
        <section v-reveal class="recap-panel">
          <header class="recap-section__head"><h3><RecapIcon name="live" /> 今日流水</h3><small>最近 {{ result.feed.length }} 条</small></header>
          <RecapLiveFeed v-if="result.feed.length" :items="result.feed" :now="nowTick" @open="openFeedItem" />
          <p v-else class="recap-muted">今天还没有记录。</p>
        </section>
        <section v-reveal="1" class="recap-panel">
          <header class="recap-section__head"><h3><RecapIcon name="days" /> 近 7 天</h3><small>播放 / 看图</small></header>
          <div class="recap-bars recap-bars--week" data-test="recap-week">
            <div v-for="day in result.week" :key="day.date" class="recap-bars__col" :title="`${formatDateLabel(day.date)} · 播放 ${day.plays} · 看图 ${day.views}`">
              <span class="recap-bars__stack">
                <i class="recap-bars__views" :style="{ height: barHeight(day.views, weekMax) }"></i>
                <i class="recap-bars__plays" :style="{ height: barHeight(day.plays, weekMax) }"></i>
              </span>
              <small>{{ weekdayLabel(day.date) }}</small>
            </div>
          </div>
          <RecapClock v-if="!result.empty" class="recap-live-clock" :hours="result.habits.hours" :peak-hour="result.habits.peak_hour" />
        </section>
      </div>
    </template>

    <template v-else-if="result">
      <EmptyState v-if="result.empty" class="recap-empty" icon="spark" :title="`${result.label}还没有观看记录`" :message="emptyMessage" data-test="recap-empty">
        <template v-if="emptyHasPrevious && canBack" #actions>
          <button type="button" class="btn btn--secondary btn--sm" data-test="recap-empty-previous" @click="shift(-1)"><AppIcon name="chevron-left" /> 看{{ result.overview.previous.label }}</button>
        </template>
      </EmptyState>

      <template v-else>
        <RecapBackdrop v-if="hero" :key="hero.key" :frames="hero.frames" :fallback="hero.fallback" data-test="recap-hero">
          <div class="recap-hero__layout">
            <HeroHeader class="recap-hero__body" :title="hero.title" :meta="hero.meta" :heading-level="3" title-data-test="recap-hero-title">
              <template #eyebrow><RecapIcon name="heart" /> {{ result.label }}{{ result.in_progress ? '（进行中）' : '' }} · {{ hero.eyebrow }}</template>
              <!-- 这一期的标签、人物与作品集（D-UD-06 TagPill，前面是颜色圆点 / 头像 / 图标），点了去对应的结果。 -->
              <div v-if="hero.tags.length || hero.people.length || hero.collections.length" class="recap-hero__chips">
                <TagPill v-for="tag in hero.tags" :key="`t${tag.id}`" clickable class="recap-chip" :label="tag.name" :style="{ '--chip-color': tag.color || 'var(--accent-color)' }" data-test="recap-hero-tag" @click="openTag(tag)">
                  <template #leading><i class="recap-chip__dot"></i></template>
                </TagPill>
                <TagPill v-for="person in hero.people" :key="`p${person.id}`" clickable class="recap-chip recap-chip--person" :label="person.name" @click="openEntity('person', person)">
                  <template #leading><img v-if="person.image_url" :src="person.image_url" alt="" /><RecapIcon v-else name="person" /></template>
                </TagPill>
                <TagPill v-for="item in hero.collections" :key="`c${item.id}`" clickable class="recap-chip" :label="item.name" @click="openEntity('collection', item)">
                  <template #leading><RecapIcon name="collection" /></template>
                </TagPill>
              </div>
              <template #actions>
                <button type="button" class="btn btn--primary" :disabled="!hero.available" data-test="recap-hero-open" @click="hero.open()"><AppIcon name="play" /> {{ hero.action }}</button>
                <span v-if="!hero.available" class="recap-hero__note">原文件已不在库中，记录仍然保留</span>
              </template>
            </HeroHeader>
            <div class="recap-hero__count" aria-hidden="true"><RecapCountUp :value="hero.count" /><small>次</small></div>
          </div>
        </RecapBackdrop>

        <section class="recap-stats" data-test="recap-stats">
          <StatTile v-for="(stat, index) in stats" :key="stat.key" v-reveal="index" size="lg" :unit="stat.unit" :label="stat.label" :note="stat.note">
            <template #icon><RecapIcon :name="stat.icon" /></template>
            <template #value><RecapCountUp :value="stat.value" /></template>
            <span v-if="stat.change" :class="['recap-stat__delta', `is-${stat.tone}`]">{{ stat.change }} <small>较{{ result.overview.previous.label }}</small></span>
          </StatTile>
        </section>

        <section v-if="result.titles.length" v-reveal class="recap-section">
          <header class="recap-section__head"><h3><RecapIcon name="spark" /> 本期称号</h3><small>按固定规则从记录里算出来，每个都附依据</small></header>
          <div class="recap-titles">
            <article v-for="(title, index) in result.titles" :key="title.key" v-reveal="index + 1" :class="['recap-title', `recap-title--${index}`]" data-test="recap-title">
              <span class="recap-title__icon"><RecapIcon :name="title.key" /></span>
              <div class="recap-title__text"><strong>{{ title.name }}</strong><p>{{ title.detail }}</p></div>
            </article>
          </div>
        </section>

        <section v-if="result.period === 'year' && result.month_best.length" v-reveal class="recap-section">
          <header class="recap-section__head"><h3><RecapIcon name="videos" /> 每月之最</h3><small>每个月播放最多的那一部</small></header>
          <RecapMonthStrip :months="result.month_best" @open="openVideo" />
        </section>

        <section v-if="result.favorite && result.favorite.related.length" v-reveal class="recap-section">
          <header class="recap-section__head"><h3><RecapIcon name="arrow" /> 相关视频还有</h3><small>与《{{ result.favorite.video.title }}》共享标签、人物或作品集，本期还没看过</small></header>
          <div class="recap-strip" data-test="recap-related">
            <RecapMediaCard
              v-for="item in result.favorite.related"
              :key="item.video.id"
              class="recap-strip__item"
              :id="item.video.id"
              :title="item.video.title"
              :subtitle="relatedSubtitle(item)"
              @open="openVideo(item.video)"
            />
          </div>
        </section>

        <div v-if="result.top_videos.length" class="recap-split recap-split--wide">
          <section v-reveal class="recap-panel">
            <header class="recap-section__head"><h3><RecapIcon name="videos" /> 本期 Top {{ result.top_videos.length }}</h3><small>按播放次数，并列看最近一次</small></header>
            <div class="recap-posters" data-test="recap-top-videos">
              <RecapMediaCard
                v-for="(item, index) in result.top_videos"
                :key="item.video.id"
                :class="['recap-posters__item', { 'recap-posters__lead': index === 0 }]"
                :id="item.video.id"
                :rank="index + 1"
                :badge="`${item.count} 次`"
                :title="item.video.title || '已删除的视频'"
                :available="item.video.available"
                @open="openVideo(item.video)"
              />
            </div>
          </section>
          <aside class="recap-side">
            <section v-if="result.top_tags.length" v-reveal="1" class="recap-panel">
              <header class="recap-section__head"><h3><RecapIcon name="tag_loyal" /> 标签</h3><small>按播放加权</small></header>
              <div class="recap-cloud" data-test="recap-tag-cloud">
                <button
                  v-for="item in result.top_tags"
                  :key="item.tag.id"
                  type="button"
                  class="btn-plain recap-cloud__tag"
                  :style="{ '--chip-color': item.tag.color || 'var(--accent-color)', fontSize: `${tagCloudScale(item.weight, tagMax)}rem` }"
                  :title="`${item.tag.name} · ${item.weight} 次播放 · ${item.videos} 部`"
                  data-test="recap-tag"
                  @click="openTag(item.tag)"
                >{{ item.tag.name }}<small>{{ item.weight }}</small></button>
              </div>
            </section>
            <section v-if="result.top_people.length || result.top_collections.length" v-reveal="2" class="recap-panel">
              <header class="recap-section__head"><h3><RecapIcon name="person" /> 人物与作品集</h3></header>
              <div class="recap-people">
                <button v-for="item in result.top_people" :key="`p${item.entity.id}`" type="button" class="btn-plain recap-person" :title="`${item.weight} 次播放 · ${item.videos} 部`" @click="openEntity('person', item.entity)">
                  <span class="recap-person__avatar"><img v-if="item.entity.image_url" :src="item.entity.image_url" alt="" loading="lazy" /><b v-else>{{ initial(item.entity.name) }}</b></span>
                  <strong>{{ item.entity.name }}</strong><small>{{ item.weight }} 次</small>
                </button>
              </div>
              <ul v-if="result.top_collections.length" class="recap-collections">
                <li v-for="item in result.top_collections" :key="`c${item.entity.id}`">
                  <button type="button" class="btn-plain recap-collection" @click="openEntity('collection', item.entity)">
                    <span class="recap-collection__cover"><img v-if="item.entity.image_url" :src="item.entity.image_url" alt="" loading="lazy" /><RecapIcon v-else name="collection" /></span>
                    <strong>{{ item.entity.name }}</strong><small>{{ item.weight }} 次 · {{ item.videos }} 部</small>
                  </button>
                </li>
              </ul>
            </section>
          </aside>
        </div>

        <section v-if="result.top_images.length" v-reveal class="recap-section">
          <header class="recap-section__head"><h3><RecapIcon name="images" /> 最常看的图片</h3><small>{{ imageHeadline }}</small></header>
          <div class="recap-mosaic" data-test="recap-top-images">
            <RecapMediaCard
              v-for="item in result.top_images"
              :key="item.image.id"
              class="recap-mosaic__item"
              kind="image"
              variant="tile"
              :id="item.image.id"
              :width="item.image.width"
              :height="item.image.height"
              :badge="`${item.count} 次`"
              :title="item.image.name"
              :available="item.image.available"
              @open="openImage(item.image)"
            />
          </div>
          <template v-if="result.favorite_image && result.favorite_image.related.length">
            <header class="recap-section__head recap-section__head--sub"><h3>相似的图片还有</h3><small>与「{{ result.favorite_image.image.name }}」共享标签或人物，本期还没看过</small></header>
            <div class="recap-strip recap-strip--images">
              <RecapMediaCard v-for="item in result.favorite_image.related" :key="item.image.id" class="recap-strip__item" kind="image" :id="item.image.id" :title="item.image.name" :subtitle="`共享 ${item.shared} 项`" @open="openImage(item.image)" />
            </div>
          </template>
        </section>

        <section v-reveal class="recap-section">
          <header class="recap-section__head"><h3><RecapIcon name="clock" /> 观看习惯</h3><small>播放与看图各算一条记录</small></header>
          <div class="recap-habits">
            <div class="recap-panel recap-habits__clock"><RecapClock :hours="result.habits.hours" :peak-hour="result.habits.peak_hour" /></div>
            <div class="recap-panel recap-habits__calendar">
              <RecapCalendar :days="result.habits.days" :mode="result.period === 'year' ? 'year' : 'month'" :today="todayISO" />
              <p class="recap-muted">活跃 {{ result.overview.active_days }} 天 · 最长连续 {{ result.overview.longest_streak }} 天</p>
            </div>
            <div class="recap-facts" data-test="recap-habit-facts">
              <article v-for="fact in habitFacts" :key="fact.key" class="recap-fact"><small>{{ fact.label }}</small><strong>{{ fact.value }}</strong><span v-if="fact.note">{{ fact.note }}</span></article>
              <div class="recap-sources" :title="sourcesTitle">
                <span v-for="item in sourceBars" :key="item.key" :class="['recap-sources__bar', `is-${item.kind}`]" :style="{ flexGrow: item.count }"></span>
              </div>
              <ul class="recap-sources__legend">
                <li v-for="item in sourceBars" :key="item.key"><i :class="`is-${item.kind}`"></i>{{ item.label }} {{ item.percent }}</li>
              </ul>
            </div>
          </div>
        </section>

        <section v-reveal class="recap-section">
          <header class="recap-section__head"><h3><RecapIcon name="explorer" /> 新鲜度</h3><small>新发现 = 第一次出现播放记录；历史补记不会让一部片成为新发现</small></header>
          <div class="recap-fresh" data-test="recap-freshness">
            <article class="recap-fact recap-fact--accent"><small>新发现</small><strong><RecapCountUp :value="result.freshness.new_videos" /> 部</strong></article>
            <article class="recap-fact"><small>重温</small><strong><RecapCountUp :value="result.freshness.rewatched_videos" /> 部</strong></article>
            <article class="recap-fact"><small>新收藏</small><strong>{{ result.freshness.new_favorite_videos + result.freshness.new_favorite_images }}</strong><span>视频 {{ result.freshness.new_favorite_videos }} · 图片 {{ result.freshness.new_favorite_images }}</span></article>
            <article class="recap-fact"><small>观影日记评分</small><strong>{{ result.freshness.diary_rated }} 条</strong><span>片库评分没有时间，不归入某一期</span></article>
          </div>
          <div v-if="result.freshness.new_samples.length" class="recap-strip recap-strip--compact">
            <RecapMediaCard v-for="video in result.freshness.new_samples" :key="video.id" class="recap-strip__item" :id="video.id" :title="video.title || '已删除的视频'" subtitle="本期新发现" :available="video.available" @open="openVideo(video)" />
          </div>
        </section>

        <template v-if="result.period === 'year'">
          <div class="recap-split">
            <section v-reveal class="recap-panel">
              <header class="recap-section__head"><h3><RecapIcon name="days" /> 12 个月</h3><small>播放 / 看图</small></header>
              <div class="recap-bars recap-bars--months" data-test="recap-months">
                <div v-for="item in result.months" :key="item.month" class="recap-bars__col" :title="`${item.month}月 · 播放 ${item.plays} · 看图 ${item.views}`">
                  <span class="recap-bars__stack">
                    <i class="recap-bars__views" :style="{ height: barHeight(item.views, monthMax) }"></i>
                    <i class="recap-bars__plays" :style="{ height: barHeight(item.plays, monthMax) }"></i>
                  </span>
                  <small>{{ item.month }}月</small>
                </div>
              </div>
            </section>
            <section v-reveal="1" class="recap-panel">
              <header class="recap-section__head"><h3><RecapIcon name="tag_loyal" /> 标签演变</h3><small>每季 Top 3</small></header>
              <ol class="recap-quarters">
                <li v-for="quarter in result.quarter_tags" :key="quarter.quarter">
                  <strong>Q{{ quarter.quarter }}</strong>
                  <span v-if="!quarter.tags.length" class="recap-muted">—</span>
                  <TagPill v-for="item in quarter.tags" :key="item.tag.id" clickable :label="item.tag.name" @click="openTag(item.tag)" />
                </li>
              </ol>
            </section>
          </div>
          <p class="recap-diary-link">
            <button type="button" class="btn btn--secondary btn--sm" data-test="recap-open-diary" @click="$emit('navigate', 'viewing-notes')"><AppIcon name="diary" /> 查看观影日记</button>
            <span>观看笔记里的「年度回顾」按日记条目统计，与这里的播放流水口径不同，两者各自保留。</span>
          </p>
        </template>
      </template>
    </template>
  </main>
</template>

<script>
// 观看回顾页（D-RECAP-UI）：实时 / 月度 / 年度三个分段，月度与年度可前后翻期。数据全部来自 GetRecap
// 的只读事实；请求带代次，晚到的旧响应直接丢弃。实时分段在页面可见时随 recap-activity 刷新，另每 60 秒兜底。
import { GetRecap } from '../../wailsjs/go/main/App';
import { runtimeEventsMixin } from './video-list/runtimeEvents.js';
import RecapBackdrop from './recap/RecapBackdrop.vue';
import RecapCalendar from './recap/RecapCalendar.vue';
import RecapClock from './recap/RecapClock.vue';
import RecapCountUp from './recap/RecapCountUp.vue';
import RecapIcon from './recap/RecapIcon.vue';
import RecapLiveFeed from './recap/RecapLiveFeed.vue';
import RecapMediaCard from './recap/RecapMediaCard.vue';
import RecapMonthStrip from './recap/RecapMonthStrip.vue';
import AppBanner from './ui/AppBanner.vue';
import AppIcon from './ui/AppIcon.vue';
import EmptyState from './ui/EmptyState.vue';
import HeroHeader from './ui/HeroHeader.vue';
import PageHeader from './ui/PageHeader.vue';
import SegmentedControl from './ui/SegmentedControl.vue';
import SkeletonBlock from './ui/SkeletonBlock.vue';
import StatTile from './ui/StatTile.vue';
import TagPill from './ui/TagPill.vue';
import { reveal } from './recap/reveal.js';
import {
  RECAP_TABS, SLOT_LABELS, SLOT_RANGES, canShiftBack, canShiftForward, changeText, changeTone, currentCursor, formatClock, formatDateLabel,
  formatDay, heroFrameURLs, imageThumbnail, percentText, recapRequest, relativeTime, requestKey, shiftCursor, sourceLabel,
  tagCloudScale, videoThumbnail
} from '../utils/recap.js';

export const RECAP_POLL_MS = 60000;
// 一次播放常伴随连续几条事件（启动、有效观看），合并成一次刷新。
export const RECAP_ACTIVITY_DEBOUNCE_MS = 600;

export default {
  name: 'RecapPage',
  mixins: [runtimeEventsMixin],
  components: {
    AppBanner, AppIcon, EmptyState, HeroHeader, PageHeader, RecapBackdrop, RecapCalendar, RecapClock, RecapCountUp, RecapIcon,
    RecapLiveFeed, RecapMediaCard, RecapMonthStrip, SegmentedControl, SkeletonBlock, StatTile, TagPill
  },
  directives: { reveal },
  props: { pageActive: { type: Boolean, default: true } },
  emits: ['open-video-id', 'open-tag', 'open-image', 'open-entity', 'navigate'],
  data() {
    return { tabs: RECAP_TABS, tab: 'month', cursor: currentCursor(), result: null, resultKey: '', loading: false, error: '', generation: 0, nowTick: Date.now() };
  },
  computed: {
    tabOptions() { return this.tabs.map(item => ({ value: item.key, label: item.label, dataTest: `recap-tab-${item.key}` })); },
    request() { return recapRequest(this.tab, this.cursor); },
    key() { return requestKey(this.request); },
    unit() { return this.tab === 'year' ? '年' : '月'; },
    canBack() { return canShiftBack(this.tab, this.cursor); },
    canForward() { return canShiftForward(this.tab, this.cursor, new Date(this.nowTick)); },
    periodLabel() { return this.tab === 'year' ? `${this.cursor.year}年` : `${this.cursor.year}年${this.cursor.month}月`; },
    todayISO() {
      const now = new Date(this.nowTick);
      return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`;
    },
    tagMax() { return Math.max(0, ...(this.result?.top_tags || []).map(item => Number(item.weight || 0))); },
    weekMax() { return Math.max(0, ...(this.result?.week || []).map(day => Number(day.plays || 0) + Number(day.views || 0))); },
    monthMax() { return Math.max(0, ...(this.result?.months || []).map(item => Number(item.plays || 0) + Number(item.views || 0))); },
    stats() {
      const overview = this.result?.overview;
      if (!overview) return [];
      const previous = overview.previous || {};
      const stat = (key, icon, label, unit, value, change, note) => ({ key, icon, label, unit, value: Number(value || 0), change: changeText(change), tone: changeTone(change), note });
      const list = [
        stat('plays', 'plays', '播放次数', '次', overview.plays, previous.plays_change, overview.legacy_plays ? `含历史补记 ${overview.legacy_plays} 次` : ''),
        stat('videos', 'videos', '看过的视频', '部', overview.videos, previous.videos_change, ''),
        stat('views', 'images', '看图次数', '次', overview.image_views, previous.image_views_change, overview.images ? `共 ${overview.images} 张` : '')
      ];
      if (this.result.period !== 'live') {
        list.push(stat('days', 'days', '活跃天数', '天', overview.active_days, null, ''), stat('streak', 'streak', '最长连续', '天', overview.longest_streak, null, ''));
      }
      return list;
    },
    // 本期最爱：有视频时用视频的真实画面帧做背景，只有看图记录时退回最常看的那张图。
    hero() {
      const favorite = this.result?.favorite;
      if (favorite) {
        const video = favorite.video;
        return {
          key: `v${video.id}-${this.resultKey}`, eyebrow: '你最爱看的是', title: `《${video.title || '已删除的视频'}》`,
          meta: `看了 ${favorite.count} 次 · 最近一次 ${formatDay(favorite.last_at)} ${formatClock(favorite.last_at)}`,
          frames: video.available ? heroFrameURLs(video) : [], fallback: videoThumbnail(video.id), count: Number(favorite.count || 0),
          tags: favorite.tags || [], people: favorite.people || [], collections: favorite.collections || [],
          available: Boolean(video.available), action: '打开详情', open: () => this.openVideo(video)
        };
      }
      const image = this.result?.favorite_image;
      if (!image) return null;
      return {
        key: `i${image.image.id}-${this.resultKey}`, eyebrow: '你最常看的图片是', title: image.image.name,
        meta: `看了 ${image.count} 次 · 最近一次 ${formatDay(image.last_at)} ${formatClock(image.last_at)}`,
        frames: [imageThumbnail(image.image.id)], fallback: '', count: Number(image.count || 0),
        tags: image.tags || [], people: image.people || [], collections: [],
        available: Boolean(image.image.available), action: '在图片页查看', open: () => this.openImage(image.image)
      };
    },
    liveHero() {
      const now = this.result?.now_playing;
      if (now) {
        const queue = now.basis === 'queue';
        return { video: now.video, eyebrow: queue ? '待播队列正在播放' : '刚刚在看', meta: queue ? '来自待播队列的当前项' : `${relativeTime(now.at, this.nowTick)} · ${sourceLabel('video', now.source)}` };
      }
      const top = this.result?.top_videos?.[0];
      return top ? { video: top.video, eyebrow: '今天看得最多', meta: `今天 ${top.count} 次 · 最近 ${formatClock(top.last_at)}` } : null;
    },
    imageHeadline() {
      const image = this.result?.favorite_image;
      return image ? `「${image.image.name}」看了 ${image.count} 次，排在第一` : '';
    },
    habitFacts() {
      const habits = this.result?.habits;
      if (!habits) return [];
      const busiest = habits.busiest_day;
      return [
        { key: 'busiest', label: '最忙的一天', value: busiest ? formatDateLabel(busiest.date) : '—', note: busiest ? `播放 ${busiest.plays} · 看图 ${busiest.views}` : '' },
        { key: 'slot', label: '最常看的时段', value: SLOT_LABELS[habits.peak_slot] || '—', note: habits.peak_slot ? `${SLOT_RANGES[habits.peak_slot]} · 高峰 ${habits.peak_hour} 点` : '' },
        { key: 'weekend', label: '周末占比', value: percentText(habits.weekend_share), note: `${habits.weekend_count} / ${habits.total} 条` },
        { key: 'night', label: '深夜占比（0–5 点）', value: percentText(habits.late_night_share), note: `${habits.late_night_count} / ${habits.total} 条` }
      ];
    },
    sourceBars() {
      const habits = this.result?.habits;
      const total = Number(habits?.total || 0);
      return (habits?.sources || []).map(item => ({
        key: `${item.kind}:${item.source}`, kind: item.kind, count: Number(item.count || 0),
        label: sourceLabel(item.kind, item.source), percent: percentText(total ? item.count / total : 0)
      }));
    },
    sourcesTitle() { return this.sourceBars.map(item => `${item.label} ${item.count}`).join(' · '); },
    // 空期（EmptyState）：说明回顾统计什么，再按情况给一句下一步——还没结束就等等，上一期有记录就翻过去。
    emptyHasPrevious() {
      const previous = this.result?.overview?.previous;
      return Boolean(!this.result?.in_progress && (previous?.plays || previous?.image_views));
    },
    emptyMessage() {
      const base = '回顾只统计真实发生的播放与看图：在片库播放、在应用内观看、手机端或 Jellyfin 观看，以及在图片页看图都会记进来。';
      const previous = this.result?.overview?.previous || {};
      if (this.result?.in_progress) return `${base}这一期还没结束，看点什么再回来。`;
      if (this.emptyHasPrevious) return `${base}${previous.label}有 ${previous.plays} 次播放、${previous.image_views} 次看图，可以翻过去看看。`;
      return base;
    }
  },
  watch: {
    key() { this.load(); },
    pageActive(active) {
      this.syncPolling();
      // 回到本页时静默刷新一次：期间可能播放过、看过图。
      if (active) this.load({ silent: this.resultKey === this.key });
    }
  },
  mounted() {
    this.load();
    this.registerRuntimeEvent('recap-activity', () => this.onActivity());
    this._onVisibility = () => {
      this.syncPolling();
      if (this.tab === 'live' && this.visible()) this.load({ silent: true });
    };
    document.addEventListener('visibilitychange', this._onVisibility);
    this.syncPolling();
  },
  beforeUnmount() {
    this.generation++;
    clearInterval(this._poll);
    clearTimeout(this._activityTimer);
    document.removeEventListener('visibilitychange', this._onVisibility);
  },
  methods: {
    formatClock,
    formatDateLabel,
    tagCloudScale,
    videoThumbnail,
    visible() {
      return this.pageActive && document.visibilityState !== 'hidden';
    },
    // 60 秒兜底刷新只在实时分段、且页面真正可见时运行。
    syncPolling() {
      const wanted = this.tab === 'live' && this.visible();
      if (wanted && !this._poll) this._poll = setInterval(() => this.pollTick(), RECAP_POLL_MS);
      if (!wanted && this._poll) {
        clearInterval(this._poll);
        this._poll = null;
      }
    },
    pollTick() {
      this.nowTick = Date.now();
      if (this.tab === 'live' && this.visible()) this.load({ silent: true });
    },
    onActivity() {
      if (this.tab !== 'live' || !this.visible()) return;
      clearTimeout(this._activityTimer);
      this._activityTimer = setTimeout(() => this.load({ silent: true }), RECAP_ACTIVITY_DEBOUNCE_MS);
    },
    setTab(key) {
      if (this.tab === key) return;
      this.tab = key;
      this.syncPolling();
    },
    shift(delta) {
      if (delta < 0 ? !this.canBack : !this.canForward) return;
      this.cursor = shiftCursor(this.tab, this.cursor, delta);
    },
    // 换期次时先清掉旧结果（显示骨架），静默刷新保留当前内容；只接受最新一次请求的响应。
    async load({ silent = false } = {}) {
      const generation = ++this.generation;
      const request = { ...this.request };
      const key = requestKey(request);
      if (this.resultKey !== key) {
        this.result = null;
        this.resultKey = '';
      }
      if (!silent || !this.result) this.loading = true;
      this.error = '';
      try {
        const result = await GetRecap(request);
        if (generation !== this.generation) return;
        this.result = result;
        this.resultKey = key;
        this.nowTick = Date.now();
      } catch (err) {
        if (generation === this.generation) this.error = `读取回顾失败：${err?.message || err}`;
      } finally {
        if (generation === this.generation) this.loading = false;
      }
    },
    openVideo(video) {
      if (!video?.id || video.available === false) return;
      this.$emit('open-video-id', Number(video.id));
    },
    openImage(image) {
      if (!image?.id || image.available === false) return;
      this.$emit('open-image', { id: Number(image.id), name: image.name || image.title || '' });
    },
    openTag(tag) {
      if (tag?.id) this.$emit('open-tag', { id: Number(tag.id), name: tag.name });
    },
    openEntity(type, entity) {
      if (entity?.id) this.$emit('open-entity', { type, id: Number(entity.id), name: entity.name || '' });
    },
    openFeedItem(item) {
      if (item.kind === 'image') this.openImage({ id: item.id, name: item.title, available: item.available });
      else this.openVideo({ id: item.id, available: item.available });
    },
    framesFor(video) {
      return video?.available ? heroFrameURLs(video) : [];
    },
    relatedSubtitle(item) {
      const rating = item.video.personal_rating;
      return `共享 ${item.shared} 项${rating === null || rating === undefined ? '' : ` · ${Number(rating).toFixed(1)} 分`}`;
    },
    barHeight(value, max) {
      return max > 0 ? `${Math.max(Number(value || 0) > 0 ? 4 : 0, (Number(value || 0) / max) * 100)}%` : '0%';
    },
    weekdayLabel(date) {
      const [year, month, day] = String(date).split('-').map(Number);
      if (date === this.todayISO) return '今天';
      return ['日', '一', '二', '三', '四', '五', '六'][new Date(year, month - 1, day).getDay()];
    },
    initial(name) {
      return String(name || '?').trim().slice(0, 1) || '?';
    }
  }
};
</script>

<style scoped>
.recap-page {
  display: grid;
  gap: 22px;
  align-content: start;
  padding-bottom: 56px;
  background:
    radial-gradient(1100px 360px at 88% -80px, color-mix(in srgb, var(--accent-color) 13%, transparent), transparent 70%),
    radial-gradient(800px 300px at 2% -60px, color-mix(in srgb, var(--warning-color) 9%, transparent), transparent 70%);
}
/* 页头是 PageHeader：页面本身是 grid + gap，不要它自带的下外边距；动作区放范围切换与翻期。 */
.recap-head { margin-bottom: 0; }
.recap-head__controls { display: flex; flex-wrap: wrap; align-items: center; gap: 12px 20px; }
/* 外观照 SegmentedControl（选项要带各自的 data-test 钩子，组件还不能透传，先保留这组按钮的标记，只换外观；
   SegmentedControl 支持逐项钩子后换成组件，删掉这几条）。 */
.recap-period { display: inline-flex; align-items: center; gap: 10px; }
.recap-period__label { min-width: 92px; font-size: var(--fs-body); font-weight: var(--fw-semibold); text-align: center; font-variant-numeric: tabular-nums; }
.recap-period__note { color: var(--text-muted); font-size: var(--fs-caption); }
.recap-pill { padding: 2px 9px; border-radius: var(--radius-pill); background: var(--accent-soft); color: var(--accent-text); font-size: var(--fs-caption); font-weight: var(--fw-semibold); }
.recap-live-dot { width: 9px; height: 9px; border-radius: 50%; background: var(--danger-color); box-shadow: 0 0 0 0 color-mix(in srgb, var(--danger-color) 60%, transparent); animation: recap-pulse 1.8s ease-out infinite; }
@keyframes recap-pulse { to { box-shadow: 0 0 0 10px transparent; } }

.recap-skeleton { display: grid; gap: 16px; }
.recap-skeleton__row { display: grid; grid-template-columns: repeat(4, 1fr); gap: 14px; }
.recap-skeleton__tile { padding: 18px; border: 1px solid var(--hairline); border-radius: var(--radius-lg); background: var(--panel-bg); }

/* 大幅背景里的文字：两主题都是白字压在暗化的画面上。 */
.recap-hero__layout { display: flex; align-items: flex-end; justify-content: space-between; gap: 24px; padding: 36px 40px; }
.recap-hero__layout .recap-hero__body { padding: 0; }
.recap-hero__chips { display: flex; flex-wrap: wrap; gap: 8px; }
.recap-hero__count {
  display: flex;
  align-items: baseline;
  gap: 4px;
  flex: none;
  color: var(--recap-hero-fg);
  font-size: var(--fs-hero);
  font-weight: var(--fw-bold);
  line-height: 0.9;
  letter-spacing: -0.05em;
  text-shadow: 0 6px 40px var(--recap-scrim);
}
.recap-hero__count small { font-size: var(--fs-body); font-weight: var(--fw-bold); letter-spacing: 0; color: var(--recap-hero-muted); }
/* 大图里的芯片是 TagPill（D-UD-06），落在压暗的画面上：两主题都是白字、半透明底（与整页详情的标签同一套 --recap-hero-*）。
   选择器带上 .recap-hero__chips，压过 TagPill 自己的默认外观。 */
.recap-hero__chips .recap-chip {
  gap: 6px;
  max-width: none;
  height: 30px;
  padding: 0 12px;
  border-color: var(--recap-hero-chip-border);
  background: var(--recap-hero-chip);
  color: var(--recap-hero-fg);
  font-size: var(--fs-secondary);
  font-weight: var(--fw-semibold);
  backdrop-filter: blur(6px);
  transition: background var(--transition), transform var(--transition);
}
.recap-hero__chips .recap-chip:hover {
  border-color: var(--recap-hero-chip-border);
  background: color-mix(in srgb, var(--recap-hero-fg) 24%, transparent);
  color: var(--recap-hero-fg);
  transform: translateY(-1px);
}
.recap-chip__dot { width: 8px; height: 8px; border-radius: 50%; background: var(--chip-color); box-shadow: 0 0 0 2px color-mix(in srgb, var(--chip-color) 35%, transparent); }
.recap-chip--person img { width: 20px; height: 20px; margin-left: -6px; border-radius: 50%; object-fit: cover; }

.recap-stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 14px; }
.recap-stat__delta { justify-self: start; padding: 1px 8px; border-radius: var(--radius-pill); font-size: var(--fs-caption); font-weight: var(--fw-bold); }
.recap-stat__delta small { font-weight: var(--fw-medium); opacity: 0.8; }
.recap-stat__delta.is-up { background: var(--accent-soft); color: var(--accent-text); }
.recap-stat__delta.is-down { background: var(--warning-soft); color: var(--warning-text); }
.recap-stat__delta.is-flat { background: var(--panel-muted-bg); color: var(--text-secondary); }

.recap-split { display: grid; grid-template-columns: repeat(auto-fit, minmax(380px, 1fr)); gap: 16px; align-items: start; }
.recap-split--wide { grid-template-columns: minmax(0, 1.75fr) minmax(300px, 1fr); }
.recap-side { display: grid; gap: 16px; min-width: 0; }

.recap-titles { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: 14px; }
.recap-title {
  position: relative;
  display: flex;
  align-items: center;
  gap: 14px;
  overflow: hidden;
  padding: 18px;
  border: 1px solid var(--accent-border);
  border-radius: var(--radius-lg);
  background: linear-gradient(135deg, var(--accent-soft), var(--panel-bg) 70%);
}
.recap-title--1 { border-color: var(--warning-border); background: linear-gradient(135deg, var(--warning-soft), var(--panel-bg) 70%); }
.recap-title--2 { border-color: var(--border-strong); background: linear-gradient(135deg, var(--panel-muted-bg), var(--panel-bg) 70%); }
.recap-title__icon {
  display: grid;
  place-items: center;
  flex: none;
  width: 52px;
  height: 52px;
  border-radius: 16px;
  background: var(--accent-color);
  color: var(--accent-on);
  font-size: var(--fs-display);
  transform: rotate(-6deg);
  box-shadow: 0 10px 24px color-mix(in srgb, var(--accent-color) 30%, transparent);
}
.recap-title--1 .recap-title__icon { background: var(--warning-color); box-shadow: 0 10px 24px color-mix(in srgb, var(--warning-color) 30%, transparent); }
.recap-title--2 .recap-title__icon { background: var(--text-primary); color: var(--panel-bg); box-shadow: none; }
.recap-title__text { display: grid; gap: 4px; min-width: 0; }
.recap-title__text strong { font-size: var(--fs-section); font-weight: var(--fw-bold); }
.recap-title__text p { margin: 0; color: var(--text-secondary); font-size: var(--fs-secondary); line-height: 1.5; }

.recap-posters { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); grid-auto-flow: dense; gap: 14px; }
.recap-posters__lead { grid-column: span 2; grid-row: span 2; }
.recap-posters__lead :deep(.recap-card__rank) { font-size: var(--fs-hero); }
.recap-posters__lead :deep(.recap-card__text strong) { font-size: var(--fs-section); }

.recap-cloud { display: flex; flex-wrap: wrap; align-items: baseline; gap: 8px 14px; }
.recap-cloud__tag {
  display: inline-flex;
  align-items: baseline;
  gap: 4px;
  padding: 2px 4px;
  border: 0;
  border-bottom: 2px solid color-mix(in srgb, var(--chip-color) 60%, transparent);
  background: transparent;
  color: var(--text-primary);
  font-weight: var(--fw-bold);
  line-height: 1.2;
  cursor: pointer;
  transition: color var(--transition), transform var(--transition);
}
.recap-cloud__tag:hover { color: var(--chip-color); transform: translateY(-2px); }
.recap-cloud__tag small { color: var(--text-muted); font-size: var(--fs-caption); font-weight: var(--fw-semibold); }

.recap-collection small { color: var(--text-muted); font-size: var(--fs-caption); }
.recap-collections { display: grid; gap: 8px; margin: 0; padding: 0; list-style: none; }
.recap-collection {
  display: grid;
  grid-template-columns: 44px minmax(0, 1fr) auto;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 6px;
  border: 1px solid var(--hairline-soft);
  border-radius: var(--radius-md);
  background: var(--panel-subtle-bg);
  color: var(--text-primary);
  text-align: left;
  cursor: pointer;
}
.recap-collection:hover { border-color: var(--accent-border); }
.recap-collection__cover { display: grid; place-items: center; width: 44px; height: 44px; overflow: hidden; border-radius: var(--radius-sm); background: var(--thumb-bg); color: var(--thumb-fg); font-size: var(--fs-title); }
.recap-collection__cover img { width: 100%; height: 100%; object-fit: cover; }
.recap-collection strong { overflow: hidden; font-size: var(--fs-secondary); text-overflow: ellipsis; white-space: nowrap; }

.recap-mosaic { columns: 6 150px; column-gap: 12px; }
.recap-mosaic__item { break-inside: avoid; margin-bottom: 12px; }

.recap-habits { display: grid; grid-template-columns: minmax(240px, 290px) minmax(280px, 1fr) minmax(280px, 340px); gap: 16px; align-items: start; }
.recap-habits__clock { place-items: center; }
.recap-facts { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; align-content: start; }
.recap-fact {
  display: grid;
  gap: 2px;
  padding: 12px 14px;
  border: 1px solid var(--hairline);
  border-radius: var(--radius-md);
  background: var(--panel-bg);
}
.recap-fact small { color: var(--text-muted); font-size: var(--fs-caption); }
.recap-fact strong { font-size: var(--fs-title); font-weight: var(--fw-bold); letter-spacing: -0.01em; }
.recap-fact > span { color: var(--text-secondary); font-size: var(--fs-caption); }
.recap-fact--accent { border-color: var(--accent-border); background: linear-gradient(135deg, var(--accent-soft), var(--panel-bg)); }
.recap-sources { grid-column: 1 / -1; display: flex; gap: 3px; height: 12px; overflow: hidden; border-radius: var(--radius-pill); background: var(--panel-muted-bg); }
.recap-sources__bar { flex-basis: 0; min-width: 4px; background: var(--accent-color); }
.recap-sources__bar.is-image { background: var(--warning-color); }
.recap-sources__bar:nth-child(2n).is-video { background: color-mix(in srgb, var(--accent-color) 60%, var(--panel-bg)); }
.recap-sources__legend { grid-column: 1 / -1; display: flex; flex-wrap: wrap; gap: 4px 12px; margin: 0; padding: 0; list-style: none; color: var(--text-secondary); font-size: var(--fs-caption); }
.recap-sources__legend i { display: inline-block; width: 8px; height: 8px; margin-right: 5px; border-radius: 2px; background: var(--accent-color); }
.recap-sources__legend i.is-image { background: var(--warning-color); }

.recap-fresh { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 12px; }
.recap-fresh .recap-fact strong { font-size: var(--fs-display); }

.recap-bars { display: grid; grid-auto-flow: column; grid-auto-columns: minmax(0, 1fr); gap: 8px; align-items: end; height: 170px; }
.recap-bars__col { display: grid; grid-template-rows: 1fr auto; gap: 6px; height: 100%; min-width: 0; text-align: center; }
.recap-bars__stack { display: flex; flex-direction: column-reverse; justify-content: flex-start; height: 100%; overflow: hidden; border-radius: 8px; background: var(--panel-muted-bg); }
.recap-bars__plays, .recap-bars__views { display: block; flex: none; transition: height 700ms cubic-bezier(0.2, 0.8, 0.2, 1); }
.recap-bars__plays { background: linear-gradient(0deg, var(--accent-color), color-mix(in srgb, var(--accent-color) 65%, var(--panel-bg))); }
.recap-bars__views { background: var(--warning-color); }
.recap-bars__col small { color: var(--text-muted); font-size: var(--fs-caption); white-space: nowrap; }
.recap-bars--week { height: 150px; }
.recap-live-clock { max-width: 240px; justify-self: center; }

.recap-quarters { display: grid; gap: 10px; margin: 0; padding: 0; list-style: none; }
.recap-quarters li { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; padding-bottom: 10px; border-bottom: 1px dashed var(--hairline); }
.recap-quarters li:last-child { border-bottom: 0; padding-bottom: 0; }
.recap-quarters strong { width: 30px; color: var(--accent-text); font-size: var(--fs-body); font-weight: var(--fw-bold); }
.recap-diary-link { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; margin: 0; color: var(--text-muted); font-size: var(--fs-secondary); }

/* v-reveal：进入视口时淡入上移，序号越大越晚。 */
.recap-reveal { opacity: 0; transform: translateY(18px); transition: opacity 620ms ease, transform 620ms cubic-bezier(0.2, 0.8, 0.2, 1); transition-delay: var(--reveal-delay, 0ms); }
.recap-reveal.is-revealed { opacity: 1; transform: none; }

@media (max-width: 1360px) {
  .recap-habits { grid-template-columns: minmax(220px, 280px) minmax(0, 1fr); }
  .recap-habits .recap-facts { grid-column: 1 / -1; grid-template-columns: repeat(4, minmax(0, 1fr)); }
}
@media (max-width: 1180px) {
  .recap-split--wide { grid-template-columns: minmax(0, 1fr); }
  .recap-side { grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); align-items: start; }
  .recap-posters { grid-template-columns: repeat(4, minmax(0, 1fr)); }
  .recap-hero__layout { padding: 28px; }
}
@media (prefers-reduced-motion: reduce) {
  .recap-reveal, .recap-bars__plays, .recap-bars__views { transition: none; }
  .recap-reveal { opacity: 1; transform: none; }
  .recap-live-dot { animation: none; }
  .recap-hero__chips .recap-chip, .recap-cloud__tag { transition: none; }
}
</style>
