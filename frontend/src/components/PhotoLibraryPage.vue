<template>
  <main class="photo-library">
    <PageHeader class="photo-library__header" title="图片" subtitle="独立扫描入库的本地图片库，可筛选、查看与管理。" />

    <section class="photo-toolbar glass-surface">
      <div class="photo-toolbar__controls">
        <!-- 搜索模式与展示模式（D-UD-06 SegmentedControl），每个选项带 photo-mode-* / photo-display-* 钩子。
             文件夹里再点一次「按文件夹」回到文件夹列表：分段控件点当前项不发事件，靠冒上来的 click 接住。 -->
        <SegmentedControl
          class="photo-search-mode"
          label="搜索模式"
          :options="searchModeOptions"
          :model-value="searchMode"
          @update:model-value="setSearchMode"
        />
        <SegmentedControl
          class="photo-view-mode"
          label="图片展示模式"
          :options="displayModeOptions"
          :model-value="displayMode"
          @update:model-value="setDisplayMode"
          @click="reselectFolders"
        />
        <input
          v-model="filters.keyword"
          class="search-input photo-toolbar__keyword"
          type="text"
          :placeholder="searchMode === 'semantic' ? '输入想找的内容，按文件名与标签匹配' : '按文件名搜索'"
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
          :class="['btn', 'btn--secondary', 'photo-toolbar__menu', { 'photo-toolbar__menu--on': activePhotoFilterCount > 0 }]"
          :aria-expanded="photoMenu === 'filter' ? 'true' : 'false'"
          data-test="photo-filter-open"
          @click="togglePhotoMenu('filter', 'photoFilterTrigger')"
        >
          <AppIcon name="filter" :size="16" />
          筛选
          <AppBadge v-if="activePhotoFilterCount > 0" tone="accent" :count="activePhotoFilterCount" />
          <AppIcon class="photo-toolbar__caret" name="chevron-down" :size="16" />
        </button>
        <button
          ref="photoManageTrigger"
          type="button"
          class="btn btn--secondary photo-toolbar__menu"
          :aria-expanded="photoMenu === 'manage' ? 'true' : 'false'"
          data-test="photo-manage-open"
          @click="togglePhotoMenu('manage', 'photoManageTrigger')"
        >
          管理
          <AppBadge v-if="photoAttentionCount > 0" tone="accent" :count="photoAttentionCount" />
          <AppIcon class="photo-toolbar__caret" name="chevron-down" :size="16" />
        </button>
        <!-- 没有勾选时在这里一键全选（与片库工具栏同一句文案）；有勾选之后全选与批量动作都在底部的选择操作条里。 -->
        <button
          v-if="images.length && !selectedImageIDs.length"
          type="button"
          class="btn btn--secondary"
          data-test="photo-select-all"
          @click="selectAllDisplayed"
        >{{ selectAllDisplayedLabel }}</button>
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
        <!-- 「缺失某项」（D-MC-03）：拍摄时间与地理位置只看解析过 EXIF 的图片；完整度页的「去处理」也落在这里。 -->
        <select
          v-model="filters.missing"
          class="select-input photo-toolbar__ai"
          aria-label="缺少某项"
          :disabled="searchMode === 'semantic'"
          :title="searchMode === 'semantic' ? '语义模式不叠加此筛选' : '只看缺少某项的图片'"
          data-test="photo-missing-filter"
        >
          <option value="">缺少…</option>
          <option v-for="option in missingOptions" :key="option.value" :value="option.value">缺少{{ option.label }}</option>
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
                class="btn-icon tag-remove"
                :aria-label="`移除人物筛选 ${selection.name}`"
                :data-test="`photo-person-filter-remove-${selection.id}`"
                @click="removePersonFilter(selection.id)"
              ><AppIcon name="close" :size="16" /></button>
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
              <div
                v-for="(item, index) in filteredPersonFilterCandidates"
                :id="`photo-person-filter-option-${item.person.id}`"
                :key="item.person.id"
                role="option"
                :aria-selected="index === personFilterActiveIndex"
                :class="['photo-tag-option', { active: index === personFilterActiveIndex }]"
                @mouseenter="personFilterActiveIndex = index"
                @mousedown.prevent="addPersonFilter(item)"
              >{{ item.person.display_name }}</div>
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

      <p v-if="semanticNotice" class="photo-toolbar__semantic-notice" role="status" data-test="photo-semantic-unavailable">
        <AppIcon name="info" :size="16" />{{ semanticNotice }}
      </p>
      <div v-if="imageTags.length" class="photo-toolbar__tags" role="group" aria-label="按标签筛选">
        <FilterChip
          v-for="tag in imageTags"
          :key="tag.id"
          :label="tag.name"
          :active="filters.tagIDs.includes(tag.id)"
          data-test="photo-tag-filter"
          @toggle="toggleTagFilter(tag.id)"
        />
      </div>
    </section>

    <!-- 切走再切回不重新加载列表（滚动位置要留着），所以用横幅告诉用户库里有变化。 -->
    <AppBanner v-if="libraryChanged" tone="accent" data-test="photo-library-refresh" title="图片库有更新。刷新会回到列表顶部。">
      <template #actions>
        <button type="button" class="btn btn--secondary btn--sm" data-test="photo-library-refresh-apply" @click="applyLibraryRefresh">刷新</button>
        <button type="button" class="btn-icon btn--sm" aria-label="忽略此提示" data-test="photo-library-refresh-dismiss" @click="libraryChanged = false"><AppIcon name="close" :size="16" /></button>
      </template>
    </AppBanner>

    <TrashUndoBanner
      ref="trashUndo"
      kind="image"
      :after-restore="afterTrashRestore"
    />

    <AppBanner v-if="error" class="photo-library__error" tone="danger" role="alert" :title="error" />
    <p v-if="folderModeRoot && folderLoading" class="photo-folder-loading" role="status" data-test="photo-folder-loading">正在整理图片文件夹…</p>
    <div v-if="folderModeRoot && folderLoading && !folderGroups.length" class="photo-folder-grid" aria-hidden="true">
      <SkeletonBlock v-for="index in 8" :key="index" shape="card" :lines="2" />
    </div>

    <div v-if="folderModeActive" class="photo-folder-breadcrumb glass-surface" data-test="photo-folder-breadcrumb">
      <button type="button" class="btn btn--ghost btn--sm" data-test="photo-folder-back" @click="leaveFolder"><AppIcon name="chevron-left" :size="16" />文件夹</button>
      <div class="photo-folder-breadcrumb__text">
        <strong>{{ activeFolder.name }}</strong>
        <span :title="activeFolder.directory">{{ activeFolder.directory }} · {{ activeFolder.count }} 张图片</span>
      </div>
      <button type="button" class="btn btn--secondary btn--sm photo-folder-breadcrumb__reveal" @click="openSystemFolder(activeFolder.directory)">在访达中显示</button>
    </div>

    <div v-if="folderModeRoot && folderGroups.length" ref="folderShell" class="photo-folder-window" data-test="photo-folder-window">
    <div v-if="folderWindowed && folderWindow.topSpacer > 0" :style="{ height: `${folderWindow.topSpacer}px` }" aria-hidden="true"></div>
    <section class="photo-folder-grid" data-test="photo-folder-grid">
      <article v-for="folder in renderedFolders" :key="folder.directory" class="photo-folder-card glass-surface">
        <button
          type="button"
          class="btn-link photo-folder-card__open"
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
        <div class="photo-folder-card__actions">
          <button type="button" class="btn btn--ghost btn--sm" data-test="photo-folder-system-open" @click="openSystemFolder(folder.directory)">在访达中显示</button>
        </div>
        <button
          type="button"
          class="btn btn--danger btn--sm photo-folder-card__delete"
          :disabled="folderDeleting"
          :title="settings?.delete_original_file ? `把 ${folder.name} 里的全部图片移到废纸篓` : `把 ${folder.name} 里的全部图片从图片库移除`"
          data-test="photo-folder-delete"
          @click="deleteFolder(folder)"
        >{{ deleteVerb }}…</button>
      </article>
    </section>
    <div v-if="folderWindowed && folderWindow.bottomSpacer > 0" :style="{ height: `${folderWindow.bottomSpacer}px` }" aria-hidden="true"></div>
    </div>

    <section
      v-else-if="images.length"
      ref="gridShell"
      :class="['photo-grid', { 'photo-grid--wall': wallMode }]"
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
            <button type="button" class="btn-link photo-card__media" :title="image.name" @click="activateImage(row.startIndex + offset)">
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
            <!-- 缩略图墙不叠任何按钮和文字：收藏、删除在查看器里，批量操作靠勾选。 -->
            <div v-if="!wallMode" class="photo-card__overlay">
              <button
                type="button"
                :class="['btn-icon', 'btn--secondary', 'btn--sm', 'photo-card__action', { 'photo-card__action--active': image.is_favorite }]"
                :title="image.is_favorite ? '取消收藏' : '收藏'"
                :aria-label="`${image.is_favorite ? '取消收藏' : '收藏'} ${image.name}`"
                :aria-pressed="image.is_favorite ? 'true' : 'false'"
                @click.stop="toggleFavorite(image)"
              ><AppIcon name="star" :size="16" /></button>
              <button
                type="button"
                class="btn-icon btn--secondary btn--sm photo-card__action photo-card__action--danger"
                :title="deleteVerb"
                :aria-label="`${deleteVerb} ${image.name}`"
                data-test="photo-card-delete"
                @click.stop="requestDelete(image)"
              ><AppIcon name="trash" :size="16" /></button>
            </div>
            <div v-if="!wallMode" class="photo-card__meta">
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

    <div v-if="!folderModeRoot && loading && !loadedOnce" class="photo-skeleton-grid" aria-hidden="true" data-test="photo-grid-skeleton">
      <SkeletonBlock v-for="index in 12" :key="index" shape="card" :lines="1" />
    </div>

    <!-- 空态（D-UD-06）：每种都给下一步。 -->
    <div v-if="showEmptyState" class="photo-empty" data-test="photo-empty">
      <EmptyState
        v-if="searchMode === 'semantic' && !filters.keyword.trim()"
        icon="search"
        data-test="photo-semantic-prompt"
        title="输入内容开始语义搜索"
        message="语义搜索按图片的文件名与标签匹配，例如“海边”。只在图片库内进行，不会返回视频。"
      />
      <EmptyState
        v-else-if="searchMode === 'semantic' && semanticCoverage && Number(semanticCoverage.indexed) === 0"
        icon="search"
        data-test="photo-semantic-no-index"
        title="没有语义命中"
        message="图片语义索引还没有建立。请先在设置页运行“图片语义索引”任务。"
      >
        <template #actions>
          <button type="button" class="btn btn--secondary btn--sm" @click="$emit('open-settings')">去设置</button>
        </template>
      </EmptyState>
      <EmptyState
        v-else-if="searchMode === 'semantic'"
        icon="search"
        title="没有语义命中"
        message="换个说法再试，或给更多图片打上标签后重跑图片语义索引。"
      >
        <template #actions>
          <button type="button" class="btn btn--secondary btn--sm" @click="setSearchMode('name')">改用文件名搜索</button>
        </template>
      </EmptyState>
      <EmptyState
        v-else-if="folderModeRoot ? imageDirectories.length === 0 : (!folderModeActive && imageDirectories.length === 0)"
        icon="folder"
        title="还没有配置图片扫描目录"
        message="先在设置页添加图片目录，再回来扫描入库。"
      >
        <template #actions>
          <button type="button" class="btn btn--primary btn--sm" data-test="photo-empty-settings" @click="$emit('open-settings')">去设置添加图片目录</button>
          <button type="button" class="btn btn--secondary btn--sm" :disabled="scanning" @click="scanNow">{{ scanning ? '扫描中…' : '立即扫描' }}</button>
        </template>
      </EmptyState>
      <EmptyState
        v-else-if="folderModeActive"
        icon="image"
        title="文件夹中没有符合条件的图片"
        message="调整筛选条件，或返回文件夹列表查看其他图集。"
      >
        <template #actions>
          <button type="button" class="btn btn--secondary btn--sm" @click="leaveFolder">返回文件夹列表</button>
        </template>
      </EmptyState>
      <EmptyState
        v-else-if="hasActiveFilters"
        icon="filter"
        :title="folderModeRoot ? '没有符合筛选条件的文件夹' : '没有符合筛选条件的图片'"
        message="调整关键词、标签或评分区间后重试。"
      />
      <EmptyState
        v-else
        icon="image"
        :title="folderModeRoot ? '还没有可展示的文件夹' : '图片库为空'"
        message="目录已配置，点击扫描把磁盘上的图片同步进来。"
      >
        <template #actions>
          <button type="button" class="btn btn--primary btn--sm" :disabled="scanning" @click="scanNow">{{ scanning ? '扫描中…' : '立即扫描' }}</button>
        </template>
      </EmptyState>
    </div>

    <button
      v-if="hasMore"
      type="button"
      class="btn btn--secondary photo-library__more"
      :disabled="loading"
      data-test="photo-load-more"
      @click="loadMore()"
    >
      {{ loading ? '加载中…' : '加载更多图片' }}
    </button>

    <!-- 选择操作条（D-UD-10）：有勾选才浮在内容区底部，放在列表之后、吸底靠 position: sticky。图片搜索没有结果总数，
         「全选当前结果（N）」只选已显示的 N 张、不往下加载；保留原有钩子 photo-select-all / photo-clear-selection。 -->
    <SelectionBar
      class="photo-selection-bar"
      :count="selectedImageIDs.length"
      :total-matching="images.length"
      :all-matching-selected="allLoadedSelected"
      select-all-data-test="photo-select-all"
      clear-data-test="photo-clear-selection"
      data-test="photo-selection-tools"
      @select-all-matching="selectAllDisplayed"
      @clear="clearSelection"
    >
      <template #actions>
        <button type="button" class="btn btn--secondary btn--sm" :disabled="batchBusy" data-test="photo-batch-tag-open" @click="openBatchTagPicker">批量添加标签</button>
        <div class="photo-tag-combobox photo-batch-tag-search">
          <input
            ref="batchTagInput"
            :disabled="batchBusy"
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
            <div
              v-for="(tag, index) in filteredBatchTags"
              :id="`photo-batch-tag-option-${tag.id}`"
              :key="tag.id"
              role="option"
              :aria-selected="index === batchTagActiveIndex"
              :class="['photo-tag-option', { active: index === batchTagActiveIndex }]"
              @mouseenter="batchTagActiveIndex = index"
              @mousedown.prevent="batchAddTag(tag)"
            >{{ tag.name }}</div>
            <span v-if="filteredBatchTags.length === 0" class="photo-tag-options__empty">没有匹配标签</span>
          </div>
        </div>
        <button type="button" class="btn btn--secondary btn--sm" :disabled="batchBusy" data-test="photo-batch-metadata-edit" @click="openMetadataBatchEdit">批量编辑…</button>
        <button type="button" class="btn btn--danger btn--sm" :disabled="batchBusy" data-test="photo-batch-delete" @click="requestBatchDelete">{{ deleteActionLabel }}</button>
      </template>
    </SelectionBar>

    <!-- 全屏查看器（D-UD-13）：关闭固定在右上角，← → 切换，Esc 退出，F 收藏。 -->
    <div v-if="viewerImage" class="photo-viewer" role="dialog" aria-modal="true" :aria-label="`查看 ${viewerImage.name}`" data-test="photo-viewer">
      <button type="button" class="btn-icon btn--secondary photo-viewer__close" aria-label="关闭" title="关闭 (Esc)" data-test="photo-viewer-close" @click="closeViewer"><AppIcon name="close" :size="20" /></button>
      <button type="button" class="btn-icon photo-viewer__nav photo-viewer__nav--prev" aria-label="上一张" :disabled="viewerIndex <= 0" title="上一张 (←)" @click="viewerPrev"><AppIcon name="chevron-left" :size="24" /></button>
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
      <button type="button" class="btn-icon photo-viewer__nav photo-viewer__nav--next" aria-label="下一张" :disabled="viewerIndex >= images.length - 1" title="下一张 (→)" @click="viewerNext"><AppIcon name="chevron-right" :size="24" /></button>

      <aside class="photo-viewer__sidebar glass-surface">
        <h3 :title="viewerImage.name">{{ viewerImage.name }}</h3>
        <button type="button" class="btn btn--secondary btn--sm photo-viewer__reveal" data-test="photo-viewer-directory" @click="revealViewerImage">在访达中显示</button>
        <WallpaperButton kind="image" :media-id="Number(viewerImage.id)" />
        <p v-if="viewerDirectoryError" class="photo-viewer__error" role="alert">{{ viewerDirectoryError }}</p>
        <dl class="photo-viewer__facts">
          <dt>路径</dt><dd :title="viewerImage.path">{{ viewerImage.path }}</dd>
          <dt>尺寸</dt><dd>{{ viewerImage.width && viewerImage.height ? `${viewerImage.width}×${viewerImage.height}` : '未探测' }}</dd>
          <dt>大小</dt><dd>{{ formatBytes(viewerImage.size) }}</dd>
          <dt>格式</dt><dd>{{ formatBadge(viewerImage) }}</dd>
          <dt>查看</dt><dd data-test="photo-viewer-view-count">{{ Number(viewerImage.view_count || 0) }} 次</dd>
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
              :class="['btn', 'btn--secondary', 'btn--sm', 'photo-viewer__favorite', { 'photo-viewer__favorite--on': viewerImage.is_favorite }]"
              :title="viewerImage.is_favorite ? '取消收藏 (F)' : '收藏 (F)'"
              :aria-pressed="viewerImage.is_favorite ? 'true' : 'false'"
              data-test="photo-viewer-favorite"
              @click="toggleFavorite(viewerImage)"
            ><AppIcon name="star" :size="16" />{{ viewerImage.is_favorite ? '已收藏' : '收藏' }}</button>
          </div>
          <div class="photo-viewer__rating">
            <input
              v-model="ratingDraft"
              type="number"
              min="0"
              max="10"
              step="0.5"
              class="number-input"
              aria-label="评分"
              placeholder="0–10，步进 0.5"
              data-test="photo-rating-input"
              @change="applyRating"
            />
            <button type="button" class="btn btn--ghost btn--sm" data-test="photo-rating-clear" @click="clearRating">清空</button>
          </div>
        </div>

        <div class="photo-viewer__block">
          <h4>标签</h4>
          <div class="photo-viewer__tags">
            <span v-for="tag in viewerImage.tags || []" :key="tag.id" class="tag-badge" :style="{ '--tag-color': tag.color }">
              {{ tag.name }}
              <button type="button" class="btn-icon tag-remove" :aria-label="`移除标签 ${tag.name}`" @click="removeTag(viewerImage, tag)"><AppIcon name="close" :size="16" /></button>
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
              <div
                v-for="(tag, index) in filteredAddableTags"
                :id="`photo-tag-option-${tag.id}`"
                :key="tag.id"
                role="option"
                :aria-selected="index === tagActiveIndex"
                :class="['photo-tag-option', { active: index === tagActiveIndex }]"
                @mouseenter="tagActiveIndex = index"
                @mousedown.prevent="addTag(viewerImage, tag)"
              >{{ tag.name }}</div>
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
                class="btn-icon tag-remove"
                :aria-label="`移除人物 ${item.person.display_name}`"
                :disabled="viewerPersonBusy"
                :data-test="`photo-person-remove-${item.person.id}`"
                @click="removeViewerPerson(item)"
              ><AppIcon name="close" :size="16" /></button>
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
              <div
                v-for="(item, index) in filteredViewerPersonCandidates"
                :id="`photo-person-option-${item.person.id}`"
                :key="item.person.id"
                role="option"
                :aria-selected="index === viewerPersonActiveIndex"
                :class="['photo-tag-option', { active: index === viewerPersonActiveIndex }]"
                @mouseenter="viewerPersonActiveIndex = index"
                @mousedown.prevent="addViewerPerson(item)"
              >{{ item.person.display_name }}</div>
              <span v-if="filteredViewerPersonCandidates.length === 0" class="photo-tag-options__empty">没有匹配人物</span>
            </div>
          </div>
        </div>

        <div class="photo-viewer__block">
          <div class="photo-viewer__block-heading">
            <h4>AI 标签候选</h4>
            <button
              type="button"
              class="btn btn--secondary btn--sm"
              :disabled="retagging"
              title="已有手工标签的图片也会重新分析"
              data-test="photo-ai-retag"
              @click="retagImage"
            >{{ retagging ? '分析中…' : '重新分析' }}</button>
          </div>
          <p v-if="viewerDetailError" class="photo-viewer__muted">{{ viewerDetailError }}</p>
          <ul v-else-if="viewerCandidates.length" class="photo-viewer__candidates" data-test="photo-ai-candidates">
            <li v-for="candidate in viewerCandidates" :key="candidate.id" class="photo-viewer__candidate">
              <span class="tag-chip" :style="{ '--tag-color': candidate.matched_tag?.color }">{{ candidate.suggested_name }}</span>
              <span class="photo-viewer__muted">{{ confidenceLabel(candidate.confidence) }}</span>
              <button
                type="button"
                class="btn btn--primary btn--sm"
                :disabled="candidateBusy"
                :data-test="`photo-ai-candidate-approve-${candidate.id}`"
                @click="approveViewerCandidate(candidate)"
              >接受</button>
              <button
                type="button"
                class="btn btn--secondary btn--sm"
                :disabled="candidateBusy"
                :data-test="`photo-ai-candidate-reject-${candidate.id}`"
                @click="rejectViewerCandidate(candidate)"
              >拒绝</button>
            </li>
          </ul>
          <p v-else class="photo-viewer__muted" data-test="photo-ai-candidates-empty">没有待审候选。</p>
          <p v-if="retagError" class="photo-viewer__error" role="alert" data-test="photo-ai-retag-error">{{ retagError }}</p>
        </div>

        <div class="photo-viewer__block">
          <button type="button" class="btn btn--danger" data-test="photo-viewer-delete" :disabled="deletingImageIDs.includes(viewerImage.id)" @click="requestDelete(viewerImage)"><AppIcon name="trash" :size="16" />{{ deleteActionLabel }}</button>
        </div>
      </aside>
    </div>

    <!-- 单张删除确认（设置里开了「删除前确认」时）：按勾选在两种去向之间选，按钮写明是哪一种（D-UD-08）。 -->
    <DialogShell
      v-if="deleteTarget"
      size="sm"
      :title="deleteFileChoice ? '移到废纸篓' : '从图片库移除'"
      :busy="deleting"
      stop-modal-clicks
      data-test="photo-delete-dialog"
      @close="deleteTarget = null"
    >
      <p class="photo-delete__message">
        将图片「{{ deleteTarget.name }}」<template v-if="deleteTarget.size > 0">（{{ formatBytes(deleteTarget.size) }}）</template>{{ deleteFileChoice ? '移到废纸篓' : '从图片库移除' }}。
      </p>
      <div class="photo-delete__options">
        <label>
          <input v-model="deleteFileChoice" type="checkbox" data-test="photo-delete-file" />
          同时把原文件移到废纸篓
        </label>
      </div>
      <p class="photo-viewer__muted" data-test="photo-delete-consequence">{{ deleteFileChoice ? deleteTrashHint : deleteRecordOnlyHint }}</p>
      <template #footer>
        <button type="button" class="btn btn--secondary" :disabled="deleting" @click="deleteTarget = null">取消</button>
        <button type="button" class="btn btn--danger" :disabled="deleting" data-test="photo-delete-confirm" @click="confirmDelete">{{ deleteFileChoice ? '移到废纸篓' : '从图片库移除' }}</button>
      </template>
    </DialogShell>

    <MetadataBatchEditDialog
      v-if="metadataBatchEdit"
      kind="image"
      :selected-ids="metadataBatchEdit.ids"
      :filter="metadataBatchEdit.filter"
      :tags="tags"
      @close="metadataBatchEdit = null"
      @applied="markImagesChanged"
      @undone="markImagesChanged"
    />

    <DialogShell
      v-if="batchDeletePending"
      size="sm"
      :title="batchDeleteFileChoice ? '移到废纸篓' : '从图片库移除'"
      :busy="batchBusy"
      stop-modal-clicks
      data-test="photo-batch-delete-dialog"
      @close="batchDeletePending = false"
    >
      <p class="photo-delete__message">将已选的 {{ selectedImageIDs.length }} 张图片{{ batchDeleteFileChoice ? '移到废纸篓' : '从图片库移除' }}。</p>
      <div class="photo-delete__options">
        <label>
          <input v-model="batchDeleteFileChoice" type="checkbox" data-test="photo-batch-delete-file" />
          同时把原文件移到废纸篓
        </label>
      </div>
      <p class="photo-viewer__muted" data-test="photo-batch-delete-consequence">{{ batchDeleteFileChoice ? deleteTrashHint : deleteRecordOnlyHint }}</p>
      <template #footer>
        <button type="button" class="btn btn--secondary" :disabled="batchBusy" @click="batchDeletePending = false">取消</button>
        <button type="button" class="btn btn--danger" :disabled="batchBusy" data-test="photo-batch-delete-confirm" @click="confirmBatchDelete">{{ batchDeleteFileChoice ? '移到废纸篓' : '从图片库移除' }}</button>
      </template>
    </DialogShell>

    <ImageAITagReviewPanel
      :visible="showAITagReview"
      @close="closeAITagReview"
      @changed="handleAITagApproved"
    />
  </main>
</template>

<script>
import {
  AddPersonImages, AddTagToImage, BatchAddTagToImages, GetAllImageDirectories, GetImageDetail, GetImageSemanticIndexStatus, GetImageTags,
  ListPeople, RemovePersonImage, OpenImageDirectory, RevealImage,
  ApproveImageAITagCandidate, GetImageAITaggingSummary, ListImageAITagCandidates, RejectImageAITagCandidate, RetryImageAITagging,
  DeleteImagesInDirectoryWithResult,
  ListImageFolderGroups, ListImageTimelineBuckets, RemoveTagFromImage, SearchImagePage,
  SearchImagesSemantic, SetImageFavorite, SetImageRating, SyncImageDirectories, RecordImageView
} from '../../wailsjs/go/main/App';
import AppBadge from './ui/AppBadge.vue';
import AppBanner from './ui/AppBanner.vue';
import AppIcon from './ui/AppIcon.vue';
import DialogShell from './ui/DialogShell.vue';
import EmptyState from './ui/EmptyState.vue';
import FilterChip from './ui/FilterChip.vue';
import PageHeader from './ui/PageHeader.vue';
import SegmentedControl from './ui/SegmentedControl.vue';
import SelectionBar, { selectAllMatchingLabel } from './ui/SelectionBar.vue';
import SkeletonBlock from './ui/SkeletonBlock.vue';
import WallpaperButton from './WallpaperButton.vue';
import BaseMenu from './ui/BaseMenu.vue';
import BasePopover from './ui/BasePopover.vue';
import TrashUndoBanner from './video-list/TrashUndoBanner.vue';
import { DELETE_RECORD_ONLY_HINT, DELETE_TRASH_HINT } from './DeleteConfirmDialog.vue';
import { summarizeTrashFailures } from './TrashCenterDialog.vue';
import ImageAITagReviewPanel from './ImageAITagReviewPanel.vue';
import MetadataBatchEditDialog from './MetadataBatchEditDialog.vue';
import { formatBytes } from '../utils/mediaDetails.js';
import { logFrontend } from '../utils/frontendLog.js';
import { photoCleanupStore, startPhotoCleanupPolling, stopPhotoCleanupPolling, refreshPhotoCleanupStatus } from '../utils/photoCleanupStore.js';
import { registerCommands, unregisterCommands } from '../utils/commandRegistry.js';
import { confirmAction, notify } from '../utils/feedback.js';
import {
  PHOTO_GROUP_MONTH, PHOTO_GROUP_NONE, PHOTO_ROW_HEADER,
  buildPhotoLayout, calculatePhotoAnchorScrollTop, calculatePhotoWindow,
  firstVisiblePhotoItemIndex, formatPhotoTimelineLabel, photoTimelineKey, resolvePhotoCellSize, resolvePhotoColumns
} from '../utils/photoGrid.js';
// 只读复用视频列表的滚动宿主解析（.main-view），不修改 virtualList.js。
import { resolveScrollOwnerDescriptor } from '../utils/virtualList.js';
import { IMAGE_MISSING_OPTIONS } from '../utils/metadataCompleteness.js';

const PAGE_SIZE = 60;
// 查看器里同一张图连续显示满这么久才记一次查看（观看回顾 D-RECAP-SOURCES）；切图、关闭或离开本页即取消。
export const VIEW_RECORD_DELAY_MS = 1500;
// 网格几何常量。卡片高度固定 = 方形缩略图 + 定高信息条，布局计算才能不依赖实测回写：
// 缩略图区高度由列宽算出（aspect-ratio 1 的等价物），信息条用 CSS 钉死成 CARD_META_HEIGHT。
const GRID_GAP = 12;
const MIN_COLUMN_WIDTH = 180;
const CARD_META_HEIGHT = 86;
// glass-surface 给卡片加了 1px 边框，卡片是 border-box：上下各 1px 要算进行高，
// 缩略图的正方形边长也要相应减去，否则内容比卡片内容盒高 2px 被裁掉。
const CARD_BORDER = 2;
const STREAM_CELL = { minColumnWidth: MIN_COLUMN_WIDTH, gap: GRID_GAP, meta: CARD_META_HEIGHT, border: CARD_BORDER };
// 缩略图墙（2026-10-12 用户裁决「不要颜色，只要一个紧凑的缩略图墙」）：同一份图片流结果，
// 格子只剩方形缩略图——更小的列宽、更窄的间隙，没有信息条和边框。
const WALL_CELL = { minColumnWidth: 120, gap: 4, meta: 0, border: 0 };
// 展示模式存在 localStorage「cineinsight-photo-display-mode」；读到未知值回到图片流。
export const PHOTO_DISPLAY_MODES = ['stream', 'wall', 'folders'];
export function normalizePhotoDisplayMode(value) {
  return PHOTO_DISPLAY_MODES.includes(value) ? value : 'stream';
}
const TIMELINE_HEADER_HEIGHT = 40;
const OVERSCAN_ROWS = 3;
// 文件夹卡片网格：最小列宽与 .photo-folder-grid 的 minmax(220px, 1fr) 一致；卡片等高（封面定比例 +
// 单行信息条），首帧按估计高度渲染前几行，挂载后实测一张卡片的高度再按行窗口化。
const FOLDER_MIN_COLUMN_WIDTH = 220;
const FOLDER_CARD_ESTIMATE = 260;
const FOLDER_INITIAL_ROWS = 6;
// 视口底边距列表底部小于该值时预取下一页；虚拟化后哨兵元素不再稳定出现在 DOM 里。
const LOAD_MORE_THRESHOLD = 400;

export default {
  name: 'PhotoLibraryPage',
  components: {
    AppBadge, AppBanner, AppIcon, BaseMenu, BasePopover, DialogShell, EmptyState, FilterChip, ImageAITagReviewPanel,
    MetadataBatchEditDialog, PageHeader, SegmentedControl, SelectionBar, SkeletonBlock, TrashUndoBanner,
    WallpaperButton
  },
  props: {
    settings: { type: Object, required: true },
    tags: { type: Array, default: () => [] },
    // 切到别的标签页时本页只是隐藏、不卸载：已加载的图片和滚动位置都要留着。
    pageActive: { type: Boolean, default: true }
  },
  // open-cleanup：「管理」菜单的「清理审阅」进清理中心的图片标签（D-P3-02，原来是本页整页接管的清理审阅页）。
  emits: ['open-settings', 'open-cleanup'],
  data() {
    return {
      viewerDirectoryError: '',
      deletingImageIDs: [],
      photoMenu: null,
      photoMenuAnchor: null,
      images: [],
      nextCursor: null,
      exhausted: false,
      loading: false,
      loadedOnce: false,
      error: '',
      searchMode: 'name',
      displayMode: normalizePhotoDisplayMode(window.localStorage?.getItem?.('cineinsight-photo-display-mode')),
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
        aiTagState: '',
        // 「缺失某项」（D-MC-03），取值见 IMAGE_MISSING_OPTIONS；'' 不筛。
        missing: ''
      },
      missingOptions: IMAGE_MISSING_OPTIONS,
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
      // 元数据批量编辑（D-MC-04）：打开时记下所选与当前筛选；null 表示没开。
      metadataBatchEdit: null,
      deleteTrashHint: DELETE_TRASH_HINT,
      deleteRecordOnlyHint: DELETE_RECORD_ONLY_HINT,
      showAITagReview: false,
      aiTagPending: 0,
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
      windowState: { startRow: 0, endRow: 0, topSpacer: 0, bottomSpacer: 0, totalHeight: 0 },
      folderColumns: 1,
      folderCardHeight: 0,
      folderWindow: { startRow: 0, endRow: 0, topSpacer: 0, bottomSpacer: 0, totalHeight: 0 }
    };
  },
  computed: {
    // 与视频库同构：徽标只数收进浮层的那几项低频筛选。
    activePhotoFilterCount() {
      let count = 0;
      if (this.filters.aiTagState) count += 1;
      if (this.filters.missing) count += 1;
      if (this.filters.personIDs.length > 0) count += 1;
      if (this.timelineMode) count += 1;
      if (this.filters.favoriteOnly) count += 1;
      if (this.filters.minRating !== '' || this.filters.maxRating !== '') count += 1;
      if (this.filters.takenAfter || this.filters.takenBefore) count += 1;
      return count;
    },
    // 删除按钮上写明是哪一种（D-UD-08）：按设置「删除原文件」决定移到废纸篓还是只从图片库移除；
    // 开着「删除前确认」时会先弹确认框（框里还能改），按钮带「…」。
    deleteVerb() { return this.settings?.delete_original_file ? '移到废纸篓' : '从图片库移除'; },
    deleteActionLabel() { return this.settings?.confirm_before_delete ? `${this.deleteVerb}…` : this.deleteVerb; },
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
        ...(this.metadataBatchFilterUsable ? [{ heading: '整理' }, { id: 'batch-edit-filter', label: '批量编辑当前筛选…' }] : []),
        { heading: '维护' },
        { id: 'cleanup', label: cleanup },
        { id: 'ai-tags', label: this.aiTagPending > 0 ? `AI 标签审阅（待审 ${this.aiTagPending}）` : 'AI 标签审阅' },
        { id: 'trash', label: '回收站' }
      ];
    },
    // 列表是不是「当前筛选」的结果：语义搜索不是；文件夹总览列的是图集不是图片。这两种情况批量编辑不给筛选范围。
    metadataBatchFilterUsable() { return this.searchMode !== 'semantic' && !this.folderModeRoot; },
    hasMore() { return !this.folderModeRoot && !this.exhausted && this.loadedOnce; },
    folderModeRoot() { return this.displayMode === 'folders' && !this.activeFolder; },
    folderModeActive() { return this.displayMode === 'folders' && Boolean(this.activeFolder); },
    // 缩略图墙只是图片流换了格子；文件夹内的图片仍用带信息条的卡片。
    wallMode() { return this.displayMode === 'wall'; },
    cellGeometry() { return this.wallMode ? WALL_CELL : STREAM_CELL; },
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
    // 文件夹视图按行窗口化：几千个文件夹各带 4 张封面时整页渲染会让滚动和切页明显卡顿。
    folderWindowed() { return this.virtualized && this.folderModeRoot; },
    folderLayout() {
      return buildPhotoLayout({
        items: this.folderGroups,
        columns: this.folderColumns,
        cellHeight: this.folderCardHeight || FOLDER_CARD_ESTIMATE,
        gap: GRID_GAP
      });
    },
    renderedFolders() {
      if (!this.folderWindowed) return this.folderGroups;
      const rows = this.folderLayout.rows;
      if (this.folderWindow.endRow <= this.folderWindow.startRow) {
        return this.folderGroups.slice(0, this.folderColumns * FOLDER_INITIAL_ROWS);
      }
      const first = rows[this.folderWindow.startRow];
      const last = rows[this.folderWindow.endRow - 1];
      return first && last ? this.folderGroups.slice(first.startIndex, last.endIndex) : [];
    },
    // 网格是否真的在视野里：切走标签页时不是。
    gridActive() { return this.pageActive; },
    layout() {
      return buildPhotoLayout({
        items: this.images,
        columns: this.columns,
        groupBy: this.timelineActive ? PHOTO_GROUP_MONTH : PHOTO_GROUP_NONE,
        cellHeight: this.mediaHeight + this.cellGeometry.meta + this.cellGeometry.border,
        headerHeight: TIMELINE_HEADER_HEIGHT,
        gap: this.cellGeometry.gap
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
        '--photo-grid-gap': `${this.cellGeometry.gap}px`,
        '--photo-card-meta': `${this.cellGeometry.meta}px`
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
    // 「全选当前结果（N）」（D-UD-10 实施修订）：图片搜索没有结果总数，当前结果就是已显示的图片，N 是它们的张数，不往下加载。
    selectAllDisplayedLabel() {
      return selectAllMatchingLabel(this.images.length);
    },
    searchModeOptions() {
      return [
        { value: 'name', label: '文件名', dataTest: 'photo-mode-name' },
        {
          value: 'semantic',
          label: '语义',
          disabled: !this.semanticAvailable,
          title: this.semanticAvailable ? '语义搜索：按文件名与标签匹配' : this.semanticNotice,
          dataTest: 'photo-mode-semantic'
        }
      ];
    },
    displayModeOptions() {
      const semantic = this.searchMode === 'semantic';
      return [
        { value: 'stream', label: '图片流', dataTest: 'photo-display-stream' },
        { value: 'wall', label: '缩略图墙', title: '同一份图片流，只显示缩略图的紧凑平铺；点图查看、勾选照旧', dataTest: 'photo-display-wall' },
        {
          value: 'folders',
          label: '按文件夹',
          disabled: semantic,
          title: semantic ? '语义搜索结果按相关度展示，暂不按文件夹汇总' : '每个直属文件夹作为一个独立图集，不递归包含子文件夹',
          dataTest: 'photo-display-folders'
        }
      ];
    },
    hasActiveFilters() {
      return Boolean(this.filters.keyword.trim()) || this.filters.tagIDs.length > 0
        || this.filters.personIDs.length > 0 || this.filters.favoriteOnly
        || this.filters.minRating !== '' || this.filters.maxRating !== ''
        || this.filters.takenAfter !== '' || this.filters.takenBefore !== ''
        || this.filters.aiTagState !== '' || this.filters.missing !== '';
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
    'viewerImage.id'(id) {
      this.viewerDirectoryError = '';
      this.scheduleViewRecord(id);
    },
    'filters.keyword'() {
      clearTimeout(this._keywordTimer);
      this._keywordTimer = setTimeout(() => this.reload(), 300);
    },
    'filters.favoriteOnly'() { this.reload(); },
    'filters.sortMode'() { this.reload(); },
    'filters.aiTagState'() { this.reload(); },
    'filters.missing'() { this.reload(); },
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
      // 离开本页时查看器虽然还开着，但图已经不在眼前：取消未到点的查看计时，回来再重新计。
      this.scheduleViewRecord(active ? this.viewerImage?.id : null);
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
    }, {
      // 待处理工作台按固定 ID 打开图片 AI 标签审阅（详细设计 §6.1）。切到图片页由调用方先做。
      id: 'photos.openAIReview',
      group: 'action',
      label: '打开图片 AI 标签审阅',
      keywords: ['ai', 'tag', 'review', '图片', '审阅'],
      run: () => this.openAITagReviewFromCommand()
    }]);
    this.$nextTick(() => {
      this.attachResizeObserver();
      this.syncWindow(true);
    });
  },
  beforeUnmount() {
    clearTimeout(this._viewRecordTimer);
    unregisterCommands('photo-library');
    clearTimeout(this._keywordTimer);
    window.removeEventListener('keydown', this.handleKeydown);
    this.detachScrollOwner();
    this.detachResizeObserver();
    stopPhotoCleanupPolling();
  },
  methods: {
    async openSystemFolder(directory) {
      try { await OpenImageDirectory(directory); }
      catch (err) { this.error = `在访达中显示失败：${err}`; }
    },
    async revealViewerImage() {
      const id = this.viewerImage?.id; if (!id) return;
      this.viewerDirectoryError = '';
      try { await RevealImage(Number(id)); }
      catch (err) { if (this.viewerImage?.id === id) this.viewerDirectoryError = `在访达中显示失败：${err}`; }
    },
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
        case 'cleanup': this.$emit('open-cleanup'); break;
        case 'ai-tags': this.openAITagReview(); break;
        case 'trash': this.openTrashDialog(); break;
        case 'batch-edit-filter': this.openMetadataBatchEditForFilter(); break;
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
    // 删除文件夹 = 按设置里的「删除原文件」处理这个目录直属的全部图片：移到废纸篓，或只从图片库移除
    // （文件留在原处）。两种都进回收站、可撤销；磁盘上的目录本身不动，子目录也不受影响。
    // 删完分组自然消失，不用再回来看到一个空文件夹。
    async deleteFolder(folder) {
      if (!folder?.directory || this.folderDeleting) return;
      const deleteFile = Boolean(this.settings?.delete_original_file);
      // 按文件夹整批处理、事先没有逐张看过，所以先确认（D-UD-07 第 2 级：写清数量与影响）。
      const confirmed = await confirmAction(deleteFile
        ? {
          title: '把文件夹里的图片移到废纸篓',
          message: `将「${folder.name}」下的 ${folder.count} 张图片移到废纸篓，可在回收站恢复。\n磁盘上的文件夹本身和子文件夹里的图片都不会动。`,
          impact: '在访达中清空废纸篓之后才会释放空间。',
          confirmText: '移到废纸篓',
          level: 'confirm',
          danger: true
        }
        : {
          title: '把文件夹里的图片从图片库移除',
          message: `将「${folder.name}」下的 ${folder.count} 张图片从图片库移除，文件保留在磁盘上。\n以后扫描到同一批文件不会再收录，可在回收站「允许重新收录」。子文件夹里的图片不受影响。`,
          impact: '只删记录，磁盘上的文件不动。',
          confirmText: '从图片库移除',
          level: 'confirm',
          danger: true
        });
      if (!confirmed) return;
      this.folderDeleting = true;
      this.error = '';
      try {
        const banner = this.$refs.trashUndo;
        const outcome = await banner.runDelete({
          deleteFile,
          total: Number(folder.count || 0),
          names: this.imageNames(),
          invoke: requestID => DeleteImagesInDirectoryWithResult(folder.directory, deleteFile, requestID)
        });
        banner.showDeleteNotice(outcome, { reportFailures: false });
        if (outcome.failures.length) {
          this.error = `「${folder.name}」有 ${outcome.failures.length} 张图片未删除：${summarizeTrashFailures(outcome.failures)}`;
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
      const { columns, mediaHeight } = resolvePhotoCellSize(width, this.cellGeometry);
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
      if (this.folderModeRoot) this.$nextTick(() => this.syncFolderWindow());
      const anchorIndex = this.currentAnchorIndex();
      if (!this.measureGrid()) {
        this.syncWindow();
        return;
      }
      this.restoreAnchorAfterRelayout(anchorIndex);
    },
    // 当前渲染窗口（含 overscan 行）里第一张照片的下标；必须在格子几何变化之前取，之后的布局已经不是这一份。
    currentAnchorIndex() {
      return this.virtualized ? firstVisiblePhotoItemIndex(this.layout, this.windowState.startRow) : -1;
    },
    // 布局已按新几何重算（列宽变化，或图片流 ↔ 缩略图墙）：把 anchorIndex 那张照片所在的行放回视口顶。
    restoreAnchorAfterRelayout(anchorIndex) {
      const willRestoreAnchor = anchorIndex >= 0 && Boolean(this.scrollOwnerEl);
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
      if (this.folderModeRoot) this.syncFolderWindow();
      this.syncWindow();
    },
    getFolderListTop() {
      const shell = this.$refs.folderShell;
      if (!this.scrollOwnerEl || !shell) return 0;
      const ownerRect = this.scrollOwnerEl.getBoundingClientRect();
      const shellRect = shell.getBoundingClientRect();
      return this.scrollOwnerEl.scrollTop + (shellRect.top - ownerRect.top);
    },
    // syncFolderWindow 按容器宽度定列数、实测一张卡片定行高，再按滚动位置算出要渲染的行。
    // 实测高度与上次不同（首帧用的是估计值）时下一帧再算一次，让占位高度对齐真实布局。
    syncFolderWindow() {
      if (!this.gridActive || !this.folderModeRoot || !this.folderGroups.length) return;
      this.resolveScrollOwner();
      const shell = this.$refs.folderShell;
      if (!this.folderWindowed || !shell) return;
      const width = shell.getBoundingClientRect().width || 0;
      if (width <= 0) return;
      const columns = resolvePhotoColumns(width, { minColumnWidth: FOLDER_MIN_COLUMN_WIDTH, gap: GRID_GAP });
      const card = shell.querySelector('.photo-folder-card');
      const cardHeight = Math.round(card?.getBoundingClientRect().height || 0);
      const remeasured = columns !== this.folderColumns || (cardHeight > 0 && cardHeight !== this.folderCardHeight);
      this.folderColumns = columns;
      if (cardHeight > 0) this.folderCardHeight = cardHeight;
      const next = calculatePhotoWindow({
        layout: this.folderLayout,
        scrollTop: this.scrollOwnerEl.scrollTop,
        viewportHeight: this.scrollOwnerEl.clientHeight,
        listTop: this.getFolderListTop(),
        overscan: OVERSCAN_ROWS
      });
      const current = this.folderWindow;
      if (next.startRow !== current.startRow || next.endRow !== current.endRow
        || next.topSpacer !== current.topSpacer || next.bottomSpacer !== current.bottomSpacer) {
        this.folderWindow = next;
      }
      if (remeasured) this.$nextTick(() => this.syncFolderWindow());
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
      if (!PHOTO_DISPLAY_MODES.includes(mode)) return;
      if (mode === 'folders' && this.searchMode === 'semantic') return;
      if (mode === this.displayMode && !this.activeFolder) return;
      // 图片流与缩略图墙是同一份结果的两种格子：不重新查询、不回顶，只换几何，视口顶部那张照片留在原处。
      if (mode !== 'folders' && this.displayMode !== 'folders') {
        const anchorIndex = this.currentAnchorIndex();
        this.displayMode = mode;
        window.localStorage?.setItem?.('cineinsight-photo-display-mode', mode);
        this.measureGrid();
        this.restoreAnchorAfterRelayout(anchorIndex);
        return;
      }
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
        ai_tag_state: this.filters.aiTagState,
        missing: this.filters.missing
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
    handleTagPersonConverted(result) {
      this.filters.tagIDs = this.filters.tagIDs.filter(id => Number(id) !== Number(result.tag_id));
      this.loadImageTags();
      this.reload();
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
        // 新的一组文件夹从第一行开始窗口化；等首帧卡片渲染出来再实测行高。
        this.folderWindow = { startRow: 0, endRow: 0, topSpacer: 0, bottomSpacer: 0, totalHeight: 0 };
        this.$nextTick(() => this.syncFolderWindow());
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
    // 命令 photos.openAIReview（待处理工作台）：打开图片 AI 标签审阅。
    openAITagReviewFromCommand() {
      this.openAITagReview();
    },
    openTrashDialog(tab = 'image') {
      this.$refs.trashUndo?.openTrashDialog(tab);
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
    // 清理中心的图片标签删除或撤销删除了图片（App 的 handleCleanupImagesChanged）：按当前条件重载。
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
    selectAllDisplayed() {
      this.selectedImageIDs = this.images.map(image => Number(image.id));
    },
    // 文件夹里再点一次「按文件夹」：回到文件夹列表（分段控件点当前项不发 update，这里接冒上来的 click）。
    reselectFolders(event) {
      if (this.activeFolder && event?.target?.closest?.('[data-test="photo-display-folders"]')) this.setDisplayMode('folders');
    },
    clearSelection() {
      this.selectedImageIDs = [];
      this.batchTagKeyword = '';
      this.batchTagMenuOpen = false;
      this.batchTagActiveIndex = 0;
    },
    openBatchTagPicker() { this.openBatchTagMenu(); this.$nextTick(() => this.$refs.batchTagInput?.focus()); },
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
    // 「批量编辑…」：所选图片，或当前筛选的全部结果。语义搜索列出的不是筛选结果，这时只提供「所选」。
    openMetadataBatchEdit() {
      if (!this.selectedImageIDs.length) return;
      const filter = this.metadataBatchFilterUsable ? this.buildFilter() : null;
      this.metadataBatchEdit = { ids: [...this.selectedImageIDs], filter };
    },
    // 管理菜单「批量编辑当前筛选…」：不需要先选中，范围就是当前筛选的全部结果。
    openMetadataBatchEditForFilter() {
      if (!this.metadataBatchFilterUsable) return;
      this.metadataBatchEdit = { ids: [], filter: this.buildFilter() };
    },
    // 图片元数据被批量改过（这里的批量编辑与撤销，或数据维护 · 完整度里的撤销）：亮出「图片库有更新」
    // 提示条，由用户决定何时重载（自动重载会冲掉滚动位置）；标签筛选栏随之重取。
    markImagesChanged() {
      this.libraryChanged = true;
      this.loadImageTags();
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
      const ids = [...this.selectedImageIDs].map(Number);
      if (!ids.length || this.batchBusy) return;
      this.batchBusy = true;
      try {
        const before = new Map(this.images.map(image => [Number(image.id), image]));
        const banner = this.$refs.trashUndo;
        const outcome = await banner.runDelete({ ids, deleteFile, names: this.imageNames(ids) });
        const removed = new Set(outcome.removedIDs);
        removed.forEach(id => this.adjustTimelineBucket(before.get(id), -1));
        this.images = this.images.filter(image => !removed.has(Number(image.id)));
        this.selectedImageIDs = ids.filter(id => !removed.has(id));
        banner.showDeleteNotice(outcome, { reportFailures: false });
        if (outcome.failures.length) {
          this.error = `批量删除部分失败：${outcome.failures.length} 张图片未删除。${summarizeTrashFailures(outcome.failures)}`;
        }
        // 单张删除早就这么做了，批量删除漏了：不作废分组的话，图片删光了文件夹
        // 还留在列表上，点进去是空的。
        this.folderGroups = [];
        this.folderLoadedOnce = false;
        if (this.folderModeActive && this.activeFolder) {
          this.activeFolder = { ...this.activeFolder, count: Math.max(0, Number(this.activeFolder.count || 0) - removed.size) };
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
    activateImage(index) {
      const image = this.images[index];
      if (!image) return;
      if (this.selectedImageIDs.length) {
        this.toggleImageSelection(image.id, !this.selectedImageIDs.includes(Number(image.id)));
        return;
      }
      this.openViewer(index);
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
    // 从别的页面（观看回顾）跳来定位一张图：已在当前结果里就直接打开查看器；否则回到照片流、
    // 清掉其余筛选、按文件名搜这一张，载入后打开。找不到（已删除、被隐藏）返回 false 并提示。
    async locateImage(target) {
      const id = Number(target?.id || 0);
      if (!id) return false;
      const loaded = this.images.findIndex(item => Number(item.id) === id);
      if (loaded >= 0 && !this.folderModeRoot) {
        this.openViewer(loaded);
        return true;
      }
      this.searchMode = 'name';
      // 缩略图墙本身就是图片流，留在原布局；只有文件夹视图要切回来。
      if (this.displayMode === 'folders') {
        this.displayMode = 'stream';
        this.activeFolder = null;
      }
      this.personFilterSelections = [];
      this.filters = { ...this.filters, keyword: String(target?.name || ''), tagIDs: [], personIDs: [], favoriteOnly: false,
        minRating: '', maxRating: '', takenAfter: '', takenBefore: '', aiTagState: '', missing: '' };
      // 条件侦听器会各自排一次重载（关键词那条带 300ms 防抖）：等它们排上后撤掉防抖，再以一次新查询为准。
      await this.$nextTick();
      clearTimeout(this._keywordTimer);
      await this.reload();
      const found = this.images.findIndex(item => Number(item.id) === id);
      if (found < 0) {
        notify('这张图片已不在图片库中，可能已删除或被隐藏。');
        return false;
      }
      this.openViewer(found);
      return true;
    },
    // 完整度页签的「去处理」（D-MC-03）：回到照片流、清掉其余筛选，只按「缺少这一项」列出。
    async applyMissingFilter(field) {
      const value = String(field || '');
      if (!IMAGE_MISSING_OPTIONS.some(option => option.value === value)) return false;
      this.searchMode = 'name';
      if (this.displayMode === 'folders') {
        this.displayMode = 'stream';
        this.activeFolder = null;
      }
      this.personFilterSelections = [];
      this.filters = { ...this.filters, keyword: '', tagIDs: [], personIDs: [], favoriteOnly: false,
        minRating: '', maxRating: '', takenAfter: '', takenBefore: '', aiTagState: '', missing: value };
      // 条件侦听器会各自排一次重载（关键词那条带 300ms 防抖）：等它们排上后撤掉防抖，再以一次新查询为准。
      await this.$nextTick();
      clearTimeout(this._keywordTimer);
      await this.reload();
      return true;
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
    // 同一张图连续显示满 VIEW_RECORD_DELAY_MS 才上报一次；切到别的图、关闭查看器或离开本页都会取消。
    // 原图加载失败时不算看过。后端另按 30 分钟去重，这里只负责「真的停留过」。
    scheduleViewRecord(id) {
      clearTimeout(this._viewRecordTimer);
      this._viewRecordTimer = null;
      const imageID = Number(id || 0);
      if (!imageID || !this.pageActive) return;
      this._viewRecordTimer = setTimeout(() => {
        this._viewRecordTimer = null;
        if (Number(this.viewerImage?.id) !== imageID || this.viewerImageError || !this.pageActive) return;
        this.recordImageView(imageID);
      }, VIEW_RECORD_DELAY_MS);
    },
    async recordImageView(imageID) {
      try {
        const result = await RecordImageView(imageID);
        if (result?.recorded) this.patchImage({ id: imageID, view_count: result.view_count, last_viewed_at: result.last_viewed_at });
      } catch (err) {
        logFrontend('PhotoLibraryPage', 'RecordImageView failed', { imageID, err: String(err) }, true);
      }
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
        const candidates = await RetryImageAITagging(image.id);
        if (Number(this.viewerImage?.id) !== Number(image.id)) return;
        this.viewerCandidates = candidates || [];
        this.refreshAITagSummary();
      } catch (err) {
        if (Number(this.viewerImage?.id) !== Number(image.id)) return;
        const message = String(err?.message || err);
        this.retagError = message.includes('AI 配置不可用')
          ? `重新分析失败：${message}。请先在设置页配置 AI 接口的 BaseURL 与模型。`
          : `重新分析失败：${message}`;
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
        // 手工标签不再让候选作废（D-PC28 规则 2）；只有这个标签已经在图片上时候选才会是 superseded。
        if (item && item.status === 'superseded') {
          this.retagError = '这个标签已经在图片上（已手动添加），没有重复写入。';
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
        if (await this.performDelete(this.deleteTarget, this.deleteFileChoice)) this.deleteTarget = null;
      } finally {
        this.deleting = false;
      }
    },
    async performDelete(image, deleteFile) {
      if (this.deletingImageIDs.includes(image.id)) return false;
      this.deletingImageIDs.push(image.id);
      try {
        const banner = this.$refs.trashUndo;
        const outcome = await banner.runDelete({ ids: [image.id], deleteFile, names: { [Number(image.id)]: image.name } });
        banner.showDeleteNotice(outcome, { reportFailures: false });
        if (!outcome.removedIDs.includes(Number(image.id))) {
          // 没删掉：失败按原因报告（确认框留着，可以重试）；磁盘不支持废纸篓而用户选了「暂不处理」时
          // 图片原样留着，不算错误，确认框随之关闭。
          if (outcome.failures.length) {
            this.error = `删除图片失败：${summarizeTrashFailures(outcome.failures)}`;
            if (this.viewerImage?.id === image.id) this.viewerDetailError = this.error;
            return false;
          }
          return true;
        }
        // If this is the last loaded picture, fetch the next page before choosing
        // the replacement so an unloaded next picture wins over the previous one.
        if (this.viewerImage?.id === image.id && this.viewerIndex === this.images.length - 1 && this.hasMore) await this.loadMore();
        const index = this.images.findIndex(item => Number(item.id) === Number(image.id));
        if (index >= 0) {
          this.images.splice(index, 1);
          if (this.viewerIndex >= 0) {
            if (index === this.viewerIndex) {
              if (this.images.length) this.openViewer(Math.min(index, this.images.length - 1));
              else this.closeViewer();
            }
            else if (index < this.viewerIndex) this.viewerIndex -= 1;
          }
        }
        this.selectedImageIDs = this.selectedImageIDs.filter(id => Number(id) !== Number(image.id));
        // 分组头显示的是后端整组总数，删掉一张后要同步减一，否则计数长期偏大。
        this.adjustTimelineBucket(image, -1);
        if (this.folderModeActive && this.activeFolder?.directory === image.directory) {
          this.activeFolder = { ...this.activeFolder, count: Math.max(0, Number(this.activeFolder.count || 0) - 1) };
          this.folderGroups = [];
          this.folderLoadedOnce = false;
        }
        // 后端已把清理分析标记为过期，空闲时不轮询，得主动同步一次。
        refreshPhotoCleanupStatus();
        return true;
      } catch (err) {
        this.error = `删除图片失败：${err}`;
        if (this.viewerImage?.id === image.id) this.viewerDetailError = this.error;
        return false;
      } finally {
        this.deletingImageIDs = this.deletingImageIDs.filter(id => id !== image.id);
      }
    },
    imageNames(ids = null) {
      const wanted = ids ? new Set(ids.map(Number)) : null;
      const names = {};
      for (const image of this.images) {
        const id = Number(image.id);
        if (!wanted || wanted.has(id)) names[id] = image.name;
      }
      return names;
    },
    // 回收站恢复（撤销条整批撤销、回收站对话框恢复、扫描隐藏重新检查找回）之后：
    // 恢复的可能是一批、也可能分散在多个文件夹，直接按当前视图重新加载；分组与计数一并重拉。
    async afterTrashRestore(imageIDs) {
      const restored = new Set((imageIDs || []).map(Number));
      // 恢复回来的图片不该再在清理审阅里显示为"已删除"。
      const review = photoCleanupStore.review;
      if (restored.size && review.deletedIDs.some(id => restored.has(Number(id)))) {
        review.deletedIDs = review.deletedIDs.filter(id => !restored.has(Number(id)));
      }
      // 文件夹分组与当前文件夹的计数由 reload 重拉。
      this.folderGroups = [];
      this.folderLoadedOnce = false;
      await this.reload();
      refreshPhotoCleanupStatus();
    }
  }
};
</script>

<style scoped>
.photo-library { padding: var(--space-4) var(--space-5) var(--space-6); display: flex; flex-direction: column; gap: var(--space-3); }
.photo-library > .photo-library__header { margin-bottom: 0; }
/* 与视频库同构地吸顶：勾选若干张之后往下滚，批量按钮必须一直在手边，否则要滚回顶部才能操作。 */
.photo-toolbar { position: sticky; top: 0; z-index: var(--z-sticky); padding: 10px var(--space-4); border: 1px solid var(--hairline); border-radius: var(--radius); background: var(--panel-bg); display: flex; flex-direction: column; gap: var(--space-2); }
.photo-toolbar__controls { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.photo-toolbar__menu { gap: 6px; padding: 0 var(--space-3); font-weight: var(--fw-regular); }
.photo-toolbar__menu--on { border-color: var(--accent-border); background: var(--accent-soft); color: var(--accent-text); font-weight: var(--fw-semibold); }
.photo-toolbar__caret { color: var(--text-muted); }
.photo-toolbar__menu--on .photo-toolbar__caret { color: var(--accent-text); }
/* 底部选择操作条（SelectionBar）：页面是 flex 纵列、行距已由 gap 给出，不再要它自带的上边距。
   批量加标签的候选列表在吸底的条上，向上展开，免得落到窗口外。 */
.photo-library > .photo-selection-bar { margin-top: 0; }
.photo-selection-bar .photo-tag-options { top: auto; bottom: calc(100% + var(--space-1)); }
.photo-batch-tag-search { width: 210px; }
.photo-tag-combobox { position: relative; min-width: 0; }
.photo-tag-combobox > .search-input { width: 100%; }
/* 候选列表叠在所在区块里（工具栏或查看器侧栏）的局部层上。 */
.photo-tag-options { position: absolute; top: calc(100% + var(--space-1)); right: 0; left: 0; z-index: 3; max-height: 220px; overflow-y: auto; padding: var(--space-1); border: 1px solid var(--border-color); border-radius: var(--radius-sm); background: var(--panel-bg); box-shadow: var(--shadow-popover); }
.photo-tag-option { display: block; width: 100%; padding: 7px var(--space-2); border-radius: calc(var(--radius-sm) - 2px); color: var(--text-primary); font-size: var(--fs-secondary); text-align: left; cursor: pointer; }
.photo-tag-option:hover,
.photo-tag-option.active { background: var(--accent-soft); color: var(--accent-text); }
.photo-tag-options__empty { display: block; padding: var(--space-2); color: var(--text-muted); font-size: var(--fs-caption); }
.photo-toolbar__keyword { max-width: 240px; }
.photo-toolbar__sort { width: auto; min-width: 120px; }
.photo-toolbar__ai { width: auto; min-width: 140px; }
.photo-toolbar__favorite { display: inline-flex; align-items: center; gap: 6px; color: var(--text-primary); font-size: var(--fs-secondary); white-space: nowrap; cursor: pointer; }
.photo-toolbar__rating { display: inline-flex; align-items: center; gap: 6px; color: var(--text-muted); font-size: var(--fs-caption); }
.photo-toolbar__rating .number-input { width: 76px; }
.photo-toolbar__taken { display: inline-flex; align-items: center; gap: 6px; color: var(--text-muted); font-size: var(--fs-caption); }
.photo-toolbar__timeline { display: inline-flex; align-items: center; gap: 6px; color: var(--text-primary); font-size: var(--fs-secondary); white-space: nowrap; cursor: pointer; }
.photo-toolbar__timeline input:disabled { cursor: not-allowed; }
.photo-toolbar__date { width: 148px; }
.photo-toolbar__date:disabled { opacity: 0.45; cursor: not-allowed; }
.photo-toolbar__tags { display: flex; flex-wrap: wrap; gap: 6px; }
.photo-toolbar__people { display: grid; gap: 6px; }
.photo-toolbar__people > span:first-child { color: var(--text-muted); font-size: var(--fs-caption); }
.photo-person-chips { display: flex; flex-wrap: wrap; gap: 6px; }
.photo-toolbar__semantic-notice { display: flex; align-items: center; gap: 6px; margin: 0; color: var(--text-muted); font-size: var(--fs-caption); }
.photo-folder-loading { margin: 0; color: var(--text-muted); font-size: var(--fs-caption); }

.photo-folder-breadcrumb { display: flex; align-items: center; gap: 10px; padding: var(--space-2) var(--space-3); border-radius: var(--radius); }
.photo-folder-breadcrumb__text { display: grid; flex: 1; min-width: 0; gap: 2px; }
.photo-folder-breadcrumb__text strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-primary); font-size: var(--fs-secondary); }
.photo-folder-breadcrumb__text span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-muted); font-size: var(--fs-caption); }
.photo-folder-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: var(--space-3); }
.photo-folder-card { position: relative; min-width: 0; overflow: hidden; border-radius: var(--radius-lg); }
/* 删除按钮压在封面右上角，且在"打开文件夹"那个大按钮之外，点它不会误进文件夹。 */
.photo-folder-card__delete { position: absolute; z-index: 2; top: var(--space-2); right: var(--space-2); opacity: 0; transition: opacity var(--transition); }
.photo-folder-card:hover .photo-folder-card__delete,
.photo-folder-card__delete:focus-visible { opacity: 1; }
/* 整张卡片（封面 + 文字）是一个按钮：用无边框的文字链接按钮，排版按卡片改。 */
.photo-folder-card__open { display: block; width: 100%; color: inherit; font-size: inherit; font-weight: var(--fw-regular); text-align: left; }
.photo-folder-card__open:hover:not(:disabled) { text-decoration: none; }
.photo-folder-card__open:focus-visible { outline: 2px solid var(--accent-color); outline-offset: -2px; }
.photo-folder-card__covers { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); grid-template-rows: repeat(2, minmax(0, 1fr)); aspect-ratio: 1.55; gap: 2px; overflow: hidden; background: var(--thumb-bg); }
.photo-folder-card__covers img { width: 100%; height: 100%; min-width: 0; min-height: 0; display: block; object-fit: cover; }
.photo-folder-card__covers[data-cover-count="1"] img { grid-column: 1 / -1; grid-row: 1 / -1; }
.photo-folder-card__covers[data-cover-count="3"] img:last-of-type { grid-column: 1 / -1; }
.photo-folder-card__cover-fallback { display: flex; align-items: center; justify-content: center; min-width: 0; min-height: 0; color: var(--text-muted); font-size: var(--fs-caption); }
.photo-folder-card__meta { display: grid; gap: 3px; padding: 10px var(--space-3) var(--space-1); }
.photo-folder-card__meta strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-primary); font-size: var(--fs-secondary); }
.photo-folder-card__meta small { color: var(--text-secondary); font-size: var(--fs-caption); }
.photo-folder-card__meta span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-muted); font-size: var(--fs-caption); }
.photo-folder-card__actions { display: flex; padding: 0 var(--space-2) var(--space-2); }

/* 虚拟化网格：外层只负责堆叠"行"，列数由 --photo-columns 显式给出（而不是 auto-fill），
   保证 DOM 布局与 photoGrid.js 的窗口计算用的是同一个列数。 */
.photo-grid { display: block; }
.photo-grid-row { display: grid; grid-template-columns: repeat(var(--photo-columns, 1), minmax(0, 1fr)); gap: var(--photo-grid-gap, 12px); box-sizing: border-box; }
.photo-skeleton-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr)); gap: var(--space-3); }
.photo-timeline-header { display: flex; align-items: flex-end; overflow: hidden; box-sizing: border-box; }
.photo-timeline-header h3 { margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-secondary); font-size: var(--fs-secondary); font-weight: var(--fw-semibold); letter-spacing: 0.2px; }
.photo-card { position: relative; overflow: hidden; border: 1px solid var(--hairline); border-radius: var(--radius); background: var(--panel-bg); }
.photo-card:hover { border-color: var(--border-strong); }
/* 勾选框只在悬停或已选时显形：常驻会在密集缩略图上形成一片噪点。
   键盘聚焦时也要显形，否则用键盘的人根本看不到它。 */
.photo-card__select { position: absolute; z-index: 3; top: 6px; left: 6px; display: grid; place-items: center; width: 26px; height: 26px; border-radius: var(--radius-sm); background: var(--overlay-strong); cursor: pointer; opacity: 0; transition: opacity var(--transition); }
.photo-card:hover .photo-card__select,
.photo-card__select:focus-within,
.photo-card--selected .photo-card__select { opacity: 1; }
.photo-card__select input { width: 16px; height: 16px; margin: 0; accent-color: var(--accent-color); }
/* 定高卡片：缩略图区高度由列宽算出（等价于 aspect-ratio 1），信息条固定高度，
   两者相加即 photoGrid 的 cellHeight，布局无需实测回写。缩略图整块是按钮：用无边框的文字链接按钮，尺寸按格子改。 */
.photo-card__media { display: block; width: 100%; height: var(--photo-cell-media, auto); aspect-ratio: 1; background: var(--thumb-bg); color: inherit; cursor: pointer; }
.photo-card__media:hover:not(:disabled) { text-decoration: none; }
.photo-card__media img { width: 100%; height: 100%; display: block; object-fit: cover; }
.photo-card__fallback { width: 100%; height: 100%; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: var(--space-2); padding: var(--space-3); color: var(--text-muted); }
.photo-card__fallback strong { max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-primary); font-size: var(--fs-caption); }
.photo-format-badge { display: inline-block; padding: 2px var(--space-2); border: 1px solid var(--border-color); border-radius: var(--radius-pill); background: var(--control-bg); color: var(--text-secondary); font-size: var(--fs-caption); font-style: normal; font-weight: var(--fw-bold); letter-spacing: 0.4px; }
.photo-card__overlay { position: absolute; top: var(--space-2); right: var(--space-2); display: flex; gap: 6px; opacity: 0; transition: opacity var(--transition); }
.photo-card:hover .photo-card__overlay,
.photo-card:focus-within .photo-card__overlay { opacity: 1; }
.photo-card__action { border-radius: var(--radius-pill); }
.photo-card__action--active,
.photo-card__action--active:hover:not(:disabled) { color: var(--warning-color); }
.photo-card__action--active .ui-icon,
.photo-viewer__favorite--on .ui-icon { fill: currentColor; }
.photo-card:has(.photo-card__action--active) .photo-card__overlay { opacity: 1; }
.photo-card__action--danger:hover:not(:disabled) { border-color: var(--danger-border); color: var(--danger-text); }
.photo-card__meta { display: grid; align-content: start; gap: 2px; height: var(--photo-card-meta, 86px); box-sizing: border-box; overflow: hidden; padding: 9px 11px 10px; }
.photo-card__meta span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--fs-caption); color: var(--text-primary); }
.photo-card__meta small { color: var(--text-muted); font-size: var(--fs-caption); }
/* 缩略图墙：格子只剩方形缩略图，无边框（WALL_CELL.border 为 0，缩略图边长不让出边框）；
   勾选框沿用上面「悬停或已选才显形」的规则。失败占位只留格式徽标，文件名在悬停提示里。 */
.photo-grid--wall .photo-card { border: 0; border-radius: var(--radius-sm); }
.photo-grid--wall .photo-card__media:focus-visible { outline: 2px solid var(--accent-color); outline-offset: -2px; }
.photo-grid--wall .photo-card__fallback { padding: 6px; }
.photo-grid--wall .photo-card__fallback strong { display: none; }
.photo-card__tags {
  margin: 2px 0 0;
  color: var(--text-secondary);
  font-size: var(--fs-caption);
  line-height: 1.4;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.photo-library__more { align-self: center; }

/* 全屏查看器：压暗层上的控件两主题都用白字（--recap-hero-*，同观看回顾的大图层）。
   grid-template-rows 必须显式给一行确定高度：隐式行是 auto，img 的 max-height:100%
   会因为父级高度不确定而失效，竖图就按原始尺寸撑出视口显示不全。 */
.photo-viewer { position: fixed; inset: 0; z-index: var(--z-modal); display: grid; grid-template-columns: minmax(0, 1fr) min(360px, 38vw); grid-template-rows: minmax(0, 1fr); background: var(--overlay-strong); }
.photo-viewer__stage { position: relative; display: flex; align-items: center; justify-content: center; min-width: 0; min-height: 0; overflow: hidden; padding: var(--space-7) 56px; }
.photo-viewer__img { max-width: 100%; max-height: 100%; object-fit: contain; border-radius: var(--radius-sm); }
.photo-viewer__fallback { max-width: 420px; display: grid; gap: 10px; justify-items: center; padding: var(--space-5); border: 1px dashed var(--recap-hero-chip-border); border-radius: var(--radius); color: var(--recap-hero-muted); text-align: center; }
.photo-viewer__fallback strong { color: var(--recap-hero-fg); word-break: break-all; }
.photo-viewer__fallback p { margin: 0; font-size: var(--fs-secondary); }
.photo-viewer__fallback small { font-size: var(--fs-caption); word-break: break-all; }
/* 关闭固定在整个查看器的右上角（宽屏时压在侧栏右上角，窄屏时在图片区右上角），所以带底色与描边，两种底上都看得见。 */
.photo-viewer__close { position: absolute; top: var(--space-3); right: var(--space-3); z-index: 3; border-radius: var(--radius-pill); }
.photo-viewer__nav { position: absolute; top: 50%; z-index: 2; width: 40px; height: 40px; border-radius: var(--radius-pill); background: var(--recap-hero-chip); color: var(--recap-hero-fg); transform: translateY(-50%); }
.photo-viewer__nav:hover:not(:disabled) { background: var(--recap-hero-chip-border); color: var(--recap-hero-fg); transform: translateY(-50%); }
.photo-viewer__nav:disabled { opacity: 0.35; cursor: default; }
.photo-viewer__nav--prev { left: 14px; }
.photo-viewer__nav--next { right: calc(min(360px, 38vw) + 14px); }
.photo-viewer__sidebar { display: flex; flex-direction: column; gap: 14px; min-width: 0; padding: 18px var(--space-4); border-radius: 0; overflow-y: auto; }
/* 侧栏内容比视口高时按内容滚动，不许把标题、按钮压扁。 */
.photo-viewer__sidebar > * { flex-shrink: 0; }
/* 右上角留给关闭按钮。 */
.photo-viewer__sidebar h3 { margin: 0; padding-right: 44px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--fs-section); font-weight: var(--fw-semibold); line-height: 34px; }
.photo-viewer__reveal { align-self: flex-start; }
.photo-viewer__facts { display: grid; grid-template-columns: max-content 1fr; gap: 6px 10px; margin: 0; font-size: var(--fs-caption); }
.photo-viewer__facts dt { color: var(--text-muted); }
.photo-viewer__facts dd { margin: 0; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-primary); }
.photo-viewer__block { display: grid; gap: var(--space-2); padding-top: var(--space-3); border-top: 1px solid var(--border-color); }
.photo-viewer__block h4 { margin: 0; font-size: var(--fs-secondary); font-weight: var(--fw-semibold); color: var(--text-secondary); }
.photo-viewer__block-heading { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.photo-viewer__favorite--on { border-color: var(--warning-border); background: var(--warning-soft); color: var(--warning-text); }
.photo-viewer__rating { display: flex; align-items: center; gap: var(--space-2); }
.photo-viewer__rating .number-input { width: 160px; }
.photo-viewer__tags { display: flex; flex-wrap: wrap; gap: 6px; }
.photo-viewer__tag-add { width: 100%; }
.photo-viewer__muted { margin: 0; color: var(--text-muted); font-size: var(--fs-caption); }
.photo-viewer__error { margin: 0; color: var(--danger-text); font-size: var(--fs-caption); }
.photo-viewer__candidates { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.photo-viewer__candidate { display: flex; align-items: center; gap: var(--space-2); }
/* 标签与人物徽标里的 ×：只有图标、贴着文字，不用按钮的方形尺寸。 */
.tag-badge .tag-remove { width: 16px; height: 16px; border-radius: var(--radius-pill); }
.tag-badge .tag-remove .ui-icon { width: 12px; height: 12px; }
.photo-delete__message { margin: 0; }
.photo-delete__options { display: grid; gap: var(--space-2); margin-top: 14px; }
.photo-delete__options label { display: inline-flex; align-items: center; gap: 7px; font-size: var(--fs-secondary); }
.photo-delete__options + .photo-viewer__muted { margin-top: var(--space-2); }

@media (max-width: 900px) {
  .photo-viewer { grid-template-columns: 1fr; grid-template-rows: minmax(0, 1fr) auto; }
  .photo-viewer__nav--next { right: 14px; }
  .photo-viewer__sidebar { max-height: 42vh; }
}
</style>
