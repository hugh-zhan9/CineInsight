<template>
  <BaseModal v-if="cleanupDialog.show" class="cleanup-modal">
      <div class="cleanup-modal-header">
        <h3>清理候选审阅</h3>
        <span class="cleanup-header__meta">
          共 {{ cleanupCandidateCount }} 项候选
          <template v-if="cleanupReleasableText"> · 可释放约 <b>{{ cleanupReleasableText }}</b></template>
        </span>
        <div class="cleanup-header__spacer"></div>
        <div v-if="cleanupResultStale" class="cleanup-outdated" data-test="cleanup-outdated-hint">
          <span>视频库在本次分析之后变过，结果可能已过期</span>
          <button type="button" class="btn-secondary btn-compact" :disabled="cleanupDialog.loading || cleanupDialog.processing" @click="reanalyzeCleanupCandidates">重新分析</button>
        </div>
        <button type="button" class="cleanup-header__close" aria-label="关闭" @click="cleanupDialog.show = false">✕</button>
      </div>

      <div v-if="cleanupDialog.analysis && !cleanupDialog.loading" class="cleanup-filter-bar">
        <button
          v-for="option in cleanupCategoryOptions"
          :key="option.key"
          type="button"
          :class="['cleanup-chip', { active: cleanupCategory === option.key }]"
          :title="option.hint"
          data-test="cleanup-category"
          @click="cleanupCategory = option.key"
        >{{ option.label }}<span v-if="option.key !== 'all'" class="cleanup-chip__count">{{ option.count }}</span></button>
        <div class="cleanup-header__spacer"></div>
        <button type="button" class="btn-secondary btn-compact" :disabled="cleanupDialog.loading || cleanupDialog.processing" @click="reanalyzeCleanupCandidates">重新分析</button>
      </div>

      <div class="cleanup-modal-body">
        <div v-if="cleanupDialog.loading" class="cleanup-loading">
          <div>正在分析视频库...</div>
          <div class="cleanup-progress-meta">
            当前阶段：{{ cleanupStageLabel }}
            <span v-if="cleanupElapsedText"> · 已运行 {{ cleanupElapsedText }}</span>
          </div>
          <div v-if="cleanupProgressPercent !== null" class="cleanup-progress-meta">
            已处理 {{ cleanupDialog.progress.current }} / {{ cleanupDialog.progress.total }}
            <span> ({{ cleanupProgressPercent }}%)</span>
          </div>
          <div v-if="cleanupDialog.progress.message" class="cleanup-progress-hint">{{ cleanupDialog.progress.message }}</div>
          <div v-if="cleanupDialog.progress.path" class="cleanup-progress-path">当前文件：{{ cleanupDialog.progress.path }}</div>
          <div class="cleanup-progress-hint">该分析会逐个读取视频文件；外置硬盘、休眠磁盘或大库场景下耗时较长，长时间停留不代表已假死。</div>
        </div>
        <div v-else-if="cleanupDialog.error" class="cleanup-error">{{ cleanupDialog.error }}</div>
        <div v-else-if="cleanupDialog.analysis" class="cleanup-body">
          <div v-if="cleanupDialog.analysis.stale_hash_count" class="cleanup-section cleanup-stale-hash-hint">
            <span>有 {{ cleanupDialog.analysis.stale_hash_count }} 个视频的源文件已变更，感知哈希待重算，暂未参与近似重复检测。</span>
            <button type="button" class="btn-secondary btn-compact" :disabled="perceptualHashRunning" @click="$emit('start-perceptual-hash')">
              {{ perceptualHashRunning ? '重算中...' : '重算感知哈希' }}
            </button>
          </div>
          <div v-if="cleanupDialog.analysis.stale_frame_hash_count" class="cleanup-section cleanup-stale-hash-hint" data-test="cleanup-stale-frame-hash-hint">
            <span>有 {{ cleanupDialog.analysis.stale_frame_hash_count }} 个视频还没有帧哈希（或源文件已变更），暂未参与截取片段识别。</span>
            <button type="button" class="btn-secondary btn-compact" data-test="cleanup-start-frame-hash" :disabled="frameHashRunning" @click="$emit('start-frame-hash')">
              {{ frameHashRunning ? '补全中...' : '补全帧哈希' }}
            </button>
          </div>

          <!-- 左侧目录承担分组，右侧只放当前目录的候选流（原型 A5）。
               候选归到"建议保留项"所在目录，组员可以位于别的目录。 -->
          <div class="cleanup-split">
            <nav class="cleanup-dirs" aria-label="按目录分组">
              <div class="cleanup-dirs__heading">按目录分组</div>
              <button
                v-for="section in cleanupFilteredSections"
                :key="section.directory"
                type="button"
                :class="['cleanup-dirs__item', { active: section.directory === activeCleanupDirectory }]"
                :title="section.directory"
                data-test="cleanup-dir-section"
                @click="activeCleanupDirectory = section.directory"
              >
                <span class="cleanup-dirs__path">{{ section.directory }}</span>
                <span class="cleanup-dirs__count">{{ section.entries.length }}</span>
              </button>
              <div class="cleanup-dirs__spacer"></div>
              <p class="cleanup-dirs__note">默认不勾选任何一项。逐条或整组勾选后统一移入回收站，回收站里可原路撤销。</p>
            </nav>

            <div class="cleanup-stream">
          <div
            v-for="section in activeCleanupSections"
            :key="section.directory"
            class="cleanup-section"
          >
            <div class="cleanup-section__head">
              <strong :title="section.directory">{{ section.directory }}</strong>
              <span>{{ section.entries.length }} 项候选 · {{ section.videoCount }} 个视频</span>
              <div class="cleanup-header__spacer"></div>
              <button type="button" class="link-btn" data-test="cleanup-suggest-group" @click="selectSuggestedInSection(section)">按建议勾选本组（保留最高画质版本）</button>
            </div>

            <template v-if="true">
              <div
                v-for="entry in section.entries"
                :key="entry.key"
                class="cleanup-card"
                data-test="cleanup-group-card"
                :data-kind="entry.kind"
              >
                <div class="cleanup-card-kind">{{ cleanupKindLabel(entry.kind) }}</div>

                <!-- 疑似同源：保留项不给勾选框，只清理可替代版本。 -->
                <template v-if="entry.kind === 'same-source'">
                  <div class="cleanup-select-row cleanup-select-row--original">
                    <CleanupThumbnail :video="entry.keeper" @preview="previewCleanupVideo" />
                    <strong>建议保留：</strong>
                    <div class="cleanup-item-text">
                      <span class="cleanup-item-main">{{ cleanupItemSummary(entry.keeper) }}</span>
                      <span v-if="entry.keeper?.path" class="cleanup-item-path" :title="entry.keeper.path">{{ entry.keeper.path }}</span>
                    </div>
                    <div class="cleanup-item-actions">
                      <button type="button" class="btn-secondary btn-compact" @click="previewCleanupVideo(entry.keeper)">预览保留项</button>
                    </div>
                  </div>
                  <p><strong>判断：</strong>{{ entry.group.reason }}<span v-if="entry.group.confidence"> · 置信度 {{ entry.group.confidence }}</span></p>
                  <div class="cleanup-select-row">
                    <input
                      type="checkbox"
                      :checked="isCleanupSelected(entry.group.alternative?.id)"
                      :disabled="isCleanupTrashed(entry.group.alternative)"
                      @change="toggleCleanupSelection(entry.group.alternative?.id)"
                    />
                    <CleanupThumbnail :video="entry.group.alternative" @preview="previewCleanupVideo" />
                    <span class="cleanup-item-text">
                      <span class="cleanup-item-main">可清理版本：{{ entry.group.alternative?.name }} · 预计释放 {{ formatFileSize(entry.group.estimated_savings) }}</span>
                      <span v-if="entry.group.alternative?.path" class="cleanup-item-path" :title="entry.group.alternative.path">{{ entry.group.alternative.path }}</span>
                      <span v-if="cleanupVideoDirectory(entry.group.alternative) !== section.directory" class="cleanup-item-otherdir" data-test="cleanup-member-otherdir">位于 {{ cleanupVideoDirectory(entry.group.alternative) }}</span>
                    </span>
                    <span class="cleanup-item-actions">
                      <button type="button" class="btn-secondary btn-compact" @click="previewCleanupVideo(entry.group.alternative)">预览该版本</button>
                      <button type="button" class="btn-secondary btn-compact" @click="rejectCleanupSameSource(entry.group)">不是同源</button>
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
                        </div>
                      </div>
                      <div class="cleanup-item-actions">
                        <button type="button" class="btn-secondary btn-compact" @click="previewCleanupVideo(entry.group.full)">预览完整片</button>
                      </div>
                    </div>
                    <div class="cleanup-clip-side">
                      <div class="cleanup-clip-side__head">B · 截取片段（可清理）</div>
                      <div class="cleanup-select-row" :class="{ 'cleanup-select-row--trashed': isCleanupTrashed(entry.group.clip) }">
                        <input
                          type="checkbox"
                          data-test="cleanup-clip-select"
                          :checked="isCleanupSelected(entry.group.clip?.id)"
                          :disabled="isCleanupTrashed(entry.group.clip)"
                          @change="toggleCleanupSelection(entry.group.clip?.id)"
                        />
                        <CleanupThumbnail :video="entry.group.clip" @preview="previewCleanupVideo" />
                        <div class="cleanup-item-text">
                          <span class="cleanup-item-main">{{ cleanupItemSummary(entry.group.clip) }}</span>
                          <span v-if="entry.group.clip?.path" class="cleanup-item-path" :title="entry.group.clip.path">{{ entry.group.clip.path }}</span>
                          <span v-if="cleanupVideoDirectory(entry.group.clip) !== section.directory" class="cleanup-item-otherdir" data-test="cleanup-member-otherdir">位于 {{ cleanupVideoDirectory(entry.group.clip) }}</span>
                        </div>
                      </div>
                      <div class="cleanup-item-actions">
                        <button type="button" class="btn-secondary btn-compact" @click="previewCleanupVideo(entry.group.clip)">预览片段</button>
                        <button type="button" class="btn-secondary btn-compact" data-test="cleanup-dismiss-clip" @click="dismissClipCandidate(entry.group)">忽略</button>
                      </div>
                    </div>
                  </div>
                  <p data-test="cleanup-clip-evidence">
                    <strong>判断：</strong>B 对上的是 A 从 {{ formatClipOffset(entry.group.offset_seconds) }} 开始的这一段，逐帧命中率 {{ formatClipMatchRate(entry.group.match_rate) }}
                    · 删除 B 可释放 {{ formatFileSize(entry.group.estimated_savings) }}
                  </p>
                </template>

                <!-- 精确重复 / 近似重复 -->
                <template v-else-if="entry.kind === 'exact' || entry.kind === 'near'">
                  <div class="cleanup-select-row cleanup-select-row--original" :class="{ 'cleanup-select-row--trashed': isCleanupTrashed(entry.keeper) }">
                    <input
                      type="checkbox"
                      :checked="isCleanupSelected(entry.keeper?.id)"
                      :disabled="isCleanupTrashed(entry.keeper)"
                      @change="toggleCleanupSelection(entry.keeper?.id)"
                    />
                    <CleanupThumbnail :video="entry.keeper" @preview="previewCleanupVideo" />
                    <strong>建议保留：</strong>
                    <div class="cleanup-item-text">
                      <span class="cleanup-item-main">{{ cleanupItemSummary(entry.keeper) }}</span>
                      <span v-if="entry.keeper?.path" class="cleanup-item-path" :title="entry.keeper.path">{{ entry.keeper.path }}</span>
                    </div>
                    <div class="cleanup-item-actions">
                      <button type="button" class="btn-secondary btn-compact" @click="previewCleanupVideo(entry.keeper)">预览</button>
                      <button v-if="entry.kind === 'near'" type="button" class="btn-secondary btn-compact" @click="dismissNearDuplicateGroup(entry.group)">不是同片</button>
                    </div>
                  </div>
                  <p><strong>原因：</strong>{{ entry.group.reason }}</p>
                  <ul>
                    <li v-for="candidate in entry.group.candidates || []" :key="`${entry.key}-${candidate.id}`">
                      <div class="cleanup-select-row" :class="{ 'cleanup-select-row--trashed': isCleanupTrashed(candidate) }">
                        <input
                          type="checkbox"
                          :checked="isCleanupSelected(candidate.id)"
                          :disabled="isCleanupTrashed(candidate)"
                          @change="toggleCleanupSelection(candidate.id)"
                        />
                        <CleanupThumbnail :video="candidate" @preview="previewCleanupVideo" />
                        <span class="cleanup-item-text">
                          <span class="cleanup-item-main">{{ cleanupItemSummary(candidate) }}</span>
                          <span v-if="candidate.path" class="cleanup-item-path" :title="candidate.path">{{ candidate.path }}</span>
                          <span v-if="cleanupVideoDirectory(candidate) !== section.directory" class="cleanup-item-otherdir" data-test="cleanup-member-otherdir">位于 {{ cleanupVideoDirectory(candidate) }}</span>
                        </span>
                        <span class="cleanup-item-actions">
                          <button type="button" class="btn-secondary btn-compact" @click="previewCleanupVideo(candidate)">预览</button>
                        </span>
                      </div>
                    </li>
                  </ul>
                </template>

                <!-- 低清视频 / 短视频：单条候选，没有保留项。 -->
                <template v-else>
                  <div class="cleanup-select-row">
                    <input
                      type="checkbox"
                      :checked="isCleanupSelected(entry.keeper?.id)"
                      :disabled="isCleanupTrashed(entry.keeper)"
                      @change="toggleCleanupSelection(entry.keeper?.id)"
                    />
                    <CleanupThumbnail :video="entry.keeper" @preview="previewCleanupVideo" />
                    <span class="cleanup-item-text">
                      <span class="cleanup-item-main">{{ cleanupItemSummary(entry.keeper) }}</span>
                      <span v-if="entry.keeper?.path" class="cleanup-item-path" :title="entry.keeper.path">{{ entry.keeper.path }}</span>
                    </span>
                    <span class="cleanup-item-actions">
                      <button type="button" class="btn-secondary btn-compact" @click="previewCleanupVideo(entry.keeper)">预览</button>
                    </span>
                  </div>
                </template>
              </div>
            </template>
          </div>

          <div v-if="cleanupCandidateCount === 0" class="cleanup-empty">
            当前没有命中轻量清理规则的候选项。
          </div>
            </div>
          </div>
        </div>
      </div>

      <!-- 底栏常显将要发生什么，以及"这一步可撤销"这件事 -->
      <div class="cleanup-modal-footer">
        <span class="cleanup-footer__summary">
          将移入回收站 <b>{{ cleanupSelection.length }}</b> 项
          <template v-if="cleanupSelectedSizeText"> · 释放 <b>{{ cleanupSelectedSizeText }}</b></template>
        </span>
        <span class="cleanup-footer__hint">移入回收站可撤销，不会立即删除磁盘文件</span>
        <div class="cleanup-header__spacer"></div>
        <button v-if="cleanupDialog.loading" @click="cleanupDialog.show = false" class="btn-secondary">后台继续分析</button>
        <button @click="cleanupDialog.show = false" class="btn-secondary">取消</button>
        <button
          @click="trashSelectedCleanupCandidates"
          class="btn-danger"
          :disabled="cleanupSelection.length === 0 || cleanupDialog.loading || cleanupDialog.processing"
        >
          {{ cleanupDialog.processing ? '处理中...' : '移入回收站' }}
        </button>
      </div>
  </BaseModal>
</template>

<script>
import { GetCleanupStatus, StartCleanupAnalysis, DismissNearDuplicateGroup, DismissClipCandidate, RejectSameSourceRelation, PreviewExternally } from '../../../wailsjs/go/main/App';
import BaseModal from '../ui/BaseModal.vue';
import CleanupThumbnail from '../CleanupThumbnail.vue';
import { confirmAction, notifyError } from '../../utils/feedback.js';
import { runtimeEventsMixin } from './runtimeEvents.js';
import { formatElapsedDuration } from './format.js';

// 清理候选审阅面板：后台分析状态、按目录分组的候选流、勾选与移入回收站。
// 父组件通过 ref 调用 open() / refreshStatus() / forgetTrashed()；需要动片库的那一步
// （批量删除 + 撤销提示条 + 重载列表）经 trashVideos 函数 prop 回到父组件，
// 这样原来的 await 顺序不用拆散。
export default {
  name: 'CleanupReviewPanel',
  components: { BaseModal, CleanupThumbnail },
  mixins: [runtimeEventsMixin],
  props: {
    // 「重算感知哈希」按钮的运行态来自父组件的后台任务状态条。
    perceptualHashRunning: { type: Boolean, default: false },
    // 「补全帧哈希」按钮的运行态同样来自父组件的后台任务状态条。
    frameHashRunning: { type: Boolean, default: false },
    trashVideos: { type: Function, required: true },
    afterTrashVideos: { type: Function, required: true }
  },
  emits: ['badge-change', 'analyzing-change', 'start-perceptual-hash', 'start-frame-hash', 'same-source-rejected', 'trash-settled'],
  data() {
    return {
      cleanupDialog: {
        show: false,
        loading: false,
        processing: false,
        analysis: null,
        error: '',
        // stale：结果算出后视频库又变过，提示可能过期但保留结果继续审阅。
        stale: false,
        progress: { stage: '', message: '', current: 0, total: 0, path: '' }
      },
      cleanupSelection: [],
      cleanupCategory: 'all',
      activeCleanupDirectory: '',
      cleanupCollapsedDirs: {},
      // 本次审阅中已移入回收站的视频 id：结果不重跑，用来提示结果已过期。
      cleanupTrashedIDs: [],
      cleanupStartedAt: 0,
      cleanupNow: Date.now(),
      cleanupTimer: null,
    };
  },
  mounted() {
    // 回到列表页时先取一次清理分析状态，后台跑出来的结果才能在按钮徽标上提醒。
    this.refreshCleanupStatus();
    // 面板关闭（后台继续分析）时也要处理：否则后台跑完没人记录结果，
    // 重新打开只能看到停在关闭那一刻的旧进度。
    this.registerRuntimeEvent('cleanup-progress', async (data) => {
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
          const status = await GetCleanupStatus();
          this.applyCleanupStatus(status);
        } catch (err) {
          console.error('读取清理分析结果失败:', err);
          this.cleanupDialog.loading = false;
          this.resetCleanupProgressTracking();
        }
      }
    });
  },
  beforeUnmount() {
    this.resetCleanupProgressTracking();
  },
  // 管理菜单的徽标与「分析中」文案仍由父组件渲染，这里把两个值镜像出去。
  watch: {
    cleanupBadgeCount: { handler(count) { this.$emit('badge-change', count); }, immediate: true },
    'cleanupDialog.loading': { handler(loading) { this.$emit('analyzing-change', loading); }, immediate: true }
  },
  computed: {
    cleanupCandidateCount() {
      return this.getAllCleanupCandidates().length;
    },
    cleanupResultStale() {
      return Boolean(this.cleanupDialog.analysis && (this.cleanupDialog.stale || this.cleanupTrashedIDs.length));
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
    cleanupCategoryOptions() {
      const analysis = this.cleanupDialog.analysis;
      const count = key => this.cleanupDirectorySections
        .reduce((total, section) => total + section.entries.filter(entry => entry.kind === key).length, 0);
      if (!analysis) return [];
      // 判定阈值挂在各自的类别上——「低清到底指多低」这个疑问就产生在这里。
      return [
        { key: 'all', label: '全部类别', count: this.cleanupCandidateCount, hint: '选中的视频会移入回收站并从库中移除，可原路撤销' },
        { key: 'exact', label: '精确重复', count: count('exact'), hint: '大小 + 采样哈希完全一致' },
        { key: 'near', label: '近似重复', count: count('near'), hint: '多帧感知哈希接近' },
        { key: 'same-source', label: '同源视频', count: count('same-source'), hint: '同一片源的不同转码或裁剪版本' },
        { key: 'clip', label: '截取片段', count: count('clip'), hint: '完整片里截下来的一段：逐帧哈希对齐命中（不会默认选中）' },
        { key: 'low-resolution', label: '低清', count: count('low-resolution'), hint: '低清视频：分辨率低于 480x320' },
        { key: 'low-duration', label: '短视频', count: count('low-duration'), hint: '短视频：时长 < 5 秒' }
      ];
    },
    // 类别筛选只收窄看到的候选，不改变分析结果本身。
    cleanupFilteredSections() {
      if (this.cleanupCategory === 'all') return this.cleanupDirectorySections;
      return this.cleanupDirectorySections
        .map(section => ({
          ...section,
          entries: section.entries.filter(entry => entry.kind === this.cleanupCategory)
        }))
        .filter(section => section.entries.length > 0);
    },
    activeCleanupSections() {
      const sections = this.cleanupFilteredSections;
      if (sections.length === 0) return [];
      const active = sections.find(section => section.directory === this.activeCleanupDirectory);
      return [active || sections[0]];
    },
    // 底栏的"释放多少"只算真正选中的那些视频，不含建议保留项。
    cleanupSelectedSizeText() {
      const selected = new Set(this.cleanupSelection);
      let bytes = 0;
      for (const section of this.cleanupDirectorySections) {
        for (const entry of section.entries) {
          for (const member of entry.members) {
            if (selected.has(member.id)) bytes += Number(member.size || 0);
          }
        }
      }
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
  },
  methods: {
    open() {
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
      if (kind === 'low-resolution') return '低清视频';
      return '短视频';
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
    // 结果不重跑，已移入回收站的行留在原地但置灰禁选，避免重复删已删的项。
    isCleanupTrashed(video) {
      return this.cleanupTrashedIDs.includes(Number(video?.id));
    },
    isCleanupDirCollapsed(directory) {
      return !!this.cleanupCollapsedDirs[directory];
    },
    toggleCleanupDir(directory) {
      this.cleanupCollapsedDirs = { ...this.cleanupCollapsedDirs, [directory]: !this.cleanupCollapsedDirs[directory] };
    },
    applyCleanupStatus(status) {
      if (!status) return;
      this.cleanupDialog.loading = !!status.running;
      this.cleanupDialog.error = status.error || '';
      this.cleanupDialog.stale = !!status.stale;
      this.cleanupDialog.analysis = status.analysis || null;
      this.cleanupDialog.progress = status.progress || { stage: '', message: '', current: 0, total: 0, path: '' };
      if (status.running) {
        this.startCleanupProgressTracking(status.started_at);
      } else {
        this.resetCleanupProgressTracking();
        this.cleanupDialog.progress = status.progress || this.cleanupDialog.progress;
      }
    },
    // 只读地同步一次后端状态，用于按钮徽标；不打开面板、不触发分析。
    async refreshCleanupStatus() {
      try {
        const status = await GetCleanupStatus();
        if (status?.running || status?.completed) {
          this.applyCleanupStatus(status);
        }
      } catch (err) {
        console.error('读取清理分析状态失败:', err);
      }
    },
    async openCleanupDialog() {
      this.cleanupDialog.show = true;
      try {
        const status = await GetCleanupStatus();
        if (status?.running || status?.completed) {
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
      this.cleanupDialog.show = true;
      try {
        await this.startNewCleanupAnalysis();
      } catch (err) {
        console.error('重新分析清理候选失败:', err);
        this.cleanupDialog.error = '重新分析清理候选失败: ' + err;
        this.cleanupDialog.loading = false;
      }
    },
    async startNewCleanupAnalysis() {
      this.cleanupSelection = [];
      this.cleanupCollapsedDirs = {};
      this.cleanupTrashedIDs = [];
      this.cleanupDialog.loading = true;
      this.cleanupDialog.processing = false;
      this.cleanupDialog.analysis = null;
      this.cleanupDialog.error = '';
      this.cleanupDialog.stale = false;
      this.cleanupDialog.progress = { stage: 'load', message: '正在准备清理候选分析…', current: 0, total: 0, path: '' };
      this.startCleanupProgressTracking();
      const started = await StartCleanupAnalysis(5, 480, 320);
      this.applyCleanupStatus(started);
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
      if (!videoID) return;
      if (this.isCleanupSelected(videoID)) {
        this.cleanupSelection = this.cleanupSelection.filter(id => id !== videoID);
        return;
      }
      this.cleanupSelection = [...this.cleanupSelection, videoID];
    },
    getSelectAllCleanupCandidates() {
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
      for (const group of analysis.same_source_groups || []) {
        if (group.alternative?.id) {
          byID.set(group.alternative.id, group.alternative);
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
    selectAllCleanupCandidates() {
      // 已移入回收站的项行内已禁选，全选也必须跳过，否则会对着已删的视频再删一次。
      this.cleanupSelection = this.getSelectAllCleanupCandidates()
        .map(video => video.id)
        .filter(id => !this.cleanupTrashedIDs.includes(Number(id)));
    },
    clearCleanupSelection() {
      this.cleanupSelection = [];
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
    async dismissNearDuplicateGroup(group) {
      const ids = [group.original?.id, ...(group.candidates || []).map(video => video.id)].filter(Boolean);
      if (ids.length < 2) return;
      try {
        await DismissNearDuplicateGroup(ids);
        this.cleanupDialog.analysis.near_duplicate_groups = (this.cleanupDialog.analysis.near_duplicate_groups || [])
          .filter(item => item !== group);
        this.cleanupSelection = this.cleanupSelection.filter(id => !ids.includes(id));
      } catch (err) {
        notifyError('忽略近似重复组失败: ' + err);
      }
    },
    // 截取片段的"忽略"：双方文件都不变时后续分析不再报出（D-028）。
    async dismissClipCandidate(group) {
      const fullID = group?.full?.id;
      const clipID = group?.clip?.id;
      if (!fullID || !clipID) return;
      try {
        await DismissClipCandidate(fullID, clipID);
        this.cleanupDialog.analysis.clip_groups = (this.cleanupDialog.analysis.clip_groups || [])
          .filter(item => item !== group);
        this.cleanupSelection = this.cleanupSelection.filter(id => id !== clipID);
      } catch (err) {
        notifyError('忽略截取片段失败: ' + err);
      }
    },
    async rejectCleanupSameSource(group) {
      if (!group?.relation_id) return;
      try {
        await RejectSameSourceRelation(group.relation_id);
        this.cleanupDialog.analysis.same_source_groups = (this.cleanupDialog.analysis.same_source_groups || [])
          .filter(item => item.relation_id !== group.relation_id);
        if (group.alternative?.id) {
          this.cleanupSelection = this.cleanupSelection.filter(id => id !== group.alternative.id);
        }
        this.$emit('same-source-rejected');
      } catch (err) {
        notifyError('更新同源判断失败: ' + err);
      }
    },
    async trashSelectedCleanupCandidates() {
      const selectedVideos = this.getAllCleanupCandidates()
        .filter(video => this.cleanupSelection.includes(video.id) && !this.isCleanupTrashed(video));
      if (selectedVideos.length === 0) {
        return;
      }
      const selectedIDs = selectedVideos.map(video => video.id);

      this.cleanupDialog.processing = true;
      try {
        // 片库侧的动作分两步交回父组件，顺序与拆分前一致：先删除并把行摘出列表，
        // 当场收窄勾选（失败重试时才不会对着已进回收站的视频再删一次），
        // 然后才是撤销提示条与列表重载。
        const { result, failedIDs, succeededIDs } = await this.trashVideos(selectedIDs);
        this.cleanupSelection = selectedIDs.filter(id => failedIDs.has(id));
        await this.afterTrashVideos(succeededIDs);
        // 已清理的项留在结果里，只标记结果可能过期；剩下的候选还能接着审阅。
        this.cleanupTrashedIDs = [...new Set([...this.cleanupTrashedIDs, ...succeededIDs])];
        if (result?.failed > 0) {
          const firstError = result.errors?.[0];
          notifyError(`批量清理完成：成功 ${result.succeeded} 个，失败 ${result.failed} 个。${firstError ? `\n首个失败：视频 ${firstError.video_id}，${firstError.error}` : ''}`);
        }
        // 不再静默重跑：先问一句，让用户自己决定是继续审阅还是刷新候选。
        if (succeededIDs.length > 0 && await confirmAction({
          title: '重新分析清理候选',
          message: `已把 ${succeededIDs.length} 个视频移入回收站。是否立即重新分析？\n选择"取消"可以继续审阅当前结果。`,
          confirmText: '重新分析'
        })) {
          await this.reanalyzeCleanupCandidates();
        }
      } catch (err) {
        console.error('批量清理失败:', err);
        notifyError('批量清理失败: ' + err);
      } finally {
        this.cleanupDialog.processing = false;
        this.$emit('trash-settled', selectedIDs);
      }
    },
    // 「按建议勾选本组」只勾非保留项；默认零选中这条边界不受影响，
    // 用户必须显式点一次才会有选中项。
    selectSuggestedInSection(section) {
      const ids = [];
      for (const entry of section.entries) {
        for (const member of entry.members) {
          if (entry.keeper && member.id === entry.keeper.id) continue;
          ids.push(member.id);
        }
      }
      this.cleanupSelection = [...new Set([...this.cleanupSelection, ...ids])];
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
:deep(.cleanup-modal) {
  width: min(920px, calc(100vw - 32px));
  max-width: calc(100vw - 32px);
  max-height: calc(100vh - 48px);
  overflow: hidden;
  padding: 0;
  display: flex;
  flex-direction: column;
}
/* 清理弹窗（原型 A5）：顶部标题栏 + 类别筛选条 + 左目录右候选流 + 常显底栏 */
.cleanup-header__meta { color: var(--text-secondary); font-size: 12px; }
.cleanup-header__meta b { color: var(--text-primary); font-family: var(--font-mono); }
.cleanup-header__spacer { flex: 1; }
.cleanup-header__close { border: 0; background: transparent; color: var(--text-muted); font-size: 16px; cursor: pointer; }

.cleanup-filter-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: none;
  padding: 8px 18px;
  border-bottom: 1px solid var(--hairline-soft);
  background: var(--panel-subtle-bg);
}

.cleanup-chip {
  height: 26px;
  padding: 0 10px;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border: 1px solid var(--hairline);
  border-radius: var(--radius);
  background: var(--panel-bg);
  color: var(--text-secondary);
  font-size: 12px;
  cursor: pointer;
}

.cleanup-chip.active { border-color: var(--accent-border); background: var(--accent-soft); color: var(--accent-text); font-weight: 600; }
.cleanup-chip__count { font-family: var(--font-mono); }

.cleanup-split { display: grid; grid-template-columns: 268px minmax(0, 1fr); min-height: 0; height: 100%; }

.cleanup-dirs {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 10px 8px;
  border-right: 1px solid var(--hairline-soft);
  background: var(--panel-subtle-bg);
  overflow-y: auto;
}

.cleanup-dirs__heading { padding: 6px 10px 4px; color: var(--text-muted); font-size: 10.5px; font-weight: 700; letter-spacing: 0.08em; }
.cleanup-dirs__item { display: flex; align-items: center; gap: 8px; padding: 7px 10px; border: 0; border-radius: var(--radius); background: transparent; color: var(--text-secondary); font-size: 12.5px; text-align: left; cursor: pointer; }
.cleanup-dirs__item.active { background: var(--accent-soft); color: var(--accent-text); font-weight: 650; }
.cleanup-dirs__path { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cleanup-dirs__count { flex: none; font-family: var(--font-mono); }
.cleanup-dirs__spacer { flex: 1; min-height: 10px; }
.cleanup-dirs__note { padding: 10px; border-top: 1px solid var(--hairline-soft); color: var(--text-muted); font-size: 11.5px; line-height: 1.6; }

.cleanup-stream { min-width: 0; overflow-y: auto; padding: 10px 16px; }
.cleanup-section__head { display: flex; align-items: baseline; gap: 10px; padding: 2px 0 8px; border-bottom: 1px solid var(--hairline-faint); margin-bottom: 10px; }
.cleanup-section__head strong { font-size: 13.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cleanup-section__head span { color: var(--text-muted); font-size: 12px; }

.cleanup-outdated {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 10px;
  border: 1px solid var(--warning-border);
  border-radius: var(--radius);
  background: var(--warning-soft);
  color: var(--warning-text);
  font-size: 11.5px;
}

.cleanup-footer__summary { font-size: 13px; }
.cleanup-footer__summary b { font-family: var(--font-mono); }
.cleanup-footer__hint { color: var(--text-muted); font-size: 11.5px; }

.cleanup-modal-header,
.cleanup-modal-footer {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 0 18px;
  flex: 0 0 auto;
  background: var(--panel-bg);
}
.cleanup-modal-header {
  height: 56px;
  border-bottom: 1px solid var(--hairline);
}
.cleanup-modal-header h3 {
  margin: 0;
  font-size: 16px;
}
.cleanup-modal-footer {
  height: 60px;
  border-top: 1px solid var(--hairline);
  background: var(--panel-subtle-bg);
}
.cleanup-modal-body {
  padding: 0;
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  padding: 16px 22px 20px;
}
.cleanup-intro,
.cleanup-loading,
.cleanup-error,
.cleanup-empty {
  color: var(--review-text-muted);
  font-size: 13px;
}
.cleanup-intro--muted {
  color: var(--review-text-secondary);
  margin-top: 4px;
}
.cleanup-summary {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
  margin: 0 0 14px;
  font-size: 13px;
  color: var(--review-text-emphasis);
}
.cleanup-summary span {
  padding: 5px 9px;
  border: 1px solid var(--border-color);
  border-radius: 6px;
  background: var(--review-neutral-chip-bg);
}
.cleanup-progress-meta,
.cleanup-progress-hint,
.cleanup-progress-path {
  margin-top: 8px;
  font-size: 13px;
  color: var(--review-text-secondary);
}
.cleanup-progress-path {
  word-break: break-all;
}
.cleanup-toolbar {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 14px;
}
.cleanup-section {
  margin-top: 18px;
}
.cleanup-dir-toggle {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  box-sizing: border-box;
  margin: 0 0 10px;
  padding: 8px 6px;
  border: none;
  border-bottom: 1px solid var(--border-color);
  background: var(--panel-bg);
  color: var(--review-text-strong);
  font-size: 12px;
  font-family: var(--font-mono, monospace);
  text-align: left;
  cursor: pointer;
}
.cleanup-dir-toggle__chevron { flex: none; width: 12px; }
.cleanup-dir-toggle__label {
  flex: none;
  padding: 1px 6px;
  border-radius: 4px;
  background: var(--review-subtle-bg);
  font-family: inherit;
  font-size: 10px;
}
.cleanup-dir-toggle__path { flex: 1; min-width: 0; word-break: break-all; }
.cleanup-dir-toggle__count { flex: none; font-size: 10px; font-family: inherit; white-space: nowrap; opacity: 0.75; }
.cleanup-card {
  padding: 12px 14px;
  border: 1px solid var(--review-border-color);
  border-radius: 8px;
  margin-top: 10px;
  background: var(--review-subtle-bg);
}
.cleanup-card-kind {
  margin-bottom: 6px;
  font-size: 11px;
  font-weight: 700;
  color: var(--review-text-strong);
  opacity: 0.8;
}
.cleanup-item-otherdir {
  display: block;
  font-size: 10px;
  font-family: var(--font-mono, monospace);
  opacity: 0.7;
}
/* 截取片段：A / B 并排，窄屏下退回上下两行。判断依据在下方那行 <p> 里。 */
.cleanup-clip-pair {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 10px;
}
.cleanup-clip-side {
  min-width: 0;
  padding: 8px;
  border: 1px solid var(--review-border-color);
  border-radius: 8px;
  background: var(--review-solid-bg);
}
.cleanup-clip-side__head {
  margin-bottom: 6px;
  font-size: 11px;
  font-weight: 700;
  color: var(--review-text-strong);
  opacity: 0.8;
}
.cleanup-select-row--trashed { opacity: 0.45; text-decoration: line-through; }
.cleanup-open-btn { display: inline-flex; align-items: center; gap: 6px; }
.cleanup-badge {
  padding: 1px 7px;
  border-radius: 999px;
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 10px;
  white-space: nowrap;
}
.cleanup-badge--done { background: var(--accent-color); color: var(--accent-on); }
.cleanup-card p,
.cleanup-card ul,
.cleanup-section ul {
  margin: 6px 0;
}
.cleanup-keep-row {
  display: flex;
  align-items: flex-start;
  gap: 6px;
}
.cleanup-select-row {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  min-width: 0;
  padding: 8px 0;
  border-top: 1px solid var(--neutral-softer);
}
.cleanup-select-row--original {
  border-top: 0;
  padding-top: 0;
}
.cleanup-item-text {
  display: flex;
  flex-direction: column;
  flex: 1 1 auto;
  min-width: 0;
  gap: 2px;
}
.cleanup-item-main {
  min-width: 0;
  color: var(--review-text-strong);
  font-size: 13px;
  font-weight: 600;
  overflow-wrap: anywhere;
}
.cleanup-item-path {
  font-size: 11px;
  color: var(--review-text-path);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cleanup-item-actions {
  display: flex;
  gap: 6px;
  flex: 0 0 auto;
}
.link-btn {
  flex: none;
  border: 0;
  background: transparent;
  color: var(--accent-text);
  font-size: 12px;
  cursor: pointer;
  padding: 0;
}
.btn-compact {
  height: 28px;
  padding: 0 10px;
  font-size: 12px;
}
</style>
