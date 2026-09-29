<template>
  <div :id="`settings-mobile`" class="settings-section">
    <h3>手机端浏览</h3>

    <!-- 老库升级后服务默认仍开着（D-PC45 保持现状），这时还没有 PIN 就提醒一次；
         关掉提示只记在本机（localStorage），不新增数据库列。 -->
    <div v-if="pinBannerVisible" class="short-feed-pin-banner" data-test="short-feed-pin-banner" role="status">
      <span>手机端访问已开启但还没有设置 PIN：同一局域网里的任何设备都能打开，并可以收藏或删除视频。建议设置 PIN。</span>
      <button type="button" class="btn-secondary btn-compact" data-test="short-feed-pin-banner-dismiss" @click="dismissPinBanner">不再提示</button>
    </div>

    <div class="setting-item">
      <label class="checkbox-label">
        <input
          type="checkbox"
          data-test="short-feed-enabled-toggle"
          :checked="shortFeedEnabled"
          :disabled="toggleBusy || !accessLoaded"
          @change="toggleShortFeed($event)"
        />
        <span>开启手机端访问</span>
      </label>
      <p class="help-text">开关即时生效：关闭后立刻停止监听，手机上再也打不开；不需要点「保存所有设置」。</p>
      <p v-if="toggleError" class="settings-error" role="alert" data-test="short-feed-toggle-error">{{ toggleError }}</p>
    </div>

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
        <span v-else-if="accessLoaded && !shortFeedEnabled">手机端访问已关闭。</span>
        <span v-else>手机端浏览服务状态未知</span>
      </div>
      <div class="short-feed-actions">
        <button type="button" class="btn-secondary" @click="refreshAll">刷新</button>
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
    <!-- 手机扫码即可打开（D-PC47）。非 macOS 平台后端返回空串，这里就不显示。 -->
    <div v-if="qrCode && shortFeedStatus && shortFeedStatus.running" class="short-feed-qr" data-test="short-feed-qr">
      <img :src="qrCode" alt="手机扫码打开手机端浏览" />
      <span>用手机相机扫码打开上面的地址。</span>
    </div>
    <!-- 127.0.0.1 不出现在界面上：这一节的用途就是"手机上输哪个地址"，
         环回地址只在「打开」按钮内部使用。多网卡时其余的作为备用地址列出。 -->
    <div v-if="shortFeedAlternateURLs.length" class="short-feed-lan-list">
      <div v-for="url in shortFeedAlternateURLs" :key="url" class="short-feed-url" data-test="short-feed-alt-url">
        备用地址：{{ url }}
      </div>
    </div>

    <!-- 访问 PIN（D-PC45）：6–32 个字符；设置、修改、清除都会让已登录的手机重新输入。 -->
    <div class="setting-item short-feed-pin" data-test="short-feed-pin">
      <label for="short-feed-pin-input">访问 PIN</label>
      <p class="help-text" data-test="short-feed-pin-state">{{ pinStateText }}</p>
      <div class="short-feed-pin-row">
        <input
          id="short-feed-pin-input"
          v-model="pinInput"
          type="password"
          class="text-input"
          autocomplete="new-password"
          maxlength="32"
          :placeholder="accessStatus?.pin_set ? '输入新的 PIN' : '6–32 个字符'"
          :disabled="pinBusy || !accessLoaded"
          data-test="short-feed-pin-input"
        />
        <button type="button" class="btn-primary" :disabled="pinBusy || !accessLoaded" data-test="short-feed-pin-save" @click="savePIN">
          {{ accessStatus?.pin_set ? '修改 PIN' : '设置 PIN' }}
        </button>
        <button
          v-if="accessStatus?.pin_set"
          type="button"
          class="btn-secondary btn-danger-outline"
          :disabled="pinBusy"
          data-test="short-feed-pin-clear"
          @click="clearPIN"
        >清除 PIN</button>
      </div>
      <p v-if="pinMessage" :class="['help-text', { 'settings-error': pinMessageIsError }]" data-test="short-feed-pin-message" :role="pinMessageIsError ? 'alert' : 'status'">{{ pinMessage }}</p>
      <!-- 输错太多次会锁住所有新登录（24 小时后自动解除）；在电脑上可以直接解除。 -->
      <div v-if="accessStatus?.login_locked" class="short-feed-lock" data-test="short-feed-login-locked" role="status">
        <span>{{ lockText }}</span>
        <button type="button" class="btn-secondary btn-compact" :disabled="pinBusy" data-test="short-feed-unlock" @click="unlockLogin">解除锁定</button>
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
      <p class="help-text">只有时长小于此上限的视频会进入手机端 Feed，并自动维护“短视频”标签，默认 5 分钟。此上限只对视频生效，图片不受它限制。这一项需要点「保存所有设置」。</p>
    </div>
    <p class="help-text">手机端 Feed 混编视频与图片：视频自动播放并有进度条，图片停在原地直到你划走，双击可在适屏与原图之间切换。手机上的收藏、点赞与桌面端是同一份数据。</p>
    <p class="help-text">手机端只显示片库里看得到的内容（扫描目录内、不在黑名单、路径未失效）。设置 PIN 后，手机打开页面要先输入 PIN，连续输错会暂时锁定。</p>
  </div>
</template>

<script>
import {
  ClearShortFeedPIN, GetShortFeedAccessStatus, GetShortFeedQRCode, GetShortFeedServerStatus,
  SetShortFeedEnabled, SetShortFeedPIN, UnlockShortFeedLogin
} from '../../../wailsjs/go/main/App';
import { confirmAction } from '../../utils/feedback.js';

export const SHORT_FEED_PIN_BANNER_KEY = 'short-feed-pin-banner-dismissed';
const PIN_MIN_LENGTH = 6;
const PIN_MAX_LENGTH = 32;

// 与后端 SetShortFeedPIN 同口径：6–32 个字符，不含控制字符。前端先挡一遍，报错更直接。
export function shortFeedPINProblem(pin) {
  const length = Array.from(String(pin ?? '')).length;
  if (length < PIN_MIN_LENGTH) return `PIN 至少 ${PIN_MIN_LENGTH} 个字符`;
  if (length > PIN_MAX_LENGTH) return `PIN 最多 ${PIN_MAX_LENGTH} 个字符`;
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(String(pin))) return 'PIN 不能包含控制字符';
  return '';
}

function readBannerDismissed() {
  try {
    return window.localStorage?.getItem(SHORT_FEED_PIN_BANNER_KEY) === '1';
  } catch {
    return false;
  }
}

// 手机端浏览分区：开关、访问 PIN 与锁定状态、服务运行状态、局域网地址与二维码。
// 开关与 PIN 走各自的专用绑定、即时生效；只有时长上限是表单字段、随设置页统一保存。
export default {
  name: 'MobileSection',
  props: {
    form: { type: Object, required: true }
  },
  data() {
    return {
      shortFeedStatus: null,
      accessStatus: null,
      accessLoaded: false,
      qrCode: '',
      toggleBusy: false,
      toggleError: '',
      pinInput: '',
      pinBusy: false,
      pinMessage: '',
      pinMessageIsError: false,
      bannerDismissed: readBannerDismissed()
    };
  },
  mounted() {
    this.refreshAll();
  },
  computed: {
    shortFeedEnabled() {
      return Boolean(this.accessStatus?.enabled);
    },
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
    pinBannerVisible() {
      return this.accessLoaded && this.shortFeedEnabled && !this.accessStatus?.pin_set && !this.bannerDismissed;
    },
    pinStateText() {
      if (!this.accessLoaded) return '正在读取 PIN 状态…';
      return this.accessStatus?.pin_set
        ? '已设置 PIN：手机打开页面需要先输入它。修改或清除后，已登录的手机需要重新输入。'
        : '未设置 PIN：同一局域网里的设备可以直接打开。';
    },
    lockText() {
      const until = this.accessStatus?.locked_until ? new Date(this.accessStatus.locked_until) : null;
      const untilText = until && !Number.isNaN(until.getTime()) ? `，将于 ${until.toLocaleString('zh-CN')} 自动解除` : '';
      return `PIN 输错次数过多，手机端的新登录已被锁定${untilText}。已登录的手机不受影响。`;
    }
  },
  methods: {
    async refreshAll() {
      await Promise.all([this.loadShortFeedStatus(), this.loadAccessStatus()]);
      await this.loadQRCode();
    },
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
    async loadAccessStatus() {
      try {
        this.accessStatus = await GetShortFeedAccessStatus();
        this.accessLoaded = true;
      } catch (err) {
        this.accessStatus = null;
        this.accessLoaded = false;
        this.toggleError = `读取手机端访问状态失败：${err}`;
      }
    },
    async loadQRCode() {
      if (!this.shortFeedStatus?.running) {
        this.qrCode = '';
        return;
      }
      try {
        this.qrCode = (await GetShortFeedQRCode()) || '';
      } catch {
        // 二维码只是便利：生成失败不影响地址本身，照样可以手输。
        this.qrCode = '';
      }
    },
    async toggleShortFeed(event) {
      const input = event?.target || null;
      const enabled = Boolean(input?.checked);
      if (this.toggleBusy) return;
      this.toggleBusy = true;
      this.toggleError = '';
      try {
        await SetShortFeedEnabled(enabled);
      } catch (err) {
        // 启动失败后端已回写关闭；界面以重读到的状态为准，不停在用户点的那个值上。
        this.toggleError = `${enabled ? '开启' : '关闭'}手机端访问失败：${err}`;
      } finally {
        this.toggleBusy = false;
        await this.refreshAll();
        // 绑定值没变（失败后仍是关）时 Vue 不会回写 DOM，勾选框会停在用户点的那一下，这里手动对齐。
        if (input) input.checked = this.shortFeedEnabled;
      }
    },
    async savePIN() {
      const problem = shortFeedPINProblem(this.pinInput);
      if (problem) {
        this.pinMessage = problem;
        this.pinMessageIsError = true;
        return;
      }
      this.pinBusy = true;
      this.pinMessage = '';
      this.pinMessageIsError = false;
      const replacing = Boolean(this.accessStatus?.pin_set);
      try {
        await SetShortFeedPIN(this.pinInput);
        this.pinInput = '';
        this.pinMessage = replacing ? 'PIN 已修改，已登录的手机需要重新输入。' : 'PIN 已设置，手机打开页面时需要输入它。';
      } catch (err) {
        this.pinMessage = `设置 PIN 失败：${err}`;
        this.pinMessageIsError = true;
      } finally {
        this.pinBusy = false;
        await this.loadAccessStatus();
      }
    },
    async clearPIN() {
      const confirmed = await confirmAction({
        title: '清除访问 PIN',
        message: '清除后，同一局域网里的任何设备都能直接打开手机端浏览，已登录的手机也会退出。确认清除吗？',
        confirmText: '清除 PIN',
        danger: true
      });
      if (!confirmed) return;
      this.pinBusy = true;
      this.pinMessage = '';
      this.pinMessageIsError = false;
      try {
        await ClearShortFeedPIN();
        this.pinMessage = 'PIN 已清除。';
      } catch (err) {
        this.pinMessage = `清除 PIN 失败：${err}`;
        this.pinMessageIsError = true;
      } finally {
        this.pinBusy = false;
        await this.loadAccessStatus();
      }
    },
    async unlockLogin() {
      this.pinBusy = true;
      this.pinMessage = '';
      this.pinMessageIsError = false;
      try {
        await UnlockShortFeedLogin();
        this.pinMessage = '已解除锁定，手机可以重新输入 PIN 登录。';
      } catch (err) {
        this.pinMessage = `解除锁定失败：${err}`;
        this.pinMessageIsError = true;
      } finally {
        this.pinBusy = false;
        await this.loadAccessStatus();
      }
    },
    dismissPinBanner() {
      this.bannerDismissed = true;
      try {
        window.localStorage?.setItem(SHORT_FEED_PIN_BANNER_KEY, '1');
      } catch {
        // 本机存不下就只在这次打开页面期间收起。
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

.short-feed-url--primary {
  color: var(--text-primary);
  font-weight: 650;
}

.short-feed-hint {
  color: var(--warning-text);
}

.short-feed-pin-banner,
.short-feed-lock {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 14px;
  padding: 8px 12px;
  border: 1px solid var(--warning-border);
  border-radius: var(--radius);
  background: var(--warning-soft);
  color: var(--warning-text);
  font-size: 12.5px;
}

.short-feed-lock { margin: 10px 0 0; }

.short-feed-qr {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 10px;
  color: var(--text-secondary);
  font-size: 12.5px;
}

.short-feed-qr img {
  width: 132px;
  height: 132px;
  image-rendering: pixelated;
  border-radius: 6px;
  background: #fff;
}

.short-feed-pin { margin-top: 18px; }
.short-feed-pin-row { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.short-feed-pin-row .text-input { flex: 1; min-width: 160px; }

</style>
