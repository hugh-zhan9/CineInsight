<template>
  <section class="cleanup-page" data-test="photo-cleanup-page">
    <header class="cleanup-page__bar glass-surface">
      <button type="button" class="btn-secondary" data-test="cleanup-back" @click="$emit('close')">← 返回图片库</button>
      <div class="cleanup-page__title">
        <h2>清理审阅</h2>
        <p class="cleanup-page__subtitle">
          精确重复默认勾选多余副本；近似重复交给你判断，默认不勾。删除都进回收站，可随时恢复。
        </p>
      </div>
      <div class="cleanup-page__summary" data-test="cleanup-summary">
        <strong>{{ selection.length }}</strong> 项待删 · 可释放 {{ formatBytes(reclaimableBytes) }}
      </div>
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
        :disabled="selection.length === 0 || running || processing"
        data-test="cleanup-delete-selected"
        @click="deleteSelected"
      >{{ processing ? '处理中…' : `删除所选 (${selection.length})` }}</button>
    </header>

    <p v-if="displayError" class="cleanup-page__notice cleanup-page__notice--error" role="alert" data-test="cleanup-error">{{ displayError }}</p>
    <p v-if="resultStale" class="cleanup-page__notice cleanup-page__notice--warn" data-test="cleanup-outdated-hint">
      图片库在本次分析之后发生过变化，下面的结果可能已过期。可以继续审阅，也可以点「重新分析」刷新候选。
    </p>
    <p v-if="analysis && analysis.stale_hash_count > 0" class="cleanup-page__notice" data-test="cleanup-stale-hint">
      有 {{ analysis.stale_hash_count }} 张图片的指纹已过期，暂未参与近似重复检测。浏览图片可自动刷新缩略图与指纹。
    </p>

    <div v-if="running" class="cleanup-page__state" data-test="cleanup-progress">
      <p class="cleanup-page__state-title">正在分析 · {{ stageLabel }}</p>
      <p v-if="progress.total > 0">已处理 {{ progress.current }} / {{ progress.total }}</p>
      <p v-if="progress.message" class="cleanup-page__muted">{{ progress.message }}</p>
      <p v-if="progress.path" class="cleanup-page__path">当前文件：{{ progress.path }}</p>
      <p class="cleanup-page__muted">分析会逐个读取图片文件，外置硬盘或大库场景耗时较长。可以直接返回图片库，分析会在后台继续。</p>
    </div>

    <template v-else-if="analysis">
      <div class="cleanup-page__toolbar">
        <span class="cleanup-page__stat">{{ entries.length }} 组 · {{ directorySections.length }} 个目录</span>
        <button type="button" class="btn-secondary btn-compact" data-test="cleanup-toggle-all" @click="toggleAllDirectories">
          {{ allCollapsed ? '全部展开' : '全部折叠' }}
        </button>
        <label class="cleanup-page__switch" data-test="cleanup-samedir-switch">
          <input type="checkbox" :checked="sameDirOnly" @change="toggleSameDirOnly" />
          只勾选与保留项同目录的副本
        </label>
        <span class="cleanup-page__hint">跨目录的副本可能是你的备份；不想让整个目录参与审阅，把它加进图片扫描黑名单。</span>
      </div>

      <div v-if="!hasGroups" class="cleanup-page__state" data-test="cleanup-empty">
        <p class="cleanup-page__state-title">没有发现重复或近似重复的图片。</p>
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
                {{ entry.kind === 'near' ? '近似重复' : '精确重复' }}
              </span>
              <span class="cleanup-group__reason">{{ entry.group.reason }}</span>
              <span v-if="entry.kind === 'near'" class="cleanup-group__similarity" data-test="cleanup-similarity">
                相似度 {{ similarityLabel(entry) }}
              </span>
              <span v-if="entry.spansDirectories" class="cleanup-group__warn" data-test="cleanup-cross-dir">跨 {{ entry.directoryCount }} 个目录</span>
              <span class="cleanup-group__spacer"></span>
              <button
                type="button"
                class="btn-secondary btn-compact"
                :class="{ 'cleanup-group__skip--active': isSkipped(entry) }"
                data-test="cleanup-skip-group"
                @click="toggleSkipGroup(entry)"
              >{{ isSkipped(entry) ? '已跳过（点我恢复）' : '本组不删' }}</button>
              <button
                v-if="entry.kind === 'near'"
                type="button"
                class="btn-secondary btn-compact"
                :disabled="dismissing"
                data-test="cleanup-dismiss-group"
                @click="dismissGroup(entry)"
              >忽略此组</button>
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
                      :disabled="isDeleted(member)"
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
                      :disabled="isKept(entry, member) || isSkipped(entry) || isDeleted(member)"
                      :aria-label="`删除 ${member.name}`"
                      data-test="cleanup-candidate-toggle"
                      @change="toggleSelection(member.id)"
                    />
                    删除
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

                <div class="cleanup-member__meta">
                  <span v-if="member.is_favorite" class="cleanup-member__badge cleanup-member__badge--keepish" data-test="cleanup-member-favorite">★ 已收藏</span>
                  <span v-if="member.personal_rating != null" class="cleanup-member__badge cleanup-member__badge--keepish">评分 {{ member.personal_rating }}</span>
                  <span
                    v-for="tag in member.tags || []"
                    :key="tag.id"
                    class="cleanup-member__badge cleanup-member__badge--tag"
                    :style="{ '--tag-color': tag.color }"
                    data-test="cleanup-member-tag"
                  >{{ tag.name }}</span>
                  <span v-if="!hasMetadata(member)" class="cleanup-member__badge cleanup-member__badge--empty">无标签 / 评分</span>
                </div>

                <p v-if="member.description" class="cleanup-member__description" :title="member.description" data-test="cleanup-member-description">
                  {{ member.description }}
                </p>

                <div class="cleanup-member__actions">
                  <button type="button" class="btn-secondary btn-compact" data-test="cleanup-reveal" @click="revealMember(member)">在文件管理器中定位</button>
                </div>
              </div>
            </div>
          </article>
        </template>
      </section>
    </template>

    <div v-else class="cleanup-page__state" data-test="cleanup-idle">
      <p class="cleanup-page__state-title">尚未分析。</p>
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
  </section>
</template>

<script>
import {
  StartImageCleanupAnalysis,
  DismissImageNearDuplicateGroup,
  BatchDeleteImages,
  OpenImageDirectory,
  RevealImage
} from '../../wailsjs/go/main/App';
import { formatBytes } from '../utils/mediaDetails.js';
import {
  photoCleanupStore,
  startPhotoCleanupPolling,
  resumePhotoCleanupPolling,
  resetPhotoCleanupReview,
  refreshPhotoCleanupStatus
} from '../utils/photoCleanupStore.js';

const STAGE_LABELS = {
  load: '读取图片记录',
  group: '按文件大小聚合',
  hash: '读取采样哈希',
  near: '比对感知哈希',
  done: '完成'
};

const DAY_MS = 24 * 60 * 60 * 1000;

export default {
  name: 'PhotoCleanupPage',
  emits: ['close', 'deleted'],
  data() {
    return {
      localError: '',
      processing: false,
      dismissing: false,
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
    // 只勾同目录副本：跨目录那份往往是备份，想保守一点的人可以一键切过去。
    sameDirOnly() { return !!this.review.sameDirOnly; },
    status() { return photoCleanupStore.status; },
    running() { return !!this.status?.running; },
    analysis() { return (this.status?.completed && this.status.analysis) || null; },
    resultStale() { return Boolean(this.analysis && (this.status?.stale || this.deletedIDs.length)); },
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
      const flatten = (groups, kind) => (groups || []).map(group => {
        const members = [group.original, ...(group.candidates || [])].filter(Boolean);
        const directories = new Set(members.map(member => this.directoryOf(member)));
        return {
          kind,
          group,
          members,
          key: `${kind}-${group.original?.id ?? 'nogroup'}`,
          directoryCount: directories.size,
          spansDirectories: directories.size > 1
        };
      });
      return [
        ...flatten(this.analysis.duplicate_groups, 'exact'),
        ...flatten(this.analysis.near_duplicate_groups, 'near')
      ];
    },
    // 顶层按目录分组：整组归到"推荐保留项"（后端 original）所在目录，
    // 手动切换保留项时组不会跳走；组员仍可位于其他目录，卡片上会标出来。
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
    hasGroups() { return this.entries.length > 0; },
    allCollapsed() {
      return this.directorySections.length > 0
        && this.directorySections.every(section => this.isDirCollapsed(section.directory));
    },
    // 默认勾选规则：精确重复内容就是同一份，除保留项外全勾；近似重复是判断题，一律不勾。
    // 打开"只勾同目录"后，精确重复里位于别处的副本也不勾。
    autoSelectedIDs() {
      const ids = [];
      for (const entry of this.entries) {
        if (entry.kind !== 'exact') continue;
        const keepID = this.keepFor(entry);
        const keepDir = this.keepDirFor(entry);
        for (const member of entry.members) {
          if (Number(member.id) === keepID) continue;
          if (this.sameDirOnly && this.directoryOf(member) !== keepDir) continue;
          ids.push(Number(member.id));
        }
      }
      return [...new Set(ids)];
    },
    reclaimableBytes() {
      const byID = new Map();
      for (const entry of this.entries) {
        for (const member of entry.members) byID.set(Number(member.id), this.memberBytes(member));
      }
      return this.selection.reduce((sum, id) => sum + (byID.get(Number(id)) || 0), 0);
    }
  },
  watch: {
    analysisKey: {
      immediate: true,
      handler(key) {
        // 只有换了一批分析结果才重置；返回图片库再进来时 key 没变，接着上次审阅。
        if (!key || key === this.review.key) return;
        this.resetGroupState(key);
      }
    }
  },
  mounted() {
    startPhotoCleanupPolling();
    refreshPhotoCleanupStatus();
    window.addEventListener('keydown', this.handleKeydown);
  },
  beforeUnmount() {
    window.removeEventListener('keydown', this.handleKeydown);
  },
  methods: {
    formatBytes,
    directoryOf(image) {
      return String(image?.directory || '').trim() || '未知目录';
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
      return Boolean(member?.is_favorite || member?.personal_rating != null || (member?.tags || []).length);
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
      if (keepPixels > 0 && pixels > 0 && keepPixels !== pixels) {
        const ratio = pixels / keepPixels;
        diff.resolution = ratio < 1 ? `仅 1/${Math.round(1 / ratio)}` : `${ratio.toFixed(1)}×`;
      }
      const keepBytes = this.memberBytes(keeper);
      const bytes = this.memberBytes(member);
      if (keepBytes > 0 && bytes > 0 && keepBytes !== bytes) {
        const delta = Math.round(((bytes - keepBytes) / keepBytes) * 100);
        if (delta !== 0) diff.size = delta < 0 ? `小 ${Math.abs(delta)}%` : `大 ${delta}%`;
      }
      const keepTime = this.memberTimeMs(keeper);
      const time = this.memberTimeMs(member);
      if (keepTime > 0 && time > 0) {
        const days = Math.round((time - keepTime) / DAY_MS);
        if (days !== 0) diff.time = days < 0 ? `早 ${Math.abs(days)} 天` : `晚 ${days} 天`;
      }
      return diff;
    },
    isDeleted(image) {
      return this.deletedIDs.includes(Number(image?.id));
    },
    keepFor(entry) {
      const override = this.keepOverrides[entry.key];
      return override != null ? Number(override) : Number(entry.group.original?.id);
    },
    keepDirFor(entry) {
      const keepID = this.keepFor(entry);
      const keeper = entry.members.find(member => Number(member.id) === keepID);
      return this.directoryOf(keeper || entry.group.original);
    },
    isKept(entry, image) {
      return Number(image?.id) === this.keepFor(entry);
    },
    setKeep(entry, image) {
      const keepID = Number(image?.id);
      this.review.keepOverrides = { ...this.keepOverrides, [entry.key]: keepID };
      if (this.isSkipped(entry)) return;
      this.review.selection = this.reselectGroup(entry, keepID);
    },
    isSkipped(entry) {
      return !!this.skippedGroups[entry.key];
    },
    toggleSkipGroup(entry) {
      if (this.isSkipped(entry)) {
        this.review.skippedGroups = { ...this.skippedGroups, [entry.key]: false };
        this.review.selection = this.reselectGroup(entry, this.keepFor(entry));
      } else {
        this.review.skippedGroups = { ...this.skippedGroups, [entry.key]: true };
        const memberIDs = new Set(entry.members.map(member => Number(member.id)));
        this.review.selection = this.selection.filter(id => !memberIDs.has(id));
      }
    },
    // 按当前默认规则重算该组的勾选：近似重复恢复后仍然不勾。
    reselectGroup(entry, keepID) {
      const memberIDs = entry.members.map(member => Number(member.id));
      const memberSet = new Set(memberIDs);
      const kept = this.selection.filter(id => !memberSet.has(id));
      if (entry.kind !== 'exact') return kept;
      const keepDir = this.keepDirFor(entry);
      const marked = entry.members
        .filter(member => Number(member.id) !== Number(keepID))
        .filter(member => !this.deletedIDs.includes(Number(member.id)))
        .filter(member => !this.sameDirOnly || this.directoryOf(member) === keepDir)
        .map(member => Number(member.id));
      return [...new Set([...kept, ...marked])];
    },
    toggleSameDirOnly() {
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
      try {
        const started = await StartImageCleanupAnalysis();
        photoCleanupStore.status = started;
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
      let succeeded = [];
      let failureNotice = '';
      try {
        const result = await BatchDeleteImages(requested, true);
        const failedIDs = new Set((result?.errors || []).map(item => Number(item.image_id)));
        succeeded = requested.filter(id => !failedIDs.has(id));
        if (result?.failed > 0) {
          failureNotice = `有 ${result.failed} 张图片删除失败，其余已移入回收站。`;
        }
        // 已删除的行留在结果里置灰，剩下的组可以接着审阅。
        this.review.deletedIDs = [...new Set([...this.deletedIDs, ...succeeded])];
        this.review.selection = requested.filter(id => failedIDs.has(id));
      } catch (err) {
        this.localError = `删除所选图片失败：${err}`;
        this.processing = false;
        return;
      } finally {
        this.processing = false;
      }
      if (succeeded.length === 0) {
        if (failureNotice) this.localError = failureNotice;
        return;
      }
      this.$emit('deleted');
      // 不静默重跑：先问一句，让用户决定是继续审阅还是刷新候选。
      const rerun = window.confirm(
        `已把 ${succeeded.length} 张图片移入回收站。是否立即重新分析？\n选择"取消"可以继续审阅当前结果。`
      );
      if (rerun) await this.startAnalysis();
      if (failureNotice) this.localError = failureNotice;
    },
    async dismissGroup(entry) {
      if (this.dismissing) return;
      const ids = entry.members.map(member => Number(member.id));
      this.dismissing = true;
      this.localError = '';
      try {
        await DismissImageNearDuplicateGroup(ids);
        if (this.status?.analysis?.near_duplicate_groups) {
          this.status.analysis.near_duplicate_groups =
            this.status.analysis.near_duplicate_groups.filter(item => item !== entry.group);
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
.cleanup-page { display: flex; flex-direction: column; gap: 12px; padding: 16px 20px 32px; }

.cleanup-page__bar { position: sticky; top: 0; z-index: 5; display: flex; align-items: center; gap: 14px; padding: 12px 16px; border-radius: 12px; }
.cleanup-page__title { min-width: 0; }
.cleanup-page__title h2 { margin: 0; font-size: 17px; }
.cleanup-page__subtitle { margin: 2px 0 0; color: var(--text-muted); font-size: 12px; }
.cleanup-page__summary { margin-left: auto; color: var(--text-secondary); font-size: 13px; white-space: nowrap; }
.cleanup-page__summary strong { color: var(--text-primary); font-size: 16px; }

.cleanup-page__notice { margin: 0; padding: 9px 14px; border: 1px solid var(--border-color); border-radius: 10px; background: var(--control-bg); color: var(--text-secondary); font-size: 12px; }
.cleanup-page__notice--warn { border-color: var(--primary-color, #0d9488); color: var(--text-primary); }
.cleanup-page__notice--error { border-color: var(--danger-color, #dc2626); color: var(--danger-color, #dc2626); }

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
.cleanup-group__kind--near { color: var(--primary-color, #0d9488); }
.cleanup-group__reason { color: var(--text-muted); font-size: 12px; }
.cleanup-group__similarity { color: var(--text-secondary); font-size: 11px; }
.cleanup-group__warn { padding: 2px 8px; border: 1px solid var(--danger-color, #dc2626); border-radius: 999px; color: var(--danger-color, #dc2626); font-size: 11px; }
.cleanup-group__skip--active { border-color: var(--primary-color, #0d9488); color: var(--primary-color, #0d9488); }

.cleanup-group__members { display: flex; flex-wrap: wrap; gap: 12px; }
.cleanup-member { display: flex; flex-direction: column; gap: 8px; width: 260px; padding: 10px; border: 1px solid var(--border-color); border-radius: 10px; background: var(--panel-bg); }
.cleanup-member--keep { border-color: var(--primary-color, #0d9488); }
.cleanup-member--marked { border-color: var(--danger-color, #dc2626); }
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
.cleanup-member__diff { margin-left: 5px; color: var(--danger-color, #dc2626); font-style: normal; }
.cleanup-member__samedir { color: var(--text-muted); }
.cleanup-member__otherdir { display: inline-block; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--danger-color, #dc2626); vertical-align: bottom; }
.cleanup-member__path { margin: 0; color: var(--text-muted); font-size: 10px; font-family: var(--font-mono, monospace); word-break: break-all; cursor: copy; }
.cleanup-member__meta { display: flex; flex-wrap: wrap; gap: 4px; }
.cleanup-member__badge { padding: 1px 7px; border-radius: 999px; background: var(--control-bg); color: var(--text-secondary); font-size: 10px; }
.cleanup-member__badge--keepish { border: 1px solid var(--primary-color, #0d9488); color: var(--primary-color, #0d9488); }
.cleanup-member__badge--tag { background: var(--tag-color, var(--control-bg)); color: #fff; }
.cleanup-member__badge--empty { color: var(--text-muted); }
.cleanup-member__description { margin: 0; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; color: var(--text-muted); font-size: 11px; }
.cleanup-member__actions { margin-top: auto; }

.cleanup-viewer { position: fixed; inset: 0; z-index: 300; display: grid; grid-template-columns: auto minmax(0, 1fr) auto; grid-template-rows: minmax(0, 1fr); align-items: center; gap: 12px; padding: 48px 24px 72px; background: rgba(8, 12, 20, 0.9); }
.cleanup-viewer__img { max-width: 100%; max-height: 100%; object-fit: contain; justify-self: center; border-radius: 6px; }
.cleanup-viewer__close { position: absolute; top: 14px; right: 14px; width: 34px; height: 34px; border: 0; border-radius: 999px; background: rgba(255, 255, 255, 0.16); color: #fff; font-size: 18px; cursor: pointer; }
.cleanup-viewer__nav { width: 40px; height: 40px; border: 0; border-radius: 999px; background: rgba(255, 255, 255, 0.16); color: #fff; font-size: 22px; cursor: pointer; }
.cleanup-viewer__nav:disabled { opacity: 0.3; cursor: default; }
.cleanup-viewer__caption { position: absolute; left: 0; right: 0; bottom: 18px; margin: 0; color: rgba(255, 255, 255, 0.85); font-size: 12px; text-align: center; }
.cleanup-viewer__caption-path { display: block; color: rgba(255, 255, 255, 0.5); font-size: 11px; word-break: break-all; }
</style>
