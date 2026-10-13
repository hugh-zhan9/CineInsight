<template>
  <section class="cleanup-page" data-test="photo-cleanup-page">
    <div class="cleanup-page__nav">
      <!-- 删除进行中不能离开（P-032 评审 Minor 5）：「不支持废纸篓」二选一挂在本页上。 -->
      <button
        type="button"
        class="btn btn--ghost btn--sm"
        data-test="cleanup-back"
        :disabled="processing"
        :title="processing ? '正在移到废纸篓，完成后再返回' : ''"
        @click="$emit('close')"
      ><AppIcon name="chevron-left" :size="16" />返回图片库</button>
    </div>

    <!-- 勾选规则与视频清理面板同一套（D-PC49，utils/cleanupSelection.js）。 -->
    <PageHeader
      class="cleanup-page__header"
      title="清理审阅"
      subtitle="精确重复默认勾选保留项以外的副本；疑似重复是画面近似，默认不勾，看过之后按组或按页「按建议勾选」。保留项锁定，要换就点另一张的「设为保留」。勾选的图片移到废纸篓，可在回收站撤销；不勾任何一张的组不会有任何改动。"
    >
      <template #actions>
        <button
          type="button"
          class="btn btn--secondary"
          data-test="cleanup-dismissed-toggle"
          @click="toggleDismissed"
        >{{ showDismissed ? '返回审阅' : '已忽略' }}</button>
        <button
          type="button"
          :class="['btn', analysis ? 'btn--secondary' : 'btn--primary']"
          :disabled="running || editLocked"
          data-test="cleanup-start"
          @click="startAnalysis"
        >{{ running ? '分析中…' : (analysis ? '重新分析' : '开始分析') }}</button>
      </template>
    </PageHeader>

    <!-- 删除走 P-030 的删除宿主：进度与取消、磁盘不支持废纸篓时的二选一、按批次撤销。 -->
    <TrashUndoBanner ref="trashUndo" kind="image" :after-restore="afterTrashRestore" />

    <AppBanner v-if="displayError" tone="danger" role="alert" data-test="cleanup-error" :title="displayError" />
    <AppBanner
      v-if="resultStale"
      tone="warning"
      data-test="cleanup-outdated-hint"
      title="图片库在本次分析之后发生过变化，下面的结果可能已过期。"
      message="可以继续审阅，也可以点「重新分析」刷新候选。"
    />
    <AppBanner
      v-if="analysis && analysis.stale_hash_count > 0"
      data-test="cleanup-stale-hint"
      :title="`有 ${analysis.stale_hash_count} 张图片还没有可用的指纹（未回填或源文件已变更），暂未参与近似重复检测。`"
    >
      <template #actions>
        <button
          type="button"
          class="btn btn--secondary btn--sm"
          data-test="cleanup-start-image-phash"
          :disabled="perceptualHashRunning"
          @click="startPerceptualHashBackfill"
        >{{ perceptualHashRunning ? '补全中…' : '补全指纹' }}</button>
      </template>
    </AppBanner>
    <AppBanner
      v-if="analysis && analysis.skipped_unavailable > 0"
      data-test="image-cleanup-skipped-hint"
      :title="`本轮跳过 ${analysis.skipped_unavailable} 张图片（文件不可访问，如外置盘未挂载）。`"
    />
    <AppBanner
      v-if="analysis?.skipped_verification > 0"
      data-test="image-cleanup-verification-hint"
      :title="`${analysis.skipped_verification} 张图片的画面未能复核（读取失败或文件已变化），未据此判定近似重复。`"
      message="检查文件后可重新分析。"
    />

    <!-- 「已忽略」（D-PC31）：「不是重复」「移出本组」留下的记录，可以撤销。 -->
    <section v-if="showDismissed" class="cleanup-page__dismissed" data-test="cleanup-dismissed">
      <p class="cleanup-page__muted">
        「不是重复」和「移出本组」留下的记录。撤销后这些图片在下次分析时重新参与近似重复检测；任一文件变化后忽略本来就会自动失效。
      </p>
      <AppBanner v-if="dismissals.error" tone="danger" role="alert" data-test="cleanup-dismissed-error" :title="dismissals.error" />
      <div v-if="dismissals.loading && dismissals.items.length === 0" class="cleanup-page__loading" role="status">
        <SkeletonBlock :lines="3" />
        <span class="cleanup-page__muted">正在读取忽略记录…</span>
      </div>
      <EmptyState
        v-else-if="dismissals.items.length === 0 && dismissals.loaded"
        data-test="cleanup-dismissed-empty"
        title="还没有忽略记录。"
        message="在疑似重复里点「不是重复」或「移出本组」，记录会出现在这里，可以随时撤销。"
      >
        <template #actions>
          <button type="button" class="btn btn--secondary btn--sm" @click="toggleDismissed">返回审阅</button>
        </template>
      </EmptyState>
      <ul v-else-if="dismissals.items.length > 0" class="cleanup-page__dismissed-list">
        <li v-for="item in dismissals.items" :key="item.id" class="cleanup-page__dismissed-item" data-test="cleanup-dismissal-item">
          <span class="cleanup-page__dismissed-media">{{ describeDismissal(item) }}</span>
          <span class="cleanup-page__muted">{{ formatDismissalTime(item.created_at) }}</span>
          <button
            type="button"
            class="btn btn--secondary btn--sm"
            data-test="cleanup-dismissal-undo"
            :disabled="dismissals.undoing"
            @click="undoDismissal(item)"
          >撤销</button>
        </li>
      </ul>
      <button
        v-if="dismissals.hasMore"
        type="button"
        class="btn btn--secondary btn--sm cleanup-page__more"
        data-test="cleanup-dismissed-more"
        :disabled="dismissals.loading"
        @click="loadDismissals(false)"
      >{{ dismissals.loading ? '读取中…' : '加载更多' }}</button>
    </section>

    <!-- 分析是本页正在做的那件事：页内只放这一条进度（D-UD-11）。 -->
    <div v-else-if="running" class="cleanup-page__state cleanup-page__progress" data-test="cleanup-progress">
      <p class="cleanup-page__state-title">正在分析 · {{ stageLabel }}</p>
      <ProgressBar
        :value="progress.total > 0 ? progress.current / progress.total : null"
        :label="progress.total > 0 ? `已处理 ${progress.current} / ${progress.total}` : '准备中'"
      />
      <p v-if="progress.message" class="cleanup-page__muted">{{ progress.message }}</p>
      <p v-if="progress.path" class="cleanup-page__path">当前文件：{{ progress.path }}</p>
      <p class="cleanup-page__muted">分析会逐个读取图片文件，外置硬盘或大库场景耗时较长。可以直接返回图片库，分析会在后台继续。</p>
      <p>
        <button
          type="button"
          class="btn btn--secondary btn--sm"
          data-test="cleanup-cancel-analysis"
          :disabled="cancelling"
          @click="cancelAnalysis"
        >{{ cancelling ? '正在取消…' : '取消分析' }}</button>
      </p>
    </div>

    <template v-else-if="analysis">
      <!-- 精确重复与疑似重复分成两个区域：点统计卡切换，各自按目录分组、各自翻页。 -->
      <div v-if="hasGroups" class="cleanup-tiles" role="tablist" aria-label="清理候选类别" data-test="cleanup-tiles">
        <StatTile
          v-for="tile in kindTiles"
          :key="tile.kind"
          tag="button"
          role="tab"
          :selected="activeKind === tile.kind"
          :aria-selected="activeKind === tile.kind"
          :icon="tile.icon"
          :value="tile.groups"
          unit="组"
          :label="tile.label"
          :note="`${tile.images} 张 · 已勾 ${tile.selected} 张`"
          :hint="tile.hint"
          :data-test="`cleanup-kind-${tile.kind}`"
          @click="setKind(tile.kind)"
        />
        <StatTile
          tag="div"
          tone="danger"
          :icon="TILE_ICONS.pending"
          :value="selection.length"
          unit="张"
          label="待删"
          :note="`合计 ${formatBytes(reclaimableBytes)}`"
          hint="两个区域的勾选合在一起删"
          data-test="cleanup-tile-pending"
        />
      </div>

      <div class="cleanup-page__toolbar">
        <span v-if="coverageText" class="cleanup-page__coverage" data-test="cleanup-coverage">{{ coverageText }}</span>
        <label class="cleanup-page__switch" data-test="cleanup-samedir-switch">
          <input type="checkbox" :checked="sameDirOnly" :disabled="editLocked" @change="toggleSameDirOnly" />
          只勾选与保留项同目录的副本
        </label>
        <span class="cleanup-page__hint">跨目录的副本可能是你的备份；不想让整个目录参与审阅，把它加进图片扫描黑名单。</span>
      </div>

      <!-- 空态要分得清「没有重复」与「还没算」（D-PC50）。 -->
      <EmptyState
        v-if="!hasGroups"
        data-test="cleanup-empty"
        icon="check"
        :title="emptyState.title"
        :message="emptyState.detail"
      >
        <template v-if="emptyState.action" #actions>
          <button
            type="button"
            class="btn btn--secondary btn--sm"
            data-test="cleanup-empty-start-phash"
            :disabled="perceptualHashRunning"
            @click="startPerceptualHashBackfill"
          >{{ perceptualHashRunning ? '补全中…' : '补全指纹' }}</button>
        </template>
      </EmptyState>

      <div v-if="hasGroups" ref="areaTop" class="cleanup-area__bar" data-test="cleanup-area-bar">
        <h3 class="cleanup-area__title">{{ KIND_LABELS[activeKind] }}</h3>
        <span class="cleanup-area__stat">{{ activeEntries.length }} 组 · {{ directorySections.length }} 个目录</span>
        <nav v-if="pageCount > 1" class="cleanup-area__pager" aria-label="重复组分页">
          <button type="button" class="btn btn--secondary btn--sm" data-test="cleanup-page-prev" :disabled="currentPage <= 1" @click="setPage(currentPage - 1)">上一页</button>
          <span>第 {{ currentPage }} / {{ pageCount }} 页（勾选跨页保留）</span>
          <button type="button" class="btn btn--secondary btn--sm" data-test="cleanup-page-next" :disabled="currentPage >= pageCount" @click="setPage(currentPage + 1)">下一页</button>
        </nav>
        <span class="cleanup-group__spacer"></span>
        <!-- 只作用于本页展开着的组：看得到的才勾，疑似重复默认仍然不勾（D-PC49）。 -->
        <button
          v-if="activeEntries.length"
          type="button"
          class="btn btn--secondary btn--sm"
          :disabled="editLocked || (!pageFullySuggested && pageSuggestableEntries.length === 0)"
          title="只作用于本页展开着的组；「本组不删」的组和被别的组锁定的图片不会勾上"
          data-test="cleanup-suggest-page"
          @click="togglePageSuggestion"
        >{{ pageFullySuggested ? '取消本页勾选' : '本页按建议勾选' }}</button>
        <button v-if="activeEntries.length" type="button" class="btn btn--ghost btn--sm" data-test="cleanup-toggle-all" @click="toggleAllDirectories">
          {{ allCollapsed ? '全部展开' : '全部折叠' }}
        </button>
      </div>

      <EmptyState
        v-if="hasGroups && !activeEntries.length"
        data-test="cleanup-area-empty"
        icon="check"
        :title="areaEmptyState.title"
        :message="areaEmptyState.detail"
      >
        <template v-if="areaEmptyState.action" #actions>
          <button
            type="button"
            class="btn btn--secondary btn--sm"
            data-test="cleanup-area-start-phash"
            :disabled="perceptualHashRunning"
            @click="startPerceptualHashBackfill"
          >{{ perceptualHashRunning ? '补全中…' : '补全指纹' }}</button>
        </template>
      </EmptyState>

      <section
        v-for="section in pagedDirectorySections"
        :key="section.directory"
        class="cleanup-dir"
        data-test="cleanup-dir-section"
      >
        <div class="cleanup-dir__head">
          <button
            type="button"
            class="btn-link cleanup-dir__toggle"
            :title="section.directory"
            :aria-expanded="!isDirCollapsed(section.directory)"
            data-test="cleanup-dir-toggle"
            @click="toggleDir(section.directory)"
          >
            <AppIcon class="cleanup-dir__chevron" :name="isDirCollapsed(section.directory) ? 'chevron-right' : 'chevron-down'" :size="16" />
            <span class="cleanup-dir__path">{{ section.directory }}</span>
            <span class="cleanup-dir__count">{{ section.groupCount }} 组 · 共 {{ section.imageCount }} 张</span>
          </button>
          <button
            v-if="section.directory !== UNKNOWN_DIRECTORY"
            type="button"
            class="btn btn--secondary btn--sm"
            data-test="cleanup-open-dir"
            @click="openDirectory(section.directory)"
          >在访达中显示</button>
        </div>

        <template v-if="!isDirCollapsed(section.directory)">
          <article
            v-for="entry in section.entries"
            :key="entry.key"
            class="cleanup-group glass-surface"
            :class="{ 'cleanup-group--skipped': isSkipped(entry) }"
            data-test="cleanup-group-card"
            :data-kind="entry.kind"
          >
            <div class="cleanup-group__head">
              <span class="cleanup-group__kind" :class="`cleanup-group__kind--${entry.kind}`">
                {{ entry.kind === 'near' ? '近似重复（默认不勾）' : '精确重复' }}
              </span>
              <span class="cleanup-group__reason">{{ entry.group.reason }}</span>
              <span v-if="entry.kind === 'near'" class="cleanup-group__similarity" data-test="cleanup-similarity">
                {{ similarityLabel(entry) }}
              </span>
              <span v-if="entry.spansDirectories" class="cleanup-group__warn" data-test="cleanup-cross-dir">跨 {{ entry.directoryCount }} 个目录</span>
              <!-- "设为保留"只是对比基准，不会连带删除别的；真正决定生死的是"删除"复选框。
                   一个都没勾时明说本组不会有任何改动，免得用户以为没勾的会被自动清掉。 -->
              <span class="cleanup-group__outcome" data-test="cleanup-group-outcome">{{ groupOutcomeLabel(entry) }}</span>
              <span class="cleanup-group__spacer"></span>
              <button
                type="button"
                class="btn btn--secondary btn--sm"
                :disabled="editLocked || isSkipped(entry) || (!isGroupFullySuggested(entry) && suggestedIDsFor(entry).length === 0)"
                data-test="cleanup-suggest-group"
                @click="toggleGroupSuggestion(entry)"
              >{{ isGroupFullySuggested(entry) ? '取消本组勾选' : '按建议勾选（保留推荐项）' }}</button>
              <button
                type="button"
                class="btn btn--secondary btn--sm"
                :class="{ 'cleanup-group__skip--active': isSkipped(entry) }"
                :aria-pressed="isSkipped(entry) ? 'true' : 'false'"
                :disabled="editLocked"
                data-test="cleanup-skip-group"
                @click="toggleSkipGroup(entry)"
              >{{ isSkipped(entry) ? '已跳过（点我恢复）' : '本组不删' }}</button>
              <button
                v-if="entry.kind === 'near'"
                type="button"
                class="btn btn--secondary btn--sm"
                :disabled="dismissing || editLocked"
                data-test="cleanup-dismiss-group"
                @click="dismissGroup(entry)"
              >不是重复</button>
            </div>

            <p v-if="isExhausted(entry)" class="cleanup-group__exhausted" data-test="cleanup-group-exhausted">本组剩下的都已移到废纸篓，没有可保留的一张，这一组不参与合并和删除。</p>

            <div class="cleanup-group__members">
              <div
                v-for="member in visibleMembers(entry)"
                :key="member.id"
                class="cleanup-member"
                :class="{
                  'cleanup-member--keep': isKept(entry, member),
                  'cleanup-member--marked': selectedIDs.has(Number(member.id)),
                  'cleanup-member--deleted': isDeleted(member)
                }"
                data-test="cleanup-member"
              >
                <button
                  type="button"
                  class="btn-link cleanup-member__thumb"
                  :title="`查看原图 · ${member.name}`"
                  data-test="cleanup-member-open"
                  @click="openViewer(entry, member)"
                >
                  <img :src="`/preview/image-thumbnail/${member.id}`" :alt="member.name" loading="lazy" />
                  <span v-if="isDeleted(member)" class="cleanup-member__deleted-flag" data-test="cleanup-member-deleted">已删除</span>
                </button>

                <div class="cleanup-member__choice">
                  <label class="cleanup-member__radio">
                    <input
                      type="radio"
                      :name="`keep-${entry.key}`"
                      :checked="isKept(entry, member)"
                      :disabled="editLocked || isDeleted(member)"
                      :aria-label="`保留 ${member.name}`"
                      data-test="cleanup-keep-toggle"
                      @change="setKeep(entry, member)"
                    />
                    设为保留
                  </label>
                  <label class="cleanup-member__check">
                    <input
                      type="checkbox"
                      :checked="selectedIDs.has(Number(member.id))"
                      :disabled="editLocked || isKept(entry, member) || isSkipped(entry) || isDeleted(member) || isProtected(entry, member)"
                      :aria-label="`删除 ${member.name}`"
                      data-test="cleanup-candidate-toggle"
                      @change="toggleSelection(member.id)"
                    />
                    删除
                    <span v-if="isProtected(entry, member)" class="cleanup-member__locked" data-test="cleanup-member-locked">（{{ lockReason(entry, member) }}）</span>
                  </label>
                </div>

                <p class="cleanup-member__name" :title="member.name">{{ member.name }}</p>

                <dl class="cleanup-member__facts">
                  <dt>分辨率</dt>
                  <dd>
                    {{ member.width && member.height ? `${member.width}×${member.height}` : '未探测' }}
                    <em v-if="diffFor(entry, member).resolution" class="cleanup-member__diff">{{ diffFor(entry, member).resolution }}</em>
                  </dd>
                  <dt>大小</dt>
                  <dd>
                    {{ formatBytes(memberBytes(member)) }}
                    <em v-if="diffFor(entry, member).size" class="cleanup-member__diff">{{ diffFor(entry, member).size }}</em>
                  </dd>
                  <dt>{{ member.taken_at ? '拍摄' : '修改' }}</dt>
                  <dd>
                    {{ memberTimeLabel(member) }}
                    <em v-if="diffFor(entry, member).time" class="cleanup-member__diff">{{ diffFor(entry, member).time }}</em>
                  </dd>
                  <dt>目录</dt>
                  <dd>
                    <span v-if="directoryOf(member) === section.directory" class="cleanup-member__samedir">本目录</span>
                    <span v-else class="cleanup-member__otherdir" :title="directoryOf(member)" data-test="cleanup-member-otherdir">
                      <AppIcon name="warning" :size="16" />{{ directoryOf(member) }}
                    </span>
                  </dd>
                </dl>

                <p class="cleanup-member__path" :title="member.path" data-test="cleanup-member-path" @click="copyPath(member)">{{ member.path }}</p>

                <!-- 整理成果（D-PC48）：保留建议优先留收藏、评分、人物、标签多的那张。 -->
                <div class="cleanup-member__meta">
                  <span v-if="member.is_favorite" class="cleanup-member__badge cleanup-member__badge--keepish" data-test="cleanup-member-favorite"><AppIcon name="star" :size="16" />已收藏</span>
                  <span v-if="member.personal_rating != null" class="cleanup-member__badge cleanup-member__badge--keepish">评分 {{ member.personal_rating }}</span>
                  <span v-if="member.curation?.people" class="cleanup-member__badge cleanup-member__badge--keepish" data-test="cleanup-member-people">人物</span>
                  <span
                    v-for="tag in member.tags || []"
                    :key="tag.id"
                    class="cleanup-member__badge cleanup-member__badge--tag"
                    :style="{ '--tag-color': tag.color }"
                    data-test="cleanup-member-tag"
                  >{{ tag.name }}</span>
                  <span v-if="!hasMetadata(member)" class="cleanup-member__badge cleanup-member__badge--empty">无收藏 / 评分 / 人物 / 标签</span>
                </div>

                <div class="cleanup-member__actions">
                  <button
                    type="button"
                    class="btn btn--secondary btn--sm"
                    :disabled="isDeleted(member)"
                    data-test="cleanup-reveal"
                    @click="revealMember(member)"
                  >在访达中显示</button>
                  <button
                    v-if="entry.kind === 'near' && entry.members.length > 2 && !isDeleted(member)"
                    type="button"
                    class="btn btn--secondary btn--sm"
                    :disabled="dismissing || editLocked"
                    data-test="cleanup-remove-member"
                    @click="removeMember(entry, member)"
                  >移出本组</button>
                </div>
              </div>
            </div>
            <nav v-if="entry.members.length > MEMBER_PAGE_SIZE" class="cleanup-page__pagination" aria-label="组内图片分页">
              <button type="button" class="btn btn--secondary btn--sm" data-test="cleanup-members-prev" :disabled="memberPage(entry) <= 1" @click="review.memberPages[entry.key] = memberPage(entry) - 1">上一批图片</button>
              <span>第 {{ memberPage(entry) }} / {{ memberPageCount(entry) }} 页 · 本组共 {{ entry.members.length }} 张</span>
              <button type="button" class="btn btn--secondary btn--sm" data-test="cleanup-members-next" :disabled="memberPage(entry) >= memberPageCount(entry)" @click="review.memberPages[entry.key] = memberPage(entry) + 1">下一批图片</button>
            </nav>
          </article>
        </template>
      </section>

      <!-- 一页 20 组，翻到底不用再滚回顶上翻页；翻页后回到本区顶部。 -->
      <nav v-if="hasGroups && pageCount > 1" class="cleanup-page__pagination" aria-label="重复组分页（底部）" data-test="cleanup-pagination-bottom">
        <button type="button" class="btn btn--secondary btn--sm" data-test="cleanup-page-prev-bottom" :disabled="currentPage <= 1" @click="setPage(currentPage - 1, true)">上一页</button>
        <span>第 {{ currentPage }} / {{ pageCount }} 页 · {{ KIND_LABELS[activeKind] }}共 {{ activeEntries.length }} 组</span>
        <button type="button" class="btn btn--secondary btn--sm" data-test="cleanup-page-next-bottom" :disabled="currentPage >= pageCount" @click="setPage(currentPage + 1, true)">下一页</button>
      </nav>
    </template>

    <EmptyState
      v-else
      data-test="cleanup-idle"
      icon="copy"
      :title="status?.cancelled ? '上一轮分析已取消，没有产生结果。' : '尚未分析。'"
      message="点「开始分析」扫描库内的精确重复与近似重复图片。"
    >
      <template #actions>
        <button type="button" class="btn btn--primary btn--sm" data-test="cleanup-idle-start" :disabled="running || editLocked" @click="startAnalysis">开始分析</button>
      </template>
    </EmptyState>

    <!-- 有勾选时才在底部浮出（D-UD-10）：「移到废纸篓」只作用于勾选的图片；不勾就没有删除。
         这里不给「全选当前结果」：疑似重复要逐组看过才能勾（D-PC49），没有「整批全勾」这回事。 -->
    <SelectionBar :count="selection.length" data-test="cleanup-selection-bar" @clear="clearSelection">
      <template #actions>
        <span class="cleanup-page__summary" data-test="cleanup-summary">移到废纸篓后，在访达清空废纸篓即可释放 {{ formatBytes(reclaimableBytes) }}</span>
        <!-- 「移出本组」「不是重复」的请求还没返回时不能删除（P-032 复审 I-1）：它们落地时会改组和勾选。 -->
        <button
          type="button"
          class="btn btn--danger"
          :disabled="selection.length === 0 || running || processing || showDismissed || dismissing"
          :title="dismissing ? '正在更新分组，完成后再删除' : ''"
          data-test="cleanup-delete-selected"
          @click="deleteSelected"
        >{{ processing ? '处理中…' : `移到废纸篓 (${selection.length})` }}</button>
      </template>
    </SelectionBar>

    <!-- 全屏查看器（D-UD-13）：关闭固定在右上角，← → 切换，Esc 退出。 -->
    <div v-if="viewer.member" class="cleanup-viewer" role="dialog" aria-modal="true" :aria-label="`查看 ${viewer.member.name}`" data-test="cleanup-viewer" @click.self="closeViewer">
      <button type="button" class="btn-icon cleanup-viewer__close" aria-label="关闭" title="关闭 (Esc)" @click="closeViewer"><AppIcon name="close" :size="20" /></button>
      <button type="button" class="btn-icon cleanup-viewer__nav cleanup-viewer__nav--prev" aria-label="上一张" title="上一张 (←)" :disabled="viewer.index <= 0" @click="stepViewer(-1)"><AppIcon name="chevron-left" :size="24" /></button>
      <img class="cleanup-viewer__img" :src="`/preview/image/${viewer.member.id}`" :alt="viewer.member.name" />
      <button type="button" class="btn-icon cleanup-viewer__nav cleanup-viewer__nav--next" aria-label="下一张" title="下一张 (→)" :disabled="viewer.index >= viewer.members.length - 1" @click="stepViewer(1)"><AppIcon name="chevron-right" :size="24" /></button>
      <p class="cleanup-viewer__caption">
        {{ viewer.member.name }} · {{ viewer.member.width }}×{{ viewer.member.height }} · {{ formatBytes(memberBytes(viewer.member)) }}
        <span class="cleanup-viewer__caption-path">{{ viewer.member.path }}</span>
      </p>
    </div>

    <!-- 删除前的汇总确认（D-PC49）与「把元数据合并到保留项」（D-PC48，默认勾选）：合并不随撤销回退，所以先确认。 -->
    <DialogShell
      v-if="deleteConfirm.show"
      class="cleanup-delete-confirm"
      size="sm"
      title="移到废纸篓"
      data-test="cleanup-delete-confirm-dialog"
      @close="answerDeleteConfirm(false)"
    >
      <p data-test="cleanup-delete-summary">
        将把 {{ deleteConfirm.summary.count }} 张图片移到废纸篓<template v-if="deleteConfirm.summary.bytes > 0">，共 {{ formatBytes(deleteConfirm.summary.bytes) }}</template>。
      </p>
      <p v-if="deleteConfirmKindsText" data-test="cleanup-delete-kinds">其中{{ deleteConfirmKindsText }}。</p>
      <p v-if="deleteConfirmSimilarityCount > 0" class="cleanup-delete-confirm__warn" data-test="cleanup-delete-similarity">
        其中 {{ deleteConfirmSimilarityCount }} 张是按感知哈希判断的近似重复，删除前请确认已逐组看过。
      </p>
      <template v-if="deleteConfirm.mergeAvailable">
        <label class="cleanup-delete-confirm__merge">
          <input v-model="deleteConfirm.merge" type="checkbox" data-test="cleanup-merge-toggle" />
          把元数据合并到保留项
        </label>
        <p class="cleanup-delete-confirm__help" data-test="cleanup-merge-scope">标签、人物、收藏、点赞与评分合并到各组保留的那张。</p>
        <p class="cleanup-delete-confirm__help" data-test="cleanup-merge-note">标签只合并手动标签（自动标签不合并）。撤销删除不会撤回合并。合并失败时不会删除任何图片。</p>
      </template>
      <p class="cleanup-delete-confirm__help">移到废纸篓后可在回收站撤销；在访达清空废纸篓才会释放空间。所在磁盘不支持废纸篓时，会先问你怎么处理。</p>
      <template #footer>
        <button type="button" class="btn btn--secondary" data-test="cleanup-delete-cancel" @click="answerDeleteConfirm(false)">取消</button>
        <button type="button" class="btn btn--danger" data-test="cleanup-delete-confirm" @click="answerDeleteConfirm(true)">移到废纸篓</button>
      </template>
    </DialogShell>
  </section>
</template>

<script>
import {
  StartImageCleanupAnalysis,
  CancelImageCleanupAnalysis,
  DismissImageNearDuplicateGroup,
  DismissImageNearDuplicateMember,
  ListCleanupDismissals,
  UndoCleanupDismissals,
  MergeMediaMetadata,
  OpenImageDirectory,
  RevealImage,
  StartImagePerceptualHashBackfill,
  GetImagePerceptualHashBackfillStatus
} from '../../wailsjs/go/main/App';
import AppBanner from './ui/AppBanner.vue';
import AppIcon from './ui/AppIcon.vue';
import DialogShell from './ui/DialogShell.vue';
import EmptyState from './ui/EmptyState.vue';
import PageHeader from './ui/PageHeader.vue';
import ProgressBar from './ui/ProgressBar.vue';
import SelectionBar from './ui/SelectionBar.vue';
import SkeletonBlock from './ui/SkeletonBlock.vue';
import StatTile from './ui/StatTile.vue';
import TrashUndoBanner from './video-list/TrashUndoBanner.vue';
import { summarizeTrashFailures } from './TrashCenterDialog.vue';
import { formatBytes } from '../utils/mediaDetails.js';
import {
  photoCleanupStore,
  startPhotoCleanupPolling,
  resumePhotoCleanupPolling,
  resetPhotoCleanupReview,
  refreshPhotoCleanupStatus
} from '../utils/photoCleanupStore.js';
import { confirmAction } from '../utils/feedback.js';
import {
  applySuggestion, cleanupGroup, clearGroupSelection, deletionPlan, deletionPlanUnchanged,
  describeMergeFailure, describeSelectionKinds, keeperOf, lockedIDs, pruneSelection,
  SELECTION_CHANGED_MESSAGE, selectionSummary, setKeeper, similarityCount, suggestedIDs
} from '../utils/cleanupSelection.js';

const STAGE_LABELS = {
  load: '读取图片记录',
  group: '按文件大小聚合',
  hash: '读取采样哈希',
  near: '比对感知哈希',
  done: '完成'
};

const DAY_MS = 24 * 60 * 60 * 1000;
// 库里目录为空时的占位串。它不是真实路径，不能拿去调打开目录。
const UNKNOWN_DIRECTORY = '未知目录';
const GROUP_PAGE_SIZE = 20;
const KIND_LABELS = { exact: '精确重复', near: '疑似重复' };
// 统计卡的线性图标（ui/icons.js 里的名字），颜色跟随 currentColor。
const TILE_ICONS = { exact: 'copy', near: 'image', pending: 'trash' };
const TILE_HINTS = { exact: '内容逐字节一致，默认已勾副本', near: '画面近似，默认不勾，需逐组确认' };
const DISMISSAL_KIND = 'image_near_duplicate';
const DISMISSAL_PAGE_SIZE = 50;

function emptyDismissals() {
  return { items: [], cursor: 0, hasMore: false, loading: false, loaded: false, undoing: false, error: '' };
}

function emptyDeleteConfirm() {
  return { show: false, summary: { count: 0, bytes: 0, byKind: {}, similar: 0 }, mergeAvailable: false, merge: true };
}

export default {
  name: 'PhotoCleanupPage',
  components: { AppBanner, AppIcon, DialogShell, EmptyState, PageHeader, ProgressBar, SelectionBar, SkeletonBlock, StatTile, TrashUndoBanner },
  // deleted：清理页改动了图片库（删除，或在撤销条 / 回收站里撤销了删除），图片库据此重载。
  emits: ['close', 'deleted'],
  data() {
    return {
      UNKNOWN_DIRECTORY,
      KIND_LABELS,
      TILE_ICONS,
      MEMBER_PAGE_SIZE: 20,
      localError: '',
      processing: false,
      // confirming：删除确认框打开着。这期间勾选、保留项与组成员和删除进行中一样锁住（P-032 复审 I-1）；
      // 与 processing 分开，是因为图片库只在真正删除时拦住离开（确认框随清理页卸载而取消）。
      confirming: false,
      dismissing: false,
      cancelling: false,
      showDismissed: false,
      dismissals: emptyDismissals(),
      deleteConfirm: emptyDeleteConfirm(),
      // 指纹补全任务的状态：没有它，用户看到"还有 N 张没指纹"却没有任何办法。
      perceptualHashStatus: null,
      unsubscribePerceptualHash: null,
      viewer: { members: [], index: -1, member: null }
    };
  },
  computed: {
    review() { return photoCleanupStore.review; },
    keepOverrides() { return this.review.keepOverrides; },
    selection() { return this.review.selection; },
    selectedIDs() { return new Set(this.selection.map(Number)); },
    deletedIDSet() { return new Set(this.deletedIDs.map(Number)); },
    skippedGroups() { return this.review.skippedGroups; },
    collapsedDirs() { return this.review.collapsedDirs; },
    deletedIDs() { return this.review.deletedIDs; },
    // 「移出本组」的成员：{ 组 key: [图片 id] }。后端图片分析缓存不回读决定，本轮审阅里靠它稳定隐藏。
    removedMembers() { return this.review.removedMembers || {}; },
    // 只勾同目录副本：跨目录那份往往是备份，想保守一点的人可以一键切过去。
    sameDirOnly() { return !!this.review.sameDirOnly; },
    status() { return photoCleanupStore.status; },
    running() { return !!this.status?.running; },
    analysis() { return (this.status?.completed && this.status.analysis) || null; },
    resultStale() { return Boolean(this.analysis && (this.status?.stale || this.deletedIDs.length)); },
    perceptualHashRunning() { return !!this.perceptualHashStatus?.running; },
    // 改勾选、保留项、组成员的控件与方法共用的锁：删除确认框打开着或删除进行中。
    editLocked() { return this.processing || this.confirming; },
    progress() { return this.status?.progress || {}; },
    stageLabel() { return STAGE_LABELS[this.progress.stage] || '准备中'; },
    displayError() { return this.localError || this.status?.error || ''; },
    analysisKey() {
      if (!this.analysis) return '';
      return String(this.status?.started_at || '') || 'unkeyed-analysis';
    },
    // 两类候选拉平成统一条目，key 带类别：同一张图可能既是精确组也是近似组的推荐保留项。
    entries() {
      if (!this.analysis) return [];
      const dismissed = new Set(this.review.dismissedKeys || []);
      const flatten = (groups, kind) => (groups || []).flatMap(group => {
        const key = `${kind}-${group.original?.id ?? 'nogroup'}`;
        if (dismissed.has(key)) return [];
        const removed = new Set((this.removedMembers[key] || []).map(Number));
        const members = [group.original, ...(group.candidates || [])]
          .filter(Boolean)
          .filter(member => !removed.has(Number(member.id)));
        if (members.length < 2) return [];
        const directories = new Set(members.map(member => this.directoryOf(member)));
        // 推荐保留项被移出本组时，由剩下成员里排第一的接替（后端按整理成果、画质排序）；
        // 已经移到废纸篓的跳过，由 cleanupGroups 按 deletedIDs 选（P-032 复审 I-a）。
        const suggestedKeeperID = members.some(member => Number(member.id) === Number(group.original?.id))
          ? Number(group.original.id)
          : Number(members[0].id);
        return [{
          kind,
          group,
          members,
          key,
          suggestedKeeperID,
          directoryCount: directories.size,
          spansDirectories: directories.size > 1
        }];
      });
      return [
        ...flatten(this.analysis.duplicate_groups, 'exact'),
        ...flatten(this.analysis.near_duplicate_groups, 'near')
      ];
    },
    // 勾选规则用的统一组（utils/cleanupSelection.js）。建议保留项已删除时接替者取第一个仍在库的成员；
    // 一个在库的都没有时这一组不参与合并与删除（P-032 复审 I-a）。
    cleanupGroups() {
      return this.entries.map(entry => cleanupGroup({
        key: entry.key,
        kind: entry.kind,
        keeperId: entry.suggestedKeeperID,
        memberIds: entry.members.map(member => member.id),
        switchable: true,
        unavailable: entry.members.filter(member => this.deletedIDSet.has(Number(member.id))).map(member => member.id)
      }));
    },
    groupByKey() {
      return new Map(this.cleanupGroups.map(group => [group.key, group]));
    },
    entryByKey() {
      return new Map(this.entries.map(entry => [entry.key, entry]));
    },
    entriesByKind() {
      const byKind = { exact: [], near: [] };
      for (const entry of this.entries) byKind[entry.kind].push(entry);
      return byKind;
    },
    // 当前区域：用户选过就用它（哪怕那一类已经清空，统计卡上看得到 0 组）；没选过时有精确重复先看精确重复。
    activeKind() {
      const chosen = this.review.kind;
      if (chosen === 'exact' || chosen === 'near') return chosen;
      return this.entriesByKind.exact.length === 0 && this.entriesByKind.near.length > 0 ? 'near' : 'exact';
    },
    activeEntries() { return this.entriesByKind[this.activeKind]; },
    // 每张图出现在哪几类里，统计卡按勾选数各类计数时只扫勾选，不扫全部成员。
    kindsByMemberID() {
      const kinds = new Map();
      for (const entry of this.entries) {
        for (const member of entry.members) {
          const id = Number(member.id);
          if (!kinds.has(id)) kinds.set(id, new Set());
          kinds.get(id).add(entry.kind);
        }
      }
      return kinds;
    },
    imageCountByKind() {
      const counts = { exact: 0, near: 0 };
      for (const kinds of this.kindsByMemberID.values()) {
        for (const kind of kinds) counts[kind] += 1;
      }
      return counts;
    },
    selectedCountByKind() {
      const counts = { exact: 0, near: 0 };
      for (const id of this.selectedIDs) {
        for (const kind of this.kindsByMemberID.get(id) || []) counts[kind] += 1;
      }
      return counts;
    },
    kindTiles() {
      return ['exact', 'near'].map(kind => ({
        kind,
        label: KIND_LABELS[kind],
        hint: TILE_HINTS[kind],
        icon: TILE_ICONS[kind],
        groups: this.entriesByKind[kind].length,
        images: this.imageCountByKind[kind],
        selected: this.selectedCountByKind[kind]
      }));
    },
    // 顶层按目录分组：整组归到"推荐保留项"（后端 original）所在目录，
    // 手动切换保留项时组不会跳走；组员仍可位于其他目录，卡片上会标出来。只分当前区域的组。
    directorySections() {
      const buckets = new Map();
      for (const entry of this.activeEntries) {
        const directory = this.directoryOf(entry.group.original);
        if (!buckets.has(directory)) buckets.set(directory, { directory, entries: [], imageIDs: new Set() });
        const bucket = buckets.get(directory);
        bucket.entries.push(entry);
        for (const member of entry.members) bucket.imageIDs.add(Number(member.id));
      }
      return [...buckets.values()]
        .map(bucket => ({ ...bucket, groupCount: bucket.entries.length, imageCount: bucket.imageIDs.size }))
        .sort((a, b) => a.directory.localeCompare(b.directory));
    },
    pageCount() { return Math.max(1, Math.ceil(this.activeEntries.length / GROUP_PAGE_SIZE)); },
    currentPage() { return Math.min(this.pageCount, Math.max(1, this.review.pages?.[this.activeKind] || 1)); },
    pagedDirectorySections() {
      const start = (this.currentPage - 1) * GROUP_PAGE_SIZE;
      let offset = 0;
      return this.directorySections.flatMap(section => {
        const entries = section.entries.slice(Math.max(0, start - offset), Math.max(0, start + GROUP_PAGE_SIZE - offset));
        offset += section.entries.length;
        return entries.length ? [{ ...section, entries }] : [];
      });
    },
    // 「本页按建议勾选」的范围：本页展开着的组（折叠起来的看不到，不替用户勾）。
    pageSuggestableEntries() {
      return this.pagedDirectorySections
        .filter(section => !this.isDirCollapsed(section.directory))
        .flatMap(section => section.entries)
        .filter(entry => this.suggestedIDsFor(entry).length > 0);
    },
    pageFullySuggested() {
      const entries = this.pageSuggestableEntries;
      return entries.length > 0 && entries.every(entry => this.isGroupFullySuggested(entry));
    },
    // 两类里只有一类有结果时，另一类的空态（全部为空走下面的 emptyState）。
    areaEmptyState() {
      if (this.activeKind === 'exact') return { title: '没有发现精确重复。', detail: '', action: false };
      const coverage = this.coverage;
      if (coverage && coverage.total > 0 && coverage.done === 0) {
        return { title: '疑似重复尚未计算。', detail: '还没有图片算过指纹，近似重复检测还没开始。', action: true };
      }
      if (coverage && coverage.total > 0 && coverage.done < coverage.total) {
        return { title: '没有发现疑似重复。', detail: `指纹只算了 ${coverage.done} / ${coverage.total} 张，其余图片还没参与近似重复检测。`, action: true };
      }
      return { title: '没有发现疑似重复。', detail: '', action: false };
    },
    membersByID() {
      const members = new Map();
      for (const entry of this.entries) {
        for (const member of entry.members) members.set(Number(member.id), member);
      }
      return members;
    },
    suggestionsByKey() {
      const options = this.selectionOptions();
      return new Map(this.cleanupGroups.map(group => [group.key, suggestedIDs(group, {
        ...options, excluded: group.memberIds.filter(id => this.deletedIDSet.has(id))
      })]));
    },
    hasGroups() { return this.entries.length > 0; },
    // 「本组不删」的组 key。
    skippedKeys() {
      return Object.keys(this.skippedGroups).filter(key => this.skippedGroups[key]);
    },
    // 同一张图可能既是某组的候选、又是另一组的保留项（精确对只在彼此之间排除）。
    // 任何一组的保留项都不允许被删，否则会出现"这一组勾了删、那一组显示保留"的自相矛盾；
    // 「本组不删」的组同理，它的成员在别的组里也不能勾（P-032 评审 Minor 7）。
    protectedIDs() {
      return lockedIDs(this.cleanupGroups, this.keepOverrides, this.skippedKeys);
    },
    keeperIDs() {
      return lockedIDs(this.cleanupGroups, this.keepOverrides);
    },
    skippedMemberIDs() {
      const skipped = new Set(this.skippedKeys);
      return new Set(this.cleanupGroups.filter(group => skipped.has(group.key)).flatMap(group => group.memberIds));
    },
    allCollapsed() {
      return this.directorySections.length > 0
        && this.directorySections.every(section => this.isDirCollapsed(section.directory));
    },
    // 默认勾选规则（D-PC49，恢复图片设计「近似重复一律不勾」）：只勾精确重复里保留项以外的副本；
    // 打开"只勾同目录"后，位于别处的副本不勾。
    autoSelectedIDs() {
      return [...new Set(this.cleanupGroups.filter(group => group.kind === 'exact')
        .flatMap(group => this.suggestionsByKey.get(group.key) || []))];
    },
    memberBytesByID() {
      const byID = new Map();
      for (const entry of this.entries) {
        for (const member of entry.members) byID.set(Number(member.id), this.memberBytes(member));
      }
      return byID;
    },
    reclaimableBytes() {
      return this.selection.reduce((sum, id) => sum + (this.memberBytesByID.get(Number(id)) || 0), 0);
    },
    // 感知哈希覆盖率（D-PC50）：近似重复只在有指纹的图片之间比。精确重复不需要指纹。
    coverage() {
      const value = this.analysis?.coverage?.perceptual_hash;
      if (!value) return null;
      return { done: Number(value.done || 0), total: Number(value.total || 0) };
    },
    coverageText() {
      const coverage = this.coverage;
      if (!coverage || coverage.total <= 0 || coverage.done >= coverage.total) return '';
      return coverage.done === 0 ? '近似重复尚未计算（还没有图片算过指纹）' : `近似重复指纹已算 ${coverage.done} / ${coverage.total}`;
    },
    emptyState() {
      const coverage = this.coverage;
      if (coverage && coverage.total > 0 && coverage.done === 0) {
        return { title: '没有发现精确重复；近似重复尚未计算。', detail: '还没有图片算过指纹，近似重复检测还没开始。', action: true };
      }
      if (coverage && coverage.total > 0 && coverage.done < coverage.total) {
        return { title: '没有发现重复或近似重复的图片。', detail: `指纹只算了 ${coverage.done} / ${coverage.total} 张，其余图片还没参与近似重复检测。`, action: true };
      }
      return { title: '没有发现重复或近似重复的图片。', detail: '', action: false };
    },
    deleteConfirmKindsText() {
      return describeSelectionKinds(this.deleteConfirm.summary, '张');
    },
    deleteConfirmSimilarityCount() {
      return similarityCount(this.deleteConfirm.summary);
    }
  },
  watch: {
    analysisKey: {
      immediate: true,
      handler(key) {
        // 只有换了一批分析结果才重置；返回图片库再进来时 key 没变，接着上次审阅。
        // 结果换批，查看器里的成员对象已经失效。
        this.closeViewer();
        if (!key || key === this.review.key) return;
        this.resetGroupState(key);
      }
    },
    running(value) {
      if (!value) this.cancelling = false;
    }
  },
  async mounted() {
    startPhotoCleanupPolling();
    window.addEventListener('keydown', this.handleKeydown);
    this.subscribePerceptualHash();
    await refreshPhotoCleanupStatus();
    // 与视频侧的清理审阅同构：进来时后端既没在跑、也没有可用结果，就直接发起一次分析，
    // 不用再多点一次"开始分析"。已有结果（哪怕已标记过期）保留下来，要不要重跑由用户决定；
    // 上一轮是用户自己取消的，也不替他重跑。
    const status = photoCleanupStore.status;
    if (!status?.running && !status?.completed && !status?.cancelled) {
      await this.startAnalysis();
    }
  },
  beforeUnmount() {
    window.removeEventListener('keydown', this.handleKeydown);
    if (this.unsubscribePerceptualHash) {
      this.unsubscribePerceptualHash();
      this.unsubscribePerceptualHash = null;
    }
    if (this._resolveDeleteConfirm) this._resolveDeleteConfirm(null);
  },
  methods: {
    formatBytes,
    async subscribePerceptualHash() {
      // 先挂监听再取状态：取状态要等一个来回，这期间用户可能已经离开本页，
      // 那时才挂上的监听就没人摘得掉了（顺序与 PhotoAITaskPanel 一致）。
      if (window.runtime?.EventsOn) {
        const off = window.runtime.EventsOn(
          'image-perceptual-hash-backfill-progress',
          status => { this.perceptualHashStatus = status; }
        );
        if (typeof off === 'function') this.unsubscribePerceptualHash = off;
      }
      try {
        const status = await GetImagePerceptualHashBackfillStatus();
        // 本页不轮询：等待期间已经收到事件的话，回来的这份是旧的，
        // 盖回去会把按钮永久停在"补全中"。
        if (!this.perceptualHashStatus) this.perceptualHashStatus = status;
      } catch (err) {
        // 取不到状态只影响按钮的运行态显示，不该挡住整个审阅页。
        console.warn('读取图片指纹补全状态失败', err);
      }
    },
    async startPerceptualHashBackfill() {
      if (this.perceptualHashRunning) return;
      this.localError = '';
      try {
        this.perceptualHashStatus = await StartImagePerceptualHashBackfill();
      } catch (err) {
        this.localError = String(err?.message || err || '补全指纹失败');
      }
    },
    directoryOf(image) {
      return String(image?.directory || '').trim() || UNKNOWN_DIRECTORY;
    },
    // file_size 是分析时 os.Stat 的实测值，比库里的 size 更贴近磁盘现状。
    memberBytes(member) {
      return Number(member?.file_size || member?.size || 0);
    },
    memberTimeMs(member) {
      if (member?.taken_at) {
        const taken = new Date(member.taken_at).getTime();
        if (Number.isFinite(taken)) return taken;
      }
      const modNS = Number(member?.mod_time_ns || 0);
      return modNS > 0 ? Math.round(modNS / 1e6) : 0;
    },
    memberTimeLabel(member) {
      const ms = this.memberTimeMs(member);
      if (!ms) return '未知';
      return new Date(ms).toLocaleString();
    },
    hasMetadata(member) {
      return Boolean(member?.is_favorite || member?.personal_rating != null || member?.curation?.people || (member?.tags || []).length);
    },
    similarityLabel(entry) {
      const distance = Number(entry.group?.max_hamming_distance || 0);
      return `指纹距离 ${distance}/64 · 已复核画面，仍需人工确认`;
    },
    memberPageCount(entry) { return Math.max(1, Math.ceil(entry.members.length / this.MEMBER_PAGE_SIZE)); },
    memberPage(entry) { return Math.min(this.memberPageCount(entry), Math.max(1, this.review.memberPages?.[entry.key] || 1)); },
    visibleMembers(entry) {
      const start = (this.memberPage(entry) - 1) * this.MEMBER_PAGE_SIZE;
      return entry.members.slice(start, start + this.MEMBER_PAGE_SIZE);
    },
    // 每一项都跟保留项比：不用自己在几个数字之间来回换算。
    diffFor(entry, member) {
      const keeper = this.membersByID.get(this.keepFor(entry));
      if (!keeper || Number(keeper.id) === Number(member.id)) return {};
      const diff = {};
      const keepPixels = (keeper.width || 0) * (keeper.height || 0);
      const pixels = (member.width || 0) * (member.height || 0);
      // 差异不到 5% 就不标了：1004×1004 对 1000×1000 标成"仅 1/1"只会误导。
      if (keepPixels > 0 && pixels > 0) {
        const ratio = pixels / keepPixels;
        if (ratio <= 0.95) diff.resolution = `仅 1/${Math.round(1 / ratio)}`;
        else if (ratio >= 1.05) diff.resolution = `${ratio.toFixed(1)}×`;
      }
      const keepBytes = this.memberBytes(keeper);
      const bytes = this.memberBytes(member);
      if (keepBytes > 0 && bytes > 0 && keepBytes !== bytes) {
        const delta = Math.round(((bytes - keepBytes) / keepBytes) * 100);
        if (delta !== 0) diff.size = delta < 0 ? `小 ${Math.abs(delta)}%` : `大 ${delta}%`;
      }
      // 只在两边取的是同一种时间时才比：EXIF 拍摄时间减文件修改时间没有意义。
      const keepTime = this.memberTimeMs(keeper);
      const time = this.memberTimeMs(member);
      const sameClock = Boolean(keeper.taken_at) === Boolean(member.taken_at);
      if (sameClock && keepTime > 0 && time > 0) {
        const days = Math.round((time - keepTime) / DAY_MS);
        if (days !== 0) diff.time = days < 0 ? `早 ${Math.abs(days)} 天` : `晚 ${days} 天`;
      }
      return diff;
    },
    isDeleted(image) {
      return this.deletedIDSet.has(Number(image?.id));
    },
    // 勾选规则的参数：跨组锁定（保留项与「本组不删」的成员）、已删除的排除、「只勾同目录」由 filter 表达。
    selectionOptions(overrides = this.keepOverrides) {
      return {
        overrides,
        skipped: this.skippedKeys,
        locked: overrides === this.keepOverrides ? this.protectedIDs : lockedIDs(this.cleanupGroups, overrides, this.skippedKeys),
        excluded: this.deletedIDs,
        filter: this.suggestionFilter()
      };
    },
    suggestionFilter() {
      // 每次操作独立缓存，setKeeper 比较旧/新保留项时分别算一次；不能逐成员扫描整组找保留项。
      const directories = new WeakMap();
      return (id, group, overrides = this.keepOverrides) => {
        if (this.skippedGroups[group.key]) return false;
        if (!this.sameDirOnly) return true;
        const entry = this.entryByKey.get(group.key);
        if (!entry) return false;
        if (!directories.has(overrides)) directories.set(overrides, new Map());
        const byGroup = directories.get(overrides);
        if (!byGroup.has(group.key)) {
          const keeper = this.membersByID.get(keeperOf(group, overrides)) || entry.group.original;
          byGroup.set(group.key, this.directoryOf(keeper));
        }
        return this.directoryOf(this.membersByID.get(Number(id))) === byGroup.get(group.key);
      };
    },
    groupOf(entry) {
      return this.groupByKey.get(entry.key) || null;
    },
    // 这张图被别的组锁定：是别的组的保留项，或者在一个「本组不删」的组里。本组也不能勾它删。
    lockReason(entry, image) {
      const id = Number(image?.id);
      // 已删除的行只标「已删除」（本组剩下的都已删时它们也锁定，但不是因为别的组）。
      if (this.isDeleted(image) || this.keepFor(entry) === id) return '';
      if (this.keeperIDs.has(id)) return '另一组要保留它';
      if (!this.isSkipped(entry) && this.skippedMemberIDs.has(id)) return '另一组设了「本组不删」';
      return '';
    },
    isProtected(entry, image) {
      return Boolean(this.lockReason(entry, image));
    },
    // 本组是否已经是"保留推荐项、其余全勾"的状态。
    isGroupFullySuggested(entry) {
      const suggested = this.suggestedIDsFor(entry);
      return suggested.length > 0 && suggested.every(id => this.selectedIDs.has(id));
    },
    suggestedIDsFor(entry) {
      return this.suggestionsByKey.get(entry.key) || [];
    },
    // 「按建议勾选」只作用于这一组（D-PC49），再点一次取消本组勾选。
    toggleGroupSuggestion(entry) {
      const group = this.groupOf(entry);
      if (!group || this.editLocked) return;
      this.review.selection = this.isGroupFullySuggested(entry)
        ? clearGroupSelection(this.selection, group)
        : applySuggestion(this.selection, group, this.selectionOptions());
    },
    groupOutcomeLabel(entry) {
      if (this.isSkipped(entry)) return '本组不删';
      if (this.isExhausted(entry)) return '本组不参与删除';
      const marked = entry.members.filter(member => this.selectedIDs.has(Number(member.id))).length;
      return marked === 0 ? '本组暂不删除任何图片' : `本组将删除 ${marked} 张`;
    },
    keepFor(entry) {
      const group = this.groupOf(entry);
      // 本组剩下的都已删：没有保留项，成员全部锁定（不会因此变成全部可删）。
      if (group?.exhausted) return null;
      const keeper = keeperOf(group, this.keepOverrides);
      // 组总有保留项；拿不到时退回组内第一个成员，避免 NaN 让整组失去保留项、全部可删。
      return keeper ?? Number(entry.members[0]?.id);
    },
    // 本组剩下的都已移到废纸篓，没有可接替的保留项（P-032 复审 I-a）。
    isExhausted(entry) {
      return Boolean(this.groupOf(entry)?.exhausted);
    },
    isKept(entry, image) {
      return Number(image?.id) === this.keepFor(entry);
    },
    // 换保留项：新保留项移出勾选，原保留项解除锁定但不自动勾上（§9.2 裁决）；本组原先是「按建议全勾」时
    // 按新保留项重算其余成员。删除进行中不能换（P-032 评审 I-1）。
    setKeep(entry, image) {
      const group = this.groupOf(entry);
      if (!group || this.editLocked) return;
      const next = setKeeper({
        groups: this.cleanupGroups,
        overrides: this.keepOverrides,
        selection: this.selection,
        excluded: this.deletedIDs,
        filter: this.suggestionFilter(),
        skipped: this.skippedKeys
      }, group, image?.id);
      this.review.keepOverrides = next.overrides;
      this.review.selection = next.selection;
    },
    isSkipped(entry) {
      return !!this.skippedGroups[entry.key];
    },
    toggleSkipGroup(entry) {
      const group = this.groupOf(entry);
      if (!group || this.editLocked) return;
      if (this.isSkipped(entry)) {
        this.review.skippedGroups = { ...this.skippedGroups, [entry.key]: false };
        // 恢复本组：按默认规则重算（精确重复重新勾副本，近似重复仍然不勾）。
        if (entry.kind === 'exact') {
          this.review.selection = applySuggestion(this.selection, group, this.selectionOptions());
        }
      } else {
        this.review.skippedGroups = { ...this.skippedGroups, [entry.key]: true };
        this.review.selection = clearGroupSelection(this.selection, group);
      }
    },
    toggleSameDirOnly() {
      if (this.editLocked) return;
      this.review.sameDirOnly = !this.sameDirOnly;
      // 切换的是默认规则，直接按新规则重算勾选（手动微调会被覆盖，这是明示行为）。
      this.applyAutoSelection();
    },
    // 折叠状态按区域分开记：同一个目录在精确重复里折起来，不影响疑似重复。
    dirKey(directory) {
      return `${this.activeKind}:${directory}`;
    },
    isDirCollapsed(directory) {
      return !!this.collapsedDirs[this.dirKey(directory)];
    },
    toggleDir(directory) {
      const key = this.dirKey(directory);
      this.review.collapsedDirs = { ...this.collapsedDirs, [key]: !this.collapsedDirs[key] };
    },
    toggleAllDirectories() {
      const collapse = !this.allCollapsed;
      const next = { ...this.collapsedDirs };
      for (const section of this.directorySections) next[this.dirKey(section.directory)] = collapse;
      this.review.collapsedDirs = next;
    },
    setKind(kind) {
      this.review.kind = kind;
    },
    // 底部翻页后回到本区顶部，否则停在新一页的末尾。
    setPage(page, scroll = false) {
      this.review.pages = { ...this.review.pages, [this.activeKind]: page };
      if (scroll) this.$nextTick(() => this.$refs.areaTop?.scrollIntoView?.({ block: 'start' }));
    },
    // 「本页按建议勾选」：本页展开着的组都按建议勾上；已经全是建议状态时再点取消这些组的勾选。
    togglePageSuggestion() {
      if (this.editLocked) return;
      const groups = this.pageSuggestableEntries.map(entry => this.groupOf(entry)).filter(Boolean);
      if (this.pageFullySuggested) {
        this.review.selection = groups.reduce((selection, group) => clearGroupSelection(selection, group), this.selection);
        return;
      }
      const options = this.selectionOptions();
      this.review.selection = groups.reduce((selection, group) => applySuggestion(selection, group, options), this.selection);
    },
    applyAutoSelection() {
      this.review.selection = this.autoSelectedIDs;
    },
    resetGroupState(key) {
      const { sameDirOnly, kind } = this.review;
      resetPhotoCleanupReview(key);
      // 勾选策略与正在看的区域是用户的偏好，不该跟着每轮分析被重置。
      this.review.sameDirOnly = !!sameDirOnly;
      this.review.kind = kind || '';
      this.applyAutoSelection();
    },
    async openDirectory(directory) {
      this.localError = '';
      try {
        await OpenImageDirectory(directory);
      } catch (err) {
        this.localError = `在访达中显示失败：${err}`;
      }
    },
    async revealMember(member) {
      this.localError = '';
      try {
        await RevealImage(Number(member.id));
      } catch (err) {
        this.localError = `在访达中显示失败：${err}`;
      }
    },
    async copyPath(member) {
      try {
        await navigator.clipboard?.writeText(member.path);
      } catch (err) {
        // 剪贴板不可用时静默：路径本身已经完整显示在界面上。
      }
    },
    openViewer(entry, member) {
      const index = entry.members.findIndex(item => Number(item.id) === Number(member.id));
      this.viewer = { members: entry.members, index, member };
    },
    closeViewer() {
      this.viewer = { members: [], index: -1, member: null };
    },
    stepViewer(delta) {
      const next = this.viewer.index + delta;
      if (next < 0 || next >= this.viewer.members.length) return;
      this.viewer = { ...this.viewer, index: next, member: this.viewer.members[next] };
    },
    handleKeydown(event) {
      if (!this.viewer.member) return;
      const tag = event.target?.tagName;
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
      if (event.key === 'Escape') { event.preventDefault(); this.closeViewer(); }
      else if (event.key === 'ArrowRight') { event.preventDefault(); this.stepViewer(1); }
      else if (event.key === 'ArrowLeft') { event.preventDefault(); this.stepViewer(-1); }
    },
    async startAnalysis() {
      if (this.running || this.editLocked) return;
      this.localError = '';
      this.showDismissed = false;
      try {
        const started = await StartImageCleanupAnalysis();
        photoCleanupStore.status = started;
        this.resetGroupState('');
        resumePhotoCleanupPolling();
      } catch (err) {
        this.localError = `启动分析失败：${err}`;
      }
    },
    // 取消只是发出请求（D-PC51）：后台在两张图之间检查，真正停下后轮询拿到 cancelled 状态。
    async cancelAnalysis() {
      if (this.cancelling || !this.running) return;
      this.cancelling = true;
      this.localError = '';
      try {
        await CancelImageCleanupAnalysis();
      } catch (err) {
        this.cancelling = false;
        this.localError = `取消分析失败：${err}`;
      }
    },
    // 选择操作条的「取消选择」与 Esc（D-UD-10）：清空两个区域的全部勾选。确认框打开着或删除进行中不动勾选。
    clearSelection() {
      if (this.editLocked) return;
      this.review.selection = [];
    },
    toggleSelection(imageID) {
      const id = Number(imageID);
      if (this.editLocked) return;
      if (this.selection.includes(id)) {
        this.review.selection = this.selection.filter(item => item !== id);
        return;
      }
      // 保留项锁定（D-PC49）：界面上勾选框已禁用，这里再兜一次。
      if (this.protectedIDs.has(id) || this.deletedIDs.includes(id)) return;
      this.review.selection = [...this.selection, id];
    },
    memberNames(ids) {
      const wanted = new Set(ids.map(Number));
      const names = {};
      for (const entry of this.entries) {
        for (const member of entry.members) {
          const id = Number(member.id);
          if (wanted.has(id)) names[id] = member.name;
        }
      }
      return names;
    },
    // 删除前的汇总确认：返回 null 表示取消，否则 { merge }。
    askDeleteConfirm(summary, mergeAvailable) {
      if (this._resolveDeleteConfirm) this._resolveDeleteConfirm(null);
      this.deleteConfirm = { show: true, summary, mergeAvailable, merge: true };
      return new Promise(resolve => {
        this._resolveDeleteConfirm = resolve;
      });
    },
    answerDeleteConfirm(confirmed) {
      const resolve = this._resolveDeleteConfirm;
      this._resolveDeleteConfirm = null;
      const merge = this.deleteConfirm.mergeAvailable && this.deleteConfirm.merge;
      this.deleteConfirm = emptyDeleteConfirm();
      if (resolve) resolve(confirmed ? { merge } : null);
    },
    async deleteSelected() {
      // 「移出本组」「不是重复」的请求还没返回时不删除（P-032 复审 I-1）：它们落地时会改组和勾选。
      if (this.selection.length === 0 || this.processing || this.running || this.deleteConfirm.show || this.showDismissed || this.dismissing) return;
      // 删除前按锁定规则裁剪勾选：保留项、「本组不删」的成员、已删除、不再出现在结果里的都不送进删除。
      // 合并计划遇到锁定项时报错，不静默跳过。
      let confirmed;
      try {
        confirmed = deletionPlan(this.cleanupGroups, this.selection, this.selectionOptions());
      } catch (err) {
        this.localError = err?.message || String(err);
        return;
      }
      const { ids: requested, plan } = confirmed;
      if (requested.length === 0) return;
      const summary = selectionSummary(this.cleanupGroups, requested, id => this.memberBytesByID.get(id) || 0);
      // 先汇总确认（D-PC49）；取消时不调用任何写入。确认框打开期间锁住勾选、保留项与组成员。
      this.confirming = true;
      let choice;
      try {
        choice = await this.askDeleteConfirm(summary, plan.length > 0);
      } finally {
        this.confirming = false;
      }
      if (!choice) return;
      // 确认期间分析结果仍可能被轮询替换：按当前状态重算一次，与确认框里的名单不同就中止，
      // 不合并、不删除，勾选保持现在的样子。
      if (!deletionPlanUnchanged(confirmed, this.cleanupGroups, this.selection, this.selectionOptions())) {
        this.localError = SELECTION_CHANGED_MESSAGE;
        return;
      }

      this.processing = true;
      this.localError = '';
      let outcome = null;
      let failureNotice = '';
      try {
        // 删除之前按组把被删项的整理成果合并到各组保留项（D-PC48），任何一组失败都不进入删除；
        // 前面几组返回的提示随失败提示一起给出。
        if (choice.merge) {
          const warnings = [];
          for (const [index, item] of plan.entries()) {
            try {
              const result = await MergeMediaMetadata('image', item.keeperId, item.sourceIds, item.options);
              warnings.push(...(result?.warnings || []));
            } catch (err) {
              // 失败的那一组用保留项的文件名指明。
              const keeperName = this.memberNames([item.keeperId])[item.keeperId] || `图片 ${item.keeperId}`;
              this.localError = describeMergeFailure(err, index, plan.length, '图片', warnings, keeperName);
              return;
            }
          }
        }
        const banner = this.$refs.trashUndo;
        outcome = await banner.runDelete({ ids: requested, deleteFile: true, names: this.memberNames(requested) });
        const removed = new Set(outcome.removedIDs.map(Number));
        // 已删除的行留在结果里置灰，剩下的组可以接着审阅；没删掉的（失败、取消、磁盘不支持时选了暂不处理）
        // 仍然勾着，按当前的组再裁一次。
        this.review.deletedIDs = [...new Set([...this.deletedIDs, ...removed])];
        this.review.selection = pruneSelection(requested.filter(id => !removed.has(id)), this.cleanupGroups, this.selectionOptions());
        banner.showDeleteNotice(outcome, { reportFailures: false });
        if (outcome.failures.length) {
          failureNotice = `有 ${outcome.failures.length} 张图片删除失败：${summarizeTrashFailures(outcome.failures)}`;
          this.localError = failureNotice;
        }
      } catch (err) {
        this.localError = `删除所选图片失败：${err}`;
        return;
      } finally {
        this.processing = false;
      }
      if (!outcome || outcome.removedIDs.length === 0) return;
      this.$emit('deleted');
      // 让工具栏徽标与"结果可能过期"跟上这次删除。
      refreshPhotoCleanupStatus();
      // 不静默重跑：先问一句，让用户决定是继续审阅还是刷新候选。
      const rerun = await confirmAction({
        title: '重新分析清理候选',
        message: `已处理 ${outcome.removedIDs.length} 张图片。是否立即重新分析？\n选择"取消"可以继续审阅当前结果。`,
        confirmText: '重新分析'
      });
      if (rerun) await this.startAnalysis();
      if (failureNotice) this.localError = failureNotice;
    },
    // 撤销条整批撤销、或在它打开的回收站里恢复之后：放掉这些图片的「已删除」标记并通知图片库重载。
    // 拿不到具体 id（部分没恢复）时整体清空，宁可少标也不要长期标错。
    async afterTrashRestore(imageIDs) {
      const restored = new Set((imageIDs || []).map(Number));
      this.review.deletedIDs = restored.size ? this.deletedIDs.filter(id => !restored.has(Number(id))) : [];
      this.$emit('deleted');
      await refreshPhotoCleanupStatus();
    },
    async dismissGroup(entry) {
      if (this.dismissing || this.editLocked) return;
      const ids = entry.members.map(member => Number(member.id));
      const confirmed = await confirmAction({
        title: '不是重复',
        message: `确认这组 ${ids.length} 张图片不是重复？\n之后的分析不再把它们报为近似重复；任一文件变化后忽略自动失效，也可以在「已忽略」里撤销。`,
        confirmText: '不是重复'
      });
      // 等确认的这段时间里可能已经打开了删除确认框或开始删除：再查一次（P-032 复审 I-1）。
      if (!confirmed || this.dismissing || this.editLocked) return;
      this.dismissing = true;
      this.localError = '';
      try {
        await DismissImageNearDuplicateGroup(ids);
        // 后端缓存里那一组还在（Dismiss 只写忽略表并标记过期），本地过滤一刷新就复原。
        // 记进 dismissedKeys 让它在本轮审阅里稳定隐藏，重新分析后自然不再出现。
        this.review.dismissedKeys = [...new Set([...(this.review.dismissedKeys || []), entry.key])];
        this.review.selection = this.selection.filter(id => !ids.includes(id));
        this.dismissals.loaded = false;
      } catch (err) {
        this.localError = `忽略近似重复组失败：${err}`;
      } finally {
        this.dismissing = false;
      }
    },
    // 「移出本组」（D-PC31）：只否决这张图与组内其他成员的配对，其余成员之间的关系不动。
    async removeMember(entry, member) {
      if (this.dismissing || this.editLocked) return;
      const ids = entry.members.map(item => Number(item.id));
      const memberID = Number(member?.id);
      if (ids.length < 3 || !ids.includes(memberID)) return;
      const confirmed = await confirmAction({
        title: '移出本组',
        message: `把「${member.name || `图片 ${memberID}`}」移出本组？\n只记录它与组内其他 ${ids.length - 1} 张不是重复，其余成员之间的关系不变。可以在「已忽略」里撤销。`,
        confirmText: '移出本组'
      });
      if (!confirmed || this.dismissing || this.editLocked) return;
      this.dismissing = true;
      this.localError = '';
      try {
        await DismissImageNearDuplicateMember(ids, memberID);
        const removed = [...new Set([...(this.removedMembers[entry.key] || []), memberID])];
        this.review.removedMembers = { ...this.removedMembers, [entry.key]: removed };
        // 移出的是推荐保留项时由剩下排第一的接替：按新的组重新裁剪，接替者不能还勾着。
        this.review.selection = pruneSelection(this.selection.filter(id => id !== memberID), this.cleanupGroups, this.selectionOptions());
        this.dismissals.loaded = false;
      } catch (err) {
        this.localError = `移出本组失败：${err}`;
      } finally {
        this.dismissing = false;
      }
    },
    toggleDismissed() {
      this.showDismissed = !this.showDismissed;
      if (this.showDismissed && !this.dismissals.loaded && !this.dismissals.loading) this.loadDismissals(true);
    },
    async loadDismissals(reset) {
      if (reset) this.dismissals = emptyDismissals();
      const cursor = reset ? 0 : this.dismissals.cursor;
      this.dismissals.loading = true;
      this.dismissals.error = '';
      try {
        const page = await ListCleanupDismissals(DISMISSAL_KIND, cursor, DISMISSAL_PAGE_SIZE);
        const known = new Set(this.dismissals.items.map(item => item.id));
        this.dismissals.items = [...this.dismissals.items, ...(page?.items || []).filter(item => !known.has(item.id))];
        this.dismissals.cursor = Number(page?.next_cursor || 0);
        this.dismissals.hasMore = Boolean(page?.has_more);
        this.dismissals.loaded = true;
      } catch (err) {
        this.dismissals.error = `读取忽略记录失败：${err}`;
      } finally {
        this.dismissals.loading = false;
      }
    },
    describeDismissal(item) {
      const name = media => {
        const text = String(media?.name || '').trim() || `图片 ${media?.id}`;
        return media?.missing ? `${text}（已删除）` : text;
      };
      const media = item?.media || [];
      if (media.length >= 2) return `「${name(media[0])}」与「${name(media[1])}」`;
      return media.length ? `「${name(media[0])}」` : `记录 ${item?.id}`;
    },
    formatDismissalTime(value) {
      const time = new Date(value).getTime();
      return Number.isFinite(time) && time > 0 ? `忽略于 ${new Date(time).toLocaleString()}` : '';
    },
    async undoDismissal(item) {
      if (!item?.id || this.dismissals.undoing) return;
      this.dismissals.undoing = true;
      this.dismissals.error = '';
      try {
        await UndoCleanupDismissals(DISMISSAL_KIND, [item.id]);
        this.dismissals.items = this.dismissals.items.filter(row => row.id !== item.id);
        // 撤销后后端把缓存结果标为可能过期；重新分析后这对图片会重新参与检测。
        await refreshPhotoCleanupStatus();
      } catch (err) {
        this.dismissals.error = `撤销忽略失败：${err}`;
      } finally {
        this.dismissals.undoing = false;
      }
    }
  }
};
</script>

<style scoped>
.cleanup-page { display: flex; flex-direction: column; gap: var(--space-3); padding: var(--space-4) var(--space-5) var(--space-6); }
.cleanup-page > .cleanup-page__header { margin-bottom: 0; }
.cleanup-page__nav { display: flex; margin-bottom: calc(-1 * var(--space-1)); }
.cleanup-page__nav .btn { margin-left: calc(-1 * var(--space-2)); }
.cleanup-page__pagination { display: flex; align-items: center; justify-content: center; gap: var(--space-3); padding: var(--space-3); flex-wrap: wrap; color: var(--text-secondary); font-size: var(--fs-caption); }
.cleanup-page__summary { color: var(--text-secondary); font-size: var(--fs-caption); }

.cleanup-page__state { display: grid; gap: 6px; padding: var(--space-7) var(--space-2); color: var(--text-secondary); font-size: var(--fs-secondary); text-align: center; }
.cleanup-page__state-title { margin: 0; color: var(--text-primary); font-size: var(--fs-body); font-weight: var(--fw-semibold); }
.cleanup-page__state p { margin: 0; }
.cleanup-page__progress { justify-items: center; }
.cleanup-page__progress > .ui-progress { width: min(420px, 100%); text-align: left; }
.cleanup-page__muted { margin: 0; color: var(--text-muted); font-size: var(--fs-caption); }
.cleanup-page__path { color: var(--text-muted); font-size: var(--fs-caption); word-break: break-all; }
.cleanup-page__loading { display: grid; gap: var(--space-2); }

.cleanup-page__toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: var(--space-3); padding: 0 2px; color: var(--text-muted); font-size: var(--fs-caption); }
.cleanup-page__switch { display: inline-flex; align-items: center; gap: 6px; color: var(--text-secondary); cursor: pointer; }
.cleanup-page__hint { flex: 1; min-width: 200px; }
.cleanup-page__coverage { color: var(--warning-text); }

/* 统计卡是共用的 StatTile（D-UI-08）；前两张兼作区域切换。 */
.cleanup-tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: var(--space-3); }

/* 当前区域的标题行：标题、计数、翻页与本页操作。 */
.cleanup-area__bar { display: flex; align-items: center; flex-wrap: wrap; gap: var(--space-2) var(--space-3); padding: var(--space-1) 2px 0; scroll-margin-top: var(--space-4); }
.cleanup-area__title { margin: 0; font-size: var(--fs-section); font-weight: var(--fw-bold); }
.cleanup-area__stat { color: var(--text-muted); font-size: var(--fs-caption); }
.cleanup-area__pager { display: inline-flex; align-items: center; gap: var(--space-2); color: var(--text-secondary); font-size: var(--fs-caption); }

.cleanup-dir { display: grid; gap: 10px; }
.cleanup-dir__head { display: flex; align-items: center; gap: 10px; padding: 6px var(--space-2); border-radius: var(--radius-sm); background: var(--panel-muted-bg); }
/* 整行是折叠开关：用文字链接的无边框按钮，排版按目录行改。 */
.cleanup-dir__toggle { display: flex; flex: 1; align-items: center; gap: var(--space-2); min-width: 0; color: var(--text-primary); font-family: var(--font-mono); font-size: var(--fs-secondary); font-weight: var(--fw-regular); text-align: left; }
.cleanup-dir__toggle:hover:not(:disabled) { text-decoration: none; color: var(--accent-text); }
.cleanup-dir__chevron { color: var(--text-muted); }
.cleanup-dir__path { flex: 1; min-width: 0; word-break: break-all; }
.cleanup-dir__count { flex: none; color: var(--text-muted); font-family: inherit; font-size: var(--fs-caption); white-space: nowrap; }

.cleanup-group { display: grid; gap: 10px; padding: 14px var(--space-4); border-radius: var(--radius-lg); }
.cleanup-group--skipped { opacity: 0.55; }
.cleanup-group__head { display: flex; align-items: center; flex-wrap: wrap; gap: var(--space-2); }
.cleanup-group__spacer { flex: 1; }
.cleanup-group__kind { padding: 2px var(--space-2); border-radius: var(--radius-sm); background: var(--panel-muted-bg); color: var(--text-secondary); font-size: var(--fs-caption); }
.cleanup-group__kind--near { color: var(--accent-text); }
.cleanup-group__reason { color: var(--text-muted); font-size: var(--fs-caption); }
.cleanup-group__similarity { color: var(--text-secondary); font-size: var(--fs-caption); }
.cleanup-group__warn { padding: 2px var(--space-2); border: 1px solid var(--danger-border); border-radius: var(--radius-pill); color: var(--danger-text); font-size: var(--fs-caption); }
.cleanup-group__outcome { padding: 2px var(--space-2); border: 1px dashed var(--hairline); border-radius: var(--radius-pill); color: var(--text-secondary); font-size: var(--fs-caption); white-space: nowrap; }
.cleanup-group__skip--active { border-color: var(--accent-border); background: var(--accent-soft); color: var(--accent-text); }
.cleanup-group__exhausted { margin: 0; color: var(--danger-text); font-size: var(--fs-caption); }

.cleanup-group__members { display: flex; flex-wrap: wrap; gap: var(--space-3); }
.cleanup-member { display: flex; flex-direction: column; gap: var(--space-2); width: 260px; padding: 10px; border: 1px solid var(--border-color); border-radius: var(--radius); background: var(--panel-bg); }
.cleanup-member--keep { border-color: var(--accent-color); }
.cleanup-member--marked { border-color: var(--danger-color); }
.cleanup-member--deleted { opacity: 0.45; }
/* 缩略图整块可点（打开查看器）：用无边框的文字链接按钮，尺寸按缩略图改。 */
.cleanup-member__thumb { position: relative; display: block; width: 100%; height: 200px; border-radius: var(--radius-sm); background: var(--thumb-bg); cursor: zoom-in; overflow: hidden; }
.cleanup-member__thumb img { width: 100%; height: 100%; object-fit: contain; }
.cleanup-member__deleted-flag { position: absolute; inset: auto 6px 6px auto; padding: 2px var(--space-2); border-radius: var(--radius-pill); background: var(--media-badge-bg); color: var(--media-badge-fg); font-size: var(--fs-caption); }
.cleanup-member__choice { display: flex; align-items: center; flex-wrap: wrap; gap: 6px 14px; font-size: var(--fs-caption); }
.cleanup-member__radio, .cleanup-member__check { display: inline-flex; align-items: center; gap: 5px; color: var(--text-secondary); cursor: pointer; }
.cleanup-member__name { margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-primary); font-size: var(--fs-secondary); }
.cleanup-member__facts { display: grid; grid-template-columns: max-content 1fr; gap: 3px var(--space-2); margin: 0; font-size: var(--fs-caption); }
.cleanup-member__facts dt { color: var(--text-muted); }
.cleanup-member__facts dd { margin: 0; min-width: 0; color: var(--text-secondary); }
.cleanup-member__diff { margin-left: 5px; color: var(--danger-text); font-style: normal; }
.cleanup-member__samedir { color: var(--text-muted); }
.cleanup-member__otherdir { display: inline-flex; align-items: center; gap: var(--space-1); max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--danger-text); vertical-align: bottom; }
.cleanup-member__path { margin: 0; color: var(--text-muted); font-size: var(--fs-caption); font-family: var(--font-mono); word-break: break-all; cursor: copy; }
.cleanup-member__meta { display: flex; flex-wrap: wrap; gap: var(--space-1); }
.cleanup-member__badge { display: inline-flex; align-items: center; gap: 3px; padding: 1px 7px; border: 1px solid transparent; border-radius: var(--radius-pill); background: var(--panel-muted-bg); color: var(--text-secondary); font-size: var(--fs-caption); }
.cleanup-member__badge .ui-icon { width: 12px; height: 12px; }
.cleanup-member__badge--keepish { border-color: var(--accent-border); color: var(--accent-text); }
/* 与片库行的标签徽标同一做法：标签色只做 35% 底色，文字用正文色，浅色标签也读得清。 */
.cleanup-member__badge--tag { background: color-mix(in srgb, var(--tag-color, var(--control-bg)) 35%, transparent); color: var(--text-primary); }
.cleanup-member__locked { color: var(--warning-text); font-size: var(--fs-caption); font-weight: var(--fw-regular); }
.cleanup-member__badge--empty { color: var(--text-muted); }
.cleanup-member__actions { display: flex; flex-wrap: wrap; gap: 6px; margin-top: auto; }

/* 全屏查看器：压暗层上的控件两主题都用白字（--recap-hero-*，同观看回顾的大图层）。 */
.cleanup-viewer { position: fixed; inset: 0; z-index: var(--z-modal); display: grid; grid-template-columns: auto minmax(0, 1fr) auto; grid-template-rows: minmax(0, 1fr); align-items: center; gap: var(--space-3); padding: var(--space-7) var(--space-5) 72px; background: var(--overlay-strong); }
.cleanup-viewer__img { max-width: 100%; max-height: 100%; object-fit: contain; justify-self: center; border-radius: var(--radius-sm); }
.cleanup-viewer__close,
.cleanup-viewer__nav { border-radius: var(--radius-pill); background: var(--recap-hero-chip); color: var(--recap-hero-fg); }
.cleanup-viewer__close:hover:not(:disabled),
.cleanup-viewer__nav:hover:not(:disabled) { background: var(--recap-hero-chip-border); color: var(--recap-hero-fg); }
.cleanup-viewer__close { position: absolute; top: 14px; right: 14px; }
.cleanup-viewer__nav { width: 40px; height: 40px; }
.cleanup-viewer__nav:disabled { opacity: 0.3; cursor: default; }
.cleanup-viewer__caption { position: absolute; left: 0; right: 0; bottom: 18px; margin: 0; padding: 0 var(--space-5); color: var(--recap-hero-fg); font-size: var(--fs-caption); text-align: center; }
.cleanup-viewer__caption-path { display: block; color: var(--recap-hero-muted); font-size: var(--fs-caption); word-break: break-all; }

/* 「已忽略」列表 */
.cleanup-page__dismissed { display: grid; gap: 10px; padding: var(--space-1) 2px; }
.cleanup-page__dismissed-list { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
.cleanup-page__dismissed-item { display: flex; align-items: center; gap: 10px; padding: var(--space-2) var(--space-3); border: 1px solid var(--border-color); border-radius: var(--radius); background: var(--panel-bg); }
.cleanup-page__dismissed-media { flex: 1; min-width: 0; color: var(--text-primary); font-size: var(--fs-secondary); overflow-wrap: anywhere; }
.cleanup-page__more { justify-self: center; }

/* 删除前的汇总确认 */
.cleanup-delete-confirm p { margin: 0 0 var(--space-2); }
.cleanup-delete-confirm__warn { color: var(--warning-text); }
.cleanup-delete-confirm__merge { display: flex; align-items: center; gap: var(--space-2); margin-top: var(--space-3); font-weight: var(--fw-semibold); cursor: pointer; }
.cleanup-delete-confirm__help { margin: 6px 0 0; color: var(--text-muted); font-size: var(--fs-caption); }
</style>
