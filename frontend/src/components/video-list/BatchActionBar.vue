<template>
    <div class="selection-toolbar">
      <span class="selection-toolbar__check" aria-hidden="true">✓</span>
      <strong>已选 {{ selectedCount }} 个</strong>
      <span v-if="selectedTotalSizeText" class="selection-toolbar__size">共 {{ selectedTotalSizeText }}</span>
      <span class="selection-toolbar__divider"></span>
      <button type="button" class="btn-secondary btn-compact" data-test="batch-playback-queue" @click="$emit('batch-playback-queue')">加入待播队列</button>
      <button @click="$emit('batch-add-tag')" class="btn-secondary btn-compact">批量标签编辑</button>
      <button @click="$emit('batch-move')" class="btn-secondary btn-compact" :disabled="migrationRunning">批量迁移</button>
      <button type="button" class="btn-secondary btn-compact" @click="$emit('batch-local-metadata')">导入本地资料</button>
      <button type="button" class="btn-secondary btn-compact" data-test="batch-playback-proxy" @click="$emit('batch-playback-proxy')">为选中生成代理</button>
      <!-- 批量生成字幕（D-PC23、MEDIA-14）：逐个排进字幕队列，确认与汇总由片库页的字幕弹窗负责。 -->
      <button type="button" class="btn-secondary btn-compact" data-test="batch-subtitle" @click="$emit('batch-subtitle')">批量生成字幕</button>
      <!-- 多版本聚合（D-MW-VERSIONS）：选中 2–20 个视频合并为一个版本组。 -->
      <button type="button" class="btn-secondary btn-compact" data-test="batch-version-group" :disabled="selectedCount < 2" @click="$emit('batch-version-group')">合并为版本组</button>
      <!-- 视频工作台（视频编辑合同「入口」）：按选中顺序合并为新视频（2 个以上）、批量去片头。 -->
      <button type="button" class="btn-secondary btn-compact" data-test="batch-edit-merge" :disabled="selectedCount < 2" @click="$emit('batch-edit-merge')">合并为新视频</button>
      <button type="button" class="btn-secondary btn-compact" data-test="batch-edit-trim" :disabled="selectedCount < 1" @click="$emit('batch-edit-trim')">批量去片头</button>
      <button @click="$emit('batch-delete')" class="btn-danger btn-compact">批量删除</button>
      <div class="selection-toolbar__spacer"></div>
      <button type="button" class="link-btn" @click="$emit('toggle-select-all')">{{ allVisibleSelected ? '取消全选' : '选择本页' }}</button>
      <button type="button" class="link-btn" @click="$emit('clear-selection')">清除选择 <kbd>Esc</kbd></button>
    </div>
</template>

<script>
// 选中若干视频后顶替结果条出现的批量操作栏。状态与动作都在片库页，这里只呈现与转发。
export default {
  name: 'BatchActionBar',
  props: {
    selectedCount: { type: Number, default: 0 },
    selectedTotalSizeText: { type: String, default: '' },
    migrationRunning: { type: Boolean, default: false },
    allVisibleSelected: { type: Boolean, default: false }
  },
  emits: ['batch-playback-queue', 'batch-add-tag', 'batch-move', 'batch-local-metadata', 'batch-playback-proxy', 'batch-subtitle', 'batch-version-group', 'batch-edit-merge', 'batch-edit-trim', 'batch-delete', 'toggle-select-all', 'clear-selection']
};
</script>

<style scoped>
/* 结果条与批量栏占同一个位置、同一个高度，互斥出现；这里是批量栏那一份。 */
.selection-toolbar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 0 -20px;
  padding: 0 20px;
  height: 44px;
  border-bottom: 1px solid var(--accent-border);
  background: var(--accent-soft);
  color: var(--accent-text);
  font-size: 12px;
  font-weight: 650;
}
.selection-toolbar__check {
  display: inline-flex;
  width: 16px;
  height: 16px;
  align-items: center;
  justify-content: center;
  border-radius: 4px;
  background: var(--accent-color);
  color: var(--accent-on);
  font-size: 11px;
}

.selection-toolbar__size { font-weight: 400; color: var(--text-secondary); }
.selection-toolbar__divider { width: 1px; height: 20px; background: var(--accent-border); }
.selection-toolbar__spacer { flex: 1; }
.selection-toolbar kbd {
  padding: 0 4px;
  border: 1px solid var(--accent-border);
  border-radius: 4px;
  font-family: var(--font-mono);
  font-size: 10.5px;
  opacity: 0.8;
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
