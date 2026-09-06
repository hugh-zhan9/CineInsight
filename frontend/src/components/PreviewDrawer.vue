<template>
  <aside class="preview-drawer" role="dialog" aria-label="媒体详情抽屉">
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
              <div
                v-if="activeSeekSprite"
                class="preview-drawer__seek-track"
                aria-hidden="true"
                @pointermove="handleSeekPointerMove"
                @pointerleave="seekPreview = null"
              >
                <div
                  v-if="seekPreview"
                  class="preview-drawer__seek-preview"
                  :style="{ left: `${seekPreview.clampedPercent}%` }"
                >
                  <span class="preview-drawer__seek-image">
                    <img
                      :src="seekSpriteSrc"
                      alt=""
                      :style="seekSpriteImageStyle"
                      @error="handleSeekSpriteError"
                    />
                  </span>
                  <time>{{ formatDuration(seekPreview.seconds) }}</time>
                </div>
              </div>
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
			<button type="button" class="btn-secondary" @click="$emit('export-local-metadata', details.video)">写出 NFO</button>
			<button type="button" class="btn-secondary" @click="$emit('enhance', details.video)">视频超分</button>
			<button type="button" class="btn-secondary" @click="$emit('find-similar', details.video)">找相似</button>
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
            <!-- 展开内容必须自己是布局容器：details 的内容在 WebKit 里走 shadow slot，
                 在 details 上写 display:grid 只作用到 summary 和整块内容之间，
                 内容里的这几个控件不是网格项，间距管不到它们。 -->
            <div class="detail-create-box__fields">
              <input v-model="newPerson.displayName" placeholder="显示姓名" maxlength="200" />
              <input v-model="newPerson.originalName" placeholder="原始姓名（可空）" maxlength="200" />
              <button type="button" class="btn-secondary" :disabled="creatingPerson" @click="createAndSelectPerson">{{ creatingPerson ? '创建中...' : '新建并加入' }}</button>
            </div>
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

          <!-- 播放代理（D-004、D-006）：mkv/avi 这类容器在应用内播不了，
               生成一份 mp4 代理之后预览与手机端自动改用它，源文件不动。 -->
          <div class="proxy-block" data-test="preview-proxy-block">
            <p class="proxy-block__status" data-test="preview-proxy-status">{{ proxyStatusText }}</p>
            <p v-if="proxyError" class="detail-error-text" data-test="preview-proxy-error">{{ proxyError }}</p>
            <div class="detail-action-row">
              <button
                type="button"
                class="btn-secondary btn-compact"
                data-test="preview-proxy-create"
                :disabled="proxyBusy"
                @click="createPlaybackProxy"
              >{{ proxyBusy ? '生成中...' : '生成播放代理' }}</button>
              <button
                v-if="playbackProxy"
                type="button"
                class="btn-secondary btn-compact btn-danger-outline"
                data-test="preview-proxy-delete"
                :disabled="proxyBusy"
                @click="deletePlaybackProxy"
              >删除此代理</button>
            </div>
          </div>
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
              <button type="button" class="btn-secondary btn-compact" @click="selectAllRelatedVideoCandidates">全选已加载</button>
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
        <section class="detail-section" data-test="person-image-section">
          <div class="detail-section__heading"><h4>关联图片（{{ personDetail.person.active_image_count || 0 }}）</h4><span>与视频分开分页</span></div>
          <p v-if="personImageError" class="detail-error-text">{{ personImageError }}</p>
          <div v-if="personImages.length" class="person-image-grid">
            <figure v-for="image in personImages" :key="image.id" class="person-image-card">
              <img :src="`/preview/image-thumbnail/${image.id}`" :alt="image.name" loading="lazy" />
              <figcaption :title="image.name">{{ image.name }}</figcaption>
              <small v-if="image.size > 0" class="person-image-card__size">{{ formatBytes(image.size) }}</small>
              <button
                type="button"
                class="btn-secondary btn-compact"
                :disabled="isPersonImageUpdating(image.id)"
                :data-test="`person-image-remove-${image.id}`"
                @click="removePersonImageRelation(image)"
              >{{ isPersonImageUpdating(image.id) ? '处理中...' : '解除关联' }}</button>
            </figure>
          </div>
          <p v-else class="detail-empty">当前没有活跃关联图片。软删除图片的关系仍会保留。</p>
          <button
            v-if="personImageCursor"
            type="button"
            class="btn-secondary"
            :disabled="personImagesLoading"
            data-test="person-image-more"
            @click="loadMorePersonImages"
          >{{ personImagesLoading ? '加载中...' : '加载更多关联图片' }}</button>
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
              <button type="button" class="btn-secondary btn-compact" @click="selectAllRelatedVideoCandidates">全选已加载</button>
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
        <section class="detail-section">
          <div class="detail-section__heading"><h4>术语表</h4><span>字幕翻译</span></div>
          <GlossaryEditor
            :collection-id="Number(currentEntry.id)"
            hint="这些术语只在本作品集的视频里生效，并覆盖同名的全局条目。"
          />
        </section>
      </template>
    </div>
  </aside>
</template>

<script>
import {
  AddCollectionVideo, AddCollectionVideos, AddPersonVideo, AddPersonVideos, CreatePerson, DeleteCollection, GetAllDirectories, GetCollectionDetail, GetPersonDetail, GetPreviewSession, GetVideoDetails, ListCollections, ListPeople,
  GetPersonImages, RefreshVideoTechnicalMetadata, RemoveCollectionCover, RemoveCollectionVideo, RemovePersonAvatar, RemovePersonImage, RemovePersonVideo, ReorderCollectionVideos, SearchLibraryVideoPage,
  SelectCollectionCover, SelectDirectory, SelectPersonAvatar, SetCollectionCover, SetPersonAvatar, UpdateCollection, UpdatePerson, UpdateVideoDetails,
  CreatePlaybackProxy, DeletePlaybackProxy, GetPlaybackProxy
} from '../../wailsjs/go/main/App';
import { createDetailNavigator, createVideoDetailsDraft, detailPlaybackStartMs, formatBytes, formatFrameRate as formatFrameRateValue, mergeCollectionCandidates, mergePersonCandidates, moveCollectionMember, toggleEntityID, validateRatingDraft } from '../utils/mediaDetails.js';
import GlossaryEditor from './GlossaryEditor.vue';
import RelatedVideoItem from './RelatedVideoItem.vue';
import { shortcutActionForEvent } from '../utils/keyboardShortcuts.js';
import { confirmAction } from '../utils/feedback.js';
import { PLAYBACK_PROXY_CODE_LABELS, playbackProxyStrategyLabel } from '../utils/playbackProxy.js';

export default {
  name: 'PreviewDrawer',
  components: { GlossaryEditor, RelatedVideoItem },
  props: {
    video: { type: Object, default: null },
    initialEntity: { type: Object, default: null },
    session: { type: Object, default: null },
    startTimeMs: { type: Number, default: null },
    resumePositionSeconds: { type: Number, default: 0 },
    pageActive: { type: Boolean, default: true }
  },
  emits: ['close', 'preview-externally', 'watch-progress', 'details-updated', 'collection-deleted', 'person-deleted', 'relations-updated', 'open-local-metadata', 'export-local-metadata', 'enhance', 'find-similar', 'shortcut', 'preview-session-stale'],
  data() {
    return {
      navigator: null, currentEntry: null, canGoBack: false,
      loading: false, error: '', saving: false, refreshingTechnical: false,
      details: null, nestedSession: null, technicalError: '', draft: { displayTitle: '', originalTitle: '', description: '', personalRating: '', personIDs: [], collectionIDs: [] },
      personKeyword: '', personCandidates: [], creatingPerson: false, collectionKeyword: '', collectionCandidates: [], collectionCursorName: '', collectionCursorID: 0, collectionHasMore: false, collectionSearching: false, newPerson: { displayName: '', originalName: '' },
      personDetail: null, personEdit: { displayName: '', originalName: '' },
      // 图片区块与视频区块各自分页、不混排（D-021）：首页来自 GetPersonDetail，
      // 后续页走 GetPersonImages。
      personImages: [], personImageCursor: 0, personImagesLoading: false, personImageUpdatingIDs: [], personImageError: '',
      collectionDetail: null, collectionEdit: { name: '', description: '' }, draggedMemberIndex: -1,
      relatedVideoKeyword: '', relatedVideoDirectory: '', relatedVideoDirectories: [], relatedVideoCandidates: [], relatedVideoSelection: [], relatedVideoCursor: null, relatedVideoHasMore: false, relatedVideoSearching: false, relatedVideoSearchPerformed: false, relatedVideoUpdatingIDs: [], relatedVideoError: '',
      appliedSeekKey: '', lastProgressEmittedAt: 0, resettingVideo: false, hasPlaybackStarted: false,
      seekPreview: null, seekSpriteUnavailable: false, seekSpriteRetryCount: 0, seekSpriteRetryTimer: null,
      playbackProxy: null, proxyBusy: false, proxyError: ''
    };
  },
  computed: {
    currentSession() {
      if (this.currentEntry?.type !== 'video') return null;
      return Number(this.currentEntry.id) === Number(this.video?.id) ? this.session : this.nestedSession;
    },
    activeSeekSprite() {
      const sprite = this.currentSession?.seek_sprite;
      if (this.seekSpriteUnavailable || !sprite?.locator_value) return null;
      const numericFields = ['frame_width', 'frame_height', 'columns', 'rows', 'frame_count', 'interval_seconds'];
      return numericFields.every(field => Number.isFinite(Number(sprite[field])) && Number(sprite[field]) > 0) ? sprite : null;
    },
    seekSpriteSrc() {
      if (!this.activeSeekSprite) return '';
      const base = this.activeSeekSprite.locator_value;
      return this.seekSpriteRetryCount > 0 ? `${base}${base.includes('?') ? '&' : '?'}r=${this.seekSpriteRetryCount}` : base;
    },
    seekSpriteImageStyle() {
      if (!this.activeSeekSprite || !this.seekPreview) return {};
      const columns = Number(this.activeSeekSprite.columns);
      const rows = Number(this.activeSeekSprite.rows);
      const column = this.seekPreview.frameIndex % columns;
      const row = Math.floor(this.seekPreview.frameIndex / columns);
      return {
        width: `${columns * 100}%`,
        height: `${rows * 100}%`,
        left: `${-column * 100}%`,
        top: `${-row * 100}%`
      };
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
    },
    // 会话里带 proxy 就说明这次内嵌播放走的是代理（后端 D-004 标注），
    // 这里直说出来——否则用户会以为应用能直接播 mkv。
    playingThroughProxy() {
      return Boolean(this.currentSession?.proxy);
    },
    proxyStatusText() {
      if (this.playingThroughProxy) {
        const proxy = this.currentSession.proxy;
        return `当前正经播放代理播放（${playbackProxyStrategyLabel(proxy.strategy)}，${this.formatBytes(proxy.size)}）。`;
      }
      const row = this.playbackProxy;
      if (!row) return '尚未生成播放代理。源文件能不能在应用内直接播，取决于它的容器格式。';
      if (row.status === 'failed') {
        return `上次生成失败：${row.last_error || '原因未记录'}`;
      }
      return `已有播放代理（${playbackProxyStrategyLabel(row.strategy)}，${this.formatBytes(row.output_size)}）。`;
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
        this.seekPreview = null; this.seekSpriteUnavailable = false; this.clearSeekSpriteRetry();
        this.$nextTick(() => this.configureVideoElement());
      }
    },
    startTimeMs() { this.appliedSeekKey = ''; this.$nextTick(() => this.configureVideoElement()); }
  },
  mounted() { this.loadRelatedVideoDirectories(); this.resetRootEntry(); window.addEventListener('keydown', this.handleReviewShortcut); },
  beforeUnmount() { window.removeEventListener('keydown', this.handleReviewShortcut); this.clearSeekSpriteRetry(); this.emitWatchProgress(true, false); this.resetVideoElement(); },
  methods: {
	handleReviewShortcut(event) {
	  if (!this.pageActive) return;
	  if (this.currentEntry?.type !== 'video') return;
	  if (document.querySelector('[role="dialog"]:not(.preview-drawer)')) return;
	  const action = shortcutActionForEvent(event);
	  if (!action) return;
	  event.preventDefault();
	  this.$emit('shortcut', { action, video: this.details?.video || this.video });
	},
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
          this.playbackProxy = null; this.proxyError = ''; this.proxyBusy = false;
          this.loadPlaybackProxy(Number(entry.id), requestToken);
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
          this.personImages = personDetail.images || [];
          this.personImageCursor = Number(personDetail.next_image_id || 0);
          this.personImagesLoading = false; this.personImageUpdatingIDs = []; this.personImageError = '';
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
      // 最后关系判定跨视频与图片（D-015）：还有图片关系时解除视频关系不会删人物，
      // 那种情况下不该吓唬用户。
      if (type === 'person' && this.isPersonFinalRelation({ videoDelta: 1 }) && !await confirmAction({ title: '解除关联', message: '这是该人物最后一个活跃关联媒体。若没有软删除媒体保留的关系，解除后人物也会被删除，确定继续吗？', confirmText: '解除', danger: true })) return;
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
    // 这一次解除会不会解掉人物的最后一条关系：videoDelta / imageDelta 指出本次
    // 解除的是哪一侧的一条关系。两侧活跃计数都归零才算最后一条。
    isPersonFinalRelation({ videoDelta = 0, imageDelta = 0 } = {}) {
      const videos = Number(this.personDetail?.person?.active_video_count || 0) - videoDelta;
      const images = Number(this.personDetail?.person?.active_image_count || 0) - imageDelta;
      return videos <= 0 && images <= 0;
    },
    isPersonImageUpdating(imageID) { return this.personImageUpdatingIDs.includes(Number(imageID)); },
    async loadMorePersonImages() {
      const personID = Number(this.currentEntry?.id);
      if (this.currentEntry?.type !== 'person' || !personID || !this.personImageCursor || this.personImagesLoading) return;
      const cursor = this.personImageCursor;
      const requestToken = this._entryLoadToken;
      this.personImagesLoading = true; this.personImageError = '';
      try {
        const page = await GetPersonImages(personID, cursor, 200);
        if (this._entryLoadToken !== requestToken || !this.isCurrentEntity('person', personID)) return;
        this.personImages = [...this.personImages, ...(page?.images || [])];
        this.personImageCursor = Number(page?.next_image_id || 0);
      } catch (err) {
        if (this.isCurrentEntity('person', personID)) this.personImageError = `加载关联图片失败：${err}`;
      } finally {
        if (this._entryLoadToken === requestToken) this.personImagesLoading = false;
      }
    },
    async removePersonImageRelation(image) {
      const personID = Number(this.currentEntry?.id); const imageID = Number(image?.id);
      if (this.currentEntry?.type !== 'person' || !personID || !imageID || this.isPersonImageUpdating(imageID)) return;
      if (this.isPersonFinalRelation({ imageDelta: 1 }) && !await confirmAction({ title: '解除关联', message: '这是该人物最后一个活跃关联媒体。若没有软删除媒体保留的关系，解除后人物也会被删除，确定继续吗？', confirmText: '解除', danger: true })) return;
      this.personImageUpdatingIDs = [...new Set([...this.personImageUpdatingIDs, imageID])]; this.personImageError = '';
      try {
        const personDeleted = await RemovePersonImage(personID, imageID);
        if (personDeleted) {
          this.$emit('person-deleted', personID);
          if (this.isCurrentEntity('person', personID)) {
            if (this.canGoBack) await this.goBack(); else this.$emit('close');
          }
          return;
        }
        this.$emit('relations-updated', { type: 'person', id: personID });
        if (this.isCurrentEntity('person', personID)) await this.loadCurrentEntry();
      } catch (err) {
        if (this.isCurrentEntity('person', personID)) this.personImageError = `解除图片关联失败：${err}`;
      } finally {
        this.personImageUpdatingIDs = this.personImageUpdatingIDs.filter(item => item !== imageID);
      }
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
    // ===== 播放代理（D-004、D-006）=====
    // 生成是后台单 worker 任务，这里等它跑完这一项再回读元数据：
    // 用户点完按钮总要看到结果，抽屉里没有别的地方能显示进度。
    async loadPlaybackProxy(videoID, entryToken) {
      try {
        const proxy = await GetPlaybackProxy(videoID);
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.playbackProxy = proxy || null;
      } catch (err) {
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.proxyError = `读取播放代理状态失败：${err}`;
      }
    },
    async createPlaybackProxy() {
      if (this.proxyBusy || this.currentEntry?.type !== 'video') return;
      const videoID = Number(this.currentEntry.id); const entryToken = this._entryLoadToken;
      this.proxyBusy = true; this.proxyError = '';
      try {
        const status = await CreatePlaybackProxy(videoID);
        const item = (status?.results || []).find(result => Number(result.video_id) === videoID);
        if (item && item.code !== 'created' && item.code !== 'already_exists' && this.isCurrentVideoRequest(videoID, entryToken)) {
          this.proxyError = `生成播放代理失败：${PLAYBACK_PROXY_CODE_LABELS[item.code] || item.code}`;
        }
      } catch (err) {
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.proxyError = `生成播放代理失败：${err}`;
      } finally {
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.proxyBusy = false;
        await this.loadPlaybackProxy(videoID, entryToken);
        await this.reloadPreviewSessionForProxy(videoID, entryToken);
      }
    },
    async deletePlaybackProxy() {
      if (this.proxyBusy || this.currentEntry?.type !== 'video') return;
      if (!await confirmAction({ title: '删除播放代理', message: '删除这份播放代理？源文件不受影响，需要时可以重新生成。', confirmText: '删除', danger: true })) return;
      const videoID = Number(this.currentEntry.id); const entryToken = this._entryLoadToken;
      this.proxyBusy = true; this.proxyError = '';
      try {
        await DeletePlaybackProxy(videoID);
      } catch (err) {
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.proxyError = `删除播放代理失败：${err}`;
      } finally {
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.proxyBusy = false;
        await this.loadPlaybackProxy(videoID, entryToken);
        await this.reloadPreviewSessionForProxy(videoID, entryToken);
      }
    },
    // 代理增删之后预览会话的 mode 会变（外部预览 <-> 内嵌），只有嵌套条目
    // 的会话由抽屉自己持有；根条目的会话归片库页，交给它自己刷。
    async reloadPreviewSessionForProxy(videoID, entryToken) {
      if (Number(videoID) === Number(this.video?.id)) {
        this.$emit('preview-session-stale', videoID);
        return;
      }
      try {
        const session = await GetPreviewSession(videoID);
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.nestedSession = session;
      } catch (err) {
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.proxyError = `刷新预览会话失败：${err}`;
      }
    },
    async savePerson() { try { await UpdatePerson(this.currentEntry.id, this.personEdit.displayName, this.personEdit.originalName); await this.loadCurrentEntry(); } catch (err) { this.error = String(err); } },
    async replacePersonAvatar() { try { const path = await SelectPersonAvatar(); if (path) { await SetPersonAvatar(this.currentEntry.id, path); await this.loadCurrentEntry(); } } catch (err) { this.error = String(err); } },
    async removePersonAvatar() { try { await RemovePersonAvatar(this.currentEntry.id); await this.loadCurrentEntry(); } catch (err) { this.error = String(err); } },
    async saveCollection() { try { await UpdateCollection(this.currentEntry.id, this.collectionEdit.name, this.collectionEdit.description); await this.loadCurrentEntry(); } catch (err) { this.error = String(err); } },
    async replaceCollectionCover() { try { const path = await SelectCollectionCover(); if (path) { await SetCollectionCover(this.currentEntry.id, path); await this.loadCurrentEntry(); } } catch (err) { this.error = String(err); } },
    async removeCollectionCover() { try { await RemoveCollectionCover(this.currentEntry.id); await this.loadCurrentEntry(); } catch (err) { this.error = String(err); } },
    async deleteCollection() {
      if (!await confirmAction({ title: '删除作品集', message: '删除作品集？其中的视频不会被删除。', confirmText: '删除', danger: true })) return;
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
    handleSeekPointerMove(event) {
      if (!this.activeSeekSprite) return;
      const bounds = event.currentTarget?.getBoundingClientRect?.();
      if (!bounds || bounds.width <= 0) return;
      const ratio = Math.min(Math.max((Number(event.clientX) - bounds.left) / bounds.width, 0), 0.999999);
      const videoDuration = Number(this.$refs.videoElement?.duration);
      const detailDuration = Number(this.details?.video?.duration);
      const duration = Number.isFinite(videoDuration) && videoDuration > 0 ? videoDuration : detailDuration;
      if (!Number.isFinite(duration) || duration <= 0) return;
      const interval = Number(this.activeSeekSprite.interval_seconds);
      const frameCount = Number(this.activeSeekSprite.frame_count);
      const seconds = ratio * duration;
      const frameIndex = Math.min(Math.floor(seconds / interval), frameCount - 1);
      this.seekPreview = {
        frameIndex,
        seconds,
        clampedPercent: Math.min(Math.max(ratio * 100, 18), 82)
      };
    },
    handleSeekSpriteError() {
      // sprite 现在在后台异步生成，404 通常表示"尚未就绪"而非永久失败：
      // 有限次数地稍后重试，重试串上参数避免命中已缓存的失败响应。
      this.seekPreview = null;
      this.seekSpriteUnavailable = true;
      if (this.seekSpriteRetryCount >= 15 || this.seekSpriteRetryTimer) return;
      this.seekSpriteRetryTimer = setTimeout(() => {
        this.seekSpriteRetryTimer = null;
        this.seekSpriteRetryCount += 1;
        this.seekSpriteUnavailable = false;
      }, 4000);
    },
    clearSeekSpriteRetry() {
      if (this.seekSpriteRetryTimer) {
        clearTimeout(this.seekSpriteRetryTimer);
        this.seekSpriteRetryTimer = null;
      }
      this.seekSpriteRetryCount = 0;
    },
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
      this.lastProgressEmittedAt = 0; this.hasPlaybackStarted = false; this.seekPreview = null; this.resettingVideo = false;
    },
    formatBytes,
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
/* 这一排动作按钮原本没有任何样式：靠行内空白撑横向间隙，换行后两行会贴死。 */
.detail-inline-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}

/* 原型 A4：抽屉固定 520px 并排停靠，不再是圆角浮块；
   窗口窄于 1100 改为覆盖式，列表不再收窄（收窄到放不下反而更难用）。 */
.preview-drawer { position: fixed; top: 52px; right: 0; bottom: 0; width: 520px; border-left: 1px solid var(--hairline); border-radius: 0; background: var(--panel-bg); box-shadow: var(--shadow-drawer); display: flex; flex-direction: column; z-index: 140; overflow: hidden; }
@media (max-width: 1100px) { .preview-drawer { width: min(520px, 100vw); } }
.preview-drawer__header { display: flex; justify-content: space-between; align-items: center; gap: 16px; height: 44px; padding: 0 16px; border-bottom: 1px solid var(--hairline); background: var(--panel-bg); }
.preview-drawer__heading { display: flex; align-items: center; gap: 10px; min-width: 0; }.preview-drawer__heading > div { min-width: 0; }
.preview-drawer__eyebrow { margin: 0 0 4px; font-size: 11px; letter-spacing: .08em; color: var(--text-muted); }.preview-drawer__header h3 { font-size: 14px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.preview-drawer__body { flex: 1; min-height: 0; overflow-y: auto; padding: 14px 16px; display: grid; grid-auto-rows: max-content; align-content: start; gap: 14px; }
.preview-drawer__player-shell { position: relative; width: 100%; aspect-ratio: 16 / 9; min-height: 220px; background: var(--player-bg); border-radius: 0; overflow: hidden; }.preview-drawer__video { position: absolute; inset: 0; width: 100%; height: 100%; min-height: 0; display: block; object-fit: contain; background: var(--player-bg); }
.preview-drawer__seek-track { position: absolute; z-index: 2; right: 12px; bottom: 42px; left: 12px; height: 12px; border-radius: 999px; background: color-mix(in srgb, var(--text-primary) 28%, transparent); cursor: default; }
.preview-drawer__seek-track::after { position: absolute; inset: 4px 0; border-radius: inherit; background: color-mix(in srgb, var(--text-primary) 58%, transparent); content: ''; }
.preview-drawer__seek-preview { position: absolute; bottom: 18px; width: 160px; transform: translateX(-50%); display: grid; gap: 4px; justify-items: center; pointer-events: none; }
.preview-drawer__seek-image { position: relative; width: 160px; height: 90px; overflow: hidden; border: 2px solid rgba(255, 255, 255, .9); border-radius: 8px; background: var(--player-bg); box-shadow: 0 6px 20px rgba(0, 0, 0, .45); }
.preview-drawer__seek-image img { position: absolute; max-width: none; object-fit: fill; }
.preview-drawer__seek-preview time { padding: 2px 6px; border-radius: 5px; color: #fff; background: rgba(0, 0, 0, .78); font-size: 11px; font-variant-numeric: tabular-nums; }
.preview-drawer__placeholder { min-height: 150px; border: 1px dashed var(--border-color); border-radius: 14px; padding: 22px; display: flex; flex-direction: column; justify-content: center; gap: 12px; color: var(--text-secondary); }
.detail-section { padding: 14px; border: 1px solid var(--hairline); border-radius: var(--radius-md); background: var(--panel-bg); display: grid; gap: 12px; }.detail-section--player { min-height: 220px; padding: 0; overflow: hidden; background: var(--player-bg); }
.detail-section__heading { display: flex; justify-content: space-between; gap: 10px; align-items: center; }.detail-section h4 { margin: 0; font-size: 15px; }.detail-section__heading span,.detail-readonly-hint { font-size: 12px; color: var(--text-muted); }
.detail-field { display: grid; gap: 6px; font-size: 12px; color: var(--text-secondary); }.detail-field input,.detail-field select,.detail-field textarea,.detail-inline-form input,.detail-create-box input { width: 100%; border: 1px solid var(--border-color); border-radius: 8px; padding: 9px 10px; background: var(--control-bg); color: var(--text-primary); }
.detail-rating-input { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 9px; }.detail-rating-input > span { color: var(--text-muted); font-size: 13px; }.detail-field small { color: var(--text-muted); font-size: 11px; font-weight: 400; }
.detail-secondary,.detail-empty { font-size: 12px; color: var(--text-muted); word-break: break-all; }.detail-error,.detail-error-text { color: var(--danger-color); }.detail-error { padding: 18px; border: 1px solid var(--danger-color); border-radius: 12px; }
.entity-chip-list { display: flex; flex-wrap: wrap; gap: 8px; }.entity-chip { display: inline-flex; align-items: center; gap: 7px; border: 1px solid var(--border-color); border-radius: 999px; padding: 4px 9px 4px 5px; background: var(--control-hover-bg); color: var(--text-primary); }.entity-chip img { width: 26px; height: 26px; border-radius: 50%; object-fit: cover; }.entity-chip__remove { color: var(--danger-color); font-size: 16px; }
.detail-inline-form,.detail-action-row { display: flex; gap: 8px; flex-wrap: wrap; }.detail-inline-form input { flex: 1; }.candidate-list { display: grid; gap: 6px; }.candidate-list button { display: flex; justify-content: space-between; gap: 10px; text-align: left; border: 1px solid var(--border-color); border-radius: 9px; padding: 9px 10px; color: var(--text-primary); background: transparent; }.candidate-list small { color: var(--text-muted); }.detail-create-box { display: grid; gap: 8px; }.detail-create-box summary { cursor: pointer; color: var(--accent-color); }.detail-create-box__fields { display: grid; gap: 8px; justify-items: start; }.detail-create-box__fields input { width: 100%; }
.related-video-editor { display: grid; gap: 9px; padding-bottom: 12px; border-bottom: 1px solid var(--border-color); }.related-video-results,.related-video-list { display: grid; gap: 8px; }
.related-video-directory-filter { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; }.related-video-directory-filter select { min-width: 0; }.related-video-directory-hint { margin: -3px 0 0; color: var(--text-muted); font-size: 11px; }
.related-video-batch-actions { display: flex; justify-content: flex-end; gap: 8px; }
.person-image-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(120px, 1fr)); gap: 8px; }
.person-image-card { margin: 0; display: grid; gap: 6px; justify-items: stretch; padding: 8px; border: 1px solid var(--hairline); border-radius: var(--radius-md); background: var(--control-hover-bg); }
.person-image-card img { width: 100%; aspect-ratio: 1; display: block; object-fit: cover; border-radius: 8px; background: var(--thumb-bg); }
.person-image-card figcaption { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-muted); font-size: 11px; }
.person-image-card__size { color: var(--text-muted); font-size: 11px; font-variant-numeric: tabular-nums; }
.selection-row { display: grid; grid-template-columns: auto 1fr auto; align-items: center; gap: 8px; }.selection-row button { background: transparent; border: 0; color: var(--text-primary); text-align: left; cursor: pointer; }.selection-row small { color: var(--text-muted); }
.technical-grid { display: grid; grid-template-columns: 90px 1fr; gap: 6px 10px; margin: 0; font-size: 12px; }.technical-grid dt { color: var(--text-muted); }.technical-grid dd { margin: 0; word-break: break-word; }.technical-status { margin: 0; font-size: 12px; }.technical-status--current { color: var(--success-color); }.technical-status--stale,.technical-status--error { color: var(--warning-strong); }
.proxy-block { display: grid; gap: 6px; margin-top: 10px; padding-top: 10px; border-top: 1px solid var(--border-color); }.proxy-block__status { margin: 0; color: var(--text-secondary); font-size: 12px; }
.stream-card { display: grid; gap: 4px; padding: 10px; border-radius: 9px; background: var(--control-hover-bg); font-size: 12px; }.stream-card span { color: var(--text-secondary); word-break: break-word; }
.entity-identity > img,.entity-avatar-placeholder { width: 96px; height: 96px; object-fit: cover; border-radius: 14px; }.entity-avatar-placeholder { display: grid; place-items: center; background: var(--control-hover-bg); color: var(--text-muted); }
@media (max-width: 900px) { .preview-drawer { width: 100vw; right: 0; bottom: 0; top: 70px; min-width: 0; border-radius: 18px 18px 0 0; } }
</style>
