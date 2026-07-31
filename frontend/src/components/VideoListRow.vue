<template>
  <div
    :class="['video-item', `video-item--${layoutMode}`, { 'video-item--selected': selected }]"
    @contextmenu.prevent="$emit('contextmenu', $event, video)"
  >
    <label class="video-select" @click.stop>
      <input
        type="checkbox"
        :checked="selected"
        :aria-label="`选择 ${video.name}`"
        @change="$emit('toggle-select', video, $event.target.checked)"
      />
    </label>
    <div class="video-thumbnail" :class="{ 'video-thumbnail--failed': thumbnailFailed }">
      <img
        v-if="!thumbnailFailed"
        :src="thumbnailURL"
        :alt="`${video.name} 缩略图`"
        loading="lazy"
        @error="thumbnailFailed = true"
      />
      <span v-else aria-hidden="true">▶</span>
    </div>
    <div class="video-info">
      <h3>{{ video.display_title || video.name }}</h3>
      <p class="video-path"><span v-if="video.display_title">{{ video.name }} · </span>{{ getDirectoryLabel(video) }}</p>
      <div class="video-meta">
        <span class="video-size">{{ formatSize(video.size) }}</span>
        <span v-if="video.duration" class="meta-divider">|</span>
        <span v-if="video.duration" class="video-duration">{{ formatDuration(video.duration) }}</span>
        <span v-if="video.resolution" class="meta-divider">|</span>
        <span v-if="video.resolution" class="video-resolution">{{ video.resolution }}</span>
        <span v-if="video.is_stale" class="meta-divider">|</span>
        <span v-if="video.is_stale" class="video-stale">路径失效</span>
        <span v-if="video.is_watched" class="meta-divider">|</span>
        <span v-if="video.is_watched" class="video-watched">已看</span>
        <span v-if="video.personal_rating !== null && video.personal_rating !== undefined" class="meta-divider">|</span>
        <span v-if="video.personal_rating !== null && video.personal_rating !== undefined" class="video-rating">评分 {{ video.personal_rating }}/10</span>
      </div>
      <div v-if="watchProgressPercent > 0" class="video-watch-progress" :title="watchProgressLabel">
        <span :style="{ width: `${watchProgressPercent}%` }"></span>
      </div>
      <button v-if="video._subtitleMatchText" type="button" class="video-subtitle-hit" @click="$emit('preview', video)">
        字幕命中 {{ formatTimestamp(video._subtitleMatchStartMs) }}：{{ video._subtitleMatchText }}
      </button>
      <div class="video-tags">
        <span
          v-for="tag in (video.tags || [])"
          :key="tag.id"
          class="tag-badge"
          :style="{ backgroundColor: tagBgColor(tag.color) }"
        >
          {{ tag.name }}
          <button v-if="!tag.automatic_kind" @click="$emit('remove-tag', video, tag)" class="tag-remove">×</button>
        </span>
        <button @click="$emit('open-add-tag', video)" class="btn-add-tag">+ 标签</button>
      </div>
    </div>

    <div class="video-actions">
      <div class="row-primary-actions">
        <button @click="$emit('preview', video)" class="btn-secondary btn-compact">预览</button>
        <button @click="$emit('play', video.id)" class="btn-action btn-compact">播放</button>
        <button
          @click="$emit('toggle-favorite', video)"
          :class="['btn-action', 'btn-compact', { active: video.is_favorite }]"
          :aria-pressed="!!video.is_favorite"
        >{{ video.is_favorite ? '★ 已收藏' : '☆ 收藏' }}</button>
        <button
          @click="$emit('toggle-watched', video)"
          :class="['btn-action', 'btn-compact', { active: video.is_watched }]"
          :aria-pressed="!!video.is_watched"
        >{{ video.is_watched ? '✓ 已看' : '标记已看' }}</button>
      </div>
      <div class="row-secondary-actions">
      <button @click="$emit('open-directory', video.id)" class="btn-action btn-compact">目录</button>
      <button
        @click="$emit('generate-subtitle', video)"
        class="btn-action btn-compact"
        :class="{ 'btn-processing': generatingSubtitleIds.includes(video.id) }"
        :disabled="generatingSubtitleIds.includes(video.id)"
      >
        {{ generatingSubtitleIds.includes(video.id) ? '生成中...' : '字幕' }}
      </button>
      <button @click="$emit('subtitle-edit', video)" class="btn-action btn-compact">编辑字幕</button>
      <button @click="$emit('subtitle-preview', video)" class="btn-action btn-compact">预览字幕</button>
      <button @click="$emit('rename', video)" class="btn-action btn-compact">重命名</button>
      <button @click="$emit('move', video)" class="btn-action btn-compact">迁移</button>
      <button @click="$emit('delete', video)" class="btn-danger btn-compact" :disabled="deletingIds.includes(video.id)">删除</button>
      </div>
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
    deletingIds: { type: Array, default: () => [] },
    selected: { type: Boolean, default: false },
    layoutMode: { type: String, default: 'list' }
  },
  emits: ['preview', 'play', 'toggle-favorite', 'toggle-watched', 'open-directory', 'generate-subtitle', 'subtitle-edit', 'subtitle-preview', 'rename', 'move', 'delete', 'open-add-tag', 'remove-tag', 'contextmenu', 'toggle-select'],
  data() {
    return { thumbnailFailed: false };
  },
  computed: {
    thumbnailURL() {
      return `/preview/thumbnail/${this.video.id}`;
    },
    watchProgressPercent() {
      const duration = Number(this.video.duration || 0);
      const position = Number(this.video.watch_position_seconds || 0);
      if (duration <= 0 || position <= 0) return 0;
      return Math.min(100, Math.max(0, (position / duration) * 100));
    },
    watchProgressLabel() {
      return `看到 ${this.formatDuration(this.video.watch_position_seconds)} / ${this.formatDuration(this.video.duration)}`;
    }
  },
  watch: {
    'video.id'() {
      this.thumbnailFailed = false;
    }
  },
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
    },
    formatTimestamp(ms) {
      const totalSeconds = Math.max(0, Math.floor(Number(ms) / 1000) || 0);
      const hours = Math.floor(totalSeconds / 3600);
      const minutes = Math.floor((totalSeconds % 3600) / 60);
      const seconds = totalSeconds % 60;
      const parts = [minutes, seconds].map(value => String(value).padStart(2, '0'));
      if (hours > 0) parts.unshift(String(hours).padStart(2, '0'));
      return parts.join(':');
    }
  }
};
</script>
