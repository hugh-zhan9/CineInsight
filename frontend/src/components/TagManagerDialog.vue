<template>
  <div v-if="visible" class="modal-overlay" @click="$emit('close')">
    <div class="modal" @click.stop>
      <h2>标签管理</h2>
      <div class="form-group">
        <input v-model="newTag.name" type="text" placeholder="标签名称" />
        <input v-model="newTag.color" type="color" />
        <button @click="handleCreateTag" class="btn-primary" :disabled="createTagLoading">添加标签</button>
        <p v-if="tagCreateError" class="error-text">{{ tagCreateError }}</p>
      </div>
      <div class="tag-list">
        <div v-for="tag in localTags" :key="tag.id" class="tag-item tag-edit-item">
          <span class="tag-color" :style="{ backgroundColor: tag.color }"></span>
          <input v-model="tag.name" type="text" class="tag-name-input" />
          <input v-model="tag.color" type="color" class="tag-color-input" />
          <button @click="saveTag(tag)" class="btn-small">保存</button>
          <button @click.stop="$emit('request-delete-tag', tag)" class="btn-danger">删除</button>
        </div>
      </div>
      <button @click="$emit('close')" class="btn-secondary">关闭</button>
    </div>
  </div>
</template>

<script>
import { CreateTag, UpdateTag, GetAllTags } from '../../wailsjs/go/main/App';

export default {
  name: 'TagManagerDialog',
  props: {
    visible: { type: Boolean, default: false },
    tags: { type: Array, default: () => [] }
  },
  emits: ['close', 'tags-changed', 'request-delete-tag'],
  data() {
    return {
      newTag: { name: '', color: '#3b82f6' },
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
        await CreateTag(name, this.newTag.color);
        this.newTag.name = '';
        this.newTag.color = '#3b82f6';
        this.$emit('tags-changed');
      } catch (err) {
        if (this.isDuplicateError(err)) {
          this.tagCreateError = '标签已存在';
          return;
        }
        console.error('创建标签失败:', err);
        this.tagCreateError = '创建标签失败';
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
        console.error('更新标签失败:', err);
        alert('更新标签失败: ' + err);
      }
    }
  }
};
</script>
