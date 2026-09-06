<template>
  <PhotoCleanupPage
    v-if="showCleanup"
    @close="closeCleanup"
    @deleted="handleCleanupDeleted"
  />

  <main v-else class="photo-library">
    <section class="photo-toolbar glass-surface">
      <div class="photo-toolbar__title">
        <h2>图片</h2>
        <p>独立扫描入库的本地图片库，可筛选、查看与管理。</p>
      </div>
      <div class="photo-toolbar__controls">
        <div class="photo-search-mode" role="group" aria-label="搜索模式">
          <button
            type="button"
            :class="['photo-search-mode__btn', { active: searchMode === 'name' }]"
            data-test="photo-mode-name"
            @click="setSearchMode('name')"
          >文件名</button>
          <button
            type="button"
            :class="['photo-search-mode__btn', { active: searchMode === 'semantic' }]"
            :disabled="!semanticAvailable"
            :title="semanticAvailable ? '按 AI 描述做语义检索' : semanticNotice"
            data-test="photo-mode-semantic"
            @click="setSearchMode('semantic')"
          >语义</button>
        </div>
        <div class="photo-view-mode" role="group" aria-label="图片展示模式">
          <button
            type="button"
            :class="['photo-view-mode__btn', { active: displayMode === 'stream' }]"
            data-test="photo-display-stream"
            @click="setDisplayMode('stream')"
          >图片流</button>
          <button
            type="button"
            :class="['photo-view-mode__btn', { active: displayMode === 'folders' }]"
            :disabled="searchMode === 'semantic'"
            :title="searchMode === 'semantic' ? '语义搜索结果按相关度展示，暂不按文件夹汇总' : '每个直属文件夹作为一个独立图集，不递归包含子文件夹'"
            data-test="photo-display-folders"
            @click="setDisplayMode('folders')"
          >按文件夹</button>
        </div>
        <input
          v-model="filters.keyword"
          class="search-input photo-toolbar__keyword"
          type="text"
          :placeholder="searchMode === 'semantic' ? '描述你想找的画面' : '按文件名搜索'"
          data-test="photo-keyword"
        />
        <select
          v-model="filters.sortMode"
          class="select-input photo-toolbar__sort"
          :disabled="searchMode === 'semantic' || timelineMode"
          :title="sortDisabledReason"
          data-test="photo-sort"
        >
          <option value="recent">最近添加</option>
          <option value="size">体积最大</option>
          <option value="rating">评分最高</option>
          <option value="taken">拍摄时间</option>
        </select>
        <label
          class="photo-toolbar__timeline"
          :title="searchMode === 'semantic' ? '语义模式按相关度排序，不做时间线分组' : '按拍摄时间倒序，并插入年月分组头'"
        >
          <input
            v-model="timelineMode"
            type="checkbox"
            :disabled="searchMode === 'semantic'"
            data-test="photo-timeline-toggle"
          />
          <span>时间线</span>
        </label>
        <button
          ref="photoFilterTrigger"
          type="button"
          :class="['toolbar-btn', { 'toolbar-btn--on': activePhotoFilterCount > 0 }]"
          data-test="photo-filter-open"
          @click="togglePhotoMenu('filter', 'photoFilterTrigger')"
        >
          筛选
          <span v-if="activePhotoFilterCount > 0" class="toolbar-btn__badge">{{ activePhotoFilterCount }}</span>
          <span class="toolbar-btn__caret">▾</span>
        </button>
        <button
          ref="photoManageTrigger"
          type="button"
          class="toolbar-btn toolbar-btn--strong"
          data-test="photo-manage-open"
          @click="togglePhotoMenu('manage', 'photoManageTrigger')"
        >
          管理
          <span v-if="photoAttentionCount > 0" class="toolbar-btn__badge">{{ photoAttentionCount }}</span>
          <span class="toolbar-btn__caret">▾</span>
        </button>
        <div v-if="images.length" :class="['photo-selection-tools', { 'photo-selection-tools--active': selectedImageIDs.length > 0 }]" data-test="photo-selection-tools">
          <span class="photo-selection-tools__count">已选 {{ selectedImageIDs.length }} 张</span>
          <button type="button" class="btn-secondary btn-compact" data-test="photo-select-all" @click="toggleSelectAll">
            {{ allLoadedSelected ? '取消全选' : '全选已加载' }}
          </button>
          <button v-if="selectedImageIDs.length" type="button" class="btn-secondary btn-compact" data-test="photo-clear-selection" @click="clearSelection">清除选择</button>
          <div v-if="selectedImageIDs.length" class="photo-tag-combobox photo-batch-tag-search">
            <input
              v-model="batchTagKeyword"
              type="search"
              class="search-input"
              placeholder="搜索标签，回车添加"
              role="combobox"
              autocomplete="off"
              aria-controls="photo-batch-tag-options"
              :aria-expanded="batchTagMenuOpen"
              :aria-activedescendant="batchTagMenuOpen && filteredBatchTags[batchTagActiveIndex] ? `photo-batch-tag-option-${filteredBatchTags[batchTagActiveIndex].id}` : undefined"
              data-test="photo-batch-tag-search"
              @focus="openBatchTagMenu"
              @input="handleBatchTagInput"
              @keydown="handleBatchTagKeydown"
              @blur="closeBatchTagMenu"
            />
            <div v-if="batchTagMenuOpen" id="photo-batch-tag-options" class="photo-tag-options" role="listbox" data-test="photo-batch-tag-options">
              <button
                v-for="(tag, index) in filteredBatchTags"
                :id="`photo-batch-tag-option-${tag.id}`"
                :key="tag.id"
                type="button"
                role="option"
                :aria-selected="index === batchTagActiveIndex"
                :class="['photo-tag-option', { active: index === batchTagActiveIndex }]"
                @mouseenter="batchTagActiveIndex = index"
                @mousedown.prevent="batchAddTag(tag)"
              >{{ tag.name }}</button>
              <span v-if="filteredBatchTags.length === 0" class="photo-tag-options__empty">没有匹配标签</span>
            </div>
          </div>
          <button v-if="selectedImageIDs.length" type="button" class="btn-danger btn-compact" :disabled="batchBusy" data-test="photo-batch-delete" @click="requestBatchDelete">删除所选</button>
        </div>
      </div>
      <BasePopover
        v-if="photoMenu === 'filter'"
        :anchor="photoMenuAnchor"
        :min-width="360"
        :teleport="false"
        panel-class="filter-popover photo-filter-popover"
        @close="closePhotoMenu"
      >
        <div class="photo-filter-popover__head"><strong>筛选条件</strong></div>
        <select
          v-model="filters.aiTagState"
          class="select-input photo-toolbar__ai"
          :disabled="searchMode === 'semantic'"
          :title="searchMode === 'semantic' ? '语义模式不叠加此筛选' : '按 AI 打标状态筛选'"
          data-test="photo-ai-state"
        >
          <option value="">AI 标签：全部</option>
          <option value="pending">有待审候选</option>
          <option value="tagged">已打标</option>
          <option value="untagged">未打标</option>
        </select>
        <label class="photo-toolbar__favorite">
          <input v-model="filters.favoriteOnly" type="checkbox" data-test="photo-favorite-only" />
          <span>仅收藏</span>
        </label>
        <div class="photo-toolbar__rating">
          <span>评分</span>
          <input v-model="filters.minRating" type="number" min="0" max="10" step="0.5" class="number-input" placeholder="最低" data-test="photo-min-rating" />
          <span>–</span>
          <input v-model="filters.maxRating" type="number" min="0" max="10" step="0.5" class="number-input" placeholder="最高" data-test="photo-max-rating" />
        </div>
        <div class="photo-toolbar__taken" :title="searchMode === 'semantic' ? '语义模式暂不支持拍摄日期筛选' : ''">
          <span>拍摄日期</span>
          <input
            v-model="filters.takenAfter"
            type="date"
            class="text-input photo-toolbar__date"
            aria-label="拍摄日期起"
            :disabled="searchMode === 'semantic'"
            data-test="photo-taken-after"
          />
          <span>–</span>
          <input
            v-model="filters.takenBefore"
            type="date"
            class="text-input photo-toolbar__date"
            aria-label="拍摄日期止"
            :disabled="searchMode === 'semantic'"
            data-test="photo-taken-before"
          />
        </div>
        <div class="photo-toolbar__people" :title="searchMode === 'semantic' ? '语义模式不叠加此筛选' : ''">
          <span>人物</span>
          <div v-if="personFilterSelections.length" class="photo-person-chips" data-test="photo-person-filter-chips">
            <span v-for="selection in personFilterSelections" :key="selection.id" class="tag-badge">
              {{ selection.name }}
              <button
                type="button"
                class="tag-remove"
                :aria-label="`移除人物筛选 ${selection.name}`"
                :data-test="`photo-person-filter-remove-${selection.id}`"
                @click="removePersonFilter(selection.id)"
              >×</button>
            </span>
          </div>
          <div class="photo-tag-combobox">
            <input
              v-model="personFilterKeyword"
              type="search"
              class="search-input"
              placeholder="搜索人物，回车添加"
              role="combobox"
              autocomplete="off"
              aria-controls="photo-person-filter-options"
              :aria-expanded="personFilterMenuOpen"
              :aria-activedescendant="personFilterMenuOpen && filteredPersonFilterCandidates[personFilterActiveIndex] ? `photo-person-filter-option-${filteredPersonFilterCandidates[personFilterActiveIndex].person.id}` : undefined"
              :disabled="searchMode === 'semantic'"
              data-test="photo-person-filter-search"
              @focus="openPersonFilterMenu"
              @input="handlePersonFilterInput"
              @keydown="handlePersonFilterKeydown"
              @blur="closePersonFilterMenu"
            />
            <div v-if="personFilterMenuOpen" id="photo-person-filter-options" class="photo-tag-options" role="listbox" data-test="photo-person-filter-options">
              <button
                v-for="(item, index) in filteredPersonFilterCandidates"
                :id="`photo-person-filter-option-${item.person.id}`"
                :key="item.person.id"
                type="button"
                role="option"
                :aria-selected="index === personFilterActiveIndex"
                :class="['photo-tag-option', { active: index === personFilterActiveIndex }]"
                @mouseenter="personFilterActiveIndex = index"
                @mousedown.prevent="addPersonFilter(item)"
              >{{ item.person.display_name }}</button>
              <span v-if="filteredPersonFilterCandidates.length === 0" class="photo-tag-options__empty">没有匹配人物</span>
            </div>
          </div>
        </div>
      </BasePopover>

      <BaseMenu
        v-if="photoMenu === 'manage'"
        :anchor="photoMenuAnchor"
        :items="photoManageItems"
        :min-width="240"
        align="end"
        label="图片库管理"
        @select="onPhotoManageSelect"
        @close="closePhotoMenu"
      />

      <p v-if="semanticNotice" class="photo-toolbar__semantic-notice" role="status" data-test="photo-semantic-unavailable">{{ semanticNotice }}</p>
      <div v-if="imageTags.length" class="photo-toolbar__tags">
        <button
          v-for="tag in imageTags"
          :key="tag.id"
          type="button"
          :class="['tag-chip', { active: filters.tagIDs.includes(tag.id) }]"
          :style="{ '--tag-color': tag.color }"
          @click="toggleTagFilter(tag.id)"
        >
          {{ tag.name }}
        </button>
      </div>
    </section>

    <!-- 切走再切回不重新加载列表（滚动位置要留着），所以用提示条告诉用户库里有变化。 -->
    <div v-if="libraryChanged" class="photo-library__refresh" role="status" data-test="photo-library-refresh">
      <span>图片库有更新。刷新会回到列表顶部。</span>
      <button type="button" class="btn-secondary btn-compact" data-test="photo-library-refresh-apply" @click="applyLibraryRefresh">刷新</button>
      <button type="button" class="photo-library__refresh-dismiss" aria-label="忽略此提示" data-test="photo-library-refresh-dismiss" @click="libraryChanged = false">×</button>
    </div>

    <p v-if="error" class="photo-library__error" role="alert">{{ error }}</p>
    <p v-if="folderModeRoot && folderLoading" class="photo-folder-loading" role="status" data-test="photo-folder-loading">正在整理图片文件夹…</p>

    <div v-if="folderModeActive" class="photo-folder-breadcrumb glass-surface" data-test="photo-folder-breadcrumb">
      <button type="button" class="btn-secondary btn-compact" data-test="photo-folder-back" @click="leaveFolder">← 文件夹</button>
      <div class="photo-folder-breadcrumb__text">
        <strong>{{ activeFolder.name }}</strong>
        <span :title="activeFolder.directory">{{ activeFolder.directory }} · {{ activeFolder.count }} 张图片</span>
      </div>
    </div>

    <section v-if="folderModeRoot && folderGroups.length" class="photo-folder-grid" data-test="photo-folder-grid">
      <article v-for="folder in folderGroups" :key="folder.directory" class="photo-folder-card glass-surface">
        <button
          type="button"
          class="photo-folder-card__open"
          :title="`打开 ${folder.directory}`"
          :aria-label="`打开文件夹 ${folder.name}`"
          @click="enterFolder(folder)"
        >
          <div class="photo-folder-card__covers" :data-cover-count="folder.covers?.length || 0">
            <template v-for="cover in (folder.covers || [])" :key="cover.id">
              <img
                v-if="!folderCoverFailed(folder, cover)"
                :src="`/preview/image-thumbnail/${cover.id}`"
                :alt="cover.name"
                loading="lazy"
                @error="markFolderCoverFailed(folder, cover)"
              />
              <span v-else class="photo-folder-card__cover-fallback">{{ formatBadge(cover) }}</span>
            </template>
            <span v-if="!(folder.covers || []).length" class="photo-folder-card__cover-fallback">空图集</span>
          </div>
          <div class="photo-folder-card__meta">
            <strong :title="folder.name">{{ folder.name }}</strong>
            <small>{{ folder.count }} 张图片<template v-if="folder.total_size > 0"> · {{ formatBytes(folder.total_size) }}</template></small>
            <span :title="folder.directory">{{ folder.directory }}</span>
          </div>
        </button>
        <button
          type="button"
          class="btn-danger btn-compact photo-folder-card__delete"
          :disabled="folderDeleting"
          :title="`删除 ${folder.name} 里的全部图片`"
          data-test="photo-folder-delete"
          @click="deleteFolder(folder)"
        >删除文件夹</button>
      </article>
    </section>

    <section
      v-else-if="images.length"
      ref="gridShell"
      class="photo-grid"
      :style="gridStyle"
      :data-scroll-owner-fallback="virtualized ? null : 'true'"
      data-test="photo-grid"
    >
      <div v-if="virtualized && windowState.topSpacer > 0" :style="{ height: `${windowState.topSpacer}px` }" aria-hidden="true"></div>

      <template v-for="row in renderedRows" :key="row.key">
        <div
          v-if="row.isHeader"
          class="photo-timeline-header"
          :style="rowStyle(row)"
          data-test="photo-timeline-header"
        >
          <h3>{{ timelineLabel(row) }}</h3>
        </div>
        <div v-else class="photo-grid-row" :style="rowStyle(row)">
          <article
            v-for="(image, offset) in rowImages(row)"
            :key="image.id"
            :class="['photo-card', { 'photo-card--selected': selectedImageIDs.includes(Number(image.id)) }]"
          >
            <label class="photo-card__select" :title="`选择 ${image.name}`" @click.stop>
              <input
                type="checkbox"
                :checked="selectedImageIDs.includes(Number(image.id))"
                :aria-label="`选择 ${image.name}`"
                :data-test="`photo-select-${image.id}`"
                @change="toggleImageSelection(image.id, $event.target.checked)"
              />
            </label>
            <button type="button" class="photo-card__media" :title="image.name" @click="openViewer(row.startIndex + offset)">
              <img
                v-if="!failedThumbs[image.id]"
                :src="`/preview/image-thumbnail/${image.id}`"
                :alt="image.name"
                loading="lazy"
                @error="markThumbFailed(image.id)"
              />
              <span v-else class="photo-card__fallback" data-test="photo-thumb-fallback">
                <strong>{{ image.name }}</strong>
                <em class="photo-format-badge">{{ formatBadge(image) }}</em>
              </span>
            </button>
            <div class="photo-card__overlay">
              <button
                type="button"
                :class="['photo-card__action', { 'photo-card__action--active': image.is_favorite }]"
                :title="image.is_favorite ? '取消收藏' : '收藏'"
                :aria-label="`${image.is_favorite ? '取消收藏' : '收藏'} ${image.name}`"
                @click.stop="toggleFavorite(image)"
              >{{ image.is_favorite ? '★' : '☆' }}</button>
              <button
                type="button"
                class="photo-card__action photo-card__action--danger"
                title="删除"
                :aria-label="`删除 ${image.name}`"
                data-test="photo-card-delete"
                @click.stop="requestDelete(image)"
              >×</button>
            </div>
            <div class="photo-card__meta">
              <span :title="image.name">{{ image.name }}</span>
              <small>
                {{ formatBytes(image.size) }}<template v-if="image.personal_rating != null"> · {{ image.personal_rating }} 分</template><template v-if="scoreLabel(image)"> · 相关度 {{ scoreLabel(image) }}</template>
              </small>
              <p v-if="cardTagText(image)" class="photo-card__tags" :title="cardTagText(image)" data-test="photo-card-tags">{{ cardTagText(image) }}</p>
            </div>
          </article>
        </div>
      </template>

      <div v-if="virtualized && windowState.bottomSpacer > 0" :style="{ height: `${windowState.bottomSpacer}px` }" aria-hidden="true"></div>
    </section>

    <div v-if="showEmptyState" class="photo-empty" data-test="photo-empty">
      <template v-if="searchMode === 'semantic' && !filters.keyword.trim()">
        <h3 data-test="photo-semantic-prompt">输入描述开始语义搜索</h3>
        <p>例如"海边日落的合影"。语义搜索只在图片库内进行，不会返回视频。</p>
      </template>
      <template v-else-if="searchMode === 'semantic'">
        <h3>没有语义命中</h3>
        <p v-if="semanticCoverage && Number(semanticCoverage.indexed) === 0" data-test="photo-semantic-no-index">
          图片语义索引还没有建立。请先在设置页生成 AI 描述并运行"图片语义索引"任务。
        </p>
        <p v-else>换个说法再试，或为更多图片生成 AI 描述后重跑图片语义索引。</p>
      </template>
      <template v-else-if="folderModeRoot && imageDirectories.length === 0">
        <h3>还没有配置图片扫描目录</h3>
        <p>先在设置页添加图片目录，再回来扫描入库。</p>
        <div class="photo-empty__actions">
          <button type="button" class="btn-primary" data-test="photo-empty-settings" @click="$emit('open-settings')">去设置添加图片目录</button>
          <button type="button" class="btn-secondary" :disabled="scanning" @click="scanNow">{{ scanning ? '扫描中...' : '立即扫描' }}</button>
        </div>
      </template>
      <template v-else-if="folderModeRoot && hasActiveFilters">
        <h3>没有符合筛选条件的文件夹</h3>
        <p>调整关键词、标签或评分区间后重试。</p>
      </template>
      <template v-else-if="folderModeRoot">
        <h3>还没有可展示的文件夹</h3>
        <p>目录已配置，点击扫描把磁盘上的图片同步进来。</p>
        <div class="photo-empty__actions">
          <button type="button" class="btn-primary" :disabled="scanning" @click="scanNow">{{ scanning ? '扫描中...' : '立即扫描' }}</button>
        </div>
      </template>
      <template v-else-if="folderModeActive">
        <h3>文件夹中没有符合条件的图片</h3>
        <p>调整筛选条件，或返回文件夹列表查看其他图集。</p>
      </template>
      <template v-else-if="imageDirectories.length === 0">
        <h3>还没有配置图片扫描目录</h3>
        <p>先在设置页添加图片目录，再回来扫描入库。</p>
        <div class="photo-empty__actions">
          <button type="button" class="btn-primary" data-test="photo-empty-settings" @click="$emit('open-settings')">去设置添加图片目录</button>
          <button type="button" class="btn-secondary" :disabled="scanning" @click="scanNow">{{ scanning ? '扫描中...' : '立即扫描' }}</button>
        </div>
      </template>
      <template v-else-if="hasActiveFilters">
        <h3>没有符合筛选条件的图片</h3>
        <p>调整关键词、标签或评分区间后重试。</p>
      </template>
      <template v-else>
        <h3>图片库为空</h3>
        <p>目录已配置，点击扫描把磁盘上的图片同步进来。</p>
        <div class="photo-empty__actions">
          <button type="button" class="btn-primary" :disabled="scanning" @click="scanNow">{{ scanning ? '扫描中...' : '立即扫描' }}</button>
        </div>
      </template>
    </div>

    <button
      v-if="hasMore"
      type="button"
      class="btn-secondary photo-library__more"
      :disabled="loading"
      data-test="photo-load-more"
      @click="loadMore()"
    >
      {{ loading ? '加载中...' : '加载更多图片' }}
    </button>

    <div v-if="viewerImage" class="photo-viewer" role="dialog" aria-modal="true" :aria-label="`查看 ${viewerImage.name}`" data-test="photo-viewer">
      <button type="button" class="photo-viewer__close" title="关闭 (Esc)" data-test="photo-viewer-close" @click="closeViewer">×</button>
      <button type="button" class="photo-viewer__nav photo-viewer__nav--prev" :disabled="viewerIndex <= 0" title="上一张 (←)" @click="viewerPrev">‹</button>
      <div class="photo-viewer__stage" @click.self="closeViewer">
        <img
          v-if="!viewerImageError"
          :key="viewerImage.id"
          :src="`/preview/image/${viewerImage.id}`"
          :alt="viewerImage.name"
          class="photo-viewer__img"
          @error="viewerImageError = true"
        />
        <div v-else class="photo-viewer__fallback" data-test="photo-viewer-fallback">
          <strong>{{ viewerImage.name }}</strong>
          <em class="photo-format-badge">{{ formatBadge(viewerImage) }}</em>
          <p>无法加载大图。文件可能已被移动、损坏，或该格式（如 HEIC/RAW）在当前平台不支持解码。</p>
          <small>{{ viewerImage.path }}</small>
          <small>{{ formatBytes(viewerImage.size) }}<template v-if="viewerImage.width && viewerImage.height"> · {{ viewerImage.width }}×{{ viewerImage.height }}</template></small>
        </div>
      </div>
      <button type="button" class="photo-viewer__nav photo-viewer__nav--next" :disabled="viewerIndex >= images.length - 1" title="下一张 (→)" @click="viewerNext">›</button>

      <aside class="photo-viewer__sidebar glass-surface">
        <h3 :title="viewerImage.name">{{ viewerImage.name }}</h3>
        <dl class="photo-viewer__facts">
          <dt>路径</dt><dd :title="viewerImage.path">{{ viewerImage.path }}</dd>
          <dt>尺寸</dt><dd>{{ viewerImage.width && viewerImage.height ? `${viewerImage.width}×${viewerImage.height}` : '未探测' }}</dd>
          <dt>大小</dt><dd>{{ formatBytes(viewerImage.size) }}</dd>
          <dt>格式</dt><dd>{{ formatBadge(viewerImage) }}</dd>
        </dl>

        <div v-if="exifFacts.length" class="photo-viewer__block" data-test="photo-viewer-exif">
          <h4>拍摄信息</h4>
          <dl class="photo-viewer__facts">
            <template v-for="fact in exifFacts" :key="fact.label">
              <dt>{{ fact.label }}</dt><dd :title="fact.value">{{ fact.value }}</dd>
            </template>
          </dl>
        </div>

        <div class="photo-viewer__block">
          <div class="photo-viewer__block-heading">
            <h4>收藏与评分</h4>
            <button
              type="button"
              :class="['photo-card__action', { 'photo-card__action--active': viewerImage.is_favorite }]"
              :title="viewerImage.is_favorite ? '取消收藏 (F)' : '收藏 (F)'"
              data-test="photo-viewer-favorite"
              @click="toggleFavorite(viewerImage)"
            >{{ viewerImage.is_favorite ? '★ 已收藏' : '☆ 收藏' }}</button>
          </div>
          <div class="photo-viewer__rating">
            <input
              v-model="ratingDraft"
              type="number"
              min="0"
              max="10"
              step="0.5"
              class="number-input"
              placeholder="0–10，步进 0.5"
              data-test="photo-rating-input"
              @change="applyRating"
            />
            <button type="button" class="btn-secondary btn-compact" data-test="photo-rating-clear" @click="clearRating">清空</button>
          </div>
        </div>

        <div class="photo-viewer__block">
          <h4>标签</h4>
          <div class="photo-viewer__tags">
            <span v-for="tag in viewerImage.tags || []" :key="tag.id" class="tag-badge" :style="{ '--tag-color': tag.color }">
              {{ tag.name }}
              <button type="button" class="tag-remove" :aria-label="`移除标签 ${tag.name}`" @click="removeTag(viewerImage, tag)">×</button>
            </span>
            <span v-if="!(viewerImage.tags || []).length" class="photo-viewer__muted">尚无标签</span>
          </div>
          <div v-if="addableTags.length" class="photo-viewer__tag-add photo-tag-combobox">
            <input
              v-model="tagKeyword"
              type="search"
              class="search-input"
              placeholder="搜索标签，回车添加"
              role="combobox"
              autocomplete="off"
              aria-controls="photo-tag-options"
              :aria-expanded="tagMenuOpen"
              :aria-activedescendant="tagMenuOpen && filteredAddableTags[tagActiveIndex] ? `photo-tag-option-${filteredAddableTags[tagActiveIndex].id}` : undefined"
              data-test="photo-tag-search"
              @focus="openTagMenu"
              @input="handleTagInput"
              @keydown="handleTagKeydown"
              @blur="closeTagMenu"
            />
            <div v-if="tagMenuOpen" id="photo-tag-options" class="photo-tag-options" role="listbox" data-test="photo-tag-options">
              <button
                v-for="(tag, index) in filteredAddableTags"
                :id="`photo-tag-option-${tag.id}`"
                :key="tag.id"
                type="button"
                role="option"
                :aria-selected="index === tagActiveIndex"
                :class="['photo-tag-option', { active: index === tagActiveIndex }]"
                @mouseenter="tagActiveIndex = index"
                @mousedown.prevent="addTag(viewerImage, tag)"
              >{{ tag.name }}</button>
              <span v-if="filteredAddableTags.length === 0" class="photo-tag-options__empty">没有匹配标签</span>
            </div>
          </div>
        </div>

        <div class="photo-viewer__block" data-test="photo-viewer-people">
          <h4>人物</h4>
          <div class="photo-viewer__tags">
            <span v-for="item in viewerPeople" :key="item.person.id" class="tag-badge">
              {{ item.person.display_name }}
              <button
                type="button"
                class="tag-remove"
                :aria-label="`移除人物 ${item.person.display_name}`"
                :disabled="viewerPersonBusy"
                :data-test="`photo-person-remove-${item.person.id}`"
                @click="removeViewerPerson(item)"
              >×</button>
            </span>
            <span v-if="!viewerPeople.length" class="photo-viewer__muted">尚无人物</span>
          </div>
          <div class="photo-viewer__tag-add photo-tag-combobox">
            <input
              v-model="viewerPersonKeyword"
              type="search"
              class="search-input"
              placeholder="搜索人物，回车关联"
              role="combobox"
              autocomplete="off"
              aria-controls="photo-person-options"
              :aria-expanded="viewerPersonMenuOpen"
              :aria-activedescendant="viewerPersonMenuOpen && filteredViewerPersonCandidates[viewerPersonActiveIndex] ? `photo-person-option-${filteredViewerPersonCandidates[viewerPersonActiveIndex].person.id}` : undefined"
              data-test="photo-person-search"
              @focus="openViewerPersonMenu"
              @input="handleViewerPersonInput"
              @keydown="handleViewerPersonKeydown"
              @blur="closeViewerPersonMenu"
            />
            <div v-if="viewerPersonMenuOpen" id="photo-person-options" class="photo-tag-options" role="listbox" data-test="photo-person-options">
              <button
                v-for="(item, index) in filteredViewerPersonCandidates"
                :id="`photo-person-option-${item.person.id}`"
                :key="item.person.id"
                type="button"
                role="option"
                :aria-selected="index === viewerPersonActiveIndex"
                :class="['photo-tag-option', { active: index === viewerPersonActiveIndex }]"
                @mouseenter="viewerPersonActiveIndex = index"
                @mousedown.prevent="addViewerPerson(item)"
              >{{ item.person.display_name }}</button>
              <span v-if="filteredViewerPersonCandidates.length === 0" class="photo-tag-options__empty">没有匹配人物</span>
            </div>
          </div>
        </div>

        <div class="photo-viewer__block">
          <div class="photo-viewer__block-heading">
            <h4>AI 标签候选</h4>
            <button
              type="button"
              class="btn-secondary btn-compact"
              :disabled="retagging"
              data-test="photo-ai-retag"
              @click="retagImage"
            >{{ retagging ? '打标中...' : '重新打标' }}</button>
          </div>
          <p v-if="viewerDetailError" class="photo-viewer__muted">{{ viewerDetailError }}</p>
          <ul v-else-if="viewerCandidates.length" class="photo-viewer__candidates" data-test="photo-ai-candidates">
            <li v-for="candidate in viewerCandidates" :key="candidate.id" class="photo-viewer__candidate">
              <span class="tag-chip" :style="{ '--tag-color': candidate.matched_tag?.color }">{{ candidate.suggested_name }}</span>
              <span class="photo-viewer__muted">{{ confidenceLabel(candidate.confidence) }}</span>
              <button
                type="button"
                class="btn-primary btn-compact"
                :disabled="candidateBusy"
                :data-test="`photo-ai-candidate-approve-${candidate.id}`"
                @click="approveViewerCandidate(candidate)"
              >接受</button>
              <button
                type="button"
                class="btn-secondary btn-compact"
                :disabled="candidateBusy"
                :data-test="`photo-ai-candidate-reject-${candidate.id}`"
                @click="rejectViewerCandidate(candidate)"
              >拒绝</button>
            </li>
          </ul>
          <p v-else class="photo-viewer__muted" data-test="photo-ai-candidates-empty">没有待审候选。</p>
          <p v-if="retagError" class="photo-library__error" role="alert" data-test="photo-ai-retag-error">{{ retagError }}</p>
        </div>

        <div class="photo-viewer__block">
          <button type="button" class="btn-danger" data-test="photo-viewer-delete" @click="requestDelete(viewerImage)">删除这张图片</button>
        </div>
      </aside>
    </div>

    <BaseModal v-if="deleteTarget" close-on-overlay stop-modal-clicks @close="deleteTarget = null">
      <h2>确认删除</h2>
      <p>确定要删除图片 "{{ deleteTarget.name }}"<template v-if="deleteTarget.size > 0">（{{ formatBytes(deleteTarget.size) }}）</template> 吗？</p>
      <div class="photo-delete__options">
        <label>
          <input v-model="deleteFileChoice" type="checkbox" data-test="photo-delete-file" />
          同时将原始文件移入回收站
        </label>
      </div>
      <p class="photo-viewer__muted">不勾选时仅移除数据库记录，原文件会保留在磁盘上。</p>
      <div class="modal-actions">
        <button type="button" class="btn-danger" :disabled="deleting" data-test="photo-delete-confirm" @click="confirmDelete">确认删除</button>
        <button type="button" class="btn-secondary" :disabled="deleting" @click="deleteTarget = null">取消</button>
      </div>
    </BaseModal>

    <BaseModal v-if="batchDeletePending" close-on-overlay stop-modal-clicks @close="batchDeletePending = false">
      <h2>确认删除所选图片</h2>
      <p>确定要删除已选的 {{ selectedImageIDs.length }} 张图片吗？</p>
      <div class="photo-delete__options">
        <label>
          <input v-model="batchDeleteFileChoice" type="checkbox" data-test="photo-batch-delete-file" />
          同时将原始文件移入回收站
        </label>
      </div>
      <div class="modal-actions">
        <button type="button" class="btn-danger" :disabled="batchBusy" data-test="photo-batch-delete-confirm" @click="confirmBatchDelete">确认删除</button>
        <button type="button" class="btn-secondary" :disabled="batchBusy" @click="batchDeletePending = false">取消</button>
      </div>
    </BaseModal>

    <PhotoTrashDialog :visible="showTrash" @close="showTrash = false" @restored="handleRestored" />

    <ImageAITagReviewPanel
      :visible="showAITagReview"
      @close="closeAITagReview"
      @changed="handleAITagApproved"
    />
  </main>
</template>

<script>
import {
  AddPersonImages, AddTagToImage, BatchAddTagToImages, BatchDeleteImages, DeleteImage, GetAllImageDirectories, GetImageDetail, GetImageSemanticIndexStatus, GetImageTags,
  ListPeople, RemovePersonImage,
  ApproveImageAITagCandidate, GetImageAITaggingSummary, ListImageAITagCandidates, RejectImageAITagCandidate, RetagImage,
  BatchDeleteImagesInDirectory,
  ListImageFolderGroups, ListImageTimelineBuckets, RemoveTagFromImage, SearchImagePage,
  SearchImagesSemantic, SetImageFavorite, SetImageRating, SyncImageDirectories
} from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';
import BaseMenu from './ui/BaseMenu.vue';
import BasePopover from './ui/BasePopover.vue';
import PhotoCleanupPage from './PhotoCleanupPage.vue';
import PhotoTrashDialog from './PhotoTrashDialog.vue';
import ImageAITagReviewPanel from './ImageAITagReviewPanel.vue';
import { formatBytes } from '../utils/mediaDetails.js';
import { photoCleanupStore, startPhotoCleanupPolling, stopPhotoCleanupPolling, refreshPhotoCleanupStatus } from '../utils/photoCleanupStore.js';
import { registerCommands, unregisterCommands } from '../utils/commandRegistry.js';
import { confirmAction } from '../utils/feedback.js';
import {
  PHOTO_GROUP_MONTH, PHOTO_GROUP_NONE, PHOTO_ROW_HEADER,
  buildPhotoLayout, calculatePhotoAnchorScrollTop, calculatePhotoWindow,
  firstVisiblePhotoItemIndex, formatPhotoTimelineLabel, photoTimelineKey, resolvePhotoColumns
} from '../utils/photoGrid.js';
// 只读复用视频列表的滚动宿主解析（.main-view），不修改 virtualList.js。
import { resolveScrollOwnerDescriptor } from '../utils/virtualList.js';

const PAGE_SIZE = 60;
// 网格几何常量。卡片高度固定 = 方形缩略图 + 定高信息条，布局计算才能不依赖实测回写：
// 缩略图区高度由列宽算出（aspect-ratio 1 的等价物），信息条用 CSS 钉死成 CARD_META_HEIGHT。
const GRID_GAP = 12;
const MIN_COLUMN_WIDTH = 180;
const CARD_META_HEIGHT = 86;
// glass-surface 给卡片加了 1px 边框，卡片是 border-box：上下各 1px 要算进行高，
// 缩略图的正方形边长也要相应减去，否则内容比卡片内容盒高 2px 被裁掉。
const CARD_BORDER = 2;
const TIMELINE_HEADER_HEIGHT = 40;
const OVERSCAN_ROWS = 3;
// 视口底边距列表底部小于该值时预取下一页；虚拟化后哨兵元素不再稳定出现在 DOM 里。
const LOAD_MORE_THRESHOLD = 400;

export default {
  name: 'PhotoLibraryPage',
  components: { BaseModal, BaseMenu, BasePopover, PhotoCleanupPage, PhotoTrashDialog, ImageAITagReviewPanel },
  props: {
    settings: { type: Object, required: true },
    tags: { type: Array, default: () => [] },
    // 切到别的标签页时本页只是隐藏、不卸载：已加载的图片和滚动位置都要留着。
    pageActive: { type: Boolean, default: true }
  },
  emits: ['open-settings'],
  data() {
    return {
      photoMenu: null,
      photoMenuAnchor: null,
      images: [],
      nextCursor: null,
      exhausted: false,
      loading: false,
      loadedOnce: false,
      error: '',
      searchMode: 'name',
      displayMode: window.localStorage?.getItem?.('cineinsight-photo-display-mode') === 'folders' ? 'folders' : 'stream',
      semanticStatus: null,
      semanticNoticeOverride: '',
      semanticOffset: 0,
      semanticCoverage: null,
      semanticScores: {},
      imageDirectories: [],
      imageTags: [],
      folderGroups: [],
      folderDeleting: false,
      folderLoading: false,
      folderLoadedOnce: false,
      activeFolder: null,
      failedFolderCovers: {},
      scanning: false,
      failedThumbs: {},
      filters: {
        keyword: '',
        tagIDs: [],
        // 人物筛选与标签同为 AND 语义；空数组等同不筛（D-015）。
        personIDs: [],
        favoriteOnly: false,
        minRating: '',
        maxRating: '',
        takenAfter: '',
        takenBefore: '',
        sortMode: 'recent',
        // ''=不筛 / described=已生成 / undescribed=未生成 / failed=生成失败
        aiTagState: ''
      },
      viewerIndex: -1,
      viewerImageError: false,
      viewerDetail: null,
      viewerDetailError: '',
      viewerCandidates: [],
      retagging: false,
      candidateBusy: false,
      retagError: '',
      ratingDraft: '',
      tagKeyword: '',
      tagMenuOpen: false,
      tagActiveIndex: 0,
      // 人物候选要查库（不像标签是本地全量），所以筛选栏与单图详情各自持一份
      // 关键词/候选/高亮项，交互与标签组合框一致。
      personFilterSelections: [],
      personFilterKeyword: '',
      personFilterCandidates: [],
      personFilterMenuOpen: false,
      personFilterActiveIndex: 0,
      viewerPeople: [],
      viewerPersonKeyword: '',
      viewerPersonCandidates: [],
      viewerPersonMenuOpen: false,
      viewerPersonActiveIndex: 0,
      viewerPersonBusy: false,
      selectedImageIDs: [],
      batchTagKeyword: '',
      batchTagMenuOpen: false,
      batchTagActiveIndex: 0,
      batchDeletePending: false,
      batchDeleteFileChoice: false,
      batchBusy: false,
      showTrash: false,
      showAITagReview: false,
      aiTagPending: 0,
      showCleanup: false,
      deleteTarget: null,
      deleteFileChoice: false,
      deleting: false,
      timelineMode: false,
      timelineBuckets: {},
      columns: 1,
      mediaHeight: 0,
      scrollOwnerEl: null,
      scrollOwnerMissing: false,
      // 切走前记下的滚动位置，切回来照原样恢复。
      inactiveScrollTop: 0,
      // 切回本页时探到列表首页与已加载的不一致，就提示可刷新（不自动刷，免得丢滚动位置）。
      libraryChanged: false,
      loadMoreQueued: false,
      windowState: { startRow: 0, endRow: 0, topSpacer: 0, bottomSpacer: 0, totalHeight: 0 }
    };
  },
  computed: {
    // 与视频库同构：徽标只数收进浮层的那几项低频筛选。
    activePhotoFilterCount() {
      let count = 0;
      if (this.filters.aiTagState) count += 1;
      if (this.filters.personIDs.length > 0) count += 1;
      if (this.timelineMode) count += 1;
      if (this.filters.favoriteOnly) count += 1;
      if (this.filters.minRating !== '' || this.filters.maxRating !== '') count += 1;
      if (this.filters.takenAfter || this.filters.takenBefore) count += 1;
      return count;
    },
    photoAttentionCount() {
      return (this.aiTagPending || 0) + (this.cleanupDone ? this.cleanupGroupCount || 0 : 0);
    },
    photoManageItems() {
      const cleanup = this.cleanupRunning
        ? `清理审阅（分析中 ${this.cleanupProgressText}）`
        : (this.cleanupDone && this.cleanupGroupCount ? `清理审阅（待审阅 ${this.cleanupGroupCount} 组）` : '清理审阅');
      return [
        { heading: '扫描' },
        { id: 'scan', label: this.scanning ? '立即扫描（进行中）' : '立即扫描', disabled: this.scanning },
        { heading: '维护' },
        { id: 'cleanup', label: cleanup },
        { id: 'ai-tags', label: this.aiTagPending > 0 ? `AI 标签审阅（待审 ${this.aiTagPending}）` : 'AI 标签审阅' },
        { id: 'trash', label: '回收站' }
      ];
    },
    hasMore() { return !this.folderModeRoot && !this.exhausted && this.loadedOnce; },
    folderModeRoot() { return this.displayMode === 'folders' && !this.activeFolder; },
    folderModeActive() { return this.displayMode === 'folders' && Boolean(this.activeFolder); },
    // 时间线分组只在文件名模式下生效：语义结果按相关度排序，插年月分组头没有意义。
    timelineActive() { return this.timelineMode && this.searchMode !== 'semantic'; },
    // 时间线开启时强制按拍摄时间排序，但不改写用户在下拉里的选择，关掉即恢复。
    effectiveSortMode() { return this.timelineActive ? 'taken' : this.filters.sortMode; },
    sortDisabledReason() {
      if (this.searchMode === 'semantic') return '语义模式按相关度排序';
      if (this.timelineMode) return '时间线分组固定按拍摄时间倒序';
      return '';
    },
    virtualized() { return Boolean(this.scrollOwnerEl) && !this.scrollOwnerMissing; },
    // 网格是否真的在视野里：切走标签页或清理审阅整页接管时都不是。
    gridActive() { return this.pageActive && !this.showCleanup; },
    layout() {
      return buildPhotoLayout({
        items: this.images,
        columns: this.columns,
        groupBy: this.timelineActive ? PHOTO_GROUP_MONTH : PHOTO_GROUP_NONE,
        cellHeight: this.mediaHeight + CARD_META_HEIGHT + CARD_BORDER,
        headerHeight: TIMELINE_HEADER_HEIGHT,
        gap: GRID_GAP
      });
    },
    renderedRows() {
      const rows = this.virtualized
        ? this.layout.rows.slice(this.windowState.startRow, this.windowState.endRow)
        : this.layout.rows;
      return rows.map(row => ({
        ...row,
        isHeader: row.kind === PHOTO_ROW_HEADER,
        // startIndex 在同一份布局里逐行递增，分组头带上它才不会在同一个年月出现两次时撞 key
        // （回收站恢复会把照片插到队首，可能造出不连续的同名分组）。
        key: row.kind === PHOTO_ROW_HEADER ? `h:${row.startIndex}:${row.groupKey}` : `c:${row.startIndex}`
      }));
    },
    gridStyle() {
      const style = {
        '--photo-columns': String(this.columns),
        // 行间隙与信息条高度由 JS 常量下发，保证 CSS 与 photoGrid 的布局计算同一份来源。
        '--photo-grid-gap': `${GRID_GAP}px`,
        '--photo-card-meta': `${CARD_META_HEIGHT}px`
      };
      // 尚未测量出宽度时不下发高度变量，让卡片退回 aspect-ratio: 1，避免首帧塌成 0 高。
      if (this.mediaHeight > 0) style['--photo-cell-media'] = `${this.mediaHeight}px`;
      return style;
    },
    semanticAvailable() { return Boolean(this.semanticStatus?.available); },
    semanticNotice() {
      if (this.semanticNoticeOverride) return this.semanticNoticeOverride;
      if (!this.semanticStatus || this.semanticAvailable) return '';
      const reason = String(this.semanticStatus.unavailable || '').trim();
      return `语义搜索不可用：${reason || '图片语义索引能力未就绪'}`;
    },
    viewerImage() { return this.viewerIndex >= 0 ? this.images[this.viewerIndex] || null : null; },
    cleanupRunning() { return !!photoCleanupStore.status?.running; },
    cleanupDone() {
      const s = photoCleanupStore.status;
      return Boolean(!s?.running && s?.completed && s?.analysis);
    },
    // 后台跑完不自动重来，徽标直接报出待审阅组数，提醒去处理。
    cleanupGroupCount() {
      const analysis = photoCleanupStore.status?.analysis;
      return (analysis?.duplicate_groups?.length || 0) + (analysis?.near_duplicate_groups?.length || 0);
    },
    cleanupProgressText() {
      const p = photoCleanupStore.status?.progress || {};
      return p.total > 0 ? `${p.current}/${p.total}` : '…';
    },
    cleanupBadgeTitle() {
      const p = photoCleanupStore.status?.progress || {};
      return `清理分析进行中${p.message ? '：' + p.message : ''}`;
    },
    // 卡片标签：直接用图片已有的标签，SearchImagePage 已经预载。
    cardTagText() {
      return (image) => (image?.tags || [])
        .map(tag => String(tag?.name || '').trim())
        .filter(Boolean)
        .join(' · ');
    },
    addableTags() {
      const applied = new Set((this.viewerImage?.tags || []).map(tag => Number(tag.id)));
      return this.tags.filter(tag => !applied.has(Number(tag.id)));
    },
    filteredAddableTags() {
      const keyword = this.tagKeyword.trim().toLowerCase();
      if (!keyword) return this.addableTags;
      return this.addableTags.filter(tag => String(tag?.name || '').toLowerCase().includes(keyword));
    },
    // 已经选进筛选条件的人物不再出现在候选里，与 addableTags 同一口径。
    filteredPersonFilterCandidates() {
      const applied = new Set(this.filters.personIDs.map(Number));
      return this.personFilterCandidates.filter(item => !applied.has(Number(item?.person?.id)));
    },
    filteredViewerPersonCandidates() {
      const applied = new Set(this.viewerPeople.map(item => Number(item?.person?.id)));
      return this.viewerPersonCandidates.filter(item => !applied.has(Number(item?.person?.id)));
    },
    filteredBatchTags() {
      const keyword = this.batchTagKeyword.trim().toLowerCase();
      if (!keyword) return this.tags;
      return this.tags.filter(tag => String(tag?.name || '').toLowerCase().includes(keyword));
    },
    allLoadedSelected() {
      return this.images.length > 0 && this.images.every(image => this.selectedImageIDs.includes(Number(image.id)));
    },
    hasActiveFilters() {
      return Boolean(this.filters.keyword.trim()) || this.filters.tagIDs.length > 0
        || this.filters.personIDs.length > 0 || this.filters.favoriteOnly
        || this.filters.minRating !== '' || this.filters.maxRating !== ''
        || this.filters.takenAfter !== '' || this.filters.takenBefore !== ''
        || this.filters.aiTagState !== '';
    },
    // exifFacts 只列出真正有值的项；全空时整个「拍摄信息」区不渲染。
    exifFacts() {
      const image = this.viewerImage;
      if (!image) return [];
      const camera = [image.camera_make, image.camera_model].map(part => String(part || '').trim()).filter(Boolean).join(' ');
      const facts = [
        { label: '拍摄时间', value: image.taken_at ? this.formatDateTime(image.taken_at) : '' },
        { label: '相机', value: camera },
        { label: '镜头', value: String(image.lens_model || '').trim() },
        { label: 'ISO', value: image.iso ? String(image.iso) : '' },
        { label: '光圈', value: image.f_number ? `f/${Number(image.f_number).toFixed(1)}` : '' },
        { label: '快门', value: image.exposure_time ? `${image.exposure_time} 秒` : '' },
        { label: '焦距', value: image.focal_length ? `${Number(image.focal_length).toFixed(0)} mm` : '' }
      ];
      if (image.gps_latitude != null && image.gps_longitude != null) {
        facts.push({ label: 'GPS', value: `${Number(image.gps_latitude).toFixed(6)}, ${Number(image.gps_longitude).toFixed(6)}` });
      }
      return facts.filter(fact => fact.value);
    },
    showEmptyState() {
      if (this.folderModeRoot) {
        return this.folderLoadedOnce && !this.folderLoading && this.folderGroups.length === 0;
      }
      return this.loadedOnce && !this.loading && this.images.length === 0;
    }
  },
  watch: {
    'filters.keyword'() {
      clearTimeout(this._keywordTimer);
      this._keywordTimer = setTimeout(() => this.reload(), 300);
    },
    'filters.favoriteOnly'() { this.reload(); },
    'filters.sortMode'() { this.reload(); },
    'filters.aiTagState'() { this.reload(); },
    'filters.minRating'() { this.reload(); },
    'filters.maxRating'() { this.reload(); },
    'filters.takenAfter'() { this.reload(); },
    'filters.takenBefore'() { this.reload(); },
    timelineMode() { this.reload(); },
    // 必须侦听长度而不是 images 本身：分页是 push/splice/unshift 原地改数组，
    // Vue 3 的非深度侦听只在整个引用被替换时触发，watch images 会让翻页后的窗口永远不刷新。
    'images.length'() {
      this.$nextTick(() => this.syncWindow());
    },
    // 滚动容器（.main-view）是各页共用的：切走时别的页面会把 scrollTop 改掉，
    // 所以离开前记下位置，切回来再放回去，不用从头往下滑。
    pageActive(active) {
      // 清理审阅页整页接管了主区域，这时保存/恢复的是它的滚动位置，与网格无关。
      if (this.showCleanup) return;
      if (!active) {
        this.inactiveScrollTop = this.scrollOwnerEl?.scrollTop || 0;
        return;
      }
      this.$nextTick(() => this.restoreScrollPosition());
      this.checkLibraryFreshness();
    }
  },
  mounted() {
    window.addEventListener('keydown', this.handleKeydown);
    this.loadImageDirectories();
    this.loadImageTags();
    this.loadSemanticStatus();
    this.refreshAITagSummary();
    this.reload();
    // 清理审阅状态由本页持续轮询：面板关闭后分析仍在后台跑，徽标可见进度。
    startPhotoCleanupPolling();
    // 命令面板的本页动作（D-029）。
    registerCommands('photo-library', [{
      id: 'action:photo-scan',
      group: 'action',
      label: '扫描图片目录',
      keywords: ['scan', '扫描', '图片'],
      enabled: () => !this.scanning,
      run: () => this.scanNow()
    }]);
    this.$nextTick(() => {
      this.attachResizeObserver();
      this.syncWindow(true);
    });
  },
  beforeUnmount() {
    unregisterCommands('photo-library');
    clearTimeout(this._keywordTimer);
    window.removeEventListener('keydown', this.handleKeydown);
    this.detachScrollOwner();
    this.detachResizeObserver();
    stopPhotoCleanupPolling();
  },
  methods: {
    togglePhotoMenu(name, triggerRef) {
      if (this.photoMenu === name) {
        this.closePhotoMenu();
        return;
      }
      this.photoMenuAnchor = this.$refs[triggerRef] || null;
      this.photoMenu = name;
    },
    closePhotoMenu() {
      this.photoMenu = null;
      this.photoMenuAnchor = null;
    },
    onPhotoManageSelect(item) {
      switch (item.id) {
        case 'scan': this.scanNow(); break;
        case 'cleanup': this.openCleanup(); break;
        case 'ai-tags': this.openAITagReview(); break;
        case 'trash': this.showTrash = true; break;
        default: break;
      }
    },
    formatBytes,
    formatBadge(image) {
      const format = String(image?.format || '').trim();
      if (format) return format.toUpperCase();
      const name = String(image?.name || '');
      const dot = name.lastIndexOf('.');
      return dot >= 0 ? name.slice(dot + 1).toUpperCase() : '未知格式';
    },
    folderCoverKey(folder, cover) {
      return `${folder?.directory || ''}:${cover?.id || ''}`;
    },
    folderCoverFailed(folder, cover) {
      return Boolean(this.failedFolderCovers[this.folderCoverKey(folder, cover)]);
    },
    markFolderCoverFailed(folder, cover) {
      const key = this.folderCoverKey(folder, cover);
      this.failedFolderCovers = { ...this.failedFolderCovers, [key]: true };
    },
    // 删除文件夹 = 把这个目录直属的图片整体移入回收站（可恢复），磁盘上的目录本身不动，
    // 子目录也不受影响。删完分组自然消失，不用再回来看到一个空文件夹。
    async deleteFolder(folder) {
      if (!folder?.directory || this.folderDeleting) return;
      const confirmed = await confirmAction({
        title: '删除文件夹',
        message: `将「${folder.name}」下的 ${folder.count} 张图片移入回收站（可恢复）。\n磁盘上的文件夹本身和子文件夹里的图片都不会动。`,
        confirmText: '移入回收站',
        danger: true
      });
      if (!confirmed) return;
      this.folderDeleting = true;
      this.error = '';
      try {
        const result = await BatchDeleteImagesInDirectory(folder.directory, this.settings?.delete_original_file || false);
        if (result?.failed) {
          this.error = `「${folder.name}」有 ${result.failed} 张图片删除失败，其余已移入回收站。`;
        }
        this.folderGroups = [];
        this.folderLoadedOnce = false;
        if (this.activeFolder?.directory === folder.directory) this.activeFolder = null;
        await this.reload();
        refreshPhotoCleanupStatus();
      } catch (err) {
        this.error = `删除文件夹失败：${err}`;
      } finally {
        this.folderDeleting = false;
      }
    },
    enterFolder(folder) {
      if (!folder?.directory) return;
      this.activeFolder = folder;
      this.failedThumbs = {};
      this.inactiveScrollTop = 0;
      if (this.scrollOwnerEl) this.scrollOwnerEl.scrollTop = 0;
      this.reload();
    },
    leaveFolder() {
      if (!this.folderModeActive) return;
      this.activeFolder = null;
      this.inactiveScrollTop = 0;
      if (this.scrollOwnerEl) this.scrollOwnerEl.scrollTop = 0;
      this.reload();
    },
    rowImages(row) {
      return this.images.slice(row.startIndex, row.endIndex);
    },
    rowStyle(row) {
      // outerHeight 把行间隙做成行自身的 padding-bottom（border-box），既不引入外边距合并，
      // 又让 topSpacer/bottomSpacer 与前缀和精确对齐。
      return { height: `${row.outerHeight}px`, paddingBottom: `${row.outerHeight - row.height}px` };
    },
    timelineLabel(row) {
      return formatPhotoTimelineLabel(row, this.timelineBuckets[row.groupKey]);
    },
    // ===== 网格虚拟化（AC-15）=====
    resolveScrollOwner() {
      const { nextOwner, sameOwner, missing } = resolveScrollOwnerDescriptor(this.$el, this.scrollOwnerEl);
      this.scrollOwnerMissing = missing;
      if (sameOwner) return;
      this.detachScrollOwner();
      this.scrollOwnerEl = nextOwner;
      // 找不到 .main-view 时退回整列表渲染，并把状态挂到 data-scroll-owner-fallback 上
      // （与 VirtualVideoList 同款可观测标记）。
      if (this.scrollOwnerEl) {
        this.scrollOwnerEl.addEventListener('scroll', this.handleOwnerScroll, { passive: true });
      }
    },
    detachScrollOwner() {
      if (!this.scrollOwnerEl) return;
      this.scrollOwnerEl.removeEventListener('scroll', this.handleOwnerScroll);
      this.scrollOwnerEl = null;
    },
    attachResizeObserver() {
      if (typeof ResizeObserver === 'undefined' || !this.$el?.nodeType) return;
      // 清理审阅页进出会把根节点从 <main> 换成 <section> 再换回新的 <main>，
      // 只判断"已有 observer"会让它一直盯着已经脱离文档的旧节点。
      if (this._resizeObserver && this._observedEl === this.$el) return;
      this.detachResizeObserver();
      this._resizeObserver = new ResizeObserver(() => this.handleGridResize());
      this._observedEl = this.$el;
      this._resizeObserver.observe(this.$el);
    },
    detachResizeObserver() {
      this._resizeObserver?.disconnect();
      this._resizeObserver = null;
      this._observedEl = null;
    },
    getListTop() {
      const shell = this.$refs.gridShell;
      if (!this.scrollOwnerEl || !shell) return 0;
      const ownerRect = this.scrollOwnerEl.getBoundingClientRect();
      const shellRect = shell.getBoundingClientRect();
      return this.scrollOwnerEl.scrollTop + (shellRect.top - ownerRect.top);
    },
    // measureGrid 只读容器宽度就能定出列数与卡片高度：卡片是定高的，不需要实测回写。
    // 网格未挂载（如 reload 清空列表期间）或还没布局出宽度时保留上一次的测量值，
    // 否则行高会被清成 0，卡片撑出行外互相压盖。
    measureGrid() {
      const width = this.$refs.gridShell?.getBoundingClientRect().width || 0;
      if (width <= 0) return false;
      const columns = resolvePhotoColumns(width, { minColumnWidth: MIN_COLUMN_WIDTH, gap: GRID_GAP });
      const cellWidth = (width - GRID_GAP * (columns - 1)) / columns;
      const mediaHeight = Math.max(0, Math.round(cellWidth) - CARD_BORDER);
      const changed = columns !== this.columns || mediaHeight !== this.mediaHeight;
      this.columns = columns;
      this.mediaHeight = mediaHeight;
      return changed;
    },
    // handleGridResize 在列数/卡片高度变化后重算布局，并把重算前视口顶部那张照片放回视口顶，
    // 避免宽度变化时滚动位置跳回列表开头。
    handleGridResize() {
      // 隐藏时 ResizeObserver 会报 0 宽，按它重算会把布局清空。
      if (!this.gridActive) return;
      const anchorIndex = this.virtualized
        ? firstVisiblePhotoItemIndex(this.layout, this.windowState.startRow)
        : -1;
      const changed = this.measureGrid();
      const willRestoreAnchor = changed && anchorIndex >= 0 && Boolean(this.scrollOwnerEl);
      // 锚点回写前 scrollTop 还停在旧布局的位置，拿它去跟新的（加宽后会变短的）总高比，会误判
      // 成"已经滚到底"而多预取一页；预取判断交给回写之后的那次同步。
      this.syncWindow(false, !willRestoreAnchor);
      if (!willRestoreAnchor) return;
      // 等新布局的占位块渲染出来再改 scrollTop，否则浏览器会按旧的 scrollHeight 把它夹回去。
      this.$nextTick(() => {
        if (!this.scrollOwnerEl) return;
        this.scrollOwnerEl.scrollTop = calculatePhotoAnchorScrollTop({
          layout: this.layout,
          listTop: this.getListTop(),
          itemIndex: anchorIndex
        });
        this.syncWindow();
      });
    },
    handleOwnerScroll() {
      if (!this.gridActive) return;
      this.syncWindow();
    },
    // 切回本页：先按记下的位置回写，再让虚拟化窗口跟上。占位块高度要等窗口重算后才
    // 回到原尺寸，浏览器可能先把 scrollTop 夹小，所以下一帧再校正一次。
    restoreScrollPosition() {
      this.resolveScrollOwner();
      const target = this.inactiveScrollTop;
      if (!this.scrollOwnerEl) return;
      // 0 也是要恢复的位置：从网格顶部进入审阅页、在审阅页里滚很远再返回，
      // 不写回去的话网格会停在审阅页的偏移上。
      this.scrollOwnerEl.scrollTop = target;
      this.syncWindow(true);
      const settle = () => {
        if (!this.scrollOwnerEl || !this.gridActive) return;
        // 这一帧之间用户可能已经刷新列表或主动滚走：保存位置变了就说明这次恢复已经作废，
        // 再写回去会把新的位置冲掉。
        if (this.inactiveScrollTop !== target) return;
        if (this.scrollOwnerEl.scrollTop === target) return;
        this.scrollOwnerEl.scrollTop = target;
        this.syncWindow();
      };
      if (typeof requestAnimationFrame === 'function') requestAnimationFrame(settle);
      else settle();
    },
    syncWindow(remeasure = false, allowLoadMore = true) {
      // 网格不在视野里（切走了标签页、或清理审阅整页接管）时滚动的是别人，
      // 继续按那个 scrollTop 算窗口会把 maybeLoadMore 一路触发到翻完整个库。
      if (!this.gridActive) return;
      this.resolveScrollOwner();
      if (remeasure || this.mediaHeight === 0) this.measureGrid();
      if (!this.virtualized) {
        this.windowState = { startRow: 0, endRow: this.layout.rows.length, topSpacer: 0, bottomSpacer: 0, totalHeight: this.layout.totalHeight };
        return;
      }
      const listTop = this.getListTop();
      const next = calculatePhotoWindow({
        layout: this.layout,
        scrollTop: this.scrollOwnerEl.scrollTop,
        viewportHeight: this.scrollOwnerEl.clientHeight,
        listTop,
        overscan: OVERSCAN_ROWS
      });
      // 绝大多数滚动帧窗口没有变化，原样赋值会白白触发一次重渲染。
      if (this.windowChanged(next)) this.windowState = next;
      if (allowLoadMore) this.maybeLoadMore(listTop, next.totalHeight);
    },
    windowChanged(next) {
      const current = this.windowState;
      return next.startRow !== current.startRow || next.endRow !== current.endRow
        || next.topSpacer !== current.topSpacer || next.bottomSpacer !== current.bottomSpacer
        || next.totalHeight !== current.totalHeight;
    },
    // maybeLoadMore 取代原来的 IntersectionObserver 哨兵：虚拟化后列表末尾的哨兵元素不再
    // 稳定存在于 DOM 中，改为按滚动位置与列表底部的距离判断。
    maybeLoadMore(listTop, totalHeight) {
      if (!this.hasMore || this.loading || this.loadMoreQueued || !this.scrollOwnerEl) return;
      const viewportBottom = this.scrollOwnerEl.scrollTop + this.scrollOwnerEl.clientHeight;
      if (viewportBottom < listTop + totalHeight - LOAD_MORE_THRESHOLD) return;
      this.loadMoreQueued = true;
      this.loadMore();
    },
    scoreLabel(image) {
      if (this.searchMode !== 'semantic') return '';
      const score = this.semanticScores[image?.id];
      return score == null ? '' : Number(score).toFixed(2);
    },
    formatDateTime(value) {
      if (!value) return '';
      const date = new Date(value);
      return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString();
    },
    async loadSemanticStatus() {
      try {
        this.semanticStatus = await GetImageSemanticIndexStatus() || null;
      } catch (err) {
        this.semanticStatus = { available: false, unavailable: String(err?.message || err) };
      }
    },
    setSearchMode(mode) {
      if (mode === this.searchMode) return;
      if (mode === 'semantic' && !this.semanticAvailable) return;
      this.searchMode = mode;
      this.semanticNoticeOverride = '';
      if (mode === 'semantic' && this.displayMode === 'folders') {
        this.setDisplayMode('stream');
        return;
      }
      this.reload();
    },
    setDisplayMode(mode) {
      if (mode !== 'stream' && mode !== 'folders') return;
      if (mode === 'folders' && this.searchMode === 'semantic') return;
      if (mode === this.displayMode && !this.activeFolder) return;
      this.displayMode = mode;
      window.localStorage?.setItem?.('cineinsight-photo-display-mode', mode);
      this.activeFolder = null;
      this.folderGroups = [];
      this.folderLoadedOnce = false;
      this.failedFolderCovers = {};
      this.inactiveScrollTop = 0;
      if (this.scrollOwnerEl) this.scrollOwnerEl.scrollTop = 0;
      this.reload();
    },
    // takenBoundary 把 date 输入转成 RFC3339；后端按闭区间比较，所以起点取当天 0 点、
    // 终点取当天最后一毫秒，让"某一天"能整天命中。
    takenBoundary(value, endOfDay) {
      const raw = String(value || '').trim();
      if (!raw) return null;
      const date = new Date(`${raw}T${endOfDay ? '23:59:59.999' : '00:00:00.000'}`);
      return Number.isNaN(date.getTime()) ? null : date.toISOString();
    },
    buildFilter(directory = this.activeFolder?.directory || '') {
      const parseRating = value => (value === '' || value == null ? null : Number(value));
      return {
        keyword: this.filters.keyword.trim(),
        directory,
        tag_ids: [...this.filters.tagIDs],
        person_ids: [...this.filters.personIDs],
        favorite_only: this.filters.favoriteOnly,
        min_rating: parseRating(this.filters.minRating),
        max_rating: parseRating(this.filters.maxRating),
        min_size: 0,
        max_size: 0,
        taken_after: this.takenBoundary(this.filters.takenAfter, false),
        taken_before: this.takenBoundary(this.filters.takenBefore, true),
        sort_mode: this.effectiveSortMode,
        ai_tag_state: this.filters.aiTagState
      };
    },
    buildSemanticFilter() {
      const parseRating = value => (value === '' || value == null ? null : Number(value));
      return {
        tag_ids: [...this.filters.tagIDs],
        favorite_only: this.filters.favoriteOnly,
        min_rating: parseRating(this.filters.minRating),
        max_rating: parseRating(this.filters.maxRating),
        min_size: 0,
        max_size: 0
      };
    },
    // checkLibraryFreshness 只比对列表首页：拉一页当前筛选下的结果，和已加载的前一页比 id。
    // 不动任何列表状态，只决定要不要亮提示条——自动重新加载会把滚动位置冲掉。
    async checkLibraryFreshness() {
      if (this.libraryChanged || this.loading || !this.loadedOnce) return;
      if (!this.pageActive) return;
      if (this.folderModeRoot) return;
      // 语义搜索是一次性查询结果，没有"库里多了几张"这个概念，跳过。
      if (this.searchMode === 'semantic') return;
      const token = this._queryToken;
      try {
        const page = await SearchImagePage({ filter: this.buildFilter(), limit: PAGE_SIZE });
        if (this._queryToken !== token) return;
        const incoming = (page?.images || []).map(image => Number(image.id));
        const loaded = this.images.slice(0, incoming.length).map(image => Number(image.id));
        if (incoming.length !== loaded.length || incoming.some((id, index) => id !== loaded[index])) {
          this.libraryChanged = true;
        }
      } catch (err) {
        // 探测失败不打扰用户：下次切回来再试。
      }
    },
    async applyLibraryRefresh() {
      // 先拿住滚动宿主：reload 期间列表会短暂清空，重新解析未必拿得到。
      // .main-view 本身不会随列表消失，引用始终有效。
      const owner = this.scrollOwnerEl;
      this.libraryChanged = false;
      await this.reload();
      this.inactiveScrollTop = 0;
      // 等新列表渲染出来再回顶，否则会被旧的 scrollHeight 夹住。
      await this.$nextTick();
      if (owner) owner.scrollTop = 0;
      this.syncWindow(true);
    },
    async reload() {
      const token = Symbol('photo-query');
      this._queryToken = token;
      this.libraryChanged = false;
      this.images = [];
      this.clearSelection();
      // 文件名游标分页与语义 offset 分页是两套状态，切换时一并清空避免串档。
      this.nextCursor = null;
      this.semanticOffset = 0;
      this.semanticScores = {};
      this.semanticCoverage = null;
      this.exhausted = false;
      this.failedThumbs = {};
      this.loadMoreQueued = false;
      this.closeViewer();
      if (this.folderModeRoot) {
        this.folderGroups = [];
        this.folderLoadedOnce = false;
        this.timelineBuckets = {};
        await this.loadFolderGroups(token);
        return;
      }
      const buckets = this.loadTimelineBuckets(token);
      const folderSummary = this.folderModeActive ? this.loadActiveFolderSummary(token) : null;
      await this.loadMore(token, true);
      await buckets;
      await folderSummary;
    },
    async loadFolderGroups(token, directory = '') {
      this.folderLoading = true;
      this.error = '';
      try {
        const groups = await ListImageFolderGroups(this.buildFilter(directory));
        if (this._queryToken !== token) return;
        this.folderGroups = groups || [];
        this.folderLoadedOnce = true;
      } catch (err) {
        if (this._queryToken !== token) return;
        this.folderGroups = [];
        this.folderLoadedOnce = true;
        this.error = `加载图片文件夹失败：${err}`;
      } finally {
        if (this._queryToken === token) this.folderLoading = false;
      }
    },
    async loadActiveFolderSummary(token) {
      try {
        const groups = await ListImageFolderGroups(this.buildFilter());
        if (this._queryToken !== token || !this.activeFolder) return;
        const current = groups?.[0];
        this.activeFolder = {
          ...this.activeFolder,
          count: Number(current?.count || 0)
        };
      } catch (err) {
        // 图集内容查询仍然可用，数量摘要失败不阻断浏览。
      }
    },
    // loadTimelineBuckets 拉后端的年月计数摘要：分组头要显示整组张数，而前端只加载了当前页，
    // 不能靠已加载条目数冒充总数，也不能为了算分组头去拉全量图片。
    async loadTimelineBuckets(token) {
      if (!this.timelineActive) {
        this.timelineBuckets = {};
        return;
      }
      try {
        const buckets = await ListImageTimelineBuckets(this.buildFilter()) || [];
        if (this._queryToken !== token) return;
        const counts = {};
        buckets.forEach(bucket => {
          counts[`${bucket.year}-${String(bucket.month).padStart(2, '0')}`] = bucket.count;
        });
        this.timelineBuckets = counts;
      } catch (err) {
        if (this._queryToken !== token) return;
        this.timelineBuckets = {};
        this.error = `加载时间线分组失败：${err}`;
      }
    },
    // adjustTimelineBucket 在删除/恢复单张照片后就地增减它所属的年月桶。
    // 不重新调 ListImageTimelineBuckets：那个接口会把全部匹配行读进后端，
    // 为一张照片重扫一遍全表在大图库上代价随库增长。分组键用与后端同一个 photoTimelineKey 定义。
    adjustTimelineBucket(image, delta) {
      if (!this.timelineActive) return;
      const group = photoTimelineKey(image);
      if (!group) return;
      const counts = { ...this.timelineBuckets };
      const next = (counts[group.key] || 0) + delta;
      if (next > 0) counts[group.key] = next;
      else delete counts[group.key];
      this.timelineBuckets = counts;
    },
    async loadMore(token = this._queryToken, force = false) {
      if (!token) { token = Symbol('photo-query'); this._queryToken = token; }
      if (this.folderModeRoot) return;
      if ((!force && this.loading) || (this.exhausted && !force)) return;
      if (this.searchMode === 'semantic') {
        await this.loadMoreSemantic(token);
        return;
      }
      this.loading = true;
      this.error = '';
      const request = { filter: this.buildFilter(), limit: PAGE_SIZE };
      if (this.nextCursor) request.cursor = this.nextCursor;
      try {
        const page = await SearchImagePage(request);
        if (this._queryToken !== token) return;
        const incoming = page?.images || [];
        this.images.push(...incoming);
        this.nextCursor = page?.next_cursor || null;
        this.exhausted = !this.nextCursor;
        this.loadedOnce = true;
      } catch (err) {
        if (this._queryToken === token) { this.error = `加载图片失败：${err}`; this.exhausted = true; this.loadedOnce = true; }
      } finally {
        if (this._queryToken === token) { this.loading = false; this.loadMoreQueued = false; }
      }
    },
    async loadMoreSemantic(token) {
      const query = this.filters.keyword.trim();
      if (!query) {
        this.images = [];
        this.semanticScores = {};
        this.semanticCoverage = null;
        this.semanticOffset = 0;
        this.exhausted = true;
        this.loadedOnce = true;
        this.loading = false;
        this.loadMoreQueued = false;
        return;
      }
      this.loading = true;
      this.error = '';
      const request = {
        query,
        filter: this.buildSemanticFilter(),
        offset: this.semanticOffset,
        limit: PAGE_SIZE
      };
      try {
        const page = await SearchImagesSemantic(request);
        if (this._queryToken !== token) return;
        const hits = page?.hits || [];
        const scores = { ...this.semanticScores };
        hits.forEach(hit => {
          if (!hit?.image) return;
          this.images.push(hit.image);
          scores[hit.image.id] = hit.score;
        });
        this.semanticScores = scores;
        this.semanticOffset += hits.length;
        this.semanticCoverage = page?.coverage || null;
        this.exhausted = !page?.has_more;
        this.loadedOnce = true;
      } catch (err) {
        if (this._queryToken !== token) return;
        this.exhausted = true;
        this.loadedOnce = true;
        this.loading = false;
        await this.handleSemanticFailure(err);
        return;
      } finally {
        if (this._queryToken === token) { this.loading = false; this.loadMoreQueued = false; }
      }
    },
    // handleSemanticFailure 明确暴露失败原因；若能力本身不可用则退回文件名模式并禁用语义入口。
    async handleSemanticFailure(err) {
      this.semanticNoticeOverride = `语义搜索失败：${String(err?.message || err)}`;
      await this.loadSemanticStatus();
      if (this.semanticAvailable) return;
      this.searchMode = 'name';
      await this.reload();
    },
    async loadImageDirectories() {
      try {
        this.imageDirectories = await GetAllImageDirectories() || [];
      } catch (err) {
        this.imageDirectories = [];
      }
    },
    async refreshAITagSummary() {
      try {
        const summary = await GetImageAITaggingSummary();
        this.aiTagPending = summary?.pending || 0;
      } catch (err) {
        // 待审计数只是个徽标，取不到就不显示，不打断页面。
        this.aiTagPending = 0;
      }
    },
    openAITagReview() {
      this.showAITagReview = true;
    },
    closeAITagReview() {
      this.showAITagReview = false;
      this.refreshAITagSummary();
    },
    async handleAITagApproved() {
      // 接受候选会给图片挂上标签：标签筛选栏与当前列表都可能过期。
      await this.loadImageTags();
      this.libraryChanged = true;
      this.refreshAITagSummary();
    },
    openCleanup() {
      // 审阅页会把网格从 DOM 里换走，先记下位置，返回时照原样恢复。
      this.inactiveScrollTop = this.scrollOwnerEl?.scrollTop || 0;
      this.showCleanup = true;
    },
    closeCleanup() {
      this.showCleanup = false;
      this.$nextTick(() => {
        // 换根之后要重新盯新的 $el，再恢复位置。
        this.attachResizeObserver();
        this.restoreScrollPosition();
        this.checkLibraryFreshness();
      });
    },
    async handleCleanupDeleted() {
      await this.reload();
      // reload 会把列表清空重来，恢复到旧位置没有意义。
      this.inactiveScrollTop = 0;
    },
    async loadImageTags() {
      try {
        this.imageTags = await GetImageTags() || [];
      } catch (err) {
        this.imageTags = [];
      }
    },
    async scanNow() {
      if (this.scanning) return;
      this.scanning = true;
      this.error = '';
      try {
        await SyncImageDirectories();
        await this.loadImageDirectories();
        await this.reload();
      } catch (err) {
        this.error = `扫描图片目录失败：${err}`;
      } finally {
        this.scanning = false;
      }
    },
    toggleTagFilter(tagID) {
      const id = Number(tagID);
      this.filters.tagIDs = this.filters.tagIDs.includes(id)
        ? this.filters.tagIDs.filter(item => item !== id)
        : [...this.filters.tagIDs, id];
      this.reload();
    },
    openPersonFilterMenu() {
      this.personFilterMenuOpen = true;
      this.personFilterActiveIndex = 0;
      this.searchPersonFilterCandidates();
    },
    closePersonFilterMenu() {
      this.personFilterMenuOpen = false;
    },
    handlePersonFilterInput() {
      this.personFilterMenuOpen = true;
      this.personFilterActiveIndex = 0;
      this.searchPersonFilterCandidates();
    },
    // 人物候选来自库里，输入即查；用 token 丢弃过期响应，避免慢的那一次覆盖新的。
    async searchPersonFilterCandidates() {
      const token = Symbol('person-filter-search');
      this._personFilterToken = token;
      try {
        const results = await ListPeople(this.personFilterKeyword.trim(), '', 0, 20);
        if (this._personFilterToken !== token) return;
        this.personFilterCandidates = results || [];
      } catch (err) {
        if (this._personFilterToken === token) this.error = `搜索人物失败：${err}`;
      }
    },
    handlePersonFilterKeydown(event) {
      if (event.key === 'Escape') {
        event.preventDefault();
        this.closePersonFilterMenu();
        return;
      }
      const candidates = this.filteredPersonFilterCandidates;
      if (!candidates.length) return;
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        this.personFilterMenuOpen = true;
        this.personFilterActiveIndex = (this.personFilterActiveIndex + 1) % candidates.length;
      } else if (event.key === 'ArrowUp') {
        event.preventDefault();
        this.personFilterMenuOpen = true;
        this.personFilterActiveIndex = (this.personFilterActiveIndex - 1 + candidates.length) % candidates.length;
      } else if (event.key === 'Enter') {
        event.preventDefault();
        this.addPersonFilter(candidates[this.personFilterActiveIndex] || candidates[0]);
      }
    },
    addPersonFilter(item) {
      const id = Number(item?.person?.id);
      if (!id || this.filters.personIDs.includes(id)) return;
      this.filters.personIDs = [...this.filters.personIDs, id];
      this.personFilterSelections = [...this.personFilterSelections, { id, name: item.person.display_name }];
      this.personFilterKeyword = '';
      this.personFilterMenuOpen = false;
      this.personFilterActiveIndex = 0;
      this.reload();
    },
    removePersonFilter(personID) {
      const id = Number(personID);
      if (!this.filters.personIDs.includes(id)) return;
      this.filters.personIDs = this.filters.personIDs.filter(item => item !== id);
      this.personFilterSelections = this.personFilterSelections.filter(item => Number(item.id) !== id);
      this.reload();
    },
    toggleImageSelection(imageID, checked) {
      const id = Number(imageID);
      if (!id) return;
      this.selectedImageIDs = checked
        ? (this.selectedImageIDs.includes(id) ? this.selectedImageIDs : [...this.selectedImageIDs, id])
        : this.selectedImageIDs.filter(item => item !== id);
    },
    toggleSelectAll() {
      this.selectedImageIDs = this.allLoadedSelected ? [] : this.images.map(image => Number(image.id));
    },
    clearSelection() {
      this.selectedImageIDs = [];
      this.batchTagKeyword = '';
      this.batchTagMenuOpen = false;
      this.batchTagActiveIndex = 0;
    },
    openBatchTagMenu() {
      this.batchTagMenuOpen = true;
      this.batchTagActiveIndex = 0;
    },
    closeBatchTagMenu() {
      this.batchTagMenuOpen = false;
    },
    handleBatchTagInput() {
      this.batchTagMenuOpen = true;
      this.batchTagActiveIndex = 0;
    },
    handleBatchTagKeydown(event) {
      if (event.key === 'Escape') {
        event.preventDefault();
        this.closeBatchTagMenu();
        return;
      }
      if (!this.filteredBatchTags.length) return;
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        this.batchTagMenuOpen = true;
        this.batchTagActiveIndex = (this.batchTagActiveIndex + 1) % this.filteredBatchTags.length;
      } else if (event.key === 'ArrowUp') {
        event.preventDefault();
        this.batchTagMenuOpen = true;
        this.batchTagActiveIndex = (this.batchTagActiveIndex - 1 + this.filteredBatchTags.length) % this.filteredBatchTags.length;
      } else if (event.key === 'Enter') {
        event.preventDefault();
        this.batchAddTag(this.filteredBatchTags[this.batchTagActiveIndex] || this.filteredBatchTags[0]);
      }
    },
    async batchAddTag(tag) {
      const ids = [...this.selectedImageIDs];
      const tagID = Number(tag?.id);
      if (!ids.length || !tagID || this.batchBusy) return;
      this.batchBusy = true;
      try {
        const result = await BatchAddTagToImages(ids, tagID);
        const failed = new Set((result?.errors || []).map(item => Number(item.image_id)));
        const tag = this.tags.find(item => Number(item.id) === tagID);
        if (tag) {
          ids.filter(id => !failed.has(id)).forEach(id => {
            const image = this.images.find(item => Number(item.id) === id);
            if (image && !(image.tags || []).some(item => Number(item.id) === tagID)) {
              this.patchImage({ id, tags: [...(image.tags || []), tag] });
            }
          });
        }
        this.selectedImageIDs = ids.filter(id => failed.has(id));
        this.batchTagKeyword = '';
        this.batchTagMenuOpen = false;
        this.batchTagActiveIndex = 0;
        if (result?.failed) this.error = `批量添加标签部分失败：${result.failed} 张图片未完成。`;
        await this.loadImageTags();
      } catch (err) {
        this.error = `批量添加标签失败：${err}`;
      } finally {
        this.batchBusy = false;
      }
    },
    requestBatchDelete() {
      if (!this.selectedImageIDs.length || this.batchBusy) return;
      if (!this.settings.confirm_before_delete) {
        this.performBatchDelete(!!this.settings.delete_original_file);
        return;
      }
      this.batchDeleteFileChoice = !!this.settings.delete_original_file;
      this.batchDeletePending = true;
    },
    async confirmBatchDelete() {
      if (!this.batchDeletePending || this.batchBusy) return;
      this.batchDeletePending = false;
      await this.performBatchDelete(this.batchDeleteFileChoice);
    },
    async performBatchDelete(deleteFile) {
      const ids = [...this.selectedImageIDs];
      if (!ids.length || this.batchBusy) return;
      this.batchBusy = true;
      try {
        const before = new Map(this.images.map(image => [Number(image.id), image]));
        const result = await BatchDeleteImages(ids, deleteFile);
        const failed = new Set((result?.errors || []).map(item => Number(item.image_id)));
        ids.filter(id => !failed.has(id)).forEach(id => this.adjustTimelineBucket(before.get(id), -1));
        this.images = this.images.filter(image => !ids.includes(Number(image.id)) || failed.has(Number(image.id)));
        this.selectedImageIDs = ids.filter(id => failed.has(id));
        if (result?.failed) this.error = `批量删除部分失败：${result.failed} 张图片未删除。`;
        // 单张删除早就这么做了，批量删除漏了：不作废分组的话，图片删光了文件夹
        // 还留在列表上，点进去是空的。
        this.folderGroups = [];
        this.folderLoadedOnce = false;
        if (this.folderModeActive && this.activeFolder) {
          const removed = ids.length - failed.size;
          this.activeFolder = { ...this.activeFolder, count: Math.max(0, Number(this.activeFolder.count || 0) - removed) };
        }
        refreshPhotoCleanupStatus();
      } catch (err) {
        this.error = `批量删除失败：${err}`;
      } finally {
        this.batchBusy = false;
      }
    },
    markThumbFailed(imageID) {
      this.failedThumbs = { ...this.failedThumbs, [imageID]: true };
    },
    openViewer(index) {
      if (index < 0 || index >= this.images.length) return;
      this.viewerIndex = index;
      this.viewerImageError = false;
      this.tagKeyword = '';
      this.tagMenuOpen = false;
      this.tagActiveIndex = 0;
      this.resetViewerPersonEditor();
      const image = this.images[index];
      this.ratingDraft = image.personal_rating == null ? '' : image.personal_rating;
      this.loadViewerDetail(image.id);
    },
    closeViewer() {
      this.viewerIndex = -1;
      this.viewerImageError = false;
      this.viewerDetail = null;
      this.viewerDetailError = '';
      this.viewerCandidates = [];
      this.retagError = '';
      this.tagKeyword = '';
      this.tagMenuOpen = false;
      this.tagActiveIndex = 0;
      this.resetViewerPersonEditor();
    },
    resetViewerPersonEditor() {
      this._viewerPersonToken = Symbol('viewer-person-search');
      this._viewerPeopleToken = Symbol('viewer-people');
      this.viewerPeople = [];
      this.viewerPersonKeyword = '';
      this.viewerPersonCandidates = [];
      this.viewerPersonMenuOpen = false;
      this.viewerPersonActiveIndex = 0;
    },
    viewerNext() {
      if (this.viewerIndex < this.images.length - 1) this.openViewer(this.viewerIndex + 1);
    },
    viewerPrev() {
      if (this.viewerIndex > 0) this.openViewer(this.viewerIndex - 1);
    },
    async loadViewerDetail(imageID) {
      const token = Symbol('photo-detail');
      this._detailToken = token;
      this.viewerDetail = null;
      this.viewerDetailError = '';
      this.viewerCandidates = [];
      this.retagError = '';
      this.loadViewerCandidates(imageID);
      try {
        const detail = await GetImageDetail(imageID);
        if (this._detailToken !== token) return;
        this.viewerDetail = detail;
        this.viewerPeople = detail?.people || [];
        const image = detail?.image;
        if (image && Number(image.id) === Number(this.viewerImage?.id)) {
          this.patchImage(image);
          this.ratingDraft = image.personal_rating == null ? '' : image.personal_rating;
        }
      } catch (err) {
        if (this._detailToken === token) this.viewerDetailError = `加载详情失败：${err}`;
      }
    },
    confidenceLabel(value) {
      return { high: '高', medium: '中', low: '低' }[value] || value;
    },
    // 候选与详情分开取：详情接口不带候选，而候选在接受/拒绝后要能单独刷新。
    async loadViewerCandidates(imageID) {
      const token = Symbol('photo-candidates');
      this._candidateToken = token;
      try {
        const items = await ListImageAITagCandidates(imageID, '', '');
        if (this._candidateToken !== token) return;
        this.viewerCandidates = items || [];
      } catch (err) {
        if (this._candidateToken !== token) return;
        this.viewerCandidates = [];
        this.retagError = `加载标签候选失败：${err}`;
      }
    },
    async retagImage() {
      const image = this.viewerImage;
      if (!image || this.retagging) return;
      this.retagging = true;
      this.retagError = '';
      try {
        const candidates = await RetagImage(image.id);
        if (Number(this.viewerImage?.id) !== Number(image.id)) return;
        this.viewerCandidates = candidates || [];
        this.refreshAITagSummary();
      } catch (err) {
        if (Number(this.viewerImage?.id) !== Number(image.id)) return;
        const message = String(err?.message || err);
        this.retagError = message.includes('AI 配置不可用')
          ? `重新打标失败：${message}。请先在设置页配置 AI 接口的 BaseURL 与模型。`
          : `重新打标失败：${message}`;
      } finally {
        this.retagging = false;
      }
    },
    async approveViewerCandidate(candidate) {
      const image = this.viewerImage;
      if (!image || this.candidateBusy) return;
      this.candidateBusy = true;
      this.retagError = '';
      try {
        const item = await ApproveImageAITagCandidate(candidate.id);
        // 该图已有手工标签时后端整体作废候选而不写标签，得说清楚为什么没挂上。
        if (item && item.status === 'superseded') {
          this.retagError = '这张图片已经有你手工打的标签，AI 候选已整体作废，没有写入标签。';
        } else {
          await this.loadViewerDetail(image.id);
          await this.loadImageTags();
        }
        await this.loadViewerCandidates(image.id);
        this.refreshAITagSummary();
      } catch (err) {
        this.retagError = `接受候选失败：${err}`;
      } finally {
        this.candidateBusy = false;
      }
    },
    async rejectViewerCandidate(candidate) {
      const image = this.viewerImage;
      if (!image || this.candidateBusy) return;
      this.candidateBusy = true;
      this.retagError = '';
      try {
        await RejectImageAITagCandidate(candidate.id);
        await this.loadViewerCandidates(image.id);
        this.refreshAITagSummary();
      } catch (err) {
        this.retagError = `拒绝候选失败：${err}`;
      } finally {
        this.candidateBusy = false;
      }
    },
    handleKeydown(event) {
      if (!this.pageActive) return;
      if (this.viewerIndex < 0) return;
      const tag = event.target?.tagName;
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
      if (event.key === 'Escape') { event.preventDefault(); this.closeViewer(); }
      else if (event.key === 'ArrowRight') { event.preventDefault(); this.viewerNext(); }
      else if (event.key === 'ArrowLeft') { event.preventDefault(); this.viewerPrev(); }
      else if (event.key === 'f' || event.key === 'F') {
        event.preventDefault();
        if (this.viewerImage) this.toggleFavorite(this.viewerImage);
      }
    },
    // patchImage 是等长替换，不会触发 'images.length' 侦听，因此不会重算虚拟化窗口。
    // 前提是它合并进来的字段不影响布局：现有接口都不会改写 taken_at/created_at，
    // 所以行数与分组不变。若将来有接口能改拍摄时间，这里要显式补一次 syncWindow。
    patchImage(updated) {
      if (!updated) return;
      const index = this.images.findIndex(item => Number(item.id) === Number(updated.id));
      if (index < 0) return;
      const merged = { ...this.images[index], ...updated };
      if (!updated.tags) merged.tags = this.images[index].tags;
      this.images.splice(index, 1, merged);
    },
    async toggleFavorite(image) {
      try {
        const updated = await SetImageFavorite(image.id, !image.is_favorite);
        this.patchImage(updated);
      } catch (err) {
        this.error = `更新收藏失败：${err}`;
      }
    },
    async applyRating() {
      const image = this.viewerImage;
      if (!image) return;
      const value = this.ratingDraft === '' || this.ratingDraft == null ? null : Number(this.ratingDraft);
      try {
        const updated = await SetImageRating(image.id, value);
        this.patchImage(updated);
        this.ratingDraft = updated?.personal_rating == null ? '' : updated.personal_rating;
      } catch (err) {
        this.error = `更新评分失败：${err}`;
        this.ratingDraft = image.personal_rating == null ? '' : image.personal_rating;
      }
    },
    async clearRating() {
      this.ratingDraft = '';
      await this.applyRating();
    },
    openTagMenu() {
      this.tagMenuOpen = true;
      this.tagActiveIndex = 0;
    },
    closeTagMenu() {
      this.tagMenuOpen = false;
    },
    handleTagInput() {
      this.tagMenuOpen = true;
      this.tagActiveIndex = 0;
    },
    handleTagKeydown(event) {
      if (event.key === 'Escape') {
        event.preventDefault();
        this.closeTagMenu();
        return;
      }
      if (!this.filteredAddableTags.length) return;
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        this.tagMenuOpen = true;
        this.tagActiveIndex = (this.tagActiveIndex + 1) % this.filteredAddableTags.length;
      } else if (event.key === 'ArrowUp') {
        event.preventDefault();
        this.tagMenuOpen = true;
        this.tagActiveIndex = (this.tagActiveIndex - 1 + this.filteredAddableTags.length) % this.filteredAddableTags.length;
      } else if (event.key === 'Enter') {
        event.preventDefault();
        this.addTag(this.viewerImage, this.filteredAddableTags[this.tagActiveIndex] || this.filteredAddableTags[0]);
      }
    },
    async addTag(image, tag) {
      const tagID = Number(tag?.id);
      if (!image || !tagID) return;
      try {
        await AddTagToImage(image.id, tagID);
        if (tag) this.patchImage({ id: image.id, tags: [...(image.tags || []), tag] });
        this.tagKeyword = '';
        this.tagMenuOpen = false;
        this.tagActiveIndex = 0;
        this.loadImageTags();
      } catch (err) {
        this.error = `添加标签失败：${err}`;
      }
    },
    async removeTag(image, tag) {
      if (!image || !tag) return;
      try {
        await RemoveTagFromImage(image.id, tag.id);
        this.patchImage({ id: image.id, tags: (image.tags || []).filter(item => Number(item.id) !== Number(tag.id)) });
        this.loadImageTags();
      } catch (err) {
        this.error = `移除标签失败：${err}`;
      }
    },
    openViewerPersonMenu() {
      this.viewerPersonMenuOpen = true;
      this.viewerPersonActiveIndex = 0;
      this.searchViewerPersonCandidates();
    },
    closeViewerPersonMenu() {
      this.viewerPersonMenuOpen = false;
    },
    handleViewerPersonInput() {
      this.viewerPersonMenuOpen = true;
      this.viewerPersonActiveIndex = 0;
      this.searchViewerPersonCandidates();
    },
    async searchViewerPersonCandidates() {
      const token = Symbol('viewer-person-search');
      this._viewerPersonToken = token;
      try {
        const results = await ListPeople(this.viewerPersonKeyword.trim(), '', 0, 20);
        if (this._viewerPersonToken !== token) return;
        this.viewerPersonCandidates = results || [];
      } catch (err) {
        if (this._viewerPersonToken === token) this.error = `搜索人物失败：${err}`;
      }
    },
    handleViewerPersonKeydown(event) {
      if (event.key === 'Escape') {
        event.preventDefault();
        this.closeViewerPersonMenu();
        return;
      }
      const candidates = this.filteredViewerPersonCandidates;
      if (!candidates.length) return;
      if (event.key === 'ArrowDown') {
        event.preventDefault();
        this.viewerPersonMenuOpen = true;
        this.viewerPersonActiveIndex = (this.viewerPersonActiveIndex + 1) % candidates.length;
      } else if (event.key === 'ArrowUp') {
        event.preventDefault();
        this.viewerPersonMenuOpen = true;
        this.viewerPersonActiveIndex = (this.viewerPersonActiveIndex - 1 + candidates.length) % candidates.length;
      } else if (event.key === 'Enter') {
        event.preventDefault();
        this.addViewerPerson(candidates[this.viewerPersonActiveIndex] || candidates[0]);
      }
    },
    async addViewerPerson(item) {
      const image = this.viewerImage;
      const personID = Number(item?.person?.id);
      if (!image || !personID || this.viewerPersonBusy) return;
      this.viewerPersonBusy = true;
      try {
        await AddPersonImages(personID, [Number(image.id)]);
        this.viewerPersonKeyword = '';
        this.viewerPersonMenuOpen = false;
        this.viewerPersonActiveIndex = 0;
        // 改动的人物正被当作筛选条件时，这张图是否还该出现在网格里已经变了，
        // 必须重查（reload 会连带关掉查看器，因为它清空了 images）。
        if (this.filters.personIDs.includes(personID)) {
          await this.reload();
          return;
        }
        await this.refreshViewerPeople(image.id);
      } catch (err) {
        this.error = `关联人物失败：${err}`;
      } finally {
        this.viewerPersonBusy = false;
      }
    },
    // 与视频侧一致：这是该人物跨视频与图片的最后一条关系时先确认，
    // 因为后端会连带删除人物本身。
    async removeViewerPerson(item) {
      const image = this.viewerImage;
      const personID = Number(item?.person?.id);
      if (!image || !personID || this.viewerPersonBusy) return;
      const isLastRelation = Number(item.active_video_count || 0) === 0 && Number(item.active_image_count || 0) <= 1;
      if (isLastRelation && !await confirmAction({
        title: '解除关联',
        message: '这是该人物最后一个活跃关联媒体。若没有软删除媒体保留的关系，解除后人物也会被删除，确定继续吗？',
        confirmText: '解除',
        danger: true
      })) return;
      this.viewerPersonBusy = true;
      try {
        const personDeleted = await RemovePersonImage(personID, Number(image.id));
        const filtered = this.filters.personIDs.includes(personID);
        if (personDeleted && filtered) {
          // 人物被连带清理后，筛选栏里指向它的条件已经指不到任何东西了；
          // removePersonFilter 自带 reload，不要再重查一次。
          this.removePersonFilter(personID);
          return;
        }
        if (filtered) {
          // 这张图不再命中当前人物筛选，网格必须重查。
          await this.reload();
          return;
        }
        await this.refreshViewerPeople(image.id);
      } catch (err) {
        this.error = `解除人物关联失败：${err}`;
      } finally {
        this.viewerPersonBusy = false;
      }
    },
    async refreshViewerPeople(imageID) {
      const token = Symbol('viewer-people');
      this._viewerPeopleToken = token;
      try {
        const detail = await GetImageDetail(imageID);
        if (this._viewerPeopleToken !== token || Number(this.viewerImage?.id) !== Number(imageID)) return;
        this.viewerPeople = detail?.people || [];
      } catch (err) {
        if (this._viewerPeopleToken === token) this.error = `加载图片人物失败：${err}`;
      }
    },
    requestDelete(image) {
      if (!image) return;
      if (!this.settings.confirm_before_delete) {
        this.performDelete(image, !!this.settings.delete_original_file);
        return;
      }
      this.deleteFileChoice = !!this.settings.delete_original_file;
      this.deleteTarget = image;
    },
    async confirmDelete() {
      if (!this.deleteTarget || this.deleting) return;
      this.deleting = true;
      try {
        await this.performDelete(this.deleteTarget, this.deleteFileChoice);
        this.deleteTarget = null;
      } finally {
        this.deleting = false;
      }
    },
    async performDelete(image, deleteFile) {
      try {
        await DeleteImage(image.id, deleteFile);
        const index = this.images.findIndex(item => Number(item.id) === Number(image.id));
        if (index >= 0) {
          this.images.splice(index, 1);
          if (this.viewerIndex >= 0) {
            if (index === this.viewerIndex) this.closeViewer();
            else if (index < this.viewerIndex) this.viewerIndex -= 1;
          }
        }
        // 分组头显示的是后端整组总数，删掉一张后要同步减一，否则计数长期偏大。
        this.adjustTimelineBucket(image, -1);
        if (this.folderModeActive && this.activeFolder?.directory === image.directory) {
          this.activeFolder = { ...this.activeFolder, count: Math.max(0, Number(this.activeFolder.count || 0) - 1) };
          this.folderGroups = [];
          this.folderLoadedOnce = false;
        }
        // 后端已把清理分析标记为过期，空闲时不轮询，得主动同步一次。
        refreshPhotoCleanupStatus();
      } catch (err) {
        this.error = `删除图片失败：${err}`;
      }
    },
    handleRestored(image) {
      if (!image) return;
      if (this.folderModeRoot) {
        this.folderGroups = [];
        this.folderLoadedOnce = false;
        this.reload();
      } else if ((!this.folderModeActive || this.activeFolder?.directory === image.directory)
        && !this.images.some(item => Number(item.id) === Number(image.id))) {
        this.images.unshift(image);
        this.adjustTimelineBucket(image, 1);
        if (this.folderModeActive) {
          this.activeFolder = { ...this.activeFolder, count: Number(this.activeFolder.count || 0) + 1 };
          this.folderGroups = [];
          this.folderLoadedOnce = false;
        }
      }
      this.loadedOnce = true;
      // 恢复回来的图片不该再在清理审阅里显示为"已删除"。
      const restoredID = Number(image.id);
      const review = photoCleanupStore.review;
      if (review.deletedIDs.includes(restoredID)) {
        review.deletedIDs = review.deletedIDs.filter(id => id !== restoredID);
      }
      refreshPhotoCleanupStatus();
    }
  }
};
</script>

<style scoped>
.photo-library { padding: 14px 18px 28px; display: flex; flex-direction: column; gap: 14px; }
/* 与视频库同构地吸顶：勾选若干张之后往下滚，批量按钮必须一直在手边，
   否则要滚回顶部才能操作。z-index 压过网格但让开浮层（1200）和弹窗（1000）。 */
.photo-toolbar { position: sticky; top: 0; z-index: 90; padding: 10px 16px; border: 1px solid var(--hairline); border-radius: var(--radius-md); background: var(--panel-bg); display: flex; flex-direction: column; gap: 8px; }
.photo-toolbar__title h2 { margin: 0 0 3px; font-size: 18px; }
.photo-toolbar__title p { margin: 0; color: var(--text-muted); font-size: 12px; }
.photo-toolbar__controls { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
/* 选中态把这一条变成与视频库同构的批量栏：主色底、发丝线分隔。 */
.photo-selection-tools { flex-basis: 100%; display: flex; flex-wrap: wrap; align-items: center; gap: 8px; padding-top: 4px; }
.photo-selection-tools--active { margin: 0 -16px -10px; padding: 8px 16px; border-top: 1px solid var(--accent-border); background: var(--accent-soft); }
.photo-selection-tools--active .photo-selection-tools__count { color: var(--accent-text); font-weight: 650; }
.photo-selection-tools__count { color: var(--text-secondary); font-size: 12px; }
.photo-batch-tag-search { width: 210px; }
.photo-tag-combobox { position: relative; min-width: 0; }
.photo-tag-combobox > .search-input { width: 100%; }
.photo-tag-options { position: absolute; top: calc(100% + 4px); right: 0; left: 0; z-index: 20; max-height: 220px; overflow-y: auto; padding: 4px; border: 1px solid var(--border-color); border-radius: 6px; background: var(--panel-bg); box-shadow: 0 10px 24px rgba(0, 0, 0, .22); }
.photo-tag-option { display: block; width: 100%; padding: 7px 8px; border: 0; border-radius: 4px; background: transparent; color: var(--text-primary); text-align: left; cursor: pointer; }
.photo-tag-option:hover,
.photo-tag-option.active { background: var(--control-bg); color: var(--accent-color); }
.photo-tag-options__empty { display: block; padding: 8px; color: var(--text-muted); font-size: 12px; }
.photo-cleanup-open-btn { display: inline-flex; align-items: center; gap: 6px; }
.photo-cleanup-badge { padding: 1px 7px; border-radius: 999px; background: var(--control-bg); color: var(--text-secondary); font-size: 10px; white-space: nowrap; }
.photo-cleanup-badge--done { background: var(--accent-color); color: #fff; }
.photo-toolbar__keyword { max-width: 240px; }
.photo-toolbar__sort { width: auto; min-width: 120px; }
.photo-toolbar__ai { width: auto; min-width: 140px; }
.photo-toolbar__favorite { display: inline-flex; align-items: center; gap: 6px; color: var(--text-primary); font-size: 13px; white-space: nowrap; cursor: pointer; }
.photo-toolbar__rating { display: inline-flex; align-items: center; gap: 6px; color: var(--text-muted); font-size: 12px; }
.photo-toolbar__rating .number-input { width: 76px; }
.photo-toolbar__taken { display: inline-flex; align-items: center; gap: 6px; color: var(--text-muted); font-size: 12px; }
.photo-toolbar__timeline { display: inline-flex; align-items: center; gap: 6px; color: var(--text-primary); font-size: 13px; white-space: nowrap; cursor: pointer; }
.photo-toolbar__timeline input:disabled { cursor: not-allowed; }
.photo-toolbar__date { width: 148px; }
.photo-toolbar__date:disabled { opacity: 0.45; cursor: not-allowed; }
.photo-toolbar__tags { display: flex; flex-wrap: wrap; gap: 6px; }
.photo-toolbar__people { display: grid; gap: 6px; }
.photo-toolbar__people > span:first-child { color: var(--text-muted); font-size: 12px; }
.photo-person-chips { display: flex; flex-wrap: wrap; gap: 6px; }
.photo-search-mode { display: inline-flex; padding: 2px; border: 1px solid var(--border-color); border-radius: 999px; background: var(--control-bg); }
.photo-search-mode__btn { padding: 4px 12px; border: 0; border-radius: 999px; background: transparent; color: var(--text-secondary); font-size: 12px; cursor: pointer; }
.photo-search-mode__btn.active { background: var(--panel-bg); color: var(--text-primary); }
.photo-search-mode__btn:disabled { opacity: 0.45; cursor: not-allowed; }
.photo-view-mode { display: inline-flex; padding: 2px; border: 1px solid var(--border-color); border-radius: 999px; background: var(--control-bg); }
.photo-view-mode__btn { padding: 4px 12px; border: 0; border-radius: 999px; background: transparent; color: var(--text-secondary); font-size: 12px; cursor: pointer; }
.photo-view-mode__btn.active { background: var(--panel-bg); color: var(--text-primary); }
.photo-view-mode__btn:disabled { opacity: 0.45; cursor: not-allowed; }
.photo-toolbar__semantic-notice { margin: 0; color: var(--text-muted); font-size: 12px; }
.photo-library__refresh { display: flex; align-items: center; gap: 10px; margin: 0 0 10px; padding: 8px 12px; border: 1px solid var(--accent-color); border-radius: 10px; background: var(--control-bg); color: var(--text-primary); font-size: 12px; }
.photo-library__refresh span { flex: 1; min-width: 0; }
.photo-library__refresh-dismiss { flex: none; width: 22px; height: 22px; padding: 0; border: 0; border-radius: 999px; background: transparent; color: var(--text-muted); font-size: 15px; line-height: 1; cursor: pointer; }
.photo-library__refresh-dismiss:hover { background: var(--border-color); color: var(--text-primary); }
.photo-library__error { margin: 0; color: var(--danger-color); }
.photo-folder-loading { margin: 0; color: var(--text-muted); font-size: 12px; }

.photo-folder-breadcrumb { display: flex; align-items: center; gap: 10px; padding: 9px 12px; border-radius: 10px; }
.photo-folder-breadcrumb__text { display: grid; min-width: 0; gap: 2px; }
.photo-folder-breadcrumb__text strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-primary); font-size: 13px; }
.photo-folder-breadcrumb__text span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-muted); font-size: 11px; }
.photo-folder-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 12px; }
.photo-folder-card { position: relative; min-width: 0; overflow: hidden; border-radius: 13px; }
/* 删除按钮压在封面右上角，且在"打开文件夹"那个大按钮之外，点它不会误进文件夹。 */
.photo-folder-card__delete { position: absolute; z-index: 2; top: 8px; right: 8px; opacity: 0; transition: opacity var(--transition); }
.photo-folder-card:hover .photo-folder-card__delete,
.photo-folder-card__delete:focus-visible { opacity: 1; }
.photo-folder-card__open { display: block; width: 100%; padding: 0; border: 0; background: transparent; color: inherit; text-align: left; cursor: pointer; }
.photo-folder-card__open:focus-visible { outline: 2px solid var(--accent-color); outline-offset: -2px; }
.photo-folder-card__covers { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); grid-template-rows: repeat(2, minmax(0, 1fr)); aspect-ratio: 1.55; gap: 2px; overflow: hidden; background: var(--thumb-bg); }
.photo-folder-card__covers img { width: 100%; height: 100%; min-width: 0; min-height: 0; display: block; object-fit: cover; }
.photo-folder-card__covers[data-cover-count="1"] img { grid-column: 1 / -1; grid-row: 1 / -1; }
.photo-folder-card__covers[data-cover-count="3"] img:last-of-type { grid-column: 1 / -1; }
.photo-folder-card__cover-fallback { display: flex; align-items: center; justify-content: center; min-width: 0; min-height: 0; color: var(--text-muted); font-size: 11px; }
.photo-folder-card__meta { display: grid; gap: 3px; padding: 10px 12px 12px; }
.photo-folder-card__meta strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-primary); font-size: 13px; }
.photo-folder-card__meta small { color: var(--text-secondary); font-size: 11px; }
.photo-folder-card__meta span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-muted); font-size: 10px; }

/* 虚拟化网格：外层只负责堆叠"行"，列数由 --photo-columns 显式给出（而不是 auto-fill），
   保证 DOM 布局与 photoGrid.js 的窗口计算用的是同一个列数。 */
.photo-grid { display: block; }
.photo-grid-row { display: grid; grid-template-columns: repeat(var(--photo-columns, 1), minmax(0, 1fr)); gap: var(--photo-grid-gap, 12px); box-sizing: border-box; }
.photo-timeline-header { display: flex; align-items: flex-end; overflow: hidden; box-sizing: border-box; }
.photo-timeline-header h3 { margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-secondary); font-size: 13px; font-weight: 600; letter-spacing: 0.2px; }
.photo-card { position: relative; overflow: hidden; border: 1px solid var(--hairline); border-radius: var(--radius-md); background: var(--panel-bg); }
.photo-card:hover { border-color: var(--border-strong); }
/* 勾选框只在悬停或已选时显形：常驻会在密集缩略图上形成一片噪点。
   键盘聚焦时也要显形，否则用键盘的人根本看不到它。 */
.photo-card__select { position: absolute; z-index: 3; top: 6px; left: 6px; display: grid; place-items: center; width: 26px; height: 26px; border-radius: 6px; background: var(--overlay-strong); cursor: pointer; opacity: 0; transition: opacity var(--transition); }
.photo-card:hover .photo-card__select,
.photo-card__select:focus-within,
.photo-card--selected .photo-card__select { opacity: 1; }
.photo-card__select input { width: 16px; height: 16px; margin: 0; accent-color: var(--accent-color); }
/* 定高卡片：缩略图区高度由列宽算出（等价于 aspect-ratio 1），信息条固定 52px，
   两者相加即 photoGrid 的 cellHeight，布局无需实测回写。 */
.photo-card__media { display: block; width: 100%; height: var(--photo-cell-media, auto); aspect-ratio: 1; padding: 0; border: 0; background: var(--thumb-bg); cursor: pointer; }
.photo-card__media img { width: 100%; height: 100%; display: block; object-fit: cover; }
.photo-card__fallback { width: 100%; height: 100%; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 8px; padding: 12px; color: var(--text-muted); }
.photo-card__fallback strong { max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-primary); font-size: 12px; }
.photo-format-badge { display: inline-block; padding: 2px 8px; border: 1px solid var(--border-color); border-radius: 999px; background: var(--control-bg); color: var(--text-secondary); font-size: 11px; font-style: normal; font-weight: 700; letter-spacing: 0.4px; }
.photo-card__overlay { position: absolute; top: 8px; right: 8px; display: flex; gap: 6px; opacity: 0; transition: opacity var(--transition); }
.photo-card:hover .photo-card__overlay,
.photo-card:focus-within .photo-card__overlay { opacity: 1; }
.photo-card__action { display: inline-flex; align-items: center; justify-content: center; min-width: 26px; height: 26px; padding: 0 7px; border: 1px solid var(--border-color); border-radius: 999px; background: var(--panel-bg); color: var(--text-secondary); font-size: 13px; cursor: pointer; }
.photo-card__action--active { color: var(--warning-color); opacity: 1; }
.photo-card:has(.photo-card__action--active) .photo-card__overlay { opacity: 1; }
.photo-card__action--danger:hover { color: var(--danger-color); border-color: var(--danger-border); }
.photo-card__meta { display: grid; align-content: start; gap: 2px; height: var(--photo-card-meta, 86px); box-sizing: border-box; overflow: hidden; padding: 9px 11px 10px; }
.photo-card__meta span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; color: var(--text-primary); }
.photo-card__meta small { color: var(--text-muted); font-size: 11px; }
.photo-card__tags {
  margin: 2px 0 0;
  color: var(--text-secondary);
  font-size: 11px;
  line-height: 1.4;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.photo-empty { padding: 48px 16px; text-align: center; color: var(--text-muted); }
.photo-empty h3 { margin: 0 0 8px; color: var(--text-primary); }
.photo-empty p { margin: 0 0 16px; font-size: 13px; }
.photo-empty__actions { display: flex; justify-content: center; gap: 10px; }
.photo-library__more { align-self: center; }

/* grid-template-rows 必须显式给一行确定高度：隐式行是 auto，img 的 max-height:100%
   会因为父级高度不确定而失效，竖图就按原始尺寸撑出视口显示不全。 */
.photo-viewer { position: fixed; inset: 0; z-index: 200; display: grid; grid-template-columns: minmax(0, 1fr) min(360px, 38vw); grid-template-rows: minmax(0, 1fr); background: rgba(8, 12, 20, 0.86); }
.photo-viewer__stage { position: relative; display: flex; align-items: center; justify-content: center; min-width: 0; min-height: 0; overflow: hidden; padding: 48px 56px; }
.photo-viewer__img { max-width: 100%; max-height: 100%; object-fit: contain; border-radius: 6px; }
.photo-viewer__fallback { max-width: 420px; display: grid; gap: 10px; justify-items: center; padding: 24px; border: 1px dashed rgba(255, 255, 255, 0.3); border-radius: 12px; color: rgba(255, 255, 255, 0.75); text-align: center; }
.photo-viewer__fallback strong { color: #fff; word-break: break-all; }
.photo-viewer__fallback p { margin: 0; font-size: 13px; }
.photo-viewer__fallback small { font-size: 11px; word-break: break-all; }
.photo-viewer__close { position: absolute; top: 14px; left: 14px; z-index: 2; width: 34px; height: 34px; border: 0; border-radius: 999px; background: rgba(255, 255, 255, 0.14); color: #fff; font-size: 18px; cursor: pointer; }
.photo-viewer__nav { position: absolute; top: 50%; z-index: 2; width: 40px; height: 40px; border: 0; border-radius: 999px; background: rgba(255, 255, 255, 0.14); color: #fff; font-size: 22px; cursor: pointer; transform: translateY(-50%); }
.photo-viewer__nav:disabled { opacity: 0.35; cursor: default; }
.photo-viewer__nav--prev { left: 14px; }
.photo-viewer__nav--next { right: calc(min(360px, 38vw) + 14px); }
.photo-viewer__sidebar { display: flex; flex-direction: column; gap: 14px; min-width: 0; padding: 18px 16px; border-radius: 0; overflow-y: auto; }
.photo-viewer__sidebar h3 { margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 15px; }
.photo-viewer__facts { display: grid; grid-template-columns: max-content 1fr; gap: 6px 10px; margin: 0; font-size: 12px; }
.photo-viewer__facts dt { color: var(--text-muted); }
.photo-viewer__facts dd { margin: 0; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-primary); }
.photo-viewer__block { display: grid; gap: 8px; padding-top: 12px; border-top: 1px solid var(--border-color); }
.photo-viewer__block h4 { margin: 0; font-size: 13px; color: var(--text-secondary); }
.photo-viewer__block-heading { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.photo-viewer__block-heading h4 { margin: 0; font-size: 13px; color: var(--text-secondary); }
.photo-viewer__rating { display: flex; align-items: center; gap: 8px; }
.photo-viewer__rating .number-input { width: 120px; }
.photo-viewer__tags { display: flex; flex-wrap: wrap; gap: 6px; }
.photo-viewer__tag-add { width: 100%; }
.photo-viewer__muted { margin: 0; color: var(--text-muted); font-size: 12px; }
.photo-viewer__candidates { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.photo-viewer__candidate { display: flex; align-items: center; gap: 8px; }
.photo-delete__options { display: grid; gap: 8px; margin-top: 14px; }
.photo-delete__options label { display: inline-flex; align-items: center; gap: 7px; font-size: 13px; }

@media (max-width: 900px) {
  .photo-viewer { grid-template-columns: 1fr; grid-template-rows: minmax(0, 1fr) auto; }
  .photo-viewer__nav--next { right: 14px; }
  .photo-viewer__sidebar { max-height: 42vh; }
}
</style>
