<template>
  <BaseModal v-if="visible" close-on-overlay stop-modal-clicks style="max-width: 400px; text-align: center;" @close="$emit('close')">
      <div style="font-size: 40px; margin-bottom: 16px;">⚠️</div>
      <h2 style="margin-bottom: 12px;">确认删除标签</h2>
      <p style="color: var(--text-primary); margin-bottom: 8px;">确定要永久删除标签 <strong style="color: var(--accent-color);">"{{ tag?.name }}"</strong> 吗？</p>
      <!-- D-PC37：删除前把影响范围说清楚（META-14），回收站里的媒体同样会失去这个标签。 -->
      <p class="tag-delete-impact" data-test="tag-delete-impact">
        <template v-if="usageLoading">正在统计受影响的视频和图片…</template>
        <template v-else-if="usageError">{{ usageError }}</template>
        <template v-else-if="usage">{{ impactText }}</template>
      </p>
      <p class="help-text">此操作不可撤销，该标签将从所有视频和图片中移除，相关待审候选会失效。</p>

      <div class="modal-actions" style="justify-content: center; margin-top: 32px;">
        <button @click="$emit('close')" class="btn-secondary" style="min-width: 100px;">取消</button>
        <button @click="$emit('confirm-delete', tag)" class="btn-danger" style="min-width: 100px;">确认删除</button>
      </div>
  </BaseModal>
</template>

<script>
import { GetTagUsageCounts } from '../../wailsjs/go/main/App';
import BaseModal from './ui/BaseModal.vue';

export default {
  name: 'TagDeleteDialog',
  components: { BaseModal },
  props: {
    visible: { type: Boolean, default: false },
    tag: { type: Object, default: null }
  },
  emits: ['close', 'confirm-delete'],
  data() {
    return { usage: null, usageLoading: false, usageError: '' };
  },
  computed: {
    impactText() {
      const usage = this.usage || {};
      const videos = Number(usage.videos || 0);
      const images = Number(usage.images || 0);
      const trashedVideos = Number(usage.trashed_videos || 0);
      const trashedImages = Number(usage.trashed_images || 0);
      const main = videos || images ? `将从 ${videos} 部视频、${images} 张图片上移除这个标签` : '目前没有视频或图片使用这个标签';
      const trashed = trashedVideos || trashedImages ? `；回收站中另有 ${trashedVideos} 部视频、${trashedImages} 张图片也会失去它` : '';
      return `${main}${trashed}。`;
    }
  },
  watch: {
    visible: { immediate: true, handler(value) { if (value) this.loadUsage(); } },
    'tag.id'() { if (this.visible) this.loadUsage(); }
  },
  methods: {
    // 统计失败只影响说明文字，不挡删除：用户已经在确认框里看到这句失败提示。
    async loadUsage() {
      const tagID = Number(this.tag?.id || 0);
      this.usage = null;
      this.usageError = '';
      if (!tagID) return;
      const token = Symbol('tag-usage');
      this._usageToken = token;
      this.usageLoading = true;
      try {
        const counts = await GetTagUsageCounts([tagID]);
        if (this._usageToken !== token) return;
        this.usage = counts?.[tagID] || counts?.[String(tagID)] || { videos: 0, images: 0 };
      } catch (err) {
        if (this._usageToken === token) this.usageError = `统计受影响的媒体失败：${err}`;
      } finally {
        if (this._usageToken === token) this.usageLoading = false;
      }
    }
  }
};
</script>

<style scoped>
.tag-delete-impact {
  min-height: 20px;
  margin: 0 0 8px;
  color: var(--text-primary);
  font-size: 13px;
}
</style>
