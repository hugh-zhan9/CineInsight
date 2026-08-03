<template>
  <aside class="preview-drawer glass-surface" role="dialog" aria-label="媒体详情抽屉">
    <div class="preview-drawer__header glass-drawer-header">
      <div class="preview-drawer__heading">
        <button v-if="canGoBack" type="button" class="btn-secondary btn-compact" @click="goBack">返回</button>
        <div>
          <p class="preview-drawer__eyebrow">{{ entryLabel }}</p>
          <h3>{{ entryTitle }}</h3>
        </div>
      </div>
      <button type="button" class="preview-drawer__close btn-secondary btn-compact" @click="$emit('close')">关闭</button>
    </div>

    <div class="preview-drawer__body">
      <div v-if="loading" class="preview-drawer__placeholder">正在读取本地详情...</div>
      <div v-else-if="error" class="detail-error" role="alert">
        <p>{{ error }}</p>
        <button type="button" class="btn-secondary" @click="loadCurrentEntry">重试</button>
      </div>

      <template v-else-if="currentEntry?.type === 'video' && details">
        <section class="detail-section detail-section--player">
          <div v-if="!currentSession" class="preview-drawer__placeholder">正在准备预览...</div>
          <template v-else-if="currentSession.mode === 'inline' && currentSession.inline_source">
            <div class="preview-drawer__player-shell">
              <video
                ref="videoElement"
                class="preview-drawer__video"
                controls
                playsinline
                preload="metadata"
                :muted="true"
                @loadedmetadata="handleLoadedMetadata"
                @play="hasPlaybackStarted = true"
                @timeupdate="handleTimeUpdate"
                @pause="emitWatchProgress(true, false)"
                @ended="emitWatchProgress(true, true)"
              >
                <source :src="currentSession.inline_source.locator_value" :type="currentSession.inline_source.mime" />
              </video>
            </div>
          </template>
          <template v-else-if="currentSession.mode === 'external-preview' && currentSession.external_action">
            <div class="preview-drawer__placeholder">
              <p>{{ currentSession.reason_message }}</p>
              <button type="button" class="btn-primary" @click="$emit('preview-externally', details.video)">{{ currentSession.external_action.button_label }}</button>
            </div>
          </template>
          <div v-else class="preview-drawer__placeholder">{{ currentSession.reason_message || '当前视频暂不支持预览。' }}</div>
        </section>

        <section class="detail-section">
          <div class="detail-section__heading"><h4>作品信息</h4><span class="detail-readonly-hint">文件名不会被修改</span></div>
          <label class="detail-field">显示标题<input v-model="draft.displayTitle" maxlength="255" /></label>
          <label class="detail-field">原始标题<input v-model="draft.originalTitle" maxlength="255" /></label>
          <label class="detail-field">简介<textarea v-model="draft.description" maxlength="65536" rows="5"></textarea></label>
          <label class="detail-field">个人评分
            <span class="detail-rating-input">
              <input v-model.trim="draft.personalRating" type="text" inputmode="decimal" maxlength="4" placeholder="未评分" aria-label="个人评分，0 到 10，支持 0.5 分" />
              <span aria-hidden="true">/ 10</span>
            </span>
            <small>输入 0–10，支持半分；留空表示未评分</small>
          </label>
          <p class="detail-secondary">原文件：{{ details.video.name }}</p>
          <div class="detail-inline-actions">
            <button type="button" class="btn-primary" :disabled="saving" @click="saveVideoDetails">{{ saving ? '保存中...' : '保存作品信息' }}</button>
            <button type="button" class="btn-secondary" @click="$emit('open-local-metadata', details.video)">导入本地资料</button>
          </div>
        </section>

        <section class="detail-section">
          <div class="detail-section__heading"><h4>演员</h4><span>{{ draft.personIDs.length }} 人</span></div>
          <div class="entity-chip-list">
            <button v-for="item in selectedPeople" :key="item.person.id" type="button" class="entity-chip" @click="openPerson(item.person.id)">
              <img v-if="item.avatar_url" :src="item.avatar_url" alt="" />
              <span>{{ item.person.display_name }}</span>
              <span class="entity-chip__remove" title="移除" @click.stop="togglePerson(item.person.id, false)">×</span>
            </button>
          </div>
          <div class="detail-inline-form">
            <input v-model="personKeyword" placeholder="搜索人物姓名" @input="searchPeople" />
            <button type="button" class="btn-secondary btn-compact" @click="searchPeople">搜索</button>
          </div>
          <div v-if="personCandidates.length" class="candidate-list">
            <button v-for="item in personCandidates" :key="item.person.id" type="button" @click="togglePerson(item.person.id, true)">
              <span>{{ item.person.display_name }}</span><small>{{ item.person.original_name || '无原始姓名' }} · {{ item.active_video_count }} 部作品</small>
            </button>
          </div>
          <details class="detail-create-box">
            <summary>明确新建人物（允许同名）</summary>
            <input v-model="newPerson.displayName" placeholder="显示姓名" maxlength="200" />
            <input v-model="newPerson.originalName" placeholder="原始姓名（可空）" maxlength="200" />
            <button type="button" class="btn-secondary" :disabled="creatingPerson" @click="createAndSelectPerson">{{ creatingPerson ? '创建中...' : '新建并加入' }}</button>
          </details>
        </section>

        <section class="detail-section">
          <div class="detail-section__heading"><h4>作品集</h4><span>可多选</span></div>
          <div class="detail-inline-form">
            <input v-model="collectionKeyword" placeholder="搜索作品集名称或简介" @keyup.enter="searchCollections(true)" />
            <button type="button" class="btn-secondary btn-compact" :disabled="collectionSearching" @click="searchCollections(true)">搜索</button>
          </div>
          <label v-for="item in collectionCandidates" :key="item.collection.id" class="selection-row">
            <input type="checkbox" :checked="draft.collectionIDs.includes(item.collection.id)" @change="toggleCollection(item.collection.id, $event.target.checked)" />
            <button type="button" @click.prevent="openCollection(item.collection.id)">{{ item.collection.name }}</button>
            <small>{{ item.active_video_count }} 部</small>
          </label>
          <button v-if="collectionHasMore" type="button" class="btn-secondary" :disabled="collectionSearching" @click="searchCollections(false)">加载更多作品集</button>
        </section>

        <section class="detail-section">
          <div class="detail-section__heading">
            <h4>技术信息</h4>
            <button type="button" class="btn-secondary btn-compact" :disabled="refreshingTechnical" @click="refreshTechnical">{{ refreshingTechnical ? '读取中...' : '重新读取' }}</button>
          </div>
          <p class="technical-status" :class="`technical-status--${details.technical_status?.state}`">{{ technicalStatusLabel }}</p>
          <p v-if="technicalError || details.technical_metadata?.last_error" class="detail-error-text">最近错误：{{ technicalError || details.technical_metadata.last_error }}</p>
          <dl class="technical-grid">
            <dt>容器</dt><dd>{{ details.technical_metadata?.format_long_name || details.technical_metadata?.format_name || '未知' }}</dd>
            <dt>大小</dt><dd>{{ formatBytes(details.video.size) }}</dd>
            <dt>时长</dt><dd>{{ formatDuration(details.video.duration) || '未知' }}</dd>
            <dt>总码率</dt><dd>{{ formatBitRate(details.technical_metadata?.total_bit_rate) }}</dd>
            <dt>快照时间</dt><dd>{{ formatDateTime(details.technical_metadata?.probed_at) }}</dd>
            <dt>修改时间</dt><dd>{{ formatNanosecondTime(details.technical_metadata?.successful_source_mod_time_ns) }}</dd>
          </dl>
          <article v-for="stream in details.streams || []" :key="stream.stream_index" class="stream-card">
            <strong>{{ streamTypeLabel(stream.stream_type) }} #{{ stream.stream_index }}</strong>
            <span>{{ stream.codec_long_name || stream.codec_name || '未知编码' }}</span>
            <span v-if="stream.stream_type === 'video'">{{ stream.width || '?' }}×{{ stream.height || '?' }} · {{ formatFrameRate(stream.avg_frame_rate, stream.real_frame_rate) }} · {{ stream.pixel_format || '未知像素格式' }}</span>
            <span v-if="stream.stream_type === 'video'">位深 {{ stream.bits_per_raw_sample ?? '未知' }} · HDR {{ stream.is_hdr === null || stream.is_hdr === undefined ? '未知' : (stream.is_hdr ? '是' : '否') }}</span>
            <span v-if="stream.stream_type === 'audio'">{{ stream.channels ?? '未知' }} 声道 · {{ stream.sample_rate ? `${stream.sample_rate} Hz` : '未知采样率' }}</span>
            <span v-if="stream.stream_type !== 'video'">{{ stream.language || '未知语言' }}{{ stream.title ? ` · ${stream.title}` : '' }}{{ stream.is_default ? ' · 默认' : '' }}</span>
          </article>
          <article v-if="details.external_subtitle" class="stream-card">
            <strong>外置字幕</strong><span>{{ details.external_subtitle.path }}</span>
            <span>{{ details.external_subtitle.language }} · {{ details.external_subtitle.segment_count }} 段 · 最后索引 {{ details.external_subtitle.last_segment_index }}</span>
          </article>
          <p v-if="!(details.streams || []).length && !details.external_subtitle" class="detail-empty">尚未读取到流信息。</p>
        </section>
      </template>

      <template v-else-if="currentEntry?.type === 'person' && personDetail">
        <section class="detail-section entity-identity">
          <img v-if="personDetail.person.avatar_url" :src="personDetail.person.avatar_url" :alt="personDetail.person.person.display_name" />
          <div class="entity-avatar-placeholder" v-else>人物</div>
          <label class="detail-field">显示姓名<input v-model="personEdit.displayName" maxlength="200" /></label>
          <label class="detail-field">原始姓名<input v-model="personEdit.originalName" maxlength="200" /></label>
          <div class="detail-action-row">
            <button type="button" class="btn-primary" @click="savePerson">保存姓名</button>
            <button type="button" class="btn-secondary" @click="replacePersonAvatar">更换头像</button>
            <button v-if="personDetail.person.avatar_url" type="button" class="btn-secondary" @click="removePersonAvatar">移除头像</button>
          </div>
        </section>
        <section class="detail-section">
          <div class="detail-section__heading"><h4>关联视频（{{ personDetail.person.active_video_count }}）</h4><span>点击卡片查看详情</span></div>
          <div class="related-video-editor">
            <div class="related-video-directory-filter">
              <select v-model="relatedVideoDirectory" class="select-input" aria-label="按文件夹筛选可关联视频" @change="searchRelatedVideos(true)">
                <option value="">全部文件夹</option>
                <option v-if="customRelatedVideoDirectory" :value="customRelatedVideoDirectory">已选：{{ customRelatedVideoDirectory }}</option>
                <option v-for="directory in relatedVideoDirectories" :key="directory.id" :value="directory.path">{{ directory.alias || directory.path }}</option>
              </select>
              <button type="button" class="btn-secondary btn-compact" data-test="related-video-folder-picker" :disabled="relatedVideoSearching" @click="chooseRelatedVideoDirectory">选择子文件夹</button>
            </div>
            <p class="related-video-directory-hint">选择父文件夹会包含其中全部子文件夹的已收录视频。</p>
            <div class="detail-inline-form">
              <input v-model="relatedVideoKeyword" placeholder="搜索标题、文件名或路径" @keyup.enter="searchRelatedVideos(true)" />
              <button type="button" class="btn-secondary btn-compact" :disabled="relatedVideoSearching" @click="searchRelatedVideos(true)">{{ relatedVideoSearching ? '搜索中...' : '搜索视频' }}</button>
            </div>
            <div v-if="availableRelatedVideoCandidates.length" class="related-video-batch-actions">
              <button type="button" class="btn-action btn-compact" @click="selectAllRelatedVideoCandidates">全选已加载</button>
              <button type="button" class="btn-primary btn-compact" :disabled="relatedVideoSelection.length === 0 || relatedVideoSearching" @click="addSelectedRelatedVideos">批量关联（{{ relatedVideoSelection.length }}）</button>
            </div>
            <p v-if="relatedVideoError" class="detail-error-text">{{ relatedVideoError }}</p>
            <div v-if="availableRelatedVideoCandidates.length" class="related-video-results">
              <RelatedVideoItem
                v-for="candidate in availableRelatedVideoCandidates"
                :key="candidate.id"
                :video="candidate"
                selectable
                :selected="relatedVideoSelection.includes(Number(candidate.id))"
                @open="openVideo(candidate.id)"
                @select="toggleRelatedVideoSelection(candidate.id, $event)"
              />
            </div>
            <button v-if="relatedVideoHasMore" type="button" class="btn-secondary" :disabled="relatedVideoSearching" @click="searchRelatedVideos(false)">{{ relatedVideoSearching ? '加载中...' : '加载更多搜索结果' }}</button>
            <p v-if="relatedVideoSearchPerformed && !relatedVideoSearching && availableRelatedVideoCandidates.length === 0" class="detail-empty">没有可关联的视频。</p>
          </div>
          <div class="related-video-list">
            <RelatedVideoItem
              v-for="related in personDetail.videos || []"
              :key="related.id"
              :video="related"
              :action-label="isRelatedVideoUpdating(related.id) ? '处理中...' : '解除关联'"
              :action-disabled="isRelatedVideoUpdating(related.id)"
              destructive
              @open="openVideo(related.id)"
              @action="removeRelatedVideo(related)"
            />
          </div>
          <p v-if="!(personDetail.videos || []).length" class="detail-empty">当前没有活跃关联视频。软删除视频的关系仍会保留。</p>
        </section>
      </template>

      <template v-else-if="currentEntry?.type === 'collection' && collectionDetail">
        <section class="detail-section entity-identity">
          <img v-if="collectionDetail.collection.cover_url" :src="collectionDetail.collection.cover_url" :alt="collectionDetail.collection.collection.name" />
          <div class="entity-avatar-placeholder" v-else>作品集</div>
          <label class="detail-field">名称<input v-model="collectionEdit.name" maxlength="200" /></label>
          <label class="detail-field">简介<textarea v-model="collectionEdit.description" maxlength="4000" rows="4"></textarea></label>
          <div class="detail-action-row">
            <button type="button" class="btn-primary" @click="saveCollection">保存</button>
            <button type="button" class="btn-secondary" @click="replaceCollectionCover">更换封面</button>
            <button v-if="collectionDetail.collection.cover_url" type="button" class="btn-secondary" @click="removeCollectionCover">移除封面</button>
            <button type="button" class="btn-danger" @click="deleteCollection">删除作品集</button>
          </div>
        </section>
        <section class="detail-section">
          <div class="detail-section__heading"><h4>成员顺序</h4><span>拖拽调整</span></div>
          <div class="related-video-editor">
            <div class="related-video-directory-filter">
              <select v-model="relatedVideoDirectory" class="select-input" aria-label="按文件夹筛选可加入视频" @change="searchRelatedVideos(true)">
                <option value="">全部文件夹</option>
                <option v-if="customRelatedVideoDirectory" :value="customRelatedVideoDirectory">已选：{{ customRelatedVideoDirectory }}</option>
                <option v-for="directory in relatedVideoDirectories" :key="directory.id" :value="directory.path">{{ directory.alias || directory.path }}</option>
              </select>
              <button type="button" class="btn-secondary btn-compact" data-test="related-video-folder-picker" :disabled="relatedVideoSearching" @click="chooseRelatedVideoDirectory">选择子文件夹</button>
            </div>
            <p class="related-video-directory-hint">选择父文件夹会包含其中全部子文件夹的已收录视频。</p>
            <div class="detail-inline-form">
              <input v-model="relatedVideoKeyword" placeholder="搜索标题、文件名或路径" @keyup.enter="searchRelatedVideos(true)" />
              <button type="button" class="btn-secondary btn-compact" :disabled="relatedVideoSearching" @click="searchRelatedVideos(true)">{{ relatedVideoSearching ? '搜索中...' : '搜索视频' }}</button>
            </div>
            <div v-if="availableRelatedVideoCandidates.length" class="related-video-batch-actions">
              <button type="button" class="btn-action btn-compact" @click="selectAllRelatedVideoCandidates">全选已加载</button>
              <button type="button" class="btn-primary btn-compact" :disabled="relatedVideoSelection.length === 0 || relatedVideoSearching" @click="addSelectedRelatedVideos">批量加入（{{ relatedVideoSelection.length }}）</button>
            </div>
            <p v-if="relatedVideoError" class="detail-error-text">{{ relatedVideoError }}</p>
            <div v-if="availableRelatedVideoCandidates.length" class="related-video-results">
              <RelatedVideoItem
                v-for="candidate in availableRelatedVideoCandidates"
                :key="candidate.id"
                :video="candidate"
                selectable
                :selected="relatedVideoSelection.includes(Number(candidate.id))"
                @open="openVideo(candidate.id)"
                @select="toggleRelatedVideoSelection(candidate.id, $event)"
              />
            </div>
            <button v-if="relatedVideoHasMore" type="button" class="btn-secondary" :disabled="relatedVideoSearching" @click="searchRelatedVideos(false)">{{ relatedVideoSearching ? '加载中...' : '加载更多搜索结果' }}</button>
            <p v-if="relatedVideoSearchPerformed && !relatedVideoSearching && availableRelatedVideoCandidates.length === 0" class="detail-empty">没有可加入的视频。</p>
          </div>
          <div class="related-video-list">
            <RelatedVideoItem
              v-for="(member, index) in collectionDetail.videos || []"
              :key="member.video.id"
              :video="member.video"
              :position-label="`${index + 1}. `"
              :action-label="isRelatedVideoUpdating(member.video.id) ? '处理中...' : '移出'"
              :action-disabled="isRelatedVideoUpdating(member.video.id)"
              destructive
              draggable
              @dragstart="draggedMemberIndex = index"
              @drop="dropCollectionMember(index)"
              @open="openVideo(member.video.id)"
              @action="removeRelatedVideo(member.video)"
            />
          </div>
          <p v-if="!(collectionDetail.videos || []).length" class="detail-empty">作品集尚无视频。</p>
        </section>
      </template>
    </div>
  </aside>
</template>

<script>
import {
  AddCollectionVideo, AddCollectionVideos, AddPersonVideo, AddPersonVideos, CreatePerson, DeleteCollection, GetAllDirectories, GetCollectionDetail, GetPersonDetail, GetPreviewSession, GetVideoDetails, ListCollections, ListPeople,
  RefreshVideoTechnicalMetadata, RemoveCollectionCover, RemoveCollectionVideo, RemovePersonAvatar, RemovePersonVideo, ReorderCollectionVideos, SearchLibraryVideoPage,
  SelectCollectionCover, SelectDirectory, SelectPersonAvatar, SetCollectionCover, SetPersonAvatar, UpdateCollection, UpdatePerson, UpdateVideoDetails
} from '../../wailsjs/go/main/App';
import { createDetailNavigator, createVideoDetailsDraft, detailPlaybackStartMs, formatFrameRate as formatFrameRateValue, mergeCollectionCandidates, mergePersonCandidates, moveCollectionMember, toggleEntityID, validateRatingDraft } from '../utils/mediaDetails.js';
import RelatedVideoItem from './RelatedVideoItem.vue';

export default {
  name: 'PreviewDrawer',
  components: { RelatedVideoItem },
  props: {
    video: { type: Object, default: null },
    initialEntity: { type: Object, default: null },
    session: { type: Object, default: null },
    startTimeMs: { type: Number, default: null },
    resumePositionSeconds: { type: Number, default: 0 }
  },
  emits: ['close', 'preview-externally', 'watch-progress', 'details-updated', 'collection-deleted', 'person-deleted', 'relations-updated', 'open-local-metadata'],
  data() {
    return {
      navigator: null, currentEntry: null, canGoBack: false,
      loading: false, error: '', saving: false, refreshingTechnical: false,
      details: null, nestedSession: null, technicalError: '', draft: { displayTitle: '', originalTitle: '', description: '', personalRating: '', personIDs: [], collectionIDs: [] },
      personKeyword: '', personCandidates: [], creatingPerson: false, collectionKeyword: '', collectionCandidates: [], collectionCursorName: '', collectionCursorID: 0, collectionHasMore: false, collectionSearching: false, newPerson: { displayName: '', originalName: '' },
      personDetail: null, personEdit: { displayName: '', originalName: '' },
      collectionDetail: null, collectionEdit: { name: '', description: '' }, draggedMemberIndex: -1,
      relatedVideoKeyword: '', relatedVideoDirectory: '', relatedVideoDirectories: [], relatedVideoCandidates: [], relatedVideoSelection: [], relatedVideoCursor: null, relatedVideoHasMore: false, relatedVideoSearching: false, relatedVideoSearchPerformed: false, relatedVideoUpdatingIDs: [], relatedVideoError: '',
      appliedSeekKey: '', lastProgressEmittedAt: 0, resettingVideo: false, hasPlaybackStarted: false
    };
  },
  computed: {
    currentSession() {
      if (this.currentEntry?.type !== 'video') return null;
      return Number(this.currentEntry.id) === Number(this.video?.id) ? this.session : this.nestedSession;
    },
    entryLabel() { return { video: '视频详情', person: '人物详情', collection: '作品集详情' }[this.currentEntry?.type] || '本地媒体详情'; },
    entryTitle() {
      if (this.currentEntry?.type === 'video') return this.details?.effective_title || this.video?.display_title || this.video?.name || '视频详情';
      if (this.currentEntry?.type === 'person') return this.personDetail?.person?.person?.display_name || '人物详情';
      return this.collectionDetail?.collection?.collection?.name || '作品集详情';
    },
    selectedPeople() {
      const byID = new Map([...(this.details?.people || []), ...this.personCandidates].map(item => [Number(item?.person?.id), item]));
      return this.draft.personIDs.map(id => byID.get(Number(id))).filter(Boolean);
    },
    relatedVideoIDs() {
      if (this.currentEntry?.type === 'person') return (this.personDetail?.videos || []).map(video => Number(video.id));
      if (this.currentEntry?.type === 'collection') return (this.collectionDetail?.videos || []).map(item => Number(item.video?.id));
      return [];
    },
    availableRelatedVideoCandidates() {
      const related = new Set(this.relatedVideoIDs);
      return this.relatedVideoCandidates.filter(video => !related.has(Number(video.id)));
    },
    customRelatedVideoDirectory() {
      const selected = String(this.relatedVideoDirectory || '').trim();
      if (!selected || this.relatedVideoDirectories.some(directory => directory.path === selected)) return '';
      return selected;
    },
    technicalStatusLabel() {
      const state = this.details?.technical_status?.state;
      return { unprobed: '尚未读取', current: '快照与当前文件一致', stale: '文件已变化，快照可能过期', error: '最近读取失败' }[state] || '状态未知';
    }
  },
  watch: {
    'video.id'() { if (!this.initialEntity) this.resetRootEntry(); },
    initialEntity: { deep: true, handler() { if (this.initialEntity) this.resetRootEntry(); } },
    currentSession: {
      immediate: true,
      handler(newSession, oldSession) {
        if (oldSession) { this.emitWatchProgress(true, false, oldSession?.video_id); this.resetVideoElement(); }
        this.appliedSeekKey = '';
        this.$nextTick(() => this.configureVideoElement());
      }
    },
    startTimeMs() { this.appliedSeekKey = ''; this.$nextTick(() => this.configureVideoElement()); }
  },
  mounted() { this.loadRelatedVideoDirectories(); this.resetRootEntry(); },
  beforeUnmount() { this.emitWatchProgress(true, false); this.resetVideoElement(); },
  methods: {
    async loadRelatedVideoDirectories() {
      try { this.relatedVideoDirectories = await GetAllDirectories() || []; }
      catch (err) { this.relatedVideoDirectories = []; }
    },
    async chooseRelatedVideoDirectory() {
      try {
        const directory = await SelectDirectory();
        if (!directory) return;
        this.relatedVideoDirectory = directory;
        await this.searchRelatedVideos(true);
      } catch (err) {
        this.relatedVideoError = '选择文件夹失败：' + err;
      }
    },
    resetRootEntry() {
      const root = this.initialEntity?.type && this.initialEntity?.id
        ? { type: this.initialEntity.type, id: Number(this.initialEntity.id) }
        : (this.video?.id ? { type: 'video', id: Number(this.video.id) } : null);
      if (!root) return;
      this.resetRelatedVideoEditor(); this.navigator = createDetailNavigator(root); this.currentEntry = root; this.canGoBack = false; this.loadCurrentEntry();
    },
    async loadCurrentEntry() {
      if (!this.currentEntry) return;
      const entry = { ...this.currentEntry };
      const requestToken = Symbol('detail-entry');
      this._entryLoadToken = requestToken;
      this.loading = true; this.error = '';
      try {
        if (entry.type === 'video') {
          const [details, nestedSession, collections] = await Promise.all([
            GetVideoDetails(entry.id),
            Number(entry.id) === Number(this.video?.id) ? null : GetPreviewSession(entry.id),
            ListCollections('', '', 0, 50)
          ]);
          if (this._entryLoadToken !== requestToken) return;
          this.details = details;
          this.technicalError = '';
          this.nestedSession = nestedSession;
          this.draft = createVideoDetailsDraft(details);
          this._personSearchToken = Symbol('person-search'); this.personCandidates = [...(details.people || [])];
          this.collectionKeyword = ''; this._collectionSearchToken = Symbol('collection-search'); this.collectionSearching = false;
          this.collectionCandidates = mergeCollectionCandidates(details.collections, collections, this.draft.collectionIDs);
          this.updateCollectionCursor(collections || []);
          this.$nextTick(() => this.configureVideoElement());
        } else if (entry.type === 'person') {
          const personDetail = await this.loadCompletePersonDetail(entry.id, requestToken);
          if (this._entryLoadToken !== requestToken) return;
          this.personDetail = personDetail;
          this.personEdit = { displayName: personDetail.person.person.display_name || '', originalName: personDetail.person.person.original_name || '' };
        } else {
          const collectionDetail = await GetCollectionDetail(entry.id);
          if (this._entryLoadToken !== requestToken) return;
          this.collectionDetail = collectionDetail;
          this.collectionEdit = { name: collectionDetail.collection.collection.name || '', description: collectionDetail.collection.collection.description || '' };
        }
      } catch (err) { if (this._entryLoadToken === requestToken) this.error = String(err); }
      finally { if (this._entryLoadToken === requestToken) this.loading = false; }
    },
    async loadCompletePersonDetail(personID, requestToken) {
      let detail = await GetPersonDetail(personID, 0, 200);
      while (detail.next_video_id && this._entryLoadToken === requestToken) {
        const page = await GetPersonDetail(personID, detail.next_video_id, 200);
        detail = { ...detail, videos: [...(detail.videos || []), ...(page.videos || [])], next_video_id: page.next_video_id };
      }
      return detail;
    },
    async navigate(entry) { this.resetRelatedVideoEditor(); this.navigator.push(entry); this.currentEntry = this.navigator.current(); this.canGoBack = this.navigator.canGoBack(); await this.loadCurrentEntry(); },
    async goBack() { this.resetRelatedVideoEditor(); this.currentEntry = this.navigator.back(); this.canGoBack = this.navigator.canGoBack(); await this.loadCurrentEntry(); },
    openPerson(id) { return this.navigate({ type: 'person', id }); },
    openCollection(id) { return this.navigate({ type: 'collection', id }); },
    openVideo(id) { return this.navigate({ type: 'video', id }); },
    isCurrentVideoRequest(videoID, entryToken) {
      return this._entryLoadToken === entryToken && this.currentEntry?.type === 'video' && Number(this.currentEntry.id) === Number(videoID);
    },
    async saveVideoDetails() {
      this.saving = true; this.error = '';
      const videoID = this.details.video.id; const entryToken = this._entryLoadToken; const operationToken = Symbol('video-save');
      this._videoSaveToken = operationToken;
      try {
        const updatedDetails = await UpdateVideoDetails({
          video_id: videoID, display_title: this.draft.displayTitle, original_title: this.draft.originalTitle,
          description: this.draft.description,
          personal_rating: validateRatingDraft(this.draft.personalRating), person_ids: [...this.draft.personIDs], collection_ids: [...this.draft.collectionIDs]
        });
        this.$emit('details-updated', updatedDetails);
        if (this.isCurrentVideoRequest(videoID, entryToken)) {
          this.details = updatedDetails; this.draft = createVideoDetailsDraft(updatedDetails);
        }
      } catch (err) { if (this.isCurrentVideoRequest(videoID, entryToken)) this.error = String(err); }
      finally { if (this._videoSaveToken === operationToken) this.saving = false; }
    },
    async searchPeople() {
      const requestToken = Symbol('person-search'); this._personSearchToken = requestToken; const keyword = this.personKeyword;
      try {
        const results = await ListPeople(keyword, '', 0, 20);
        if (this._personSearchToken === requestToken) this.personCandidates = mergePersonCandidates(this.personCandidates, results, this.draft.personIDs);
      } catch (err) { if (this._personSearchToken === requestToken) this.error = String(err); }
    },
    togglePerson(id, force) { this.draft.personIDs = toggleEntityID(this.draft.personIDs, id, force); },
    toggleCollection(id, force) { this.draft.collectionIDs = toggleEntityID(this.draft.collectionIDs, id, force); },
    resetRelatedVideoEditor() {
      this._relatedVideoSearchToken = Symbol('related-video-search');
      this.relatedVideoKeyword = ''; this.relatedVideoDirectory = ''; this.relatedVideoCandidates = []; this.relatedVideoSelection = []; this.relatedVideoCursor = null; this.relatedVideoHasMore = false; this.relatedVideoSearching = false;
      this.relatedVideoSearchPerformed = false; this.relatedVideoUpdatingIDs = []; this.relatedVideoError = '';
    },
    async searchRelatedVideos(reset = true) {
      if (!['person', 'collection'].includes(this.currentEntry?.type)) return;
      if (!reset && (!this.relatedVideoHasMore || this.relatedVideoSearching)) return;
      const requestToken = reset ? Symbol('related-video-search') : this._relatedVideoSearchToken;
      this._relatedVideoSearchToken = requestToken;
      this.relatedVideoSearching = true; this.relatedVideoError = '';
      try {
        const request = {
          filter: {
            search_mode: 'file', keyword: this.relatedVideoKeyword, path_prefix: this.relatedVideoDirectory, smart_view: '', tag_ids: [],
            min_size: 0, max_size: 0, min_height: 0, max_height: 0,
            min_rating: null, max_rating: null, sort_mode: 'balanced'
          },
          limit: 30
        };
        if (!reset && this.relatedVideoCursor) request.cursor = this.relatedVideoCursor;
        const page = await SearchLibraryVideoPage(request);
        if (this._relatedVideoSearchToken !== requestToken) return;
        const incoming = page?.videos || [];
        const combined = reset ? incoming : [...this.relatedVideoCandidates, ...incoming];
        this.relatedVideoCandidates = [...new Map(combined.map(video => [Number(video.id), video])).values()];
        this.relatedVideoCursor = page?.next_cursor || null;
        this.relatedVideoHasMore = !!page?.next_cursor;
        if (reset) this.relatedVideoSelection = [];
        this.relatedVideoSearchPerformed = true;
      } catch (err) {
        if (this._relatedVideoSearchToken === requestToken) this.relatedVideoError = `搜索视频失败：${err}`;
      } finally {
        if (this._relatedVideoSearchToken === requestToken) this.relatedVideoSearching = false;
      }
    },
    toggleRelatedVideoSelection(videoID, selected) {
      const id = Number(videoID);
      this.relatedVideoSelection = selected
        ? [...new Set([...this.relatedVideoSelection, id])]
        : this.relatedVideoSelection.filter(item => Number(item) !== id);
    },
    selectAllRelatedVideoCandidates() {
      this.relatedVideoSelection = [...new Set([
        ...this.relatedVideoSelection,
        ...this.availableRelatedVideoCandidates.map(video => Number(video.id))
      ])];
    },
    async addSelectedRelatedVideos() {
      const type = this.currentEntry?.type; const entityID = Number(this.currentEntry?.id);
      const videoIDs = [...new Set(this.relatedVideoSelection.map(Number).filter(id => id > 0 && !this.relatedVideoIDs.includes(id)))];
      if (!entityID || videoIDs.length === 0 || !['person', 'collection'].includes(type)) return;
      this.relatedVideoUpdatingIDs = [...new Set([...this.relatedVideoUpdatingIDs, ...videoIDs])]; this.relatedVideoError = '';
      try {
        if (type === 'person') await AddPersonVideos(entityID, videoIDs);
        else await AddCollectionVideos(entityID, videoIDs);
        this.relatedVideoSelection = [];
        this.$emit('relations-updated', { type, id: entityID });
        if (this.isCurrentEntity(type, entityID)) await this.loadCurrentEntry();
      } catch (err) { if (this.isCurrentEntity(type, entityID)) this.relatedVideoError = `批量关联视频失败：${err}`; }
      finally { this.relatedVideoUpdatingIDs = this.relatedVideoUpdatingIDs.filter(id => !videoIDs.includes(Number(id))); }
    },
    isRelatedVideoUpdating(videoID) { return this.relatedVideoUpdatingIDs.includes(Number(videoID)); },
    isCurrentEntity(type, entityID) { return this.currentEntry?.type === type && Number(this.currentEntry?.id) === Number(entityID); },
    setRelatedVideoUpdating(videoID, updating) {
      const id = Number(videoID);
      this.relatedVideoUpdatingIDs = updating
        ? [...new Set([...this.relatedVideoUpdatingIDs, id])]
        : this.relatedVideoUpdatingIDs.filter(item => item !== id);
    },
    async addRelatedVideo(video) {
      const type = this.currentEntry?.type; const entityID = Number(this.currentEntry?.id); const videoID = Number(video?.id);
      if (!entityID || !videoID || this.isRelatedVideoUpdating(videoID) || !['person', 'collection'].includes(type)) return;
      this.setRelatedVideoUpdating(videoID, true); this.relatedVideoError = '';
      try {
        if (type === 'person') await AddPersonVideo(entityID, videoID);
        else await AddCollectionVideo(entityID, videoID);
        this.$emit('relations-updated', { type, id: entityID });
        if (this.isCurrentEntity(type, entityID)) await this.loadCurrentEntry();
      } catch (err) { if (this.isCurrentEntity(type, entityID)) this.relatedVideoError = `关联视频失败：${err}`; }
      finally { this.setRelatedVideoUpdating(videoID, false); }
    },
    async removeRelatedVideo(video) {
      const type = this.currentEntry?.type; const entityID = Number(this.currentEntry?.id); const videoID = Number(video?.id);
      if (!entityID || !videoID || this.isRelatedVideoUpdating(videoID) || !['person', 'collection'].includes(type)) return;
      if (type === 'person' && Number(this.personDetail?.person?.active_video_count || 0) <= 1 && !window.confirm('这是该人物最后一个活跃关联视频。若没有软删除视频保留的关系，解除后人物也会被删除，确定继续吗？')) return;
      this.setRelatedVideoUpdating(videoID, true); this.relatedVideoError = '';
      try {
        if (type === 'person') {
          const personDeleted = await RemovePersonVideo(entityID, videoID);
          if (personDeleted) {
            this.$emit('person-deleted', entityID);
            if (this.isCurrentEntity(type, entityID)) {
              if (this.canGoBack) await this.goBack(); else this.$emit('close');
            }
            return;
          }
        } else {
          await RemoveCollectionVideo(entityID, videoID);
        }
        this.$emit('relations-updated', { type, id: entityID });
        if (this.isCurrentEntity(type, entityID)) await this.loadCurrentEntry();
      } catch (err) { if (this.isCurrentEntity(type, entityID)) this.relatedVideoError = `解除视频关联失败：${err}`; }
      finally { this.setRelatedVideoUpdating(videoID, false); }
    },
    updateCollectionCursor(page) {
      const last = page[page.length - 1];
      this.collectionCursorName = last?.cursor_name || '';
      this.collectionCursorID = Number(last?.collection?.id || 0);
      this.collectionHasMore = page.length === 50;
    },
    async searchCollections(reset) {
      if (this.collectionSearching && !reset) return;
      const requestToken = reset ? Symbol('collection-search') : (this._collectionSearchToken || Symbol('collection-search'));
      this._collectionSearchToken = requestToken; this.collectionSearching = true;
      const cursorName = reset ? '' : this.collectionCursorName; const cursorID = reset ? 0 : this.collectionCursorID;
      try {
        const page = await ListCollections(this.collectionKeyword, cursorName, cursorID, 50) || [];
        if (this._collectionSearchToken !== requestToken) return;
        const incoming = reset ? page : [...this.collectionCandidates, ...page];
        this.collectionCandidates = mergeCollectionCandidates(this.collectionCandidates, incoming, this.draft.collectionIDs);
        this.updateCollectionCursor(page);
      } catch (err) { if (this._collectionSearchToken === requestToken) this.error = String(err); }
      finally { if (this._collectionSearchToken === requestToken) this.collectionSearching = false; }
    },
    async createAndSelectPerson() {
      if (this.creatingPerson) return;
      this.creatingPerson = true;
      const videoID = this.currentEntry?.type === 'video' ? Number(this.currentEntry.id) : 0;
      const entryToken = this._entryLoadToken; const operationToken = Symbol('person-create');
      this._personCreateToken = operationToken;
      try {
        const person = await CreatePerson(this.newPerson.displayName, this.newPerson.originalName);
        if (this._personCreateToken !== operationToken || !this.isCurrentVideoRequest(videoID, entryToken)) return;
        const item = { person, avatar_url: '', active_video_count: 0 };
        this.personCandidates = [item, ...this.personCandidates]; this.togglePerson(person.id, true);
        this.newPerson = { displayName: '', originalName: '' };
      } catch (err) {
        if (this._personCreateToken === operationToken && this.isCurrentVideoRequest(videoID, entryToken)) this.error = String(err);
      }
      finally { if (this._personCreateToken === operationToken) this.creatingPerson = false; }
    },
    async refreshTechnical() {
      this.refreshingTechnical = true; this.technicalError = '';
      const videoID = this.details.video.id; const entryToken = this._entryLoadToken; const operationToken = Symbol('technical-refresh');
      this._technicalRefreshToken = operationToken;
      try {
        const updatedDetails = await RefreshVideoTechnicalMetadata(videoID); this.$emit('details-updated', updatedDetails);
        if (this.isCurrentVideoRequest(videoID, entryToken)) { this.details = updatedDetails; this.draft = createVideoDetailsDraft(updatedDetails); }
      }
      catch (err) {
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.technicalError = String(err);
        try {
          const previousDetails = await GetVideoDetails(videoID);
          if (this.isCurrentVideoRequest(videoID, entryToken)) this.details = previousDetails;
        } catch (reloadErr) {
          if (this.isCurrentVideoRequest(videoID, entryToken)) this.error = `技术信息读取失败：${err}；重新加载旧快照失败：${reloadErr}`;
        }
      }
      finally { if (this._technicalRefreshToken === operationToken) this.refreshingTechnical = false; }
    },
    async savePerson() { try { await UpdatePerson(this.currentEntry.id, this.personEdit.displayName, this.personEdit.originalName); await this.loadCurrentEntry(); } catch (err) { this.error = String(err); } },
    async replacePersonAvatar() { try { const path = await SelectPersonAvatar(); if (path) { await SetPersonAvatar(this.currentEntry.id, path); await this.loadCurrentEntry(); } } catch (err) { this.error = String(err); } },
    async removePersonAvatar() { try { await RemovePersonAvatar(this.currentEntry.id); await this.loadCurrentEntry(); } catch (err) { this.error = String(err); } },
    async saveCollection() { try { await UpdateCollection(this.currentEntry.id, this.collectionEdit.name, this.collectionEdit.description); await this.loadCurrentEntry(); } catch (err) { this.error = String(err); } },
    async replaceCollectionCover() { try { const path = await SelectCollectionCover(); if (path) { await SetCollectionCover(this.currentEntry.id, path); await this.loadCurrentEntry(); } } catch (err) { this.error = String(err); } },
    async removeCollectionCover() { try { await RemoveCollectionCover(this.currentEntry.id); await this.loadCurrentEntry(); } catch (err) { this.error = String(err); } },
    async deleteCollection() {
      if (!window.confirm('删除作品集？其中的视频不会被删除。')) return;
      try { const id = this.currentEntry.id; await DeleteCollection(id); this.$emit('collection-deleted', id); if (this.canGoBack) await this.goBack(); else this.$emit('close'); }
      catch (err) { this.error = String(err); }
    },
    async dropCollectionMember(index) {
      if (this.draggedMemberIndex < 0) return;
      const previous = this.collectionDetail.videos;
      const moved = moveCollectionMember(previous, this.draggedMemberIndex, index); this.draggedMemberIndex = -1; this.collectionDetail.videos = moved;
      try { await ReorderCollectionVideos(this.currentEntry.id, moved.map(item => item.video.id)); }
      catch (err) { this.collectionDetail.videos = previous; this.error = String(err); }
    },
    handleLoadedMetadata() { this.configureVideoElement(); },
    configureVideoElement() { const video = this.$refs.videoElement; if (!video) return; video.defaultMuted = true; video.muted = true; this.applyStartTime(video); },
    applyStartTime(video) {
      const startTimeMs = detailPlaybackStartMs({
        entryID: this.currentEntry?.id,
        rootVideoID: this.video?.id,
        explicitStartTimeMs: this.startTimeMs,
        rootResumePositionSeconds: this.resumePositionSeconds,
        nestedResumePositionSeconds: this.details?.video?.watch_position_seconds
      });
      if (video.readyState < 1 || startTimeMs === 0) return;
      let seekSeconds = startTimeMs / 1000; if (Number.isFinite(video.duration) && video.duration > 0) seekSeconds = Math.min(seekSeconds, Math.max(video.duration - 0.001, 0));
      const seekKey = `${this.currentSession?.video_id || ''}:${seekSeconds}`; if (seekKey === this.appliedSeekKey) return; video.currentTime = seekSeconds; this.appliedSeekKey = seekKey;
    },
    handleTimeUpdate() { this.emitWatchProgress(false, false); },
    emitWatchProgress(force, completed, videoID = null) {
      if (this.resettingVideo || (!this.hasPlaybackStarted && !completed)) return; const video = this.$refs.videoElement; const positionSeconds = Number(video?.currentTime || 0);
      if (!Number.isFinite(positionSeconds) || positionSeconds <= 0) return; const now = Date.now(); if (!force && now - this.lastProgressEmittedAt < 10000) return;
      this.lastProgressEmittedAt = now; this.$emit('watch-progress', { videoID: Number(videoID || this.currentSession?.video_id || this.currentEntry?.id || this.video?.id || 0), positionSeconds, completed: !!completed });
    },
    resetVideoElement() {
      const video = this.$refs.videoElement; if (!video) return; this.resettingVideo = true;
      try { video.pause(); } catch (err) {} try { video.currentTime = 0; } catch (err) {}
      video.defaultMuted = true; video.muted = true; this.appliedSeekKey = ''; video.removeAttribute('src'); const source = video.querySelector('source'); if (source) source.removeAttribute('src'); video.load();
      this.lastProgressEmittedAt = 0; this.hasPlaybackStarted = false; this.resettingVideo = false;
    },
    formatBytes(value) { const bytes = Number(value); if (!Number.isFinite(bytes)) return '未知'; const units = ['B', 'KB', 'MB', 'GB', 'TB']; let size = bytes; let unit = 0; while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit++; } return `${size.toFixed(unit ? 1 : 0)} ${units[unit]}`; },
    formatDuration(seconds) { const value = Number(seconds); if (!value) return ''; const h = Math.floor(value / 3600); const m = Math.floor((value % 3600) / 60); const s = Math.floor(value % 60); return [h, m, s].filter((_, i) => i > 0 || h > 0).map(v => String(v).padStart(2, '0')).join(':'); },
    formatBitRate(value) { const bitrate = Number(value); return Number.isFinite(bitrate) && bitrate > 0 ? `${(bitrate / 1000000).toFixed(2)} Mbps` : '未知'; },
    formatFrameRate(avgFrameRate, realFrameRate) { return formatFrameRateValue(avgFrameRate, realFrameRate); },
    formatDateTime(value) { const date = value ? new Date(value) : null; return date && Number.isFinite(date.getTime()) ? date.toLocaleString() : '未知'; },
    formatNanosecondTime(value) { const nanoseconds = Number(value); if (!Number.isFinite(nanoseconds) || nanoseconds <= 0) return '未知'; return new Date(nanoseconds / 1000000).toLocaleString(); },
    streamTypeLabel(type) { return { video: '视频流', audio: '音轨', subtitle: '内封字幕' }[type] || type; }
  }
};
</script>

<style scoped>
.preview-drawer { position: fixed; top: 74px; right: 12px; bottom: 12px; width: min(520px, 46vw); min-width: 360px; border-radius: 18px; display: flex; flex-direction: column; z-index: 140; overflow: hidden; }
.preview-drawer__header { display: flex; justify-content: space-between; align-items: flex-start; gap: 16px; padding: 16px 18px; border-bottom: 1px solid var(--border-color); background: var(--glass-strong-bg); }
.preview-drawer__heading { display: flex; align-items: center; gap: 10px; min-width: 0; }.preview-drawer__heading > div { min-width: 0; }
.preview-drawer__eyebrow { margin: 0 0 4px; font-size: 11px; letter-spacing: .08em; color: var(--text-muted); }.preview-drawer__header h3 { font-size: 17px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.preview-drawer__body { flex: 1; min-height: 0; overflow-y: auto; padding: 18px; display: grid; grid-auto-rows: max-content; align-content: start; gap: 14px; }
.preview-drawer__player-shell { position: relative; width: 100%; aspect-ratio: 16 / 9; min-height: 220px; background: #020617; border-radius: 14px; overflow: hidden; }.preview-drawer__video { position: absolute; inset: 0; width: 100%; height: 100%; min-height: 0; display: block; object-fit: contain; background: #020617; }
.preview-drawer__placeholder { min-height: 150px; border: 1px dashed var(--border-color); border-radius: 14px; padding: 22px; display: flex; flex-direction: column; justify-content: center; gap: 12px; color: var(--text-secondary); }
.detail-section { padding: 16px; border: 1px solid var(--border-color); border-radius: 14px; background: var(--panel-bg); display: grid; gap: 12px; }.detail-section--player { min-height: 220px; padding: 0; overflow: hidden; background: #020617; }
.detail-section__heading { display: flex; justify-content: space-between; gap: 10px; align-items: center; }.detail-section h4 { margin: 0; font-size: 15px; }.detail-section__heading span,.detail-readonly-hint { font-size: 12px; color: var(--text-muted); }
.detail-field { display: grid; gap: 6px; font-size: 12px; color: var(--text-secondary); }.detail-field input,.detail-field select,.detail-field textarea,.detail-inline-form input,.detail-create-box input { width: 100%; border: 1px solid var(--border-color); border-radius: 8px; padding: 9px 10px; background: var(--input-bg); color: var(--text-primary); }
.detail-rating-input { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 9px; }.detail-rating-input > span { color: var(--text-muted); font-size: 13px; }.detail-field small { color: var(--text-muted); font-size: 11px; font-weight: 400; }
.detail-secondary,.detail-empty { font-size: 12px; color: var(--text-muted); word-break: break-all; }.detail-error,.detail-error-text { color: var(--danger-color); }.detail-error { padding: 18px; border: 1px solid var(--danger-color); border-radius: 12px; }
.entity-chip-list { display: flex; flex-wrap: wrap; gap: 8px; }.entity-chip { display: inline-flex; align-items: center; gap: 7px; border: 1px solid var(--border-color); border-radius: 999px; padding: 4px 9px 4px 5px; background: var(--control-hover-bg); color: var(--text-primary); }.entity-chip img { width: 26px; height: 26px; border-radius: 50%; object-fit: cover; }.entity-chip__remove { color: var(--danger-color); font-size: 16px; }
.detail-inline-form,.detail-action-row { display: flex; gap: 8px; flex-wrap: wrap; }.detail-inline-form input { flex: 1; }.candidate-list { display: grid; gap: 6px; }.candidate-list button { display: flex; justify-content: space-between; gap: 10px; text-align: left; border: 1px solid var(--border-color); border-radius: 9px; padding: 9px 10px; color: var(--text-primary); background: transparent; }.candidate-list small { color: var(--text-muted); }.detail-create-box { display: grid; gap: 8px; }.detail-create-box summary { cursor: pointer; color: var(--accent-color); }
.related-video-editor { display: grid; gap: 9px; padding-bottom: 12px; border-bottom: 1px solid var(--border-color); }.related-video-results,.related-video-list { display: grid; gap: 8px; }
.related-video-directory-filter { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; }.related-video-directory-filter select { min-width: 0; }.related-video-directory-hint { margin: -3px 0 0; color: var(--text-muted); font-size: 11px; }
.related-video-batch-actions { display: flex; justify-content: flex-end; gap: 8px; }
.selection-row { display: grid; grid-template-columns: auto 1fr auto; align-items: center; gap: 8px; }.selection-row button { background: transparent; border: 0; color: var(--text-primary); text-align: left; cursor: pointer; }.selection-row small { color: var(--text-muted); }
.technical-grid { display: grid; grid-template-columns: 90px 1fr; gap: 6px 10px; margin: 0; font-size: 12px; }.technical-grid dt { color: var(--text-muted); }.technical-grid dd { margin: 0; word-break: break-word; }.technical-status { margin: 0; font-size: 12px; }.technical-status--current { color: var(--success-color, #15803d); }.technical-status--stale,.technical-status--error { color: #b45309; }
.stream-card { display: grid; gap: 4px; padding: 10px; border-radius: 9px; background: var(--control-hover-bg); font-size: 12px; }.stream-card span { color: var(--text-secondary); word-break: break-word; }
.entity-identity > img,.entity-avatar-placeholder { width: 96px; height: 96px; object-fit: cover; border-radius: 14px; }.entity-avatar-placeholder { display: grid; place-items: center; background: var(--control-hover-bg); color: var(--text-muted); }
@media (max-width: 900px) { .preview-drawer { width: 100vw; right: 0; bottom: 0; top: 70px; min-width: 0; border-radius: 18px 18px 0 0; } }
</style>
