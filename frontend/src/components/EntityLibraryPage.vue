<template>
  <main :class="['entity-library', { 'entity-library--with-drawer': drawerEntity }]">
    <section class="entity-library__toolbar glass-surface">
      <div class="entity-library__title">
        <button v-if="selectedEntity" type="button" class="btn-secondary btn-compact" @click="closeEntity">返回{{ isPeople ? '人物' : '作品集' }}列表</button>
        <div>
          <h2>{{ selectedEntity ? selectedEntityName : (isPeople ? '人物' : '作品集') }}</h2>
          <p v-if="selectedEntity">{{ entityVideos.length }} / {{ selectedEntityVideoCount }} 部已加载，可直接预览、播放或打开目录。</p>
          <p v-else>{{ isPeople ? '本地维护的演员实体；同名人物会保持独立。' : '手工编排的视频集合；一个视频可以属于多个作品集。' }}</p>
        </div>
      </div>
      <div v-if="selectedEntity" class="entity-library__search">
        <button type="button" class="btn-primary" @click="drawerEntity = { ...selectedEntity }">编辑与批量关联</button>
      </div>
      <div v-else class="entity-library__search">
        <input v-model="keyword" :placeholder="isPeople ? '搜索显示或原始姓名' : '搜索名称或简介'" @keyup.enter="reload" />
        <button type="button" class="btn-secondary" @click="reload">搜索</button>
      </div>
    </section>

    <section v-if="!selectedEntity" class="entity-library__create glass-surface">
      <input v-model="createForm.name" :placeholder="isPeople ? '显示姓名' : '作品集名称'" maxlength="200" />
      <input v-model="createForm.secondary" :placeholder="isPeople ? '原始姓名（可空）' : '简介（可空）'" :maxlength="isPeople ? 200 : 4000" />
      <button type="button" class="btn-primary" :disabled="creating" @click="createEntity">{{ creating ? '创建中...' : (isPeople ? '明确新建人物' : '新建作品集') }}</button>
    </section>

    <p v-if="error" class="entity-library__error" role="alert">{{ error }}</p>

    <template v-if="!selectedEntity">
      <section class="entity-library__grid">
        <button v-for="item in items" :key="entityID(item)" type="button" class="entity-card glass-surface" @click="openEntity(item)">
          <img v-if="assetURL(item)" :src="assetURL(item)" alt="" />
          <div v-else class="entity-card__placeholder">{{ isPeople ? '人物' : '作品集' }}</div>
          <div>
            <strong>{{ entityName(item) }}</strong>
            <span>{{ entitySecondary(item) }}</span>
            <small>{{ item.active_video_count || 0 }} 部活跃作品</small>
          </div>
        </button>
      </section>
      <div v-if="!loading && !items.length" class="empty-state">暂无{{ isPeople ? '人物' : '作品集' }}。</div>
    </template>

    <template v-else>
      <section v-if="entityVideos.length" class="entity-video-grid" aria-label="相关视频">
        <article v-for="video in entityVideos" :key="video.id" class="entity-video-card glass-surface">
          <button type="button" class="entity-video-card__preview" @click="openVideoDetails(video)">
            <img :src="`/preview/thumbnail/${video.id}`" :alt="`${video.name} 缩略图`" loading="lazy" />
          </button>
          <div class="entity-video-card__copy">
            <strong :title="video.display_title || video.name">{{ video.display_title || video.name }}</strong>
            <span :title="video.name">{{ video.name }}</span>
            <small>{{ formatBytes(video.size) }} · {{ formatDuration(video.duration) }}</small>
          </div>
          <div class="entity-video-card__actions">
            <button type="button" class="btn-secondary btn-compact" @click="openVideoDetails(video)">预览</button>
            <button type="button" class="btn-primary btn-compact" :disabled="playingVideoIDs.includes(Number(video.id))" @click="playEntityVideo(video)">{{ playingVideoIDs.includes(Number(video.id)) ? '启动中...' : '播放' }}</button>
            <button type="button" class="btn-secondary btn-compact" @click="openVideoDirectory(video)">目录</button>
          </div>
        </article>
      </section>
      <div v-if="!entityVideosLoading && !entityVideos.length" class="empty-state">当前还没有关联视频。点击“编辑与批量关联”可按文件夹筛选并多选加入。</div>
    </template>

    <div ref="loadSentinel" class="entity-library__sentinel" aria-hidden="true"></div>
    <button
      v-if="selectedEntity ? entityVideosHasMore : hasMore"
      type="button"
      class="btn-secondary entity-library__more"
      :disabled="selectedEntity ? entityVideosLoading : loading"
      @click="selectedEntity ? loadEntityVideos(false) : loadMore()"
    >
      {{ (selectedEntity ? entityVideosLoading : loading) ? '加载中...' : (selectedEntity ? '加载更多相关视频' : `加载更多${isPeople ? '人物' : '作品集'}`) }}
    </button>

    <PreviewDrawer
      v-if="drawerEntity"
      :initial-entity="drawerEntity"
      @close="drawerEntity = null"
      @preview-externally="previewExternally"
      @watch-progress="handleWatchProgress"
      @collection-deleted="handleCollectionDeleted"
      @person-deleted="handlePersonDeleted"
      @relations-updated="handleRelationsUpdated"
    />
  </main>
</template>

<script>
import {
  CreateCollection, CreatePerson, GetCollectionDetail, GetPersonDetail, ListCollections, ListPeople, OpenDirectory, PlayVideo,
  PreviewExternally, UpdateVideoWatchProgress
} from '../../wailsjs/go/main/App';
import PreviewDrawer from './PreviewDrawer.vue';
import { formatBytes, formatDuration } from '../utils/mediaDetails.js';

export default {
  name: 'EntityLibraryPage',
  components: { PreviewDrawer },
  props: { entityType: { type: String, required: true } },
  data() {
    return {
      items: [], keyword: '', cursorName: '', cursorID: 0, hasMore: true, loading: false, creating: false, error: '',
      createForm: { name: '', secondary: '' }, selectedEntity: null, selectedItem: null, drawerEntity: null,
      entityVideos: [], entityVideosHasMore: false, entityVideosLoading: false, entityVideoCursor: 0, collectionVideoPool: [],
      playingVideoIDs: []
    };
  },
  computed: {
    isPeople() { return this.entityType === 'person'; },
    selectedEntityName() { return this.selectedItem ? this.entityName(this.selectedItem) : (this.isPeople ? '人物作品' : '作品集成员'); },
    selectedEntityVideoCount() { return Number(this.selectedItem?.active_video_count || this.entityVideos.length); }
  },
  watch: {
    entityType() { this.closeEntity(); this.reload(); }
  },
  mounted() { this.setupInfiniteLoading(); this.reload(); },
  beforeUnmount() { this._intersectionObserver?.disconnect(); },
  methods: {
    formatBytes,
    formatDuration,
    setupInfiniteLoading() {
      if (typeof IntersectionObserver === 'undefined') return;
      const root = this.$el?.closest?.('.main-view') || null;
      this._intersectionObserver = new IntersectionObserver(entries => {
        if (!entries.some(entry => entry.isIntersecting)) return;
        if (this.selectedEntity) this.loadEntityVideos(false);
        else this.loadMore();
      }, { root, rootMargin: '320px 0px' });
      this.$nextTick(() => this.observeSentinel());
    },
    observeSentinel() {
      if (!this._intersectionObserver || !this.$refs.loadSentinel) return;
      if (this._observedSentinel) this._intersectionObserver.unobserve(this._observedSentinel);
      this._observedSentinel = this.$refs.loadSentinel;
      this._intersectionObserver.observe(this._observedSentinel);
    },
    async reload() {
      const requestToken = Symbol('entity-query'); this._queryToken = requestToken;
      this.items = []; this.cursorName = ''; this.cursorID = 0; this.hasMore = true;
      await this.loadMore(requestToken, true);
    },
    async loadMore(requestToken = this._queryToken, force = false) {
      if (!requestToken) { requestToken = Symbol('entity-query'); this._queryToken = requestToken; }
      if ((!force && this.loading) || !this.hasMore || this.selectedEntity) return;
      this.loading = true; this.error = '';
      const isPeople = this.isPeople; const keyword = this.keyword; const cursorName = this.cursorName; const cursorID = this.cursorID;
      try {
        const page = isPeople
          ? await ListPeople(keyword, cursorName, cursorID, 50)
          : await ListCollections(keyword, cursorName, cursorID, 50);
        if (this._queryToken !== requestToken) return;
        const next = page || []; this.items.push(...next); this.hasMore = next.length === 50;
        if (next.length) { const last = next[next.length - 1]; this.cursorName = last.cursor_name; this.cursorID = this.entityID(last); }
      } catch (err) { if (this._queryToken === requestToken) this.error = String(err); }
      finally { if (this._queryToken === requestToken) this.loading = false; }
    },
    async createEntity() {
      if (!this.createForm.name.trim()) return; this.creating = true; this.error = '';
      try {
        const entity = this.isPeople
          ? await CreatePerson(this.createForm.name, this.createForm.secondary)
          : await CreateCollection(this.createForm.name, this.createForm.secondary);
        this.createForm = { name: '', secondary: '' }; await this.reload();
        const item = this.items.find(candidate => this.entityID(candidate) === Number(entity.id)) || this.wrapCreatedEntity(entity);
        await this.openEntity(item);
      } catch (err) { this.error = String(err); }
      finally { this.creating = false; }
    },
    wrapCreatedEntity(entity) {
      return this.isPeople
        ? { person: entity, avatar_url: '', active_video_count: 0 }
        : { collection: entity, cover_url: '', active_video_count: 0 };
    },
    async openEntity(item) {
      this.selectedItem = item;
      this.selectedEntity = { type: this.entityType, id: this.entityID(item) };
      this.drawerEntity = { ...this.selectedEntity };
      this.entityVideos = []; this.entityVideoCursor = 0; this.collectionVideoPool = []; this.entityVideosHasMore = true;
      await this.loadEntityVideos(true);
      this.$nextTick(() => this.observeSentinel());
    },
    closeEntity() {
      this.selectedEntity = null; this.selectedItem = null; this.drawerEntity = null;
      this.entityVideos = []; this.collectionVideoPool = []; this.entityVideosHasMore = false; this.entityVideoCursor = 0;
      this.$nextTick(() => this.observeSentinel());
    },
    async loadEntityVideos(reset = false) {
      if (!this.selectedEntity || this.entityVideosLoading || (!reset && !this.entityVideosHasMore)) return;
      const entity = { ...this.selectedEntity }; const token = reset ? Symbol('entity-videos') : this._entityVideosToken;
      this._entityVideosToken = token; this.entityVideosLoading = true; this.error = '';
      try {
        if (entity.type === 'person') {
          const cursor = reset ? 0 : this.entityVideoCursor;
          const detail = await GetPersonDetail(entity.id, cursor, 30);
          if (this._entityVideosToken !== token || Number(this.selectedEntity?.id) !== Number(entity.id)) return;
          this.selectedItem = detail.person;
          const incoming = detail.videos || [];
          this.entityVideos = reset ? incoming : [...this.entityVideos, ...incoming];
          this.entityVideoCursor = Number(detail.next_video_id || 0);
          this.entityVideosHasMore = this.entityVideoCursor > 0;
        } else if (reset) {
          const detail = await GetCollectionDetail(entity.id);
          if (this._entityVideosToken !== token || Number(this.selectedEntity?.id) !== Number(entity.id)) return;
          this.selectedItem = detail.collection;
          this.collectionVideoPool = (detail.videos || []).map(item => item.video);
          this.entityVideos = this.collectionVideoPool.slice(0, 30);
          this.entityVideoCursor = this.entityVideos.length;
          this.entityVideosHasMore = this.entityVideoCursor < this.collectionVideoPool.length;
        } else {
          const nextIndex = this.entityVideoCursor + 30;
          this.entityVideos = this.collectionVideoPool.slice(0, nextIndex);
          this.entityVideoCursor = this.entityVideos.length;
          this.entityVideosHasMore = this.entityVideoCursor < this.collectionVideoPool.length;
        }
        this.patchSelectedItem();
      } catch (err) { if (this._entityVideosToken === token) this.error = `加载相关视频失败：${err}`; }
      finally { if (this._entityVideosToken === token) this.entityVideosLoading = false; }
    },
    patchSelectedItem() {
      if (!this.selectedItem) return;
      const id = this.entityID(this.selectedItem);
      const index = this.items.findIndex(item => this.entityID(item) === id);
      if (index >= 0) this.items.splice(index, 1, { ...this.items[index], ...this.selectedItem });
    },
    openVideoDetails(video) { this.drawerEntity = { type: 'video', id: Number(video.id) }; },
    async playEntityVideo(video) {
      const id = Number(video.id); if (!id || this.playingVideoIDs.includes(id)) return;
      this.playingVideoIDs = [...this.playingVideoIDs, id]; this.error = '';
      try {
        const result = await PlayVideo(id);
        if (result?.dispatch_succeeded === false) this.error = result.user_message || `播放失败：${video.name}`;
      } catch (err) { this.error = `播放失败：${err}`; }
      finally { this.playingVideoIDs = this.playingVideoIDs.filter(item => item !== id); }
    },
    async openVideoDirectory(video) {
      try { await OpenDirectory(Number(video.id)); }
      catch (err) { this.error = `打开目录失败：${err}`; }
    },
    entityID(item) { return Number(this.isPeople ? item?.person?.id : item?.collection?.id); },
    entityName(item) { return this.isPeople ? item?.person?.display_name : item?.collection?.name; },
    entitySecondary(item) { return this.isPeople ? (item?.person?.original_name || '无原始姓名') : (item?.collection?.description || '无简介'); },
    assetURL(item) { return this.isPeople ? item?.avatar_url : item?.cover_url; },
    async previewExternally(video) {
      try { await PreviewExternally(video.id); }
      catch (err) { this.error = `外部预览失败：${err}`; }
    },
    handleWatchProgress(progress) {
      const videoID = Number(progress?.videoID || 0); if (!videoID) return;
      const save = () => UpdateVideoWatchProgress(videoID, Number(progress?.positionSeconds || 0), !!progress?.completed);
      this._watchProgressPromise = (this._watchProgressPromise || Promise.resolve()).then(save).catch(err => { this.error = `保存观看进度失败：${err}`; });
    },
    async handleCollectionDeleted() { this.closeEntity(); await this.reload(); },
    async handlePersonDeleted() { this.closeEntity(); await this.reload(); },
    async handleRelationsUpdated(event) {
      if (event?.type === this.selectedEntity?.type && Number(event?.id) === Number(this.selectedEntity?.id)) await this.loadEntityVideos(true);
    }
  }
};
</script>

<style scoped>
.entity-library { padding: 14px 18px 28px; display: flex; flex-direction: column; gap: 14px; }.entity-library--with-drawer { padding-right: min(540px, 48vw); }
.entity-library__toolbar,.entity-library__create { padding: 14px 16px; border-radius: 14px; display: flex; gap: 14px; align-items: center; justify-content: space-between; }.entity-library__toolbar h2 { margin: 0 0 3px; font-size: 18px; }.entity-library__toolbar p { margin: 0; color: var(--text-muted); font-size: 12px; }
.entity-library__title { display: flex; align-items: center; gap: 12px; min-width: 0; }.entity-library__title > div { min-width: 0; }.entity-library__title h2,.entity-library__title p { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.entity-library__search,.entity-library__create { display: flex; gap: 8px; }.entity-library input { min-width: 180px; border: 1px solid var(--border-color); border-radius: 8px; padding: 9px 10px; color: var(--text-primary); background: var(--control-bg); }.entity-library__create { justify-content: flex-start; }.entity-library__create input:nth-child(2) { flex: 1; }
.entity-library__grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 12px; }.entity-card { display: grid; grid-template-columns: 72px 1fr; gap: 12px; align-items: center; padding: 12px; border-radius: 13px; text-align: left; color: var(--text-primary); cursor: pointer; }.entity-card img,.entity-card__placeholder { width: 72px; height: 72px; object-fit: cover; border-radius: 11px; }.entity-card__placeholder { display: grid; place-items: center; color: var(--text-muted); background: var(--control-hover-bg); }.entity-card > div:last-child { min-width: 0; display: grid; gap: 5px; }.entity-card span,.entity-card small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-muted); }
.entity-video-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 12px; }.entity-video-card { min-width: 0; overflow: hidden; border-radius: 13px; }.entity-video-card__preview { width: 100%; aspect-ratio: 16 / 9; overflow: hidden; border: 0; background: var(--thumb-bg); cursor: pointer; }.entity-video-card__preview img { width: 100%; height: 100%; display: block; object-fit: cover; }.entity-video-card__copy { display: grid; gap: 4px; min-width: 0; padding: 11px 12px 7px; }.entity-video-card__copy strong,.entity-video-card__copy span,.entity-video-card__copy small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.entity-video-card__copy span,.entity-video-card__copy small { color: var(--text-muted); font-size: 11px; }.entity-video-card__actions { display: flex; justify-content: flex-end; gap: 7px; padding: 0 12px 11px; }
.entity-library__more { align-self: center; }.entity-library__sentinel { height: 1px; }.entity-library__error { color: var(--danger-color); }
@media (max-width: 900px) { .entity-library--with-drawer { padding-right: 18px; }.entity-library__toolbar,.entity-library__create { align-items: stretch; flex-direction: column; }.entity-library__title { align-items: flex-start; }.entity-library__search { display: flex; }.entity-video-grid { grid-template-columns: repeat(auto-fill, minmax(230px, 1fr)); } }
</style>
