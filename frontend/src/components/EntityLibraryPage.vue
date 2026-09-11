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

    <!-- 人脸分析认出来但还没命名的面孔就在这里等着：命名或关联之后它们才成为下面列表里的人物。
         面板与 AI 标签管理里的「人物候选」是同一个组件、同一批簇。 -->
    <section v-if="isPeople && !selectedEntity" class="entity-face-review glass-surface" data-test="people-face-review">
      <div class="entity-face-review__head">
        <div>
          <h3>待命名人脸</h3>
          <p data-test="people-face-review-summary">{{ faceReviewSummary }}</p>
        </div>
        <button type="button" class="btn-secondary btn-compact" data-test="people-face-review-toggle" @click="toggleFaceReview">{{ faceReviewCollapsed ? '展开' : '收起' }}</button>
      </div>
      <FaceClusterReviewPanel v-show="!faceReviewCollapsed" @changed="reload" @loaded="onFaceReviewLoaded" />
    </section>

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
      <section v-if="isPeople" class="entity-image-section" aria-label="相关图片" data-test="person-image-section">
        <h3>图片（{{ selectedEntityImageCount }}）</h3>
        <p v-if="entityImagesError" class="entity-library__error" role="alert">{{ entityImagesError }}</p>
        <ImageBatchTagControls v-if="entityImages.length" :key="selectedEntity.id" v-model:selectedIDs="selectedPersonImageIDs" :image-i-ds="entityImages.map(image => image.id)" />
        <div v-if="entityImages.length" class="entity-image-grid">
          <figure v-for="image in entityImages" :key="image.id" class="entity-image-card glass-surface">
            <label class="entity-image-card__select"><input v-model="selectedPersonImageIDs" type="checkbox" :value="image.id" :aria-label="`选择 ${image.name}`" />选择</label>
            <button type="button" class="entity-image-card__preview" :aria-label="`放大 ${image.name}`" @click="imagePreview = image"><img :src="`/preview/image-thumbnail/${image.id}`" :alt="image.name" loading="lazy" /></button>
            <figcaption :title="image.name">{{ image.name }}</figcaption>
            <small v-if="image.size > 0" class="entity-image-card__size">{{ formatBytes(image.size) }}</small>
            <div class="entity-image-card__actions">
              <button type="button" class="btn-secondary btn-compact" @click="revealPersonImage(image)">目录</button>
              <button type="button" class="btn-secondary btn-compact" :disabled="unlinkingImageIDs.includes(image.id)" :data-test="`entity-image-unlink-${image.id}`" @click="unlinkPersonImage(image)">解绑</button>
            </div>
          </figure>
        </div>
        <div v-else class="empty-state">当前还没有关联图片。在照片页的单图详情里可以维护人物。</div>
        <button
          v-if="entityImageCursor"
          type="button"
          class="btn-secondary entity-library__more"
          :disabled="entityImagesLoading"
          data-test="person-image-more"
          @click="loadMoreEntityImages"
        >{{ entityImagesLoading ? '加载中...' : '加载更多相关图片' }}</button>
      </section>
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

    <ImageSourceDialog v-if="imagePreview" :image="imagePreview" allow-unlink :busy="unlinkingImageIDs.includes(imagePreview.id)" :action-error="entityImagesError" @close="imagePreview = null" @unlink="unlinkPersonImage" />

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
  CreateCollection, CreatePerson, GetCollectionDetail, GetPersonDetail, GetPersonImages, ListCollections, ListPeople, OpenDirectory, PlayVideo,
  PreviewExternally, UpdateVideoWatchProgress, RevealImage, RemovePersonImage
} from '../../wailsjs/go/main/App';
import FaceClusterReviewPanel from './FaceClusterReviewPanel.vue';
import PreviewDrawer from './PreviewDrawer.vue';
import ImageSourceDialog from './ImageSourceDialog.vue';
import ImageBatchTagControls from './ImageBatchTagControls.vue';
import { confirmAction } from '../utils/feedback.js';
import { formatBytes, formatDuration } from '../utils/mediaDetails.js';
import { registerCommands, unregisterCommands } from '../utils/commandRegistry.js';

export default {
  name: 'EntityLibraryPage',
  components: { FaceClusterReviewPanel, PreviewDrawer, ImageSourceDialog, ImageBatchTagControls },
  props: {
    entityType: { type: String, required: true },
    // 命令面板「作品集 · X」/「人物 · X」的落点（D-029）：{ id, name }，置一次即消费。
    focusEntity: { type: Object, default: null }
  },
  data() {
    return {
      items: [], keyword: '', cursorName: '', cursorID: 0, hasMore: true, loading: false, creating: false, error: '',
      createForm: { name: '', secondary: '' }, selectedEntity: null, selectedItem: null, drawerEntity: null,
      entityVideos: [], entityVideosHasMore: false, entityVideosLoading: false, entityVideoCursor: 0, collectionVideoPool: [],
      // 人物详情的图片区块独立分页、不与视频混排（D-021）；作品集不覆盖图片。
      entityImages: [], entityImageCursor: 0, entityImagesLoading: false, entityImagesError: '',
      playingVideoIDs: [], selectedPersonImageIDs: [], imagePreview: null, unlinkingImageIDs: [],
      // 待命名人脸分区：面板自己拉数据，这里只记摘要与收起状态。没有待处理项时默认收起，
      // 用户手动点过展开/收起之后就不再替他做主。
      faceReviewCounts: null, faceReviewCollapsed: false, faceReviewToggled: false
    };
  },
  computed: {
    isPeople() { return this.entityType === 'person'; },
    selectedEntityName() { return this.selectedItem ? this.entityName(this.selectedItem) : (this.isPeople ? '人物作品' : '作品集成员'); },
    selectedEntityVideoCount() { return Number(this.selectedItem?.active_video_count || this.entityVideos.length); },
    selectedEntityImageCount() { return Number(this.selectedItem?.active_image_count || this.entityImages.length); },
    faceReviewSummary() {
      const counts = this.faceReviewCounts;
      if (!counts) return '正在读取人脸候选…';
      const parts = [];
      if (counts.unnamed) parts.push(`${counts.unnamed} 组未命名`);
      if (counts.appendPending) parts.push(`${counts.appendPending} 组待确认追加`);
      if (!parts.length) return '暂无待处理的人脸候选。人脸分析在设置页启动，认出的面孔会出现在这里。';
      return `${parts.join(' · ')}；命名或关联后会成为下面列表里的人物。`;
    }
  },
  watch: {
    entityType() { this.closeEntity(); this.reload(); },
    focusEntity(focus) { this.applyFocusEntity(focus); }
  },
  mounted() {
    this.setupInfiniteLoading();
    this.reload();
    this.applyFocusEntity(this.focusEntity);
    // 命令面板的本页动作（D-029）。两种实体各挂一个 scope：两个页面不会同时挂载。
    registerCommands(this.isPeople ? 'entity-person' : 'entity-collection', [{
      id: this.isPeople ? 'action:person-reload' : 'action:collection-reload',
      group: 'action',
      label: this.isPeople ? '刷新人物列表' : '刷新作品集列表',
      keywords: ['reload', '刷新'],
      enabled: () => !this.loading,
      run: () => this.reload()
    }]);
  },
  beforeUnmount() {
    unregisterCommands(this.isPeople ? 'entity-person' : 'entity-collection');
    this._intersectionObserver?.disconnect();
  },
  methods: {
    async revealPersonImage(image) {
      try { await RevealImage(Number(image.id)); }
      catch (err) { this.entityImagesError = `打开目录失败：${err}`; }
    },
    async unlinkPersonImage(image) {
      const personID = Number(this.selectedEntity?.id); const imageID = Number(image.id);
      if (!this.isPeople || !personID || this.unlinkingImageIDs.includes(imageID)) return;
      this.unlinkingImageIDs.push(imageID); this.entityImagesError = '';
      try {
        const last = Number(this.selectedItem?.active_video_count || 0) === 0 && Number(this.selectedItem?.active_image_count || 0) <= 1;
        if (last && !await confirmAction({ title: '解除关联', message: '这是该人物最后一个活跃关联媒体。若没有软删除媒体保留的关系，解除后人物也会被删除，确定继续吗？图片文件和图库记录会保留。', confirmText: '解除', danger: true })) return;
        const deleted = await RemovePersonImage(personID, imageID);
        if (Number(this.selectedEntity?.id) !== personID) return;
        this.imagePreview = null;
        if (deleted) { this.closeEntity(); await this.reload(); return; }
        this._entityImagesToken = null; this.entityImagesLoading = false;
        this.entityImages = this.entityImages.filter(item => Number(item.id) !== imageID);
        this.selectedPersonImageIDs = this.selectedPersonImageIDs.filter(id => id !== imageID);
        if (this.selectedItem) this.selectedItem.active_image_count = Math.max(0, Number(this.selectedItem.active_image_count || 0) - 1);
        this.patchSelectedItem();
        // Refresh an open person drawer so its relation list agrees with the page.
        if (this.drawerEntity?.type === 'person' && Number(this.drawerEntity.id) === personID) {
          this.drawerEntity = null; await this.$nextTick();
          if (Number(this.selectedEntity?.id) === personID) this.drawerEntity = { type: 'person', id: personID };
        }
      } catch (err) { if (Number(this.selectedEntity?.id) === personID) this.entityImagesError = `解绑失败：${err}`; }
      finally { this.unlinkingImageIDs = this.unlinkingImageIDs.filter(id => id !== imageID); }
    },
    formatBytes,
    formatDuration,
    onFaceReviewLoaded(counts) {
      this.faceReviewCounts = { unnamed: Number(counts?.unnamed || 0), appendPending: Number(counts?.appendPending || 0) };
      if (!this.faceReviewToggled) {
        this.faceReviewCollapsed = this.faceReviewCounts.unnamed + this.faceReviewCounts.appendPending === 0;
      }
    },
    toggleFaceReview() {
      this.faceReviewToggled = true;
      this.faceReviewCollapsed = !this.faceReviewCollapsed;
    },
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
    // 命令面板指定了要打开的实体：列表里已有就用列表里的那份（带封面与作品数），
    // 还没加载到就先用最小包装，详情返回后 openEntity 会把 selectedItem 换成真值。
    applyFocusEntity(focus) {
      const id = Number(focus?.id || 0);
      if (!id) return undefined;
      const known = this.items.find(item => this.entityID(item) === id);
      const fallback = this.isPeople
        ? { person: { id, display_name: focus.name || '' }, avatar_url: '', active_video_count: 0 }
        : { collection: { id, name: focus.name || '' }, cover_url: '', active_video_count: 0 };
      return this.openEntity(known || fallback);
    },
    async openEntity(item) {
      this.selectedItem = item;
      this.selectedEntity = { type: this.entityType, id: this.entityID(item) };
      this.drawerEntity = { ...this.selectedEntity };
      this.entityVideos = []; this.entityVideoCursor = 0; this.collectionVideoPool = []; this.entityVideosHasMore = true;
      this.resetEntityImages();
      await this.loadEntityVideos(true);
      this.$nextTick(() => this.observeSentinel());
    },
    closeEntity() {
      this.selectedEntity = null; this.selectedItem = null; this.drawerEntity = null;
      this.entityVideos = []; this.collectionVideoPool = []; this.entityVideosHasMore = false; this.entityVideoCursor = 0;
      this.resetEntityImages();
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
          // 视频翻页拿到的还是图片首页，只在 reset 时采纳，否则会把已加载的图片顶掉。
          if (reset) {
            this.entityImages = detail.images || [];
            this.entityImageCursor = Number(detail.next_image_id || 0);
          }
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
    resetEntityImages() {
      this._entityImagesToken = null; this.imagePreview = null; this.selectedPersonImageIDs = [];
      this.entityImages = []; this.entityImageCursor = 0; this.entityImagesLoading = false; this.entityImagesError = '';
    },
    async loadMoreEntityImages() {
      const entity = { ...(this.selectedEntity || {}) };
      if (entity.type !== 'person' || !this.entityImageCursor || this.entityImagesLoading) return;
      const token = Symbol('entity-images'); this._entityImagesToken = token;
      this.entityImagesLoading = true; this.entityImagesError = '';
      try {
        const page = await GetPersonImages(entity.id, this.entityImageCursor, 30);
        if (this._entityImagesToken !== token || Number(this.selectedEntity?.id) !== Number(entity.id)) return;
        this.entityImages = [...this.entityImages, ...(page?.images || [])];
        this.entityImageCursor = Number(page?.next_image_id || 0);
      } catch (err) {
        if (this._entityImagesToken === token) this.entityImagesError = `加载相关图片失败：${err}`;
      } finally {
        if (this._entityImagesToken === token) this.entityImagesLoading = false;
      }
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
.entity-image-card__select { display: flex; align-items: center; gap: 6px; font-size: 12px; }
.entity-image-card__select input { min-width: 0; width: auto; padding: 0; }
.entity-image-card__preview { padding: 0; border: 0; background: transparent; cursor: zoom-in; }
.entity-image-card__actions { display: flex; gap: 8px; justify-content: flex-end; }
.entity-library { padding: 14px 18px 28px; display: flex; flex-direction: column; gap: 14px; }.entity-library--with-drawer { padding-right: calc(18px + 520px); }
.entity-library__toolbar,.entity-library__create { padding: 10px 16px; border: 1px solid var(--hairline); border-radius: var(--radius-md); background: var(--panel-bg); display: flex; gap: 12px; align-items: center; justify-content: space-between; }.entity-library__toolbar h2 { margin: 0 0 3px; font-size: 18px; }.entity-library__toolbar p { margin: 0; color: var(--text-muted); font-size: 12px; }
.entity-library__title { display: flex; align-items: center; gap: 12px; min-width: 0; }.entity-library__title > div { min-width: 0; }.entity-library__title h2,.entity-library__title p { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.entity-face-review { display: grid; gap: 10px; padding: 12px 14px; border-radius: var(--radius-md); }.entity-face-review__head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }.entity-face-review__head h3 { margin: 0; font-size: 14px; }.entity-face-review__head p { margin: 3px 0 0; color: var(--text-secondary); font-size: 12px; }
.entity-library__search,.entity-library__create { display: flex; gap: 8px; }.entity-library input { min-width: 180px; border: 1px solid var(--border-color); border-radius: 8px; padding: 9px 10px; color: var(--text-primary); background: var(--control-bg); }.entity-library__create { justify-content: flex-start; }.entity-library__create input:nth-child(2) { flex: 1; }
/* 原型 A9：卡片改成竖版，封面在上、名称与作品数在下；列宽随窗口自增。 */
.entity-library__grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 12px; }
.entity-card { display: flex; flex-direction: column; padding: 0; overflow: hidden; border: 1px solid var(--hairline); border-radius: var(--radius-md); background: var(--panel-bg); text-align: left; color: var(--text-primary); cursor: pointer; }
.entity-card:hover { border-color: var(--border-strong); }
.entity-card img,.entity-card__placeholder { width: 100%; height: 150px; object-fit: cover; border-radius: 0; }
.entity-card__placeholder { display: grid; place-items: center; color: var(--text-muted); background: var(--thumb-fallback-bg); }
.entity-card > div:last-child { min-width: 0; display: grid; gap: 3px; padding: 9px 10px 11px; }
.entity-card strong { font-size: 13px; font-weight: 650; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.entity-card span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-muted); font-size: 11px; }
.entity-card small { color: var(--accent-text); font-size: 11.5px; font-weight: 600; }
.entity-video-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 12px; }.entity-video-card { min-width: 0; overflow: hidden; border: 1px solid var(--hairline); border-radius: var(--radius-md); background: var(--panel-bg); }.entity-video-card__preview { width: 100%; aspect-ratio: 16 / 9; overflow: hidden; border: 0; background: var(--thumb-bg); cursor: pointer; }.entity-video-card__preview img { width: 100%; height: 100%; display: block; object-fit: cover; }.entity-video-card__copy { display: grid; gap: 4px; min-width: 0; padding: 11px 12px 7px; }.entity-video-card__copy strong,.entity-video-card__copy span,.entity-video-card__copy small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.entity-video-card__copy span,.entity-video-card__copy small { color: var(--text-muted); font-size: 11px; }.entity-video-card__actions { display: flex; justify-content: flex-end; gap: 7px; padding: 0 12px 11px; }
.entity-image-section { display: grid; gap: 10px; }
.entity-image-section h3 { margin: 0; font-size: 15px; }
.entity-image-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); gap: 12px; }
.entity-image-card { margin: 0; min-width: 0; overflow: hidden; display: grid; gap: 6px; padding: 8px; border: 1px solid var(--hairline); border-radius: var(--radius-md); background: var(--panel-bg); }
.entity-image-card img { width: 100%; aspect-ratio: 1; display: block; object-fit: cover; border-radius: 8px; background: var(--thumb-bg); }
.entity-image-card figcaption { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-muted); font-size: 11px; }
.entity-image-card__size { color: var(--text-muted); font-size: 11px; font-variant-numeric: tabular-nums; }
.entity-library__more { align-self: center; }.entity-library__sentinel { height: 1px; }.entity-library__error { color: var(--danger-color); }
@media (max-width: 1100px) { .entity-library--with-drawer { padding-right: 18px; } }
@media (max-width: 900px) {.entity-library__toolbar,.entity-library__create { align-items: stretch; flex-direction: column; }.entity-library__title { align-items: flex-start; }.entity-library__search { display: flex; }.entity-video-grid { grid-template-columns: repeat(auto-fill, minmax(230px, 1fr)); } }
</style>
