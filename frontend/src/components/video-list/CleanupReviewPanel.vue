<template>
  <!-- 清理中心的视频标签（D-P3-02）：原来是片库页里的弹窗，三期起是清理中心页的内容，结构与图片标签一致——
       顶部分类统计卡切换类别、按目录分组、每组「设为保留」「不是重复」、有勾选才浮出的选择操作条「移到废纸篓」。
       删除宿主（进度与取消、不支持废纸篓时的二选一、撤销提示）在清理中心页上，经 trashVideos / afterTrashVideos 交回。 -->
  <section class="cleanup-review" data-test="cleanup-review-panel">
    <!-- 集中整理之后的独立审阅：只看这一次涉及的组，「返回全库分析」回到整库的结果。 -->
    <AppBanner
      v-if="consolidationReviewTaskID"
      tone="accent"
      title="本次集中整理结果"
      message="这里只列这次集中整理涉及的组；删除前会再核对一次文件、分组与保留保护。"
      data-test="cleanup-consolidation-review"
    >
      <template #actions>
        <button type="button" class="btn btn--secondary btn--sm" :disabled="cleanupCloseLocked" data-test="cleanup-global" @click="open()">返回全库分析</button>
      </template>
    </AppBanner>

    <!-- 结果在分析之后变过（库里增删了视频、或本次审阅已移走一些）：照样可以接着审阅，也可以重新分析。 -->
    <AppBanner
      v-if="cleanupResultStale && !cleanupDialog.loading"
      tone="warning"
      data-test="cleanup-outdated-hint"
      title="视频库在本次分析之后变过，结果可能已过期"
      message="可以继续审阅，也可以点「重新分析」刷新候选。"
    >
      <template #actions>
        <button type="button" class="btn btn--secondary btn--sm" :disabled="cleanupDialog.loading || cleanupDialog.processing" @click="reanalyzeCleanupCandidates">重新分析</button>
      </template>
    </AppBanner>

    <!-- 分类统计卡（与图片标签一致，D-P3-02）：一次看一类，点卡片切换；卡片上是组数、涉及的视频数与已勾数，
         还没算完的类别再跟一段覆盖度。「已忽略」不依赖本轮结果，分析被取消或失败时也能进来撤销。 -->
    <div
      v-if="!cleanupDialog.loading && statusLoaded"
      :class="['cleanup-tiles', { 'cleanup-tiles--single': !cleanupDialog.analysis }]"
      role="tablist"
      aria-label="按类别查看"
      data-test="cleanup-tiles"
    >
      <template v-if="cleanupDialog.analysis">
        <StatTile
          v-for="option in cleanupCategoryOptions"
          :key="option.key"
          tag="button"
          role="tab"
          :selected="activeCleanupCategory === option.key"
          :aria-selected="activeCleanupCategory === option.key ? 'true' : 'false'"
          :icon="option.icon"
          :value="option.count"
          :unit="option.unit"
          :label="option.label"
          :note="option.note"
          :hint="option.tip"
          :title="option.hint"
          data-test="cleanup-category"
          :data-category="option.key"
          @click="selectCleanupCategory(option.key)"
        >
          <span v-if="option.coverage" class="cleanup-tile__coverage" data-test="cleanup-coverage-chip">{{ option.coverage }}</span>
        </StatTile>
      </template>
      <StatTile
        tag="button"
        role="tab"
        :selected="activeCleanupCategory === 'dismissed'"
        :aria-selected="activeCleanupCategory === 'dismissed' ? 'true' : 'false'"
        icon="undo"
        :value="dismissedTileValue"
        label="已忽略"
        note="可以撤销"
        hint="「不是重复」「移出本组」「忽略」的记录"
        title="「不是重复」「移出本组」「忽略」留下的记录，可以撤销"
        data-test="cleanup-dismissed-tab"
        @click="selectCleanupCategory('dismissed')"
      />
    </div>

    <!-- 分析是本页正在做的那件事：页内只放这一条进度（D-UD-11）。离开这一页分析照样在后台跑。 -->
    <div v-if="cleanupDialog.loading" class="cleanup-state cleanup-loading" role="status" data-test="cleanup-analyzing">
      <p class="cleanup-state__title">正在分析视频库…</p>
      <ProgressBar class="cleanup-progress-bar" :value="cleanupProgressPercent === null ? null : cleanupProgressPercent / 100" />
      <p class="cleanup-progress-meta">
        当前阶段：{{ cleanupStageLabel }}
        <span v-if="cleanupElapsedText"> · 已运行 {{ cleanupElapsedText }}</span>
      </p>
      <p v-if="cleanupProgressPercent !== null" class="cleanup-progress-meta">
        已处理 {{ cleanupDialog.progress.current }} / {{ cleanupDialog.progress.total }}
        <span> ({{ cleanupProgressPercent }}%)</span>
      </p>
      <p v-if="cleanupDialog.progress.message" class="cleanup-progress-hint">{{ cleanupDialog.progress.message }}</p>
      <p v-if="cleanupDialog.progress.path" class="cleanup-progress-path">当前文件：{{ cleanupDialog.progress.path }}</p>
      <p class="cleanup-progress-hint">该分析会逐个读取视频文件；外置硬盘、休眠磁盘或大库场景下耗时较长，长时间停留不代表已假死。可以离开这一页，分析会在后台继续。</p>
      <p class="cleanup-progress-actions">
        <button
          type="button"
          class="btn btn--secondary btn--sm"
          data-test="cleanup-cancel-analysis"
          :disabled="cleanupDialog.cancelling"
          @click="cancelCleanupAnalysis"
        >{{ cleanupDialog.cancelling ? '正在取消…' : '取消分析' }}</button>
      </p>
    </div>

    <!-- 「已忽略」（D-PC31）：按种类分页列出记录，逐条撤销。 -->
    <section v-else-if="activeCleanupCategory === 'dismissed'" class="cleanup-dismissed" data-test="cleanup-dismissed">
      <SegmentedControl
        class="cleanup-dismissed__kinds"
        label="忽略记录的种类"
        :options="dismissalKindSegments"
        :model-value="dismissals.kind"
        data-test="cleanup-dismissed-kinds"
        @update:model-value="switchDismissalKind"
      />
      <p class="cleanup-dismissed__hint">{{ dismissalHint }}</p>
      <AppBanner v-if="dismissals.error" tone="danger" role="alert" data-test="cleanup-dismissed-error" :title="dismissals.error">
        <template #actions>
          <button
            type="button"
            class="btn btn--secondary btn--sm"
            data-test="cleanup-dismissed-retry"
            :disabled="dismissals.loading"
            @click="loadCleanupDismissals(dismissals.items.length === 0)"
          >重试</button>
        </template>
      </AppBanner>
      <div v-if="dismissals.loading && dismissals.items.length === 0" class="cleanup-dismissed__loading" role="status">
        <SkeletonBlock :lines="3" />
        <span class="cleanup-muted">正在读取忽略记录…</span>
      </div>
      <EmptyState
        v-else-if="dismissals.items.length === 0 && dismissals.loaded"
        data-test="cleanup-dismissed-empty"
        icon="check"
        title="这一类还没有忽略记录。"
        message="在候选里点「不是重复」「移出本组」或「忽略」，记录会出现在这里，可以随时撤销。"
      >
        <template #actions>
          <button type="button" class="btn btn--secondary btn--sm" data-test="cleanup-dismissed-back" @click="selectCleanupCategory('')">回到候选</button>
        </template>
      </EmptyState>
      <ul v-else-if="dismissals.items.length > 0" class="cleanup-dismissed__list">
        <li v-for="item in dismissals.items" :key="item.id" class="cleanup-dismissed__item" data-test="cleanup-dismissal-item">
          <span class="cleanup-dismissed__media">{{ describeDismissal(item) }}</span>
          <span class="cleanup-dismissed__time">{{ formatDismissalTime(item.created_at) }}</span>
          <button
            type="button"
            class="btn btn--secondary btn--sm"
            data-test="cleanup-dismissal-undo"
            :disabled="dismissals.undoing"
            @click="undoCleanupDismissal(item)"
          >撤销</button>
        </li>
      </ul>
      <button
        v-if="dismissals.hasMore"
        type="button"
        class="btn btn--secondary btn--sm cleanup-dismissed__more"
        data-test="cleanup-dismissed-more"
        :disabled="dismissals.loading"
        @click="loadCleanupDismissals(false)"
      >{{ dismissals.loading ? '读取中…' : '加载更多' }}</button>
    </section>

    <AppBanner v-else-if="cleanupDialog.error" class="cleanup-error-banner" tone="danger" role="alert" title="清理分析没有完成" :message="cleanupDialog.error" data-test="cleanup-analysis-error">
      <template #actions>
        <button type="button" class="btn btn--secondary btn--sm" data-test="cleanup-retry" :disabled="cleanupDialog.processing" @click="reanalyzeCleanupCandidates">重新分析</button>
      </template>
    </AppBanner>

    <EmptyState
      v-else-if="cleanupDialog.cancelled && !cleanupDialog.analysis"
      data-test="cleanup-cancelled"
      icon="info"
      title="上一轮分析已取消，没有产生结果。"
      message="要看清理候选，重新分析一次。"
    >
      <template #actions>
        <button type="button" class="btn btn--secondary btn--sm" :disabled="cleanupDialog.processing" @click="reanalyzeCleanupCandidates">重新分析</button>
      </template>
    </EmptyState>

    <div v-else-if="cleanupDialog.analysis" class="cleanup-body">
      <AppBanner
        v-if="cleanupDialog.analysis.stale_hash_count"
        data-test="cleanup-stale-hash-hint"
        :title="`有 ${cleanupDialog.analysis.stale_hash_count} 个视频还没有可用的感知哈希（未回填或源文件已变更），暂未参与近似重复检测。`"
      >
        <template #actions>
          <button type="button" class="btn btn--secondary btn--sm" :disabled="perceptualHashRunning" @click="$emit('start-perceptual-hash')">
            {{ perceptualHashRunning ? '补全中…' : '补全感知哈希' }}
          </button>
        </template>
      </AppBanner>
      <AppBanner v-if="cleanupSkippedText" data-test="cleanup-skipped-hint" :title="cleanupSkippedText" />
      <AppBanner
        v-if="cleanupDialog.analysis.skipped_clip_verification"
        data-test="cleanup-clip-verification-skipped"
        :title="`有 ${cleanupDialog.analysis.skipped_clip_verification} 组截取片段配对因画面复核超时、读取失败或文件变化而跳过，其余分析已完成。可稍后重新分析以重试。`"
      />
      <AppBanner
        v-if="cleanupDialog.analysis.stale_frame_hash_count"
        data-test="cleanup-stale-frame-hash-hint"
        :title="`有 ${cleanupDialog.analysis.stale_frame_hash_count} 个视频还没有帧哈希（或源文件已变更），暂未参与截取片段识别。`"
      >
        <template #actions>
          <button type="button" class="btn btn--secondary btn--sm" data-test="cleanup-start-frame-hash" :disabled="frameHashRunning" @click="$emit('start-frame-hash')">
            {{ frameHashRunning ? '补全中…' : '补全帧哈希' }}
          </button>
        </template>
      </AppBanner>
      <AppBanner v-if="cleanupFocusNotice" tone="warning" data-test="cleanup-focus-missing" :title="cleanupFocusNotice" />

      <!-- 当前类别的标题行：这一类的条数与目录数；全部类别的候选数与可释放空间；「集中整理」。 -->
      <div v-if="cleanupDirectorySections.length > 0" class="cleanup-area__bar" data-test="cleanup-area-bar">
        <h3 class="cleanup-area__title">{{ activeCategoryLabel }}</h3>
        <span class="cleanup-area__stat" data-test="cleanup-area-stat">{{ activeCategoryStat }}</span>
        <span class="cleanup-area__stat">
          全部类别共 {{ cleanupCandidateCount }} 项候选
          <template v-if="cleanupReleasableText"> · 移到废纸篓后，在访达清空废纸篓即可释放约 <b>{{ cleanupReleasableText }}</b></template>
        </span>
        <span class="cleanup-area__spacer"></span>
        <button
          v-if="!consolidationReviewTaskID"
          type="button"
          class="btn btn--secondary btn--sm"
          data-test="cleanup-consolidate"
          :disabled="!consolidationScope.length || cleanupDialog.processing || cleanupDialog.loading || cleanupGroupRequests > 0"
          @click="openConsolidation()"
        >集中整理（{{ consolidationScope.length }} 组）</button>
      </div>

      <!-- 空态要分得清「没有」与「还没算」（D-PC50），并给出下一步。 -->
      <EmptyState
        v-if="cleanupEmptyState"
        data-test="cleanup-empty-state"
        icon="check"
        :title="cleanupEmptyState.title"
        :message="cleanupEmptyState.details.join(' ')"
      >
        <template #actions>
          <button
            v-if="cleanupEmptyState.action === 'perceptual-hash'"
            type="button"
            class="btn btn--secondary btn--sm"
            data-test="cleanup-empty-start-perceptual-hash"
            :disabled="perceptualHashRunning"
            @click="$emit('start-perceptual-hash')"
          >{{ perceptualHashRunning ? '补全中…' : '补全感知哈希' }}</button>
          <button
            v-else-if="cleanupEmptyState.action === 'frame-hash'"
            type="button"
            class="btn btn--secondary btn--sm"
            data-test="cleanup-empty-start-frame-hash"
            :disabled="frameHashRunning"
            @click="$emit('start-frame-hash')"
          >{{ frameHashRunning ? '补全中…' : '补全帧哈希' }}</button>
          <button
            v-else
            type="button"
            class="btn btn--secondary btn--sm"
            data-test="cleanup-empty-reanalyze"
            :disabled="cleanupDialog.processing"
            @click="reanalyzeCleanupCandidates"
          >重新分析</button>
        </template>
      </EmptyState>

      <!-- 左侧目录承担分组，右侧只放当前目录的候选流（原型 A5）。
           候选归到"建议保留项"所在目录，组员可以位于别的目录。 -->
      <div v-else class="cleanup-split">
        <nav class="cleanup-dirs" aria-label="按目录分组">
          <div class="cleanup-dirs__heading">按目录分组</div>
          <button
            v-for="section in cleanupVisibleDirectorySections"
            :key="section.directory"
            type="button"
            :class="['btn-link', 'cleanup-dirs__item', { active: section.directory === activeCleanupDirectory }]"
            :title="section.directory"
            data-test="cleanup-dir-section"
            @click="activeCleanupDirectory = section.directory"
          >
            <span class="cleanup-dirs__path">{{ section.directory }}</span>
            <span
              v-if="cleanupSelectedCountByDirectory.get(section.directory)"
              class="cleanup-dirs__selected"
              data-test="cleanup-dir-selected"
              :title="`这个目录的候选里已勾选 ${cleanupSelectedCountByDirectory.get(section.directory)} 个`"
            >已勾 {{ cleanupSelectedCountByDirectory.get(section.directory) }}</span>
            <span class="cleanup-dirs__count">{{ section.entries.length }}</span>
          </button>
          <nav v-if="cleanupFilteredSections.length > 80" class="cleanup-pager" aria-label="清理目录分页"><button type="button" class="btn btn--secondary btn--sm" :disabled="cleanupDirectoryPage === 0" @click="cleanupDirectoryPage--">上一页</button><span>{{ cleanupDirectoryPage + 1 }} / {{ Math.ceil(cleanupFilteredSections.length / 80) }}</span><button type="button" class="btn btn--secondary btn--sm" :disabled="(cleanupDirectoryPage + 1) * 80 >= cleanupFilteredSections.length" @click="cleanupDirectoryPage++">下一页</button></nav>
          <!-- 统一勾选规则（D-PC49）：与图片清理页同一套，规则本身在 utils/cleanupSelection.js。 -->
          <p class="cleanup-dirs__note">默认只勾选精确重复里保留项以外的副本；近似重复、同源、截取片段默认不勾选。保留项锁定，要先「设为保留」切换。勾选的视频移到废纸篓，可在回收站撤销。</p>
        </nav>

        <div class="cleanup-stream">
          <div
            v-for="section in activeCleanupSections"
            :key="section.directory"
            class="cleanup-section"
          >
            <div class="cleanup-section__head">
              <strong :title="section.directory">{{ section.directory }}</strong>
              <span>{{ section.entryCount }} 项候选 · {{ section.videoCount }} 个视频</span>
              <nav v-if="section.entryCount > 40" class="cleanup-pager" aria-label="清理分组分页"><button type="button" class="btn btn--secondary btn--sm" :disabled="cleanupGroupPage === 0" @click="cleanupGroupPage--">上一页</button><span>{{ cleanupGroupPage + 1 }} / {{ Math.ceil(section.entryCount / 40) }}</span><button type="button" class="btn btn--secondary btn--sm" :disabled="(cleanupGroupPage + 1) * 40 >= section.entryCount" @click="cleanupGroupPage++">下一页</button></nav>
            </div>

            <div
              v-for="entry in section.entries"
              :key="entry.key"
              :class="['cleanup-card', { 'cleanup-card--focused': isFocusedEntry(entry) }]"
              data-test="cleanup-group-card"
              :data-kind="entry.kind"
              :data-focused="isFocusedEntry(entry) ? 'true' : null"
            >
              <div class="cleanup-card-kind">{{ cleanupKindLabel(entry.kind) }}</div>
              <div class="cleanup-consolidation-scope">
                <label v-if="!consolidationReviewTaskID && consolidationEligible(entry)"><input type="checkbox" data-test="cleanup-consolidation-scope" :checked="consolidationScope.includes(entry.key)" :disabled="cleanupDialog.processing || cleanupSkippedGroups.includes(entry.key)" @change="toggleConsolidationScope(entry)" />纳入集中整理</label>
                <label><input type="checkbox" data-test="cleanup-skip-group" :checked="cleanupSkippedGroups.includes(entry.key)" :disabled="cleanupDialog.processing" @change="toggleSkippedGroup(entry)" />本组不删</label>
              </div>

              <!-- 疑似同源：保留项不给勾选框，只清理可替代版本。 -->
              <template v-if="entry.kind === 'same-source'">
                <div class="cleanup-select-row cleanup-select-row--original">
                  <CleanupThumbnail :video="entry.keeper" @preview="previewCleanupVideo" />
                  <strong>建议保留：</strong>
                  <div class="cleanup-item-text">
                    <span class="cleanup-item-main">{{ cleanupItemSummary(entry.keeper) }}</span>
                    <span v-if="entry.keeper?.path" class="cleanup-item-path" :title="entry.keeper.path">{{ entry.keeper.path }}</span>
                    <span v-if="curationFor(entry.keeper).length" class="cleanup-curation" data-test="cleanup-curation">
                      <span v-for="badge in curationFor(entry.keeper)" :key="badge.key" class="cleanup-curation__item" :title="badge.title">{{ badge.text }}</span>
                    </span>
                  </div>
                  <div class="cleanup-item-actions">
                    <button type="button" class="btn btn--secondary btn--sm" @click="previewCleanupVideo(entry.keeper)">预览保留项</button>
                  </div>
                </div>
                <p><strong>判断：</strong>{{ entry.group.reason }}<span v-if="entry.group.confidence"> · 置信度 {{ entry.group.confidence }}</span><span v-if="entry.group.confirmed" class="cleanup-confirmed-badge" data-test="cleanup-same-source-confirmed">已确认同源</span></p>
                <div class="cleanup-select-row">
                  <input
                    type="checkbox"
                    :checked="isCleanupSelected(entry.group.alternative?.id)"
                    :disabled="cleanupDialog.processing || isCleanupTrashed(entry.group.alternative) || isCleanupLocked(entry.group.alternative?.id)"
                    @change="toggleCleanupSelection(entry.group.alternative?.id)"
                  />
                  <CleanupThumbnail :video="entry.group.alternative" @preview="previewCleanupVideo" />
                  <span class="cleanup-item-text">
                    <span class="cleanup-item-main">可清理版本：{{ entry.group.alternative?.name }} · 预计释放 {{ formatFileSize(entry.group.estimated_savings) }}</span>
                    <span v-if="entry.group.alternative?.path" class="cleanup-item-path" :title="entry.group.alternative.path">{{ entry.group.alternative.path }}</span>
                    <span v-if="cleanupVideoDirectory(entry.group.alternative) !== section.directory" class="cleanup-item-otherdir" data-test="cleanup-member-otherdir">位于 {{ cleanupVideoDirectory(entry.group.alternative) }}</span>
                    <span v-if="curationFor(entry.group.alternative).length" class="cleanup-curation" data-test="cleanup-curation">
                      <span v-for="badge in curationFor(entry.group.alternative)" :key="badge.key" class="cleanup-curation__item" :title="badge.title">{{ badge.text }}</span>
                    </span>
                    <span v-if="isCleanupLocked(entry.group.alternative?.id)" class="cleanup-item-locked" data-test="cleanup-member-locked">另一组要保留它</span>
                  </span>
                  <span class="cleanup-item-actions">
                    <button type="button" class="btn btn--secondary btn--sm" @click="previewCleanupVideo(entry.group.alternative)">预览该版本</button>
                    <button type="button" class="btn btn--secondary btn--sm" data-test="cleanup-reject-same-source" :disabled="cleanupDialog.processing" @click="rejectCleanupSameSource(entry.group)">不是同源</button>
                  </span>
                </div>
              </template>

              <!-- 截取片段：并排看 A（完整片）与 B（截取），A 不给勾选框。
                   判断依据（对齐偏移与命中率）就摆在中间，不用点开别处去找。 -->
              <template v-else-if="entry.kind === 'clip'">
                <div class="cleanup-clip-pair" data-test="cleanup-clip-pair">
                  <div class="cleanup-clip-side">
                    <div class="cleanup-clip-side__head">A · 完整片（建议保留）</div>
                    <div class="cleanup-select-row cleanup-select-row--original">
                      <CleanupThumbnail :video="entry.group.full" @preview="previewCleanupVideo" />
                      <div class="cleanup-item-text">
                        <span class="cleanup-item-main">{{ cleanupItemSummary(entry.group.full) }}</span>
                        <span v-if="entry.group.full?.path" class="cleanup-item-path" :title="entry.group.full.path">{{ entry.group.full.path }}</span>
                        <span v-if="curationFor(entry.group.full).length" class="cleanup-curation" data-test="cleanup-curation">
                          <span v-for="badge in curationFor(entry.group.full)" :key="badge.key" class="cleanup-curation__item" :title="badge.title">{{ badge.text }}</span>
                        </span>
                      </div>
                    </div>
                    <div class="cleanup-item-actions">
                      <button type="button" class="btn btn--secondary btn--sm" @click="previewCleanupVideo(entry.group.full)">预览完整片</button>
                    </div>
                  </div>
                  <div class="cleanup-clip-side">
                    <div class="cleanup-clip-side__head">B · 截取片段（可清理）</div>
                    <div class="cleanup-select-row" :class="{ 'cleanup-select-row--trashed': isCleanupTrashed(entry.group.clip) }">
                      <input
                        type="checkbox"
                        data-test="cleanup-clip-select"
                        :checked="isCleanupSelected(entry.group.clip?.id)"
                        :disabled="cleanupDialog.processing || isCleanupTrashed(entry.group.clip) || isCleanupLocked(entry.group.clip?.id)"
                        @change="toggleCleanupSelection(entry.group.clip?.id)"
                      />
                      <CleanupThumbnail :video="entry.group.clip" @preview="previewCleanupVideo" />
                      <div class="cleanup-item-text">
                        <span class="cleanup-item-main">{{ cleanupItemSummary(entry.group.clip) }}</span>
                        <span v-if="entry.group.clip?.path" class="cleanup-item-path" :title="entry.group.clip.path">{{ entry.group.clip.path }}</span>
                        <span v-if="cleanupVideoDirectory(entry.group.clip) !== section.directory" class="cleanup-item-otherdir" data-test="cleanup-member-otherdir">位于 {{ cleanupVideoDirectory(entry.group.clip) }}</span>
                        <span v-if="curationFor(entry.group.clip).length" class="cleanup-curation" data-test="cleanup-curation">
                          <span v-for="badge in curationFor(entry.group.clip)" :key="badge.key" class="cleanup-curation__item" :title="badge.title">{{ badge.text }}</span>
                        </span>
                        <span v-if="isCleanupLocked(entry.group.clip?.id)" class="cleanup-item-locked" data-test="cleanup-member-locked">另一组要保留它</span>
                      </div>
                    </div>
                    <div class="cleanup-item-actions">
                      <button type="button" class="btn btn--secondary btn--sm" @click="previewCleanupVideo(entry.group.clip)">预览片段</button>
                      <button type="button" class="btn btn--secondary btn--sm" data-test="cleanup-dismiss-clip" :disabled="cleanupDialog.processing" @click="dismissClipCandidate(entry.group)">忽略</button>
                    </div>
                  </div>
                </div>
                <p data-test="cleanup-clip-evidence">
                  <strong>判断：</strong>B 对上的是 A 从 {{ formatClipOffset(entry.group.offset_seconds) }} 开始的这一段，逐帧命中率 {{ formatClipMatchRate(entry.group.match_rate) }}
                  · 删除 B 可释放 {{ formatFileSize(entry.group.estimated_savings) }}
                </p>
              </template>

              <!-- 精确重复 / 近似重复：保留项锁定（不能勾），「设为保留」换保留项；「按建议勾选本组」只作用于这一组。 -->
              <template v-else-if="entry.kind === 'exact' || entry.kind === 'near'">
                <div class="cleanup-card-head">
                  <span class="cleanup-card-reason"><strong>原因：</strong>{{ entry.group.reason }}</span>
                  <div class="cleanup-header__spacer"></div>
                  <button
                    type="button"
                    class="btn-link"
                    data-test="cleanup-suggest-group"
                    :disabled="cleanupDialog.processing || entrySuggestedIDs(entry).length === 0"
                    @click="toggleEntrySuggestion(entry)"
                  >{{ isEntryFullySuggested(entry) ? '取消本组勾选' : '按建议勾选本组' }}</button>
                  <button
                    v-if="entry.kind === 'near'"
                    type="button"
                    class="btn btn--secondary btn--sm"
                    data-test="cleanup-dismiss-near-group"
                    :disabled="cleanupDialog.processing"
                    @click="dismissNearDuplicateGroup(entry.group)"
                  >不是重复</button>
                </div>
                <p v-if="isEntryExhausted(entry)" class="cleanup-card-exhausted" data-test="cleanup-group-exhausted">本组剩下的都已移到废纸篓，没有可保留的一份，这一组不参与合并和删除。</p>
                <ul class="cleanup-member-list">
                  <li v-for="member in entry.members" :key="`${entry.key}-${member.id}`">
                    <div
                      class="cleanup-select-row"
                      :class="{ 'cleanup-select-row--keeper': isEntryKeeper(entry, member), 'cleanup-select-row--trashed': isCleanupTrashed(member) }"
                      data-test="cleanup-member-row"
                    >
                      <input
                        type="checkbox"
                        data-test="cleanup-member-select"
                        :checked="isCleanupSelected(member.id)"
                        :disabled="cleanupDialog.processing || isCleanupTrashed(member) || isCleanupLocked(member.id)"
                        :aria-label="`移到废纸篓 ${member.name || ''}`"
                        @change="toggleCleanupSelection(member.id)"
                      />
                      <CleanupThumbnail :video="member" @preview="previewCleanupVideo" />
                      <span class="cleanup-item-text">
                        <span class="cleanup-item-main">{{ cleanupItemSummary(member) }}</span>
                        <span v-if="member.path" class="cleanup-item-path" :title="member.path">{{ member.path }}</span>
                        <span v-if="cleanupVideoDirectory(member) !== section.directory" class="cleanup-item-otherdir" data-test="cleanup-member-otherdir">位于 {{ cleanupVideoDirectory(member) }}</span>
                        <span v-if="curationFor(member).length" class="cleanup-curation" data-test="cleanup-curation">
                          <span v-for="badge in curationFor(member)" :key="badge.key" class="cleanup-curation__item" :title="badge.title">{{ badge.text }}</span>
                        </span>
                        <span v-if="isLockedByOtherGroup(entry, member)" class="cleanup-item-locked" data-test="cleanup-member-locked">另一组要保留它</span>
                      </span>
                      <span class="cleanup-item-actions">
                        <button type="button" class="btn btn--secondary btn--sm" @click="previewCleanupVideo(member)">预览</button>
                        <strong v-if="isEntryKeeper(entry, member)" class="cleanup-keeper-label" data-test="cleanup-keeper-label">{{ member.id === entryGroup(entry)?.keeperId ? '建议保留' : '保留' }}</strong>
                        <button
                          v-if="!isEntryKeeper(entry, member) && !isCleanupTrashed(member)"
                          type="button"
                          class="btn btn--secondary btn--sm"
                          data-test="cleanup-set-keeper"
                          :disabled="cleanupDialog.processing"
                          @click="setCleanupKeeper(entry, member)"
                        >设为保留</button>
                        <button
                          v-if="entry.kind === 'near' && entry.members.length > 2 && !isCleanupTrashed(member)"
                          type="button"
                          class="btn btn--secondary btn--sm"
                          data-test="cleanup-remove-member"
                          :disabled="cleanupDialog.processing"
                          @click="removeNearDuplicateMember(entry.group, member)"
                        >移出本组</button>
                      </span>
                    </div>
                  </li>
                </ul>
              </template>

              <!-- 极低分辨率 / 极短片段：单条候选，没有保留项；可以「忽略」（D-PC31）。 -->
              <template v-else>
                <div class="cleanup-select-row" :class="{ 'cleanup-select-row--trashed': isCleanupTrashed(entry.keeper) }">
                  <input
                    type="checkbox"
                    :checked="isCleanupSelected(entry.keeper?.id)"
                    :disabled="cleanupDialog.processing || isCleanupTrashed(entry.keeper) || isCleanupLocked(entry.keeper?.id)"
                    @change="toggleCleanupSelection(entry.keeper?.id)"
                  />
                  <CleanupThumbnail :video="entry.keeper" @preview="previewCleanupVideo" />
                  <span class="cleanup-item-text">
                    <span class="cleanup-item-main">{{ cleanupItemSummary(entry.keeper) }}</span>
                    <span v-if="entry.keeper?.path" class="cleanup-item-path" :title="entry.keeper.path">{{ entry.keeper.path }}</span>
                    <span v-if="curationFor(entry.keeper).length" class="cleanup-curation" data-test="cleanup-curation">
                      <span v-for="badge in curationFor(entry.keeper)" :key="badge.key" class="cleanup-curation__item" :title="badge.title">{{ badge.text }}</span>
                    </span>
                    <span v-if="isCleanupLocked(entry.keeper?.id)" class="cleanup-item-locked" data-test="cleanup-member-locked">另一组要保留它</span>
                  </span>
                  <span class="cleanup-item-actions">
                    <button type="button" class="btn btn--secondary btn--sm" @click="previewCleanupVideo(entry.keeper)">预览</button>
                    <button
                      v-if="!isCleanupTrashed(entry.keeper)"
                      type="button"
                      class="btn btn--secondary btn--sm"
                      data-test="cleanup-dismiss-video"
                      :disabled="cleanupDialog.processing"
                      @click="dismissCleanupVideo(entry)"
                    >忽略</button>
                  </span>
                </div>
              </template>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 第一次读清理分析状态。 -->
    <div v-else-if="!statusLoaded" class="cleanup-state" role="status" data-test="cleanup-status-loading">
      <SkeletonBlock :lines="4" />
      <span class="cleanup-muted">正在读取清理分析…</span>
    </div>

    <EmptyState
      v-else
      data-test="cleanup-video-idle"
      icon="copy"
      title="还没有分析过视频库。"
      message="开始分析后，这里会列出精确重复、疑似重复、同源片段与极短 / 低清的视频。"
    >
      <template #actions>
        <button type="button" class="btn btn--primary btn--sm" data-test="cleanup-video-start" :disabled="cleanupDialog.processing" @click="reanalyzeCleanupCandidates">开始分析</button>
      </template>
    </EmptyState>

    <!-- 有勾选才在底部浮出（D-UD-10，与图片标签一致）：「移到废纸篓」只作用于勾选的视频。不给「全选当前结果」：
         近似、同源与截取要逐组看过才能勾（D-PC49）。勾选是审阅结果、不是临时选择：Esc 不清空，只有「取消选择」清。
         改分组的请求（移出本组、各类忽略）还没返回时不能删除（P-032 复审 I-1）；确认框打开期间 processing 已经为真
         （删除流程从确认开始），按钮文字仍写「移到废纸篓」。 -->
    <SelectionBar :count="cleanupSelection.length" :clear-on-escape="false" data-test="cleanup-selection-bar" @clear="clearCleanupSelection">
      <template #actions>
        <span class="cleanup-footer__summary" data-test="cleanup-selection-summary">
          <template v-if="cleanupSelectedSizeText">共 <b>{{ cleanupSelectedSizeText }}</b> · </template>移到废纸篓后可在回收站撤销；在访达清空废纸篓才会释放空间
        </span>
        <button
          type="button"
          class="btn btn--danger"
          data-test="cleanup-trash-selected"
          :disabled="cleanupSelection.length === 0 || cleanupDialog.loading || cleanupDialog.processing || activeCleanupCategory === 'dismissed' || cleanupGroupRequests > 0"
          :title="cleanupGroupRequests > 0 ? '正在更新分组，完成后再删除' : ''"
          @click="trashSelectedCleanupCandidates"
        >{{ cleanupDialog.processing && !deleteConfirm.show ? '处理中…' : `移到废纸篓 (${cleanupSelection.length})` }}</button>
      </template>
    </SelectionBar>

    <!-- 删除前的汇总确认（D-PC49）与「把元数据合并到保留项」（D-PC48，默认勾选）。
         合并在删除之前单独调用；合并失败就不删除。 -->
    <DialogShell
      v-if="deleteConfirm.show"
      title="移到废纸篓"
      size="sm"
      class="cleanup-delete-confirm"
      data-test="cleanup-delete-confirm-dialog"
      @close="answerCleanupDelete(false)"
    >
      <p data-test="cleanup-delete-summary">
        将把 {{ deleteConfirm.summary.count }} 个视频移到废纸篓<template v-if="deleteConfirm.summary.bytes > 0">，共 {{ formatFileSize(deleteConfirm.summary.bytes) }}</template>。
      </p>
      <p v-if="deleteConfirmKindsText" data-test="cleanup-delete-kinds">其中{{ deleteConfirmKindsText }}。</p>
      <p v-if="deleteConfirmSimilarityCount > 0" class="cleanup-delete-confirm__warn" data-test="cleanup-delete-similarity">
        其中 {{ deleteConfirmSimilarityCount }} 个是按画面相似度判断的（近似重复、同源或截取片段），删除前请确认已逐组看过。
      </p>
      <template v-if="deleteConfirm.mergeAvailable">
        <label class="cleanup-delete-confirm__merge">
          <input v-model="deleteConfirm.merge" type="checkbox" data-test="cleanup-merge-toggle" />
          把元数据合并到保留项
        </label>
        <!-- 合并范围按类别说明（§9.1）：截取片段组不合并观看状态与字幕。 -->
        <p v-if="deleteConfirm.mergeScope.full" class="cleanup-delete-confirm__help" data-test="cleanup-merge-scope-full">
          标签、人物、作品集、收藏 / 点赞 / 评分与观看状态（已看、断点）合并到各组保留项；保留项没有同名字幕时，把被删项的字幕复制一份给保留项。
        </p>
        <p v-if="deleteConfirm.mergeScope.clip" class="cleanup-delete-confirm__help" data-test="cleanup-merge-scope-clip">
          截取片段组只合并标签、人物、作品集、收藏 / 点赞 / 评分，不合并观看状态和字幕。
        </p>
        <p class="cleanup-delete-confirm__help" data-test="cleanup-merge-note">标签只合并手动标签（自动标签不合并）。撤销删除不会撤回合并。合并失败时不会删除任何视频。</p>
      </template>
      <p class="cleanup-delete-confirm__help">移到废纸篓后可在回收站撤销；在访达清空废纸篓才会释放空间。所在磁盘不支持废纸篓时，会先问你怎么处理。</p>
      <template #footer>
        <button type="button" class="btn btn--secondary" data-test="cleanup-delete-cancel" @click="answerCleanupDelete(false)">取消</button>
        <button type="button" class="btn btn--danger" data-test="cleanup-delete-confirm" @click="answerCleanupDelete(true)">移到废纸篓</button>
      </template>
    </DialogShell>
    <CleanupConsolidationDialog ref="consolidationDialog" :request="consolidationRequestData" @review="openConsolidationReview" @moved="consolidationMoved" @started="consolidationStarted" />
  </section>
</template>

<script>
import {
  GetCleanupStatus, StartCleanupAnalysisFromSettings, CancelCleanupAnalysis,
  DismissNearDuplicateGroup, DismissNearDuplicateMember, DismissClipCandidate, DismissCleanupVideo, RejectSameSourceRelation,
  ListCleanupDismissals, UndoCleanupDismissals, MergeMediaMetadata, PreviewExternally, GetCleanupConsolidationReview
} from '../../../wailsjs/go/main/App';
import AppBanner from '../ui/AppBanner.vue';
import DialogShell from '../ui/DialogShell.vue';
import EmptyState from '../ui/EmptyState.vue';
import ProgressBar from '../ui/ProgressBar.vue';
import SegmentedControl from '../ui/SegmentedControl.vue';
import SelectionBar from '../ui/SelectionBar.vue';
import SkeletonBlock from '../ui/SkeletonBlock.vue';
import StatTile from '../ui/StatTile.vue';
import CleanupConsolidationDialog from './CleanupConsolidationDialog.vue';
import { consolidationRequest, consolidationGroupIdentity, consolidationReviewSignature, CONSOLIDATION_KINDS } from '../../utils/cleanupConsolidation.js';
import CleanupThumbnail from '../CleanupThumbnail.vue';
import { confirmAction, notify, notifyError, notifySuccess, notifyUndoable } from '../../utils/feedback.js';
import { clipKey, groupPairKeys, memberPairKeys, undoCleanupDismissalRecords, videoKey } from '../../utils/cleanupDismissals.js';
import {
  applySuggestion, cleanupGroup, clearGroupSelection, curationBadges, defaultSelection, deletionPlan, deletionPlanUnchanged,
  describeMergeFailure, describeSelectionKinds, isGroupFullySuggested, keeperOf, lockedIDs, pruneSelection,
  SELECTION_CHANGED_MESSAGE, selectionSummary, setKeeper, similarityCount, suggestedIDs
} from '../../utils/cleanupSelection.js';
import { runtimeEventsMixin } from './runtimeEvents.js';
import { formatElapsedDuration } from './format.js';

const DISMISSAL_PAGE_SIZE = 50;

const DISMISSAL_KINDS = [
  { key: 'near_duplicate', label: '近似重复' },
  { key: 'clip', label: '截取片段' },
  { key: 'short', label: '极短片段' },
  { key: 'low', label: '极低分辨率' }
];

function emptyDismissals(kind = 'near_duplicate') {
  return { kind, items: [], cursor: 0, hasMore: false, loading: false, loaded: false, undoing: false, error: '' };
}

function emptyDeleteConfirm() {
  return { show: false, summary: { count: 0, bytes: 0, byKind: {}, similar: 0 }, mergeAvailable: false, mergeScope: { full: false, clip: false }, merge: true };
}

// 分类统计卡（D-P3-02，与图片标签的统计卡一致）：精确重复 · 疑似重复 · 同源片段 · 极短 / 低清，再加「已忽略」。
// 同源视频与截取片段合在「同源片段」，极低分辨率与极短片段合在「极短 / 低清」；卡片内的候选卡仍按原类别标注。
const CATEGORY_TILES = [
  { key: 'exact', kinds: ['exact'], label: '精确重复', unit: '组', icon: 'copy' },
  { key: 'near', kinds: ['near'], label: '疑似重复', unit: '组', icon: 'transcode' },
  { key: 'same-source', kinds: ['same-source', 'clip'], label: '同源片段', unit: '组', icon: 'videos' },
  { key: 'low', kinds: ['low-resolution', 'low-duration'], label: '极短 / 低清', unit: '个', icon: 'clock' }
];
const DISMISSED_CATEGORY = 'dismissed';

// 清理候选审阅（清理中心的视频标签）：后台分析状态、分类统计卡、按目录分组的候选流、勾选与移到废纸篓。
// 宿主（CleanupCenterPage）通过 ref 调用 open() / refreshStatus() / forgetTrashed() / openConsolidation()；
// 删除那一步（批量删除 + 撤销提示 + 通知片库刷新）经 trashVideos / afterTrashVideos 函数 prop 回到宿主，
// 这样原来的 await 顺序不用拆散。宿主据 state-change 显示「上次分析时间」、决定能不能离开这一页。
export default {
  name: 'CleanupReviewPanel',
  components: { AppBanner, DialogShell, EmptyState, ProgressBar, SegmentedControl, SelectionBar, SkeletonBlock, StatTile, CleanupThumbnail, CleanupConsolidationDialog },
  mixins: [runtimeEventsMixin],
  props: {
    // 「补全感知哈希」按钮的运行态来自宿主跟踪的后台任务状态。
    perceptualHashRunning: { type: Boolean, default: false },
    // 「补全帧哈希」按钮的运行态同样来自宿主。
    frameHashRunning: { type: Boolean, default: false },
    // trashVideos(ids, { names }) → { result: { succeeded, failed, errors[{video_id, error}] }, failedIDs:Set, succeededIDs:[] }
    // 宿主经删除宿主执行删除（进度、取消、不支持废纸篓时的二选一）。names 是 { id: 文件名 }，供二选一弹窗列名字。
    trashVideos: { type: Function, required: true },
    afterTrashVideos: { type: Function, required: true }
  },
  emits: ['badge-change', 'analyzing-change', 'start-perceptual-hash', 'start-frame-hash', 'same-source-rejected', 'trash-settled', 'consolidation-moved', 'state-change'],
  data() {
    return {
      cleanupDialog: {
        loading: false,
        processing: false,
        // cancelling：已经点了「取消分析」，等后台真正停下（期间仍显示进度）。
        cancelling: false,
        // cancelled：上一轮被用户取消，没有结果、也不算失败（D-PC51）。
        cancelled: false,
        analysis: null,
        error: '',
        // stale：结果算出后视频库又变过，提示可能过期但保留结果继续审阅。
        stale: false,
        progress: { stage: '', message: '', current: 0, total: 0, path: '' }
      },
      cleanupSelection: [],
      cleanupSkippedGroups: [],
      consolidationScope: [],
      consolidationReviewTaskID: 0,
      consolidationExternalLocks: [],
      consolidationReviewBaseline: '',
      cleanupModeGeneration: 0,
      // 默认勾选只在换了一批分析结果时套一次（按 started_at 区分），回读同一批结果不能把用户取消的勾重新勾上。
      cleanupSelectionKey: null,
      // 「设为保留」的覆盖：{ 组 key: 视频 id }。
      cleanupKeepOverrides: {},
      // 正在进行、会改分组的请求数（移出本组、不是重复、各类忽略、不是同源）：大于 0 时不能删除（P-032 复审 I-1）。
      cleanupGroupRequests: 0,
      cleanupStatusRequestID: 0,
      // 用户点过的分类（统计卡的 key 或 'dismissed'）；空串表示还没选过，按 defaultCleanupCategory 看第一类有候选的。
      cleanupCategory: '',
      // 第一次读到后端状态之前显示骨架。
      statusLoaded: false,
      // 当前这批全库分析的开始时间（清理中心页头的「上次分析」）。
      analysisStartedAt: '',
      activeCleanupDirectory: '',
      cleanupGroupPage: 0,
      cleanupDirectoryPage: 0,
      cleanupCollapsedDirs: {},
      // 本次审阅中已移到废纸篓的视频 id：结果不重跑，用来提示结果已过期。
      cleanupTrashedIDs: [],
      // 从 AI 同源审阅「去清理」过来时要定位的那一对（{ relationId }）；找到之后清掉。
      cleanupFocus: null,
      cleanupFocusedRelationID: 0,
      cleanupFocusNotice: '',
      dismissals: emptyDismissals(),
      deleteConfirm: emptyDeleteConfirm(),
      cleanupStartedAt: 0,
      cleanupNow: Date.now(),
      cleanupTimer: null,
    };
  },
  mounted() {
    // 挂上时先取一次清理分析状态（只读，不触发分析）。
    this.refreshCleanupStatus();
    // 集中整理在后台移动了文件（这一轮有移动成功的、且已经结束）：本次结果可能已过期，标出来。
    // 对话框开着时它自己也会报 moved，重复标记无妨。
    this.registerRuntimeEvent('cleanup-consolidation-progress', summary => {
      if (summary?.status && summary.status !== 'running' && Number(summary.completed) > 0) this.markConsolidationStale();
    });
    // 离开这一页（分析在后台继续）时也要处理：否则后台跑完没人记录结果，
    // 回来只能看到停在离开那一刻的旧进度。
    this.registerRuntimeEvent('cleanup-progress', async (data) => {
      if (this.consolidationReviewTaskID) return;
      this.startCleanupProgressTracking();
      this.cleanupDialog.loading = data?.stage !== 'done';
      this.cleanupDialog.progress = {
        stage: data?.stage || '',
        message: data?.message || '',
        current: Number(data?.current || 0),
        total: Number(data?.total || 0),
        path: data?.path || ''
      };
      if (data?.stage === 'done') {
        // 回读失败也必须收尾，否则 1 秒计时器和"分析中"徽标会一直挂着。
        try {
          const { status, current } = await this.readCleanupStatus();
          if (!current) return;
          this.applyCleanupStatus(status);
        } catch (err) {
          console.error('读取清理分析结果失败:', err);
          this.cleanupDialog.loading = false;
          this.cleanupDialog.cancelling = false;
          this.resetCleanupProgressTracking();
        }
      }
    });
  },
  beforeUnmount() {
    this._disposed = true;
    this.cleanupModeGeneration++;
    this.cleanupStatusRequestID++;
    this.resetCleanupProgressTracking();
    if (this._resolveDeleteConfirm) this._resolveDeleteConfirm(null);
  },
  // 候选数与「分析中」镜像出去（徽标用）；state-change 给宿主页头与离开保护用。
  watch: {
    activeCleanupDirectory() { this.cleanupGroupPage = 0; },
    activeCleanupCategory() { this.cleanupGroupPage = 0; this.cleanupDirectoryPage = 0; },
    cleanupDirectoryPageCount(count) { this.cleanupDirectoryPage = Math.min(this.cleanupDirectoryPage, count - 1); },
    cleanupGroupPageCount(count) { this.cleanupGroupPage = Math.min(this.cleanupGroupPage, count - 1); },
    cleanupBadgeCount: { handler(count) { this.$emit('badge-change', count); }, immediate: true },
    'cleanupDialog.loading': { handler(loading) { this.$emit('analyzing-change', loading); }, immediate: true },
    reviewState: { handler(state) { this.$emit('state-change', state); }, immediate: true }
  },
  computed: {
    // 给宿主（清理中心页）的状态：页头的「上次分析 / 正在分析」、「重新分析」能不能点、能不能离开这一页。
    // locked：删除确认框开着或删除进行中（删除流程从确认框打开起算）。
    reviewState() {
      return {
        analyzing: Boolean(this.cleanupDialog.loading),
        processing: Boolean(this.cleanupDialog.processing),
        locked: Boolean(this.cleanupCloseLocked),
        cancelled: Boolean(this.cleanupDialog.cancelled),
        failed: Boolean(this.cleanupDialog.error),
        statusLoaded: Boolean(this.statusLoaded),
        hasAnalysis: Boolean(this.cleanupDialog.analysis || this.analysisStartedAt),
        startedAt: this.analysisStartedAt
      };
    },
    consolidationRequestData() {
      if (this.consolidationReviewTaskID || this.cleanupTrashedIDs.length || !this.cleanupDialog.analysis) return null;
      return consolidationRequest(this.cleanupGroups, { scope: this.consolidationScope, overrides: this.cleanupKeepOverrides, skipped: this.cleanupSkippedGroups, selection: this.cleanupSelection });
    },
    cleanupCandidateCount() {
      return this.getAllCleanupCandidates().length;
    },
    cleanupResultStale() {
      return Boolean(this.cleanupDialog.analysis && (this.cleanupDialog.stale || this.cleanupTrashedIDs.length));
    },
    // 删除流程从确认框打开起算（processing 在那时已经置上），到合并与删除全部结束为止。
    cleanupCloseLocked() {
      return this.cleanupDialog.processing || this.deleteConfirm.show;
    },
    cleanupThresholds() {
      const thresholds = this.cleanupDialog.analysis?.thresholds || {};
      return {
        shortSeconds: Number(thresholds.short_seconds || 0),
        lowWidth: Number(thresholds.low_width || 0),
        lowHeight: Number(thresholds.low_height || 0)
      };
    },
    // 各类别的前置条件覆盖率（D-PC50）。老结果没有 coverage 时为 null，界面不显示覆盖率。
    cleanupCoverage() {
      const coverage = this.cleanupDialog.analysis?.coverage;
      if (!coverage) return null;
      const count = value => ({ done: Number(value?.done || 0), total: Number(value?.total || 0) });
      return {
        near: count(coverage.perceptual_hash),
        clip: count(coverage.frame_hash),
        'same-source': { done: Number(coverage.same_source?.evaluated || 0), total: Number(coverage.same_source?.total || 0) }
      };
    },
    // 顶层按目录分组：每条候选归到"建议保留项"所在目录（单条候选就按它自己的目录），
    // 组员仍可位于别的目录，行内会标出来。
    cleanupDirectorySections() {
      const analysis = this.cleanupDialog.analysis;
      if (!analysis) return [];
      const buckets = new Map();
      const push = (kind, key, group, keeper, members) => {
        const directory = this.cleanupVideoDirectory(keeper);
        if (!buckets.has(directory)) buckets.set(directory, { directory, entries: [], videoCount: 0 });
        const bucket = buckets.get(directory);
        bucket.entries.push({ kind, key, group, keeper, members });
        bucket.videoCount += members.length;
      };
      for (const group of analysis.same_source_groups || []) {
        push('same-source', `same-source-${group.relation_id}`, group, group.preferred,
          [group.preferred, group.alternative].filter(Boolean));
      }
      for (const group of analysis.duplicate_groups || []) {
        push('exact', `exact-${group.original?.id}`, group, group.original,
          [group.original, ...(group.candidates || [])].filter(Boolean));
      }
      for (const group of analysis.near_duplicate_groups || []) {
        push('near', `near-${group.original?.id}`, group, group.original,
          [group.original, ...(group.candidates || [])].filter(Boolean));
      }
      for (const group of analysis.clip_groups || []) {
        // 建议保留完整片，所以整条候选归到完整片所在目录；片段可能在别的目录，行内会标出来。
        push('clip', `clip-${group.full?.id}-${group.clip?.id}`, group, group.full,
          [group.full, group.clip].filter(Boolean));
      }
      for (const video of analysis.low_resolution || []) {
        push('low-resolution', `res-${video.id}`, null, video, [video]);
      }
      for (const video of analysis.low_duration || []) {
        push('low-duration', `dur-${video.id}`, null, video, [video]);
      }
      return [...buckets.values()].sort((a, b) => a.directory.localeCompare(b.directory));
    },
    // 勾选规则用的统一组（utils/cleanupSelection.js）。同源与截取片段的保留项固定，精确 / 近似可以换。
    // 建议保留项已经移到废纸篓时（例如「移出本组」让它升了上来），接替者从仍在库的成员里选；
    // 一个在库的都没有时这一组不参与合并与删除（P-032 复审 I-a）。
    cleanupGroups() {
      const groups = [];
      for (const section of this.cleanupDirectorySections) {
        for (const entry of section.entries) {
          const single = entry.kind === 'low-resolution' || entry.kind === 'low-duration';
          groups.push(cleanupGroup({
            key: entry.key,
            kind: entry.kind,
            keeperId: single ? null : entry.keeper?.id,
            memberIds: entry.members.map(member => member.id),
            switchable: entry.kind === 'exact' || entry.kind === 'near',
            unavailable: this.cleanupTrashedIDs
          }));
        }
      }
      return groups;
    },
    cleanupGroupByKey() {
      return new Map(this.cleanupGroups.map(group => [group.key, group]));
    },
    cleanupLockedIDs() {
      return new Set([...lockedIDs(this.cleanupGroups, this.cleanupKeepOverrides, this.cleanupSkippedGroups), ...this.consolidationExternalLocks]);
    },
    cleanupMemberSizes() {
      const sizes = new Map();
      for (const section of this.cleanupDirectorySections) {
        for (const entry of section.entries) {
          for (const member of entry.members) sizes.set(member.id, Number(member.size || 0));
        }
      }
      return sizes;
    },
    // 本轮被跳过的条目。外置盘没挂载时这个数会很大，而在有它之前，插着盘和
    // 不插盘跑出来的界面长得一模一样——用户只会觉得"检测不准"。
    cleanupSkippedText() {
      const analysis = this.cleanupDialog.analysis;
      if (!analysis) return '';
      const unavailable = analysis.skipped_unavailable || 0;
      const metadata = analysis.skipped_metadata || 0;
      if (!unavailable && !metadata) return '';
      const parts = [];
      if (unavailable) parts.push(`本轮跳过 ${unavailable} 个视频（文件不可访问，如外置盘未挂载）`);
      if (metadata) parts.push(`另有 ${metadata} 个取不到时长或分辨率，只参与精确重复`);
      return `${parts.join('，')}。`;
    },
    // 每张分类统计卡的数字：候选条数（组或单条）、涉及的视频数、其中已勾的视频数。
    cleanupCategoryStats() {
      const selected = new Set(this.cleanupSelection);
      const stats = Object.fromEntries(CATEGORY_TILES.map(tile => [tile.key, { count: 0, videos: new Set() }]));
      const tileOf = new Map(CATEGORY_TILES.flatMap(tile => tile.kinds.map(kind => [kind, tile.key])));
      for (const section of this.cleanupDirectorySections) {
        for (const entry of section.entries) {
          const bucket = stats[tileOf.get(entry.kind)];
          if (!bucket) continue;
          bucket.count += 1;
          for (const member of entry.members) bucket.videos.add(Number(member.id));
        }
      }
      return Object.fromEntries(Object.entries(stats).map(([key, bucket]) => [key, {
        count: bucket.count,
        videos: bucket.videos.size,
        selected: [...bucket.videos].filter(id => selected.has(id)).length
      }]));
    },
    cleanupCategoryOptions() {
      if (!this.cleanupDialog.analysis) return [];
      // 判定阈值挂在各自的类别上——「低清到底指多低」这个疑问就产生在这里。
      // 两类阈值来自设置（D-PC36），说明按本轮实际使用的阈值显示。
      const { lowWidth, lowHeight, shortSeconds } = this.cleanupThresholds;
      const lowRule = lowWidth > 0 && lowHeight > 0 ? `分辨率低于 ${lowWidth}×${lowHeight}` : '分辨率低于设置的阈值';
      const shortRule = shortSeconds > 0 ? `时长 < ${shortSeconds} 秒` : '时长低于设置的阈值';
      // hint 是卡片的悬停说明（完整判定规则），tip 是卡片上的一行短说明。
      const hints = {
        exact: '大小 + 采样哈希完全一致；建议跨组集中保留在同一目录，默认勾选其余副本',
        near: '多帧感知哈希接近；默认不勾选',
        'same-source': '同源视频是同一片源的不同转码或裁剪版本，来自 AI 打标的「查找同源」；截取片段是完整片里截下来的一段，逐帧哈希对齐命中。都不会默认选中',
        low: `极低分辨率：${lowRule}；极短片段：${shortRule}（阈值在设置「自动化与扫描」里修改）`
      };
      const tips = {
        exact: '内容一致，默认已勾副本',
        near: '画面近似，默认不勾',
        'same-source': '同源与截取片段，默认不勾',
        low: lowWidth > 0 && lowHeight > 0 && shortSeconds > 0 ? `低于 ${lowWidth}×${lowHeight} 或短于 ${shortSeconds} 秒` : '低于设置的阈值'
      };
      return CATEGORY_TILES.map(tile => {
        const stats = this.cleanupCategoryStats[tile.key];
        return {
          ...tile,
          count: stats.count,
          note: `${stats.videos} 个视频 · 已勾 ${stats.selected} 个`,
          tip: tips[tile.key],
          hint: hints[tile.key],
          coverage: this.categoryCoverageLabel(tile.key)
        };
      });
    },
    // 正在看的分类：用户点过就用它（哪怕那一类已经清空，卡片上看得到 0）；没点过时看第一类有候选的。
    activeCleanupCategory() {
      const chosen = this.cleanupCategory;
      if (chosen === DISMISSED_CATEGORY || CATEGORY_TILES.some(tile => tile.key === chosen)) return chosen;
      return this.defaultCleanupCategory;
    },
    defaultCleanupCategory() {
      return (this.cleanupCategoryOptions.find(option => option.count > 0) || CATEGORY_TILES[0]).key;
    },
    activeCategoryLabel() {
      return (CATEGORY_TILES.find(tile => tile.key === this.activeCleanupCategory) || CATEGORY_TILES[0]).label;
    },
    // 标题行上这一类的规模：「3 组 · 2 个目录」。
    activeCategoryStat() {
      const tile = CATEGORY_TILES.find(item => item.key === this.activeCleanupCategory) || CATEGORY_TILES[0];
      const count = this.cleanupCategoryStats[tile.key]?.count || 0;
      return `${count} ${tile.unit} · ${this.cleanupFilteredSections.length} 个目录`;
    },
    // 「已忽略」卡片上的数字：读过才知道有几条（分页读，还有下一页时带「+」）。
    dismissedTileValue() {
      if (!this.dismissals.loaded) return '—';
      return `${this.dismissals.items.length}${this.dismissals.hasMore ? '+' : ''}`;
    },
    // 分类只收窄看到的候选，不改变分析结果本身。
    cleanupFilteredSections() {
      const tile = CATEGORY_TILES.find(item => item.key === this.activeCleanupCategory);
      if (!tile) return [];
      return this.cleanupDirectorySections
        .map(section => {
          const entries = section.entries.filter(entry => tile.kinds.includes(entry.kind));
          return { ...section, entries, videoCount: entries.reduce((total, entry) => total + entry.members.length, 0) };
        })
        .filter(section => section.entries.length > 0);
    },
    // 目录栏上每个目录已勾选几个（P-032 评审 Minor 8）：只看当前看得到的候选，同一视频在一个目录里只算一次。
    cleanupSelectedCountByDirectory() {
      const selected = new Set(this.cleanupSelection);
      const counts = new Map();
      for (const section of this.cleanupFilteredSections) {
        const ids = new Set();
        for (const entry of section.entries) {
          for (const member of entry.members) {
            if (selected.has(member.id)) ids.add(member.id);
          }
        }
        counts.set(section.directory, ids.size);
      }
      return counts;
    },
    cleanupDirectoryPageCount() { return Math.max(1, Math.ceil(this.cleanupFilteredSections.length / 80)); },
    cleanupActiveSection() {
      const sections = this.cleanupFilteredSections;
      return sections.find(section => section.directory === this.activeCleanupDirectory) || sections[0];
    },
    cleanupGroupPageCount() { return Math.max(1, Math.ceil((this.cleanupActiveSection?.entries.length || 0) / 40)); },
    cleanupVisibleDirectorySections() {
      const sections = this.cleanupFilteredSections;
      const page = Math.min(this.cleanupDirectoryPage, this.cleanupDirectoryPageCount - 1);
      return sections.slice(page * 80, (page + 1) * 80);
    },
    activeCleanupSections() {
      const section = this.cleanupActiveSection;
      if (!section) return [];
      const page = Math.min(this.cleanupGroupPage, this.cleanupGroupPageCount - 1);
      return [{ ...section, entryCount: section.entries.length, entries: section.entries.slice(page * 40, (page + 1) * 40) }];
    },
    // 当前分类为空时的空态：分清「没有」与「还没算 / 只算了一部分」（D-PC50）。整轮都没有候选、用户也没点过哪一类时
    // 说整轮的。action 是下一步：补全感知哈希 / 帧哈希；都不用补时给「重新分析」。
    cleanupEmptyState() {
      if (!this.cleanupDialog.analysis || this.cleanupFilteredSections.length > 0) return null;
      const notComputed = kind => {
        const coverage = this.cleanupCoverage?.[kind];
        return Boolean(coverage && coverage.total > 0 && coverage.done === 0);
      };
      if (this.cleanupDirectorySections.length === 0 && !this.cleanupCategory) {
        const details = ['near', 'clip', 'same-source']
          .map(kind => this.coverageNote(kind))
          .filter(Boolean);
        const action = notComputed('near') ? 'perceptual-hash' : notComputed('clip') ? 'frame-hash' : null;
        return { title: '当前没有命中轻量清理规则的候选项。', details, action };
      }
      const tile = CATEGORY_TILES.find(item => item.key === this.activeCleanupCategory) || CATEGORY_TILES[0];
      const NOT_COMPUTED_DETAILS = {
        near: '还没有视频算过感知哈希，近似重复检测还没开始。',
        clip: '还没有视频算过帧哈希，截取片段识别还没开始。',
        'same-source': '同源候选来自 AI 打标的「查找同源」，还没有视频做过同源判断。'
      };
      const action = tile.kinds.includes('near') && notComputed('near') ? 'perceptual-hash'
        : tile.kinds.includes('clip') && notComputed('clip') ? 'frame-hash' : null;
      const measured = tile.kinds.filter(kind => this.cleanupCoverage?.[kind]?.total > 0);
      if (measured.length > 0 && measured.every(notComputed)) {
        return { title: '尚未计算', details: measured.map(kind => NOT_COMPUTED_DETAILS[kind]), action };
      }
      const notes = tile.kinds.map(kind => this.coverageNote(kind)).filter(Boolean);
      return { title: `没有发现${tile.label}候选。`, details: notes, action };
    },
    // 底栏的"释放多少"只算真正选中的那些视频，不含建议保留项。
    cleanupSelectedSizeText() {
      const bytes = this.selectedBytes(this.cleanupSelection);
      return bytes > 0 ? this.formatFileSize(bytes) : '';
    },
    cleanupReleasableText() {
      let bytes = 0;
      for (const section of this.cleanupDirectorySections) {
        for (const entry of section.entries) {
          // 每组里除建议保留项之外的部分才是可释放空间。
          for (const member of entry.members) {
            if (entry.keeper && member.id === entry.keeper.id) continue;
            bytes += Number(member.size || 0);
          }
        }
      }
      return bytes > 0 ? this.formatFileSize(bytes) : '';
    },
    // 后台跑完不自动重来，按钮徽标直接报出待审阅项数，提醒去处理。
    cleanupBadgeCount() {
      const analysis = this.cleanupDialog.analysis;
      if (!analysis) return 0;
      return (analysis.duplicate_groups?.length || 0)
        + (analysis.near_duplicate_groups?.length || 0)
        + (analysis.same_source_groups?.length || 0)
        + (analysis.clip_groups?.length || 0)
        + (analysis.low_resolution?.length || 0)
        + (analysis.low_duration?.length || 0);
    },
    cleanupStageLabel() {
      const stage = this.cleanupDialog.progress.stage;
      if (stage === 'load') return '读取候选记录';
      if (stage === 'group') return '按文件大小整理候选';
      if (stage === 'hash') return '计算疑似重复文件哈希';
      if (stage === 'done') return '分析完成';
      return '准备分析';
    },
    cleanupElapsedText() {
      if (!this.cleanupStartedAt) return '';
      return this.formatElapsedDuration(this.cleanupNow - this.cleanupStartedAt);
    },
    cleanupProgressPercent() {
      const stage = this.cleanupDialog.progress.stage;
      if (stage === 'load' || stage === 'done') return null;
      const total = Number(this.cleanupDialog.progress.total || 0);
      const current = Number(this.cleanupDialog.progress.current || 0);
      if (total <= 0) return null;
      return Math.min(100, Math.max(0, Math.round((current / total) * 100)));
    },
    dismissalKindSegments() {
      return this.dismissalKindOptions.map(option => ({ value: option.key, label: option.label }));
    },
    dismissalKindOptions() {
      return DISMISSAL_KINDS;
    },
    dismissalHint() {
      switch (this.dismissals.kind) {
        case 'near_duplicate':
          return '「不是重复」和「移出本组」留下的记录。撤销后这些视频在下次分析时重新参与近似重复检测；当时一并判为「不是同源」的同源关系不会恢复。';
        case 'clip':
          return '截取片段「忽略」留下的记录。撤销后这一对在下次分析时重新参与识别。';
        default:
          return '「忽略」留下的记录。撤销后这个视频在下次分析时重新参与这一类判断。';
      }
    },
    deleteConfirmKindsText() {
      return describeSelectionKinds(this.deleteConfirm.summary, '个');
    },
    deleteConfirmSimilarityCount() {
      return similarityCount(this.deleteConfirm.summary);
    }
  },
  methods: {
    consolidationEligible(entry) { return CONSOLIDATION_KINDS.includes(entry.kind); },
    toggleConsolidationScope(entry) {
      if (this.cleanupDialog.processing || this.cleanupSkippedGroups.includes(entry.key) || !this.consolidationEligible(entry)) return;
      this.consolidationScope = this.consolidationScope.includes(entry.key) ? this.consolidationScope.filter(key => key !== entry.key) : [...this.consolidationScope, entry.key];
    },
    toggleSkippedGroup(entry) {
      if (this.cleanupDialog.processing) return;
      this.cleanupSkippedGroups = this.cleanupSkippedGroups.includes(entry.key) ? this.cleanupSkippedGroups.filter(key => key !== entry.key) : [...this.cleanupSkippedGroups, entry.key];
      this.consolidationScope = this.consolidationScope.filter(key => key !== entry.key);
      this.cleanupSelection = pruneSelection(this.cleanupSelection, this.cleanupGroups, this.selectionOptions());
    },
    resetConsolidationReview() {
      this.cleanupModeGeneration++; this.cleanupStatusRequestID++;
      if (this.consolidationReviewTaskID) { this.cleanupSelectionKey = null; this.cleanupKeepOverrides = {}; this.cleanupSelection = []; this.cleanupSkippedGroups = []; this.consolidationScope = []; }
      this.consolidationReviewTaskID = 0; this.consolidationExternalLocks = []; this.consolidationReviewBaseline = '';
    },
    openConsolidation(taskID = 0) {
      if (this.cleanupCloseLocked || this.cleanupGroupRequests > 0) { notify('当前清理操作完成后再打开集中整理任务。'); return false; }
      if (!taskID && (this.cleanupTrashedIDs.length || this.cleanupDialog.stale)) { notifyError('分析结果可能已过期，请重新分析后再集中整理。'); return; }
      if (!taskID && !this.consolidationRequestData?.groups.length) return;
      if (taskID) this.resetConsolidationReview();
      return this.$refs.consolidationDialog?.open(Number(taskID));
    },
    markConsolidationStale() { this.cleanupDialog.stale = true; },
    consolidationStarted() { this.cleanupDialog.stale = true; },
    consolidationMoved(summary) { this.cleanupDialog.stale = true; this.$emit('consolidation-moved', summary); },
    async openConsolidationReview(taskID) {
      if (this.cleanupCloseLocked || this.cleanupGroupRequests > 0) return;
      this.resetConsolidationReview();
      this.cleanupSkippedGroups = []; this.consolidationScope = []; this.cleanupSelection = []; this.cleanupKeepOverrides = {};
      this.consolidationReviewTaskID = Number(taskID);
      const generation = this.cleanupModeGeneration;
      this.resetCleanupProgressTracking(); this.cleanupDialog.loading = true; this.cleanupDialog.error = ''; this.cleanupDialog.analysis = null;
      try {
        const review = await GetCleanupConsolidationReview(Number(taskID));
        if (generation !== this.cleanupModeGeneration) return;
        if (!review || Number(review.task_id) !== Number(taskID)) throw new Error('集中整理结果不可用，请重新打开任务。');
        this.cleanupDialog.analysis = review.analysis;
        this.cleanupDialog.stale = false; this.cleanupDialog.cancelled = false; this.cleanupTrashedIDs = []; this.cleanupCategory = ''; this.activeCleanupDirectory = '';
        this.consolidationExternalLocks = review.locked_ids || [];
        const keepers = new Map((review.groups || []).map(group => [consolidationGroupIdentity(group), group.keeper_id]));
        this.cleanupKeepOverrides = Object.fromEntries(this.cleanupGroups.filter(group => group.switchable).map(group => [group.key, keepers.get(consolidationGroupIdentity(group))]));
        this.cleanupSelection = pruneSelection((review.groups || []).flatMap(group => group.selected_ids || []), this.cleanupGroups, this.selectionOptions());
        this.consolidationReviewBaseline = consolidationReviewSignature(review);
      } catch (err) { if (generation === this.cleanupModeGeneration) this.cleanupDialog.error = String(err?.message || err); }
      finally { if (generation === this.cleanupModeGeneration) this.cleanupDialog.loading = false; }
    },
    async revalidateConsolidationReview() {
      const taskID = this.consolidationReviewTaskID;
      if (!taskID) return;
      const generation = this.cleanupModeGeneration;
      const review = await GetCleanupConsolidationReview(taskID);
      if (generation !== this.cleanupModeGeneration || taskID !== this.consolidationReviewTaskID || consolidationReviewSignature(review) !== this.consolidationReviewBaseline) {
        throw new Error('本次集中整理的文件、分组或保留保护已变化，请重新打开任务审阅。这次没有合并或删除。');
      }
    },
    // focus：{ relationId }，从 AI 同源审阅「去清理」过来时定位那一对同源候选。
    open(focus = null) {
      if (this.cleanupCloseLocked || this.cleanupGroupRequests > 0) return;
      this.resetConsolidationReview();
      const relationId = Number(focus?.relationId || 0);
      this.cleanupFocus = relationId > 0 ? { relationId } : null;
      this.cleanupFocusedRelationID = 0;
      this.cleanupFocusNotice = '';
      return this.openCleanupDialog();
    },
    refreshStatus() {
      return this.refreshCleanupStatus();
    },
    forgetTrashed(videoID) {
      this.forgetCleanupTrashed(videoID);
    },
    formatElapsedDuration,
    startCleanupProgressTracking(startedAt = 0) {
      // 进列表页时分析可能早就在跑了，用后端的 started_at 才能算对已运行时长。
      const backendStart = startedAt ? new Date(startedAt).getTime() : 0;
      if (Number.isFinite(backendStart) && backendStart > 0) {
        this.cleanupStartedAt = backendStart;
      } else if (!this.cleanupStartedAt) {
        this.cleanupStartedAt = Date.now();
      }
      this.cleanupNow = Date.now();
      if (this.cleanupTimer) {
        return;
      }
      this.cleanupTimer = window.setInterval(() => {
        this.cleanupNow = Date.now();
      }, 1000);
    },
    resetCleanupProgressTracking() {
      if (this.cleanupTimer) {
        clearInterval(this.cleanupTimer);
        this.cleanupTimer = null;
      }
      this.cleanupStartedAt = 0;
      this.cleanupNow = Date.now();
      if (this.cleanupDialog?.progress) {
        this.cleanupDialog.progress = { stage: '', message: '', current: 0, total: 0, path: '' };
      }
    },
    cleanupVideoDirectory(video) {
      return String(video?.directory || '').trim() || '未知目录';
    },
    cleanupKindLabel(kind) {
      if (kind === 'same-source') return '疑似同源（不会默认选中）';
      if (kind === 'exact') return '精确重复';
      if (kind === 'near') return '近似重复（不同转码，不会默认选中）';
      if (kind === 'clip') return '截取片段（不会默认选中）';
      const { lowWidth, lowHeight, shortSeconds } = this.cleanupThresholds;
      if (kind === 'low-resolution') return lowWidth > 0 && lowHeight > 0 ? `极低分辨率（< ${lowWidth}×${lowHeight}）` : '极低分辨率';
      return shortSeconds > 0 ? `极短片段（< ${shortSeconds} 秒）` : '极短片段';
    },
    // 类别标题旁的覆盖率：还没算显示「尚未计算」，只算了一部分显示「已算 X / Y」。
    coverageLabel(kind) {
      const coverage = this.cleanupCoverage?.[kind];
      if (!coverage || coverage.total <= 0) return '';
      if (coverage.done === 0) return '尚未计算';
      if (coverage.done < coverage.total) return `已算 ${coverage.done} / ${coverage.total}`;
      return '';
    },
    // 统计卡上的覆盖度：同源片段那张合了两类，各自标出没算完的那一类。
    categoryCoverageLabel(key) {
      if (key === 'near') return this.coverageLabel('near');
      if (key !== 'same-source') return '';
      return [['same-source', '同源视频'], ['clip', '截取片段']]
        .map(([kind, name]) => (this.coverageLabel(kind) ? `${name}${this.coverageLabel(kind)}` : ''))
        .filter(Boolean)
        .join(' · ');
    },
    coverageNote(kind) {
      const coverage = this.cleanupCoverage?.[kind];
      if (!coverage || coverage.total <= 0 || coverage.done >= coverage.total) return '';
      if (kind === 'near') {
        return coverage.done === 0
          ? '近似重复尚未计算：还没有视频算过感知哈希。'
          : `近似重复只覆盖了一部分：感知哈希已算 ${coverage.done} / ${coverage.total}。`;
      }
      if (kind === 'clip') {
        return coverage.done === 0
          ? '截取片段尚未计算：还没有视频算过帧哈希。'
          : `截取片段只覆盖了一部分：帧哈希已算 ${coverage.done} / ${coverage.total}。`;
      }
      return coverage.done === 0
        ? '同源视频尚未计算：同源候选来自 AI 打标的「查找同源」，还没有视频做过同源判断。'
        : `同源视频只覆盖了一部分：AI 打标已对 ${coverage.done} / ${coverage.total} 个视频做过同源判断。`;
    },
    // key 为空串时回到默认分类（第一类有候选的）。
    selectCleanupCategory(key) {
      this.cleanupCategory = key;
      if (key === DISMISSED_CATEGORY && !this.dismissals.loaded && !this.dismissals.loading) {
        this.loadCleanupDismissals(true);
      }
    },
    // 偏移可能是 0（片头截取），formatDuration 对 0 返回空串，这里单独给一个口径。
    formatClipOffset(seconds) {
      const value = Number(seconds || 0);
      if (!Number.isFinite(value) || value <= 0) return '00:00';
      return this.formatDuration(value) || '00:00';
    },
    formatClipMatchRate(rate) {
      const value = Number(rate || 0);
      if (!Number.isFinite(value) || value <= 0) return '0%';
      return `${Math.round(value * 100)}%`;
    },
    // 结果不重跑，已移到废纸篓的行留在原地但置灰禁选，避免重复删已删的项。
    isCleanupTrashed(video) {
      return this.cleanupTrashedIDs.includes(Number(video?.id));
    },
    isCleanupDirCollapsed(directory) {
      return !!this.cleanupCollapsedDirs[directory];
    },
    toggleCleanupDir(directory) {
      this.cleanupCollapsedDirs = { ...this.cleanupCollapsedDirs, [directory]: !this.cleanupCollapsedDirs[directory] };
    },
    curationFor(video) {
      if (!video) return [];
      return curationBadges(this.cleanupDialog.analysis?.curation?.[video.id]);
    },
    selectionOptions(overrides = this.cleanupKeepOverrides) {
      const locked = overrides === this.cleanupKeepOverrides ? this.cleanupLockedIDs : new Set([...lockedIDs(this.cleanupGroups, overrides, this.cleanupSkippedGroups), ...this.consolidationExternalLocks]);
      return { overrides, skipped: this.cleanupSkippedGroups, locked, excluded: this.cleanupTrashedIDs };
    },
    entryGroup(entry) {
      return this.cleanupGroupByKey.get(entry.key) || null;
    },
    isEntryKeeper(entry, member) {
      return keeperOf(this.entryGroup(entry), this.cleanupKeepOverrides) === Number(member?.id);
    },
    isCleanupLocked(videoID) {
      return this.cleanupLockedIDs.has(Number(videoID));
    },
    // 这一行是别的组的保留项：本组也不能勾它。已移到废纸篓的行不再这样标（本组剩下的都已删时它们也锁定）。
    isLockedByOtherGroup(entry, member) {
      return this.isCleanupLocked(member?.id) && !this.isEntryKeeper(entry, member) && !this.isCleanupTrashed(member);
    },
    // 本组剩下的都已移到废纸篓，没有可接替的保留项（P-032 复审 I-a）。
    isEntryExhausted(entry) {
      return Boolean(this.entryGroup(entry)?.exhausted);
    },
    entrySuggestedIDs(entry) {
      return suggestedIDs(this.entryGroup(entry), this.selectionOptions());
    },
    isEntryFullySuggested(entry) {
      return isGroupFullySuggested(this.cleanupSelection, this.entryGroup(entry), this.selectionOptions());
    },
    // 「按建议勾选本组」只作用于这一组（D-PC49），再点一次取消本组勾选。
    toggleEntrySuggestion(entry) {
      const group = this.entryGroup(entry);
      if (!group || this.cleanupDialog.processing) return;
      this.cleanupSelection = this.isEntryFullySuggested(entry)
        ? clearGroupSelection(this.cleanupSelection, group)
        : applySuggestion(this.cleanupSelection, group, this.selectionOptions());
    },
    // 删除进行中不能换保留项（P-032 评审 I-1）：合并计划已按原保留项算好，这时换过去的新保留项
    // 可能正在被删。勾选、移出本组与各类忽略同理，按钮都禁用，这里再兜一次。
    setCleanupKeeper(entry, member) {
      const group = this.entryGroup(entry);
      if (!group || this.cleanupDialog.processing || this.isCleanupTrashed(member)) return;
      const next = setKeeper({
        groups: this.cleanupGroups,
        overrides: this.cleanupKeepOverrides,
        selection: this.cleanupSelection,
        excluded: this.cleanupTrashedIDs, skipped: this.cleanupSkippedGroups
      }, group, member.id);
      this.cleanupKeepOverrides = next.overrides;
      this.cleanupSelection = pruneSelection(next.selection, this.cleanupGroups, this.selectionOptions());
    },
    selectedBytes(ids) {
      const sizes = this.cleanupMemberSizes;
      let bytes = 0;
      for (const id of new Set(ids)) bytes += sizes.get(id) || 0;
      return bytes;
    },
    applyCleanupStatus(status) {
      if (!status || this.consolidationReviewTaskID) return;
      this.cleanupDialog.loading = !!status.running;
      this.cleanupDialog.error = status.error || '';
      this.cleanupDialog.stale = !!status.stale;
      this.cleanupDialog.cancelled = !status.running && !!status.cancelled;
      if (!status.running) this.cleanupDialog.cancelling = false;
      this.cleanupDialog.analysis = status.analysis || null;
      this.analysisStartedAt = this.cleanupDialog.analysis ? String(status.started_at || '') : '';
      if (this.cleanupDialog.analysis) {
        const key = String(status.started_at || '');
        if (key !== this.cleanupSelectionKey) {
          // 换了一批结果：套一次默认勾选（只勾精确重复的非保留项），保留项覆盖作废。
          this.cleanupSelectionKey = key;
          this.cleanupKeepOverrides = {};
          this.cleanupSkippedGroups = [];
          this.consolidationScope = [];
          this.cleanupSelection = defaultSelection(this.cleanupGroups, this.selectionOptions({}));
        } else {
          this.cleanupSelection = pruneSelection(this.cleanupSelection, this.cleanupGroups, this.selectionOptions());
        }
      } else {
        this.cleanupSelection = [];
      }
      this.cleanupDialog.progress = status.progress || { stage: '', message: '', current: 0, total: 0, path: '' };
      if (status.running) {
        this.startCleanupProgressTracking(status.started_at);
      } else {
        this.resetCleanupProgressTracking();
        this.cleanupDialog.progress = status.progress || this.cleanupDialog.progress;
      }
      this.applyCleanupFocus();
    },
    // 定位「去清理」带过来的同源对：切到同源类别与它所在的目录。结果里没有这一对时说明原因。
    applyCleanupFocus() {
      const focus = this.cleanupFocus;
      if (!focus || !this.cleanupDialog.analysis) return;
      this.cleanupFocus = null;
      for (const section of this.cleanupDirectorySections) {
        const entry = section.entries.find(item => item.kind === 'same-source' && Number(item.group?.relation_id) === focus.relationId);
        if (!entry) continue;
        this.cleanupCategory = 'same-source';
        this.activeCleanupDirectory = section.directory;
        this.cleanupFocusedRelationID = focus.relationId;
        this.cleanupFocusNotice = '';
        // 先让类别/目录切换的归零 watcher 落地，再定位两个分页。
        this.$nextTick(() => {
          if (this.cleanupFocusedRelationID !== focus.relationId || this.cleanupCategory !== 'same-source' || this.activeCleanupDirectory !== section.directory) return;
          const directoryIndex = this.cleanupFilteredSections.findIndex(item => item.directory === section.directory);
          const filtered = this.cleanupFilteredSections[directoryIndex];
          const entryIndex = filtered?.entries.findIndex(item => item.key === entry.key) ?? -1;
          if (directoryIndex < 0 || entryIndex < 0) return;
          this.cleanupDirectoryPage = Math.floor(directoryIndex / 80);
          this.cleanupGroupPage = Math.floor(entryIndex / 40);
          this.$nextTick(() => {
            const node = document.querySelector('[data-test="cleanup-group-card"][data-focused="true"]');
            if (node && typeof node.scrollIntoView === 'function') node.scrollIntoView({ block: 'center' });
          });
        });
        return;
      }
      this.cleanupFocusNotice = '要处理的那一对同源视频不在当前的分析结果里（可能是分析之后才确认的）。点「重新分析」后再找。';
    },
    isFocusedEntry(entry) {
      return entry.kind === 'same-source' && this.cleanupFocusedRelationID > 0
        && Number(entry.group?.relation_id) === this.cleanupFocusedRelationID;
    },
    // 决策保存后，之前发出的回读不能把已处理的行重新盖回来。
    async readCleanupStatus() {
      const requestID = ++this.cleanupStatusRequestID;
      try {
        const status = await GetCleanupStatus();
        return { status, current: requestID === this.cleanupStatusRequestID };
      } catch (err) {
        if (requestID !== this.cleanupStatusRequestID) return { current: false };
        throw err;
      } finally {
        // 读过一次（成功或失败）就不再显示首屏骨架；失败时由调用方给出错误。
        this.statusLoaded = true;
      }
    },
    // 只读地同步一次后端状态（进入这一页、换回这个标签时）；不触发分析。
    async refreshCleanupStatus() {
      if (this.consolidationReviewTaskID) return;
      try {
        const { status, current } = await this.readCleanupStatus();
        if (!current) return;
        if (status?.running || status?.completed || status?.cancelled) {
          this.applyCleanupStatus(status);
        }
      } catch (err) {
        console.error('读取清理分析状态失败:', err);
      }
    },
    async openCleanupDialog() {
      try {
        const { status, current } = await this.readCleanupStatus();
        if (!current) return;
        // 上一轮被用户取消时不自动重跑：显示「已取消」，要不要重来由用户决定。
        if (status?.running || status?.completed || status?.cancelled) {
          this.applyCleanupStatus(status);
          return;
        }
        await this.startNewCleanupAnalysis();
      } catch (err) {
        console.error('获取清理候选失败:', err);
        this.cleanupDialog.error = '获取清理候选失败: ' + err;
        this.cleanupDialog.loading = false;
      }
    },
    async reanalyzeCleanupCandidates() {
      this.resetConsolidationReview();
      if (this.cleanupCategory === DISMISSED_CATEGORY) this.cleanupCategory = '';
      try {
        await this.startNewCleanupAnalysis();
      } catch (err) {
        console.error('重新分析清理候选失败:', err);
        this.cleanupDialog.error = '重新分析清理候选失败: ' + err;
        this.cleanupDialog.loading = false;
      }
    },
    async startNewCleanupAnalysis() {
      this.resetConsolidationReview();
      this.cleanupSkippedGroups = []; this.consolidationScope = [];
      const requestID = ++this.cleanupStatusRequestID;
      this.cleanupSelection = [];
      this.cleanupSelectionKey = null;
      this.cleanupKeepOverrides = {};
      this.cleanupCollapsedDirs = {};
      this.cleanupTrashedIDs = [];
      this.cleanupFocusedRelationID = 0;
      this.cleanupFocusNotice = '';
      this.cleanupDialog.loading = true;
      this.cleanupDialog.processing = false;
      this.cleanupDialog.cancelling = false;
      this.cleanupDialog.cancelled = false;
      this.cleanupDialog.analysis = null;
      this.cleanupDialog.error = '';
      this.cleanupDialog.stale = false;
      this.cleanupDialog.progress = { stage: 'load', message: '正在准备清理候选分析…', current: 0, total: 0, path: '' };
      this.startCleanupProgressTracking();
      // 「极短片段 / 极低分辨率」阈值由后端读设置（D-PC36），前端不再写死。
      const started = await StartCleanupAnalysisFromSettings();
      if (requestID === this.cleanupStatusRequestID) this.applyCleanupStatus(started);
    },
    // 取消只是发出请求：后台在两项之间检查，真正停下后经 cleanup-progress 的 done 事件回到 cancelled 状态。
    async cancelCleanupAnalysis() {
      if (this.cleanupDialog.cancelling) return;
      this.cleanupDialog.cancelling = true;
      try {
        await CancelCleanupAnalysis();
      } catch (err) {
        this.cleanupDialog.cancelling = false;
        notifyError('取消清理分析失败：' + err);
      }
    },
    getAllCleanupCandidates() {
      const analysis = this.cleanupDialog.analysis || {};
      const byID = new Map();
      for (const group of analysis.duplicate_groups || []) {
        if (group.original?.id) {
          byID.set(group.original.id, group.original);
        }
        for (const candidate of group.candidates || []) {
          byID.set(candidate.id, candidate);
        }
      }
      for (const group of analysis.near_duplicate_groups || []) {
        if (group.original?.id) {
          byID.set(group.original.id, group.original);
        }
        for (const candidate of group.candidates || []) {
          byID.set(candidate.id, candidate);
        }
      }
      for (const group of analysis.same_source_groups || []) {
        if (group.alternative?.id) {
          byID.set(group.alternative.id, group.alternative);
        }
      }
      for (const group of analysis.clip_groups || []) {
        if (group.clip?.id) {
          byID.set(group.clip.id, group.clip);
        }
      }
      for (const video of analysis.low_duration || []) {
        byID.set(video.id, video);
      }
      for (const video of analysis.low_resolution || []) {
        byID.set(video.id, video);
      }
      return Array.from(byID.values());
    },
    isCleanupSelected(videoID) {
      return this.cleanupSelection.includes(videoID);
    },
    toggleCleanupSelection(videoID) {
      if (!videoID || this.cleanupDialog.processing) return;
      if (this.isCleanupSelected(videoID)) {
        this.cleanupSelection = this.cleanupSelection.filter(id => id !== videoID);
        return;
      }
      // 保留项锁定（D-PC49）：界面上勾选框已禁用，这里再兜一次，免得键盘或脚本绕过去。
      if (this.isCleanupLocked(videoID) || this.cleanupTrashedIDs.includes(Number(videoID))) return;
      this.cleanupSelection = [...this.cleanupSelection, videoID];
    },
    // 选择操作条的「取消选择」（D-UD-10）：确认框打开着或删除进行中（processing）不动勾选。
    clearCleanupSelection() {
      if (this.cleanupDialog.processing) return;
      this.cleanupSelection = [];
    },
    // 改分组的请求（移出本组、不是重复、各类忽略、不是同源）统一走这里（P-032 复审 I-1）：
    // 等用户确认的这段时间里可能已经开始删除流程，确认之后再查一次；请求进行中计数，删除入口据此拦截。
    async runCleanupGroupRequest(confirmOptions, request) {
      if (this.cleanupDialog.processing) return;
      const generation = this.cleanupModeGeneration;
      const confirmed = await confirmAction(confirmOptions);
      if (!confirmed || this.cleanupDialog.processing || generation !== this.cleanupModeGeneration) return;
      this.cleanupGroupRequests++;
      try {
        await request();
      } finally {
        this.cleanupGroupRequests--;
      }
    },
    // 审阅重复候选要的是"两个文件摆一起看"，交给系统播放器比在应用内抽屉里
    // 一个个开更顺手；走 PreviewExternally 而不是 PlayVideo，免得审阅把播放次数刷上去。
    async previewCleanupVideo(video) {
      if (!video) return;
      try {
        await PreviewExternally(video.id);
      } catch (err) {
        console.error('外部预览失败:', err);
        notifyError('用系统播放器打开失败: ' + err);
      }
    },
    // 近似重复「不是重复」：整组两两配对记为忽略（同时否决这些对上待审的同源关系），忽略前先确认（D-PC31）。
    async dismissNearDuplicateGroup(group) {
      const ids = [group.original?.id, ...(group.candidates || []).map(video => video.id)].filter(Boolean);
      if (ids.length < 2) return;
      await this.runCleanupGroupRequest({
        title: '不是重复',
        message: `确认这组 ${ids.length} 个视频不是重复？\n之后的分析不再把它们报为近似重复，这些配对上待审的同源关系也一并判为「不是同源」。任一文件变化后忽略自动失效，也可以在「已忽略」里撤销。`,
        confirmText: '不是重复'
      }, async () => {
        try {
          await DismissNearDuplicateGroup(ids);
          this.cleanupStatusRequestID++;
          const analysis = this.cleanupDialog.analysis;
          if (analysis) {
            analysis.near_duplicate_groups = (analysis.near_duplicate_groups || [])
              .filter(item => ![item.original?.id, ...(item.candidates || []).map(video => video.id)].every(id => ids.includes(id)));
          }
          this.cleanupSelection = this.cleanupSelection.filter(id => !ids.includes(id));
          notifyUndoable('已记录“不是重复”，这组视频之间的配对不再作为近似重复候选。',
            () => this.undoDismissalFromToast('near_duplicate', groupPairKeys(ids)),
            { label: `清理：${ids.length} 个视频「不是重复」` });
          await this.refreshCleanupStatus();
        } catch (err) {
          notifyError('忽略近似重复组失败: ' + err);
        }
      });
    },
    // 「移出本组」：只否决这个成员与组内其他成员的配对，其余成员之间的关系不动（D-PC31）。
    async removeNearDuplicateMember(group, member) {
      const ids = [group.original?.id, ...(group.candidates || []).map(video => video.id)].filter(Boolean);
      const memberID = Number(member?.id);
      if (ids.length < 3 || !ids.includes(memberID)) return;
      await this.runCleanupGroupRequest({
        title: '移出本组',
        message: `把「${member.name || `视频 ${memberID}`}」移出本组？\n只记录它与组内其他 ${ids.length - 1} 个视频不是重复，其余成员之间的关系不变。可以在「已忽略」里撤销。`,
        confirmText: '移出本组'
      }, async () => {
        try {
          await DismissNearDuplicateMember(ids, memberID);
          this.cleanupStatusRequestID++;
          const analysis = this.cleanupDialog.analysis;
          const oldKey = `near-${group.original?.id}`;
          let newKey = oldKey;
          if (analysis) {
            analysis.near_duplicate_groups = (analysis.near_duplicate_groups || []).flatMap(item => {
              const members = [item.original, ...(item.candidates || [])].filter(Boolean);
              const memberIDs = members.map(video => video.id);
              if (memberIDs.length !== ids.length || !memberIDs.every(id => ids.includes(id))) return [item];
              const rest = members.filter(video => video.id !== memberID);
              if (rest.length < 2) return [];
              // 原片（组 key）按推荐顺序取 rest[0]，与后端拆组一致，回读后 key 不变；它已移到废纸篓时，
              // 保留项由 cleanupGroups 从仍在库的成员里接替（P-032 复审 I-a）。
              newKey = `near-${rest[0].id}`;
              return [{ ...item, original: rest[0], candidates: rest.slice(1) }];
            });
          }
          // 组 key 跟着原保留项走，移出原保留项后换成新原片的 key（P-032 复审 m1）。用户「设为保留」的那一份
          // 还在组里时，把覆盖搬到新 key 下，锁定随之保留；与图片清理页一致。
          this.cleanupKeepOverrides = this.migrateKeepOverride(oldKey, newKey, memberID);
          // 移出的是原保留项时 rest[0] 升为保留项：按新的组重新裁剪，把它移出勾选（P-032 评审 I-1）。
          this.cleanupSelection = pruneSelection(this.cleanupSelection.filter(id => id !== memberID), this.cleanupGroups, this.selectionOptions());
          notifyUndoable('已移出本组，其余成员仍在这一组里。',
            () => this.undoDismissalFromToast('near_duplicate', memberPairKeys(ids, memberID)),
            { label: `清理：「${member.name || `视频 ${memberID}`}」移出本组` });
          await this.refreshCleanupStatus();
        } catch (err) {
          notifyError('移出本组失败: ' + err);
        }
      });
    },
    // 「移出本组」之后的保留项覆盖：指向被移出成员的覆盖作废；组 key 变了就搬到新 key 下。
    migrateKeepOverride(oldKey, newKey, removedID) {
      const overrides = { ...this.cleanupKeepOverrides };
      if (!(oldKey in overrides)) return overrides;
      const override = Number(overrides[oldKey]);
      delete overrides[oldKey];
      if (override !== removedID) overrides[newKey] = override;
      return overrides;
    },
    // 截取片段的"忽略"：双方文件都不变时后续分析不再报出（D-028）。
    async dismissClipCandidate(group) {
      const fullID = group?.full?.id;
      const clipID = group?.clip?.id;
      if (!fullID || !clipID) return;
      await this.runCleanupGroupRequest({
        title: '忽略截取片段',
        message: '忽略这对截取片段候选？\n双方文件都不变时，之后的分析不再报出这一对。可以在「已忽略」里撤销。',
        confirmText: '忽略'
      }, async () => {
        try {
          await DismissClipCandidate(fullID, clipID);
          this.cleanupStatusRequestID++;
          const analysis = this.cleanupDialog.analysis;
          if (analysis) {
            analysis.clip_groups = (analysis.clip_groups || [])
              .filter(item => item.full?.id !== fullID || item.clip?.id !== clipID);
          }
          this.cleanupSelection = this.cleanupSelection.filter(id => id !== clipID);
          notifyUndoable('已记录忽略；双方文件未变时，这对截取候选不会再次出现。',
            () => this.undoDismissalFromToast('clip', [clipKey(fullID, clipID)]),
            { label: `清理：忽略截取片段「${group.clip?.name || `视频 ${clipID}`}」` });
          await this.refreshCleanupStatus();
        } catch (err) {
          notifyError('忽略截取片段失败: ' + err);
        }
      });
    },
    // 极短片段 / 极低分辨率也可以忽略（D-PC31、APP-11）：文件不变就不再报出，管理菜单的徽标随之消退。
    async dismissCleanupVideo(entry) {
      const video = entry?.keeper;
      const category = entry?.kind === 'low-duration' ? 'short' : entry?.kind === 'low-resolution' ? 'low' : '';
      if (!video?.id || !category) return;
      const label = category === 'short' ? '极短片段' : '极低分辨率';
      await this.runCleanupGroupRequest({
        title: `忽略${label}候选`,
        message: `忽略「${video.name || `视频 ${video.id}`}」这条${label}候选？\n文件不变时之后的分析不再报出它。可以在「已忽略」里撤销。`,
        confirmText: '忽略'
      }, async () => {
        try {
          await DismissCleanupVideo(video.id, category);
          this.cleanupStatusRequestID++;
          const analysis = this.cleanupDialog.analysis;
          if (analysis) {
            const key = category === 'short' ? 'low_duration' : 'low_resolution';
            analysis[key] = (analysis[key] || []).filter(item => item.id !== video.id);
          }
          // 同一个视频可能还在别的类别里，只有它不再是任何候选时才放掉勾选。
          const remaining = new Set(this.getAllCleanupCandidates().map(item => item.id));
          this.cleanupSelection = this.cleanupSelection.filter(id => remaining.has(id));
          notifyUndoable(`已忽略这条${label}候选。`,
            () => this.undoDismissalFromToast(category, [videoKey(video.id)]),
            { label: `清理：忽略${label}「${video.name || `视频 ${video.id}`}」` });
          await this.refreshCleanupStatus();
        } catch (err) {
          notifyError(`忽略${label}候选失败: ` + err);
        }
      });
    },
    async rejectCleanupSameSource(group) {
      if (!group?.relation_id) return;
      await this.runCleanupGroupRequest({
        title: '不是同源',
        message: '确认这两个视频不是同源？\n双方内容未变时不再作为相似关系候选，AI 同源审阅也不再询问这一对。这个判断不会出现在「已忽略」列表里。',
        confirmText: '不是同源'
      }, async () => {
        try {
          await RejectSameSourceRelation(group.relation_id);
          this.cleanupStatusRequestID++;
          const analysis = this.cleanupDialog.analysis;
          if (analysis) {
            analysis.same_source_groups = (analysis.same_source_groups || [])
              .filter(item => item.relation_id !== group.relation_id);
          }
          if (group.alternative?.id) {
            this.cleanupSelection = this.cleanupSelection.filter(id => id !== group.alternative.id);
          }
          this.$emit('same-source-rejected');
          notifySuccess('已记录“不是同源”，双方内容未变时不再作为相似关系候选。');
          await this.refreshCleanupStatus();
        } catch (err) {
          notifyError('更新同源判断失败: ' + err);
        }
      });
    },
    // 「已忽略」页签（D-PC31）：按类别分页列出忽略记录，可以逐条撤销。
    switchDismissalKind(kind) {
      if (this.dismissals.kind === kind && this.dismissals.loaded) return;
      this.dismissals = emptyDismissals(kind);
      this.loadCleanupDismissals(true);
    },
    async loadCleanupDismissals(reset) {
      const kind = this.dismissals.kind;
      if (reset) this.dismissals = emptyDismissals(kind);
      const cursor = reset ? 0 : this.dismissals.cursor;
      this.dismissals.loading = true;
      this.dismissals.error = '';
      try {
        const page = await ListCleanupDismissals(kind, cursor, DISMISSAL_PAGE_SIZE);
        // 读取期间切了类别：丢掉这份结果。
        if (this.dismissals.kind !== kind) return;
        const known = new Set(this.dismissals.items.map(item => item.id));
        this.dismissals.items = [...this.dismissals.items, ...(page?.items || []).filter(item => !known.has(item.id))];
        this.dismissals.cursor = Number(page?.next_cursor || 0);
        this.dismissals.hasMore = Boolean(page?.has_more);
        this.dismissals.loaded = true;
      } catch (err) {
        if (this.dismissals.kind === kind) this.dismissals.error = '读取忽略记录失败：' + err;
      } finally {
        if (this.dismissals.kind === kind) this.dismissals.loading = false;
      }
    },
    describeDismissal(item) {
      const name = media => {
        const text = String(media?.name || '').trim() || `视频 ${media?.id}`;
        return media?.missing ? `${text}（已删除）` : text;
      };
      const media = item?.media || [];
      if (item?.kind === 'clip' && media.length >= 2) return `片段「${name(media[1])}」 · 完整片「${name(media[0])}」`;
      if (media.length >= 2) return `「${name(media[0])}」与「${name(media[1])}」`;
      return media.length ? `「${name(media[0])}」` : `记录 ${item?.id}`;
    },
    formatDismissalTime(value) {
      const time = new Date(value).getTime();
      return Number.isFinite(time) && time > 0 ? `忽略于 ${new Date(time).toLocaleString()}` : '';
    },
    // 「不是重复」「移出本组」「忽略」之后提示与任务中心「最近操作」里的「撤销」（D-P3-06）：找回这一步写下的忽略记录
    // 并撤销（utils/cleanupDismissals.js）。已经在「已忽略」里撤销过的按撤销完成处理、说一声；失败抛出，记录留着可再试。
    // 撤销之后与「已忽略」里逐条撤销一样：重新分析后这些候选重新参与检测，已读过的「已忽略」列表重读。
    async undoDismissalFromToast(kind, keys) {
      const { removed } = await undoCleanupDismissalRecords(kind, keys);
      if (!removed) {
        notify('这条忽略已经撤销过了。');
        return;
      }
      notifySuccess('已撤销忽略。重新分析后会重新参与检测。');
      if (this._disposed) return;
      if (this.dismissals.loaded && this.dismissals.kind === kind) await this.loadCleanupDismissals(true);
      await this.refreshCleanupStatus();
    },
    async undoCleanupDismissal(item) {
      if (!item?.id || this.dismissals.undoing) return;
      const kind = this.dismissals.kind;
      this.dismissals.undoing = true;
      try {
        await UndoCleanupDismissals(kind, [item.id]);
        if (this.dismissals.kind === kind) {
          this.dismissals.items = this.dismissals.items.filter(row => row.id !== item.id);
        }
        notifySuccess('已撤销忽略。重新分析后会重新参与检测。');
        // 撤销后后端把缓存结果标为可能过期，回读一次让「结果可能已过期」跟上。
        await this.refreshCleanupStatus();
      } catch (err) {
        notifyError('撤销忽略失败：' + err);
      } finally {
        this.dismissals.undoing = false;
      }
    },
    // 删除前的汇总确认：返回 null 表示取消，否则 { merge }。plan 是合并计划，决定合并范围怎么说明。
    askCleanupDeleteConfirm(summary, plan) {
      if (this._resolveDeleteConfirm) this._resolveDeleteConfirm(null);
      this.deleteConfirm = {
        show: true,
        summary,
        mergeAvailable: plan.length > 0,
        mergeScope: { full: plan.some(item => item.kind !== 'clip'), clip: plan.some(item => item.kind === 'clip') },
        merge: true
      };
      return new Promise(resolve => {
        this._resolveDeleteConfirm = resolve;
      });
    },
    answerCleanupDelete(confirmed) {
      const resolve = this._resolveDeleteConfirm;
      this._resolveDeleteConfirm = null;
      const merge = this.deleteConfirm.mergeAvailable && this.deleteConfirm.merge;
      this.deleteConfirm = emptyDeleteConfirm();
      if (resolve) resolve(confirmed ? { merge } : null);
    },
    // 删除之前按组把被删项的整理成果合并到各组保留项（D-PC48），截取片段组不合并观看状态与字幕（§9.1）。
    // 任何一组失败都不进入删除：合并是幂等的并集，处理好之后再删一次即可。
    async mergeCleanupMetadata(plan) {
      const warnings = [];
      for (const [index, item] of plan.entries()) {
        try {
          const result = await MergeMediaMetadata('video', item.keeperId, item.sourceIds, item.options);
          warnings.push(...(result?.warnings || []));
        } catch (err) {
          // 前面几组已经合并，它们的提示（例如字幕复制失败）随失败提示一起给出；失败的那一组用保留项的文件名指明。
          const keeper = this.getAllCleanupCandidates().find(video => video.id === item.keeperId);
          notifyError(describeMergeFailure(err, index, plan.length, '视频', warnings, keeper?.name || `视频 ${item.keeperId}`));
          return null;
        }
      }
      return warnings;
    },
    async trashSelectedCleanupCandidates() {
      // 「移出本组」、各类忽略等改分组的请求还没返回时不删除（P-032 复审 I-1）：它们落地时会改组和勾选。
      if (this.cleanupDialog.processing || this.deleteConfirm.show || this.activeCleanupCategory === DISMISSED_CATEGORY || this.cleanupGroupRequests > 0) return;
      // 删除前按锁定规则裁剪勾选（与图片清理页同一做法，P-032 评审 I-1）：保留项、已移到废纸篓、
      // 不再出现在结果里的都不送进删除。合并计划遇到锁定项时报错，不静默跳过。
      this.cleanupDialog.processing = true;
      let confirmed;
      try {
        if (this.consolidationReviewTaskID) await this.revalidateConsolidationReview();
        confirmed = deletionPlan(this.cleanupGroups, this.cleanupSelection, this.selectionOptions());
      } catch (err) {
        this.cleanupDialog.processing = false;
        notifyError(err?.message || String(err));
        return;
      }
      const { ids: selectedIDs, plan } = confirmed;
      if (selectedIDs.length === 0) {
        this.cleanupDialog.processing = false;
        return;
      }
      const wanted = new Set(selectedIDs);
      const names = Object.fromEntries(this.getAllCleanupCandidates()
        .filter(video => wanted.has(video.id))
        .map(video => [video.id, video.name]));
      const summary = selectionSummary(this.cleanupGroups, selectedIDs, id => this.cleanupMemberSizes.get(id) || 0);

      // 从确认框打开起就进入删除流程（P-032 复审 I-1）：勾选、「设为保留」「移出本组」与各类忽略
      // 按删除进行中的同一组规则禁用和拦截，直到整个流程结束。
      this.cleanupDialog.processing = true;
      let trashAttempted = false;
      try {
        // 先汇总确认（D-PC49）；取消时不调用任何写入。
        const choice = await this.askCleanupDeleteConfirm(summary, plan);
        if (!choice) return;
        if (this.consolidationReviewTaskID) await this.revalidateConsolidationReview();
        // 确认期间分析结果仍可能被回读替换、改分组的请求也可能刚落地：按当前状态重算一次，
        // 与确认框里的名单不同就中止，不合并、不删除，勾选保持现在的样子。
        if (!deletionPlanUnchanged(confirmed, this.cleanupGroups, this.cleanupSelection, this.selectionOptions())) {
          notifyError(SELECTION_CHANGED_MESSAGE);
          return;
        }
        let mergeWarnings = [];
        if (choice.merge && plan.length > 0) {
          mergeWarnings = await this.mergeCleanupMetadata(plan);
          if (mergeWarnings === null) return;
        }
        trashAttempted = true;
        // 片库侧的动作分两步交回父组件，顺序与拆分前一致：先删除并把行摘出列表，
        // 当场收窄勾选（失败重试时才不会对着已进废纸篓的视频再删一次），
        // 然后才是撤销提示条与列表重载。
        const { result, failedIDs, succeededIDs } = await this.trashVideos(selectedIDs, { names });
        // 勾选改成失败项时按当前的组再裁一次：删除期间结果可能被回读替换过。
        this.cleanupSelection = pruneSelection(selectedIDs.filter(id => failedIDs.has(id)), this.cleanupGroups, this.selectionOptions());
        await this.afterTrashVideos(succeededIDs);
        // 已清理的项留在结果里，只标记结果可能过期；剩下的候选还能接着审阅。
        this.cleanupTrashedIDs = [...new Set([...this.cleanupTrashedIDs, ...succeededIDs])];
        if (result?.failed > 0) {
          const firstError = result.errors?.[0];
          notifyError(`批量清理完成：成功 ${result.succeeded} 个，失败 ${result.failed} 个。${firstError ? `\n首个失败：视频 ${firstError.video_id}，${firstError.error}` : ''}`);
        }
        if (mergeWarnings.length > 0) {
          notify(`元数据已合并，但有 ${mergeWarnings.length} 条提示：${mergeWarnings.join('；')}`, { timeout: 0 });
        }
        // 不再静默重跑：先问一句，让用户自己决定是继续审阅还是刷新候选。
        if (succeededIDs.length > 0 && await confirmAction({
          title: '重新分析清理候选',
          message: `已处理 ${succeededIDs.length} 个视频。是否立即重新分析？\n选择"取消"可以继续审阅当前结果。`,
          confirmText: '重新分析'
        })) {
          await this.reanalyzeCleanupCandidates();
        }
      } catch (err) {
        console.error('批量清理失败:', err);
        notifyError('批量清理失败: ' + err);
      } finally {
        this.cleanupDialog.processing = false;
        if (trashAttempted) this.$emit('trash-settled', selectedIDs);
      }
    },
    // 恢复了具体某个视频就只放它；拿不到 id 时整体清空，宁可少标也不要长期标错。
    forgetCleanupTrashed(videoID) {
      const id = Number(videoID);
      this.cleanupTrashedIDs = Number.isFinite(id) && id > 0
        ? this.cleanupTrashedIDs.filter(item => item !== id)
        : [];
    },
    formatDuration(seconds) {
      if (!seconds) return '';
      const h = Math.floor(seconds / 3600);
      const m = Math.floor((seconds % 3600) / 60);
      const s = Math.floor(seconds % 60);
      const parts = [];
      if (h > 0) parts.push(h.toString().padStart(2, '0'));
      parts.push(m.toString().padStart(2, '0'));
      parts.push(s.toString().padStart(2, '0'));
      return parts.join(':');
    },
    // 清理行的一句话摘要：名字 · 分辨率 · 时长 · 大小。留哪个删哪个首先是个体积问题，
    // 以前这一行偏偏没有大小，只能去看"预计释放"倒推。
    cleanupItemSummary(video) {
      const size = Number(video?.size || 0);
      return [
        video?.name,
        video?.resolution || '未知分辨率',
        this.formatDuration(video?.duration) || '00:00',
        size > 0 ? this.formatFileSize(size) : '',
      ].filter(Boolean).join(' · ');
    },
    formatFileSize(bytes) {
      const value = Number(bytes || 0);
      if (value <= 0) return '0 B';
      const units = ['B', 'KB', 'MB', 'GB', 'TB'];
      const index = Math.min(units.length - 1, Math.floor(Math.log(value) / Math.log(1024)));
      const scaled = value / Math.pow(1024, index);
      return `${scaled.toFixed(scaled >= 10 || index === 0 ? 0 : 1)} ${units[index]}`;
    },
  }
};
</script>

<style scoped>
/* 清理中心的视频标签（D-P3-02）：统计卡 → 当前类别的标题行 → 左目录右候选流 → 吸底的选择操作条。
   页面整体随 .main-view 滚动；目录栏吸顶，自己滚。 */
/* 正文字号与颜色沿用原弹窗正文（DialogShell 的 --fs-body）：卡片里的「建议保留」「判断」等不跟着页面默认字号变大。 */
.cleanup-review { display: flex; flex-direction: column; gap: var(--space-3); color: var(--text-primary); font-size: var(--fs-body); }
.cleanup-muted { color: var(--text-muted); font-size: var(--fs-caption); }

/* 分类统计卡（共用 StatTile，与图片标签一致）：前四张是分类，最后一张「已忽略」。 */
.cleanup-tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(176px, 1fr)); gap: var(--space-3); }
/* 没有结果时只有「已忽略」一张：保持卡片宽度，不铺满整行。 */
.cleanup-tiles--single { grid-template-columns: minmax(176px, 264px); }
/* 类别上的覆盖度（D-PC50）：「尚未计算」「已算 X / Y」。 */
.cleanup-tile__coverage { color: var(--warning-text); font-size: var(--fs-caption); }

/* 分析中、首屏读取：居中的一块状态。 */
.cleanup-state { display: grid; justify-items: center; gap: var(--space-2); padding: var(--space-7) var(--space-2); color: var(--text-secondary); font-size: var(--fs-secondary); text-align: center; }
.cleanup-state > .ui-skeleton { width: min(520px, 100%); }
.cleanup-state__title { margin: 0; color: var(--text-primary); font-size: var(--fs-body); font-weight: var(--fw-semibold); }
.cleanup-state p { margin: 0; }
.cleanup-progress-bar { width: min(420px, 100%); text-align: left; }
.cleanup-progress-meta,
.cleanup-progress-hint,
.cleanup-progress-path { color: var(--text-secondary); font-size: var(--fs-secondary); }
.cleanup-progress-hint { max-width: 560px; color: var(--text-muted); font-size: var(--fs-caption); }
.cleanup-progress-path { max-width: 560px; color: var(--text-muted); font-size: var(--fs-caption); word-break: break-all; }
.cleanup-state .cleanup-progress-actions { margin-top: var(--space-2); }

.cleanup-body { display: flex; flex-direction: column; gap: var(--space-3); }

/* 当前类别的标题行：标题、候选数与可释放空间、「集中整理」。 */
.cleanup-area__bar { display: flex; align-items: center; flex-wrap: wrap; gap: var(--space-2) var(--space-3); padding: var(--space-1) 2px 0; }
.cleanup-area__title { margin: 0; font-size: var(--fs-section); font-weight: var(--fw-bold); }
.cleanup-area__stat { color: var(--text-muted); font-size: var(--fs-caption); }
.cleanup-area__stat b { color: var(--text-primary); font-family: var(--font-mono); }
.cleanup-area__spacer { flex: 1; }

/* 左侧目录承担分组（原型 A5），吸顶、自己滚；右侧是当前目录的候选流，随页面滚。 */
.cleanup-split { display: grid; grid-template-columns: 268px minmax(0, 1fr); align-items: start; gap: var(--space-4); }
.cleanup-dirs {
  position: sticky;
  top: var(--space-3);
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: calc(100vh - 140px);
  padding: 10px 8px;
  border: 1px solid var(--hairline);
  border-radius: var(--radius-lg);
  background: var(--panel-subtle-bg);
  overflow-y: auto;
}
.cleanup-dirs__heading { padding: 6px 10px 4px; color: var(--text-muted); font-size: var(--fs-caption); font-weight: var(--fw-bold); letter-spacing: 0.08em; }
.cleanup-dirs__item { display: flex; align-items: center; gap: 8px; padding: 7px 10px; border: 0; border-radius: var(--radius-sm); background: transparent; color: var(--text-secondary); font-size: var(--fs-secondary); text-align: left; cursor: pointer; }
.cleanup-dirs__item.active { background: var(--accent-soft); color: var(--accent-text); font-weight: var(--fw-semibold); }
.cleanup-dirs__item:hover:not(:disabled) { text-decoration: none; background: var(--control-hover-bg); }
.cleanup-dirs__item.active:hover:not(:disabled) { background: var(--accent-soft); }
.cleanup-dirs__path { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cleanup-dirs__count { flex: none; font-family: var(--font-mono); }
.cleanup-dirs__selected { flex: none; padding: 0 6px; border: 1px solid var(--accent-color); border-radius: var(--radius-pill); color: var(--accent-text); font-size: var(--fs-caption); white-space: nowrap; }
.cleanup-dirs__note { margin: var(--space-2) 0 0; padding: 10px; border-top: 1px solid var(--hairline-soft); color: var(--text-muted); font-size: var(--fs-caption); line-height: 1.6; }
.cleanup-pager { display: flex; align-items: center; gap: var(--space-2); color: var(--text-secondary); font-size: var(--fs-caption); }

.cleanup-stream { min-width: 0; }
.cleanup-section__head { display: flex; align-items: baseline; flex-wrap: wrap; gap: 10px; padding: 2px 0 8px; border-bottom: 1px solid var(--hairline-faint); margin-bottom: 10px; }
.cleanup-section__head strong { min-width: 0; font-size: var(--fs-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cleanup-section__head span { color: var(--text-muted); font-size: var(--fs-caption); }

/* 候选卡 */
.cleanup-card {
  padding: 12px 14px;
  border: 1px solid var(--review-border-color);
  border-radius: var(--radius);
  margin-top: 10px;
  background: var(--review-subtle-bg);
}
.cleanup-card--focused { border-color: var(--accent-border); box-shadow: 0 0 0 2px var(--accent-soft); }
.cleanup-card-kind { margin-bottom: 6px; font-size: var(--fs-caption); font-weight: var(--fw-bold); color: var(--review-text-strong); opacity: 0.8; }
.cleanup-card p,
.cleanup-card ul { margin: 6px 0; }
.cleanup-card-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin: 2px 0 4px; }
.cleanup-card-head .btn-link { flex: none; font-size: var(--fs-caption); }
.cleanup-card-reason { color: var(--review-text-secondary); font-size: var(--fs-secondary); }
.cleanup-card-exhausted { margin: 0 0 4px; color: var(--warning-text); font-size: var(--fs-caption); }
.cleanup-header__spacer { flex: 1; }
.cleanup-consolidation-scope { display: flex; flex-wrap: wrap; gap: var(--space-3); margin-bottom: var(--space-1); color: var(--text-secondary); font-size: var(--fs-caption); }
.cleanup-consolidation-scope label { display: inline-flex; align-items: center; gap: var(--space-1); cursor: pointer; }
.cleanup-member-list { list-style: none; padding: 0; }
.cleanup-select-row {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-width: 0;
  padding: 8px 0;
  border-top: 1px solid var(--neutral-softer);
}
.cleanup-select-row--original,
.cleanup-select-row--keeper { border-top: 0; }
.cleanup-select-row--original { padding-top: 0; }
.cleanup-select-row--trashed { opacity: 0.45; text-decoration: line-through; }
.cleanup-item-text { display: flex; flex-direction: column; flex: 1 1 auto; min-width: 0; gap: 2px; }
.cleanup-item-main { min-width: 0; color: var(--review-text-strong); font-size: var(--fs-secondary); font-weight: var(--fw-semibold); overflow-wrap: anywhere; }
.cleanup-item-path { font-size: var(--fs-caption); color: var(--review-text-path); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cleanup-item-otherdir { display: block; font-size: var(--fs-caption); font-family: var(--font-mono); opacity: 0.7; }
.cleanup-item-locked { color: var(--warning-text); font-size: var(--fs-caption); }
.cleanup-item-actions { display: flex; flex-wrap: wrap; gap: 6px; flex: 0 0 auto; }
.cleanup-keeper-label { display: inline-flex; align-items: center; justify-content: center; flex: none; min-width: 68px; padding: 0 10px; font-size: var(--fs-caption); color: var(--accent-text); white-space: nowrap; }
/* 整理成果标记（D-PC48）：保留建议优先留整理成果多的那份。 */
.cleanup-curation { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 2px; }
.cleanup-curation__item { padding: 0 6px; border: 1px solid var(--accent-border); border-radius: var(--radius-pill); background: var(--accent-soft); color: var(--accent-text); font-size: var(--fs-caption); line-height: 16px; white-space: nowrap; }
.cleanup-confirmed-badge {
  display: inline-block;
  margin-left: 8px;
  padding: 0 6px;
  border-radius: var(--radius-pill);
  font-size: var(--fs-caption);
  line-height: 18px;
  color: var(--success-color, var(--accent-color));
  border: 1px solid currentColor;
}
/* 截取片段：A / B 并排，窄屏下退回上下两行。判断依据在下方那行 <p> 里。 */
.cleanup-clip-pair { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 10px; }
.cleanup-clip-side { min-width: 0; padding: 8px; border: 1px solid var(--review-border-color); border-radius: var(--radius); background: var(--review-solid-bg); }
.cleanup-clip-side__head { margin-bottom: 6px; font-size: var(--fs-caption); font-weight: var(--fw-bold); color: var(--review-text-strong); opacity: 0.8; }

/* 「已忽略」 */
.cleanup-dismissed { display: grid; gap: 10px; }
.cleanup-dismissed__kinds { display: flex; flex-wrap: wrap; gap: 8px; }
.cleanup-dismissed__hint { margin: 0; color: var(--review-text-secondary); font-size: var(--fs-caption); }
.cleanup-dismissed__loading { display: grid; gap: var(--space-2); }
.cleanup-dismissed__list { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
.cleanup-dismissed__item { display: flex; align-items: center; gap: 10px; padding: 8px 10px; border: 1px solid var(--review-border-color); border-radius: var(--radius); background: var(--review-subtle-bg); }
.cleanup-dismissed__media { flex: 1; min-width: 0; color: var(--review-text-strong); font-size: var(--fs-secondary); overflow-wrap: anywhere; }
.cleanup-dismissed__time { flex: none; color: var(--text-muted); font-size: var(--fs-caption); }
.cleanup-dismissed__more { justify-self: center; }

/* 选择操作条上的说明：体积与「可撤销」。 */
.cleanup-footer__summary { color: var(--text-secondary); font-size: var(--fs-caption); }
.cleanup-footer__summary b { color: var(--text-primary); font-family: var(--font-mono); }

/* 删除前的汇总确认 */
:deep(.cleanup-delete-confirm) { max-width: 520px; }
.cleanup-delete-confirm p { margin: 0 0 var(--space-2); }
.cleanup-delete-confirm__warn { color: var(--warning-text); }
.cleanup-delete-confirm__merge { display: flex; align-items: center; gap: 8px; margin-top: 12px; font-weight: var(--fw-semibold); cursor: pointer; }
.cleanup-delete-confirm__help { margin: 6px 0 0; color: var(--text-muted); font-size: var(--fs-caption); }

@media (max-width: 760px) {
  .cleanup-split { grid-template-columns: minmax(0, 1fr); }
  .cleanup-dirs { position: static; max-height: 240px; }
}
</style>
