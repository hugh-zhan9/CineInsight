<template>
  <section class="page-content viewer-detail" data-test="viewer-detail">
    <!-- 观看侧整页详情（D-UI-05，详细设计「整页详情」）：只读展示 + 常用动作；改资料去管理侧的抽屉。
         注释放在根元素里面：放在外面开发模式会变成多根片段，宿主的 v-show 与 $el 都会失效。 -->
    <nav class="viewer-detail__nav">
      <button type="button" class="btn-secondary btn-compact" data-test="detail-back" @click="$emit('back')"><RecapIcon name="arrow" class="viewer-detail__back-icon" /> 返回</button>
    </nav>

    <RecapBackdrop v-if="loading && !details" size="small" data-test="detail-loading">
      <div class="recap-hero__body"><p class="recap-hero__meta">正在读取视频详情…</p></div>
    </RecapBackdrop>
    <div v-else-if="error && !details" class="recap-panel viewer-detail__failure" role="alert" data-test="detail-error">
      <p>{{ error }}</p>
      <div class="recap-hero__actions">
        <button type="button" class="btn-secondary btn-compact" data-test="detail-retry" @click="load">重试</button>
        <button type="button" class="btn-secondary btn-compact" data-test="detail-go-library" @click="$emit('navigate', 'videos')">去片库</button>
      </div>
    </div>

    <template v-else-if="details">
      <!-- 背景区：背景图或画面帧轮播；「在应用内播放」时播放器就地展开在这里。 -->
      <RecapBackdrop :key="`detail-${video.id}`" :frames="backdrop.frames" :fallback="backdrop.fallback" data-test="detail-backdrop">
        <div :class="['viewer-detail__hero', { 'viewer-detail__hero--playing': playerOpen }]">
          <div v-if="playerOpen" class="viewer-detail__player" data-test="detail-player">
            <InlineVideoPlayer
              :ref="setPlayer"
              :video-id="Number(video.id)"
              :session="session"
              :start-time-ms="playerStart.startTimeMs"
              :resume-position-seconds="playerStart.resumePositionSeconds"
              :bookmark-range="bookmarkRange"
              :autoplay="true"
              :duration-seconds="Number(video.duration) || 0"
              @watch-progress="saveWatchProgress"
              @preview-externally="previewExternally"
              @preview-session-stale="reloadSession(true)"
              @bookmark-range-end="bookmarkRangeFinished = true"
            />
            <!-- 播放代理的提示与失败（抽屉放在技术信息区块，这里跟在播放器下面）；外部预览模式下可以直接生成代理。 -->
            <div v-if="proxyNotice || proxyError || canCreateProxy || bookmarkRangeFinished" class="viewer-detail__player-notes">
              <p v-if="proxyNotice" data-test="detail-proxy-notice">{{ proxyNotice }}</p>
              <p v-if="proxyError" class="viewer-detail__error" role="alert" data-test="detail-proxy-error">{{ proxyError }}</p>
              <button v-if="canCreateProxy" type="button" class="btn-secondary btn-compact" data-test="detail-proxy-create" :disabled="proxyBusy || proxyPending" @click="player.createPlaybackProxy()">{{ proxyCreateLabel }}</button>
              <span v-if="bookmarkRangeFinished" class="viewer-detail__range-end" data-test="detail-bookmark-range-end">
                已到书签区间末尾
                <button type="button" class="btn-secondary btn-compact" @click="continueBookmarkPlayback">继续整片</button>
              </span>
            </div>
          </div>
          <span v-else class="viewer-detail__poster">
            <img v-if="!posterFailed" :src="posterURL" alt="" data-test="detail-poster" @error="posterFailed = true" />
            <RecapIcon v-else name="plays" />
          </span>
          <div class="recap-hero__body viewer-detail__body">
            <h2 class="recap-hero__title" data-test="detail-title">{{ title }}</h2>
            <p v-if="originalTitle" class="recap-hero__meta" data-test="detail-original-title">{{ originalTitle }}</p>
            <p v-if="facts.length" class="recap-hero__meta viewer-detail__facts">
              <span v-for="fact in facts" :key="fact.key" :data-test="`detail-fact-${fact.key}`">{{ fact.text }}</span>
            </p>
            <div v-if="tags.length" class="viewer-detail__tags">
              <button
                v-for="tag in tags"
                :key="tag.id"
                type="button"
                class="tag-badge viewer-detail__tag"
                :style="{ backgroundColor: tagColor(tag.color) }"
                :title="`在片库里看「${tag.name}」`"
                :data-test="`detail-tag-${tag.id}`"
                @click="$emit('open-tag', Number(tag.id))"
              ><span class="tag-badge__name">{{ tag.name }}</span></button>
            </div>
            <!-- 动作：播放与抽屉「播放」同一调用（PlayVideo，记一次正式播放）；收藏、已看、评分点了即存。 -->
            <div class="recap-hero__actions" data-test="detail-actions">
              <button type="button" class="btn-primary" data-test="detail-play" :disabled="actionBusy === 'play'" @click="playExternally"><RecapIcon name="play" /> {{ actionBusy === 'play' ? '启动中…' : '播放' }}</button>
              <button type="button" class="btn-secondary" data-test="detail-inline-play" @click="toggleInlinePlayer">{{ playerOpen ? '收起播放器' : '在应用内播放' }}</button>
              <QueueActionButton :video-ids="[Number(video.id)]" label="加入待播" />
              <button type="button" :class="['btn-secondary', { 'viewer-detail__toggle--on': video.is_favorite }]" :aria-pressed="!!video.is_favorite" data-test="detail-favorite" :disabled="!!actionBusy" @click="toggleState('favorite')">{{ video.is_favorite ? '已收藏' : '收藏' }}</button>
              <button type="button" :class="['btn-secondary', { 'viewer-detail__toggle--on': video.is_watched }]" :aria-pressed="!!video.is_watched" data-test="detail-watched" :disabled="!!actionBusy" @click="toggleState('watched')">{{ video.is_watched ? '已看过' : '标记已看' }}</button>
            </div>
            <RatingStars :value="rating" :disabled="ratingSaving" @rate="saveRating" />
            <div class="recap-hero__actions viewer-detail__more">
              <button type="button" class="btn-secondary btn-compact" data-test="detail-quote-card" :disabled="quoteCardLoading" @click="openQuoteCard">台词卡</button>
              <button type="button" class="btn-secondary btn-compact" data-test="detail-find-similar" @click="findSimilarScenes">找相似画面</button>
              <button type="button" class="btn-secondary btn-compact" data-test="detail-edit" @click="$emit('edit-in-manage', Number(video.id))">编辑资料</button>
            </div>
            <p v-if="actionError" class="viewer-detail__error" role="alert" data-test="detail-action-error">{{ actionError }}</p>
          </div>
        </div>
      </RecapBackdrop>
      <section v-if="description" class="recap-section" data-test="detail-description">
        <header class="recap-section__head"><h3><RecapIcon name="diary" /> 简介</h3></header>
        <p class="viewer-detail__description">{{ description }}</p>
      </section>

      <section v-if="people.length" class="recap-section" data-test="detail-people">
        <header class="recap-section__head"><h3><RecapIcon name="person" /> 演职人员</h3><small>{{ people.length }} 人</small></header>
        <div class="recap-people">
          <button v-for="item in people" :key="item.person.id" type="button" class="recap-person" :data-test="`detail-person-${item.person.id}`" @click="openEntity('person', item.person.id, item.person.display_name)">
            <span class="recap-person__avatar"><img v-if="item.avatar_url" :src="item.avatar_url" alt="" loading="lazy" /><b v-else>{{ initial(item.person.display_name) }}</b></span>
            <strong>{{ item.person.display_name }}</strong><small>{{ item.active_video_count }} 部</small>
          </button>
        </div>
      </section>

      <!-- 片段书签沿用 BookmarkPanel（标题、搜索与分页都在它里面）；「记下当前位置」只在应用内播放时可用。 -->
      <section class="recap-section" data-test="detail-bookmarks">
        <BookmarkPanel :key="video.id" :video="video" :session="playerOpen ? session : null" :current-position="currentPositionSeconds" @open-bookmark="openBookmark" @open-video="openBookmarkVideo" />
      </section>

      <section v-if="blockVisible(relatedBlock)" class="recap-section" data-test="detail-related">
        <header class="recap-section__head"><h3><RecapIcon name="spark" /> 相关推荐</h3><small>同一批人物的其他作品</small></header>
        <p v-if="relatedBlock.error" class="viewer-detail__error" role="alert" data-test="detail-related-error">{{ relatedBlock.error }}</p>
        <p v-else-if="relatedBlock.loading && !relatedBlock.items.length" class="recap-muted">正在读取…</p>
        <div v-if="relatedBlock.items.length" class="recap-strip">
          <RecapMediaCard
            v-for="item in relatedBlock.items"
            :key="item.video.id"
            class="recap-strip__item"
            :id="Number(item.video.id)"
            :title="videoTitle(item.video)"
            :subtitle="item.reason"
            data-test="detail-related-item"
            @open="openVideo(item.video.id)"
          />
        </div>
      </section>
      <!-- 不在任何版本组里的片没有这一节；读取中也不先占一个空节。 -->
      <section v-if="versionBlock.error || versionMembers.length" class="recap-section" data-test="detail-versions">
        <header class="recap-section__head"><h3><RecapIcon name="videos" /> 版本</h3><small v-if="versionMembers.length">{{ versionMembers.length }} 个版本，第一个是主版本</small></header>
        <p v-if="versionBlock.error" class="viewer-detail__error" role="alert" data-test="detail-versions-error">{{ versionBlock.error }}</p>
        <div v-if="versionMembers.length" class="viewer-detail__rows">
          <article v-for="member in versionMembers" :key="member.video_id" class="recap-panel viewer-detail__row" :data-test="`detail-version-${member.video_id}`">
            <div class="viewer-detail__row-copy">
              <strong>{{ versionTitle(member) }}</strong>
              <small>{{ versionFacts(member) }}</small>
            </div>
            <span v-if="Number(member.video_id) === Number(video.id)" class="viewer-detail__current">当前</span>
            <template v-else>
              <button type="button" class="btn-secondary btn-compact" :data-test="`detail-version-compare-${member.video_id}`" @click="compareVersion(member.video_id)">对比画质</button>
              <button type="button" class="btn-secondary btn-compact" :data-test="`detail-version-open-${member.video_id}`" :disabled="member.is_stale" @click="openVideo(member.video_id)">打开</button>
            </template>
          </article>
        </div>
      </section>

      <section v-if="collectionItems.length" class="recap-section" data-test="detail-collections">
        <header class="recap-section__head"><h3><RecapIcon name="collection" /> 所属作品集</h3></header>
        <div class="viewer-detail__rows">
          <article v-for="item in collectionItems" :key="item.id" class="recap-panel viewer-detail__collection" :data-test="`detail-collection-${item.id}`">
            <div class="viewer-detail__row-copy">
              <button type="button" class="viewer-detail__link" :data-test="`detail-collection-open-${item.id}`" @click="openEntity('collection', item.id, item.name)">{{ item.name }}</button>
              <small v-if="item.state === 'loading'">正在读取…</small>
              <small v-else-if="item.state === 'error'" class="viewer-detail__error" role="alert">{{ item.error }}</small>
              <small v-else-if="item.position" :data-test="`detail-collection-position-${item.id}`">第 {{ item.position }} 部 · 共 {{ item.total }} 部{{ item.next ? '' : ' · 已是最后一部' }}</small>
              <small v-else>共 {{ item.total }} 部</small>
            </div>
            <RecapMediaCard
              v-if="item.next"
              :id="Number(item.next.id)"
              :title="videoTitle(item.next)"
              subtitle="下一部"
              :data-test="`detail-collection-next-${item.id}`"
              @open="openVideo(item.next.id)"
            />
          </article>
        </div>
      </section>

      <section v-if="record.length" class="recap-section" data-test="detail-history">
        <header class="recap-section__head"><h3><RecapIcon name="clock" /> 看过的记录</h3></header>
        <div class="viewer-detail__stats">
          <StatTile v-for="tile in record" :key="tile.key" :value="tile.value" :unit="tile.unit" :label="tile.label" :data-test="`detail-history-${tile.key}`">
            <template #icon><RecapIcon :name="tile.icon" /></template>
          </StatTile>
        </div>
      </section>
    </template>
    <QuoteCardDialog v-if="quoteCard" v-bind="quoteCard" @close="quoteCard = null" />
  </section>
</template>

<script>
// 观看侧整页详情（D-UI-05、D-UI-06、D-UI-08；详细设计「整页详情」）。
// 数据：GetVideoDetails 一次取齐（含 artwork.has_poster/has_fanart、人物、作品集、标签）；
//   相关推荐 = 本片人物在抽屉人物详情里的作品（GetPersonDetail）；版本 = 片库页同一份 version_groups 汇总
//   （SearchLibraryVideoPage 按本片路径精确筛选并打开合并版本）；作品集的下一部 = GetCollectionDetail 的成员顺序；
//   书签沿用 BookmarkPanel。各块各自加载、各自失败，没有内容的块不显示。
// 内嵌播放：InlineVideoPlayer 受控模式（会话归本页，BookmarkPanel 记位置也要它）；带 startAtMs 进来时自动在应用内起播。
// 返回、相关推荐、标签、人物与作品集都只发事件，页面切换与返回栈归 App（D-UI-07、D-UI-10）。
import {
  GetCollectionDetail, GetPersonDetail, GetPreviewSession, GetSubtitleSegments, GetVideoDetails, PlayVideo, PreviewExternally,
  SearchLibraryVideoPage, SetVideoFavorite, SetVideoWatched, UpdateVideoRating, UpdateVideoWatchProgress
} from '../../../wailsjs/go/main/App';
import BookmarkPanel from '../BookmarkPanel.vue';
import QueueActionButton from '../QueueActionButton.vue';
import QuoteCardDialog from '../QuoteCardDialog.vue';
import InlineVideoPlayer from '../player/InlineVideoPlayer.vue';
import RecapBackdrop from '../recap/RecapBackdrop.vue';
import RecapIcon from '../recap/RecapIcon.vue';
import RecapMediaCard from '../recap/RecapMediaCard.vue';
import StatTile from '../ui/StatTile.vue';
import RatingStars from './RatingStars.vue';
import { translateBackendError } from '../../utils/feedback.js';
import { subtitleCueAt } from '../../utils/quoteCard.js';
import { videoArtwork } from '../../utils/recap.js';
import { tagBackgroundColor } from '../../utils/tagColor.js';
import { openVersionCompare } from '../../utils/versionCompare.js';
import { versionMemberFacts, versionMemberTitle } from '../../utils/versionGroups.js';
import { resumePosition } from '../../utils/watchState.js';
import {
  COLLECTION_DETAIL_LIMIT, RELATED_PEOPLE_LIMIT, RELATED_PER_PERSON, collectionProgress, detailBackdrop, detailFacts, detailTitle,
  mergeRelatedVideos, ratingOf, viewingRecord
} from './videoDetail.js';

const STATE_TOGGLES = {
  favorite: { field: 'is_favorite', call: (id, value) => SetVideoFavorite(id, value), failure: '更新收藏状态失败' },
  watched: { field: 'is_watched', call: (id, value) => SetVideoWatched(id, value), failure: '更新观看状态失败' }
};
const WATCH_PROGRESS_ORIGINS = ['resume', 'start', 'jump'];
const emptyBlock = () => ({ loading: false, error: '', items: [] });

function errorText(err) {
  return translateBackendError(err?.message || err);
}

export default {
  name: 'VideoDetailPage',
  components: { BookmarkPanel, InlineVideoPlayer, QueueActionButton, QuoteCardDialog, RatingStars, RecapBackdrop, RecapIcon, RecapMediaCard, StatTile },
  props: {
    // 要展示的视频 ID 与可选的起播位置（毫秒，场景检索 / 书签定位进来时有）。
    videoId: { type: Number, default: 0 },
    startAtMs: { type: Number, default: null }
  },
  // back：返回来源页；open-video(id)：相关推荐、下一部、其他版本，宿主压栈后换成那一部；
  // navigate(pageKey)：去别的页面；edit-in-manage(id)：去片库管理并在抽屉里打开这部片；
  // open-tag(tagId)：去视频页按这个标签筛选；open-entity({ type: person|collection, id, name })：去人物 / 作品集页
  // （与回顾页同一载荷）；find-similar({ videoID, atMS, title })：去场景检索找相似画面（与抽屉的 find-similar-scenes 同一载荷）。
  emits: ['back', 'open-video', 'navigate', 'edit-in-manage', 'open-tag', 'open-entity', 'find-similar'],
  data() {
    return {
      loading: false, error: '', details: null, posterFailed: false,
      relatedBlock: emptyBlock(), versionBlock: { loading: false, error: '', group: null }, collectionStates: {},
      // 内嵌播放：会话归本页（受控）；playerStart 在每次展开时定下来，播放中不再变。
      playerOpen: false, session: null, player: null,
      playerStart: { startTimeMs: null, resumePositionSeconds: 0 },
      bookmarkRange: null, bookmarkRangeFinished: false,
      actionBusy: '', actionError: '', ratingSaving: false,
      quoteCard: null, quoteCardLoading: false
    };
  },
  computed: {
    video() { return this.details?.video || null; },
    title() { return detailTitle(this.details); },
    originalTitle() {
      const original = String(this.video?.original_title || '').trim();
      return original && original !== this.title ? original : '';
    },
    description() { return String(this.video?.description || '').trim(); },
    backdrop() { return detailBackdrop(this.details); },
    // 海报找不到时服务端回落缩略图（D-UI-12），所以总是请求海报。
    posterURL() { return videoArtwork(Number(this.video?.id || 0), 'poster'); },
    versionMembers() { return this.versionBlock.group?.members || []; },
    facts() { return detailFacts(this.video, this.versionMembers.length); },
    tags() { return (this.video?.tags || []).filter(tag => tag?.id); },
    rating() { return ratingOf(this.video); },
    people() { return this.details?.people || []; },
    record() { return viewingRecord(this.video); },
    collectionItems() {
      return (this.details?.collections || []).map(item => {
        const id = Number(item.collection?.id);
        const state = this.collectionStates[id] || {};
        return {
          id, name: item.collection?.name || '未命名作品集',
          state: state.state || 'ready', error: state.error || '',
          total: state.total ?? Number(item.active_video_count || 0), position: state.position || 0, next: state.next || null
        };
      });
    },
    // 播放代理的状态归播放器（D-UI-11），这里只转发来显示；播放器挂上之前给出与它初始状态相同的值。
    proxyBusy() { return !!this.player?.proxyBusy; },
    proxyPending() { return !!this.player?.proxyPending; },
    proxyError() { return this.player?.proxyError || ''; },
    proxyNotice() { return this.player?.proxyNotice || ''; },
    proxyCreateLabel() { return this.player?.proxyCreateLabel ?? '生成播放代理'; },
    // 外部预览模式（容器内嵌不了）下给出「生成播放代理」；内嵌报错时播放器自己的报错框里已经有这个按钮。
    canCreateProxy() { return Boolean(this.player) && this.session?.mode === 'external-preview'; }
  },
  watch: {
    videoId() { this.scrollToTop(); this.load(); },
    // 同一部片又带着新的起播位置进来（再次从场景检索命中）：在应用内从新位置起播。
    startAtMs(value) {
      if (value !== null && value !== undefined && this.details && Number(this.video?.id) === Number(this.videoId)) this.openPlayer({ startTimeMs: value });
    }
  },
  mounted() {
    this.scrollToTop();
    this.load();
  },
  beforeUnmount() {
    this._loadToken = (this._loadToken || 0) + 1;
  },
  methods: {
    tagColor: tagBackgroundColor,
    versionFacts: versionMemberFacts,
    videoTitle(video) { return String(video?.display_title || video?.name || '未命名视频'); },
    versionTitle(member) {
      const label = String(member?.label || '').trim();
      const title = versionMemberTitle(member);
      return label ? `${label} · ${title}` : title;
    },
    initial(name) { return String(name || '?').trim().slice(0, 1) || '?'; },
    blockVisible(block, items = block.items) { return Boolean(block.loading || block.error || items?.length); },
    isCurrentLoad(token) { return token === this._loadToken; },
    // 换一部片时回到顶部；来源页的滚动位置由 App 记、App 还原（D-UI-10）。滚动容器是顶栏下方唯一的 .main-view。
    scrollToTop() {
      const owner = this.$el?.closest?.('.main-view');
      if (owner) owner.scrollTop = 0;
    },
    // ===== 取数 =====
    // 详情一次取齐；其余各块拿到详情后并行取，晚到的旧响应（已经换了片）丢掉。
    async load() {
      const id = Number(this.videoId);
      const token = (this._loadToken = (this._loadToken || 0) + 1);
      this.closePlayer();
      Object.assign(this, {
        details: null, error: '', posterFailed: false, actionBusy: '', actionError: '', ratingSaving: false, quoteCard: null,
        relatedBlock: emptyBlock(), versionBlock: { loading: false, error: '', group: null }, collectionStates: {}
      });
      if (!id) { this.loading = false; return; }
      this.loading = true;
      let details = null;
      try {
        details = await GetVideoDetails(id);
      } catch (err) {
        if (this.isCurrentLoad(token)) { this.loading = false; this.error = `读取视频详情失败：${errorText(err)}`; }
        return;
      }
      if (!this.isCurrentLoad(token)) return;
      this.loading = false;
      this.details = details;
      this.loadRelated(details, token);
      this.loadVersions(details, token);
      this.loadCollections(details, token);
      // 场景检索、书签定位带着时刻进来：直接在应用内从那一刻起播。
      if (this.startAtMs !== null && this.startAtMs !== undefined) this.openPlayer({ startTimeMs: this.startAtMs });
    },
    async loadRelated(details, token) {
      const people = (details.people || []).slice(0, RELATED_PEOPLE_LIMIT);
      if (!people.length) return;
      this.relatedBlock = { loading: true, error: '', items: [] };
      const results = await Promise.allSettled(people.map(item => GetPersonDetail(Number(item.person.id), 0, RELATED_PER_PERSON)));
      if (!this.isCurrentLoad(token)) return;
      const groups = results.map((result, index) => (result.status === 'fulfilled'
        ? { name: people[index].person.display_name, videos: result.value?.videos || [] } : null)).filter(Boolean);
      const failed = results.find(result => result.status === 'rejected');
      this.relatedBlock = {
        loading: false,
        error: failed ? `读取相关推荐失败：${errorText(failed.reason)}` : '',
        items: mergeRelatedVideos(groups, details.video.id)
      };
    },
    // 版本组汇总与片库页同一份（version_groups）：按本片路径精确筛选（path_prefix 等于文件路径只命中它自己）
    // 并打开合并版本，本片就是这一页唯一的代表，汇总里带全部活跃成员。不在组里时没有这一节。
    async loadVersions(details, token) {
      const path = String(details.video.path || '');
      if (!path) return;
      this.versionBlock = { loading: true, error: '', group: null };
      try {
        const page = await SearchLibraryVideoPage({ filter: { path_prefix: path, collapse_versions: true }, limit: 1 });
        if (!this.isCurrentLoad(token)) return;
        const group = page?.version_groups?.[String(details.video.id)] || null;
        this.versionBlock = { loading: false, error: '', group: Number(group?.member_count) >= 2 ? group : null };
      } catch (err) {
        if (this.isCurrentLoad(token)) this.versionBlock = { loading: false, error: `读取版本失败：${errorText(err)}`, group: null };
      }
    },
    // 所属作品集：名字来自详情，第几部与下一部来自各自的成员顺序（只读前几个作品集）。
    loadCollections(details, token) {
      const ids = (details.collections || []).map(item => Number(item.collection?.id)).filter(Boolean).slice(0, COLLECTION_DETAIL_LIMIT);
      this.collectionStates = Object.fromEntries(ids.map(id => [id, { state: 'loading' }]));
      return Promise.all(ids.map(async id => {
        let patch;
        try {
          patch = { state: 'ready', ...collectionProgress(await GetCollectionDetail(id), details.video.id) };
        } catch (err) {
          patch = { state: 'error', error: `读取作品集失败：${errorText(err)}` };
        }
        if (this.isCurrentLoad(token)) this.collectionStates = { ...this.collectionStates, [id]: patch };
      }));
    },
    // ===== 内嵌播放（InlineVideoPlayer，D-UI-11）=====
    setPlayer(player) { this.player = player; },
    // 展开播放器：起播参数这一刻定下来。没指定时刻时按断点续播（与抽屉同一个 resumePosition）。
    openPlayer({ startTimeMs = null, bookmarkRange = null } = {}) {
      if (!this.video) return;
      this.playerStart = {
        startTimeMs: startTimeMs === null || startTimeMs === undefined ? null : Number(startTimeMs),
        resumePositionSeconds: resumePosition(this.video)
      };
      this.bookmarkRange = bookmarkRange;
      this.bookmarkRangeFinished = false;
      if (this.playerOpen) return;
      this.playerOpen = true;
      this.reloadSession(false);
    },
    closePlayer() {
      this._sessionToken = (this._sessionToken || 0) + 1;
      this.playerOpen = false; this.session = null; this.player = null;
      this.bookmarkRange = null; this.bookmarkRangeFinished = false;
    },
    toggleInlinePlayer() {
      if (this.playerOpen) this.closePlayer();
      else this.openPlayer();
    },
    // 会话归本页：第一次取不到时给出与播放器自取时同样的失败说明；代理增删后的重取失败保留原会话，只提示。
    async reloadSession(refresh) {
      const videoID = Number(this.video?.id || 0);
      const token = (this._sessionToken = (this._sessionToken || 0) + 1);
      if (!videoID) return;
      if (!refresh) this.session = null;
      try {
        const session = await GetPreviewSession(videoID);
        if (token === this._sessionToken) this.session = session;
      } catch (err) {
        if (token !== this._sessionToken) return;
        if (refresh) this.actionError = '刷新预览会话失败，请稍后重试。';
        else this.session = { video_id: videoID, mode: 'unsupported', reason_code: 'preview_failed', reason_message: '准备预览失败，请稍后重试。' };
      }
    },
    // 进度上报（D-PC42 的 5 参契约）：时长只在播放器读得到时带上，origin 用播放器给的起播来源。按顺序串行写。
    saveWatchProgress(progress) {
      const videoID = Number(progress?.videoID || 0);
      if (!videoID) return;
      const origin = WATCH_PROGRESS_ORIGINS.includes(progress?.origin) ? progress.origin : 'resume';
      const reported = Number(progress?.durationSeconds);
      const duration = Number.isFinite(reported) && reported > 0 ? reported : 0;
      const save = async () => {
        const updated = await UpdateVideoWatchProgress(videoID, Number(progress?.positionSeconds || 0), duration, !!progress?.completed, origin);
        this.mergeVideo(videoID, updated);
      };
      this._progressChain = (this._progressChain || Promise.resolve()).then(save)
        .catch(err => { this.actionError = `保存观看进度失败：${errorText(err)}`; });
      return this._progressChain;
    },
    async previewExternally() {
      const videoID = Number(this.video?.id || 0);
      if (!videoID) return;
      try { await PreviewExternally(videoID); } catch (err) { this.actionError = `外部预览失败：${errorText(err)}`; }
    },
    // 书签「定位 / 核对原片」：在应用内从书签起点播，播到区间末尾暂停（D-UI-11 的书签区间）。
    openBookmark(result) {
      if (result?.status !== 'ready' || Number(result.video?.id) !== Number(this.video?.id)) return;
      this._bookmarkSeq = (this._bookmarkSeq || 0) + 1;
      const range = { videoID: Number(result.video.id), startMS: result.bookmark.start_ms, endMS: result.bookmark.end_ms, sourceToken: result.source_token, requestID: `detail:${this._bookmarkSeq}` };
      this.openPlayer({ startTimeMs: range.startMS, bookmarkRange: range });
    },
    // 书签编辑里「打开当前原片核对」：是这一部就重开内嵌播放（重取会话），别的片交给宿主。
    openBookmarkVideo(video) {
      const id = Number(video?.id || 0);
      if (!id) return;
      if (id !== Number(this.video?.id)) { this.openVideo(id); return; }
      this.closePlayer();
      this.openPlayer();
    },
    continueBookmarkPlayback() {
      this.bookmarkRangeFinished = false;
      Promise.resolve(this.player?.play()).catch(err => { this.actionError = `继续播放失败：${errorText(err)}`; });
    },
    // 当前位置（秒）：播放器开着读播放器，否则用进来时指定的时刻或续播断点（抽屉里播放器定位后的同一个位置）。
    currentPositionSeconds() {
      if (this.playerOpen && this.player) return Number(this.player.currentTime() || 0);
      if (this.startAtMs !== null && this.startAtMs !== undefined) return Number(this.startAtMs) / 1000;
      return resumePosition(this.video);
    },
    // 动作接口回来的整条视频写回详情；状态接口不带标签时沿用已有的，别用 null 盖掉。
    mergeVideo(videoID, updated) {
      if (!updated || !this.details || Number(this.video?.id) !== Number(videoID)) return;
      const video = { ...this.details.video, ...updated };
      if (!Array.isArray(updated.tags)) video.tags = this.details.video.tags;
      this.details = { ...this.details, video };
    },
    // ===== 动作 =====
    // 播放走正式播放（PlayVideo），与抽屉动作条「播放」同一调用；失败原因就地显示（后端文案不含路径）。
    async playExternally() {
      const videoID = Number(this.video?.id || 0);
      if (!videoID || this.actionBusy) return;
      const token = this._loadToken;
      this.actionBusy = 'play'; this.actionError = '';
      try {
        const result = await PlayVideo(videoID);
        if (!this.isCurrentLoad(token)) return;
        if (result && result.dispatch_succeeded === false) this.actionError = result.user_message || '播放失败';
        else this.mergeVideo(videoID, result?.video);
      } catch (err) {
        if (this.isCurrentLoad(token)) this.actionError = `播放失败：${errorText(err)}`;
      } finally {
        if (this.actionBusy === 'play') this.actionBusy = '';
      }
    },
    async toggleState(kind) {
      const toggle = STATE_TOGGLES[kind];
      const videoID = Number(this.video?.id || 0);
      if (!toggle || !videoID || this.actionBusy) return;
      const token = this._loadToken;
      const next = !this.video[toggle.field];
      this.actionBusy = kind; this.actionError = '';
      try {
        const updated = await toggle.call(videoID, next);
        if (this.isCurrentLoad(token)) this.mergeVideo(videoID, updated || { [toggle.field]: next });
      } catch (err) {
        if (this.isCurrentLoad(token)) this.actionError = `${toggle.failure}：${errorText(err)}`;
      } finally {
        if (this.actionBusy === kind) this.actionBusy = '';
      }
    },
    // 评分点一下即保存（UpdateVideoRating），null 表示清除；与抽屉的星级同一条路。
    async saveRating(value) {
      const videoID = Number(this.video?.id || 0);
      if (!videoID || this.ratingSaving || value === this.rating) return;
      const token = this._loadToken;
      this.ratingSaving = true; this.actionError = '';
      try {
        const updated = await UpdateVideoRating(videoID, value);
        if (this.isCurrentLoad(token)) this.mergeVideo(videoID, { personal_rating: updated?.personal_rating ?? null });
      } catch (err) {
        if (this.isCurrentLoad(token)) this.actionError = `保存评分失败：${errorText(err)}`;
      } finally {
        if (this.isCurrentLoad(token)) this.ratingSaving = false;
      }
    },
    // 台词卡：当前位置的画面 + 同名 .srt 里这一刻的字幕（与抽屉同一入口）；读不到字幕照样打开，台词留空。
    async openQuoteCard() {
      const videoID = Number(this.video?.id || 0);
      if (!videoID || this.quoteCardLoading) return;
      const atMS = Math.round(this.currentPositionSeconds() * 1000);
      let quote = '';
      this.quoteCardLoading = true;
      try {
        quote = subtitleCueAt(await GetSubtitleSegments(videoID), atMS);
      } catch (_err) {
        quote = '';
      } finally {
        this.quoteCardLoading = false;
      }
      if (Number(this.video?.id) !== videoID) return;
      this.quoteCard = { videoID, atMS, quote, title: this.title };
    },
    findSimilarScenes() {
      const videoID = Number(this.video?.id || 0);
      if (!videoID) return;
      this.$emit('find-similar', { videoID, atMS: Math.round(this.currentPositionSeconds() * 1000), title: this.title });
    },
    // 「对比画质」：本片在左、所选版本在右，交给全局的版本对比播放器。
    compareVersion(videoID) { return openVersionCompare(Number(this.video?.id), Number(videoID)); },
    openVideo(id) {
      const videoID = Number(id || 0);
      if (videoID && videoID !== Number(this.video?.id)) this.$emit('open-video', videoID);
    },
    openEntity(type, id, name) {
      if (Number(id)) this.$emit('open-entity', { type, id: Number(id), name: name || '' });
    }
  }
};
</script>

<style scoped>
.viewer-detail {
  display: grid;
  gap: 26px;
  align-content: start;
  padding-bottom: 56px;
}
.viewer-detail__nav { display: flex; align-items: center; gap: 12px; }
.viewer-detail__back-icon { transform: rotate(180deg); }
.viewer-detail__failure p { margin: 0; color: var(--danger-text); }
.viewer-detail__error { margin: 0; color: var(--danger-text); font-size: 13px; }
.viewer-detail__hero { display: grid; grid-template-columns: minmax(150px, 220px) minmax(0, 1fr); gap: 8px 28px; align-items: end; padding: 32px 0 0 40px; }
.viewer-detail__hero--playing { grid-template-columns: minmax(0, 1fr); padding: 24px 24px 0; }
.viewer-detail__poster {
  display: grid;
  place-items: center;
  align-self: end;
  aspect-ratio: 2 / 3;
  margin-bottom: 32px;
  overflow: hidden;
  border-radius: var(--radius-lg);
  background: var(--player-bg);
  color: var(--recap-hero-muted);
  font-size: 32px;
  box-shadow: 0 18px 40px var(--recap-scrim);
}
.viewer-detail__poster img { width: 100%; height: 100%; object-fit: cover; }
.viewer-detail__body { padding-left: 0; }
.viewer-detail__hero--playing .viewer-detail__body { padding: 8px 16px 28px; max-width: none; }
.viewer-detail__player { display: grid; gap: 10px; width: 100%; max-width: 1100px; justify-self: center; }
/* 播放器的占位与报错框原本放在浅色抽屉里：落在深色背景区上时给它一层面板底色，两种主题下都看得清。 */
.viewer-detail__player :deep(.preview-drawer__placeholder) { border-color: var(--hairline); background: var(--panel-bg); color: var(--text-secondary); }
.viewer-detail__player-notes { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; color: var(--recap-hero-muted); font-size: 12.5px; }
.viewer-detail__player-notes p { margin: 0; }
.viewer-detail__range-end { display: inline-flex; align-items: center; gap: 8px; }
.viewer-detail__facts { display: flex; flex-wrap: wrap; gap: 4px 14px; font-variant-numeric: tabular-nums; }
.viewer-detail__tags { display: flex; flex-wrap: wrap; gap: 6px; }
.viewer-detail__tag { color: var(--recap-hero-fg); font-family: inherit; cursor: pointer; }
.viewer-detail__tag:hover { border-color: var(--recap-hero-muted); }
.viewer-detail__toggle--on { border-color: var(--accent-color); color: var(--accent-text); }
.viewer-detail__more { margin-top: 0; }
.viewer-detail__description { max-width: 860px; margin: 0; color: var(--text-secondary); line-height: 1.75; white-space: pre-line; }
.viewer-detail__rows { display: grid; gap: 10px; }
.viewer-detail__row { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; padding: 12px 16px; }
.viewer-detail__row-copy { display: grid; flex: 1; gap: 4px; min-width: 0; }
.viewer-detail__row-copy strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.viewer-detail__row-copy small { color: var(--text-muted); font-size: 12px; }
.viewer-detail__current { color: var(--accent-text); font-size: 12px; font-weight: 650; }
.viewer-detail__collection { display: grid; grid-template-columns: minmax(0, 1fr) minmax(160px, 200px); align-items: center; gap: 16px; padding: 14px 18px; }
.viewer-detail__link { justify-self: start; padding: 0; border: 0; background: transparent; color: var(--text-primary); font: inherit; font-weight: 700; cursor: pointer; }
.viewer-detail__link:hover { color: var(--accent-text); }
.viewer-detail__stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 14px; }
@media (max-width: 900px) {
  .viewer-detail__hero { grid-template-columns: minmax(0, 1fr); padding: 24px 24px 0; }
  .viewer-detail__poster { display: none; }
  .viewer-detail__collection { grid-template-columns: minmax(0, 1fr); }
}
</style>
