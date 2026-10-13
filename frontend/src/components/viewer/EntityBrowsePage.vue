<template>
  <section :class="['page-content', 'entity-browse', `entity-browse--${kind}`]" :data-test="`entity-browse-${kind}`">
    <!-- 人物 / 作品集观看页（D-P2-06，详细设计 §3.3）：列表与对象页在同一组件内切换；只看不改，
         编辑、删除、合并、人脸审阅都在管理侧，这里只有「在管理中编辑」。注释放在根元素里面（多根片段会让宿主的 v-show 失效）。 -->
    <div v-show="!selected" class="entity-browse__list" data-test="entity-browse-list">
      <PageHeader :title="noun" :subtitle="listSubtitle">
        <template #actions>
          <label class="entity-browse__search">
            <AppIcon name="search" />
            <input
              v-model="keyword"
              type="search"
              class="search-input"
              :placeholder="isPeople ? '搜索姓名' : '搜索作品集名称或简介'"
              :aria-label="`搜索${noun}`"
              data-test="entity-browse-search"
              @keydown.enter="searchNow"
            />
          </label>
        </template>
      </PageHeader>

      <AppBanner v-if="list.error" tone="danger" :title="list.error" data-test="entity-browse-list-error">
        <template #actions><button type="button" class="btn btn--secondary btn--sm" data-test="entity-browse-list-retry" @click="loadMore()">重试</button></template>
      </AppBanner>
      <AppBanner v-if="failedProgressIDs.length" tone="warning" :title="`有 ${failedProgressIDs.length} 个作品集的观看进度没读到`" data-test="entity-browse-progress-error">
        <template #actions><button type="button" class="btn btn--secondary btn--sm" data-test="entity-browse-progress-retry" @click="loadCardProgress(failedProgressIDs)">重试</button></template>
      </AppBanner>

      <div v-if="list.loading && !list.items.length" :class="gridClass" aria-busy="true" data-test="entity-browse-list-loading">
        <SkeletonBlock v-for="n in 12" :key="n" :class="{ 'entity-browse__skeleton-avatar': isPeople }" :shape="isPeople ? 'thumb' : 'poster'" :lines="1" />
      </div>
      <EmptyState
        v-else-if="!list.loading && !list.error && !list.items.length"
        :icon="isPeople ? 'person' : 'collection'"
        :title="searchedKeyword ? `没有找到「${searchedKeyword}」` : `还没有${noun}`"
        :message="searchedKeyword ? '换个关键词，或清空搜索看全部。' : emptyListMessage"
        data-test="entity-browse-list-empty"
      >
        <template #actions>
          <button v-if="searchedKeyword" type="button" class="btn btn--secondary" data-test="entity-browse-clear-search" @click="clearSearch">清空搜索</button>
          <button v-else type="button" class="btn btn--secondary" data-test="entity-browse-go-manage" @click="editInManage(null)">去人物与作品集管理</button>
        </template>
      </EmptyState>

      <div v-if="list.items.length" :class="gridClass">
        <template v-if="isPeople">
          <button
            v-for="item in list.items"
            :key="itemID(item)"
            type="button"
            class="btn-plain entity-browse__person"
            :data-test="`entity-browse-person-${itemID(item)}`"
            @click="openFromList(item)"
          >
            <span class="entity-browse__avatar">
              <img v-if="item.avatar_url && !brokenImages[`p${itemID(item)}`]" :src="item.avatar_url" alt="" loading="lazy" @error="markBroken(`p${itemID(item)}`)" />
              <b v-else>{{ initial(itemName(item)) }}</b>
            </span>
            <strong :title="itemName(item)">{{ itemName(item) }}</strong>
            <small>{{ personCounts(item) }}</small>
          </button>
        </template>
        <template v-else>
          <button
            v-for="item in list.items"
            :key="itemID(item)"
            type="button"
            class="btn-plain entity-browse__collection"
            :data-test="`entity-browse-collection-${itemID(item)}`"
            @click="openFromList(item)"
          >
            <span class="entity-browse__cover">
              <img v-if="item.cover_url && !brokenImages[`c${itemID(item)}`]" :src="item.cover_url" alt="" loading="lazy" @error="markBroken(`c${itemID(item)}`)" />
              <AppIcon v-else name="collection" :size="24" />
            </span>
            <strong :title="itemName(item)">{{ itemName(item) }}</strong>
            <span class="entity-browse__card-progress" :data-test="`entity-browse-collection-progress-${itemID(item)}`">
              <SkeletonBlock v-if="cardProgressState(item) === 'loading'" :lines="1" />
              <small v-else-if="cardProgressState(item) === 'error'" class="entity-browse__muted">进度没读到 · 共 {{ item.active_video_count || 0 }} 部视频</small>
              <template v-else-if="cardProgressData(item)">
                <small>{{ progressText(cardProgressData(item)) }}</small>
                <ProgressBar v-if="progressStarted(cardProgressData(item))" :value="progressRatio(cardProgressData(item))" />
              </template>
              <small v-else class="entity-browse__muted">共 {{ item.active_video_count || 0 }} 部视频</small>
            </span>
          </button>
        </template>
      </div>
      <div ref="listSentinel" class="entity-browse__sentinel" aria-hidden="true"></div>
      <button
        v-if="list.items.length && list.hasMore && !list.error"
        type="button"
        class="btn btn--secondary entity-browse__more"
        :disabled="list.loading"
        data-test="entity-browse-list-more"
        @click="loadMore()"
      >{{ list.loading ? '正在加载…' : `加载更多${noun}` }}</button>
    </div>

    <template v-if="selected">
      <nav class="entity-browse__nav">
        <button type="button" class="btn btn--secondary btn--sm" data-test="entity-browse-back" @click="closeEntity"><AppIcon name="chevron-left" /> 返回{{ noun }}</button>
      </nav>

      <!-- 人物页：大图头部（头像、名字、作品数）+ 作品海报墙（版本合成一张）+ 相关图片条带。三块各自加载、各自出错。 -->
      <template v-if="isPeople">
        <RecapBackdrop size="small" data-test="entity-person-hero">
          <div class="entity-browse__hero">
            <span class="entity-browse__hero-avatar">
              <img v-if="personAvatar && !brokenImages.hero" :src="personAvatar" alt="" @error="markBroken('hero')" />
              <b v-else>{{ initial(selectedName) }}</b>
            </span>
            <HeroHeader class="recap-hero__body entity-browse__hero-body" :title="selectedName || '人物'" :meta="personMeta" title-data-test="entity-person-name" meta-data-test="entity-person-counts">
              <template #eyebrow><AppIcon name="person" /> 人物</template>
              <template #actions>
                <button type="button" class="btn btn--secondary" data-test="entity-edit-in-manage" @click="editInManage(selected)">在管理中编辑</button>
              </template>
            </HeroHeader>
          </div>
        </RecapBackdrop>
        <AppBanner v-if="hero.error" tone="danger" :title="hero.error" data-test="entity-person-hero-error">
          <template #actions><button type="button" class="btn btn--secondary btn--sm" data-test="entity-person-hero-retry" @click="loadPersonHero(entityToken)">重试</button></template>
        </AppBanner>

        <section class="recap-section" data-test="entity-person-works">
          <header class="recap-section__head"><h3><AppIcon name="videos" /> 作品</h3><small>按年份从新到旧，同一部的多个版本合成一张</small></header>
          <AppBanner v-if="works.error" tone="danger" :title="works.error" data-test="entity-person-works-error">
            <template #actions><button type="button" class="btn btn--secondary btn--sm" data-test="entity-person-works-retry" @click="loadWorks(entityToken, { reset: !works.items.length })">重试</button></template>
          </AppBanner>
          <div v-if="works.loading && !works.items.length" class="entity-browse__posters" aria-busy="true" data-test="entity-person-works-loading">
            <SkeletonBlock v-for="n in 8" :key="n" shape="poster" :lines="1" />
          </div>
          <EmptyState
            v-else-if="works.loaded && !works.error && !works.items.length"
            icon="videos"
            title="还没有关联的视频"
            message="在管理中编辑这个人物，可以按文件夹筛选并把视频关联进来。"
            data-test="entity-person-works-empty"
          >
            <template #actions><button type="button" class="btn btn--secondary" @click="editInManage(selected)">在管理中编辑</button></template>
          </EmptyState>
          <div v-if="works.items.length" class="entity-browse__posters">
            <RecapMediaCard
              v-for="video in works.items"
              :key="video.id"
              variant="poster"
              :id="Number(video.id)"
              :cover="posterURL(video.id)"
              :title="videoTitle(video)"
              :subtitle="workSubtitle(video)"
              :badge="workBadge(video)"
              :available="!video.is_stale"
              :data-test="`entity-person-work-${video.id}`"
              @open="openVideo(video.id)"
            />
          </div>
          <div ref="worksSentinel" class="entity-browse__sentinel" aria-hidden="true"></div>
          <button
            v-if="works.items.length && works.cursor && !works.error"
            type="button"
            class="btn btn--secondary entity-browse__more"
            :disabled="works.loading"
            data-test="entity-person-works-more"
            @click="loadWorks(entityToken, { reset: false })"
          >{{ works.loading ? '正在加载…' : '加载更多作品' }}</button>
        </section>

        <section class="recap-section" data-test="entity-person-images">
          <header class="recap-section__head"><h3><AppIcon name="images" /> 相关图片</h3><small v-if="personImageCount">{{ personImageCount }} 张</small></header>
          <AppBanner v-if="images.error" tone="danger" :title="images.error" data-test="entity-person-images-error">
            <template #actions><button type="button" class="btn btn--secondary btn--sm" data-test="entity-person-images-retry" @click="loadImages(entityToken, { reset: !images.items.length })">重试</button></template>
          </AppBanner>
          <div v-if="images.loading && !images.items.length" class="recap-strip recap-strip--images" aria-busy="true" data-test="entity-person-images-loading">
            <SkeletonBlock v-for="n in 6" :key="n" shape="thumb" />
          </div>
          <EmptyState
            v-else-if="images.loaded && !images.error && !images.items.length"
            icon="images"
            title="还没有关联的图片"
            message="在图片的单图详情里标出人物，图片就会出现在这里。"
            data-test="entity-person-images-empty"
          >
            <template #actions><button type="button" class="btn btn--secondary" data-test="entity-person-images-go-photos" @click="$emit('navigate', 'photos')">去图片</button></template>
          </EmptyState>
          <div v-if="images.items.length" class="recap-strip recap-strip--images">
            <RecapMediaCard
              v-for="image in images.items"
              :key="image.id"
              kind="image"
              variant="tile"
              class="recap-strip__item"
              :id="Number(image.id)"
              :title="image.name"
              :available="!image.is_stale"
              :data-test="`entity-person-image-${image.id}`"
              @open="$emit('open-image', image)"
            />
            <button
              v-if="images.cursor"
              type="button"
              class="btn btn--secondary entity-browse__strip-more"
              :disabled="images.loading"
              data-test="entity-person-images-more"
              @click="loadImages(entityToken, { reset: false })"
            >{{ images.loading ? '正在加载…' : '更多图片' }}</button>
          </div>
        </section>
      </template>

      <!-- 作品集页：大图头部（封面、名称、进度、「从下一部播放」「全部加入待播」）+ 按顺序的各部。
           进度与「第 N / M 部」只用进度接口的口径（只算在线成员、版本组合成一部）；离线成员照列、标「离线」、不编号。 -->
      <template v-else>
        <RecapBackdrop size="small" :frames="collectionCover ? [collectionCover] : []" data-test="entity-collection-hero">
          <div class="entity-browse__hero">
            <span class="entity-browse__hero-cover">
              <img v-if="collectionCover && !brokenImages.hero" :src="collectionCover" alt="" @error="markBroken('hero')" />
              <AppIcon v-else name="collection" :size="24" />
            </span>
            <HeroHeader class="recap-hero__body entity-browse__hero-body" :title="selectedName || '作品集'" :meta="collectionDescription" title-data-test="entity-collection-name">
              <template #eyebrow><AppIcon name="collection" /> 作品集</template>
              <div class="entity-browse__hero-progress" data-test="entity-collection-progress">
                <p v-if="progress.loading && !progress.data" class="recap-hero__meta">正在读取观看进度…</p>
                <p v-else-if="progress.error" class="recap-hero__meta" role="alert">
                  {{ progress.error }}
                  <button type="button" class="btn btn--secondary btn--sm" data-test="entity-collection-progress-retry" @click="retryProgress">重试</button>
                </p>
                <template v-else-if="progress.data">
                  <p class="recap-hero__meta" data-test="entity-collection-progress-text">{{ heroProgressText }}</p>
                  <ProgressBar v-if="progress.data.total_works" class="entity-browse__hero-bar" :value="progressRatio(progress.data)" :label="`看过 ${progress.data.watched_works} / ${progress.data.total_works} 部`" />
                </template>
              </div>
              <template #actions>
                <button
                  v-if="nextRequest"
                  type="button"
                  class="btn btn--primary"
                  data-test="entity-collection-play-next"
                  @click="playNext"
                ><AppIcon name="play" /> 从下一部播放</button>
                <QueueActionButton :collection-id="Number(selected.id)" label="全部加入待播" size="md" :disabled="!onlineWorkCount" />
                <button type="button" class="btn btn--secondary" data-test="entity-edit-in-manage" @click="editInManage(selected)">在管理中编辑</button>
              </template>
            </HeroHeader>
          </div>
        </RecapBackdrop>

        <section class="recap-section" data-test="entity-collection-works">
          <header class="recap-section__head">
            <h3><AppIcon name="videos" /> 各部</h3>
            <small>按作品集顺序；同一部的多个版本算一部{{ offlineCount ? `；${offlineCount} 个离线的不计入` : '' }}</small>
          </header>
          <AppBanner v-if="detail.error || groups.error" tone="danger" :title="detail.error || groups.error" data-test="entity-collection-works-error">
            <template #actions><button type="button" class="btn btn--secondary btn--sm" data-test="entity-collection-works-retry" @click="loadCollection()">重试</button></template>
          </AppBanner>
          <SkeletonBlock v-if="worksPending" :lines="6" data-test="entity-collection-works-loading" />
          <EmptyState
            v-else-if="detail.data && !collectionRows.length"
            icon="collection"
            title="这个作品集还没有作品"
            message="在管理中编辑这个作品集，可以把视频加进来并排好顺序。"
            data-test="entity-collection-works-empty"
          >
            <template #actions><button type="button" class="btn btn--secondary" @click="editInManage(selected)">在管理中编辑</button></template>
          </EmptyState>
          <ol v-else-if="detail.data" class="entity-browse__works">
            <li v-for="row in collectionRows" :key="row.key">
              <button
                type="button"
                :class="['btn-plain', 'entity-browse__work', { 'entity-browse__work--next': row.key === nextRowKey, 'entity-browse__work--offline': row.offline }]"
                :disabled="row.offline"
                :aria-current="row.key === nextRowKey ? 'step' : undefined"
                :data-test="`entity-collection-work-${row.representative.id}`"
                @click="openVideo(row.openID)"
              >
                <span class="entity-browse__work-index">{{ row.index || '—' }}</span>
                <span class="entity-browse__work-thumb"><img :src="thumbnailURL(row.representative.id)" alt="" loading="lazy" /></span>
                <span class="entity-browse__work-copy">
                  <strong :title="videoTitle(row.representative)">{{ videoTitle(row.representative) }}</strong>
                  <small>{{ workFacts(row) }}</small>
                </span>
                <span class="entity-browse__work-badges">
                  <AppBadge v-if="row.key === nextRowKey" tone="accent" data-test="entity-collection-next-badge">下一部</AppBadge>
                  <AppBadge :tone="stateTone(row.state)" :data-test="`entity-collection-state-${row.representative.id}`">{{ stateLabel(row.state) }}</AppBadge>
                </span>
              </button>
            </li>
          </ol>
        </section>
      </template>
    </template>
  </section>
</template>

<script>
import {
  GetCollectionDetail, GetPersonDetail, GetPersonImages, ListCollectionProgress, ListCollections, ListPeople, SearchLibraryVideoPage
} from '../../../wailsjs/go/main/App';
import QueueActionButton from '../QueueActionButton.vue';
import RecapBackdrop from '../recap/RecapBackdrop.vue';
import RecapMediaCard from '../recap/RecapMediaCard.vue';
import AppBadge from '../ui/AppBadge.vue';
import AppBanner from '../ui/AppBanner.vue';
import AppIcon from '../ui/AppIcon.vue';
import EmptyState from '../ui/EmptyState.vue';
import HeroHeader from '../ui/HeroHeader.vue';
import PageHeader from '../ui/PageHeader.vue';
import ProgressBar from '../ui/ProgressBar.vue';
import SkeletonBlock from '../ui/SkeletonBlock.vue';
import { translateBackendError } from '../../utils/feedback.js';
import { formatDuration } from '../../utils/mediaDetails.js';
import { videoArtwork, videoThumbnail } from '../../utils/recap.js';

// 列表每批取多少（ListPeople / ListCollections 按名称游标翻页）；作品集卡片的进度按这一批一起取。
export const ENTITY_PAGE_SIZE = 60;
// 人物作品海报墙每批（SearchLibraryVideoPage，人物筛选 + 合并版本 + 年份从新到旧）。
export const PERSON_WORKS_PAGE_SIZE = 60;
export const PERSON_IMAGES_PAGE_SIZE = 30;
// ListCollectionProgress 按 ID 一次最多 200 个。
export const PROGRESS_BATCH_LIMIT = 200;
export const SEARCH_DEBOUNCE_MS = 250;

const NOUNS = { person: '人物', collection: '作品集' };
const STATE_LABELS = { watched: '看过', 'in-progress': '在看', unwatched: '没看', offline: '离线' };
// 「下一部」用主色徽标；在看用琥珀色（进行中），离线那一行整行压暗，徽标不再另加颜色。
const STATE_TONES = { watched: 'success', 'in-progress': 'warning', unwatched: 'neutral', offline: 'neutral' };

function errorText(err) {
  return translateBackendError(err?.message || err);
}

function timeOf(value) {
  const time = value ? Date.parse(value) : NaN;
  return Number.isFinite(time) ? time : -Infinity;
}

export function videoTitle(video) {
  return String(video?.display_title || video?.name || '未命名视频');
}

// 看过 / 在看 / 没看，与 D-P2-02 同一口径：任一成员（版本）看过即看过；都没看过、有一个有续播位置即在看。
export function watchStateOf(videos) {
  const list = (Array.isArray(videos) ? videos : [videos]).filter(Boolean);
  if (list.some(video => video.is_watched)) return 'watched';
  if (list.some(video => Number(video.watch_position_seconds) > 0)) return 'in-progress';
  return 'unwatched';
}

// 作品集卡片与作品集页头部的进度文字：「第 N / M 部」的 N 是下一部（接着看的那一部），M 是进度接口的总部数。
export function collectionProgressText(progress) {
  if (!progress) return '';
  const total = Number(progress.total_works || 0);
  if (!total) return '还没有在线的作品';
  if (progress.complete) return `已看完 · 共 ${total} 部`;
  if (!collectionStarted(progress) || !progress.next) return `共 ${total} 部 · 还没开始看`;
  return `第 ${progress.next.work_index} / ${total} 部`;
}

export function collectionStarted(progress) {
  if (!progress) return false;
  return Boolean(progress.complete) || Number(progress.watched_works || 0) + Number(progress.in_progress_works || 0) > 0;
}

export function collectionProgressRatio(progress) {
  const total = Number(progress?.total_works || 0);
  return total ? Math.min(1, Number(progress.watched_works || 0) / total) : 0;
}

// 「从下一部播放」：进整页详情并在应用内起播；在看的那一部从续播位置接着放（next.video_id 就是有断点的那个文件），
// 其余从头放。没有下一部（全部看完或没有在线作品）时返回 null。
export function playNextRequest(progress) {
  const next = progress?.next;
  const id = Number(next?.video_id || 0);
  if (!id) return null;
  const position = Number(next.watch_position_seconds || 0);
  return { id, startAtMs: next.resume && position > 0 ? Math.round(position * 1000) : 0 };
}

// 在线成员比进度接口算出的部数多，说明有版本组合成了一部：这时才去查各成员所属的版本组。
export function needsVersionGroups(members, progress) {
  if (!progress) return false;
  const online = (members || []).filter(item => item?.video && !item.video.is_stale).length;
  return online > Number(progress.total_works || 0);
}

// 作品集页的各部（D-P2-02）。members 是 GetCollectionDetail 的成员（已按 position 排好）；
// groups 是「视频 ID → { groupID, order }」，不在表里的就不在版本组里。
// - 离线（失效）成员单列一行、不编号、不计入部数（进度接口只算在线成员）；
// - 同一版本组的在线成员合成一部，位置取组里最先出现的那个；代表取组内版本顺序第一位（同序取 ID 小的）；
// - 状态按整部算；在看那一部打开有续播位置的那个文件（进度最近更新的一个），否则打开代表。
export function collectionWorks(members, groups = {}) {
  const rows = [];
  const byGroup = new Map();
  let index = 0;
  for (const member of members || []) {
    const video = member?.video;
    const id = Number(video?.id || 0);
    if (!id) continue;
    if (video.is_stale) {
      rows.push({ key: `offline-${id}`, index: 0, offline: true, videos: [video] });
      continue;
    }
    const group = groups[id];
    if (group && byGroup.has(group.groupID)) {
      byGroup.get(group.groupID).videos.push(video);
      continue;
    }
    index += 1;
    const row = { key: `work-${id}`, index, offline: false, videos: [video] };
    rows.push(row);
    if (group) byGroup.set(group.groupID, row);
  }
  return rows.map(row => {
    const order = video => Number(groups[Number(video.id)]?.order || 0);
    const videos = [...row.videos].sort((a, b) => order(a) - order(b) || Number(a.id) - Number(b.id));
    const state = row.offline ? 'offline' : watchStateOf(videos);
    const resume = state === 'in-progress'
      ? videos.filter(video => Number(video.watch_position_seconds) > 0)
        .reduce((best, video) => (!best || timeOf(video.watch_progress_updated_at) > timeOf(best.watch_progress_updated_at) ? video : best), null)
      : null;
    const representative = videos[0];
    return { ...row, videos, representative, state, resumeVideo: resume, openID: Number((resume || representative).id) };
  });
}

// 下一部在列表里是哪一行：包含 next.video_id 的那一部。
export function nextWorkKey(rows, progress) {
  const id = Number(progress?.next?.video_id || 0);
  if (!id) return '';
  return (rows || []).find(row => !row.offline && row.videos.some(video => Number(video.id) === id))?.key || '';
}

export default {
  name: 'EntityBrowsePage',
  components: { AppBadge, AppBanner, AppIcon, EmptyState, HeroHeader, PageHeader, ProgressBar, QueueActionButton, RecapBackdrop, RecapMediaCard, SkeletonBlock },
  props: {
    // person | collection：同一个组件挂两次（人物页、作品集页），各自一份状态。
    kind: { type: String, required: true, validator: kind => kind === 'person' || kind === 'collection' },
    // 宿主切回本页时为 true：作品集的进度可能在别处看过片后变了，回来时重取（保留现有内容，不闪骨架）。
    pageActive: { type: Boolean, default: true },
    // 命令面板、首页、整页详情、回顾指定要打开的对象：{ id, name }，置一次即消费。
    focusEntity: { type: Object, default: null }
  },
  // open-video(id)：进整页详情；play-video({ id, startAtMs })：进整页详情并在应用内从这一刻起播；
  // open-image(image)：去图片页看这一张；edit-in-manage({ kind, id?, name? })：去管理侧「人物与作品集」，带 id 时定位到它；
  // navigate(pageKey)：空状态的下一步（去图片）。
  emits: ['open-video', 'play-video', 'open-image', 'edit-in-manage', 'navigate'],
  data() {
    return {
      keyword: '',
      searchedKeyword: '',
      list: { items: [], loading: false, error: '', hasMore: true, cursorName: '', cursorID: 0 },
      // 作品集卡片的进度：collection_id → { state: loading | ready | error, data }。
      cardProgress: {},
      brokenImages: {},
      // 当前打开的对象：{ id, name, item }；null 是列表。
      selected: null,
      entityToken: 0,
      hero: { loading: false, error: '', item: null },
      works: { loading: false, error: '', items: [], cursor: null, groups: {}, loaded: false },
      images: { loading: false, error: '', items: [], cursor: 0, loaded: false },
      detail: { loading: false, error: '', data: null },
      progress: { loading: false, error: '', data: null },
      groups: { loading: false, error: '', map: {}, ready: false }
    };
  },
  computed: {
    isPeople() { return this.kind === 'person'; },
    noun() { return NOUNS[this.kind]; },
    gridClass() { return this.isPeople ? 'entity-browse__people' : 'entity-browse__collections'; },
    listSubtitle() {
      return this.isPeople ? '按名称排列；点一位看 TA 的作品与图片。' : '按名称排列；卡片上是追到哪一部。';
    },
    emptyListMessage() {
      return this.isPeople
        ? '人物来自本地资料（NFO）导入、人脸命名，或在管理侧新建。'
        : '作品集来自本地资料里的系列、采纳的建议作品集，或在管理侧新建。';
    },
    failedProgressIDs() {
      if (this.isPeople) return [];
      return this.list.items.map(item => this.itemID(item)).filter(id => this.cardProgress[id]?.state === 'error');
    },
    selectedName() {
      if (!this.selected) return '';
      if (this.isPeople) return String(this.hero.item?.person?.display_name || this.selected.name || '');
      return String(this.detail.data?.collection?.collection?.name || this.progress.data?.name || this.selected.name || '');
    },
    personItem() { return this.hero.item || this.selected?.item || null; },
    personAvatar() { return String(this.personItem?.avatar_url || ''); },
    personImageCount() { return Number(this.personItem?.active_image_count || 0); },
    personMeta() {
      if (this.hero.loading && !this.hero.item) return '正在读取…';
      const item = this.personItem;
      if (!item) return '';
      const parts = [];
      if (item.person?.original_name && item.person.original_name !== item.person.display_name) parts.push(item.person.original_name);
      parts.push(`${Number(item.active_video_count || 0)} 部视频`);
      if (Number(item.active_image_count || 0)) parts.push(`${Number(item.active_image_count)} 张图片`);
      return parts.join(' · ');
    },
    collectionItem() { return this.detail.data?.collection || this.selected?.item || null; },
    collectionCover() { return String(this.collectionItem?.cover_url || ''); },
    collectionDescription() { return String(this.collectionItem?.collection?.description || ''); },
    collectionRows() { return collectionWorks(this.detail.data?.videos, this.groups.map); },
    onlineWorkCount() { return this.collectionRows.filter(row => !row.offline).length; },
    offlineCount() { return this.collectionRows.filter(row => row.offline).length; },
    nextRowKey() { return nextWorkKey(this.collectionRows, this.progress.data); },
    nextRequest() { return playNextRequest(this.progress.data); },
    // 头部进度：「第 N / M 部」+ 下一部是哪一部（在看的写看到哪）。
    heroProgressText() {
      const progress = this.progress.data;
      const base = collectionProgressText(progress);
      const next = progress?.next;
      if (!next || !collectionStarted(progress)) return base;
      const where = next.resume && Number(next.watch_position_seconds) > 0 ? ` · 看到 ${formatDuration(next.watch_position_seconds)}` : '';
      return `${base} · 下一部《${next.title || '未命名视频'}》${where}`;
    },
    // 列表区第一次加载（含需要的版本组查询）时出骨架；之后的刷新保留现有内容。
    worksPending() {
      if (!this.detail.data) return this.detail.loading;
      return this.groups.loading && !this.groups.ready;
    }
  },
  watch: {
    keyword() {
      clearTimeout(this._searchTimer);
      this._searchTimer = setTimeout(() => this.searchNow(), SEARCH_DEBOUNCE_MS);
    },
    focusEntity(focus) { this.applyFocus(focus); },
    pageActive(active) { if (active) this.refreshOnReturn(); }
  },
  created() {
    this._listToken = 0;
  },
  mounted() {
    this.setupInfiniteLoading();
    this.reloadList();
    this.applyFocus(this.focusEntity);
  },
  beforeUnmount() {
    clearTimeout(this._searchTimer);
    this._intersectionObserver?.disconnect();
    this._listToken += 1;
    this.entityToken += 1;
  },
  methods: {
    progressText: collectionProgressText,
    progressRatio: collectionProgressRatio,
    progressStarted: collectionStarted,
    videoTitle,
    itemID(item) { return Number(this.isPeople ? item?.person?.id : item?.collection?.id) || 0; },
    itemName(item) { return String((this.isPeople ? item?.person?.display_name : item?.collection?.name) || '未命名'); },
    personCounts(item) {
      const images = Number(item?.active_image_count || 0);
      return `${Number(item?.active_video_count || 0)} 部视频${images ? ` · ${images} 张图片` : ''}`;
    },
    initial(name) { return String(name || '?').trim().slice(0, 1) || '?'; },
    markBroken(key) { this.brokenImages = { ...this.brokenImages, [key]: true }; },
    posterURL(id) { return videoArtwork(Number(id), 'poster'); },
    thumbnailURL(id) { return videoThumbnail(Number(id)); },
    stateLabel(state) { return STATE_LABELS[state] || ''; },
    stateTone(state) { return STATE_TONES[state] || 'neutral'; },
    scroller() { return this.$el?.closest?.('.main-view') || null; },

    // ===== 列表 =====
    searchNow() {
      clearTimeout(this._searchTimer);
      if (this.keyword.trim() === this.searchedKeyword && this.list.items.length) return undefined;
      return this.reloadList();
    },
    clearSearch() {
      this.keyword = '';
      return this.searchNow();
    },
    reloadList() {
      this._listToken += 1;
      this.searchedKeyword = this.keyword.trim();
      this.list = { items: [], loading: false, error: '', hasMore: true, cursorName: '', cursorID: 0 };
      return this.loadMore();
    },
    async loadMore() {
      const token = this._listToken;
      if (this.list.loading || !this.list.hasMore) return;
      const { cursorName, cursorID } = this.list;
      const keyword = this.searchedKeyword;
      this.list.loading = true;
      this.list.error = '';
      try {
        const page = (this.isPeople
          ? await ListPeople(keyword, cursorName, cursorID, ENTITY_PAGE_SIZE)
          : await ListCollections(keyword, cursorName, cursorID, ENTITY_PAGE_SIZE)) || [];
        if (token !== this._listToken) return;
        this.list.items.push(...page);
        this.list.hasMore = page.length === ENTITY_PAGE_SIZE;
        if (page.length) {
          const last = page[page.length - 1];
          this.list.cursorName = last.cursor_name;
          this.list.cursorID = this.itemID(last);
        }
        if (!this.isPeople) this.loadCardProgress(page.map(item => this.itemID(item)));
      } catch (err) {
        if (token === this._listToken) this.list.error = `读取${this.noun}失败：${errorText(err)}`;
      } finally {
        if (token === this._listToken) this.list.loading = false;
      }
    },
    // 作品集卡片的进度：每批卡片一次 ListCollectionProgress（最多 200 个 ID 一次）。一批失败只标这一批，
    // 卡片上写「进度没读到」，列表上方给一个重试。同一个作品集只认最新一次请求的结果。
    async loadCardProgress(ids) {
      const wanted = [...new Set((ids || []).map(Number).filter(Boolean))];
      const batches = [];
      for (let start = 0; start < wanted.length; start += PROGRESS_BATCH_LIMIT) batches.push(wanted.slice(start, start + PROGRESS_BATCH_LIMIT));
      await Promise.all(batches.map(async batch => {
        this._progressSeq = (this._progressSeq || 0) + 1;
        const seq = this._progressSeq;
        const patch = {};
        for (const id of batch) patch[id] = { state: 'loading', data: this.cardProgress[id]?.data || null, seq };
        this.cardProgress = { ...this.cardProgress, ...patch };
        let result = null;
        let failed = false;
        try {
          result = await ListCollectionProgress({ collection_ids: batch });
        } catch (_err) {
          failed = true;
        }
        const byID = new Map((result || []).map(item => [Number(item.collection_id), item]));
        const done = {};
        for (const id of batch) {
          const current = this.cardProgress[id];
          if (current?.seq !== seq) continue;
          done[id] = failed ? { ...current, state: 'error' } : { state: 'ready', data: byID.get(id) || null, seq };
        }
        this.cardProgress = { ...this.cardProgress, ...done };
      }));
    },
    cardProgressState(item) { return this.cardProgress[this.itemID(item)]?.state || ''; },
    cardProgressData(item) { return this.cardProgress[this.itemID(item)]?.data || null; },

    setupInfiniteLoading() {
      if (typeof IntersectionObserver === 'undefined') return;
      this._intersectionObserver = new IntersectionObserver(entries => {
        for (const entry of entries) {
          if (!entry.isIntersecting) continue;
          if (entry.target === this.$refs.listSentinel && !this.selected && !this.list.error) this.loadMore();
          if (entry.target === this.$refs.worksSentinel && this.selected && this.isPeople && this.works.cursor && !this.works.error) {
            this.loadWorks(this.entityToken, { reset: false });
          }
        }
      }, { root: this.scroller(), rootMargin: '480px 0px' });
      this.observeSentinels();
    },
    observeSentinels() {
      const observer = this._intersectionObserver;
      if (!observer) return;
      observer.disconnect();
      for (const ref of ['listSentinel', 'worksSentinel']) if (this.$refs[ref]) observer.observe(this.$refs[ref]);
    },

    // ===== 打开 / 关闭对象 =====
    applyFocus(focus) {
      const id = Number(focus?.id || 0);
      if (!id) return;
      const known = this.list.items.find(item => this.itemID(item) === id) || null;
      this.openEntity({ id, name: focus.name || (known ? this.itemName(known) : ''), item: known });
    },
    openFromList(item) {
      const scroller = this.scroller();
      this._listScrollTop = scroller ? scroller.scrollTop : 0;
      this.openEntity({ id: this.itemID(item), name: this.itemName(item), item });
    },
    openEntity({ id, name = '', item = null }) {
      const entityID = Number(id);
      if (!entityID) return;
      if (!this.selected && this._listScrollTop === undefined) this._listScrollTop = 0;
      this.entityToken += 1;
      this.selected = { id: entityID, name, item };
      this.brokenImages = { ...this.brokenImages, hero: false };
      this.hero = { loading: false, error: '', item: null };
      this.works = { loading: false, error: '', items: [], cursor: null, groups: {}, loaded: false };
      this.images = { loading: false, error: '', items: [], cursor: 0, loaded: false };
      this.detail = { loading: false, error: '', data: null };
      this.progress = { loading: false, error: '', data: null };
      this.groups = { loading: false, error: '', map: {}, ready: false };
      const token = this.entityToken;
      if (this.isPeople) {
        this.loadPersonHero(token);
        this.loadWorks(token, { reset: true });
        this.loadImages(token, { reset: true });
      } else {
        this.loadCollection();
      }
      this.$nextTick(() => {
        const scroller = this.scroller();
        if (scroller) scroller.scrollTop = 0;
        this.observeSentinels();
      });
    },
    closeEntity() {
      this.entityToken += 1;
      this.selected = null;
      const top = this._listScrollTop || 0;
      this._listScrollTop = undefined;
      this.$nextTick(() => {
        const scroller = this.scroller();
        if (scroller) scroller.scrollTop = top;
        this.observeSentinels();
      });
    },
    // 回到本页：作品集的进度可能变了（在整页详情里看过、标了已看）。对象页重取作品集与进度，列表重取已加载卡片的进度。
    refreshOnReturn() {
      if (this.isPeople) return;
      if (this.selected) this.loadCollection();
      else this.loadCardProgress(this.list.items.map(item => this.itemID(item)));
    },
    openVideo(id) {
      const videoID = Number(id || 0);
      if (videoID) this.$emit('open-video', videoID);
    },
    playNext() {
      if (this.nextRequest) this.$emit('play-video', { ...this.nextRequest });
    },
    // 「在管理中编辑」：去管理侧「人物与作品集」并定位到这个对象；target 为空时只切过去（空列表的下一步）。
    editInManage(target) {
      const payload = { kind: this.kind };
      if (target?.id) Object.assign(payload, { id: Number(target.id), name: this.selectedName || target.name || '' });
      this.$emit('edit-in-manage', payload);
    },

    // ===== 人物页 =====
    async loadPersonHero(token) {
      if (token !== this.entityToken || !this.selected) return;
      const id = this.selected.id;
      this.hero = { ...this.hero, loading: true, error: '' };
      try {
        const detail = await GetPersonDetail(id, 0, 1);
        if (token !== this.entityToken) return;
        this.hero = { loading: false, error: '', item: detail?.person || null };
      } catch (err) {
        if (token === this.entityToken) this.hero = { ...this.hero, loading: false, error: `读取人物失败：${errorText(err)}` };
      }
    },
    async loadWorks(token, { reset }) {
      if (token !== this.entityToken || !this.selected || this.works.loading) return;
      if (!reset && !this.works.cursor) return;
      const id = this.selected.id;
      const request = { filter: { person_ids: [id], collapse_versions: true, sort_mode: 'year_desc' }, limit: PERSON_WORKS_PAGE_SIZE };
      if (!reset) request.cursor = this.works.cursor;
      this.works = { ...this.works, loading: true, error: '' };
      try {
        const page = await SearchLibraryVideoPage(request);
        if (token !== this.entityToken) return;
        const videos = page?.videos || [];
        this.works = {
          loading: false,
          error: '',
          items: reset ? videos : [...this.works.items, ...videos],
          cursor: videos.length ? page?.next_cursor || null : null,
          groups: { ...(reset ? {} : this.works.groups), ...(page?.version_groups || {}) },
          loaded: true
        };
        this.$nextTick(() => this.observeSentinels());
      } catch (err) {
        if (token === this.entityToken) this.works = { ...this.works, loading: false, error: `读取作品失败：${errorText(err)}`, loaded: true };
      }
    },
    async loadImages(token, { reset }) {
      if (token !== this.entityToken || !this.selected || this.images.loading) return;
      if (!reset && !this.images.cursor) return;
      const id = this.selected.id;
      this.images = { ...this.images, loading: true, error: '' };
      try {
        const page = await GetPersonImages(id, reset ? 0 : this.images.cursor, PERSON_IMAGES_PAGE_SIZE);
        if (token !== this.entityToken) return;
        const incoming = page?.images || [];
        this.images = { loading: false, error: '', items: reset ? incoming : [...this.images.items, ...incoming], cursor: Number(page?.next_image_id || 0), loaded: true };
      } catch (err) {
        if (token === this.entityToken) this.images = { ...this.images, loading: false, error: `读取相关图片失败：${errorText(err)}`, loaded: true };
      }
    },
    // 海报卡下面一行：年份 · 版本数；离线的写明。看过 / 在看放在右上角徽标（版本组按整组算）。
    workSubtitle(video) {
      const parts = [];
      if (Number(video?.year) > 0) parts.push(`${video.year}`);
      const versions = Number(this.works.groups[video.id]?.member_count || 0);
      if (versions >= 2) parts.push(`${versions} 个版本`);
      if (video?.is_stale) parts.push('离线');
      return parts.join(' · ');
    },
    workBadge(video) {
      const members = this.works.groups[video.id]?.members;
      const state = watchStateOf(members?.length ? members : video);
      return state === 'unwatched' ? '' : STATE_LABELS[state];
    },

    // ===== 作品集页 =====
    // 作品集（成员与资料）与进度两路并行，各自出错；都回来后，若有版本组合成一部（在线成员多于总部数），再查版本组。
    async loadCollection() {
      const token = this.entityToken;
      if (!this.selected) return;
      await Promise.all([this.loadCollectionDetail(token), this.loadCollectionProgress(token)]);
      if (token === this.entityToken) await this.loadVersionGroups(token);
    },
    async loadCollectionDetail(token) {
      const id = this.selected.id;
      this.detail = { ...this.detail, loading: true, error: '' };
      try {
        const data = await GetCollectionDetail(id);
        if (token === this.entityToken) this.detail = { loading: false, error: '', data: data || null };
      } catch (err) {
        if (token === this.entityToken) this.detail = { ...this.detail, loading: false, error: `读取作品集失败：${errorText(err)}` };
      }
    },
    async loadCollectionProgress(token) {
      const id = this.selected.id;
      this.progress = { ...this.progress, loading: true, error: '' };
      try {
        const result = await ListCollectionProgress({ collection_ids: [id] });
        if (token === this.entityToken) this.progress = { loading: false, error: '', data: (result || []).find(item => Number(item.collection_id) === id) || null };
      } catch (err) {
        if (token === this.entityToken) this.progress = { ...this.progress, loading: false, error: `读取观看进度失败：${errorText(err)}` };
      }
    },
    async retryProgress() {
      const token = this.entityToken;
      await this.loadCollectionProgress(token);
      if (token === this.entityToken) await this.loadVersionGroups(token);
    },
    // 版本组：按成员的文件路径精确筛选并打开合并版本（与整页详情的「版本」同一做法），读出它所属的组与组内顺序。
    async loadVersionGroups(token) {
      const members = this.detail.data?.videos || [];
      if (!needsVersionGroups(members, this.progress.data)) {
        this.groups = { loading: false, error: '', map: {}, ready: true };
        return;
      }
      const online = members.map(member => member.video).filter(video => video && !video.is_stale && video.path);
      this.groups = { ...this.groups, loading: true, error: '' };
      try {
        const pages = await Promise.all(online.map(video => SearchLibraryVideoPage({ filter: { path_prefix: video.path, collapse_versions: true }, limit: 1 })));
        if (token !== this.entityToken) return;
        const map = {};
        online.forEach((video, index) => {
          const group = pages[index]?.version_groups?.[video.id];
          if (!group || Number(group.member_count) < 2) return;
          const member = (group.members || []).find(item => Number(item.video_id) === Number(video.id));
          map[Number(video.id)] = { groupID: Number(group.group_id), order: Number(member?.position || 0) };
        });
        this.groups = { loading: false, error: '', map, ready: true };
      } catch (err) {
        if (token === this.entityToken) this.groups = { ...this.groups, loading: false, error: `读取版本信息失败：${errorText(err)}` };
      }
    },
    workFacts(row) {
      const video = row.representative;
      const parts = [];
      if (Number(video?.year) > 0) parts.push(`${video.year}`);
      const duration = formatDuration(video?.duration);
      if (duration) parts.push(duration);
      if (row.videos.length >= 2) parts.push(`${row.videos.length} 个版本`);
      if (row.resumeVideo) parts.push(`看到 ${formatDuration(row.resumeVideo.watch_position_seconds)}`);
      if (row.offline) parts.push('文件当前不可用，不计入部数');
      return parts.join(' · ');
    }
  }
};
</script>

<style scoped>
.entity-browse { display: grid; gap: var(--space-5); align-content: start; padding-bottom: var(--space-7); }
.entity-browse__list { display: grid; gap: var(--space-4); align-content: start; }
.entity-browse__list :deep(.ui-page-header) { margin-bottom: 0; }
.entity-browse__nav { display: flex; align-items: center; gap: var(--space-3); }
.entity-browse__search { position: relative; display: flex; align-items: center; width: min(320px, 100%); color: var(--text-muted); }
.entity-browse__search .ui-icon { position: absolute; left: 10px; pointer-events: none; }
.entity-browse__search .search-input { padding-left: 32px; }
.entity-browse__muted { color: var(--text-muted); }
.entity-browse__sentinel { height: 1px; }
.entity-browse__more { justify-self: center; }
.recap-section__head h3 .ui-icon { color: var(--accent-text); }

/* 人物头像墙 */
.entity-browse__people { display: grid; grid-template-columns: repeat(auto-fill, minmax(128px, 1fr)); gap: var(--space-5) var(--space-4); }
.entity-browse__person { display: grid; justify-items: center; gap: var(--space-1); min-width: 0; padding: var(--space-2) 0; border-radius: var(--radius); color: var(--text-primary); text-align: center; cursor: pointer; }
.entity-browse__avatar {
  display: grid;
  place-items: center;
  width: 96px;
  height: 96px;
  margin-bottom: var(--space-1);
  overflow: hidden;
  border-radius: 50%;
  background: linear-gradient(135deg, var(--accent-color), var(--warning-color));
  color: var(--accent-on);
  box-shadow: 0 0 0 2px var(--accent-border);
  transition: transform var(--transition);
}
.entity-browse__person:hover .entity-browse__avatar { transform: scale(1.05); }
.entity-browse__avatar img { width: 100%; height: 100%; object-fit: cover; }
.entity-browse__avatar b { font-size: var(--fs-display); font-weight: var(--fw-bold); }
.entity-browse__person strong { max-width: 100%; overflow: hidden; font-size: var(--fs-secondary); font-weight: var(--fw-semibold); text-overflow: ellipsis; white-space: nowrap; }
.entity-browse__person small { color: var(--text-muted); font-size: var(--fs-caption); }
.entity-browse__skeleton-avatar { justify-items: center; }
.entity-browse__skeleton-avatar :deep(.ui-skeleton__media) { width: 96px; aspect-ratio: 1; border-radius: 50%; }

/* 作品集封面墙 */
.entity-browse__collections { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: var(--space-5) var(--space-4); }
.entity-browse__collection { display: grid; align-content: start; gap: var(--space-2); min-width: 0; color: var(--text-primary); text-align: left; cursor: pointer; }
.entity-browse__cover {
  display: grid;
  place-items: center;
  width: 100%;
  aspect-ratio: 2 / 3;
  overflow: hidden;
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--thumb-fallback-bg);
  color: var(--thumb-fg);
  transition: transform 260ms ease, box-shadow 260ms ease;
}
.entity-browse__collection:hover .entity-browse__cover { transform: translateY(-4px); box-shadow: var(--shadow-popover); }
.entity-browse__cover img { width: 100%; height: 100%; object-fit: cover; }
.entity-browse__collection strong { overflow: hidden; font-size: var(--fs-secondary); font-weight: var(--fw-semibold); text-overflow: ellipsis; white-space: nowrap; }
.entity-browse__card-progress { display: grid; gap: var(--space-1); min-height: 20px; }
.entity-browse__card-progress small { color: var(--text-secondary); font-size: var(--fs-caption); }

/* 对象页头部：左边头像或封面，右边 HeroHeader（文字层在压暗层上，两主题都是白字）。 */
.entity-browse__hero { display: grid; grid-template-columns: minmax(120px, 168px) minmax(0, 1fr); gap: var(--space-2) var(--space-6); align-items: end; padding: var(--space-6) 0 0 var(--space-7); }
.entity-browse__hero-body { padding-left: 0; }
.entity-browse__hero-avatar,
.entity-browse__hero-cover {
  display: grid;
  place-items: center;
  align-self: end;
  margin-bottom: var(--space-6);
  overflow: hidden;
  background: var(--player-bg);
  color: var(--recap-hero-muted);
  box-shadow: 0 18px 40px var(--recap-scrim);
}
.entity-browse__hero-avatar { aspect-ratio: 1; border-radius: 50%; border: 2px solid var(--recap-hero-chip-border); }
.entity-browse__hero-avatar b { color: var(--recap-hero-fg); font-size: var(--fs-hero); font-weight: var(--fw-bold); }
.entity-browse__hero-cover { aspect-ratio: 2 / 3; border: 1px solid var(--recap-hero-chip-border); border-radius: var(--radius-lg); }
.entity-browse__hero-avatar img,
.entity-browse__hero-cover img { width: 100%; height: 100%; object-fit: cover; }
.entity-browse__hero-progress { display: grid; gap: var(--space-2); max-width: 520px; }
.entity-browse__hero-progress p { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); }
.entity-browse__hero-bar :deep(.ui-progress__head) { color: var(--recap-hero-muted); }
.entity-browse__hero :deep(.ui-hero-header__meta) { display: -webkit-box; overflow: hidden; -webkit-line-clamp: 2; -webkit-box-orient: vertical; }

/* 人物作品海报墙 */
.entity-browse__posters { display: grid; grid-template-columns: repeat(auto-fill, minmax(132px, 1fr)); gap: var(--space-4) 14px; }
.entity-browse__strip-more { align-self: center; justify-self: start; }

/* 作品集各部：编号 · 缩略图 · 片名与事实 · 状态；下一部描主色边。 */
.entity-browse__works { display: grid; gap: var(--space-2); margin: 0; padding: 0; list-style: none; }
.entity-browse__work {
  display: grid;
  grid-template-columns: 32px 128px minmax(0, 1fr) auto;
  align-items: center;
  gap: var(--space-4);
  width: 100%;
  padding: var(--space-2) var(--space-4) var(--space-2) var(--space-3);
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--panel-bg);
  color: var(--text-primary);
  text-align: left;
  cursor: pointer;
}
.entity-browse__work:hover:not(:disabled) { border-color: var(--border-strong); background: var(--row-hover-bg); }
.entity-browse__work--next { border-color: var(--accent-border); background: var(--accent-soft); }
.entity-browse__work--offline { cursor: default; opacity: 0.6; }
.entity-browse__work-index { color: var(--text-muted); font-family: var(--font-mono); font-size: var(--fs-body); font-variant-numeric: tabular-nums; text-align: center; }
.entity-browse__work--next .entity-browse__work-index { color: var(--accent-text); font-weight: var(--fw-bold); }
.entity-browse__work-thumb { display: block; aspect-ratio: 16 / 9; overflow: hidden; border-radius: var(--radius-sm); background: var(--thumb-bg); }
.entity-browse__work-thumb img { display: block; width: 100%; height: 100%; object-fit: cover; }
.entity-browse__work-copy { display: grid; gap: var(--space-1); min-width: 0; }
.entity-browse__work-copy strong { overflow: hidden; font-size: var(--fs-body); font-weight: var(--fw-semibold); text-overflow: ellipsis; white-space: nowrap; }
.entity-browse__work-copy small { overflow: hidden; color: var(--text-muted); font-size: var(--fs-caption); text-overflow: ellipsis; white-space: nowrap; }
.entity-browse__work-badges { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: var(--space-2); }

@media (max-width: 900px) {
  .entity-browse__hero { grid-template-columns: minmax(0, 1fr); padding: var(--space-5) var(--space-5) 0; }
  .entity-browse__hero-avatar,
  .entity-browse__hero-cover { display: none; }
  .entity-browse__work { grid-template-columns: 28px minmax(0, 1fr) auto; }
  .entity-browse__work-thumb { display: none; }
}
@media (prefers-reduced-motion: reduce) {
  .entity-browse__avatar,
  .entity-browse__cover { transition: none; }
  .entity-browse__person:hover .entity-browse__avatar,
  .entity-browse__collection:hover .entity-browse__cover { transform: none; }
}
</style>
