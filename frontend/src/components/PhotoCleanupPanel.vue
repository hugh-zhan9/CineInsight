<template>
  <BaseModal class="photo-cleanup-modal" close-on-overlay stop-modal-clicks data-test="photo-cleanup-panel" @close="$emit('close')">
    <div class="photo-cleanup__header">
      <div>
        <h2>清理审阅</h2>
        <p class="photo-cleanup__intro">
          精确重复（大小 + 采样哈希）与近似重复（感知哈希）两类候选，按目录分组展示。每组默认保留一份（可用左侧圆点切换保留项），与保留项同目录的其余项已默认勾选删除（可再微调）；删除后移入回收站，可随时恢复。
        </p>
      </div>
      <button type="button" class="btn-secondary" @click="$emit('close')">关闭</button>
    </div>

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

      <p v-else-if="displayError" class="photo-cleanup__error" role="alert">{{ displayError }}</p>

      <template v-else-if="analysis">
        <section v-if="analysis.duplicate_groups?.length" class="photo-cleanup__section" data-test="cleanup-exact-section">
          <h3>精确重复（{{ analysis.duplicate_groups.length }} 组）</h3>
          <article v-for="group in analysis.duplicate_groups" :key="`exact-${group.original?.id}`" class="photo-cleanup-card glass-surface" :class="{ 'photo-cleanup-card--skipped': isSkipped(group) }">
            <div class="photo-cleanup-card__head">
              <p class="photo-cleanup-card__reason">{{ group.reason }}</p>
              <button
                type="button"
                class="btn-secondary btn-compact"
                :class="{ 'photo-cleanup-skip--active': isSkipped(group) }"
                data-test="cleanup-skip-group"
                @click="toggleSkipGroup(group)"
              >{{ isSkipped(group) ? '已跳过（点我恢复）' : '本组不删' }}</button>
            </div>
            <div v-for="sub in groupByDirectory(group)" :key="sub.directory" class="photo-cleanup-dirgroup" :data-test="`cleanup-dirgroup`">
              <button
                type="button"
                class="photo-cleanup-dirgroup__dir"
                :title="sub.directory"
                :aria-expanded="!isDirCollapsed(group, sub.directory)"
                data-test="cleanup-dir-toggle"
                @click="toggleDir(group, sub.directory)"
              >
                <span class="photo-cleanup-dirgroup__chevron">{{ isDirCollapsed(group, sub.directory) ? '▸' : '▾' }}</span>
                <span class="photo-cleanup-dirgroup__label">目录</span>
                <span class="photo-cleanup-dirgroup__path">{{ sub.directory }}</span>
                <span class="photo-cleanup-dirgroup__count">{{ sub.members.length }} 张</span>
              </button>
              <template v-if="!isDirCollapsed(group, sub.directory)">
                <label
                  v-for="member in sub.members"
                  :key="member.id"
                  class="photo-cleanup-row"
                  :class="{ 'photo-cleanup-row--original': isKept(group, member) }"
                >
                  <input
                    type="radio"
                    class="photo-cleanup-keep-radio"
                    :name="`keep-${groupKey(group)}`"
                    :checked="isKept(group, member)"
                    :aria-label="`保留 ${member.name}`"
                    data-test="cleanup-keep-toggle"
                    @change="setKeep(group, member)"
                  />
                  <input
                    type="checkbox"
                    :checked="selection.includes(Number(member.id))"
                    :disabled="isKept(group, member) || isSkipped(group)"
                    :aria-label="`删除 ${member.name}`"
                    data-test="cleanup-candidate-toggle"
                    @change="toggleSelection(member.id)"
                  />
                  <img class="photo-cleanup-thumb" :src="`/preview/image-thumbnail/${member.id}`" :alt="member.name" loading="lazy" />
                  <div class="photo-cleanup-meta">
                    <span :title="member.path">{{ member.name }}</span>
                    <small>{{ describeImage(member) }}</small>
                  </div>
                  <span v-if="isKept(group, member)" class="photo-cleanup-keep">{{ keepOverrides[groupKey(group)] != null ? '保留' : '建议保留' }}</span>
                </label>
              </template>
            </div>
          </article>
        </section>

        <section v-if="analysis.near_duplicate_groups?.length" class="photo-cleanup__section" data-test="cleanup-near-section">
          <h3>近似重复（{{ analysis.near_duplicate_groups.length }} 组）</h3>
          <article v-for="group in analysis.near_duplicate_groups" :key="`near-${group.original?.id}`" class="photo-cleanup-card glass-surface" :class="{ 'photo-cleanup-card--skipped': isSkipped(group) }">
            <div class="photo-cleanup-card__head">
              <p class="photo-cleanup-card__reason">{{ group.reason }}</p>
              <button
                type="button"
                class="btn-secondary btn-compact"
                :class="{ 'photo-cleanup-skip--active': isSkipped(group) }"
                data-test="cleanup-skip-group"
                @click="toggleSkipGroup(group)"
              >{{ isSkipped(group) ? '已跳过（点我恢复）' : '本组不删' }}</button>
            </div>
            <div v-for="sub in groupByDirectory(group)" :key="sub.directory" class="photo-cleanup-dirgroup" :data-test="`cleanup-dirgroup`">
              <button
                type="button"
                class="photo-cleanup-dirgroup__dir"
                :title="sub.directory"
                :aria-expanded="!isDirCollapsed(group, sub.directory)"
                data-test="cleanup-dir-toggle"
                @click="toggleDir(group, sub.directory)"
              >
                <span class="photo-cleanup-dirgroup__chevron">{{ isDirCollapsed(group, sub.directory) ? '▸' : '▾' }}</span>
                <span class="photo-cleanup-dirgroup__label">目录</span>
                <span class="photo-cleanup-dirgroup__path">{{ sub.directory }}</span>
                <span class="photo-cleanup-dirgroup__count">{{ sub.members.length }} 张</span>
              </button>
              <template v-if="!isDirCollapsed(group, sub.directory)">
                <label
                  v-for="member in sub.members"
                  :key="member.id"
                  class="photo-cleanup-row"
                  :class="{ 'photo-cleanup-row--original': isKept(group, member) }"
                >
                  <input
                    type="radio"
                    class="photo-cleanup-keep-radio"
                    :name="`keep-${groupKey(group)}`"
                    :checked="isKept(group, member)"
                    :aria-label="`保留 ${member.name}`"
                    data-test="cleanup-keep-toggle"
                    @change="setKeep(group, member)"
                  />
                  <input
                    type="checkbox"
                    :checked="selection.includes(Number(member.id))"
                    :disabled="isKept(group, member) || isSkipped(group)"
                    :aria-label="`删除 ${member.name}`"
                    data-test="cleanup-candidate-toggle"
                    @change="toggleSelection(member.id)"
                  />
                  <img class="photo-cleanup-thumb" :src="`/preview/image-thumbnail/${member.id}`" :alt="member.name" loading="lazy" />
                  <div class="photo-cleanup-meta">
                    <span :title="member.path">{{ member.name }}</span>
                    <small>{{ describeImage(member) }}</small>
                  </div>
                  <span v-if="isKept(group, member)" class="photo-cleanup-keep">{{ keepOverrides[groupKey(group)] != null ? '保留' : '建议保留' }}</span>
                </label>
              </template>
            </div>
            <div class="photo-cleanup-card__actions">
              <button
                type="button"
                class="btn-secondary btn-compact"
                :disabled="dismissing"
                data-test="cleanup-dismiss-group"
                @click="dismissGroup(group)"
              >忽略此组</button>
            </div>
          </article>
        </section>

        <p v-if="!hasGroups" class="photo-cleanup__empty" data-test="cleanup-empty">
          没有发现重复或近似重复的图片。
        </p>
      </template>

      <p v-else class="photo-cleanup__muted" data-test="cleanup-idle">
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
import { photoCleanupStore, startPhotoCleanupPolling } from '../utils/photoCleanupStore.js';

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
      // 每组用户手动选定的保留图片 id（覆盖建议保留项）；未覆盖则用建议保留项。
      keepOverrides: {},
      // 勾选 = 待删除。切换保留项会重算（保留项不勾、其余勾上），仍可单独微调。
      selection: [],
      // 整组跳过删除的组 key 集合；跳过时该组勾选清空且禁用，可随时切回。
      skippedGroups: {},
      collapsedDirs: {},
      processing: false,
      dismissing: false
    };
  },
  computed: {
    // 状态由共享 store 提供；关闭面板后仍由图片库页持续轮询，重开即恢复。
    status() { return photoCleanupStore.status; },
    running() { return !!this.status?.running; },
    analysis() { return (this.status?.completed && this.status.analysis) || null; },
    progress() { return this.status?.progress || {}; },
    stageLabel() { return STAGE_LABELS[this.progress.stage] || '准备中'; },
    displayError() { return this.localError || this.status?.error || ''; },
    hasGroups() {
      return Boolean(this.analysis?.duplicate_groups?.length || this.analysis?.near_duplicate_groups?.length);
    },
    // 与"当前保留项"同目录的候选默认勾选待删（重复往往是同目录多出一份）。
    autoSelectedIDs() {
      if (!this.analysis) return [];
      const ids = [];
      const groups = [...(this.analysis.duplicate_groups || []), ...(this.analysis.near_duplicate_groups || [])];
      for (const group of groups) {
        const keepDir = this.keepDirFor(group);
        for (const candidate of group.candidates || []) {
          if (this.directoryOf(candidate) === keepDir) ids.push(Number(candidate.id));
        }
      }
      return [...new Set(ids)];
    }
  },
  watch: {
    analysis: {
      immediate: true,
      handler() {
        // 新分析结果出来：重置保留覆盖、按建议保留项默认勾选同目录候选、清空折叠。
        this.resetGroupState();
      }
    }
  },
  mounted() {
    // 状态轮询由图片库页统一持有；这里仅确保它在跑。
    startPhotoCleanupPolling();
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
    groupKey(group) {
      return String(group.original?.id ?? 'nogroup');
    },
    groupMembers(group) {
      return [group.original, ...(group.candidates || [])].filter(Boolean);
    },
    // 当前保留项 id：用户覆盖优先，否则建议保留项。
    keepFor(group) {
      const override = this.keepOverrides[this.groupKey(group)];
      return override != null ? Number(override) : Number(group.original?.id);
    },
    keepDirFor(group) {
      const keepID = this.keepFor(group);
      const keeper = this.groupMembers(group).find(m => Number(m.id) === Number(keepID));
      return this.directoryOf(keeper || group.original);
    },
    isKept(group, image) {
      return Number(image?.id) === this.keepFor(group);
    },
    // 切换保留项：新保留项取消勾选，其余成员勾上待删（之后仍可单独微调）。
    setKeep(group, image) {
      const keepID = Number(image?.id);
      this.keepOverrides = { ...this.keepOverrides, [this.groupKey(group)]: keepID };
      if (this.isSkipped(group)) return;
      const memberIDs = new Set(this.groupMembers(group).map(m => Number(m.id)));
      this.selection = [...new Set([
        ...this.selection.filter(id => !memberIDs.has(id)),
        ...[...memberIDs].filter(id => id !== keepID)
      ])];
    },
    isSkipped(group) {
      return !!this.skippedGroups[this.groupKey(group)];
    },
    // 跳过/恢复整组：跳过时清空该组所有待删勾选（一张都不删），恢复时按当前保留项重算默认勾选。
    toggleSkipGroup(group) {
      const key = this.groupKey(group);
      const memberIDs = new Set(this.groupMembers(group).map(m => Number(m.id)));
      if (this.isSkipped(group)) {
        this.skippedGroups = { ...this.skippedGroups, [key]: false };
        const keepID = this.keepFor(group);
        this.selection = [...new Set([
          ...this.selection.filter(id => !memberIDs.has(id)),
          ...[...memberIDs].filter(id => id !== keepID)
        ])];
      } else {
        this.skippedGroups = { ...this.skippedGroups, [key]: true };
        this.selection = this.selection.filter(id => !memberIDs.has(id));
      }
    },
    // 把一组重复候选按目录拆成子组：同一目录的图聚在一起（重复往往同目录），
    // 返回 [{ directory, original, candidates }]，保留组内"建议保留"判定。
    // 把一组成员按目录拆成子组展示；成员是否"保留"在渲染时按 keepFor 动态判定，
    // 所以切换保留项无需重算分组。
    groupByDirectory(group) {
      const buckets = new Map();
      for (const member of this.groupMembers(group)) {
        const dir = this.directoryOf(member);
        if (!buckets.has(dir)) buckets.set(dir, { directory: dir, members: [] });
        buckets.get(dir).members.push(member);
      }
      return [...buckets.values()];
    },
    dirKey(group, dir) {
      return `${group.original?.id ?? 'nogroup'}::${dir}`;
    },
    isDirCollapsed(group, dir) {
      return !!this.collapsedDirs[this.dirKey(group, dir)];
    },
    toggleDir(group, dir) {
      const key = this.dirKey(group, dir);
      this.collapsedDirs = { ...this.collapsedDirs, [key]: !this.collapsedDirs[key] };
    },
    applyAutoSelection() {
      this.selection = this.autoSelectedIDs;
    },
    resetGroupState() {
      this.keepOverrides = {};
      this.skippedGroups = {};
      this.applyAutoSelection();
      this.collapsedDirs = {};
    },
    async startAnalysis() {
      if (this.running || this.processing) return;
      this.localError = '';
      this.selection = [];
      this.keepOverrides = {};
      try {
        // 写入共享 store，后台 worker 已在服务端跑；store 轮询会接续刷新。
        photoCleanupStore.status = await StartImageCleanupAnalysis();
      } catch (err) {
        this.localError = `启动分析失败：${err}`;
        return;
      }
    },
    toggleSelection(imageID) {
      const id = Number(imageID);
      this.selection = this.selection.includes(id)
        ? this.selection.filter(item => item !== id)
        : [...this.selection, id];
    },
    async deleteSelected() {
      if (this.selection.length === 0 || this.processing || this.running) return;
      this.processing = true;
      this.localError = '';
      let deleted = false;
      let failureNotice = '';
      try {
        const result = await BatchDeleteImages([...this.selection], true);
        if (result?.failed > 0) {
          failureNotice = `有 ${result.failed} 张图片删除失败，其余已移入回收站。`;
        }
        this.selection = [];
        deleted = true;
      } catch (err) {
        this.localError = `删除所选图片失败：${err}`;
      } finally {
        this.processing = false;
      }
      if (!deleted) return;
      this.$emit('deleted');
      // 删除后后端分析结果已失效，立即重跑一次分析刷新候选。
      await this.startAnalysis();
      if (failureNotice) this.localError = failureNotice;
    },
    async dismissGroup(group) {
      if (this.dismissing) return;
      const ids = [group.original, ...(group.candidates || [])]
        .filter(Boolean)
        .map(image => Number(image.id));
      this.dismissing = true;
      this.localError = '';
      try {
        await DismissImageNearDuplicateGroup(ids);
        if (this.status?.analysis?.near_duplicate_groups) {
          this.status.analysis.near_duplicate_groups = this.status.analysis.near_duplicate_groups.filter(item => item !== group);
        }
        this.selection = this.selection.filter(id => !ids.includes(id));
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
.photo-cleanup__body { flex: 1; min-height: 120px; margin-top: 12px; overflow-y: auto; display: flex; flex-direction: column; gap: 14px; }
.photo-cleanup__progress { display: grid; gap: 6px; padding: 16px 4px; color: var(--text-primary); font-size: 13px; }
.photo-cleanup__progress p { margin: 0; }
.photo-cleanup__path { color: var(--text-muted); font-size: 11px; word-break: break-all; }
.photo-cleanup__error { margin: 0; color: var(--danger-color); }
.photo-cleanup__empty,
.photo-cleanup__muted { margin: 0; padding: 20px 4px; color: var(--text-muted); font-size: 13px; }
.photo-cleanup__section { display: grid; gap: 10px; }
.photo-cleanup__section h3 { margin: 0; font-size: 14px; color: var(--text-secondary); }
.photo-cleanup-card { display: grid; gap: 8px; padding: 12px; border-radius: 12px; }
.photo-cleanup-card__head { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.photo-cleanup-card__reason { margin: 0; color: var(--text-muted); font-size: 12px; }
.photo-cleanup-card--skipped { opacity: 0.6; }
.photo-cleanup-skip--active { border-color: var(--primary-color, #0d9488); color: var(--primary-color, #0d9488); }
.photo-cleanup-dirgroup { display: grid; gap: 4px; }
.photo-cleanup-dirgroup + .photo-cleanup-dirgroup { margin-top: 4px; padding-top: 8px; border-top: 1px dashed var(--border-color); }
.photo-cleanup-dirgroup__dir { display: flex; align-items: center; gap: 6px; margin: 0; padding: 3px 6px; border: none; border-radius: 6px; background: transparent; color: var(--text-secondary); font-size: 11px; font-family: var(--font-mono, monospace); text-align: left; cursor: pointer; width: 100%; box-sizing: border-box; }
.photo-cleanup-dirgroup__dir:hover { background: var(--control-bg); }
.photo-cleanup-dirgroup__chevron { flex: none; width: 12px; color: var(--text-muted); }
.photo-cleanup-dirgroup__label { flex: none; padding: 1px 6px; border-radius: 4px; background: var(--control-bg); color: var(--text-muted); font-size: 10px; font-family: inherit; }
.photo-cleanup-dirgroup__path { flex: 1; min-width: 0; word-break: break-all; }
.photo-cleanup-dirgroup__count { flex: none; color: var(--text-muted); font-size: 10px; font-family: inherit; white-space: nowrap; }
.photo-cleanup-row { display: flex; align-items: center; gap: 10px; padding: 6px 8px; border-radius: 10px; }
.photo-cleanup-row--original { background: var(--control-bg); }
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
