<template>
  <aside id="workbench-edit-projects" class="edit-projects" aria-label="编辑项目" data-test="edit-projects">
    <div class="edit-section__header edit-projects__header">
      <h3>编辑项目</h3>
      <button type="button" class="btn btn--ghost btn--sm" data-test="edit-projects-refresh" @click="$emit('refresh')">
        <AppIcon name="refresh" />刷新
      </button>
    </div>
    <div v-if="projects.length || kindFilter !== 'all' || keyword" class="edit-projects__filters" role="group" aria-label="按项目类型筛选">
      <button
        v-for="filter in filters"
        :key="filter.kind"
        type="button"
        class="btn btn--sm edit-projects__filter"
        :data-test="`edit-projects-filter-${filter.kind}`"
        :aria-pressed="kindFilter === filter.kind"
        @click="kindFilter = filter.kind"
      >{{ filter.label }} <span class="edit-pill edit-projects__filter-count">{{ filter.count }}</span></button>
    </div>
    <div v-if="projects.length > 6 || keyword" class="edit-projects__search">
      <AppIcon name="search" />
      <input v-model="keyword" type="search" aria-label="筛选项目名称" placeholder="筛选项目名称…" data-test="edit-projects-search" />
      <button v-if="keyword" type="button" class="btn-icon btn--sm" aria-label="清空项目名称筛选" title="清空项目名称筛选" @click="keyword = ''">
        <AppIcon name="close" />
      </button>
    </div>
    <button type="button" class="btn btn--secondary btn--sm edit-projects__more edit-projects__history" data-test="edit-projects-completed-crops" :aria-pressed="showCompletedCrops" @click="$emit('toggle-completed-crops')">显示已完成裁剪</button>
    <p v-if="error" class="edit-error" role="alert">{{ error }}</p>
    <p v-else-if="!projects.length && hasHiddenCrops" class="help-text" data-test="edit-projects-empty">
      当前没有显示的编辑项目。裁剪完成的记录仍保留，可打开「显示已完成裁剪」查看。{{ hasMore ? '也可加载更早的项目。' : '' }}
    </p>
    <p v-else-if="!projects.length" class="help-text" data-test="edit-projects-empty">
      还没有编辑项目。在片库里多选视频后点「合并为新视频」或「批量去片头」，或在行菜单选「高清替换…」。
    </p>
    <p v-else-if="!filteredProjects.length" class="help-text" data-test="edit-projects-empty">没有符合筛选条件的项目。</p>
    <ul class="edit-projects__list" data-test="edit-projects-list">
      <li
        v-for="project in filteredProjects"
        :key="project.id"
        class="edit-projects__item"
        :class="{
          active: project.id === selectedId,
          'edit-projects__item--recovery': project.error_code === 'replace_recovery_pending',
          'edit-projects__item--failed': project.status === 'failed' && project.error_code !== 'replace_recovery_pending'
        }"
        :data-test="`edit-project-${project.id}`"
      >
        <button type="button" class="btn edit-projects__select" :aria-current="project.id === selectedId ? 'true' : undefined" :data-test="`edit-project-open-${project.id}`" @click="$emit('select', project.id)">
          <span class="edit-projects__title" :title="project.title || '未命名项目'">{{ project.title || '未命名项目' }}</span>
          <span class="edit-projects__meta">
            <AppBadge>{{ kindLabel(project.kind) }}</AppBadge>
            <AppBadge v-if="project.error_code === 'replace_recovery_pending'" tone="warning">待恢复</AppBadge>
            <AppBadge v-else :tone="statusTone(project.status)">{{ statusLabel(project.status) }}</AppBadge>
            <span v-if="project.item_count > 0" class="edit-projects__count">{{ countText(project) }}</span>
          </span>
          <ProgressBar v-if="showProgress(project)" class="edit-progress" :value="percent(project) / 100" />
        </button>
        <div class="edit-projects__actions">
          <button v-if="cancellable(project)" type="button" class="btn btn--ghost btn--sm" :disabled="busy[project.id]" :data-test="`edit-project-cancel-${project.id}`" @click="$emit('cancel', project)">取消</button>
          <button v-if="requeueable(project) && project.error_code !== 'replace_recovery_pending'" type="button" class="btn btn--ghost btn--sm" :disabled="busy[project.id]" :data-test="`edit-project-requeue-${project.id}`" @click="$emit('requeue', project)">继续</button>
          <button v-if="!active(project) && project.error_code !== 'replace_recovery_pending'" type="button" class="btn btn--ghost btn--sm edit-projects__delete" :disabled="busy[project.id]" :data-test="`edit-project-delete-${project.id}`" @click="$emit('delete', project)">删除</button>
        </div>
      </li>
    </ul>
    <button
      v-if="hasMore"
      type="button"
      class="btn btn--secondary btn--sm edit-projects__more"
      data-test="edit-projects-more"
      :disabled="loadingMore"
      @click="$emit('load-more')"
    >{{ loadingMore ? '正在加载…' : '加载更多' }}</button>
  </aside>
</template>

<script>
import { EDIT_ACTIVE, EDIT_CANCELLABLE, EDIT_REQUEUEABLE, editKindLabel, editStatusLabel } from '../../utils/videoEdit.js';
import AppBadge from '../ui/AppBadge.vue';
import AppIcon from '../ui/AppIcon.vue';
import ProgressBar from '../ui/ProgressBar.vue';

const PROJECT_FILTERS = { all: '全部', crop: '裁剪', merge: '合并', trim_intro: '去片头', hd_replace: '高清' };
const PROJECT_STATUS_TONES = {
  analyzing: 'accent', queued: 'accent', running: 'accent', completed: 'success',
  partial: 'warning', replace_recovery_pending: 'warning', failed: 'danger'
};

export function editProjectStatusTone(status) {
  return PROJECT_STATUS_TONES[status] || 'neutral';
}

// 工作台左栏：最近的编辑项目（状态、进度）与取消 / 继续未完成项 / 删除记录。动作交给页面执行。
export default {
  name: 'EditProjectList',
  components: { AppBadge, AppIcon, ProgressBar },
  props: {
    projects: { type: Array, default: () => [] },
    selectedId: { type: Number, default: 0 },
    showCompletedCrops: { type: Boolean, default: false },
    hasHiddenCrops: { type: Boolean, default: false },
    busy: { type: Object, default: () => ({}) },
    error: { type: String, default: '' },
    // 一页 50 个，按 ID 倒序；还有更早的项目时末尾给「加载更多」。
    hasMore: { type: Boolean, default: false },
    loadingMore: { type: Boolean, default: false }
  },
  emits: ['select', 'cancel', 'requeue', 'delete', 'refresh', 'load-more', 'toggle-completed-crops'],
  data() {
    return { kindFilter: 'all', keyword: '' };
  },
  computed: {
    filters() {
      return Object.entries(PROJECT_FILTERS).map(([kind, label]) => ({
        kind, label, count: kind === 'all' ? this.projects.length : this.projects.filter(project => project.kind === kind).length
      }));
    },
    filteredProjects() {
      const keyword = this.keyword.trim().toLowerCase();
      return this.projects.filter(project =>
        (this.kindFilter === 'all' || project.kind === this.kindFilter) &&
        (!keyword || (project.title || '未命名项目').toLowerCase().includes(keyword))
      );
    }
  },
  methods: {
    kindLabel: editKindLabel,
    statusLabel: editStatusLabel,
    statusTone: editProjectStatusTone,
    cancellable(project) {
      return EDIT_CANCELLABLE.includes(project.status);
    },
    requeueable(project) {
      return EDIT_REQUEUEABLE.includes(project.status);
    },
    active(project) {
      return EDIT_ACTIVE.includes(project.status);
    },
    showProgress(project) {
      return ['queued', 'running', 'analyzing'].includes(project.status);
    },
    percent(project) {
      return Math.max(0, Math.min(100, Math.round(Number(project.progress || 0) * 100)));
    },
    countText(project) {
      return `${project.completed_count}/${project.item_count} 已完成`;
    }
  }
};
</script>
