<template>
  <section class="cleanup-page" data-test="photo-cleanup-page">
    <header class="cleanup-page__bar glass-surface">
      <!-- 删除进行中不能离开（P-032 评审 Minor 5）：撤销条与「不支持废纸篓」二选一都挂在本页上。 -->
      <button
        type="button"
        class="btn-secondary"
        data-test="cleanup-back"
        :disabled="processing"
        :title="processing ? '正在移到废纸篓，完成后再返回' : ''"
        @click="$emit('close')"
      >← 返回图片库</button>
      <div class="cleanup-page__title">
        <h2>清理审阅</h2>
        <!-- 勾选规则与视频清理面板同一套（D-PC49，utils/cleanupSelection.js）。 -->
        <p class="cleanup-page__subtitle">
          精确重复默认勾选保留项以外的副本；近似重复是哈希判定，默认不勾，请逐组看过再「按建议勾选」。
          保留项锁定，要换就点另一张的「保留这份」。勾选的图片移到废纸篓，可在回收站撤销；不勾任何一张的组不会有任何改动。
        </p>
      </div>
      <div class="cleanup-page__summary" data-test="cleanup-summary">
        <strong>{{ selection.length }}</strong> 项待删 · 移到废纸篓后，在访达清空废纸篓即可释放 {{ formatBytes(reclaimableBytes) }}
      </div>
      <button
        type="button"
        class="btn-secondary"
        data-test="cleanup-dismissed-toggle"
        @click="toggleDismissed"
      >{{ showDismissed ? '返回审阅' : '已忽略' }}</button>
      <button
        type="button"
        class="btn-secondary"
        :disabled="running || processing"
        data-test="cleanup-start"
        @click="startAnalysis"
      >{{ running ? '分析中…' : (analysis ? '重新分析' : '开始分析') }}</button>
      <button
        type="button"
        class="btn-danger"
        :disabled="selection.length === 0 || running || processing || showDismissed"
        data-test="cleanup-delete-selected"
        @click="deleteSelected"
      >{{ processing ? '处理中…' : `移到废纸篓 (${selection.length})` }}</button>
    </header>

    <!-- 删除走 P-030 的删除宿主：进度与取消、磁盘不支持废纸篓时的二选一、按批次撤销。 -->
    <TrashUndoBanner ref="trashUndo" kind="image" :after-restore="afterTrashRestore" />

    <p v-if="displayError" class="cleanup-page__notice cleanup-page__notice--error" role="alert" data-test="cleanup-error">{{ displayError }}</p>
    <p v-if="resultStale" class="cleanup-page__notice cleanup-page__notice--warn" data-test="cleanup-outdated-hint">
      图片库在本次分析之后发生过变化，下面的结果可能已过期。可以继续审阅，也可以点「重新分析」刷新候选。
    </p>
    <p v-if="analysis && analysis.stale_hash_count > 0" class="cleanup-page__notice" data-test="cleanup-stale-hint">
      有 {{ analysis.stale_hash_count }} 张图片还没有可用的指纹（未回填或源文件已变更），暂未参与近似重复检测。
      <button
        type="button"
        class="btn-secondary btn-compact"
        data-test="cleanup-start-image-phash"
        :disabled="perceptualHashRunning"
        @click="startPerceptualHashBackfill"
      >{{ perceptualHashRunning ? '补全中…' : '补全指纹' }}</button>
    </p>
    <p v-if="analysis && analysis.skipped_unavailable > 0" class="cleanup-page__notice" data-test="image-cleanup-skipped-hint">
      本轮跳过 {{ analysis.skipped_unavailable }} 张图片（文件不可访问，如外置盘未挂载）。
    </p>

    <!-- 「已忽略」（D-PC31）：「不是重复」「移出本组」留下的记录，可以撤销。 -->
    <section v-if="showDismissed" class="cleanup-page__dismissed" data-test="cleanup-dismissed">
      <p class="cleanup-page__muted">
        「不是重复」和「移出本组」留下的记录。撤销后这些图片在下次分析时重新参与近似重复检测；任一文件变化后忽略本来就会自动失效。
      </p>
      <p v-if="dismissals.error" class="cleanup-page__notice cleanup-page__notice--error" data-test="cleanup-dismissed-error">{{ dismissals.error }}</p>
      <p v-if="dismissals.loading && dismissals.items.length === 0" class="cleanup-page__muted">正在读取忽略记录…</p>
      <p v-else-if="dismissals.items.length === 0 && dismissals.loaded" class="cleanup-page__muted" data-test="cleanup-dismissed-empty">还没有忽略记录。</p>
      <ul v-else-if="dismissals.items.length > 0" class="cleanup-page__dismissed-list">
        <li v-for="item in dismissals.items" :key="item.id" class="cleanup-page__dismissed-item" data-test="cleanup-dismissal-item">
          <span class="cleanup-page__dismissed-media">{{ describeDismissal(item) }}</span>
          <span class="cleanup-page__muted">{{ formatDismissalTime(item.created_at) }}</span>
          <button
            type="button"
            class="btn-secondary btn-compact"
            data-test="cleanup-dismissal-undo"
            :disabled="dismissals.undoing"
            @click="undoDismissal(item)"
          >撤销</button>
        </li>
      </ul>
      <button
        v-if="dismissals.hasMore"
        type="button"
        class="btn-secondary btn-compact"
        data-test="cleanup-dismissed-more"
        :disabled="dismissals.loading"
        @click="loadDismissals(false)"
      >{{ dismissals.loading ? '读取中…' : '加载更多' }}</button>
    </section>

    <div v-else-if="running" class="cleanup-page__state" data-test="cleanup-progress">
      <p class="cleanup-page__state-title">正在分析 · {{ stageLabel }}</p>
      <p v-if="progress.total > 0">已处理 {{ progress.current }} / {{ progress.total }}</p>
      <p v-if="progress.message" class="cleanup-page__muted">{{ progress.message }}</p>
      <p v-if="progress.path" class="cleanup-page__path">当前文件：{{ progress.path }}</p>
      <p class="cleanup-page__muted">分析会逐个读取图片文件，外置硬盘或大库场景耗时较长。可以直接返回图片库，分析会在后台继续。</p>
      <p>
        <button
          type="button"
          class="btn-secondary btn-compact"
          data-test="cleanup-cancel-analysis"
          :disabled="cancelling"
          @click="cancelAnalysis"
        >{{ cancelling ? '正在取消…' : '取消分析' }}</button>
      </p>
    </div>

    <template v-else-if="analysis">
      <div class="cleanup-page__toolbar">
        <span class="cleanup-page__stat">{{ entries.length }} 组 · {{ directorySections.length }} 个目录</span>
        <span v-if="coverageText" class="cleanup-page__coverage" data-test="cleanup-coverage">{{ coverageText }}</span>
        <button type="button" class="btn-secondary btn-compact" data-test="cleanup-toggle-all" @click="toggleAllDirectories">
          {{ allCollapsed ? '全部展开' : '全部折叠' }}
        </button>
        <label class="cleanup-page__switch" data-test="cleanup-samedir-switch">
          <input type="checkbox" :checked="sameDirOnly" :disabled="processing" @change="toggleSameDirOnly" />
          只勾选与保留项同目录的副本
        </label>
        <span class="cleanup-page__hint">跨目录的副本可能是你的备份；不想让整个目录参与审阅，把它加进图片扫描黑名单。</span>
      </div>

      <!-- 空态要分得清「没有重复」与「还没算」（D-PC50）。 -->
      <div v-if="!hasGroups" class="cleanup-page__state" data-test="cleanup-empty">
        <p class="cleanup-page__state-title">{{ emptyState.title }}</p>
        <p v-if="emptyState.detail" class="cleanup-page__muted" data-test="cleanup-empty-detail">{{ emptyState.detail }}</p>
        <p v-if="emptyState.action">
          <button
            type="button"
            class="btn-secondary btn-compact"
            data-test="cleanup-empty-start-phash"
            :disabled="perceptualHashRunning"
            @click="startPerceptualHashBackfill"
          >{{ perceptualHashRunning ? '补全中…' : '补全指纹' }}</button>
        </p>
      </div>

      <section
        v-for="section in directorySections"
        :key="section.directory"
        class="cleanup-dir"
        data-test="cleanup-dir-section"
      >
        <div class="cleanup-dir__head">
          <button
            type="button"
            class="cleanup-dir__toggle"
            :title="section.directory"
            :aria-expanded="!isDirCollapsed(section.directory)"
            data-test="cleanup-dir-toggle"
            @click="toggleDir(section.directory)"
          >
            <span class="cleanup-dir__chevron">{{ isDirCollapsed(section.directory) ? '▸' : '▾' }}</span>
            <span class="cleanup-dir__path">{{ section.directory }}</span>
            <span class="cleanup-dir__count">{{ section.entries.length }} 组 · 共 {{ section.imageCount }} 张</span>
          </button>
          <button
            v-if="section.directory !== UNKNOWN_DIRECTORY"
            type="button"
            class="btn-secondary btn-compact"
            data-test="cleanup-open-dir"
            @click="openDirectory(section.directory)"
          >打开目录</button>
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
                相似度 {{ similarityLabel(entry) }}
              </span>
              <span v-if="entry.spansDirectories" class="cleanup-group__warn" data-test="cleanup-cross-dir">跨 {{ entry.directoryCount }} 个目录</span>
              <!-- "保留这份"只是对比基准，不会连带删除别的；真正决定生死的是"删除"复选框。
                   一个都没勾时明说本组不会有任何改动，免得用户以为没勾的会被自动清掉。 -->
              <span class="cleanup-group__outcome" data-test="cleanup-group-outcome">{{ groupOutcomeLabel(entry) }}</span>
              <span class="cleanup-group__spacer"></span>
              <button
                type="button"
                class="btn-secondary btn-compact"
                :disabled="processing || isSkipped(entry) || (!isGroupFullySuggested(entry) && suggestedIDsFor(entry).length === 0)"
                data-test="cleanup-suggest-group"
                @click="toggleGroupSuggestion(entry)"
              >{{ isGroupFullySuggested(entry) ? '取消本组勾选' : '按建议勾选（保留推荐项）' }}</button>
              <button
                type="button"
                class="btn-secondary btn-compact"
                :class="{ 'cleanup-group__skip--active': isSkipped(entry) }"
                :disabled="processing"
                data-test="cleanup-skip-group"
                @click="toggleSkipGroup(entry)"
              >{{ isSkipped(entry) ? '已跳过（点我恢复）' : '本组不删' }}</button>
              <button
                v-if="entry.kind === 'near'"
                type="button"
                class="btn-secondary btn-compact"
                :disabled="dismissing || processing"
                data-test="cleanup-dismiss-group"
                @click="dismissGroup(entry)"
              >不是重复</button>
            </div>

            <div class="cleanup-group__members">
              <div
                v-for="member in entry.members"
                :key="member.id"
                class="cleanup-member"
                :class="{
                  'cleanup-member--keep': isKept(entry, member),
                  'cleanup-member--marked': selection.includes(Number(member.id)),
                  'cleanup-member--deleted': isDeleted(member)
                }"
                data-test="cleanup-member"
              >
                <button
                  type="button"
                  class="cleanup-member__thumb"
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
                      :disabled="processing || isDeleted(member)"
                      :aria-label="`保留 ${member.name}`"
                      data-test="cleanup-keep-toggle"
                      @change="setKeep(entry, member)"
                    />
                    保留这份
                  </label>
                  <label class="cleanup-member__check">
                    <input
                      type="checkbox"
                      :checked="selection.includes(Number(member.id))"
                      :disabled="processing || isKept(entry, member) || isSkipped(entry) || isDeleted(member) || isProtected(entry, member)"
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
                      ⚠ {{ directoryOf(member) }}
                    </span>
                  </dd>
                </dl>

                <p class="cleanup-member__path" :title="member.path" data-test="cleanup-member-path" @click="copyPath(member)">{{ member.path }}</p>

                <!-- 整理成果（D-PC48）：保留建议优先留收藏、评分、人物、标签多的那张。 -->
                <div class="cleanup-member__meta">
                  <span v-if="member.is_favorite" class="cleanup-member__badge cleanup-member__badge--keepish" data-test="cleanup-member-favorite">★ 已收藏</span>
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
                    class="btn-secondary btn-compact"
                    :disabled="isDeleted(member)"
                    data-test="cleanup-reveal"
                    @click="revealMember(member)"
                  >在文件管理器中定位</button>
                  <button
                    v-if="entry.kind === 'near' && entry.members.length > 2 && !isDeleted(member)"
                    type="button"
                    class="btn-secondary btn-compact"
                    :disabled="dismissing || processing"
                    data-test="cleanup-remove-member"
                    @click="removeMember(entry, member)"
                  >移出本组</button>
                </div>
              </div>
            </div>
          </article>
        </template>
      </section>
    </template>

    <div v-else class="cleanup-page__state" data-test="cleanup-idle">
      <p class="cleanup-page__state-title">{{ status?.cancelled ? '上一轮分析已取消，没有产生结果。' : '尚未分析。' }}</p>
      <p class="cleanup-page__muted">点「开始分析」扫描库内的精确重复与近似重复图片。</p>
    </div>

    <div v-if="viewer.member" class="cleanup-viewer" role="dialog" aria-modal="true" data-test="cleanup-viewer" @click.self="closeViewer">
      <button type="button" class="cleanup-viewer__close" title="关闭 (Esc)" @click="closeViewer">×</button>
      <button type="button" class="cleanup-viewer__nav cleanup-viewer__nav--prev" :disabled="viewer.index <= 0" @click="stepViewer(-1)">‹</button>
      <img class="cleanup-viewer__img" :src="`/preview/image/${viewer.member.id}`" :alt="viewer.member.name" />
      <button type="button" class="cleanup-viewer__nav cleanup-viewer__nav--next" :disabled="viewer.index >= viewer.members.length - 1" @click="stepViewer(1)">›</button>
      <p class="cleanup-viewer__caption">
        {{ viewer.member.name }} · {{ viewer.member.width }}×{{ viewer.member.height }} · {{ formatBytes(memberBytes(viewer.member)) }}
        <span class="cleanup-viewer__caption-path">{{ viewer.member.path }}</span>
      </p>
    </div>

    <!-- 删除前的汇总确认（D-PC49）与「把元数据合并到保留项」（D-PC48，默认勾选）。 -->
    <BaseModal
      v-if="deleteConfirm.show"
      class="cleanup-delete-confirm"
      aria-labelledby="photo-cleanup-delete-confirm-title"
      data-test="cleanup-delete-confirm-dialog"
      @close="answerDeleteConfirm(false)"
    >
      <h2 id="photo-cleanup-delete-confirm-title">移到废纸篓</h2>
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
      <div class="modal-actions">
        <button type="button" class="btn-secondary" data-test="cleanup-delete-cancel" @click="answerDeleteConfirm(false)">取消</button>
        <button type="button" class="btn-danger" data-test="cleanup-delete-confirm" @click="answerDeleteConfirm(true)">移到废纸篓</button>
      </div>
    </BaseModal>
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
import BaseModal from './ui/BaseModal.vue';
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
  applySuggestion, cleanupGroup, clearGroupSelection, defaultSelection, describeMergeFailure, describeSelectionKinds,
  isGroupFullySuggested, keeperOf, lockedIDs, mergePlan, pruneSelection, selectionSummary, setKeeper, similarityCount,
  suggestedIDs
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
  components: { BaseModal, TrashUndoBanner },
  // deleted：清理页改动了图片库（删除，或在撤销条 / 回收站里撤销了删除），图片库据此重载。
  emits: ['close', 'deleted'],
  data() {
    return {
      UNKNOWN_DIRECTORY,
      localError: '',
      processing: false,
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
        // 推荐保留项被移出本组时，由剩下成员里排第一的接替（后端按整理成果、画质排序）。
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
    // 勾选规则用的统一组（utils/cleanupSelection.js）。
    cleanupGroups() {
      return this.entries.map(entry => cleanupGroup({
        key: entry.key,
        kind: entry.kind,
        keeperId: entry.suggestedKeeperID,
        memberIds: entry.members.map(member => member.id),
        switchable: true
      }));
    },
    groupByKey() {
      return new Map(this.cleanupGroups.map(group => [group.key, group]));
    },
    entryByKey() {
      return new Map(this.entries.map(entry => [entry.key, entry]));
    },
    // 顶层按目录分组：整组归到"推荐保留项"（后端 original）所在目录，
    // 手动切换保留项时组不会跳走；组员仍可位于其他目录，卡片上会标出来。
    directorySections() {
      const buckets = new Map();
      for (const entry of this.entries) {
        const directory = this.directoryOf(entry.group.original);
        if (!buckets.has(directory)) buckets.set(directory, { directory, entries: [], imageIDs: new Set() });
        const bucket = buckets.get(directory);
        bucket.entries.push(entry);
        for (const member of entry.members) bucket.imageIDs.add(Number(member.id));
      }
      return [...buckets.values()]
        .map(bucket => ({ ...bucket, imageCount: bucket.imageIDs.size }))
        .sort((a, b) => a.directory.localeCompare(b.directory));
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
      return defaultSelection(this.cleanupGroups, this.selectionOptions());
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
      // dHash 是 64 位，距离越小越像；换算成百分比更直观。
      return `${Math.round((1 - distance / 64) * 100)}%（汉明距离 ${distance}/64）`;
    },
    // 每一项都跟保留项比：不用自己在几个数字之间来回换算。
    diffFor(entry, member) {
      const keeper = entry.members.find(item => Number(item.id) === this.keepFor(entry));
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
      return this.deletedIDs.includes(Number(image?.id));
    },
    // 勾选规则的参数：跨组锁定（保留项与「本组不删」的成员）、已删除的排除、「只勾同目录」由 filter 表达。
    selectionOptions(overrides = this.keepOverrides) {
      return {
        overrides,
        skipped: this.skippedKeys,
        locked: lockedIDs(this.cleanupGroups, overrides, this.skippedKeys),
        excluded: this.deletedIDs,
        filter: this.suggestionAllows
      };
    },
    suggestionAllows(id, group, overrides = this.keepOverrides) {
      if (this.skippedGroups[group.key]) return false;
      if (!this.sameDirOnly) return true;
      const entry = this.entryByKey.get(group.key);
      if (!entry) return false;
      const keepID = keeperOf(group, overrides);
      const keeper = entry.members.find(member => Number(member.id) === keepID) || entry.group.original;
      const member = entry.members.find(item => Number(item.id) === Number(id));
      return this.directoryOf(member) === this.directoryOf(keeper);
    },
    groupOf(entry) {
      return this.groupByKey.get(entry.key) || null;
    },
    // 这张图被别的组锁定：是别的组的保留项，或者在一个「本组不删」的组里。本组也不能勾它删。
    lockReason(entry, image) {
      const id = Number(image?.id);
      if (this.keepFor(entry) === id) return '';
      if (this.keeperIDs.has(id)) return '另一组要保留它';
      if (!this.isSkipped(entry) && this.skippedMemberIDs.has(id)) return '另一组设了「本组不删」';
      return '';
    },
    isProtected(entry, image) {
      return Boolean(this.lockReason(entry, image));
    },
    // 本组是否已经是"保留推荐项、其余全勾"的状态。
    isGroupFullySuggested(entry) {
      return isGroupFullySuggested(this.selection, this.groupOf(entry), this.selectionOptions());
    },
    suggestedIDsFor(entry) {
      return suggestedIDs(this.groupOf(entry), this.selectionOptions());
    },
    // 「按建议勾选」只作用于这一组（D-PC49），再点一次取消本组勾选。
    toggleGroupSuggestion(entry) {
      const group = this.groupOf(entry);
      if (!group || this.processing) return;
      this.review.selection = this.isGroupFullySuggested(entry)
        ? clearGroupSelection(this.selection, group)
        : applySuggestion(this.selection, group, this.selectionOptions());
    },
    groupOutcomeLabel(entry) {
      if (this.isSkipped(entry)) return '本组不删';
      const marked = entry.members.filter(member => this.selection.includes(Number(member.id))).length;
      return marked === 0 ? '本组暂不删除任何图片' : `本组将删除 ${marked} 张`;
    },
    keepFor(entry) {
      const keeper = keeperOf(this.groupOf(entry), this.keepOverrides);
      // 组总有保留项；拿不到时退回组内第一个成员，避免 NaN 让整组失去保留项、全部可删。
      return keeper ?? Number(entry.members[0]?.id);
    },
    isKept(entry, image) {
      return Number(image?.id) === this.keepFor(entry);
    },
    // 换保留项：新保留项移出勾选，原保留项解除锁定但不自动勾上（§9.2 裁决）；本组原先是「按建议全勾」时
    // 按新保留项重算其余成员。删除进行中不能换（P-032 评审 I-1）。
    setKeep(entry, image) {
      const group = this.groupOf(entry);
      if (!group || this.processing) return;
      const next = setKeeper({
        groups: this.cleanupGroups,
        overrides: this.keepOverrides,
        selection: this.selection,
        excluded: this.deletedIDs,
        filter: this.suggestionAllows,
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
      if (!group || this.processing) return;
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
      if (this.processing) return;
      this.review.sameDirOnly = !this.sameDirOnly;
      // 切换的是默认规则，直接按新规则重算勾选（手动微调会被覆盖，这是明示行为）。
      this.applyAutoSelection();
    },
    isDirCollapsed(directory) {
      return !!this.collapsedDirs[directory];
    },
    toggleDir(directory) {
      this.review.collapsedDirs = { ...this.collapsedDirs, [directory]: !this.collapsedDirs[directory] };
    },
    toggleAllDirectories() {
      const collapse = !this.allCollapsed;
      const next = {};
      for (const section of this.directorySections) next[section.directory] = collapse;
      this.review.collapsedDirs = next;
    },
    applyAutoSelection() {
      this.review.selection = this.autoSelectedIDs;
    },
    resetGroupState(key) {
      const sameDirOnly = this.sameDirOnly;
      resetPhotoCleanupReview(key);
      // 勾选策略是用户的偏好，不该跟着每轮分析被重置。
      this.review.sameDirOnly = sameDirOnly;
      this.applyAutoSelection();
    },
    async openDirectory(directory) {
      this.localError = '';
      try {
        await OpenImageDirectory(directory);
      } catch (err) {
        this.localError = `打开目录失败：${err}`;
      }
    },
    async revealMember(member) {
      this.localError = '';
      try {
        await RevealImage(Number(member.id));
      } catch (err) {
        this.localError = `定位文件失败：${err}`;
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
      if (this.running || this.processing) return;
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
    toggleSelection(imageID) {
      const id = Number(imageID);
      if (this.processing) return;
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
      if (this.selection.length === 0 || this.processing || this.running || this.deleteConfirm.show || this.showDismissed) return;
      // 删除前按锁定规则裁剪勾选：保留项、「本组不删」的成员、已删除、不再出现在结果里的都不送进删除。
      const requested = pruneSelection(this.selection, this.cleanupGroups, this.selectionOptions());
      if (requested.length === 0) return;
      let plan;
      try {
        plan = mergePlan(this.cleanupGroups, requested, this.selectionOptions());
      } catch (err) {
        this.localError = err?.message || String(err);
        return;
      }
      const summary = selectionSummary(this.cleanupGroups, requested, id => this.memberBytesByID.get(id) || 0);
      // 先汇总确认（D-PC49）；取消时不调用任何写入。
      const choice = await this.askDeleteConfirm(summary, plan.length > 0);
      if (!choice) return;

      this.processing = true;
      this.localError = '';
      let outcome = null;
      let failureNotice = '';
      try {
        // 删除之前按组把被删项的整理成果合并到各组保留项（D-PC48），任何一组失败都不进入删除。
        if (choice.merge) {
          for (const [index, item] of plan.entries()) {
            try {
              await MergeMediaMetadata('image', item.keeperId, item.sourceIds, item.options);
            } catch (err) {
              this.localError = describeMergeFailure(err, index, plan.length, '图片');
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
      if (this.dismissing || this.processing) return;
      const ids = entry.members.map(member => Number(member.id));
      const confirmed = await confirmAction({
        title: '不是重复',
        message: `确认这组 ${ids.length} 张图片不是重复？\n之后的分析不再把它们报为近似重复；任一文件变化后忽略自动失效，也可以在「已忽略」里撤销。`,
        confirmText: '不是重复'
      });
      if (!confirmed) return;
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
      if (this.dismissing || this.processing) return;
      const ids = entry.members.map(item => Number(item.id));
      const memberID = Number(member?.id);
      if (ids.length < 3 || !ids.includes(memberID)) return;
      const confirmed = await confirmAction({
        title: '移出本组',
        message: `把「${member.name || `图片 ${memberID}`}」移出本组？\n只记录它与组内其他 ${ids.length - 1} 张不是重复，其余成员之间的关系不变。可以在「已忽略」里撤销。`,
        confirmText: '移出本组'
      });
      if (!confirmed) return;
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
.cleanup-page { display: flex; flex-direction: column; gap: 12px; padding: 16px 20px 32px; }

.cleanup-page__bar { position: sticky; top: 0; z-index: 5; display: flex; align-items: center; gap: 14px; padding: 12px 16px; border-radius: 12px; }
.cleanup-page__title { min-width: 0; }
.cleanup-page__title h2 { margin: 0; font-size: 17px; }
.cleanup-page__subtitle { margin: 2px 0 0; color: var(--text-muted); font-size: 12px; }
.cleanup-page__summary { margin-left: auto; color: var(--text-secondary); font-size: 13px; white-space: nowrap; }
.cleanup-page__summary strong { color: var(--text-primary); font-size: 16px; }

.cleanup-page__notice { margin: 0; padding: 9px 14px; border: 1px solid var(--border-color); border-radius: 10px; background: var(--control-bg); color: var(--text-secondary); font-size: 12px; }
.cleanup-page__notice--warn { border-color: var(--accent-color); color: var(--text-primary); }
.cleanup-page__notice--error { border-color: var(--danger-color); color: var(--danger-color); }

.cleanup-page__state { display: grid; gap: 6px; padding: 48px 8px; color: var(--text-secondary); font-size: 13px; text-align: center; }
.cleanup-page__state-title { margin: 0; color: var(--text-primary); font-size: 15px; }
.cleanup-page__state p { margin: 0; }
.cleanup-page__muted { color: var(--text-muted); font-size: 12px; }
.cleanup-page__path { color: var(--text-muted); font-size: 11px; word-break: break-all; }

.cleanup-page__toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; padding: 0 2px; color: var(--text-muted); font-size: 12px; }
.cleanup-page__stat { color: var(--text-secondary); }
.cleanup-page__switch { display: inline-flex; align-items: center; gap: 6px; color: var(--text-secondary); cursor: pointer; }
.cleanup-page__hint { flex: 1; min-width: 200px; }

.cleanup-dir { display: grid; gap: 10px; }
.cleanup-dir__head { display: flex; align-items: center; gap: 10px; padding: 6px 8px; border-radius: 8px; background: var(--control-bg); }
.cleanup-dir__toggle { display: flex; align-items: center; gap: 8px; flex: 1; min-width: 0; padding: 0; border: 0; background: transparent; color: var(--text-primary); font-size: 13px; font-family: var(--font-mono, monospace); text-align: left; cursor: pointer; }
.cleanup-dir__chevron { flex: none; width: 12px; color: var(--text-muted); }
.cleanup-dir__path { flex: 1; min-width: 0; word-break: break-all; }
.cleanup-dir__count { flex: none; color: var(--text-muted); font-size: 11px; font-family: initial; white-space: nowrap; }

.cleanup-group { display: grid; gap: 10px; padding: 14px 16px; border-radius: 12px; }
.cleanup-group--skipped { opacity: 0.55; }
.cleanup-group__head { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.cleanup-group__spacer { flex: 1; }
.cleanup-group__kind { padding: 2px 8px; border-radius: 4px; background: var(--control-bg); color: var(--text-secondary); font-size: 11px; }
.cleanup-group__kind--near { color: var(--accent-color); }
.cleanup-group__reason { color: var(--text-muted); font-size: 12px; }
.cleanup-group__similarity { color: var(--text-secondary); font-size: 11px; }
.cleanup-group__warn { padding: 2px 8px; border: 1px solid var(--danger-color); border-radius: 999px; color: var(--danger-color); font-size: 11px; }
.cleanup-group__outcome { padding: 2px 8px; border: 1px dashed var(--hairline); border-radius: 999px; color: var(--text-secondary); font-size: 11px; white-space: nowrap; }
.cleanup-group__skip--active { border-color: var(--accent-color); color: var(--accent-color); }

.cleanup-group__members { display: flex; flex-wrap: wrap; gap: 12px; }
.cleanup-member { display: flex; flex-direction: column; gap: 8px; width: 260px; padding: 10px; border: 1px solid var(--border-color); border-radius: 10px; background: var(--panel-bg); }
.cleanup-member--keep { border-color: var(--accent-color); }
.cleanup-member--marked { border-color: var(--danger-color); }
.cleanup-member--deleted { opacity: 0.45; }
.cleanup-member__thumb { position: relative; display: block; width: 100%; height: 200px; padding: 0; border: 0; border-radius: 8px; background: var(--thumb-bg); cursor: zoom-in; overflow: hidden; }
.cleanup-member__thumb img { width: 100%; height: 100%; object-fit: contain; }
.cleanup-member__deleted-flag { position: absolute; inset: auto 6px 6px auto; padding: 2px 8px; border-radius: 999px; background: rgba(0, 0, 0, 0.65); color: #fff; font-size: 11px; }
.cleanup-member__choice { display: flex; align-items: center; gap: 14px; font-size: 12px; }
.cleanup-member__radio, .cleanup-member__check { display: inline-flex; align-items: center; gap: 5px; color: var(--text-secondary); cursor: pointer; }
.cleanup-member__name { margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-primary); font-size: 13px; }
.cleanup-member__facts { display: grid; grid-template-columns: max-content 1fr; gap: 3px 8px; margin: 0; font-size: 11px; }
.cleanup-member__facts dt { color: var(--text-muted); }
.cleanup-member__facts dd { margin: 0; min-width: 0; color: var(--text-secondary); }
.cleanup-member__diff { margin-left: 5px; color: var(--danger-color); font-style: normal; }
.cleanup-member__samedir { color: var(--text-muted); }
.cleanup-member__otherdir { display: inline-block; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--danger-color); vertical-align: bottom; }
.cleanup-member__path { margin: 0; color: var(--text-muted); font-size: 10px; font-family: var(--font-mono, monospace); word-break: break-all; cursor: copy; }
.cleanup-member__meta { display: flex; flex-wrap: wrap; gap: 4px; }
.cleanup-member__badge { padding: 1px 7px; border-radius: 999px; background: var(--control-bg); color: var(--text-secondary); font-size: 10px; }
.cleanup-member__badge--keepish { border: 1px solid var(--accent-color); color: var(--accent-color); }
/* 与片库行的标签徽标同一做法：标签色只做 35% 底色，文字用正文色，浅色标签也读得清。 */
.cleanup-member__badge--tag { background: color-mix(in srgb, var(--tag-color, var(--control-bg)) 35%, transparent); color: var(--text-primary); }
.cleanup-member__locked { color: var(--warning-text); font-size: 12px; font-weight: 400; }
.cleanup-member__badge--empty { color: var(--text-muted); }
.cleanup-member__actions { margin-top: auto; }

.cleanup-viewer { position: fixed; inset: 0; z-index: 300; display: grid; grid-template-columns: auto minmax(0, 1fr) auto; grid-template-rows: minmax(0, 1fr); align-items: center; gap: 12px; padding: 48px 24px 72px; background: rgba(8, 12, 20, 0.9); }
.cleanup-viewer__img { max-width: 100%; max-height: 100%; object-fit: contain; justify-self: center; border-radius: 6px; }
.cleanup-viewer__close { position: absolute; top: 14px; right: 14px; width: 34px; height: 34px; border: 0; border-radius: 999px; background: rgba(255, 255, 255, 0.16); color: #fff; font-size: 18px; cursor: pointer; }
.cleanup-viewer__nav { width: 40px; height: 40px; border: 0; border-radius: 999px; background: rgba(255, 255, 255, 0.16); color: #fff; font-size: 22px; cursor: pointer; }
.cleanup-viewer__nav:disabled { opacity: 0.3; cursor: default; }
.cleanup-viewer__caption { position: absolute; left: 0; right: 0; bottom: 18px; margin: 0; color: rgba(255, 255, 255, 0.85); font-size: 12px; text-align: center; }
.cleanup-viewer__caption-path { display: block; color: rgba(255, 255, 255, 0.5); font-size: 11px; word-break: break-all; }
.cleanup-page__coverage { color: var(--warning-text); }

/* 「已忽略」列表 */
.cleanup-page__dismissed { display: grid; gap: 10px; padding: 4px 2px; }
.cleanup-page__dismissed-list { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
.cleanup-page__dismissed-item { display: flex; align-items: center; gap: 10px; padding: 8px 12px; border: 1px solid var(--border-color); border-radius: 10px; background: var(--panel-bg); }
.cleanup-page__dismissed-media { flex: 1; min-width: 0; color: var(--text-primary); font-size: 13px; overflow-wrap: anywhere; }

/* 删除前的汇总确认 */
:deep(.cleanup-delete-confirm) { max-width: 520px; }
.cleanup-delete-confirm__warn { color: var(--warning-text); }
.cleanup-delete-confirm__merge { display: flex; align-items: center; gap: 8px; margin-top: 12px; font-weight: 600; cursor: pointer; }
.cleanup-delete-confirm__help { margin: 6px 0 0; color: var(--text-muted); font-size: 12px; }
</style>
