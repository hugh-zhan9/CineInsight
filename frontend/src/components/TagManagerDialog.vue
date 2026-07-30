<template>
  <div v-if="visible" class="modal-overlay" @click="$emit('close')">
    <div class="modal" @click.stop style="max-width: 520px; max-height: 90vh; overflow-y: auto;">
      <h2>标签管理</h2>
      
      <!-- 创建新标签 -->
      <div class="setting-item">
        <label>新建标签</label>
        <div style="display: flex; gap: 8px; margin-top: 8px;">
          <input 
            v-model="newTag.name" 
            type="text" 
            placeholder="输入标签名称..." 
            class="text-input" 
            style="flex: 1;"
            @keyup.enter="handleCreateTag" 
          />
          <button @click="handleCreateTag" class="btn-primary" :disabled="createTagLoading">添加</button>
        </div>
        <p v-if="tagCreateError" class="help-text" style="color: var(--danger-color);">{{ tagCreateError }}</p>
      </div>

      <div class="divider"></div>

      <div class="setting-item">
        <label>合并同义标签</label>
        <p class="help-text">待合并标签的所有视频关联会转移到目标标签，原标签随后删除。普通标签与 AI 标签分别在各自类型内合并；按住 Ctrl/⌘ 可多选来源。</p>
        <select v-model.number="mergeTargetId" class="text-input merge-select">
          <option :value="0">选择保留的目标标签</option>
          <option v-for="tag in mergeableTags" :key="`target-${tag.id}`" :value="tag.id">{{ tag.name }}</option>
        </select>
        <select v-model="mergeSourceIds" class="text-input merge-select" multiple>
          <option
            v-for="tag in mergeSourceTags"
            :key="`source-${tag.id}`"
            :value="tag.id"
            :disabled="tag.id === mergeTargetId"
          >
            {{ tag.name }}
          </option>
        </select>
        <div class="merge-actions">
          <span v-if="mergeError" class="help-text merge-error">{{ mergeError }}</span>
          <button class="btn-primary" :disabled="mergeLoading || !canMerge" @click="handleMergeTags">
            {{ mergeLoading ? '合并中...' : '合并标签' }}
          </button>
        </div>
      </div>

      <div class="divider"></div>

      <!-- 标签列表 -->
      <div class="tag-list-container" style="max-height: 260px; overflow-y: auto; padding-right: 4px;">
        <div v-for="tag in localTags" :key="tag.id" class="tag-edit-row">
          <input v-model="tag.color" type="color" class="color-picker" :disabled="tag.is_system || tag.automatic_kind" style="width: 28px; height: 28px; border: none; padding: 0; background: none; cursor: pointer; border-radius: 4px;" />
          <input v-model="tag.name" type="text" class="text-input" :disabled="tag.is_system || tag.automatic_kind" style="height: 32px; font-size: 13px;" />
          <div style="display: flex; gap: 6px;">
            <span v-if="tag.is_system" class="system-tag-note">AI 标签</span>
            <span v-else-if="tag.automatic_kind" class="system-tag-note">自动标签</span>
            <template v-else>
              <button @click="saveTag(tag)" class="btn-action">保存</button>
              <button @click.stop="$emit('request-delete-tag', tag)" class="btn-action" style="color: var(--danger-color); border-color: var(--danger-color);">删除</button>
            </template>
          </div>
        </div>
        <div v-if="localTags.length === 0" class="help-text" style="text-align: center; padding: 20px;">暂无标签</div>
      </div>

      <div class="modal-actions">
        <button @click="$emit('close')" class="btn-secondary">完成</button>
      </div>
    </div>
  </div>
</template>

<script>
import { CreateTag, MergeTags, UpdateTag } from '../../wailsjs/go/main/App';

export default {
  name: 'TagManagerDialog',
  props: {
    visible: { type: Boolean, default: false },
    tags: { type: Array, default: () => [] }
  },
  emits: ['close', 'tags-changed', 'request-delete-tag'],
  data() {
    return {
      newTag: { name: '' },
      createTagLoading: false,
      tagCreateError: '',
      localTags: [],
      mergeTargetId: 0,
      mergeSourceIds: [],
      mergeLoading: false,
      mergeError: ''
    };
  },
  computed: {
    mergeableTags() {
      return this.localTags.filter(tag => !tag.automatic_kind);
    },
    mergeSourceTags() {
      const target = this.mergeableTags.find(tag => Number(tag.id) === Number(this.mergeTargetId));
      if (!target) return [];
      return this.mergeableTags.filter(tag => tag.is_system === target.is_system);
    },
    canMerge() {
      return this.mergeTargetId > 0 && this.mergeSourceIds.some(id => Number(id) !== Number(this.mergeTargetId));
    }
  },
  watch: {
    mergeTargetId() {
      const allowed = new Set(this.mergeSourceTags.map(tag => Number(tag.id)));
      this.mergeSourceIds = this.mergeSourceIds.filter(id => allowed.has(Number(id)));
    },
    tags: {
      handler(val) {
        this.localTags = val.map(t => ({ ...t }));
      },
      immediate: true,
      deep: true
    },
    visible(val) {
      if (val) {
        this.tagCreateError = '';
        this.mergeError = '';
        this.mergeTargetId = 0;
        this.mergeSourceIds = [];
        this.localTags = this.tags.map(t => ({ ...t }));
      }
    }
  },
  methods: {
    isDuplicateError(err) {
      const raw = err && (err.message || err.error || err.toString ? err.toString() : err);
      const msg = String(raw || '').toLowerCase();
      return msg.includes('tag_exists') || msg.includes('unique') || msg.includes('duplicate') || msg.includes('constraint');
    },
    async handleCreateTag() {
      if (this.createTagLoading) return;
      const name = this.newTag.name.trim();
      if (!name) return;
      this.tagCreateError = '';
      if (this.localTags.some(t => String(t.name).toLowerCase() === name.toLowerCase())) {
        this.tagCreateError = '标签已存在';
        return;
      }
      this.createTagLoading = true;

      try {
        await CreateTag(name, '');
        this.newTag.name = '';
        this.$emit('tags-changed');
      } catch (err) {
        if (this.isDuplicateError(err)) {
          this.tagCreateError = '标签已存在';
          return;
        }
        this.tagCreateError = '创建失败';
      } finally {
        this.createTagLoading = false;
      }
    },
    async saveTag(tag) {
      const name = (tag.name || '').trim();
      if (!name) {
        alert('标签名称不能为空');
        return;
      }
      try {
        await UpdateTag(tag.id, name, tag.color);
        this.$emit('tags-changed');
      } catch (err) {
        alert('更新失败: ' + err);
      }
    },
    async handleMergeTags() {
      if (this.mergeLoading || !this.canMerge) return;
      const sourceIds = this.mergeSourceIds
        .map(Number)
        .filter(id => id > 0 && id !== Number(this.mergeTargetId));
      const target = this.mergeableTags.find(tag => Number(tag.id) === Number(this.mergeTargetId));
      const sourceNames = this.mergeableTags
        .filter(tag => sourceIds.includes(Number(tag.id)))
        .map(tag => `「${tag.name}」`)
        .join('、');
      if (!target || !window.confirm(`确定将 ${sourceNames} 合并到「${target.name}」吗？源标签会被删除，此操作不能自动撤销。`)) return;
      this.mergeLoading = true;
      this.mergeError = '';
      try {
        await MergeTags(sourceIds, Number(this.mergeTargetId));
        this.mergeSourceIds = [];
        this.mergeTargetId = 0;
        this.$emit('tags-changed');
      } catch (err) {
        this.mergeError = '合并失败: ' + String(err);
      } finally {
        this.mergeLoading = false;
      }
    }
  }
};
</script>

<style scoped>
.tag-list-container::-webkit-scrollbar { width: 4px; }
.merge-select { width: 100%; margin-top: 8px; }
.merge-select[multiple] { min-height: 92px; }
.merge-actions { display: flex; justify-content: space-between; align-items: center; gap: 8px; margin-top: 8px; }
.merge-error { color: var(--danger-color); }
.tag-list-container::-webkit-scrollbar-thumb { background: var(--border-color); border-radius: 4px; }
.color-picker::-webkit-color-swatch-wrapper { padding: 0; }
.color-picker::-webkit-color-swatch { border: 1px solid var(--border-color); border-radius: 4px; }
.system-tag-note { align-self: center; color: var(--text-secondary); font-size: 12px; white-space: nowrap; }
</style>
