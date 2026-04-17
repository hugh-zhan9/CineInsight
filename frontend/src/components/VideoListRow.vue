<template>
  <div
    class="video-item"
    @contextmenu.prevent="$emit('contextmenu', $event, video)"
  >
    <div class="video-info">
      <h3>{{ video.name }}</h3>
      <p class="video-path">{{ getDirectoryLabel(video) }}</p>
      <div class="video-meta">
        <span class="video-size">{{ formatSize(video.size) }}</span>
        <span v-if="video.duration" class="meta-divider">|</span>
        <span v-if="video.duration" class="video-duration">{{ formatDuration(video.duration) }}</span>
        <span v-if="video.resolution" class="meta-divider">|</span>
        <span v-if="video.resolution" class="video-resolution">{{ video.resolution }}</span>
        <span v-if="video.is_stale" class="meta-divider">|</span>
        <span v-if="video.is_stale" class="video-stale">路径失效</span>
      </div>
      <p v-if="video._subtitleMatchText" class="video-subtitle-hit">字幕命中: {{ video._subtitleMatchText }}</p>
      <div class="video-tags">
        <span
          v-for="tag in (video.tags || [])"
          :key="tag.id"
          class="tag-badge"
          :style="{ backgroundColor: tagBgColor(tag.color) }"
        >
          {{ tag.name }}
          <button @click="$emit('remove-tag', video, tag)" class="tag-remove">×</button>
        </span>
        <button @click="$emit('open-add-tag', video)" class="btn-add-tag">+ 标签</button>
      </div>
    </div>

    <div class="video-actions">
      <button @click="$emit('preview', video)" class="btn-secondary">预览</button>
      <button @click="$emit('play', video.id)" class="btn-action">播放</button>
      <button @click="$emit('open-directory', video.id)" class="btn-action">打开目录</button>
      <button
        @click="$emit('generate-subtitle', video)"
        class="btn-action"
        :class="{ 'btn-processing': generatingSubtitleIds.includes(video.id) }"
        :disabled="generatingSubtitleIds.includes(video.id)"
      >
        {{ generatingSubtitleIds.includes(video.id) ? '生成中...' : '字幕' }}
      </button>
      <button @click="$emit('subtitle-preview', video)" class="btn-action">字幕预览</button>
      <button @click="$emit('rename', video)" class="btn-action">重命名</button>
      <button @click="$emit('delete', video)" class="btn-danger" :disabled="deletingIds.includes(video.id)">删除</button>
    </div>
  </div>
</template>

<script>
export default {
  name: 'VideoListRow',
  props: {
    video: { type: Object, required: true },
    directories: { type: Array, default: () => [] },
    generatingSubtitleIds: { type: Array, default: () => [] },
    deletingIds: { type: Array, default: () => [] }
  },
  emits: ['preview', 'play', 'open-directory', 'generate-subtitle', 'subtitle-preview', 'rename', 'delete', 'open-add-tag', 'remove-tag', 'contextmenu'],
  methods: {
    tagBgColor(hex) {
      if (!hex || !hex.startsWith('#')) return hex;
      const r = parseInt(hex.slice(1, 3), 16);
      const g = parseInt(hex.slice(3, 5), 16);
      const b = parseInt(hex.slice(5, 7), 16);
      return `rgba(${r},${g},${b},0.35)`;
    },
    getDirectoryLabel(video) {
      if (!this.directories || this.directories.length === 0) return video.directory;

      const sortedDirs = [...this.directories]
        .filter((dir) => dir.alias)
        .sort((a, b) => b.path.length - a.path.length);

      for (const dir of sortedDirs) {
        if (dir.path === video.directory) {
          return dir.alias;
        }

        const isWindows = video.directory.includes('\\');
        const sep = isWindows ? '\\' : '/';
        const prefix = dir.path.endsWith(sep) ? dir.path : dir.path + sep;

        if (video.directory.startsWith(prefix)) {
          const suffix = video.directory.substring(prefix.length);
          return `${dir.alias}${sep}${suffix}`;
        }
      }

      return video.directory;
    },
    formatSize(bytes) {
      if (bytes === 0 || bytes === null || bytes === undefined) return '0 B';
      const k = 1024;
      const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
      const i = Math.floor(Math.log(bytes) / Math.log(k));
      const value = bytes / Math.pow(k, i);
      return `${value.toFixed(value >= 10 || i === 0 ? 0 : 1)} ${sizes[i]}`;
    },
    formatDuration(seconds) {
      if (!seconds) return '';
      const h = Math.floor(seconds / 3600);
      const m = Math.floor((seconds % 3600) / 60);
      const s = Math.floor(seconds % 60);
      const parts = [];
      if (h > 0) parts.push(h.toString().padStart(2, '0'));
      parts.push(m.toString().padStart(2, '0'));
      parts.push(s.toString().padStart(2, '0'));
      return parts.join(':');
    }
  }
};
</script>
