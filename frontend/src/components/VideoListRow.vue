<template>
  <div
    :class="[
      'video-item',
      `video-item--${layoutMode}`,
      `video-item--${density}`,
      { 'video-item--selected': selected, 'video-item--focused': keyboardFocused, 'video-item--narrow': narrow }
    ]"
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
      <span v-if="video.duration" class="video-thumbnail__duration">{{ formatDuration(video.duration) }}</span>
      <span v-if="layoutMode === 'grid' && video._semanticScore !== null && video._semanticScore !== undefined" class="video-thumbnail__score">
        {{ formatSemanticScore(video._semanticScore) }}
      </span>
      <span v-if="watchProgressPercent > 0" class="video-thumbnail__progress" :title="watchProgressLabel">
        <i :style="{ width: `${watchProgressPercent}%` }"></i>
      </span>
    </div>

    <div class="video-info">
      <div class="video-title-row">
        <h3 :title="video.display_title || video.name">{{ video.display_title || video.name }}</h3>
        <span v-if="video.is_watched" class="video-badge video-badge--accent">已看</span>
        <span v-if="video.is_stale" class="video-badge video-badge--danger">路径失效</span>
      </div>

      <p class="video-path" :title="video.path">{{ video.name }} <span class="video-path__sep">·</span> {{ getDirectoryLabel(video) }}</p>

      <div class="video-meta">
        <span>{{ formatSize(video.size) }}</span>
        <span class="meta-divider">|</span>
        <span>{{ video.resolution || '未知分辨率' }}</span>
        <span class="meta-divider">|</span>
        <span class="video-rating">{{ ratingLabel }}</span>
        <template v-if="watchProgressPercent > 0">
          <span class="meta-divider">|</span>
          <span>{{ watchProgressLabel }}</span>
        </template>
        <template v-if="video._semanticScore !== null && video._semanticScore !== undefined">
          <span class="meta-divider">|</span>
          <span class="video-semantic-score">相似度 {{ formatSemanticScore(video._semanticScore) }}</span>
        </template>
      </div>

      <div class="video-tags">
        <!-- 标签全部渲染、照实换行，一个都不折叠不截断；行随之长高，虚拟列表按实测行高定位。
             "+ 标签"留在条带外面常驻。 -->
        <div ref="tagStrip" class="video-tags__strip" data-test="row-tag-strip">
          <span
            v-for="tag in (video.tags || [])"
            :key="tag.id"
            class="tag-badge"
            :style="{ backgroundColor: tagBgColor(tag.color) }"
          >
            <span class="tag-badge__name">{{ tag.name }}</span>
            <button v-if="!tag.automatic_kind" @click="$emit('remove-tag', video, tag)" class="tag-remove">×</button>
          </span>
        </div>
        <button @click="$emit('open-add-tag', video)" class="btn-add-tag">+ 标签</button>
        <button
          v-if="video._subtitleMatchText"
          type="button"
          class="video-subtitle-hit"
          @click="$emit('preview', video)"
        >{{ formatTimestamp(video._subtitleMatchStartMs) }} 「{{ video._subtitleMatchText }}」</button>
      </div>
    </div>

    <!-- 常驻四个高频动作，其余七个进 ⋯。不做"悬停才出现"：
         鼠标扫过时整列按钮闪烁反而更难扫读。
         多选态下整排隐去（不留占位文案，批量操作条已经说明了当前处境）。 -->
    <div v-if="!actionsSuspended" class="video-actions">
      <button v-if="!narrow" type="button" class="row-btn" @click="$emit('preview', video)">预览</button>
      <button type="button" class="row-btn row-btn--primary" @click="$emit('play', video.id)">播放</button>
      <button
        type="button"
        :class="['row-btn', 'row-btn--icon', { 'row-btn--fav': video.is_favorite }]"
        :aria-pressed="!!video.is_favorite"
        :aria-label="video.is_favorite ? '取消收藏' : '收藏'"
        @click="$emit('toggle-favorite', video)"
      >♥</button>
      <button
        v-if="!narrow"
        type="button"
        :class="['row-btn', { active: video.is_watched }]"
        :aria-pressed="!!video.is_watched"
        @click="$emit('toggle-watched', video)"
      >已看</button>
      <button
        type="button"
        class="row-btn row-btn--icon"
        aria-label="更多操作"
        aria-haspopup="menu"
        @click="$emit('open-row-menu', video, $event.currentTarget)"
      >⋯</button>
    </div>
  </div>
</template>

<script>
export default {
  name: 'VideoListRow',
  props: {
    video: { type: Object, required: true },
    directories: { type: Array, default: () => [] },
    selected: { type: Boolean, default: false },
    keyboardFocused: { type: Boolean, default: false },
    layoutMode: { type: String, default: 'list' },
    density: { type: String, default: 'compact' },
    // 详情抽屉展开后列表收窄，行切到窄变体：缩略图变小，只留播放 / ♥ / ⋯
    narrow: { type: Boolean, default: false },
    // 多选态下整排隐去行内动作：这时用户的目标是批量操作，
    // 行内按钮只会带来误点。
    actionsSuspended: { type: Boolean, default: false }
  },
  emits: ['preview', 'play', 'toggle-favorite', 'toggle-watched', 'open-add-tag', 'remove-tag', 'contextmenu', 'toggle-select', 'open-row-menu'],
  data() {
    return {
      thumbnailFailed: false
    };
  },
  computed: {
    thumbnailURL() {
      return `/preview/thumbnail/${this.video.id}`;
    },
    ratingLabel() {
      const rating = this.video.personal_rating;
      if (rating === null || rating === undefined) return '未评分';
      return `评分 ${rating}/10`;
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
    // 虚拟列表会复用行组件：换了视频必须重置失败标记，
    // 否则一次缩略图失败会让后面复用到这一行的视频都显示占位。
    'video.id'() {
      this.thumbnailFailed = false;
    }
  },
  methods: {
    formatSemanticScore(value) {
      return `${Math.round(Math.max(-1, Math.min(1, Number(value) || 0)) * 100)}%`;
    },
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
