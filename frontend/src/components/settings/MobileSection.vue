<template>
  <div :id="`settings-mobile`" class="settings-section">
    <h3>手机端浏览</h3>
    <div class="short-feed-status">
      <div class="short-feed-status-main">
        <strong>{{ shortFeedStatusText }}</strong>
        <!-- 主位放手机该输的局域网地址；127.0.0.1 只有这台机器自己能开，
             顶在"手机端浏览"下面会让人拿着手机去试一个必然连不上的地址。 -->
        <template v-if="shortFeedStatus && shortFeedStatus.running">
          <span v-if="shortFeedPhoneURL" class="short-feed-url short-feed-url--primary" data-test="short-feed-phone-url">{{ shortFeedPhoneURL }}</span>
          <span v-else class="short-feed-hint" data-test="short-feed-no-lan">没找到局域网地址，手机连不上；检查这台电脑是否连着 Wi-Fi 或有线网。</span>
        </template>
        <span v-else-if="shortFeedStatus && shortFeedStatus.startup_error">{{ shortFeedStatus.startup_error }}</span>
        <span v-else>手机端浏览服务状态未知</span>
      </div>
      <div class="short-feed-actions">
        <button type="button" class="btn-secondary" @click="loadShortFeedStatus">刷新</button>
        <button
          v-if="shortFeedStatus && shortFeedStatus.running"
          type="button"
          class="btn-primary"
          @click="openShortFeed"
        >
          打开
        </button>
      </div>
    </div>
    <!-- 127.0.0.1 不出现在界面上：这一节的用途就是"手机上输哪个地址"，
         环回地址只在「打开」按钮内部使用。多网卡时其余的作为备用地址列出。 -->
    <div v-if="shortFeedAlternateURLs.length" class="short-feed-lan-list">
      <div v-for="url in shortFeedAlternateURLs" :key="url" class="short-feed-url" data-test="short-feed-alt-url">
        备用地址：{{ url }}
      </div>
    </div>
    <div class="setting-item short-feed-duration-setting">
      <label>视频时长上限（分钟）</label>
      <input
        type="number"
        v-model.number="form.short_feed_max_duration_minutes"
        min="1"
        max="180"
        step="1"
        class="number-input"
      />
      <p class="help-text">只有时长小于此上限的视频会进入手机端 Feed，并自动维护“短视频”标签，默认 5 分钟。此上限只对视频生效，图片不受它限制。</p>
    </div>
	  <div class="setting-item">
		<label class="checkbox-label">
		  <input data-test="short-feed-feedback-sync-toggle" type="checkbox" v-model="form.short_feed_feedback_sync_enabled" />
		  <span>将手机端喜欢与收藏同步到主库</span>
		</label>
		<p class="help-text">喜欢会维护“短视频喜欢”自动标签，收藏会写入视频库/图片库的收藏；关闭不会删除已经同步的结果。</p>
	  </div>
    <p class="help-text">手机端 Feed 混编视频与图片：视频自动播放并有进度条，图片停在原地直到你划走，双击可在适屏与原图之间切换。</p>
    <p class="help-text">此页面仅面向本机/局域网直接访问，当前版本不启用登录或 PIN。</p>
  </div>
</template>

<script>
import { GetShortFeedServerStatus } from '../../../wailsjs/go/main/App';

// 手机端浏览分区：短视频流服务的运行状态与局域网地址。
export default {
  name: 'MobileSection',
  props: {
    form: { type: Object, required: true }
  },
  data() {
    return { shortFeedStatus: null };
  },
  mounted() {
    this.loadShortFeedStatus();
  },
  computed: {
    shortFeedPhoneURL() {
      return (this.shortFeedStatus?.lan_urls || [])[0] || '';
    },
    shortFeedAlternateURLs() {
      return (this.shortFeedStatus?.lan_urls || []).slice(1);
    },
    shortFeedStatusText() {
      if (!this.shortFeedStatus) return '未加载';
      if (this.shortFeedStatus.running) {
        return this.shortFeedStatus.fallback_used ? '运行中（备用端口）' : '运行中';
      }
      return '未运行';
    },
  },
  methods: {
    async loadShortFeedStatus() {
      try {
        this.shortFeedStatus = await GetShortFeedServerStatus();
      } catch (err) {
        this.shortFeedStatus = {
          running: false,
          startup_error: String(err)
        };
      }
    },
    openShortFeed() {
      if (this.shortFeedStatus?.url) {
        window.open(this.shortFeedStatus.url, '_blank', 'noopener,noreferrer');
      }
    },
  }
};
</script>

<style scoped>

.short-feed-lan-list {
  display: grid;
  gap: 6px;
  margin-top: 10px;
}

.short-feed-url {
  padding: 8px 10px;
  border-radius: 6px;
  background: var(--bg-color);
}

</style>
