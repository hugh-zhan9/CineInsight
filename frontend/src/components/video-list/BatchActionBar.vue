<template>
  <SelectionBar
    class="selection-toolbar"
    :count="selectedCount"
    :total-matching="totalMatching"
    :all-matching-selected="allMatchingSelected"
    :selecting-all="selectingAll"
    @select-all-matching="$emit('select-all-matching')"
    @clear="$emit('clear-selection')"
  >
    <template #actions>
      <span v-if="selectedTotalSizeText" class="selection-toolbar__size">共 {{ selectedTotalSizeText }}</span>
      <button type="button" class="btn btn--secondary btn--sm" data-test="batch-playback-queue" @click="$emit('batch-playback-queue')">加入待播队列</button>
      <button type="button" class="btn btn--secondary btn--sm" data-test="batch-add-tag" @click="$emit('batch-add-tag')">批量标签编辑</button>
      <!-- 元数据批量编辑（D-MC-04）：年份、评分、标签、人物、作品集，作用于所选或当前筛选的全部结果，可撤销。 -->
      <button type="button" class="btn btn--secondary btn--sm" data-test="batch-metadata-edit" @click="$emit('batch-metadata-edit')">批量编辑…</button>
      <button type="button" class="btn btn--secondary btn--sm" data-test="batch-move" :disabled="migrationRunning" @click="$emit('batch-move')">批量迁移</button>
      <button type="button" class="btn btn--secondary btn--sm" data-test="batch-local-metadata" @click="$emit('batch-local-metadata')">导入本地资料</button>
      <button type="button" class="btn btn--secondary btn--sm" data-test="batch-playback-proxy" @click="$emit('batch-playback-proxy')">为选中生成代理</button>
      <!-- 批量生成字幕（D-PC23、MEDIA-14）：逐个排进字幕队列，确认与汇总由片库页的字幕弹窗负责。 -->
      <button type="button" class="btn btn--secondary btn--sm" data-test="batch-subtitle" @click="$emit('batch-subtitle')">批量生成字幕</button>
      <!-- 多版本聚合（D-MW-VERSIONS）：选中 2–20 个视频合并为一个版本组。 -->
      <button type="button" class="btn btn--secondary btn--sm" data-test="batch-version-group" :disabled="selectedCount < 2" @click="$emit('batch-version-group')">合并为版本组</button>
      <!-- 版本对比播放：正好选中两个时出现，按选中顺序左右同步播放比画质。 -->
      <button v-if="selectedCount === 2" type="button" class="btn btn--secondary btn--sm" data-test="batch-compare-play" @click="$emit('batch-compare')">对比播放</button>
      <!-- 视频工作台（视频编辑合同「入口」）：按选中顺序合并为新视频（2 个以上）、批量去片头。 -->
      <button type="button" class="btn btn--secondary btn--sm" data-test="batch-edit-merge" :disabled="selectedCount < 2" @click="$emit('batch-edit-merge')">合并为新视频</button>
      <button type="button" class="btn btn--secondary btn--sm" data-test="batch-edit-trim" :disabled="selectedCount < 1" @click="$emit('batch-edit-trim')">批量去片头</button>
      <button type="button" class="btn btn--secondary btn--sm" data-test="batch-edit-crop" :disabled="selectedCount < 1" @click="$emit('batch-edit-crop')">批量裁剪画面</button>
      <!-- 删除用三个词之一（D-UD-08）：按设置里的默认方式写「移到废纸篓」或「从片库移除」，要先确认时带「…」。 -->
      <button type="button" class="btn btn--danger btn--sm" data-test="batch-delete" @click="$emit('batch-delete')">{{ deleteLabel }}</button>
    </template>
  </SelectionBar>
</template>

<script>
import SelectionBar from '../ui/SelectionBar.vue';

// 选中条目的合计体积（「共 3.0 GB」）。只能合计已加载的行；有选中的条目不在已加载的行里时没有 size 可用，
// 宁可不显示也不显示一个偏小的数，返回空串。
export function selectedSizeText(videos, selectedIDs) {
  const ids = new Set(selectedIDs || []);
  const loaded = (videos || []).filter(video => ids.has(video.id));
  if (loaded.length !== ids.size || loaded.length === 0) return '';
  const bytes = loaded.reduce((total, video) => total + Number(video.size || 0), 0);
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const index = bytes > 0 ? Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024))) : 0;
  const value = bytes / Math.pow(1024, index);
  return `${value.toFixed(value >= 10 || index === 0 ? 0 : 1)} ${units[index]}`;
}

// 选中若干视频后浮在内容区底部的批量操作条（统一设计 D-UD-10，全站同一个 SelectionBar）：没选中时不出现，
// 默认只作用于勾选的条目；要作用于整个筛选结果，必须先点「全选当前结果（N）」。「取消选择」与 Esc 清空。
// 状态与动作都在片库页，这里只呈现与转发。
export default {
  name: 'BatchActionBar',
  components: { SelectionBar },
  props: {
    selectedCount: { type: Number, default: 0 },
    // 「全选当前结果（N）」的 N：点了会选中的条数（片库页 selectAllScope.total，工具栏按钮用同一个数）——
    // 筛选结果是命中数，语义检索、相似视频与随机批次是已加载的条数；不知道时为 null，不出「全选当前结果」。
    totalMatching: { type: Number, default: null },
    allMatchingSelected: { type: Boolean, default: false },
    // 片库页正在为「全选当前结果」补取剩余的页（「正在选择…」）。
    selectingAll: { type: Boolean, default: false },
    selectedTotalSizeText: { type: String, default: '' },
    migrationRunning: { type: Boolean, default: false },
    deleteLabel: { type: String, default: '移到废纸篓' }
  },
  emits: ['batch-playback-queue', 'batch-add-tag', 'batch-metadata-edit', 'batch-move', 'batch-local-metadata', 'batch-playback-proxy', 'batch-subtitle', 'batch-version-group', 'batch-compare', 'batch-edit-merge', 'batch-edit-trim', 'batch-edit-crop', 'batch-delete', 'select-all-matching', 'clear-selection']
};
</script>

<style scoped>
.selection-toolbar__size { color: var(--text-secondary); font-size: var(--fs-caption); white-space: nowrap; }
</style>
