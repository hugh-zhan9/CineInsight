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

    <!-- 缩略图墙（2026-10-12 用户裁决「只要一个紧凑的缩略图墙」）：整块缩略图就是网格卡上的「预览」，
         标题只进悬停提示和读屏名，其余动作走右键菜单；失效只留一个角标。 -->
    <button
      v-if="layoutMode === 'wall'"
      type="button"
      class="btn-link video-thumbnail video-wall-tile"
      :class="{ 'video-thumbnail--failed': thumbnailFailed }"
      :title="wallLabel"
      :aria-label="wallLabel"
      data-test="library-wall-tile"
      @click="$emit('preview', video)"
    >
      <img v-if="!thumbnailFailed" :src="thumbnailURL" alt="" loading="lazy" @error="thumbnailFailed = true" />
      <AppIcon v-else name="play" :size="24" />
      <span v-if="video.is_stale" class="video-badge video-badge--danger video-wall-tile__stale" aria-hidden="true" data-test="wall-stale-reason">失效</span>
    </button>
    <div v-else class="video-thumbnail" :class="{ 'video-thumbnail--failed': thumbnailFailed }">
      <img
        v-if="!thumbnailFailed"
        :src="thumbnailURL"
        :alt="`${video.name} 缩略图`"
        loading="lazy"
        @error="thumbnailFailed = true"
      />
      <AppIcon v-else name="play" :size="24" />
      <span v-if="video.duration" class="video-thumbnail__duration">{{ formatDuration(video.duration) }}</span>
      <span v-if="layoutMode === 'grid' && video._semanticScore !== null && video._semanticScore !== undefined" class="video-thumbnail__score">
        {{ formatSemanticScore(video._semanticScore) }}
      </span>
      <span v-if="watchProgressPercent > 0" class="video-thumbnail__progress" :title="watchProgressLabel">
        <i :style="{ width: `${watchProgressPercent}%` }"></i>
      </span>
    </div>

    <div v-if="layoutMode !== 'wall'" class="video-info">
      <div class="video-title-row">
        <h3 :title="video.display_title || video.name">{{ video.display_title || video.name }}</h3>
        <span v-if="video.is_watched" class="video-badge video-badge--accent">已看</span>
        <span
          v-if="video.is_stale"
          class="video-badge video-badge--danger"
          :title="staleReasonHint"
          data-test="row-stale-reason"
        >路径失效 · {{ staleReasonText }}</span>
        <!-- 多版本聚合（D-MW-VERSIONS）：属于活跃成员 ≥2 的版本组时显示「N 个版本」与汇总，点击在卡片内展开。 -->
        <VersionGroupBadge v-if="versionSummary" :summary="versionSummary" :expanded="versionsExpanded" @toggle="$emit('toggle-versions', video)" />
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
        <!-- 正式播放次数（videos.play_count，口径不变）；随机与应用内观看的流水见「回顾」页。 -->
        <template v-if="Number(video.play_count) > 0">
          <span class="meta-divider">|</span>
          <span data-test="row-play-count" title="外部播放器正式播放次数；随机播放与应用内观看见「回顾」">播放 {{ video.play_count }} 次</span>
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
            <!-- 自动标签被人工覆盖（D-PC36、META-13）：角标数据按视频批量带出，不逐行查询。 -->
            <span
              v-if="isManualOverride(tag)"
              class="tag-manual-badge"
              title="这个自动标签是你手动调整的，自动规则不再动它；可在「+ 标签」里恢复自动"
              data-test="row-tag-manual"
            >手动</span>
            <button
              v-if="!tag.automatic_kind || ['short_video', 'low_resolution'].includes(tag.automatic_kind)"
              type="button"
              class="btn-icon tag-remove video-tag-remove"
              :aria-label="`移除标签 ${tag.name}`"
              @click="$emit('remove-tag', video, tag)"
            ><AppIcon name="close" :size="16" /></button>
          </span>
        </div>
        <button type="button" class="btn btn-add-tag" @click="$emit('open-add-tag', video)">+ 标签</button>
        <button
          v-if="video._subtitleMatchText"
          type="button"
          class="btn-link video-subtitle-hit"
          @click="$emit('preview', video)"
        >{{ formatTimestamp(video._subtitleMatchStartMs) }} 「{{ video._subtitleMatchText }}」</button>
      </div>
    </div>

    <!-- 常驻四个高频动作，其余七个进 ⋯。不做"悬停才出现"：
         鼠标扫过时整列按钮闪烁反而更难扫读。
         多选态下整排隐去（不留占位文案，批量操作条已经说明了当前处境）。 -->
    <div v-if="!actionsSuspended && layoutMode !== 'wall'" class="video-actions">
      <button v-if="!narrow" type="button" class="btn btn--secondary btn--sm video-row-btn" @click="$emit('preview', video)">预览</button>
      <button type="button" class="btn btn--primary btn--sm video-row-btn" @click="$emit('play', video.id)">播放</button>
      <button
        type="button"
        :class="['btn-icon', 'btn--secondary', 'btn--sm', 'video-row-btn', 'video-row-btn--icon', { 'video-row-btn--fav': video.is_favorite }]"
        :aria-pressed="!!video.is_favorite"
        :aria-label="video.is_favorite ? '取消收藏' : '收藏'"
        @click="$emit('toggle-favorite', video)"
      ><AppIcon name="heart" :size="16" /></button>
      <!-- 点赞（D-PC40、PLAY-02）：窄行与网格卡放不下，只留在 ⋯ 菜单里。 -->
      <button
        v-if="!narrow && layoutMode !== 'grid'"
        type="button"
        :class="['btn', 'btn--secondary', 'btn--sm', 'video-row-btn', 'video-row-btn--icon', { 'video-row-btn--on': video.is_liked }]"
        :aria-pressed="!!video.is_liked"
        :aria-label="video.is_liked ? '取消点赞' : '点赞'"
        data-test="row-like"
        @click="$emit('toggle-liked', video)"
      >赞</button>
      <!-- 「已看」在抽屉打开时的窄行里同样保留（D-PC47、PLAY-14）：边预览边标记是最常见的用法。 -->
      <button
        type="button"
        :class="['btn', 'btn--secondary', 'btn--sm', 'video-row-btn', { 'video-row-btn--on': video.is_watched }]"
        :aria-pressed="!!video.is_watched"
        data-test="row-watched"
        @click="$emit('toggle-watched', video)"
      >已看</button>
      <button
        type="button"
        class="btn-icon btn--secondary btn--sm video-row-btn video-row-btn--icon"
        aria-label="更多操作"
        aria-haspopup="menu"
        @click="$emit('open-row-menu', video, $event.currentTarget)"
      ><AppIcon name="more" :size="16" /></button>
    </div>
  </div>
</template>

<script>
import { tagBackgroundColor } from '../utils/tagColor.js';
import { resumePosition } from '../utils/watchState.js';
import VersionGroupBadge from './video-list/VersionGroupBadge.vue';
import AppIcon from './ui/AppIcon.vue';

// 失效原因（videos.stale_reason，D-PC06）的中文名。片库页的「路径失效」分组复用同一份。
// unknown 是后端 ListStaleReasonCounts 对空原因（历史失效行）的归类。
export const STALE_REASON_LABELS = {
  offline_root: '磁盘未连接',
  missing_file: '文件不存在',
  removed_root: '目录已移除',
  outside_roots: '不在任何扫描目录',
  play_failed: '播放失败',
  read_error: '读取失败',
  watcher_missing: '实时监听发现消失',
  unknown: '原因未记录'
};

// 每种原因的下一步提示，悬停在失效徽标上可见。
const STALE_REASON_HINTS = {
  offline_root: '文件所在的磁盘没有连接。接上磁盘后会自动恢复。',
  missing_file: '原位置找不到这个文件。文件回到原处后点「重新检查」即可恢复。',
  removed_root: '它所在的扫描目录已被删除。把目录加回来，记录会自动恢复。',
  outside_roots: '它不在任何扫描目录里。把所在目录加入扫描目录后会恢复。',
  play_failed: '播放时没能打开这个文件。确认文件在原处后点「重新检查」。',
  read_error: '扫描时读不了这个文件（可能是权限问题）。处理后点「重新检查」。',
  watcher_missing: '实时监听发现文件已不在原处。文件回来后点「重新检查」即可恢复。',
  unknown: '这条记录失效时还没有记录原因。点「重新检查」看看文件是否还在。'
};

export function staleReasonLabel(reason) {
  return STALE_REASON_LABELS[String(reason || '') || 'unknown'] || STALE_REASON_LABELS.unknown;
}

export default {
  name: 'VideoListRow',
  components: { AppIcon, VersionGroupBadge },
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
    actionsSuspended: { type: Boolean, default: false },
    // 该视频被人工覆盖、结果为「加上」的自动标签种类（automatic_override_kinds[String(id)]）。
    overrideKinds: { type: Array, default: () => [] },
    // 本行代表的版本组汇总（LibraryVideoPage.version_groups[id]）；不属于组时为 null。
    versionSummary: { type: Object, default: null },
    versionsExpanded: { type: Boolean, default: false }
  },
  emits: ['preview', 'play', 'toggle-favorite', 'toggle-liked', 'toggle-watched', 'open-add-tag', 'remove-tag', 'contextmenu', 'toggle-select', 'open-row-menu', 'toggle-versions'],
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
    // 进度条与抽屉起播共用 watchState.js 的 resumePosition（D-PC41/42）：标已看之前留下的旧断点
    // 不显示；标已看之后又在看的（重看）照常显示进度（PLAY-10）；落进片尾区间的按看完处理。
    watchProgressPercent() {
      const duration = Number(this.video.duration || 0);
      const position = resumePosition(this.video);
      if (duration <= 0 || position <= 0) return 0;
      return Math.min(100, Math.max(0, (position / duration) * 100));
    },
    watchProgressLabel() {
      const verb = this.video.is_watched ? '重看到' : '看到';
      return `${verb} ${this.formatDuration(this.video.watch_position_seconds)} / ${this.formatDuration(this.video.duration)}`;
    },
    staleReasonText() {
      return staleReasonLabel(this.video.stale_reason);
    },
    staleReasonHint() {
      return STALE_REASON_HINTS[String(this.video.stale_reason || '') || 'unknown'] || STALE_REASON_HINTS.unknown;
    },
    // 缩略图墙不显示文字：标题和失效原因都只进悬停提示与读屏名。
    wallLabel() {
      const title = this.video.display_title || this.video.name;
      return this.video.is_stale ? `${title}（路径失效 · ${this.staleReasonText}）` : title;
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
    isManualOverride(tag) {
      const kind = String(tag?.automatic_kind || '');
      return !!kind && (this.overrideKinds || []).includes(kind);
    },
    formatSemanticScore(value) {
      return `${Math.round(Math.max(-1, Math.min(1, Number(value) || 0)) * 100)}%`;
    },
    tagBgColor(hex) {
      return tagBackgroundColor(hex);
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

<style scoped>
/* 行内动作（统一按钮 .btn--sm）：外观来自全局按钮类，这里只管行里的尺寸与开关态。
   收藏、点赞、已看是开关：开着时用强调色（收藏用危险色），悬停时保持开着的样子。 */
.video-row-btn--icon { width: var(--h-compact); padding: 0; color: var(--text-secondary); }
.video-row-btn--on,
.video-row-btn--on:hover:not(:disabled) { border-color: var(--accent-color); background: var(--accent-soft); color: var(--accent-text); font-weight: var(--fw-semibold); }
.video-row-btn--fav,
.video-row-btn--fav:hover:not(:disabled) { border-color: var(--danger-border); background: var(--danger-soft); color: var(--danger-text); }
/* 网格卡底部一行四个动作平分宽度，图标按钮固定宽。 */
.video-item--grid .video-row-btn { flex: 1; padding: 0; }
.video-item--grid .video-row-btn--icon { flex: 0 0 var(--h-compact); }

/* 标签上的 ×：嵌在 22px 高的标签里，不要图标按钮的 34px 方块。 */
.video-tag-remove { width: 16px; height: 16px; border-radius: var(--radius-sm); }
.video-tag-remove:hover:not(:disabled) { background: transparent; opacity: 1; }

/* 字幕命中片段是一段可点的引文（.btn-link），单行省略。 */
.video-subtitle-hit { display: inline-block; }
.video-subtitle-hit:hover:not(:disabled) { text-decoration: none; border-color: var(--accent-color); }

/* 缩略图墙的格子整块可点（.btn-link），不要链接的下划线。 */
.video-wall-tile:hover:not(:disabled) { text-decoration: none; }
</style>
