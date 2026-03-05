<template>
  <div v-if="visible" class="modal-overlay" @click="$emit('close')">
    <div class="modal" @click.stop style="max-width: 480px;">
      <h2>为视频添加标签</h2>

      <!-- 输入与搜索 -->
      <div class="setting-item">
        <label>标签名称</label>
        <div style="display: flex; gap: 8px; margin-top: 8px;">
          <input
            v-model="newTagName"
            type="text"
            class="text-input"
            style="flex: 1;"
            placeholder="输入名称进行搜索或创建..."
            @keyup.enter="addNewTag"
          />
          <button @click="addNewTag" class="btn-primary">创建并添加</button>
        </div>
        <p class="help-text">输入名称可实时过滤已有标签，若不存在则直接创建并添加。</p>
      </div>

      <div class="divider"></div>

      <!-- 已有标签列表 -->
      <div class="tag-selector-container">
        <div
          v-for="tag in filteredTags"
          :key="tag.id"
          class="clickable-tag-item"
          @click="addExistingTag(tag)"
        >
          <span class="color-dot" :style="{ backgroundColor: tag.color }"></span>
          <span class="tag-name">{{ tag.name }}</span>
          <span class="add-icon">+</span>
        </div>
        
        <div v-if="filteredTags.length === 0" class="empty-state-mini">
          {{ newTagName.trim() ? '未找到匹配标签' : '没有可选的标签' }}
        </div>
      </div>

      <div class="modal-actions">
        <button @click="$emit('close')" class="btn-secondary">取消</button>
      </div>
    </div>
  </div>
</template>

<script>
import { CreateTag, AddTagToVideo } from '../../wailsjs/go/main/App';

export default {
  name: 'AddTagDialog',
  props: {
    visible: { type: Boolean, default: false },
    video: { type: Object, default: null },
    tags: { type: Array, default: () => [] }
  },
  emits: ['close', 'tag-added'],
  data() {
    return {
      newTagName: ''
    };
  },
  computed: {
    availableTags() {
      if (!this.video || !this.video.tags) return this.tags;
      const videoTagIds = this.video.tags.map(t => t.id);
      return this.tags.filter(t => !videoTagIds.includes(t.id));
    },
    filteredTags() {
      const kw = this.newTagName.trim().toLowerCase();
      if (!kw) return this.availableTags;
      return this.availableTags.filter(t => t.name.toLowerCase().includes(kw));
    }
  },
  watch: {
    visible(val) {
      if (val) {
        this.newTagName = '';
      }
    }
  },
  methods: {
    isDuplicateError(err) {
      const raw = err && (err.message || err.error || err.toString ? err.toString() : err);
      const msg = String(raw || '').toLowerCase();
      return msg.includes('tag_exists') || msg.includes('unique') || msg.includes('duplicate') || msg.includes('constraint');
    },
    async addNewTag() {
      const tagName = this.newTagName.trim();
      if (!tagName) return;

      try {
        let newTag = null;
        try {
          newTag = await CreateTag(tagName, '');
        } catch (err) {
          if (this.isDuplicateError(err)) {
            newTag = this.tags.find(t => t.name === tagName) || null;
          } else {
            throw err;
          }
        }

        if (!newTag) return;

        await AddTagToVideo(this.video.id, newTag.id);
        this.newTagName = '';
        this.$emit('tag-added');
        this.$emit('close');
      } catch (err) {
        console.error('添加标签失败:', err);
      }
    },
    async addExistingTag(tag) {
      try {
        await AddTagToVideo(this.video.id, tag.id);
        this.$emit('tag-added');
        this.$emit('close');
      } catch (err) {
        console.error('添加标签失败:', err);
      }
    }
  }
};
</script>

<style scoped>
.tag-selector-container {
  max-height: 280px;
  overflow-y: auto;
  padding-right: 4px;
}

.tag-selector-container::-webkit-scrollbar { width: 4px; }
.tag-selector-container::-webkit-scrollbar-thumb { background: var(--border-color); border-radius: 4px; }

.clickable-tag-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 14px;
  border-radius: var(--radius-md);
  cursor: pointer;
  transition: var(--transition);
  margin-bottom: 4px;
}

.clickable-tag-item:hover {
  background: var(--bg-color);
  transform: translateX(4px);
}

.color-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
}

.tag-name {
  flex: 1;
  font-size: 14px;
  font-weight: 500;
}

.add-icon {
  color: var(--text-muted);
  font-size: 18px;
  font-weight: 300;
}

.clickable-tag-item:hover .add-icon {
  color: var(--accent-color);
}

.empty-state-mini {
  text-align: center;
  padding: 30px 0;
  color: var(--text-secondary);
  font-size: 13px;
}
</style>
