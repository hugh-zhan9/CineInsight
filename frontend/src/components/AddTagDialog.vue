<template>
  <div v-if="visible" class="modal-overlay" @click="$emit('close')">
    <div class="modal" @click.stop>
      <h2>为视频添加标签</h2>

      <!-- 手动输入新标签 -->
      <div class="form-group">
        <div class="input-with-button">
          <input
            v-model="newTagName"
            type="text"
            placeholder="输入新标签名称..."
            @keyup.enter="addNewTag"
          />
          <input v-model="newTagColor" type="color" />
          <button @click="addNewTag" class="btn-primary">创建并添加</button>
        </div>
        <p class="hint-text">输入新标签名称，选择颜色，点击创建并添加</p>
      </div>

      <div class="divider">或选择已有标签</div>

      <!-- 已有标签列表 -->
      <div class="tag-list">
        <div
          v-for="tag in availableTags"
          :key="tag.id"
          class="tag-item clickable"
          @click="addExistingTag(tag)"
        >
          <span class="tag-color" :style="{ backgroundColor: tag.color }"></span>
          <span>{{ tag.name }}</span>
        </div>
        <div v-if="availableTags.length === 0" class="empty-hint">
          所有标签都已添加
        </div>
      </div>
      <button @click="$emit('close')" class="btn-secondary">关闭</button>
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
      newTagName: '',
      newTagColor: '#3b82f6'
    };
  },
  computed: {
    availableTags() {
      if (!this.video || !this.video.tags) return this.tags;
      const videoTagIds = this.video.tags.map(t => t.id);
      return this.tags.filter(t => !videoTagIds.includes(t.id));
    }
  },
  watch: {
    visible(val) {
      if (val) {
        this.newTagName = '';
        this.newTagColor = '#3b82f6';
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
      if (!tagName) {
        alert('请输入标签名称');
        return;
      }

      try {
        let newTag = null;
        try {
          newTag = await CreateTag(tagName, this.newTagColor);
        } catch (err) {
          if (this.isDuplicateError(err)) {
            newTag = this.tags.find(t => t.name === tagName) || null;
          } else {
            throw err;
          }
        }

        if (!newTag) {
          alert('标签已存在，但未找到对应记录');
          return;
        }

        await AddTagToVideo(this.video.id, newTag.id);
        this.newTagName = '';
        this.newTagColor = '#3b82f6';
        this.$emit('tag-added');
        this.$emit('close');
      } catch (err) {
        console.error('添加标签失败:', err);
        alert('添加标签失败: ' + err);
      }
    },
    async addExistingTag(tag) {
      try {
        await AddTagToVideo(this.video.id, tag.id);
        this.$emit('tag-added');
        this.$emit('close');
      } catch (err) {
        console.error('添加标签失败:', err);
        alert('添加标签失败: ' + err);
      }
    }
  }
};
</script>
