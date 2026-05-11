<template>
  <div v-if="visible" class="modal-overlay" @click="$emit('close')">
    <div class="modal" @click.stop style="max-width: 480px;">
      <h2>{{ dialogTitle }}</h2>

      <div v-if="isBatchMode" class="batch-summary">
        <div class="batch-summary-title">已选择 {{ videoIds.length }} 个视频</div>
        <p class="help-text">选中一个或多个标签后统一应用到全部已选视频；共同标签可从全部已选视频上批量移除。</p>
      </div>

      <!-- 输入与搜索 -->
      <div class="setting-item">
        <label>{{ isBatchMode ? '添加标签' : '选择标签' }}</label>
        <div style="display: flex; gap: 8px; margin-top: 8px;">
          <input
            v-model="newTagName"
            type="text"
            class="text-input"
            style="flex: 1;"
            placeholder="输入名称进行搜索或创建..."
            @keyup.enter="addNewTag"
          />
          <button @click="addNewTag" class="btn-primary">创建并选中</button>
        </div>
        <p class="help-text">输入名称可实时过滤已有标签，若不存在则创建新标签并加入本次选择。</p>
      </div>

      <template v-if="isBatchMode">
        <div class="divider"></div>

        <div class="setting-item">
          <label>共同标签</label>
          <p class="help-text">以下标签存在于全部已选视频中，可批量移除关联。</p>
          <div v-if="commonTags.length > 0" class="common-tag-list">
            <div
              v-for="tag in commonTags"
              :key="tag.id"
              class="common-tag-item"
            >
              <span class="color-dot" :style="{ backgroundColor: tag.color }"></span>
              <span class="tag-name">{{ tag.name }}</span>
              <button
                type="button"
                class="btn-danger btn-small"
                :disabled="processingTagIds.includes(tag.id)"
                @click="removeCommonTag(tag)"
              >
                移除
              </button>
            </div>
          </div>
          <div v-else class="empty-state-mini">
            当前选择的视频没有共同标签
          </div>
        </div>
      </template>

      <div class="divider"></div>

      <div v-if="selectedTags.length > 0" class="selected-tag-strip">
        <span
          v-for="tag in selectedTags"
          :key="tag.id"
          class="selected-tag-pill"
        >
          <span class="color-dot" :style="{ backgroundColor: tag.color }"></span>
          <span>{{ tag.name }}</span>
          <button type="button" class="remove-selected-tag" @click="toggleTag(tag)" :aria-label="`取消选择 ${tag.name}`">×</button>
        </span>
      </div>

      <!-- 已有标签列表 -->
      <div class="tag-selector-container">
        <div
          v-for="tag in filteredTags"
          :key="tag.id"
          :class="['clickable-tag-item', { selected: isTagSelected(tag) }]"
          @click="toggleTag(tag)"
        >
          <span class="color-dot" :style="{ backgroundColor: tag.color }"></span>
          <span class="tag-name">{{ tag.name }}</span>
          <span class="add-icon">{{ isTagSelected(tag) ? '✓' : '+' }}</span>
        </div>
        
        <div v-if="filteredTags.length === 0" class="empty-state-mini">
          {{ newTagName.trim() ? '未找到匹配标签' : '没有可选的标签' }}
        </div>
      </div>

      <div class="modal-actions">
        <button @click="$emit('close')" class="btn-secondary">取消</button>
        <button @click="applySelectedTags" class="btn-primary" :disabled="selectedTagIds.length === 0 || applying">
          {{ applying ? '添加中...' : `添加 ${selectedTagIds.length} 个标签` }}
        </button>
      </div>
    </div>
  </div>
</template>

<script>
import { CreateTag, AddTagToVideo, BatchAddTagToVideos, BatchRemoveTagFromVideos } from '../../wailsjs/go/main/App';
import { selectedTagsFromIds, toggleSelectedTagId, uniqueTagsById } from '../utils/addTagSelection.js';

export default {
  name: 'AddTagDialog',
  props: {
    visible: { type: Boolean, default: false },
    video: { type: Object, default: null },
    tags: { type: Array, default: () => [] },
    videoIds: { type: Array, default: () => [] },
    selectedVideos: { type: Array, default: () => [] },
    mode: { type: String, default: 'single' }
  },
  emits: ['close', 'tag-added'],
  data() {
    return {
      newTagName: '',
      selectedTagIds: [],
      createdTags: [],
      processingTagIds: [],
      applying: false
    };
  },
  computed: {
    isBatchMode() {
      return this.mode === 'batch';
    },
    dialogTitle() {
      return this.isBatchMode ? '批量标签编辑' : '为视频添加标签';
    },
    commonTags() {
      if (!this.isBatchMode || this.selectedVideos.length === 0) return [];
      const selectedCount = this.selectedVideos.length;
      const counts = new Map();
      const byId = new Map();
      for (const video of this.selectedVideos) {
        const seen = new Set();
        for (const tag of video.tags || []) {
          if (!tag || seen.has(tag.id)) continue;
          seen.add(tag.id);
          byId.set(tag.id, tag);
          counts.set(tag.id, (counts.get(tag.id) || 0) + 1);
        }
      }
      return Array.from(counts.entries())
        .filter(([, count]) => count === selectedCount)
        .map(([id]) => byId.get(id))
        .filter(Boolean)
        .sort((a, b) => String(a.name || '').localeCompare(String(b.name || ''), 'zh-Hans-CN'));
    },
    allTags() {
      return uniqueTagsById([...this.tags, ...this.createdTags]);
    },
    availableTags() {
      if (this.isBatchMode) {
        const commonTagIds = new Set(this.commonTags.map(tag => tag.id));
        return this.allTags.filter(tag => !commonTagIds.has(tag.id));
      }
      if (!this.video || !this.video.tags) return this.allTags;
      const videoTagIds = this.video.tags.map(t => t.id);
      return this.allTags.filter(t => !videoTagIds.includes(t.id));
    },
    filteredTags() {
      const kw = this.newTagName.trim().toLowerCase();
      if (!kw) return this.availableTags;
      return this.availableTags.filter(t => t.name.toLowerCase().includes(kw));
    },
    selectedTags() {
      return selectedTagsFromIds(this.allTags, this.selectedTagIds);
    }
  },
  watch: {
    visible(val) {
      if (val) {
        this.newTagName = '';
        this.selectedTagIds = [];
        this.createdTags = [];
        this.processingTagIds = [];
        this.applying = false;
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

        this.createdTags = uniqueTagsById([...this.createdTags, newTag]);
        if (!this.selectedTagIds.includes(Number(newTag.id))) {
          this.selectedTagIds = toggleSelectedTagId(this.selectedTagIds, newTag.id);
        }
        this.newTagName = '';
      } catch (err) {
        console.error('添加标签失败:', err);
      }
    },
    toggleTag(tag) {
      if (!tag) return;
      this.selectedTagIds = toggleSelectedTagId(this.selectedTagIds, tag.id);
    },
    isTagSelected(tag) {
      return this.selectedTagIds.includes(Number(tag?.id));
    },
    async applySelectedTags() {
      if (this.selectedTagIds.length === 0 || this.applying) return;
      this.applying = true;
      try {
        for (const tagID of this.selectedTagIds) {
          await this.addTag(tagID);
        }
        this.$emit('tag-added');
        this.$emit('close');
      } catch (err) {
        console.error('添加标签失败:', err);
        alert('添加标签失败: ' + err);
      } finally {
        this.applying = false;
      }
    },
    async addTag(tagID) {
      if (this.isBatchMode) {
        await BatchAddTagToVideos(this.videoIds, tagID);
        return;
      }
      await AddTagToVideo(this.video.id, tagID);
    },
    async removeCommonTag(tag) {
      if (!this.isBatchMode || !tag) return;
      if (!confirm(`确定要从 ${this.videoIds.length} 个已选视频中移除标签「${tag.name}」吗？`)) return;
      if (this.processingTagIds.includes(tag.id)) return;
      this.processingTagIds = [...this.processingTagIds, tag.id];
      try {
        const result = await BatchRemoveTagFromVideos(this.videoIds, tag.id);
        this.$emit('tag-added');
        if (result?.failed > 0) {
          const firstError = result.errors?.[0];
          alert(`批量移除完成：成功 ${result.succeeded} 个，失败 ${result.failed} 个。${firstError ? `\n首个失败：视频 ${firstError.video_id}，${firstError.error}` : ''}`);
        }
      } catch (err) {
        console.error('批量移除标签失败:', err);
        alert('批量移除标签失败: ' + err);
      } finally {
        this.processingTagIds = this.processingTagIds.filter(id => id !== tag.id);
      }
    }
  }
};
</script>

<style scoped>
.batch-summary {
  padding: 10px 12px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
  background: var(--bg-color);
  margin-bottom: 14px;
}

.batch-summary-title {
  font-weight: 700;
  color: var(--text-primary);
}

.common-tag-list {
  margin-top: 8px;
  max-height: 160px;
  overflow-y: auto;
  padding-right: 4px;
}

.common-tag-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 9px 12px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
  margin-bottom: 6px;
  background: var(--card-bg);
}

.btn-small {
  padding: 5px 9px;
  font-size: 12px;
}

.tag-selector-container {
  max-height: 280px;
  overflow-y: auto;
  padding-right: 4px;
}

.selected-tag-strip {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 10px;
}

.selected-tag-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
  padding: 5px 8px;
  border: 1px solid var(--border-color);
  border-radius: 6px;
  background: var(--bg-color);
  color: var(--text-primary);
  font-size: 13px;
}

.remove-selected-tag {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  border: none;
  border-radius: 50%;
  background: transparent;
  color: var(--text-muted);
  cursor: pointer;
  font-size: 16px;
  line-height: 1;
}

.remove-selected-tag:hover {
  background: rgba(239, 68, 68, 0.12);
  color: var(--danger-color);
}

.tag-selector-container::-webkit-scrollbar { width: 4px; }
.tag-selector-container::-webkit-scrollbar-thumb { background: var(--border-color); border-radius: 4px; }

.clickable-tag-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 14px;
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  cursor: pointer;
  transition: var(--transition);
  margin-bottom: 4px;
}

.clickable-tag-item:hover {
  background: var(--bg-color);
  transform: translateX(4px);
}

.clickable-tag-item.selected {
  border: 1px solid var(--accent-color);
  background: rgba(59, 130, 246, 0.12);
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
