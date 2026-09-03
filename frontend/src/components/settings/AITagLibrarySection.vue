<template>
	<div :id="`settings-ai-tag-library`" class="settings-section ai-tag-library-section">
	  <div class="settings-section-heading">
		<h3>AI 标签库</h3>
		<button type="button" class="btn-secondary" :disabled="loading || !loaded" @click="$emit('add-group')">添加分类</button>
	  </div>
	  <p class="help-text">每个分类占一行，可在分类内维护多个标签；只有启用的标签会发送给模型。可直接填写已有普通标签的名称，保存后会保留它现有的视频关联并加入 AI 标签库。</p>
	  <div v-if="loading" class="empty-hint">正在加载标签库...</div>
	  <div v-else-if="errorMessage" class="ai-tag-library-error">
		<span>{{ errorMessage }}</span>
		<button data-test="reload-ai-tag-library" type="button" class="btn-secondary btn-compact" @click="$emit('reload')">重新加载</button>
	  </div>
	  <div v-else class="ai-tag-library-list">
		<div v-for="(group, groupIndex) in groups" :key="group._key" class="ai-tag-library-group">
		  <div class="ai-tag-library-group-heading">
			<input v-model.trim="group.namespace" type="text" class="text-input ai-tag-namespace-input" placeholder="分类名称" aria-label="标签分类名称" />
			<span class="ai-tag-count">{{ group.tags.length }} 个标签</span>
			<button type="button" class="btn-secondary btn-compact" @click="$emit('add-tag', groupIndex)">添加标签</button>
			<button type="button" class="btn-danger btn-compact" @click="$emit('remove-group', groupIndex)">删除分类</button>
		  </div>
		  <div class="ai-tag-library-group-tags">
			<div v-for="(tag, tagIndex) in group.tags" :key="tag._key" class="ai-tag-library-tag">
			  <input v-model.trim="tag.name" type="text" class="text-input" placeholder="标签名称" :aria-label="`${group.namespace || '未命名分类'}标签名称`" />
			  <input v-model="tag.color" type="color" class="ai-tag-color" aria-label="标签颜色" />
			  <label class="ai-tag-active"><input v-model="tag.is_active" type="checkbox" />启用</label>
			  <button type="button" class="btn-secondary btn-danger-outline ai-tag-remove" title="移出 AI 标签库" :aria-label="`移出标签 ${tag.name || tagIndex + 1}`" @click="$emit('remove-tag', groupIndex, tagIndex)">×</button>
			</div>
			<div v-if="group.tags.length === 0" class="empty-hint ai-tag-group-empty">当前分类暂无标签</div>
		  </div>
		</div>
		<div v-if="groups.length === 0" class="empty-hint">当前未配置 AI 标签，后台分析将暂停。</div>
	  </div>
	</div>
</template>

<script>
// AI 标签库编辑器。标签组数组由 SettingsPage 持有（保存流程要用它做校验与提交），
// 这里只呈现并把增删动作发回去。
export default {
  name: 'AITagLibrarySection',
  props: {
    groups: { type: Array, default: () => [] },
    loading: { type: Boolean, default: false },
    loaded: { type: Boolean, default: false },
    errorMessage: { type: String, default: '' }
  },
  emits: ['reload', 'add-group', 'remove-group', 'add-tag', 'remove-tag']
};
</script>

<style scoped>
.ai-tag-library-section {
  grid-column: 1 / -1;
}

.ai-tag-library-list {
  display: grid;
  gap: 0;
  max-height: 420px;
  margin-top: 12px;
  padding-right: 4px;
  overflow-y: auto;
}

.ai-tag-library-group {
  display: grid;
  grid-template-columns: minmax(220px, 0.7fr) minmax(0, 2fr);
  gap: 14px;
  padding: 12px 0;
  border-bottom: 1px solid var(--border-color);
}

.ai-tag-library-group:first-child {
  border-top: 1px solid var(--border-color);
}

.ai-tag-library-group-heading {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-content: start;
  align-items: center;
  gap: 8px;
}

.ai-tag-namespace-input {
  font-weight: 650;
}

.ai-tag-count {
  color: var(--text-secondary);
  font-size: 12px;
  white-space: nowrap;
}

.ai-tag-library-group-heading .btn-compact {
  width: 100%;
}

.ai-tag-library-group-tags {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 8px;
}

.ai-tag-library-tag {
  display: grid;
  grid-template-columns: minmax(120px, 1fr) 32px auto 32px;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.ai-tag-group-empty {
  align-self: center;
  text-align: left;
}

.ai-tag-color {
  width: 32px;
  height: 32px;
  padding: 0;
  border: 1px solid var(--border-color);
  border-radius: 4px;
  background: transparent;
}

.ai-tag-active {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  white-space: nowrap;
}

.ai-tag-remove {
  width: 32px;
  height: 32px;
  padding: 0;
}

.ai-tag-library-error {
  margin-top: 10px;
  color: var(--danger-color);
  font-size: 13px;
}


/* 窄屏下标签组从两栏收到一栏（规则随模板从 SettingsPage 搬过来）。 */
@media (max-width: 980px) {
  .ai-tag-library-group {
    grid-template-columns: minmax(190px, 0.65fr) minmax(0, 1.8fr);
  }
}

@media (max-width: 600px) {
  .ai-tag-library-group {
    grid-template-columns: 1fr;
  }

  .ai-tag-library-group-tags {
    grid-template-columns: 1fr;
  }
}
</style>
