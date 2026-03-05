<template>
  <div v-if="visible" class="modal-overlay" @click="$emit('close')">
    <div class="modal" @click.stop style="max-width: 520px;">
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

      <!-- 标签列表 -->
      <div class="tag-list-container" style="max-height: 350px; overflow-y: auto; padding-right: 4px;">
        <div v-for="tag in localTags" :key="tag.id" class="tag-edit-row">
          <input v-model="tag.color" type="color" class="color-picker" style="width: 28px; height: 28px; border: none; padding: 0; background: none; cursor: pointer; border-radius: 4px;" />
          <input v-model="tag.name" type="text" class="text-input" style="height: 32px; font-size: 13px;" />
          <div style="display: flex; gap: 6px;">
            <button @click="saveTag(tag)" class="btn-action">保存</button>
            <button @click.stop="$emit('request-delete-tag', tag)" class="btn-action" style="color: var(--danger-color); border-color: var(--danger-color);">删除</button>
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
import { CreateTag, UpdateTag } from '../../wailsjs/go/main/App';

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
      localTags: []
    };
  },
  watch: {
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
    }
  }
};
</script>

<style scoped>
.tag-list-container::-webkit-scrollbar { width: 4px; }
.tag-list-container::-webkit-scrollbar-thumb { background: var(--border-color); border-radius: 4px; }
.color-picker::-webkit-color-swatch-wrapper { padding: 0; }
.color-picker::-webkit-color-swatch { border: 1px solid var(--border-color); border-radius: 4px; }
</style>
