<template>
  <aside class="edit-projects" data-test="edit-projects">
    <div class="edit-inline edit-inline--spread">
      <h3>编辑项目</h3>
      <button type="button" class="row-btn" data-test="edit-projects-refresh" @click="$emit('refresh')">刷新</button>
    </div>
    <p v-if="error" class="edit-error" role="alert">{{ error }}</p>
    <p v-else-if="!projects.length" class="help-text" data-test="edit-projects-empty">
      还没有编辑项目。在片库里多选视频后点「合并为新视频」或「批量去片头」，或在行菜单选「高清替换…」。
    </p>
    <ul class="edit-projects__list">
      <li
        v-for="project in projects"
        :key="project.id"
        :class="['edit-projects__item', { active: project.id === selectedId }]"
        :data-test="`edit-project-${project.id}`"
      >
        <button type="button" class="edit-projects__select" :data-test="`edit-project-open-${project.id}`" @click="$emit('select', project.id)">
          <span class="edit-projects__title" :title="project.title">{{ project.title || '未命名项目' }}</span>
          <span class="help-text">{{ kindLabel(project.kind) }} · {{ statusLabel(project.status) }}{{ countText(project) }}</span>
          <span v-if="showProgress(project)" class="edit-progress"><i :style="{ width: `${percent(project)}%` }"></i></span>
        </button>
        <div class="edit-projects__actions">
          <button v-if="cancellable(project)" type="button" class="row-btn" :disabled="busy[project.id]" :data-test="`edit-project-cancel-${project.id}`" @click="$emit('cancel', project)">取消</button>
          <button v-if="requeueable(project)" type="button" class="row-btn" :disabled="busy[project.id]" :data-test="`edit-project-requeue-${project.id}`" @click="$emit('requeue', project)">继续</button>
          <button v-if="!active(project)" type="button" class="row-btn" :disabled="busy[project.id]" :data-test="`edit-project-delete-${project.id}`" @click="$emit('delete', project)">删除</button>
        </div>
      </li>
    </ul>
  </aside>
</template>

<script>
import { EDIT_ACTIVE, EDIT_CANCELLABLE, EDIT_REQUEUEABLE, editKindLabel, editStatusLabel } from '../../utils/videoEdit.js';

// 工作台左栏：最近的编辑项目（状态、进度）与取消 / 继续未完成项 / 删除记录。动作交给页面执行。
export default {
  name: 'EditProjectList',
  props: {
    projects: { type: Array, default: () => [] },
    selectedId: { type: Number, default: 0 },
    busy: { type: Object, default: () => ({}) },
    error: { type: String, default: '' }
  },
  emits: ['select', 'cancel', 'requeue', 'delete', 'refresh'],
  methods: {
    kindLabel: editKindLabel,
    statusLabel: editStatusLabel,
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
      return project.item_count > 0 ? ` · ${project.completed_count}/${project.item_count} 已完成` : '';
    }
  }
};
</script>
