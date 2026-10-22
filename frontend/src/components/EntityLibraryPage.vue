<template>
  <!-- 人物与作品集管理页（D-P3-04）：页头 + 三个标签 人物 · 作品集 · 人脸审阅。人物与作品集两个标签共用下面的列表 / 对象区
       （listKind 是正在管的那一种实体），人脸审阅标签挂完整的人脸审阅面板（待处理 · 已命名 · 已忽略）。 -->
  <main :class="['entity-library', { 'entity-library--with-drawer': drawerEntity && !isFacesTab }]">
    <PageHeader class="entity-library__header" title="人物与作品集" :subtitle="pageSubtitle">
      <template v-if="!isFacesTab && !selectedEntity" #actions>
        <div class="entity-library__search">
          <input v-model="keyword" :placeholder="isPeople ? '搜索显示或原始姓名' : '搜索名称或简介'" :aria-label="isPeople ? '搜索人物' : '搜索作品集'" data-test="entity-library-search" @keyup.enter="reload" />
          <button type="button" class="btn btn--secondary" @click="reload">搜索</button>
        </div>
      </template>
      <template #below>
        <AppTabs :tabs="tabItems" :model-value="activeTab" id-prefix="entities-manage" aria-label="人物与作品集" @update:model-value="selectTab" />
      </template>
    </PageHeader>

    <div
      v-show="!isFacesTab"
      :id="`entities-manage-panel-${listKind}`"
      class="entity-library__panel"
      role="tabpanel"
      :aria-labelledby="`entities-manage-tab-${listKind}`"
      :aria-busy="listLoadingFirst || entityLoadingFirst ? 'true' : undefined"
    >
    <template v-if="selectedEntity">
      <nav class="entity-library__nav">
        <button type="button" class="btn btn--secondary btn--sm" @click="closeEntity"><AppIcon name="chevron-left" /> 返回{{ isPeople ? '人物' : '作品集' }}列表</button>
      </nav>
      <PageHeader class="entity-library__entity-head" level="section" :title="selectedEntityName" :subtitle="entitySubtitle" data-test="entity-library-entity-head">
        <template #actions>
          <button type="button" class="btn btn--primary" @click="drawerEntity = { ...selectedEntity }">编辑与批量关联</button>
          <!-- D-PC32：人物合并与显式删除（META-03 / META-05）。 -->
          <template v-if="isPeople">
            <button type="button" class="btn btn--secondary" data-test="person-merge-open" :disabled="personActionBusy" @click="openMergeForm">合并其他人物</button>
            <button type="button" class="btn btn--danger" data-test="person-delete" :disabled="personActionBusy" @click="requestDeletePerson">删除人物</button>
          </template>
        </template>
      </PageHeader>
    </template>

    <CollectionEpisodePanel v-if="selectedEntity && !isPeople && !isFacesTab" :collection-id="Number(selectedEntity.id)" />
    <section v-if="!selectedEntity" class="entity-library__create glass-surface">
      <input ref="createName" v-model="createForm.name" :placeholder="isPeople ? '显示姓名' : '作品集名称'" :aria-label="isPeople ? '新人物的显示姓名' : '新作品集的名称'" maxlength="200" />
      <input v-model="createForm.secondary" :placeholder="isPeople ? '原始姓名（可空）' : '简介（可空）'" :aria-label="isPeople ? '新人物的原始姓名' : '新作品集的简介'" :maxlength="isPeople ? 200 : 4000" />
      <button type="button" class="btn btn--primary" :disabled="creating" @click="createEntity">{{ creating ? '创建中…' : (isPeople ? '明确新建人物' : '新建作品集') }}</button>
    </section>

    <p v-if="error" class="entity-library__error" role="alert">{{ error }}</p>

    <!-- 合并人物：当前人物是保留目标，勾选要并入的同一个人（例如 NFO 导入重复建出来的同名人物）。 -->
    <section v-if="isPeople && selectedEntity && mergeForm.open" class="entity-person-merge glass-surface" data-test="person-merge-panel">
      <p>把其他人物合并到「{{ selectedEntityName }}」：它们的视频、图片关系并入「{{ selectedEntityName }}」（重复的自动去掉），人脸组改指向「{{ selectedEntityName }}」，被并入的人物随后删除。</p>
      <div class="entity-library__search">
        <input v-model="mergeForm.keyword" placeholder="搜索要并入的人物" aria-label="搜索要并入的人物" data-test="person-merge-search" @keyup.enter="searchMergeCandidates" />
        <button type="button" class="btn btn--secondary" :disabled="mergeForm.searching" @click="searchMergeCandidates">搜索</button>
      </div>
      <div class="entity-person-merge__options">
        <label v-for="item in mergeCandidates" :key="entityID(item)" class="entity-person-merge__option" :data-test="`person-merge-option-${entityID(item)}`">
          <input type="checkbox" :checked="mergeForm.selectedIDs.includes(entityID(item))" @change="toggleMergeSource(item, $event.target.checked)" />
          <span>{{ entityName(item) }}</span>
          <small>{{ entitySecondary(item) }} · {{ item.active_video_count || 0 }} 部视频 · {{ item.active_image_count || 0 }} 张图片 · #{{ entityID(item) }}</small>
        </label>
        <p v-if="!mergeForm.searching && !mergeCandidates.length" class="entity-person-merge__empty">没有可并入的人物，换个关键词试试。</p>
      </div>
      <p v-if="mergeForm.error" class="entity-library__error" role="alert">{{ mergeForm.error }}</p>
      <div class="entity-person-merge__actions">
        <button type="button" class="btn btn--secondary" :disabled="personActionBusy" @click="closeMergeForm">取消</button>
        <button type="button" class="btn btn--danger" data-test="person-merge-submit" :disabled="personActionBusy || !mergeForm.selectedIDs.length" @click="submitMerge">合并（{{ mergeForm.selectedIDs.length }}）</button>
      </div>
    </section>

    <template v-if="!selectedEntity">
      <!-- 列表的三种状态：首次读取骨架屏；读失败横幅 + 重试；读到了但没有时空状态（给下一步）。 -->
      <AppBanner v-if="listError" tone="danger" :title="`${isPeople ? '人物' : '作品集'}列表没读到`" :message="listError" data-test="entity-library-list-error">
        <template #actions>
          <button type="button" class="btn btn--secondary btn--sm" data-test="entity-library-list-retry" :disabled="loading" @click="reload">重试</button>
        </template>
      </AppBanner>
      <div v-if="listLoadingFirst" class="entity-library__grid" data-test="entity-library-loading">
        <SkeletonBlock v-for="index in 6" :key="index" shape="card" :lines="2" />
      </div>
      <section v-else class="entity-library__grid">
        <button v-for="item in items" :key="entityID(item)" type="button" class="btn-plain entity-card glass-surface" @click="openEntity(item)">
          <img v-if="assetURL(item)" :src="assetURL(item)" alt="" />
          <div v-else class="entity-card__placeholder">{{ isPeople ? '人物' : '作品集' }}</div>
          <div>
            <strong>{{ entityName(item) }}</strong>
            <span>{{ entitySecondary(item) }}</span>
            <small v-if="isPeople" class="entity-card__media-count">{{ item.active_video_count || 0 }} 部视频 · {{ item.active_image_count || 0 }} 张图片</small>
            <small v-else>{{ item.active_video_count || 0 }} 部活跃作品</small>
          </div>
        </button>
      </section>
      <EmptyState
        v-if="!loading && !listError && !items.length"
        :icon="isPeople ? 'person' : 'collection'"
        :title="`暂无${isPeople ? '人物' : '作品集'}`"
        :message="emptyMessage"
        data-test="entity-library-empty"
      >
        <template #actions>
          <button v-if="keyword.trim()" type="button" class="btn btn--secondary" data-test="entity-library-clear-search" @click="clearSearch">清空搜索</button>
          <button v-else-if="isPeople" type="button" class="btn btn--secondary" data-test="entity-library-go-faces" @click="selectTab('faces')">去人脸审阅</button>
          <button v-else type="button" class="btn btn--secondary" data-test="entity-library-focus-create" @click="focusCreate">新建作品集</button>
        </template>
      </EmptyState>
    </template>

    <template v-else>
      <AppBanner v-if="entityError" tone="danger" :title="`${isPeople ? '人物' : '作品集'}的内容没读到`" :message="entityError" data-test="entity-library-entity-error">
        <template #actions>
          <button type="button" class="btn btn--secondary btn--sm" data-test="entity-library-entity-retry" :disabled="entityVideosLoading" @click="loadEntityVideos(true)">重试</button>
        </template>
      </AppBanner>
      <div v-if="entityLoadingFirst" class="entity-video-grid" data-test="entity-library-entity-loading">
        <SkeletonBlock v-for="index in 4" :key="index" shape="card" :lines="2" />
      </div>
      <section v-if="entityVideos.length" class="entity-video-grid" aria-label="相关视频">
        <article v-for="video in entityVideos" :key="video.id" class="entity-video-card glass-surface">
          <button type="button" class="btn-plain entity-video-card__preview" @click="openVideoDetails(video)">
            <img :src="`/preview/thumbnail/${video.id}`" :alt="`${video.name} 缩略图`" loading="lazy" />
          </button>
          <div class="entity-video-card__copy">
            <strong :title="video.display_title || video.name">{{ video.display_title || video.name }}</strong>
            <span :title="video.name">{{ video.name }}</span>
            <small>{{ formatBytes(video.size) }} · {{ formatDuration(video.duration) }}</small>
          </div>
          <div class="entity-video-card__actions">
            <button type="button" class="btn btn--secondary btn--sm" :data-test="`entity-video-detail-${video.id}`" @click="openVideoDetails(video)">详情</button>
            <button type="button" class="btn btn--primary btn--sm" :disabled="playingVideoIDs.includes(Number(video.id))" @click="playEntityVideo(video)">{{ playingVideoIDs.includes(Number(video.id)) ? '启动中…' : '播放' }}</button>
            <button type="button" class="btn-icon btn--secondary btn--sm" aria-label="在访达中显示" title="在访达中显示" @click="openVideoDirectory(video)"><AppIcon name="folder" /></button>
            <!-- 人物页上「移除」容易被当成从这个人物名下拿掉：写明是从片库移除，点了一律先确认（PersonMediaDeleteDialog）。 -->
            <button v-if="isPeople" type="button" class="btn btn--danger btn--sm" :data-test="`person-video-delete-${video.id}`" @click="requestPersonMediaDelete('video', video)">从片库移除…</button>
          </div>
        </article>
      </section>
      <EmptyState v-if="!entityVideosLoading && !entityError && !entityVideos.length" icon="videos" title="当前还没有关联视频" message="点击「编辑与批量关联」可按文件夹筛选并多选加入。" data-test="entity-videos-empty">
        <template #actions>
          <button type="button" class="btn btn--secondary" data-test="entity-videos-empty-edit" @click="drawerEntity = { ...selectedEntity }">编辑与批量关联</button>
        </template>
      </EmptyState>
      <section v-if="isPeople" class="entity-image-section" aria-label="相关图片" data-test="person-image-section">
        <h3>图片（{{ selectedEntityImageCount }}）</h3>
        <p v-if="entityImagesError" class="entity-library__error" role="alert">{{ entityImagesError }}</p>
        <ImageBatchTagControls v-if="entityImages.length" :key="selectedEntity.id" v-model:selectedIDs="selectedPersonImageIDs" :image-i-ds="entityImages.map(image => image.id)" />
        <div v-if="entityImages.length" class="entity-image-grid">
          <figure v-for="image in entityImages" :key="image.id" class="entity-image-card glass-surface">
            <label class="entity-image-card__select"><input v-model="selectedPersonImageIDs" type="checkbox" :value="image.id" :aria-label="`选择 ${image.name}`" />选择</label>
            <button type="button" class="btn-plain entity-image-card__preview" :aria-label="`放大 ${image.name}`" @click="imagePreview = image"><img :src="`/preview/image-thumbnail/${image.id}`" :alt="image.name" loading="lazy" /></button>
            <figcaption :title="image.name">{{ image.name }}</figcaption>
            <small v-if="image.size > 0" class="entity-image-card__size">{{ formatBytes(image.size) }}</small>
            <div class="entity-image-card__actions">
              <button type="button" class="btn-icon btn--secondary btn--sm" aria-label="在访达中显示" title="在访达中显示" @click="revealPersonImage(image)"><AppIcon name="folder" /></button>
              <button type="button" class="btn btn--secondary btn--sm" :disabled="unlinkingImageIDs.includes(image.id)" :data-test="`entity-image-unlink-${image.id}`" @click="unlinkPersonImage(image)">解绑</button>
              <button type="button" class="btn btn--danger btn--sm" :disabled="unlinkingImageIDs.includes(image.id)" :data-test="`person-image-delete-${image.id}`" @click="requestPersonMediaDelete('image', image)">从图片库移除…</button>
            </div>
          </figure>
        </div>
        <EmptyState v-else icon="images" title="当前还没有关联图片" message="在照片页的单图详情里可以维护人物。" data-test="person-images-empty" />
        <button
          v-if="entityImageCursor"
          type="button"
          class="btn btn--secondary entity-library__more"
          :disabled="entityImagesLoading"
          data-test="person-image-more"
          @click="loadMoreEntityImages"
        >{{ entityImagesLoading ? '加载中…' : '加载更多相关图片' }}</button>
      </section>
    </template>

    <div ref="loadSentinel" class="entity-library__sentinel" aria-hidden="true"></div>
    <button
      v-if="selectedEntity ? entityVideosHasMore : hasMore"
      type="button"
      class="btn btn--secondary entity-library__more"
      :disabled="selectedEntity ? entityVideosLoading : loading"
      @click="selectedEntity ? loadEntityVideos(false) : loadMore()"
    >
      {{ (selectedEntity ? entityVideosLoading : loading) ? '加载中…' : (selectedEntity ? '加载更多相关视频' : `加载更多${isPeople ? '人物' : '作品集'}`) }}
    </button>
    </div>

    <!-- 人脸审阅（D-P3-04）：人脸分析认出的面孔在这里命名、关联、确认追加或忽略，命名或关联之后成为「人物」标签里的人物。
         面板随本页一起挂载（标签上的数量要靠它读），不在这个标签上时只隐藏。 -->
    <section
      v-show="isFacesTab"
      id="entities-manage-panel-faces"
      class="entity-face-review"
      role="tabpanel"
      aria-labelledby="entities-manage-tab-faces"
      data-test="people-face-review"
    >
      <p class="entity-face-review__summary" data-test="people-face-review-summary">{{ faceReviewSummary }}</p>
      <FaceClusterReviewPanel @changed="handleFacesChanged" @loaded="onFaceReviewLoaded" />
    </section>

    <ImageSourceDialog v-if="imagePreview" :image="imagePreview" allow-unlink allow-delete :busy="unlinkingImageIDs.includes(imagePreview.id)" :action-error="entityImagesError" @close="imagePreview = null" @unlink="unlinkPersonImage" @delete="requestPersonMediaDelete('image', $event)" />
    <PersonMediaDeleteDialog v-if="personMediaDeleteTarget" :target="personMediaDeleteTarget" @close="personMediaDeleteTarget = null" @deleted="handlePersonMediaDeleted" @restored="handlePersonMediaRestored" />

    <PreviewDrawer
      v-if="drawerEntity && !isFacesTab"
      ref="personDrawer"
      :initial-entity="drawerEntity"
      @close="drawerEntity = null"
      @preview-externally="previewExternally"
      @watch-progress="handleWatchProgress"
      @collection-deleted="handleCollectionDeleted"
      @person-deleted="handlePersonDeleted"
      @relations-updated="handleRelationsUpdated"
      @media-deleted="handlePersonMediaDeleted($event, true)"
      @media-restored="handlePersonMediaRestored($event, true)"
    />
  </main>
</template>

<script>
import CollectionEpisodePanel from './CollectionEpisodePanel.vue';
import { playVideoAfterProgress as PlayVideo, saveVideoWatchProgress as UpdateVideoWatchProgress } from '../utils/watchProgress.js';
import {
  CreateCollection, CreatePerson, DeletePerson, GetCollectionDetail, GetPersonDeletionImpact, GetPersonDetail, GetPersonImages, ListCollections, ListPeople,
  MergePeople, OpenDirectory, PreviewExternally, RevealImage, RemovePersonImage
} from '../../wailsjs/go/main/App';
import FaceClusterReviewPanel from './FaceClusterReviewPanel.vue';
import PreviewDrawer from './PreviewDrawer.vue';
import ImageSourceDialog from './ImageSourceDialog.vue';
import PersonMediaDeleteDialog from './PersonMediaDeleteDialog.vue';
import ImageBatchTagControls from './ImageBatchTagControls.vue';
import AppBanner from './ui/AppBanner.vue';
import AppIcon from './ui/AppIcon.vue';
import AppTabs from './ui/AppTabs.vue';
import EmptyState from './ui/EmptyState.vue';
import PageHeader from './ui/PageHeader.vue';
import SkeletonBlock from './ui/SkeletonBlock.vue';
import { confirmAction, notify, notifySuccess } from '../utils/feedback.js';
import { formatBytes, formatDuration } from '../utils/mediaDetails.js';
import { registerCommands, unregisterCommands } from '../utils/commandRegistry.js';
import { DEFAULT_ENTITIES_MANAGE_TAB, ENTITIES_MANAGE_TABS, isEntitiesManageTab } from '../utils/appCommands.js';

// UpdateVideoWatchProgress 的起播来源（D-PC42）。
const WATCH_PROGRESS_ORIGINS = ['resume', 'start', 'jump'];
// 人物合并 / 删除的错误码（D-PC32）。
const PERSON_ERROR_TEXT = {
  invalid_merge: '不能把人物合并到它自己。',
  person_not_found: '有人物已经不存在了，列表已刷新。'
};
function personErrorText(err, fallback) {
  const raw = String(err?.message || err || '');
  const code = Object.keys(PERSON_ERROR_TEXT).find(item => raw.includes(item));
  return { code, text: code ? PERSON_ERROR_TEXT[code] : `${fallback}：${raw}` };
}
const emptyMergeForm = () => ({ open: false, keyword: '', results: [], selectedIDs: [], searching: false, error: '' });
// 列表 / 对象区管的实体种类：标签 person / collection 各管一种；人脸审阅标签不换种类（切回来时列表与打开的对象都还在）。
const LIST_KINDS = ['person', 'collection'];
const TAB_DATA_TESTS = { person: 'entities-manage-tab-person', collection: 'entities-manage-tab-collection', faces: 'entities-manage-tab-faces' };
const PAGE_SUBTITLES = {
  person: '本地维护的演员实体；同名人物会保持独立。',
  collection: '手工编排的视频集合；一个视频可以属于多个作品集。',
  faces: '人脸分析认出的面孔：在这里命名、关联或确认追加之后，才会成为「人物」里的人物。'
};
const COMMAND_SCOPE = 'entities-manage';

// 管理侧「人物与作品集」（D-P3-04，详细设计 §3.8）：页头 + 标签 人物 · 作品集 · 人脸审阅。
// - 当前标签归 App（页面参数 tab，点标签发 update:tab）；people.openFaceReview 由 App 带着 { tab: 'faces' } 切过来。
// - 人物 / 作品集：新建、改名（编辑抽屉）、合并、删除、批量关联、从片库移除媒体、解绑图片、作品集排序，全部编辑能力都在这里。
// - focusEntity（观看页「在管理中编辑」）：{ id, name }，在对应的标签上打开这个对象并连编辑抽屉一起打开，置一次即消费。
export default {
  name: 'EntityLibraryPage',
  components: { CollectionEpisodePanel, AppBanner, AppIcon, AppTabs, EmptyState, FaceClusterReviewPanel, PreviewDrawer, ImageSourceDialog, ImageBatchTagControls, PageHeader, PersonMediaDeleteDialog, SkeletonBlock },
  // open-video(video)：点了某部相关视频，App 按管理区分流（片库管理 + 详情抽屉）。update:tab(key)：用户点了标签。
  emits: ['open-video', 'update:tab'],
  props: {
    tab: { type: String, default: DEFAULT_ENTITIES_MANAGE_TAB },
    focusEntity: { type: Object, default: null }
  },
  data() {
    return {
      listKind: LIST_KINDS.includes(this.tab) ? this.tab : 'person',
      items: [], keyword: '', cursorName: '', cursorID: 0, hasMore: true, loading: false, creating: false, error: '',
      // 列表读失败（横幅 + 重试）；打开的对象读失败（横幅 + 重试）。其余动作的失败照旧写在 error 里。
      listError: '', entityError: '',
      createForm: { name: '', secondary: '' }, selectedEntity: null, selectedItem: null, drawerEntity: null,
      entityVideos: [], entityVideosHasMore: false, entityVideosLoading: false, entityVideoCursor: 0, collectionVideoPool: [],
      // 人物详情的图片区块独立分页、不与视频混排（D-021）；作品集不覆盖图片。
      entityImages: [], entityImageCursor: 0, entityImagesLoading: false, entityImagesError: '',
      playingVideoIDs: [], selectedPersonImageIDs: [], imagePreview: null, unlinkingImageIDs: [],
      personMediaDeleteTarget: null,
      // 人脸审阅：面板自己拉数据，这里只记它报上来的数量（标签上的数字与摘要）。
      faceReviewCounts: null,
      mergeForm: emptyMergeForm(), personActionBusy: false
    };
  },
  computed: {
    activeTab() { return isEntitiesManageTab(this.tab) ? this.tab : DEFAULT_ENTITIES_MANAGE_TAB; },
    isFacesTab() { return this.activeTab === 'faces'; },
    isPeople() { return this.listKind === 'person'; },
    tabItems() {
      return ENTITIES_MANAGE_TABS.map(tab => ({
        key: tab.key,
        label: tab.label,
        dataTest: TAB_DATA_TESTS[tab.key],
        count: tab.key === 'faces' ? this.faceReviewTabCount : null
      }));
    },
    // 人脸审阅标签上的数字：面板读到之后才有（未命名还有下一页时带「+」），没有待处理项时不写。
    faceReviewTabCount() {
      const counts = this.faceReviewCounts;
      if (!counts) return null;
      const total = counts.unnamed + counts.appendPending;
      if (!total && !counts.unnamedHasMore) return null;
      return `${total}${counts.unnamedHasMore ? '+' : ''}`;
    },
    listLoadingFirst() { return this.loading && !this.items.length && !this.listError; },
    entityLoadingFirst() { return this.entityVideosLoading && !this.entityVideos.length && !this.entityError; },
    emptyMessage() {
      if (this.keyword.trim()) return '换个关键词再搜一次，或清空搜索看全部。';
      return this.isPeople ? '在上面明确新建人物，或者去「人脸审阅」给认出来的面孔起名。' : '在上面新建作品集，再把视频加进去。';
    },
    selectedEntityName() { return this.selectedItem ? this.entityName(this.selectedItem) : (this.isPeople ? '人物作品' : '作品集成员'); },
    selectedEntityVideoCount() { return Number(this.selectedItem?.active_video_count || this.entityVideos.length); },
    selectedEntityImageCount() { return Number(this.selectedItem?.active_image_count || this.entityImages.length); },
    // 页头副标题跟着标签走；打开某一个对象时，它的名字与已加载的数量写在下面的分区标题上。
    pageSubtitle() { return PAGE_SUBTITLES[this.activeTab]; },
    entitySubtitle() {
      if (this.isPeople) {
        return `已加载：视频 ${this.entityVideos.length} / ${this.selectedEntityVideoCount} 部 · 图片 ${this.entityImages.length} / ${this.selectedEntityImageCount} 张。`;
      }
      return `${this.entityVideos.length} / ${this.selectedEntityVideoCount} 部已加载，可直接查看详情、播放或在访达中显示。`;
    },
    faceReviewSummary() {
      const counts = this.faceReviewCounts;
      if (!counts) return '正在读取人脸候选…';
      const parts = [];
      // 未命名按页加载（D-PC29）：还有下一页时写成「N+」，不假装这就是全部。
      if (counts.unnamed) parts.push(`${counts.unnamed}${counts.unnamedHasMore ? '+' : ''} 组未命名`);
      if (counts.appendPending) parts.push(`${counts.appendPending} 组待确认追加`);
      if (!parts.length) return '暂无待处理的人脸候选。人脸分析在设置页启动，认出的面孔会出现在这里。';
      return `${parts.join(' · ')}；命名或关联后会出现在「人物」标签里。`;
    },
    mergeCandidates() {
      const selfID = Number(this.selectedEntity?.id || 0);
      return this.mergeForm.results.filter(item => this.entityID(item) && this.entityID(item) !== selfID);
    }
  },
  watch: {
    // 标签换到另一种实体：列表从头读，打开的对象与表单收起（原先是整页按种类重挂）。先于 focusEntity 处理，
    // 「在管理中编辑」同一拍换标签又给对象时，对象落在新的那一种上（applyFocusEntity 也会先同步一次）。
    tab() { this.syncListKind(); },
    focusEntity(focus) { this.applyFocusEntity(focus); }
  },
  mounted() {
    this.setupInfiniteLoading();
    this.reload();
    this.applyFocusEntity(this.focusEntity);
    this.registerPageCommands();
  },
  beforeUnmount() {
    unregisterCommands(COMMAND_SCOPE);
    this._intersectionObserver?.disconnect();
  },
  methods: {
    requestPersonMediaDelete(kind, media) {
      if (!this.isPeople || !this.selectedEntity) return;
      this.imagePreview = null;
      this.personMediaDeleteTarget = { kind, media, personID: Number(this.selectedEntity.id) };
    },
    handlePersonMediaDeleted(target, fromDrawer = false) {
      const id = Number(target.media.id);
      if (!this.isPeople || Number(this.selectedEntity?.id) !== target.personID) return;
      // Discard in-flight pages which could still contain the deleted media.
      this._entityVideosToken = Symbol('media-deleted'); this.entityVideosLoading = false;
      this._entityImagesToken = null; this.entityImagesLoading = false;
      if (target.kind === 'video') {
        this.entityVideos = this.entityVideos.filter(item => Number(item.id) !== id);
        if (this.drawerEntity?.type === 'video' && Number(this.drawerEntity.id) === id) this.drawerEntity = null;
      } else {
        this.entityImages = this.entityImages.filter(item => Number(item.id) !== id);
        this.selectedPersonImageIDs = this.selectedPersonImageIDs.filter(value => Number(value) !== id);
        if (Number(this.imagePreview?.id) === id) this.imagePreview = null;
      }
      const field = target.kind === 'video' ? 'active_video_count' : 'active_image_count';
      if (this.selectedItem) this.selectedItem[field] = Math.max(0, Number(this.selectedItem[field] || 0) - 1);
      this.patchSelectedItem();
      if (!fromDrawer) this.$refs.personDrawer?.handlePersonMediaDeleted?.(target, false);
      if (target.kind === 'image' && !this.entityImages.length && this.entityImageCursor) this.loadMoreEntityImages();
      if (target.kind === 'video' && !this.entityVideos.length && this.entityVideosHasMore) this.loadEntityVideos(false);
    },
    // 删除被撤销或从回收站恢复（PersonMediaDeleteDialog 的 restored，来自本页或抽屉）：恢复的媒体回到人物名下，
    // 重读当前人物的视频首页、图片首页与计数；本页发起的恢复顺带让停在人物详情的抽屉也重读。
    async handlePersonMediaRestored(payload, fromDrawer = false) {
      if (!fromDrawer) this.$refs.personDrawer?.handlePersonMediaRestored?.(payload, false);
      if (!this.isPeople || !this.selectedEntity) return;
      this._entityVideosToken = Symbol('media-restored'); this.entityVideosLoading = false;
      this._entityImagesToken = null; this.entityImagesLoading = false;
      await this.loadEntityVideos(true);
    },
    async revealPersonImage(image) {
      try { await RevealImage(Number(image.id)); }
      catch (err) { this.entityImagesError = `在访达中显示失败：${err}`; }
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
    // 命令面板的本页动作（D-029）：刷新当前那一种实体的列表（命令 ID 按种类，换种类时重新登记），以及
    // people.openFaceReview——待处理与 App 全局命令跳转用的固定命令 ID（详细设计 §6.1 / D-PC27），切到「人脸审阅」标签。
    registerPageCommands() {
      registerCommands(COMMAND_SCOPE, [
        {
          id: this.isPeople ? 'action:person-reload' : 'action:collection-reload',
          group: 'action',
          label: this.isPeople ? '刷新人物列表' : '刷新作品集列表',
          keywords: ['reload', '刷新'],
          enabled: () => !this.loading,
          run: () => this.reload()
        },
        {
          id: 'people.openFaceReview',
          group: 'action',
          label: '处理待命名人脸',
          keywords: ['face', '人脸', '人脸审阅', '人物候选', '待处理'],
          run: () => this.selectTab('faces')
        }
      ]);
    },
    selectTab(key) {
      if (isEntitiesManageTab(key) && key !== this.activeTab) this.$emit('update:tab', key);
    },
    // 当前标签是人物或作品集、又不是正在管的那一种：换过去，列表从头读，打开的对象、搜索与表单都收起。
    syncListKind() {
      const kind = this.activeTab;
      if (!LIST_KINDS.includes(kind) || kind === this.listKind) return;
      this.listKind = kind;
      this.closeEntity();
      this.keyword = '';
      this.createForm = { name: '', secondary: '' };
      this.error = '';
      this.reload();
      this.registerPageCommands();
    },
    clearSearch() {
      this.keyword = '';
      this.reload();
    },
    focusCreate() {
      this.$refs.createName?.focus?.();
    },
    onFaceReviewLoaded(counts) {
      this.faceReviewCounts = { unnamed: Number(counts?.unnamed || 0), appendPending: Number(counts?.appendPending || 0), unnamedHasMore: Boolean(counts?.unnamedHasMore) };
    },
    // 人脸审阅里命名、关联、改派之后人物列表变了：停在人物列表上就重读；正打开着某个人物时等回到列表再读
    // （对象打开时 reload 读不了列表，会把列表清空）。
    handleFacesChanged() {
      if (!this.isPeople) return;
      if (this.selectedEntity) this._listStale = true;
      else this.reload();
    },
    openMergeForm() {
      this.mergeForm = { ...emptyMergeForm(), open: true };
      this.searchMergeCandidates();
    },
    closeMergeForm() {
      this.mergeForm = emptyMergeForm();
    },
    async searchMergeCandidates() {
      const token = Symbol('person-merge-search');
      this._mergeSearchToken = token;
      this.mergeForm.searching = true;
      this.mergeForm.error = '';
      try {
        const results = await ListPeople(this.mergeForm.keyword.trim(), '', 0, 50);
        if (this._mergeSearchToken !== token) return;
        this.mergeForm.results = results || [];
      } catch (err) {
        if (this._mergeSearchToken === token) this.mergeForm.error = `搜索人物失败：${err}`;
      } finally {
        if (this._mergeSearchToken === token) this.mergeForm.searching = false;
      }
    },
    toggleMergeSource(item, selected) {
      const id = this.entityID(item);
      const others = this.mergeForm.selectedIDs.filter(value => value !== id);
      this.mergeForm.selectedIDs = selected ? [...others, id] : others;
      // 选中的人物可能被下一次搜索换掉：把名字留一份，确认框和结果提示要用。
      this._mergeNames = { ...(this._mergeNames || {}), [id]: this.entityName(item) || `人物 #${id}` };
    },
    async submitMerge() {
      const targetID = Number(this.selectedEntity?.id || 0);
      const sourceIDs = [...this.mergeForm.selectedIDs];
      if (!this.isPeople || !targetID || !sourceIDs.length || this.personActionBusy) return;
      const targetName = this.selectedEntityName;
      const names = sourceIDs.map(id => `「${this._mergeNames?.[id] || `人物 #${id}`}」`).join('、');
      const confirmed = await confirmAction({
        title: '合并人物',
        message: `将 ${names} 合并到「${targetName}」？它们的视频、图片关系会并入「${targetName}」（重复的自动去掉），人脸组改指向「${targetName}」，${names} 随后删除。此操作不能撤销。`,
        confirmText: '合并',
        danger: true
      });
      if (!confirmed) return;
      this.personActionBusy = true;
      this.mergeForm.error = '';
      try {
        const result = await MergePeople(targetID, sourceIDs);
        const removed = new Set(sourceIDs);
        this.items = this.items.filter(item => !removed.has(this.entityID(item)));
        if (result?.target && Number(this.selectedEntity?.id) === targetID) {
          this.selectedItem = result.target;
          this.patchSelectedItem();
        }
        notifySuccess(`已把 ${Number(result?.merged_count || sourceIDs.length)} 个人物合并到「${targetName}」：迁移 ${Number(result?.video_links_moved || 0)} 条视频关系、${Number(result?.image_links_moved || 0)} 条图片关系。`);
        // 头像复制在事务之后做，失败只进 warnings（详细设计 §1.2b）：合并本身已经成功。
        if (result?.warnings?.length) notify(`合并已完成，另有提示：${result.warnings.join('；')}`);
        this.closeMergeForm();
        if (Number(this.selectedEntity?.id) === targetID) {
          await this.loadEntityVideos(true);
          if (this.drawerEntity?.type === 'person' && Number(this.drawerEntity.id) === targetID) {
            this.drawerEntity = null; await this.$nextTick();
            if (Number(this.selectedEntity?.id) === targetID) this.drawerEntity = { type: 'person', id: targetID };
          }
        }
      } catch (err) {
        const { code, text } = personErrorText(err, '合并人物失败');
        // 重搜会清空提示，先重搜再写原因。
        if (code === 'person_not_found') await this.searchMergeCandidates();
        this.mergeForm.error = text;
      } finally {
        this.personActionBusy = false;
      }
    },
    // 显式删除人物（META-05）：先读影响范围写进确认框；零关系的人物也能在这里删掉。
    async requestDeletePerson() {
      const personID = Number(this.selectedEntity?.id || 0);
      if (!this.isPeople || !personID || this.personActionBusy) return;
      const name = this.selectedEntityName;
      this.personActionBusy = true;
      this.error = '';
      try {
        let impact;
        try {
          impact = await GetPersonDeletionImpact(personID);
        } catch (err) {
          this.error = personErrorText(err, '读取删除影响失败').text;
          return;
        }
        const faces = Number(impact?.face_cluster_count || 0);
        const confirmed = await confirmAction({
          title: '删除人物',
          message: `删除「${name}」？将解除它与 ${Number(impact?.video_count || 0)} 部视频、${Number(impact?.image_count || 0)} 张图片的关系${faces ? `，${faces} 组人脸回到待命名` : ''}；视频、图片文件和片库记录都会保留。此操作不能撤销。`,
          confirmText: '删除人物',
          danger: true
        });
        if (!confirmed) return;
        await DeletePerson(personID);
        notifySuccess(`已删除人物「${name}」。`);
        this.closeEntity();
        await this.reload();
      } catch (err) {
        const { code, text } = personErrorText(err, '删除人物失败');
        if (code === 'person_not_found') { this.closeEntity(); await this.reload(); }
        this.error = text;
      } finally {
        this.personActionBusy = false;
      }
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
      this.items = []; this.cursorName = ''; this.cursorID = 0; this.hasMore = true; this.listError = '';
      await this.loadMore(requestToken, true);
    },
    async loadMore(requestToken = this._queryToken, force = false) {
      if (!requestToken) { requestToken = Symbol('entity-query'); this._queryToken = requestToken; }
      if ((!force && this.loading) || !this.hasMore || this.selectedEntity) return;
      this.loading = true; this.error = ''; this.listError = '';
      const isPeople = this.isPeople; const keyword = this.keyword; const cursorName = this.cursorName; const cursorID = this.cursorID;
      try {
        const page = isPeople
          ? await ListPeople(keyword, cursorName, cursorID, 50)
          : await ListCollections(keyword, cursorName, cursorID, 50);
        if (this._queryToken !== requestToken) return;
        const next = page || []; this.items.push(...next); this.hasMore = next.length === 50;
        if (next.length) { const last = next[next.length - 1]; this.cursorName = last.cursor_name; this.cursorID = this.entityID(last); }
      } catch (err) {
        // 读失败横幅 + 重试；停住自动翻页（哨兵一直在视口里时不会连发失败的请求），「重试」从头读。
        if (this._queryToken === requestToken) { this.listError = String(err?.message || err || '') || '读取失败'; this.hasMore = false; }
      }
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
      this.syncListKind();
      if (!LIST_KINDS.includes(this.activeTab)) return undefined;
      const known = this.items.find(item => this.entityID(item) === id);
      const fallback = this.isPeople
        ? { person: { id, display_name: focus.name || '' }, avatar_url: '', active_video_count: 0 }
        : { collection: { id, name: focus.name || '' }, cover_url: '', active_video_count: 0 };
      return this.openEntity(known || fallback);
    },
    async openEntity(item) {
      this.selectedItem = item;
      this.selectedEntity = { type: this.listKind, id: this.entityID(item) };
      this.drawerEntity = { ...this.selectedEntity };
      this.entityVideos = []; this.entityVideoCursor = 0; this.collectionVideoPool = []; this.entityVideosHasMore = true;
      this.resetEntityImages();
      await this.loadEntityVideos(true);
      this.$nextTick(() => this.observeSentinel());
    },
    closeEntity() {
      this.selectedEntity = null; this.selectedItem = null; this.drawerEntity = null; this.entityError = '';
      this.entityVideos = []; this.collectionVideoPool = []; this.entityVideosHasMore = false; this.entityVideoCursor = 0;
      this.resetEntityImages();
      this.mergeForm = emptyMergeForm();
      // 对象打开期间人脸审阅改过人物（handleFacesChanged）：回到列表时补读一次。
      if (this._listStale) { this._listStale = false; this.reload(); }
      this.$nextTick(() => this.observeSentinel());
    },
    async loadEntityVideos(reset = false) {
      if (!this.selectedEntity || this.entityVideosLoading || (!reset && !this.entityVideosHasMore)) return;
      const entity = { ...this.selectedEntity }; const token = reset ? Symbol('entity-videos') : this._entityVideosToken;
      this._entityVideosToken = token; this.entityVideosLoading = true; this.error = ''; this.entityError = '';
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
      } catch (err) {
        // 横幅 + 重试（从头重读这个对象）；翻页停住，免得哨兵在视口里时反复发失败的请求。
        if (this._entityVideosToken === token) { this.entityError = String(err?.message || err || '') || '读取失败'; this.entityVideosHasMore = false; }
      }
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
    // 点视频交给 App 按管理区分流（片库管理 + 详情抽屉），不在本页抽屉里开；人物 / 作品集本身的编辑是本页的
    // 「编辑与批量关联」抽屉。
    openVideoDetails(video) { this.$emit('open-video', video); },
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
      catch (err) { this.error = `在访达中显示失败：${err}`; }
    },
    entityID(item) { return Number(this.isPeople ? item?.person?.id : item?.collection?.id); },
    entityName(item) { return this.isPeople ? item?.person?.display_name : item?.collection?.name; },
    entitySecondary(item) { return this.isPeople ? (item?.person?.original_name || '无原始姓名') : (item?.collection?.description || '无简介'); },
    assetURL(item) { return this.isPeople ? item?.avatar_url : item?.cover_url; },
    async previewExternally(video) {
      try { await PreviewExternally(video.id); }
      catch (err) { this.error = `外部预览失败：${err}`; }
    },
    // UpdateVideoWatchProgress 的 5 参契约（D-PC42）：duration 只在库内时长未知时采用，拿不到就传 0；
    // origin 优先用抽屉上报的起播来源。本页的抽屉从不带字幕命中或指定时间打开，会话只会从断点或
    // 片头开始——这两种在后端同样允许回写更早的位置（只有 jump 限制只前进），所以缺省按 resume。
    handleWatchProgress(progress) {
      const videoID = Number(progress?.videoID || 0); if (!videoID) return;
      const origin = WATCH_PROGRESS_ORIGINS.includes(progress?.origin) ? progress.origin : 'resume';
      const reportedDuration = Number(progress?.durationSeconds);
      const duration = Number.isFinite(reportedDuration) && reportedDuration > 0 ? reportedDuration : 0;
      const pending = UpdateVideoWatchProgress(videoID, Number(progress?.positionSeconds || 0), duration, !!progress?.completed, origin);
      const save = () => pending;
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
.entity-image-card__select { display: flex; align-items: center; gap: 6px; font-size: var(--fs-caption); }
.entity-image-card__select input { min-width: 0; width: auto; padding: 0; }
.entity-image-card__preview { display: block; width: 100%; min-width: 0; padding: 0; border: 0; background: transparent; cursor: zoom-in; }
.entity-image-card__actions { display: flex; flex-wrap: wrap; gap: 8px; justify-content: flex-end; }
.entity-library { padding: 14px 18px 28px; display: flex; flex-direction: column; gap: 14px; }.entity-library--with-drawer { padding-right: calc(18px + 520px); }
/* 页头是 PageHeader（下面挂标签）：这里只把它与下面的分区间距交给外层 gap。 */
.entity-library__header { margin-bottom: 0; }
/* 人物 / 作品集两个标签共用的列表与对象区：分区之间的间距与外层一致。 */
.entity-library__panel { display: flex; flex-direction: column; gap: 14px; min-width: 0; }
/* 打开的对象：名字、已加载数量与「编辑与批量关联」等动作用分区级的页头（h2）。 */
.entity-library__entity-head { margin-bottom: 0; }
.entity-library__nav { display: flex; align-items: center; }
.entity-library__create { padding: 10px 16px; border: 1px solid var(--hairline); border-radius: var(--radius-md); background: var(--panel-bg); display: flex; gap: 12px; align-items: center; justify-content: space-between; }
.entity-face-review { display: grid; gap: var(--space-3); min-width: 0; }
.entity-face-review__summary { margin: 0; color: var(--text-secondary); font-size: var(--fs-secondary); }
.entity-library__search,.entity-library__create { display: flex; gap: 8px; }.entity-library input { min-width: 180px; height: var(--h-unit); border: 1px solid var(--border-color); border-radius: var(--radius-sm); padding: 0 10px; color: var(--text-primary); background: var(--control-bg); font-size: var(--fs-secondary); }.entity-library__create { justify-content: flex-start; }.entity-library__create input:nth-child(2) { flex: 1; }
/* 原型 A9：卡片改成竖版，封面在上、名称与作品数在下；列宽随窗口自增。 */
.entity-library__grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 12px; }
.entity-card { display: flex; flex-direction: column; padding: 0; overflow: hidden; border: 1px solid var(--hairline); border-radius: var(--radius-md); background: var(--panel-bg); text-align: left; color: var(--text-primary); cursor: pointer; }
.entity-card:hover { border-color: var(--border-strong); }
.entity-card img,.entity-card__placeholder { width: 100%; height: 150px; object-fit: cover; border-radius: 0; }
.entity-card__placeholder { display: grid; place-items: center; color: var(--text-muted); background: var(--thumb-fallback-bg); }
.entity-card > div:last-child { min-width: 0; display: grid; gap: 3px; padding: 9px 10px 11px; }
.entity-card strong { font-size: var(--fs-secondary); font-weight: var(--fw-semibold); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.entity-card span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-muted); font-size: var(--fs-caption); }
.entity-card small { color: var(--accent-text); font-size: var(--fs-caption); font-weight: var(--fw-semibold); }
.entity-card .entity-card__media-count { white-space: normal; overflow-wrap: anywhere; line-height: 1.5; }
.entity-video-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 12px; }.entity-video-card { min-width: 0; overflow: hidden; border: 1px solid var(--hairline); border-radius: var(--radius-md); background: var(--panel-bg); }.entity-video-card__preview { width: 100%; aspect-ratio: 16 / 9; overflow: hidden; border: 0; background: var(--thumb-bg); cursor: pointer; }.entity-video-card__preview img { width: 100%; height: 100%; display: block; object-fit: cover; }.entity-video-card__copy { display: grid; gap: 4px; min-width: 0; padding: 11px 12px 7px; }.entity-video-card__copy strong,.entity-video-card__copy span,.entity-video-card__copy small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.entity-video-card__copy span,.entity-video-card__copy small { color: var(--text-muted); font-size: var(--fs-caption); }.entity-video-card__actions { display: flex; justify-content: flex-end; gap: 7px; padding: 0 12px 11px; }
.entity-image-section { display: grid; gap: 10px; }
.entity-video-card__actions { flex-wrap: wrap; }
.entity-image-section h3 { margin: 0; font-size: var(--fs-section); }
.entity-image-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); align-items: start; gap: 12px; }
.entity-image-card { margin: 0; min-width: 0; overflow: hidden; display: grid; grid-template-columns: minmax(0, 1fr); gap: 6px; padding: 8px; border: 1px solid var(--hairline); border-radius: var(--radius-md); background: var(--panel-bg); }
.entity-image-card img { width: 100%; height: auto; display: block; object-fit: contain; border-radius: var(--radius-sm); background: var(--thumb-bg); }
.entity-image-card figcaption { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-muted); font-size: var(--fs-caption); }
.entity-image-card__size { color: var(--text-muted); font-size: var(--fs-caption); font-variant-numeric: tabular-nums; }
.entity-library__more { align-self: center; }.entity-library__sentinel { height: 1px; }.entity-library__error { color: var(--danger-text); }
.entity-person-merge { display: grid; gap: 10px; padding: 12px 14px; border: 1px solid var(--hairline); border-radius: var(--radius-md); background: var(--panel-bg); }
.entity-person-merge > p { margin: 0; color: var(--text-secondary); font-size: var(--fs-caption); line-height: 1.5; }
.entity-person-merge__options { display: grid; gap: 4px; max-height: 240px; overflow-y: auto; }
.entity-person-merge__option { display: grid; grid-template-columns: auto minmax(0, max-content) minmax(0, 1fr); align-items: center; gap: 8px; padding: 6px 8px; border-radius: var(--radius-sm); cursor: pointer; font-size: var(--fs-secondary); }
.entity-person-merge__option:hover { background: var(--control-hover-bg); }
.entity-person-merge__option input { min-width: 0; width: auto; padding: 0; }
.entity-person-merge__option small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-muted); font-size: var(--fs-caption); }
.entity-person-merge__empty { margin: 0; color: var(--text-muted); font-size: var(--fs-caption); }
.entity-person-merge__actions { display: flex; justify-content: flex-end; gap: 8px; }
@media (max-width: 1100px) { .entity-library--with-drawer { padding-right: 18px; } }
@media (max-width: 900px) {.entity-library__create { align-items: stretch; flex-direction: column; }.entity-library__search { display: flex; }.entity-video-grid { grid-template-columns: repeat(auto-fill, minmax(230px, 1fr)); } }
</style>
