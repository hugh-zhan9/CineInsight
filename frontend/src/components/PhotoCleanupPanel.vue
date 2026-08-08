<template>
  <BaseModal class="photo-cleanup-modal" close-on-overlay stop-modal-clicks data-test="photo-cleanup-panel" @close="$emit('close')">
    <div class="photo-cleanup__header">
      <div>
        <h2>清理审阅</h2>
        <p class="photo-cleanup__intro">
          按目录展示：每个目录下列出「推荐保留项在该目录」的重复组，组内成员可能位于别的目录（会标出所在目录）。每组默认保留一份（可用左侧圆点切换保留项），与保留项同目录的其余项已默认勾选删除（可再微调）；删除后移入回收站，可随时恢复。
        </p>
      </div>
      <button type="button" class="btn-secondary" @click="$emit('close')">关闭</button>
    </div>

    <p
      v-if="resultStale"
      class="photo-cleanup__stale photo-cleanup__stale--outdated"
      data-test="cleanup-outdated-hint"
    >
      图片库在本次分析之后发生过变化，下面的结果可能已过期。你可以继续审阅，也可以点「重新分析」刷新候选。
    </p>

    <!-- 出错只当横幅提示，不能顶掉已经审阅到一半的结果。 -->
    <p v-if="displayError" class="photo-cleanup__error" role="alert" data-test="cleanup-error">{{ displayError }}</p>

    <p
      v-if="analysis && analysis.stale_hash_count > 0"
      class="photo-cleanup__stale"
      data-test="cleanup-stale-hint"
    >
      有 {{ analysis.stale_hash_count }} 张图片的指纹已过期，暂未参与近似重复检测。部分图片指纹已过期，浏览图片可自动刷新缩略图与指纹。
    </p>

    <div class="photo-cleanup__body">
      <div v-if="running" class="photo-cleanup__progress" data-test="cleanup-progress">
        <p>分析进行中 · {{ stageLabel }}</p>
        <p v-if="progress.total > 0">已处理 {{ progress.current }} / {{ progress.total }}</p>
        <p v-if="progress.message" class="photo-cleanup__muted">{{ progress.message }}</p>
        <p v-if="progress.path" class="photo-cleanup__path">当前文件：{{ progress.path }}</p>
      </div>

      <template v-else-if="analysis">
        <section
          v-for="section in directorySections"
          :key="section.directory"
          class="photo-cleanup__section"
          data-test="cleanup-dir-section"
        >
          <button
            type="button"
            class="photo-cleanup-dirgroup__dir"
            :title="section.directory"
            :aria-expanded="!isDirCollapsed(section.directory)"
            data-test="cleanup-dir-toggle"
            @click="toggleDir(section.directory)"
          >
            <span class="photo-cleanup-dirgroup__chevron">{{ isDirCollapsed(section.directory) ? '▸' : '▾' }}</span>
            <span class="photo-cleanup-dirgroup__label">目录</span>
            <span class="photo-cleanup-dirgroup__path">{{ section.directory }}</span>
            <span class="photo-cleanup-dirgroup__count">{{ section.entries.length }} 组 · 共 {{ section.imageCount }} 张</span>
          </button>

          <template v-if="!isDirCollapsed(section.directory)">
            <article
              v-for="entry in section.entries"
              :key="entry.key"
              class="photo-cleanup-card glass-surface"
              :class="{ 'photo-cleanup-card--skipped': isSkipped(entry) }"
              data-test="cleanup-group-card"
              :data-kind="entry.kind"
            >
              <div class="photo-cleanup-card__head">
                <p class="photo-cleanup-card__reason">
                  <span class="photo-cleanup-card__kind" :class="`photo-cleanup-card__kind--${entry.kind}`">
                    {{ entry.kind === 'near' ? '近似重复' : '精确重复' }}
                  </span>
                  {{ entry.group.reason }}
                </p>
                <button
                  type="button"
                  class="btn-secondary btn-compact"
                  :class="{ 'photo-cleanup-skip--active': isSkipped(entry) }"
                  data-test="cleanup-skip-group"
                  @click="toggleSkipGroup(entry)"
                >{{ isSkipped(entry) ? '已跳过（点我恢复）' : '本组不删' }}</button>
              </div>

              <label
                v-for="member in entry.members"
                :key="member.id"
                class="photo-cleanup-row"
                :class="{
                  'photo-cleanup-row--original': isKept(entry, member),
                  'photo-cleanup-row--deleted': isDeleted(member)
                }"
              >
                <input
                  type="radio"
                  class="photo-cleanup-keep-radio"
                  :name="`keep-${entry.key}`"
                  :checked="isKept(entry, member)"
                  :disabled="isDeleted(member)"
                  :aria-label="`保留 ${member.name}`"
                  data-test="cleanup-keep-toggle"
                  @change="setKeep(entry, member)"
                />
                <input
                  type="checkbox"
                  :checked="selection.includes(Number(member.id))"
                  :disabled="isKept(entry, member) || isSkipped(entry) || isDeleted(member)"
                  :aria-label="`删除 ${member.name}`"
                  data-test="cleanup-candidate-toggle"
                  @change="toggleSelection(member.id)"
                />
                <img class="photo-cleanup-thumb" :src="`/preview/image-thumbnail/${member.id}`" :alt="member.name" loading="lazy" />
                <div class="photo-cleanup-meta">
                  <span :title="member.path">{{ member.name }}</span>
                  <small>{{ describeImage(member) }}</small>
                  <!-- 组员可以不在本目录：只有落在别处时才标出它真正的目录。 -->
                  <small
                    v-if="directoryOf(member) !== section.directory"
                    class="photo-cleanup-otherdir"
                    :title="directoryOf(member)"
                    data-test="cleanup-member-otherdir"
                  >位于 {{ directoryOf(member) }}</small>
                </div>
                <span v-if="isDeleted(member)" class="photo-cleanup-keep" data-test="cleanup-member-deleted">已删除</span>
                <span v-else-if="isKept(entry, member)" class="photo-cleanup-keep">{{ keepOverrides[entry.key] != null ? '保留' : '建议保留' }}</span>
              </label>

              <div v-if="entry.kind === 'near'" class="photo-cleanup-card__actions">
                <button
                  type="button"
                  class="btn-secondary btn-compact"
                  :disabled="dismissing"
                  data-test="cleanup-dismiss-group"
                  @click="dismissGroup(entry)"
                >忽略此组</button>
              </div>
            </article>
          </template>
        </section>

        <p v-if="!hasGroups" class="photo-cleanup__empty" data-test="cleanup-empty">
          没有发现重复或近似重复的图片。
        </p>
      </template>

      <p v-else-if="!displayError" class="photo-cleanup__muted" data-test="cleanup-idle">
        尚未分析。点击"开始分析"扫描库内的精确重复与近似重复图片。
      </p>
    </div>

    <div class="photo-cleanup__footer">
      <button
        type="button"
        class="btn-secondary"
        :disabled="running || processing"
        data-test="cleanup-start"
        @click="startAnalysis"
      >
        {{ running ? '分析中...' : (analysis ? '重新分析' : '开始分析') }}
      </button>
      <button
        type="button"
        class="btn-danger"
        :disabled="selection.length === 0 || running || processing"
        data-test="cleanup-delete-selected"
        @click="deleteSelected"
      >
        {{ processing ? '处理中...' : `删除所选 (${selection.length})` }}
      </button>
    </div>
  </BaseModal>
</template>

<script>
import {
  StartImageCleanupAnalysis,
  DismissImageNearDuplicateGroup,
  BatchDeleteImages
} from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';
import { formatBytes } from '../utils/mediaDetails.js';
import { photoCleanupStore, startPhotoCleanupPolling, resumePhotoCleanupPolling, resetPhotoCleanupReview, refreshPhotoCleanupStatus } from '../utils/photoCleanupStore.js';

const STAGE_LABELS = {
  load: '读取图片记录',
  group: '按文件大小聚合',
  hash: '读取采样哈希',
  near: '比对感知哈希',
  done: '完成'
};

export default {
  name: 'PhotoCleanupPanel',
  components: { BaseModal },
  emits: ['close', 'deleted'],
  data() {
    return {
      localError: '',
      processing: false,
      dismissing: false
    };
  },
  computed: {
    // 审阅进度存在共享 store 里，关闭面板再打开可以接着上次继续。
    review() { return photoCleanupStore.review; },
    // 每组用户手动选定的保留图片 id（覆盖建议保留项）；未覆盖则用建议保留项。
    keepOverrides() { return this.review.keepOverrides; },
    // 勾选 = 待删除。切换保留项会重算（保留项不勾、其余勾上），仍可单独微调。
    selection() { return this.review.selection; },
    // 整组跳过删除的组 key 集合；跳过时该组勾选清空且禁用，可随时切回。
    skippedGroups() { return this.review.skippedGroups; },
    // 折叠状态按目录记（顶层就是目录）。
    collapsedDirs() { return this.review.collapsedDirs; },
    // 已删除的图片 id：结果不重跑，这些行置灰标注"已删除"。
    deletedIDs() { return this.review.deletedIDs; },
    // 审阅进度绑定到这一批分析：换批才重置，重开面板不重置。
    analysisKey() {
      if (!this.analysis) return '';
      // started_at 由后端每次启动分析时写入。不能拿组构成兜底：忽略某组会改变组构成，
      // 从而误判成"换了一批"，把用户的审阅进度清掉。
      return String(this.status?.started_at || '') || 'unkeyed-analysis';
    },
    // 状态由共享 store 提供；关闭面板后分析继续在后台跑，重开即恢复。
    status() { return photoCleanupStore.status; },
    running() { return !!this.status?.running; },
    analysis() { return (this.status?.completed && this.status.analysis) || null; },
    // 结果算出后图片库又变过：保留结果继续审阅，只提示可能过期。
    resultStale() { return Boolean(this.analysis && (this.status?.stale || this.deletedIDs.length)); },
    progress() { return this.status?.progress || {}; },
    stageLabel() { return STAGE_LABELS[this.progress.stage] || '准备中'; },
    displayError() { return this.localError || this.status?.error || ''; },
    // 两类候选拉平成统一条目，key 带类别：同一张图可能既是精确组也是近似组的推荐保留项。
    entries() {
      if (!this.analysis) return [];
      const flatten = (groups, kind) => (groups || []).map(group => ({
        kind,
        group,
        key: `${kind}-${group.original?.id ?? 'nogroup'}`,
        members: this.groupMembers(group)
      }));
      return [
        ...flatten(this.analysis.duplicate_groups, 'exact'),
        ...flatten(this.analysis.near_duplicate_groups, 'near')
      ];
    },
    // 顶层按目录分组：整组归到"推荐保留项"（后端 original）所在目录，
    // 所以手动切换保留项时组不会跳到别的目录去；组员仍可位于其他目录。
    directorySections() {
      const buckets = new Map();
      for (const entry of this.entries) {
        const directory = this.directoryOf(entry.group.original);
        if (!buckets.has(directory)) buckets.set(directory, { directory, entries: [], imageCount: 0 });
        const bucket = buckets.get(directory);
        bucket.entries.push(entry);
        bucket.imageCount += entry.members.length;
      }
      return [...buckets.values()].sort((a, b) => a.directory.localeCompare(b.directory));
    },
    hasGroups() {
      return this.entries.length > 0;
    },
    // 与"当前保留项"同目录的候选默认勾选待删（重复往往是同目录多出一份）。
    autoSelectedIDs() {
      const ids = [];
      for (const entry of this.entries) {
        const keepDir = this.keepDirFor(entry);
        for (const candidate of entry.group.candidates || []) {
          if (this.directoryOf(candidate) === keepDir) ids.push(Number(candidate.id));
        }
      }
      return [...new Set(ids)];
    }
  },
  watch: {
    analysisKey: {
      immediate: true,
      handler(key) {
        // 只有换了一批分析结果才重置：重置保留覆盖、按建议保留项默认勾选同目录候选、清空折叠。
        // 重开面板时 key 没变，上次审阅到哪儿就接着哪儿。
        if (!key || key === this.review.key) return;
        this.resetGroupState(key);
      }
    }
  },
  mounted() {
    // 空闲时不轮询，所以打开面板要显式拉一次：后端可能已把结果标记为过期。
    startPhotoCleanupPolling();
    refreshPhotoCleanupStatus();
  },
  methods: {
    formatBytes,
    describeImage(image) {
      const dims = image?.width && image?.height ? `${image.width}×${image.height}` : '尺寸未探测';
      return `${dims} · ${formatBytes(image?.size || 0)}`;
    },
    directoryOf(image) {
      return String(image?.directory || '').trim() || '未知目录';
    },
    groupMembers(group) {
      return [group.original, ...(group.candidates || [])].filter(Boolean);
    },
    isDeleted(image) {
      return this.deletedIDs.includes(Number(image?.id));
    },
    // 当前保留项 id：用户覆盖优先，否则建议保留项。
    keepFor(entry) {
      const override = this.keepOverrides[entry.key];
      return override != null ? Number(override) : Number(entry.group.original?.id);
    },
    keepDirFor(entry) {
      const keepID = this.keepFor(entry);
      const keeper = entry.members.find(m => Number(m.id) === Number(keepID));
      return this.directoryOf(keeper || entry.group.original);
    },
    isKept(entry, image) {
      return Number(image?.id) === this.keepFor(entry);
    },
    // 切换保留项：新保留项取消勾选，其余成员勾上待删（之后仍可单独微调）。
    setKeep(entry, image) {
      const keepID = Number(image?.id);
      this.review.keepOverrides = { ...this.keepOverrides, [entry.key]: keepID };
      if (this.isSkipped(entry)) return;
      this.review.selection = this.reselectGroup(entry, keepID);
    },
    isSkipped(entry) {
      return !!this.skippedGroups[entry.key];
    },
    // 跳过/恢复整组：跳过时清空该组所有待删勾选（一张都不删），恢复时按当前保留项重算默认勾选。
    toggleSkipGroup(entry) {
      if (this.isSkipped(entry)) {
        this.review.skippedGroups = { ...this.skippedGroups, [entry.key]: false };
        this.review.selection = this.reselectGroup(entry, this.keepFor(entry));
      } else {
        this.review.skippedGroups = { ...this.skippedGroups, [entry.key]: true };
        const memberIDs = new Set(entry.members.map(m => Number(m.id)));
        this.review.selection = this.selection.filter(id => !memberIDs.has(id));
      }
    },
    // 该组除保留项外全部勾上待删；已删除的成员不再参与勾选。
    reselectGroup(entry, keepID) {
      const memberIDs = entry.members.map(m => Number(m.id));
      const memberSet = new Set(memberIDs);
      return [...new Set([
        ...this.selection.filter(id => !memberSet.has(id)),
        ...memberIDs.filter(id => id !== Number(keepID) && !this.deletedIDs.includes(id))
      ])];
    },
    isDirCollapsed(directory) {
      return !!this.collapsedDirs[directory];
    },
    toggleDir(directory) {
      this.review.collapsedDirs = { ...this.collapsedDirs, [directory]: !this.collapsedDirs[directory] };
    },
    applyAutoSelection() {
      this.review.selection = this.autoSelectedIDs;
    },
    resetGroupState(key) {
      resetPhotoCleanupReview(key);
      this.applyAutoSelection();
    },
    async startAnalysis() {
      if (this.running || this.processing) return;
      this.localError = '';
      try {
        // 写入共享 store，后台 worker 已在服务端跑；恢复轮询接续刷新进度。
        // 启动成功才清审阅进度：失败时旧结果和勾选都得原样留着。
        const started = await StartImageCleanupAnalysis();
        photoCleanupStore.status = started;
        // 先换状态再清审阅进度：此时 entries 已空，默认勾选不会算到旧结果上。
        this.resetGroupState('');
        resumePhotoCleanupPolling();
      } catch (err) {
        this.localError = `启动分析失败：${err}`;
      }
    },
    toggleSelection(imageID) {
      const id = Number(imageID);
      this.review.selection = this.selection.includes(id)
        ? this.selection.filter(item => item !== id)
        : [...this.selection, id];
    },
    async deleteSelected() {
      if (this.selection.length === 0 || this.processing || this.running) return;
      const requested = [...this.selection];
      this.processing = true;
      this.localError = '';
      let deleted = false;
      let succeeded = [];
      let failureNotice = '';
      try {
        const result = await BatchDeleteImages(requested, true);
        const failedIDs = new Set((result?.errors || []).map(item => Number(item.image_id)));
        succeeded = requested.filter(id => !failedIDs.has(id));
        if (result?.failed > 0) {
          failureNotice = `有 ${result.failed} 张图片删除失败，其余已移入回收站。`;
        }
        // 只有真正删掉的行才置灰；失败的留着可以重试。
        this.review.deletedIDs = [...new Set([...this.deletedIDs, ...succeeded])];
        this.review.selection = requested.filter(id => failedIDs.has(id));
        deleted = true;
      } catch (err) {
        this.localError = `删除所选图片失败：${err}`;
      } finally {
        this.processing = false;
      }
      if (!deleted || succeeded.length === 0) {
        if (failureNotice) this.localError = failureNotice;
        return;
      }
      this.$emit('deleted');
      // 不再静默重跑：先问一句，让用户自己决定是继续审阅还是刷新候选。
      const rerun = window.confirm(
        `已把 ${succeeded.length} 张图片移入回收站。是否立即重新分析？\n选择"取消"可以继续审阅当前结果。`
      );
      if (rerun) {
        await this.startAnalysis();
      }
      if (failureNotice) this.localError = failureNotice;
    },
    async dismissGroup(entry) {
      if (this.dismissing) return;
      const ids = entry.members.map(image => Number(image.id));
      this.dismissing = true;
      this.localError = '';
      try {
        await DismissImageNearDuplicateGroup(ids);
        if (this.status?.analysis?.near_duplicate_groups) {
          this.status.analysis.near_duplicate_groups = this.status.analysis.near_duplicate_groups.filter(item => item !== entry.group);
        }
        this.review.selection = this.selection.filter(id => !ids.includes(id));
      } catch (err) {
        this.localError = `忽略近似重复组失败：${err}`;
      } finally {
        this.dismissing = false;
      }
    }
  }
};
</script>

<style scoped>
:deep(.photo-cleanup-modal) { width: min(860px, 92vw); max-height: 84vh; display: flex; flex-direction: column; }
.photo-cleanup__header { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.photo-cleanup__header h2 { margin: 0 0 4px; }
.photo-cleanup__intro { margin: 0; color: var(--text-muted); font-size: 12px; }
.photo-cleanup__stale { margin: 12px 0 0; padding: 8px 12px; border: 1px solid var(--border-color); border-radius: 10px; background: var(--control-bg); color: var(--text-secondary); font-size: 12px; }
.photo-cleanup__stale--outdated { border-color: var(--primary-color, #0d9488); color: var(--text-primary); }
.photo-cleanup__body { flex: 1; min-height: 120px; margin-top: 12px; overflow-y: auto; display: flex; flex-direction: column; gap: 14px; }
.photo-cleanup__progress { display: grid; gap: 6px; padding: 16px 4px; color: var(--text-primary); font-size: 13px; }
.photo-cleanup__progress p { margin: 0; }
.photo-cleanup__path { color: var(--text-muted); font-size: 11px; word-break: break-all; }
.photo-cleanup__error { margin: 0; color: var(--danger-color); }
.photo-cleanup__empty,
.photo-cleanup__muted { margin: 0; padding: 20px 4px; color: var(--text-muted); font-size: 13px; }
.photo-cleanup__section { display: grid; gap: 10px; }
.photo-cleanup-card { display: grid; gap: 8px; padding: 12px; border-radius: 12px; }
.photo-cleanup-card__head { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.photo-cleanup-card__reason { margin: 0; color: var(--text-muted); font-size: 12px; }
.photo-cleanup-card--skipped { opacity: 0.6; }
.photo-cleanup-skip--active { border-color: var(--primary-color, #0d9488); color: var(--primary-color, #0d9488); }
.photo-cleanup-card__kind { margin-right: 6px; padding: 1px 6px; border-radius: 4px; background: var(--control-bg); color: var(--text-secondary); font-size: 10px; }
.photo-cleanup-card__kind--near { color: var(--primary-color, #0d9488); }
.photo-cleanup-dirgroup__dir { display: flex; align-items: center; gap: 6px; margin: 0; padding: 5px 6px; border: none; border-radius: 6px; background: var(--control-bg); color: var(--text-secondary); font-size: 11px; font-family: var(--font-mono, monospace); text-align: left; cursor: pointer; width: 100%; box-sizing: border-box; }
.photo-cleanup-dirgroup__dir:hover { background: var(--control-bg); }
.photo-cleanup-dirgroup__chevron { flex: none; width: 12px; color: var(--text-muted); }
.photo-cleanup-dirgroup__label { flex: none; padding: 1px 6px; border-radius: 4px; background: var(--control-bg); color: var(--text-muted); font-size: 10px; font-family: inherit; }
.photo-cleanup-dirgroup__path { flex: 1; min-width: 0; word-break: break-all; }
.photo-cleanup-dirgroup__count { flex: none; color: var(--text-muted); font-size: 10px; font-family: inherit; white-space: nowrap; }
.photo-cleanup-row { display: flex; align-items: center; gap: 10px; padding: 6px 8px; border-radius: 10px; }
.photo-cleanup-row--original { background: var(--control-bg); }
.photo-cleanup-row--deleted { opacity: 0.45; text-decoration: line-through; }
.photo-cleanup-otherdir { color: var(--text-muted); font-size: 10px; font-family: var(--font-mono, monospace); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
label.photo-cleanup-row { cursor: pointer; }
label.photo-cleanup-row:hover { background: var(--control-bg); }
.photo-cleanup-thumb { width: 56px; height: 56px; flex: none; border-radius: 8px; object-fit: cover; background: var(--thumb-bg); }
.photo-cleanup-meta { display: grid; gap: 2px; min-width: 0; flex: 1; }
.photo-cleanup-meta span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; color: var(--text-primary); }
.photo-cleanup-meta small { color: var(--text-muted); font-size: 11px; }
.photo-cleanup-keep { flex: none; padding: 2px 10px; border: 1px solid var(--border-color); border-radius: 999px; color: var(--text-secondary); font-size: 11px; white-space: nowrap; }
.photo-cleanup-keep-radio { flex: none; accent-color: var(--primary-color, #0d9488); cursor: pointer; }
.photo-cleanup-card__actions { display: flex; justify-content: flex-end; }
.photo-cleanup__footer { display: flex; justify-content: flex-end; gap: 10px; margin-top: 14px; padding-top: 12px; border-top: 1px solid var(--border-color); }
</style>
