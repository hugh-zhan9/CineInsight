<template>
  <aside class="preview-drawer" role="dialog" aria-label="媒体详情抽屉">
    <div class="preview-drawer__header glass-drawer-header">
      <div class="preview-drawer__heading">
        <button v-if="canGoBack" type="button" class="btn-secondary btn-compact" @click="requestGoBack">返回</button>
        <div>
          <p class="preview-drawer__eyebrow">{{ entryLabel }}</p>
          <h3>{{ entryTitle }}</h3>
        </div>
      </div>
      <button type="button" class="preview-drawer__close btn-secondary btn-compact" data-test="preview-drawer-close" @click="requestClose">关闭</button>
    </div>

    <div class="preview-drawer__body">
      <div v-if="loading" class="preview-drawer__placeholder">正在读取本地详情...</div>
      <div v-else-if="error" class="detail-error" role="alert">
        <p>{{ error }}</p>
        <button type="button" class="btn-secondary" @click="loadCurrentEntry">重试</button>
      </div>

      <template v-else-if="currentEntry?.type === 'video' && details">
        <!-- 动作条（D-PC47、PLAY-14）：播放、收藏、点赞、已看与星级评分都在抽屉顶部，不必只靠快捷键。
             评分点一下即保存（UpdateVideoRating），不经「保存作品信息」。 -->
        <section v-if="actionVideo" class="detail-section drawer-action-bar" data-test="drawer-action-bar" aria-label="常用操作">
          <div class="drawer-action-bar__buttons">
            <button type="button" class="row-btn row-btn--primary" data-test="drawer-action-play" :disabled="actionBusy === 'play'" @click="playFromDrawer">{{ actionBusy === 'play' ? '启动中...' : '播放' }}</button>
            <button type="button" :class="['row-btn', { 'row-btn--fav': actionVideo.is_favorite }]" :aria-pressed="!!actionVideo.is_favorite" data-test="drawer-action-favorite" :disabled="!!actionBusy" @click="toggleVideoState('favorite')">收藏</button>
            <button type="button" :class="['row-btn', { active: actionVideo.is_liked }]" :aria-pressed="!!actionVideo.is_liked" data-test="drawer-action-like" :disabled="!!actionBusy" @click="toggleVideoState('liked')">点赞</button>
            <button type="button" :class="['row-btn', { active: actionVideo.is_watched }]" :aria-pressed="!!actionVideo.is_watched" data-test="drawer-action-watched" :disabled="!!actionBusy" @click="toggleVideoState('watched')">已看</button>
          </div>
          <div class="drawer-stars" role="group" aria-label="个人评分，0 到 10，支持 0.5 分" data-test="drawer-stars">
            <span v-for="star in 10" :key="star" :class="['drawer-star', `drawer-star--${starFill(star)}`]">
              <button type="button" class="drawer-star__half drawer-star__half--left" :aria-label="`评 ${star - 0.5} 分`" :aria-pressed="ratingValue === star - 0.5" :data-test="`drawer-star-${star - 0.5}`" :disabled="ratingSaving" @click="rateFromStars(star - 0.5)"></button>
              <button type="button" class="drawer-star__half drawer-star__half--right" :aria-label="`评 ${star} 分`" :aria-pressed="ratingValue === star" :data-test="`drawer-star-${star}`" :disabled="ratingSaving" @click="rateFromStars(star)"></button>
            </span>
            <span class="drawer-stars__value" data-test="drawer-stars-value">{{ ratingValue === null ? '未评分' : `${ratingValue.toFixed(1)} / 10` }}</span>
            <button v-if="ratingValue !== null" type="button" class="btn-secondary btn-compact" data-test="drawer-stars-clear" :disabled="ratingSaving" @click="rateFromStars(null)">清除</button>
          </div>
          <p v-if="actionError" class="detail-error-text" role="alert" data-test="drawer-action-error">{{ actionError }}</p>
        </section>

        <section class="detail-section detail-section--player">
          <div v-if="!currentSession" class="preview-drawer__placeholder">正在准备预览...</div>
          <template v-else-if="currentSession.mode === 'inline' && currentSession.inline_source && !inlinePlaybackFailed">
            <div class="preview-drawer__player-shell">
              <video
                ref="videoElement"
                class="preview-drawer__video"
                controls
                playsinline
                preload="metadata"
                :muted="true"
                @loadedmetadata="handleLoadedMetadata"
                @play="handlePlay"
                @timeupdate="handleTimeUpdate"
                @seeking="handleSeeking"
                @pause="handlePause"
                @ended="emitWatchProgress(true, true)"
                @error="handleVideoError"
              >
                <source :src="currentSession.inline_source.locator_value" :type="currentSession.inline_source.mime" @error="handleSourceError" />
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
          <!-- 内嵌播放器报错（D-PC26、PLAY-05）：解码失败不再是一块黑屏，给出系统播放器与播放代理两条路。
               已经在经代理播放时再生成一份也没用，只留系统播放器。 -->
          <div v-else-if="currentSession.mode === 'inline' && inlinePlaybackFailed" class="preview-drawer__placeholder" role="alert" data-test="preview-inline-error">
            <p>无法内嵌播放此文件</p>
            <div class="detail-action-row">
              <button type="button" class="btn-primary" data-test="preview-inline-error-external" @click="$emit('preview-externally', details.video)">用系统播放器预览</button>
              <button v-if="!playingThroughProxy" type="button" class="btn-secondary" data-test="preview-inline-error-proxy" :disabled="proxyBusy || proxyPending" @click="createPlaybackProxy">{{ proxyCreateLabel }}</button>
            </div>
            <p v-if="proxyProgressText" class="preview-drawer__proxy-progress" data-test="preview-inline-error-progress">{{ proxyProgressText }}</p>
          </div>
          <template v-else-if="currentSession.mode === 'external-preview' && currentSession.external_action">
            <div class="preview-drawer__placeholder">
              <p>{{ currentSession.reason_message }}</p>
              <button type="button" class="btn-primary" @click="$emit('preview-externally', details.video)">{{ currentSession.external_action.button_label }}</button>
              <p v-if="proxyProgressText" class="preview-drawer__proxy-progress" data-test="preview-player-proxy-progress">{{ proxyProgressText }}</p>
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
              <input v-model.trim="draft.personalRating" type="text" inputmode="decimal" maxlength="4" placeholder="未评分" aria-label="个人评分，0 到 10，支持 0.5 分" data-test="detail-rating-input" :disabled="ratingSaving" @change="saveRatingNow" />
              <span aria-hidden="true">/ 10</span>
            </span>
            <!-- D-PC47：评分改完即保存（UpdateVideoRating），不必等「保存作品信息」。 -->
            <small>输入 0–10，支持半分；留空表示未评分。改完即保存</small>
            <small v-if="ratingError" class="detail-error-text" role="alert" data-test="detail-rating-error">{{ ratingError }}</small>
          </label>
          <p class="detail-secondary">原文件：{{ details.video.name }}</p>
          <div class="detail-inline-actions">
            <button type="button" class="btn-primary" data-test="drawer-save-details" :disabled="saving" @click="saveVideoDetails">{{ saving ? '保存中...' : '保存作品信息' }}</button>
            <button type="button" class="btn-secondary" @click="$emit('open-local-metadata', details.video)">导入本地资料</button>
			<button type="button" class="btn-secondary" @click="$emit('export-local-metadata', details.video)">写出 NFO</button>
			<button type="button" class="btn-secondary" @click="$emit('enhance', details.video)">视频超分</button>
			<button type="button" class="btn-secondary" @click="$emit('find-similar', details.video)">找相似</button>
          </div>
        </section>

        <section class="detail-section">
          <!-- 与标签分类「人物」、人物页同一个叫法（META-02）。 -->
          <div class="detail-section__heading"><h4>人物</h4><span>{{ draft.personIDs.length + pendingPeople.length }} 人</span></div>
          <div class="entity-chip-list">
            <button v-for="item in selectedPeople" :key="item.person.id" type="button" class="entity-chip" @click="openPerson(item.person.id)">
              <img v-if="item.avatar_url" :src="item.avatar_url" alt="" />
              <span>{{ item.person.display_name }}</span>
              <span class="entity-chip__remove" title="移除" :data-test="`drawer-person-remove-${item.person.id}`" @click.stop="removePerson(item)">×</span>
            </button>
            <!-- 「新建并加入」只在本地暂存（D-PC32 / META-05），点「保存作品信息」时才新建人物并关联。 -->
            <span v-for="pending in pendingPeople" :key="pending.key" class="entity-chip entity-chip--pending" :data-test="`drawer-pending-person-${pending.key}`">
              <span>{{ pending.displayName }}</span>
              <small>保存后新建</small>
              <button type="button" class="entity-chip__remove" title="移除" @click="removePendingPerson(pending.key)">×</button>
            </span>
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
              <button type="button" class="btn-secondary" data-test="drawer-person-stage" :disabled="creatingPerson || !newPerson.displayName.trim()" @click="createAndSelectPerson">新建并加入</button>
              <small class="detail-create-box__hint">点「保存作品信息」时才会真正新建并关联；不保存就不会留下空人物。</small>
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
               生成一份 mp4 代理之后预览与手机端自动改用它，源文件不动。
               排队与进度来自 playback-proxy-state（D-PC26）：这一项完成后自动重取预览会话、切到内嵌播放。 -->
          <div class="proxy-block" data-test="preview-proxy-block">
            <p class="proxy-block__status" data-test="preview-proxy-status">{{ proxyStatusText }}</p>
            <p v-if="proxyNotice" class="proxy-block__status" data-test="preview-proxy-notice">{{ proxyNotice }}</p>
            <p v-if="proxyError" class="detail-error-text" data-test="preview-proxy-error">{{ proxyError }}</p>
            <div class="detail-action-row">
              <button
                type="button"
                class="btn-secondary btn-compact"
                data-test="preview-proxy-create"
                :disabled="proxyBusy || proxyPending"
                @click="createPlaybackProxy"
              >{{ proxyCreateLabel }}</button>
              <button
                v-if="playbackProxy"
                type="button"
                class="btn-secondary btn-compact btn-danger-outline"
                data-test="preview-proxy-delete"
                :disabled="proxyBusy || proxyPending"
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
            <button type="button" class="btn-secondary" @click="replacePersonAvatar" :disabled="avatarSaving">从文件选择头像</button>
            <button type="button" class="btn-secondary" :disabled="avatarSaving" @click="avatarPickerOpen = true; avatarError = ''">从图片库选择头像</button>
            <button v-if="personDetail.person.avatar_url" type="button" class="btn-secondary" @click="removePersonAvatar" :disabled="avatarSaving">移除头像</button>
          </div>
        </section>
        <p v-if="avatarError" role="alert" class="detail-error-text">{{ avatarError }}</p>
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
            >
              <template #actions><button type="button" class="btn-danger btn-compact" :disabled="isRelatedVideoUpdating(related.id)" :data-test="`drawer-person-video-delete-${related.id}`" @click="requestPersonMediaDelete('video', related)">删除</button></template>
            </RelatedVideoItem>
          </div>
          <p v-if="!(personDetail.videos || []).length" class="detail-empty">当前没有活跃关联视频。软删除视频的关系仍会保留。</p>
        </section>
        <section class="detail-section" data-test="person-image-section">
          <div class="detail-section__heading"><h4>关联图片（{{ personDetail.person.active_image_count || 0 }}）</h4><span>与视频分开分页</span></div>
          <p v-if="personImageError" class="detail-error-text">{{ personImageError }}</p>
          <ImageBatchTagControls v-if="personImages.length" :key="currentEntry.id" v-model:selectedIDs="selectedPersonImageIDs" :image-i-ds="personImages.map(image => image.id)" />
          <div v-if="personImages.length" class="person-image-grid">
            <figure v-for="image in personImages" :key="image.id" class="person-image-card">
              <label class="person-image-card__select"><input v-model="selectedPersonImageIDs" type="checkbox" :value="image.id" :aria-label="`选择 ${image.name}`" />选择</label>
              <button type="button" class="person-image-card__preview" :aria-label="`放大 ${image.name}`" @click="imagePreview = image"><img :src="`/preview/image-thumbnail/${image.id}`" :alt="image.name" loading="lazy" /></button>
              <figcaption :title="image.name">{{ image.name }}</figcaption>
              <button type="button" class="btn-secondary btn-compact" :disabled="avatarSaving" :data-test="`person-image-avatar-${image.id}`" @click="setAvatarFromImage(image)">设为头像</button>
              <small v-if="image.size > 0" class="person-image-card__size">{{ formatBytes(image.size) }}</small>
              <button
                type="button"
                class="btn-secondary btn-compact"
                :disabled="isPersonImageUpdating(image.id)"
                :data-test="`person-image-remove-${image.id}`"
                @click="removePersonImageRelation(image)"
              >{{ isPersonImageUpdating(image.id) ? '处理中...' : '解除关联' }}</button>
              <button type="button" class="btn-danger btn-compact" :disabled="isPersonImageUpdating(image.id)" :data-test="`drawer-person-image-delete-${image.id}`" @click="requestPersonMediaDelete('image', image)">删除图片</button>
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
    <ImageLibraryPicker v-if="avatarPickerOpen" :busy="avatarSaving" :action-error="avatarError" @close="avatarPickerOpen = false" @select="setAvatarFromImage" />
    <ImageSourceDialog v-if="imagePreview" :image="imagePreview" allow-unlink allow-delete :busy="isPersonImageUpdating(imagePreview.id)" :action-error="personImageError" @close="imagePreview = null" @unlink="removePersonImageRelation" @delete="requestPersonMediaDelete('image', $event)" />
    <PersonMediaDeleteDialog v-if="personMediaDeleteTarget" :target="personMediaDeleteTarget" @close="personMediaDeleteTarget = null" @deleted="handlePersonMediaDeleted" @restored="handlePersonMediaRestored" />
  </aside>
</template>

<script>
import {
  AddCollectionVideo, AddCollectionVideos, AddPersonVideo, AddPersonVideos, CreatePerson, DeleteCollection, GetAllDirectories, GetCollectionDetail, GetPersonDetail, GetPreviewSession, GetVideoDetails, ListCollections, ListPeople,
  GetPersonImages, RefreshVideoTechnicalMetadata, RemoveCollectionCover, RemoveCollectionVideo, RemovePersonAvatar, RemovePersonImage, RemovePersonVideo, ReorderCollectionVideos, SearchLibraryVideoPage,
  SelectCollectionCover, SelectDirectory, SelectPersonAvatar, SetCollectionCover, SetPersonAvatar, UpdateCollection, UpdatePerson, UpdateVideoDetails,
  UpdateVideoRating, CreatePlaybackProxy, DeletePlaybackProxy, GetPlaybackProxy, GetPlaybackProxyStatus,
  PlayVideo, SetVideoFavorite, SetVideoLiked, SetVideoWatched, RecordViewEvent
} from '../../wailsjs/go/main/App';
import { createDetailNavigator, createVideoDetailsDraft, detailPlaybackOrigin, detailPlaybackStartMs, formatBytes, formatFrameRate as formatFrameRateValue, mergeCollectionCandidates, mergePersonCandidates, moveCollectionMember, toggleEntityID, validateRatingDraft } from '../utils/mediaDetails.js';
import { isWatchCompleted, resumePosition } from '../utils/watchState.js';
import { createPlaybackAccumulator, newViewSessionID, viewThreshold } from '../utils/viewThreshold.js';
import GlossaryEditor from './GlossaryEditor.vue';
import ImageSourceDialog from './ImageSourceDialog.vue';
import ImageLibraryPicker from './ImageLibraryPicker.vue';
import PersonMediaDeleteDialog from './PersonMediaDeleteDialog.vue';
import ImageBatchTagControls from './ImageBatchTagControls.vue';
import RelatedVideoItem from './RelatedVideoItem.vue';
import { shortcutActionForEvent } from '../utils/keyboardShortcuts.js';
import { confirmAction } from '../utils/feedback.js';
import { PLAYBACK_PROXY_CODE_LABELS, playbackProxyItemState, playbackProxyItemStateText, playbackProxyStrategyLabel } from '../utils/playbackProxy.js';

// 动作条的三个状态开关：字段名、绑定与失败时的说法。
const VIDEO_STATE_TOGGLES = {
  favorite: { field: 'is_favorite', call: (id, value) => SetVideoFavorite(id, value), failure: '更新收藏状态失败' },
  liked: { field: 'is_liked', call: (id, value) => SetVideoLiked(id, value), failure: '更新点赞状态失败' },
  watched: { field: 'is_watched', call: (id, value) => SetVideoWatched(id, value), failure: '更新观看状态失败' }
};

const sameIDSet = (a, b) => {
  const left = [...new Set((a || []).map(Number))].sort((x, y) => x - y);
  const right = [...new Set((b || []).map(Number))].sort((x, y) => x - y);
  return left.length === right.length && left.every((value, index) => value === right[index]);
};

export default {
  name: 'PreviewDrawer',
  components: { ImageLibraryPicker, GlossaryEditor, RelatedVideoItem, ImageSourceDialog, ImageBatchTagControls, PersonMediaDeleteDialog },
  props: {
    video: { type: Object, default: null },
    initialEntity: { type: Object, default: null },
    session: { type: Object, default: null },
    startTimeMs: { type: Number, default: null },
    resumePositionSeconds: { type: Number, default: 0 },
    pageActive: { type: Boolean, default: true }
  },
  // watch-progress 载荷：{ videoID, positionSeconds, completed, origin: resume|start|jump, durationSeconds（未知为 0）}（D-PC42）。
  // media-restored：人物媒体删除后被撤销或从回收站恢复，载荷 { kind, ids }，宿主据此重读列表。
  // playback-attempted：动作条「播放」的 PlaybackAttemptResult，宿主可按自己的列表口径处理失效与重定位。
  emits: ['close', 'preview-externally', 'watch-progress', 'details-updated', 'collection-deleted', 'person-deleted', 'relations-updated', 'media-deleted', 'media-restored', 'open-local-metadata', 'export-local-metadata', 'enhance', 'find-similar', 'shortcut', 'preview-session-stale', 'playback-attempted'],
  data() {
    return {
      selectedPersonImageIDs: [], imagePreview: null, avatarPickerOpen: false, avatarSaving: false, avatarError: '',
      personMediaDeleteTarget: null,
      navigator: null, currentEntry: null, canGoBack: false,
      loading: false, error: '', saving: false, refreshingTechnical: false,
      details: null, nestedSession: null, technicalError: '', draft: { displayTitle: '', originalTitle: '', description: '', personalRating: '', personIDs: [], collectionIDs: [] },
      personKeyword: '', personCandidates: [], creatingPerson: false, collectionKeyword: '', collectionCandidates: [], collectionCursorName: '', collectionCursorID: 0, collectionHasMore: false, collectionSearching: false, newPerson: { displayName: '', originalName: '' },
      // 「新建并加入」暂存的人物（D-PC32）：保存作品信息时才 CreatePerson 并关联；换条目、不保存就丢弃。
      pendingPeople: [], ratingSaving: false, ratingError: '',
      personDetail: null, personEdit: { displayName: '', originalName: '' },
      // 图片区块与视频区块各自分页、不混排（D-021）：首页来自 GetPersonDetail，
      // 后续页走 GetPersonImages。
      personImages: [], personImageCursor: 0, personImagesLoading: false, personImageUpdatingIDs: [], personImageError: '',
      collectionDetail: null, collectionEdit: { name: '', description: '' }, draggedMemberIndex: -1,
      relatedVideoKeyword: '', relatedVideoDirectory: '', relatedVideoDirectories: [], relatedVideoCandidates: [], relatedVideoSelection: [], relatedVideoCursor: null, relatedVideoHasMore: false, relatedVideoSearching: false, relatedVideoSearchPerformed: false, relatedVideoUpdatingIDs: [], relatedVideoError: '',
      appliedSeekKey: '', lastProgressEmittedAt: 0, resettingVideo: false, hasPlaybackStarted: false,
      seekPreview: null, seekSpriteUnavailable: false, seekSpriteRetryCount: 0, seekSpriteRetryTimer: null,
      playbackProxy: null, proxyBusy: false, proxyError: '', proxyNotice: '',
      // 正在等的那一项代理（D-PC26）：{ videoID, state }，state 为 playbackProxyItemState 的结果，
      // 入队请求还没返回时 phase 为 submitting。只有它完成时才自动切换，旧结果不会触发。
      proxyTracking: null,
      inlinePlaybackFailed: false,
      actionBusy: '', actionError: ''
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
    // 视频条目上有没有还没保存的修改：作品信息、人物、作品集，以及暂存待新建的人物。
    hasUnsavedVideoDraft() {
      if (this.currentEntry?.type !== 'video' || !this.details) return false;
      if (this.pendingPeople.length) return true;
      const saved = createVideoDetailsDraft(this.details);
      const draft = this.draft;
      return saved.displayTitle !== draft.displayTitle
        || saved.originalTitle !== draft.originalTitle
        || saved.description !== draft.description
        || String(saved.personalRating) !== String(draft.personalRating ?? '')
        || !sameIDSet(saved.personIDs, draft.personIDs)
        || !sameIDSet(saved.collectionIDs, draft.collectionIDs);
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
    // 动作条读的状态：根条目以宿主传入的行为准（快捷键在宿主那边改了收藏 / 已看，这里要跟着变），
    // 嵌套条目只有抽屉自己读到的详情。
    actionVideo() {
      const base = this.currentEntry?.type === 'video' ? this.details?.video : null;
      if (!base) return null;
      const live = Number(this.video?.id) === Number(base.id) ? this.video : null;
      if (!live) return base;
      return {
        ...base,
        is_favorite: live.is_favorite ?? base.is_favorite,
        is_liked: live.is_liked ?? base.is_liked,
        is_watched: live.is_watched ?? base.is_watched
      };
    },
    ratingValue() {
      const rating = this.details?.video?.personal_rating;
      if (rating === null || rating === undefined || rating === '') return null;
      const value = Number(rating);
      return Number.isFinite(value) ? value : null;
    },
    proxyPending() {
      return !!this.proxyTracking && this.currentEntry?.type === 'video' && this.proxyTracking.videoID === Number(this.currentEntry.id);
    },
    // 「排队中（第 N 个）」「生成中」（D-PC26、PLAY-04）。入队请求还没回来时说「正在加入队列」。
    proxyProgressText() {
      if (!this.proxyPending) return '';
      return playbackProxyItemStateText(this.proxyTracking.state) || '正在加入队列';
    },
    proxyCreateLabel() {
      if (this.proxyPending) return this.proxyTracking.state?.phase === 'queued' ? '排队中...' : '生成中...';
      return this.proxyBusy ? '处理中...' : '生成播放代理';
    },
    proxyStatusText() {
      if (this.proxyPending) return `${this.proxyProgressText}。完成后会自动切到内嵌播放。`;
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
        // 换了会话就是换了一次播放：报错状态清掉，重新开一个观看会话（D-PC43）。
        this.inlinePlaybackFailed = false;
        this.startViewSession(newSession);
        this.$nextTick(() => this.configureVideoElement());
      }
    },
    startTimeMs(value) {
      // 抽屉开着时又从字幕命中跳了一次：这一次播放从此按 jump 上报，只允许往前写断点（D-PC42）。
      if (value !== null && value !== undefined && this._viewSession) this._viewSession.origin = 'jump';
      this.appliedSeekKey = ''; this.$nextTick(() => this.configureVideoElement());
    }
  },
  mounted() {
    this.loadRelatedVideoDirectories(); this.resetRootEntry(); window.addEventListener('keydown', this.handleReviewShortcut);
    // 代理队列的排位与完成（D-PC26）：本项完成后自动重取预览会话。
    if (window.runtime?.EventsOn) {
      const off = window.runtime.EventsOn('playback-proxy-state', status => this.applyProxyStatus(status, { fromEvent: true }));
      if (typeof off === 'function') this._proxyStateOff = off;
    }
  },
  beforeUnmount() {
    window.removeEventListener('keydown', this.handleReviewShortcut); this.clearSeekSpriteRetry(); this.emitWatchProgress(true, false); this.resetVideoElement();
    this._proxyStateOff?.(); this._proxyStateOff = null;
  },
  methods: {
    requestPersonMediaDelete(kind, media) {
      if (this.currentEntry?.type !== 'person') return;
      this.imagePreview = null;
      this.personMediaDeleteTarget = { kind, media, personID: Number(this.currentEntry.id) };
    },
    handlePersonMediaDeleted(target, notify = true) {
      const id = Number(target.media.id);
      if (this.isCurrentEntity('person', target.personID) && this.personDetail) {
        this._entryLoadToken = Symbol('media-deleted'); this.loading = false; this.personImagesLoading = false;
        if (target.kind === 'video') {
          this._relatedVideoSearchToken = Symbol('media-deleted'); this.relatedVideoSearching = false;
          this.personDetail.videos = (this.personDetail.videos || []).filter(item => Number(item.id) !== id);
          this.relatedVideoCandidates = this.relatedVideoCandidates.filter(item => Number(item.id) !== id);
          this.relatedVideoSelection = this.relatedVideoSelection.filter(value => Number(value) !== id);
        } else {
          this.personImages = this.personImages.filter(item => Number(item.id) !== id);
          this.selectedPersonImageIDs = this.selectedPersonImageIDs.filter(value => Number(value) !== id);
          if (Number(this.imagePreview?.id) === id) this.imagePreview = null;
        }
        const field = target.kind === 'video' ? 'active_video_count' : 'active_image_count';
        this.personDetail.person[field] = Math.max(0, Number(this.personDetail.person[field] || 0) - 1);
        if (target.kind === 'image' && !this.personImages.length && this.personImageCursor) this.loadMorePersonImages();
      }
      if (notify) this.$emit('media-deleted', target);
    },
    // 删除被撤销、或从撤销条打开的回收站里恢复了（PersonMediaDeleteDialog 的 restored）：恢复的媒体回到人物名下，
    // 当前停在人物详情就整页重读（视频、图片与计数一起），并告诉宿主刷新它自己的列表。
    async handlePersonMediaRestored(payload, notify = true) {
      if (notify) this.$emit('media-restored', { kind: payload?.kind || '', ids: [...(payload?.ids || [])] });
      if (this.currentEntry?.type === 'person') await this.loadCurrentEntry();
    },
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
      this.imagePreview = null; this.selectedPersonImageIDs = []; this.avatarPickerOpen = false; this.avatarError = '';
      this.pendingPeople = []; this.ratingError = ''; this.ratingSaving = false;
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
          this.playbackProxy = null; this.proxyError = ''; this.proxyNotice = ''; this.proxyBusy = false; this.proxyTracking = null;
          this.actionBusy = ''; this.actionError = '';
          this.loadPlaybackProxy(Number(entry.id), requestToken);
          this.loadProxyQueueState(Number(entry.id), requestToken);
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
    // 离开有未保存修改的视频条目前确认（D-PC32）：关闭抽屉、返回、跳到人物或作品集都会丢掉草稿。
    async confirmDiscardVideoDraft() {
      if (!this.hasUnsavedVideoDraft) return true;
      return confirmAction({ title: '放弃未保存的修改', message: '作品信息、人物或作品集还有未保存的修改，离开后会丢失。确定放弃吗？', confirmText: '放弃修改', danger: true });
    },
    async requestClose() { if (await this.confirmDiscardVideoDraft()) this.$emit('close'); },
    async requestGoBack() { if (await this.confirmDiscardVideoDraft()) await this.goBack(); },
    async navigate(entry) {
      if (!await this.confirmDiscardVideoDraft()) return;
      this.resetRelatedVideoEditor(); this.navigator.push(entry); this.currentEntry = this.navigator.current(); this.canGoBack = this.navigator.canGoBack(); await this.loadCurrentEntry();
    },
    async goBack() { this.resetRelatedVideoEditor(); this.currentEntry = this.navigator.back(); this.canGoBack = this.navigator.canGoBack(); await this.loadCurrentEntry(); },
    openPerson(id) { return this.navigate({ type: 'person', id }); },
    openCollection(id) { return this.navigate({ type: 'collection', id }); },
    openVideo(id) { return this.navigate({ type: 'video', id }); },
    isCurrentVideoRequest(videoID, entryToken) {
      return this._entryLoadToken === entryToken && this.currentEntry?.type === 'video' && Number(this.currentEntry.id) === Number(videoID);
    },
    async saveVideoDetails() {
      // 暂存的人物正在新建：再点一次保存不能再建一遍。
      if (this.creatingPerson) return;
      this.saving = true; this.error = '';
      const videoID = this.details.video.id; const entryToken = this._entryLoadToken; const operationToken = Symbol('video-save');
      this._videoSaveToken = operationToken;
      // 草稿在任何 await 之前取快照：新建人物期间用户可能已经切到别的条目。
      const draft = { ...this.draft, personIDs: [...this.draft.personIDs], collectionIDs: [...this.draft.collectionIDs] };
      const pending = [...this.pendingPeople];
      try {
        const personalRating = validateRatingDraft(draft.personalRating);
        const createdIDs = await this.createPendingPeople(pending, videoID, entryToken);
        const updatedDetails = await UpdateVideoDetails({
          video_id: videoID, display_title: draft.displayTitle, original_title: draft.originalTitle,
          description: draft.description,
          personal_rating: personalRating, person_ids: [...new Set([...draft.personIDs, ...createdIDs])], collection_ids: draft.collectionIDs
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
    // 移除已保存的人物关系前，若它是该人物最后一个活跃媒体，复用「最后一条关系」确认（D-PC32 / META-05）：
    // 保存时后端会顺手删掉已无任何关系的人物。本次才搜出来、还没保存过的人物移除不需要确认。
    async removePerson(item) {
      const id = Number(item?.person?.id);
      if (!id) return;
      const saved = (this.details?.people || []).find(person => Number(person?.person?.id) === id);
      const activeRelations = Number(saved?.active_video_count || 0) + Number(saved?.active_image_count || 0);
      if (saved && activeRelations <= 1 && !await confirmAction({
        title: '移除人物',
        message: `这是「${item.person.display_name || `人物 #${id}`}」最后一个活跃关联媒体。若没有软删除媒体保留的关系，保存后人物也会被删除，确定移除吗？`,
        confirmText: '移除',
        danger: true
      })) return;
      this.togglePerson(id, false);
    },
    removePendingPerson(key) { this.pendingPeople = this.pendingPeople.filter(item => item.key !== key); },
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
        if (this.isCurrentEntity('person', personID)) this.imagePreview = null;
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
    // 「新建并加入」（D-PC32）：只在本地暂存姓名，保存作品信息时才调用 CreatePerson。
    // 以前点一下就写库，不保存直接关抽屉会留下一个没有任何关系、又删不掉的人物（META-05）。
    createAndSelectPerson() {
      const displayName = this.newPerson.displayName.trim();
      if (!displayName || this.currentEntry?.type !== 'video') return;
      this._pendingPersonSeq = (this._pendingPersonSeq || 0) + 1;
      this.pendingPeople = [...this.pendingPeople, { key: this._pendingPersonSeq, displayName, originalName: this.newPerson.originalName.trim() }];
      this.newPerson = { displayName: '', originalName: '' };
    },
    // 保存时逐个新建暂存的人物。建好一个就把它转成正式候选并选中：后面的步骤失败时，重试不会再建一遍。
    async createPendingPeople(pending, videoID, entryToken) {
      if (!pending.length) return [];
      this.creatingPerson = true;
      const ids = [];
      try {
        for (const item of pending) {
          const person = await CreatePerson(item.displayName, item.originalName);
          ids.push(Number(person.id));
          if (this.isCurrentVideoRequest(videoID, entryToken)) {
            this.pendingPeople = this.pendingPeople.filter(entry => entry.key !== item.key);
            this.personCandidates = [{ person, avatar_url: '', active_video_count: 0, active_image_count: 0 }, ...this.personCandidates];
            this.togglePerson(person.id, true);
          }
        }
        return ids;
      } finally {
        this.creatingPerson = false;
      }
    },
    // 评分即时保存（D-PC47）：输入框失焦或回车即调 UpdateVideoRating，不经「保存作品信息」。
    async saveRatingNow() {
      if (this.currentEntry?.type !== 'video' || !this.details?.video) return;
      const videoID = Number(this.details.video.id); const entryToken = this._entryLoadToken;
      let rating;
      try { rating = validateRatingDraft(this.draft.personalRating); }
      catch (err) { this.ratingError = err?.message || String(err); return; }
      this.ratingError = '';
      const current = this.details.video.personal_rating ?? null;
      if (rating === current) return;
      this.ratingSaving = true;
      try {
        const updated = await UpdateVideoRating(videoID, rating);
        if (!this.isCurrentVideoRequest(videoID, entryToken)) return;
        const value = updated?.personal_rating ?? null;
        this.details = { ...this.details, video: { ...this.details.video, personal_rating: value } };
        this.draft.personalRating = value === null ? '' : String(value);
        this.$emit('details-updated', this.details);
      } catch (err) {
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.ratingError = `保存评分失败：${err}`;
      } finally {
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.ratingSaving = false;
      }
    },
    // ===== 动作条（D-PC47、PLAY-14）=====
    starFill(star) {
      const value = this.ratingValue;
      if (value === null) return 'empty';
      if (value >= star) return 'full';
      return value >= star - 0.5 ? 'half' : 'empty';
    },
    // 星级点一下即保存：与评分输入框同一条路（saveRatingNow → UpdateVideoRating），null 表示清除。
    rateFromStars(value) {
      if (this.ratingSaving) return;
      this.draft.personalRating = value === null ? '' : String(value);
      return this.saveRatingNow();
    },
    // 收藏 / 点赞 / 已看：直接调对应绑定，拿回的整条视频写回详情，并按「作品信息已更新」通知宿主刷新那一行。
    async toggleVideoState(kind) {
      const toggle = VIDEO_STATE_TOGGLES[kind];
      const current = this.actionVideo;
      if (!toggle || !current || this.actionBusy) return;
      const videoID = Number(current.id); const entryToken = this._entryLoadToken;
      this.actionBusy = kind; this.actionError = '';
      try {
        const updated = await toggle.call(videoID, !current[toggle.field]);
        if (!this.isCurrentVideoRequest(videoID, entryToken)) return;
        // 状态接口没带 tags 时沿用已有的标签，别用 null 盖掉。
        const video = { ...this.details.video, ...(updated || { [toggle.field]: !current[toggle.field] }) };
        if (!Array.isArray(updated?.tags)) video.tags = this.details.video.tags;
        this.details = { ...this.details, video };
        this.$emit('details-updated', this.details);
      } catch (err) {
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.actionError = `${toggle.failure}：${err}`;
      } finally {
        if (this.actionBusy === kind) this.actionBusy = '';
      }
    },
    // 播放走正式播放（PlayVideo），永远打开源文件。失败原因就地显示（后端文案不含路径），
    // 整个结果交给宿主，由它按自己的列表口径处理失效与重定位。
    async playFromDrawer() {
      const current = this.actionVideo;
      if (!current || this.actionBusy) return;
      const videoID = Number(current.id); const entryToken = this._entryLoadToken;
      this.actionBusy = 'play'; this.actionError = '';
      try {
        const result = await PlayVideo(videoID);
        this.$emit('playback-attempted', result);
        if (result && result.dispatch_succeeded === false && this.isCurrentVideoRequest(videoID, entryToken)) {
          this.actionError = result.user_message || '播放失败';
        }
      } catch (err) {
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.actionError = `播放失败：${err}`;
      } finally {
        if (this.actionBusy === 'play') this.actionBusy = '';
      }
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
    // ===== 播放代理（D-004、D-006、D-PC26）=====
    // 生成是后台单 worker 的 FIFO 队列：CreatePlaybackProxy 只负责入队，立即返回，不等这一项跑完。
    // 排位与完成靠 playback-proxy-state 事件：本项在队列里显示「排队中（第 N 个）」、处理中显示「生成中」，
    // 本项有了结果再回读代理元数据；结果为可用时重取预览会话，自动切到内嵌播放。
    async loadPlaybackProxy(videoID, entryToken) {
      try {
        const proxy = await GetPlaybackProxy(videoID);
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.playbackProxy = proxy || null;
      } catch (err) {
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.proxyError = `读取播放代理状态失败：${err}`;
      }
    },
    // 打开条目时这一项可能已经被别处（行菜单、批量、自动）排进队列：读一次当前快照补上排位，
    // 之后跟着事件走。只认排队与处理中，已有的旧结果不触发切换。
    async loadProxyQueueState(videoID, entryToken) {
      try {
        const status = await GetPlaybackProxyStatus();
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.applyProxyStatus(status, { fromEvent: true });
      } catch (err) {
        // 队列快照只用来显示排位，读不到时等下一次事件即可，不打扰用户。
      }
    },
    async createPlaybackProxy() {
      if (this.proxyBusy || this.proxyPending || this.currentEntry?.type !== 'video') return;
      const videoID = Number(this.currentEntry.id); const entryToken = this._entryLoadToken;
      this.proxyBusy = true; this.proxyError = ''; this.proxyNotice = '';
      this.proxyTracking = { videoID, state: { phase: 'submitting' } };
      let status = null;
      let enqueued = false;
      try {
        status = await CreatePlaybackProxy(videoID);
        enqueued = true;
      } catch (err) {
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.proxyError = `生成播放代理失败：${err}`;
        if (this.proxyTracking?.videoID === videoID) this.proxyTracking = null;
      } finally {
        if (this.isCurrentVideoRequest(videoID, entryToken)) this.proxyBusy = false;
      }
      // 入队的回执本身就是一份快照：已经在排队就接着等，已经有结果（或快照里没有这一项）就当场收尾。
      if (enqueued && this.isCurrentVideoRequest(videoID, entryToken)) this.applyProxyStatus(status || { running: false }, { fromEvent: false });
    },
    // 按一份代理状态快照推进当前视频的那一项（D-PC26、PLAY-04）。排队与处理中只更新显示；
    // 只有正在等的那一项（proxyTracking）有了结果、或整轮结束时，才收尾一次。
    // 入队请求还没返回时，事件里的空白或旧结果都不算数（那一刻队列里还没有这一项）。
    applyProxyStatus(status, { fromEvent = true } = {}) {
      if (!status || this.currentEntry?.type !== 'video') return;
      const videoID = Number(this.currentEntry.id);
      const tracking = this.proxyTracking?.videoID === videoID ? this.proxyTracking : null;
      const state = playbackProxyItemState(status, videoID, { tracked: !!tracking });
      if (state.phase === 'queued' || state.phase === 'processing') {
        this.proxyTracking = { videoID, state };
        return;
      }
      if (!tracking) return;
      if (fromEvent && tracking.state?.phase === 'submitting') return;
      if (fromEvent && state.phase === 'idle' && status.running) return;
      this.proxyTracking = null;
      this.finishProxyTask(videoID, this._entryLoadToken, state);
    },
    async finishProxyTask(videoID, entryToken, state) {
      const outcome = state.phase === 'done' ? state.outcome : '';
      if (outcome === 'failed') {
        this.proxyError = `生成播放代理失败：${PLAYBACK_PROXY_CODE_LABELS[state.result.code] || state.result.code}`;
      } else if (outcome === 'cancelled') {
        this.proxyNotice = '播放代理任务已取消。';
      }
      await this.loadPlaybackProxy(videoID, entryToken);
      if (!this.isCurrentVideoRequest(videoID, entryToken)) return;
      // 结果里有这一项就以结果为准；整轮结束却没有这一项的结果（结果条数有上限），以回读的代理状态为准。
      const ready = outcome ? outcome === 'ready' : this.playbackProxy?.status === 'ready';
      if (ready) await this.reloadPreviewSessionForProxy(videoID, entryToken);
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
    async setAvatarFromImage(image) {
      if (this.avatarSaving || this.currentEntry?.type !== 'person') return;
      const personID = Number(this.currentEntry.id);
      this.avatarSaving = true; this.avatarError = '';
      try {
        if (!image.path) throw new Error('图片路径不可用');
        await SetPersonAvatar(personID, image.path);
        if (this.isCurrentEntity('person', personID)) await this.loadCurrentEntry();
      } catch (err) {
        if (this.isCurrentEntity('person', personID)) this.avatarError = `设置头像失败：${err}`;
      } finally { this.avatarSaving = false; }
    },
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
    // 起播参数：根条目用宿主算好的续播位置；嵌套条目用同一套续播判定（utils/watchState.js，PLAY-10）——
    // 已看片只有「标已看之后又看过」的断点才续播，落进片尾区间的断点从头播。
    playbackStartOptions() {
      return {
        entryID: this.currentEntry?.id,
        rootVideoID: this.video?.id,
        explicitStartTimeMs: this.startTimeMs,
        rootResumePositionSeconds: this.resumePositionSeconds,
        nestedResumePositionSeconds: resumePosition(this.details?.video)
      };
    },
    applyStartTime(video) {
      const startTimeMs = detailPlaybackStartMs(this.playbackStartOptions());
      if (video.readyState < 1 || startTimeMs === 0) return;
      let seekSeconds = startTimeMs / 1000; if (Number.isFinite(video.duration) && video.duration > 0) seekSeconds = Math.min(seekSeconds, Math.max(video.duration - 0.001, 0));
      const seekKey = `${this.currentSession?.video_id || ''}:${seekSeconds}`; if (seekKey === this.appliedSeekKey) return; video.currentTime = seekSeconds; this.appliedSeekKey = seekKey;
    },
    // ===== 观看会话（D-PC43、PLAY-07）=====
    // 每次打开内嵌播放器是一个会话：会话标识每次新生成，累计播放首次越过 viewThreshold、或判定看完时
    // 调一次 RecordViewEvent(inline_view)。起播来源（D-PC42）在第一次开始播放时定下，整场播放不变。
    startViewSession(session) {
      const inline = session?.mode === 'inline' && session?.inline_source;
      const videoID = Number(session?.video_id || (this.currentEntry?.type === 'video' ? this.currentEntry.id : 0) || 0);
      this._viewSession = inline && videoID
        ? { id: newViewSessionID(), videoID, origin: '', accumulator: createPlaybackAccumulator(), recorded: false }
        : null;
    },
    playerDurationSeconds(video = this.$refs.videoElement) {
      const duration = Number(video?.duration);
      return Number.isFinite(duration) && duration > 0 ? duration : 0;
    },
    handlePlay() {
      this.hasPlaybackStarted = true;
      const session = this._viewSession;
      if (!session) return;
      if (!session.origin) session.origin = detailPlaybackOrigin(this.playbackStartOptions());
      session.accumulator.breakSegment();
    },
    // 暂停与拖动都断开当前一段累计：之后的一大步不能被墙钟放行成「播放」。
    handlePause() {
      this._viewSession?.accumulator.breakSegment();
      this.emitWatchProgress(true, false);
    },
    handleSeeking() { this._viewSession?.accumulator.breakSegment(); },
    handleTimeUpdate() {
      const video = this.$refs.videoElement;
      if (video && !video.paused && !video.seeking) this._viewSession?.accumulator.sample(video.currentTime, Date.now(), video.playbackRate);
      this.maybeRecordView(false);
      this.emitWatchProgress(false, false);
    },
    maybeRecordView(completedHint) {
      const session = this._viewSession;
      if (!session || session.recorded || (!this.hasPlaybackStarted && !completedHint)) return;
      const video = this.$refs.videoElement;
      const detailsDuration = Number(this.details?.video?.id) === session.videoID ? Number(this.details.video.duration) : 0;
      const duration = this.playerDurationSeconds(video) || (detailsDuration > 0 ? detailsDuration : 0);
      const completed = !!completedHint || isWatchCompleted(Number(video?.currentTime || 0), duration);
      if (!completed && session.accumulator.seconds < viewThreshold(duration)) return;
      session.recorded = true;
      // 只写一次；失败不重试，后端也不会把失败的这次记进去重表。
      Promise.resolve()
        .then(() => RecordViewEvent(session.videoID, 'inline_view', session.id))
        .catch(err => console.error('记录有效观看失败:', err));
    },
    emitWatchProgress(force, completed, videoID = null) {
      if (this.resettingVideo) return;
      this.maybeRecordView(completed);
      if (!this.hasPlaybackStarted && !completed) return; const video = this.$refs.videoElement; const positionSeconds = Number(video?.currentTime || 0);
      if (!Number.isFinite(positionSeconds) || positionSeconds <= 0) return; const now = Date.now(); if (!force && now - this.lastProgressEmittedAt < 10000) return;
      this.lastProgressEmittedAt = now;
      // 起播来源（D-PC42）：从断点 resume、从片头 start、从字幕命中或指定时间 jump；时长未知传 0，
      // 后端只在库内时长未知时采用它。
      const origin = this._viewSession?.origin || detailPlaybackOrigin(this.playbackStartOptions());
      this.$emit('watch-progress', {
        videoID: Number(videoID || this._viewSession?.videoID || this.currentSession?.video_id || this.currentEntry?.id || this.video?.id || 0),
        positionSeconds,
        completed: !!completed,
        origin,
        durationSeconds: this.playerDurationSeconds(video)
      });
    },
    // 内嵌播放器报错（PLAY-05）：<video> 自己的 error 带 MediaError（解码失败、源不支持）；
    // <source> 的 error 只在它还带着地址时才算数——换会话时清掉地址再 load() 也会触发一次。
    handleVideoError(event) {
      if (this.resettingVideo || !event?.target?.error) return;
      this.inlinePlaybackFailed = true;
    },
    handleSourceError(event) {
      if (this.resettingVideo || !event?.target?.getAttribute?.('src')) return;
      this.inlinePlaybackFailed = true;
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
.person-image-card__preview { display: block; width: 100%; min-width: 0; border: 0; padding: 0; background: transparent; cursor: zoom-in; }
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
.entity-chip--pending { border-style: dashed; }.entity-chip--pending small { color: var(--text-muted); font-size: 11px; }.entity-chip--pending .entity-chip__remove { border: 0; padding: 0; background: transparent; cursor: pointer; }.detail-create-box__hint { color: var(--text-muted); font-size: 11px; }
.entity-chip-list { display: flex; flex-wrap: wrap; gap: 8px; }.entity-chip { display: inline-flex; align-items: center; gap: 7px; border: 1px solid var(--border-color); border-radius: 999px; padding: 4px 9px 4px 5px; background: var(--control-hover-bg); color: var(--text-primary); }.entity-chip img { width: 26px; height: 26px; border-radius: 50%; object-fit: cover; }.entity-chip__remove { color: var(--danger-color); font-size: 16px; }
.detail-inline-form,.detail-action-row { display: flex; gap: 8px; flex-wrap: wrap; }.detail-inline-form input { flex: 1; }.candidate-list { display: grid; gap: 6px; }.candidate-list button { display: flex; justify-content: space-between; gap: 10px; text-align: left; border: 1px solid var(--border-color); border-radius: 9px; padding: 9px 10px; color: var(--text-primary); background: transparent; }.candidate-list small { color: var(--text-muted); }.detail-create-box { display: grid; gap: 8px; }.detail-create-box summary { cursor: pointer; color: var(--accent-color); }.detail-create-box__fields { display: grid; gap: 8px; justify-items: start; }.detail-create-box__fields input { width: 100%; }
.related-video-editor { display: grid; gap: 9px; padding-bottom: 12px; border-bottom: 1px solid var(--border-color); }.related-video-results,.related-video-list { display: grid; gap: 8px; }
.related-video-directory-filter { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px; }.related-video-directory-filter select { min-width: 0; }.related-video-directory-hint { margin: -3px 0 0; color: var(--text-muted); font-size: 11px; }
.related-video-batch-actions { display: flex; justify-content: flex-end; gap: 8px; }
.person-image-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(120px, 1fr)); align-items: start; gap: 8px; }
.person-image-card { margin: 0; min-width: 0; display: grid; grid-template-columns: minmax(0, 1fr); gap: 6px; justify-items: stretch; padding: 8px; border: 1px solid var(--hairline); border-radius: var(--radius-md); background: var(--control-hover-bg); }
.person-image-card img { width: 100%; height: auto; display: block; object-fit: contain; border-radius: 8px; background: var(--thumb-bg); }
.person-image-card figcaption { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-muted); font-size: 11px; }
.person-image-card__size { color: var(--text-muted); font-size: 11px; font-variant-numeric: tabular-nums; }
.selection-row { display: grid; grid-template-columns: auto 1fr auto; align-items: center; gap: 8px; }.selection-row button { background: transparent; border: 0; color: var(--text-primary); text-align: left; cursor: pointer; }.selection-row small { color: var(--text-muted); }
.technical-grid { display: grid; grid-template-columns: 90px 1fr; gap: 6px 10px; margin: 0; font-size: 12px; }.technical-grid dt { color: var(--text-muted); }.technical-grid dd { margin: 0; word-break: break-word; }.technical-status { margin: 0; font-size: 12px; }.technical-status--current { color: var(--success-color); }.technical-status--stale,.technical-status--error { color: var(--warning-strong); }
.proxy-block { display: grid; gap: 6px; margin-top: 10px; padding-top: 10px; border-top: 1px solid var(--border-color); }.proxy-block__status { margin: 0; color: var(--text-secondary); font-size: 12px; }
.preview-drawer__proxy-progress { margin: 0; color: var(--text-secondary); font-size: 12px; }
/* 动作条：按钮沿用行上的 row-btn（收藏 / 已看的按下态与列表一致），星级每颗分左右两半，各是一个按钮。 */
.drawer-action-bar { gap: 10px; }
.drawer-action-bar__buttons { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.drawer-stars { display: flex; flex-wrap: wrap; align-items: center; gap: 2px; }
.drawer-star { position: relative; display: inline-block; width: 20px; height: 20px; flex: none; }
.drawer-star::before,.drawer-star::after { content: '★'; position: absolute; inset: 0; font-size: 18px; line-height: 20px; text-align: left; pointer-events: none; }
.drawer-star::before { color: var(--border-strong); }
.drawer-star::after { width: 0; overflow: hidden; color: var(--accent-color); }
.drawer-star--half::after { width: 50%; }
.drawer-star--full::after { width: 100%; }
.drawer-star__half { position: absolute; top: 0; bottom: 0; width: 50%; z-index: 1; border: 0; padding: 0; background: transparent; cursor: pointer; }
.drawer-star__half--left { left: 0; }.drawer-star__half--right { right: 0; }
.drawer-star__half:disabled { cursor: progress; }
.drawer-star__half:focus-visible { outline: 2px solid var(--accent-color); outline-offset: 1px; border-radius: 3px; }
.drawer-stars__value { margin: 0 6px 0 8px; color: var(--text-secondary); font-size: 12px; font-variant-numeric: tabular-nums; }
.stream-card { display: grid; gap: 4px; padding: 10px; border-radius: 9px; background: var(--control-hover-bg); font-size: 12px; }.stream-card span { color: var(--text-secondary); word-break: break-word; }
.entity-identity > img,.entity-avatar-placeholder { width: 96px; height: 96px; object-fit: cover; border-radius: 14px; }.entity-avatar-placeholder { display: grid; place-items: center; background: var(--control-hover-bg); color: var(--text-muted); }
@media (max-width: 900px) { .preview-drawer { width: 100vw; right: 0; bottom: 0; top: 70px; min-width: 0; border-radius: 18px 18px 0 0; } }
</style>
