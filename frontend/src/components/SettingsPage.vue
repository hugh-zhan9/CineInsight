<template>
  <div class="page-content settings-page">
    <!-- 原型 A10：分区改成左侧锚点导航 + 右侧连续长表单。
         这是全应用唯一出现侧栏的地方。 -->
    <div class="settings-shell">
      <nav class="settings-nav" aria-label="设置分区">
        <button
          v-for="section in navSections"
          :key="section.key"
          type="button"
          :class="['settings-nav__item', { active: activeSection === section.key }]"
          @click="scrollToSection(section.key)"
        >{{ section.label }}</button>
      </nav>

      <div class="settings-body" ref="settingsBody">
    <h2>设置</h2>

    <!-- 分区顺序必须与 SETTINGS_SECTIONS 一致（D-PC58：扫描目录排第 2 位）。 -->
    <div class="settings-grid-shell">
    <BasicSection :form="settingsForm" />

    <ScanDirectoriesSection
      :form="settingsForm"
      :directories="directories"
      :watcher-status="watcherStatus"
      :reload-watcher-status="loadLibraryWatcherStatus"
      @directories-changed="$emit('directories-changed', $event)"
    />

    <AutomationSection :form="settingsForm" @error="reportSettingsError" />

    <IINASyncSection />

    <IdleSchedulingSection :form="settingsForm" />

    <ProxySection :form="settingsForm" />

    <EnhanceSection />

    <FaceSection :form="settingsForm" :ensure-saved="ensureSavedFor" />

    <MobileSection :form="settingsForm" />

    <JellyfinSection />

    <BrowserBridgeSection :form="settingsForm" @error="reportSettingsError" />

    <AITagSection :form="settingsForm" :ensure-saved="ensureSavedFor" />

    <OnlineSourceSection :form="settingsForm" />

    <RandomAndFormatsSection :form="settingsForm" />

    <SubtitleSection :form="settingsForm" />

    <DatabaseSection
      :form="settingsForm"
      :ensure-saved="ensureSavedFor"
      @relaunch-required="$emit('relaunch-required', $event)"
    />
    </div>

    <div class="settings-actions">
      <span v-if="dirty && !settingsSaving" class="settings-dirty-hint" data-test="settings-dirty-hint" role="status">有未保存的修改</span>
      <div
        v-if="saveMessage"
        class="settings-save-status"
        :class="`settings-save-status--${saveState}`"
        :role="saveState === 'error' ? 'alert' : 'status'"
      >
        {{ saveMessage }}
      </div>
      <button @click="saveSettings" class="btn-primary settings-save-button" :disabled="settingsSaving">
        {{ settingsSaving ? '正在保存...' : '保存所有设置' }}
      </button>
    </div>
      </div>
    </div>
  </div>
</template>

<script>
import { UpdateSettings, TriggerAITagging, GetLibraryWatcherStatus } from '../../wailsjs/go/main/App';
import { normalizeIdleThresholdMinutes, normalizeIdleWindowBound } from '../utils/idleScheduling.js';
import { normalizeProxyCacheLimitBytes } from '../utils/playbackProxy.js';
import { registerCommands, unregisterCommands } from '../utils/commandRegistry.js';
import { confirmAction } from '../utils/feedback.js';

// 左侧锚点导航的分区清单，顺序与模板里的分区顺序一致，是锚点的唯一来源。
// 命令面板的「设置 · 各分区」动作也读这份清单（D-029），因此导出而不是本地常量。
//
// saveMode（D-PC57）决定分区标题旁的标记：
//   save    表单字段，要点「保存所有设置」才生效；
//   instant 分区里的操作点了就生效（或只是状态展示），与保存按钮无关；
//   mixed   两种都有：开关类即时生效，其余字段仍要保存。
export const SETTINGS_SECTIONS = [
  { key: 'basic', label: '基本设置', saveMode: 'save' },
  { key: 'scan-dirs', label: '扫描目录管理', saveMode: 'instant' },
  { key: 'image-dirs', label: '图片扫描目录', saveMode: 'instant' },
  { key: 'automation', label: '自动化与扫描', saveMode: 'save' },
  { key: 'iina-sync', label: 'IINA 进度同步', saveMode: 'instant' },
  { key: 'idle-scheduling', label: '后台任务调度', saveMode: 'save' },
  { key: 'playback-proxy', label: '播放兼容缓存', saveMode: 'save' },
  { key: 'enhance', label: '视频超分', saveMode: 'instant' },
  { key: 'face', label: '人脸识别', saveMode: 'save' },
  { key: 'mobile', label: '手机端浏览', saveMode: 'mixed' },
  { key: 'jellyfin', label: 'Jellyfin 客户端连接', saveMode: 'instant' },
  { key: 'browser-bridge', label: '浏览器插件', saveMode: 'save' },
  { key: 'ai-tags', label: 'AI 标签', saveMode: 'save' },
  { key: 'online-sources', label: '在线资料源', saveMode: 'save' },
  { key: 'random', label: '智能随机播放', saveMode: 'save' },
  { key: 'video-formats', label: '支持的视频格式', saveMode: 'save' },
  { key: 'image-formats', label: '支持的图片格式', saveMode: 'save' },
  { key: 'subtitle-translate', label: '字幕翻译', saveMode: 'save' },
  { key: 'subtitle-quality', label: '字幕识别质量', saveMode: 'save' },
  { key: 'database', label: '数据库', saveMode: 'instant' },
  { key: 'backup', label: '数据库备份', saveMode: 'save' }
];

export const SAVE_MODE_LABELS = {
  save: '需保存',
  instant: '即时生效',
  mixed: '部分需保存'
};

const DEFAULT_VIDEO_EXTENSIONS = '.mp4,.avi,.mkv,.mov,.wmv,.flv,.webm,.m4v,.ts,.3gp,.mpg,.mpeg,.rm,.rmvb,.vob,.divx,.f4v,.asf,.qt';

// 清理中心阈值的默认值（D-PC36），与 database.DefaultCleanup* 一致；后端对 <=0 同样取默认。
const DEFAULT_CLEANUP_SHORT_SECONDS = 5;
const DEFAULT_CLEANUP_LOW_WIDTH = 480;
const DEFAULT_CLEANUP_LOW_HEIGHT = 320;

function positiveIntOr(value, fallback) {
  const number = Math.trunc(Number(value));
  return Number.isFinite(number) && number > 0 ? number : fallback;
}

// 设置读回来之后先落成「界面上显示的就是会生效的值」。
export function normalizeSettingsForm(settings) {
  const form = { ...(settings || {}) };
  if (!form.video_extensions || String(form.video_extensions).trim() === '') {
    form.video_extensions = DEFAULT_VIDEO_EXTENSIONS;
  }
  form.ai_tagging_images_per_request = form.ai_tagging_images_per_request || 10;
  form.ai_tagging_subtitle_char_limit = form.ai_tagging_subtitle_char_limit || 4000;
  form.ai_tagging_startup_batch_size = form.ai_tagging_startup_batch_size || 10;
  form.ai_tagging_max_extra_frames = form.ai_tagging_max_extra_frames || 20;
  form.semantic_embedding_model = form.semantic_embedding_model || '';
  form.short_feed_max_duration_minutes = form.short_feed_max_duration_minutes || 5;
  // 「反馈回流」开关已从界面删除（P-021 结论：它只控制已删掉的收藏 / 点赞投影）。
  // 通用保存仍会无条件写这一列，所以原值照样带回去，不借保存把它悄悄改掉。
  if (form.short_feed_feedback_sync_enabled == null) form.short_feed_feedback_sync_enabled = true;
  form.scan_exclude_paths = form.scan_exclude_paths || '';
  form.image_scan_exclude_paths = form.image_scan_exclude_paths || '';
  form.image_extensions = form.image_extensions || '';
  form.subtitle_translation_provider = form.subtitle_translation_provider || 'deepl';
  form.subtitle_whisperx_model = form.subtitle_whisperx_model || 'medium';
  form.subtitle_whisperx_batch_size = form.subtitle_whisperx_batch_size || 8;
  form.backup_directory = form.backup_directory || '';
  form.backup_retention_count = form.backup_retention_count || 7;
  form.backup_interval_hours = Number.isFinite(form.backup_interval_hours) ? form.backup_interval_hours : 24;
  form.random_half_life_days = Number.isFinite(form.random_half_life_days) ? form.random_half_life_days : 90;
  // 空闲调度：老库/异常值也要落到合法区间，界面上显示的就是会生效的值。
  if (form.idle_scheduling_enabled == null) form.idle_scheduling_enabled = true;
  form.idle_threshold_minutes = normalizeIdleThresholdMinutes(form.idle_threshold_minutes);
  form.idle_require_ac_power = Boolean(form.idle_require_ac_power);
  form.idle_window_start = normalizeIdleWindowBound(form.idle_window_start);
  form.idle_window_end = normalizeIdleWindowBound(form.idle_window_end);
  // 桌面通知默认开（D-013）：老库读回 undefined 时界面也要显示成开着。
  if (form.desktop_notifications_enabled == null) form.desktop_notifications_enabled = true;
  // 播放代理：0 是「不限」这个真实取值，只有读不出数值时才回落到默认 50 GiB。
  form.auto_compatibility_proxy = Boolean(form.auto_compatibility_proxy);
  form.proxy_cache_limit_bytes = normalizeProxyCacheLimitBytes(form.proxy_cache_limit_bytes);
  // 人脸识别（D-016、D-022）：两项默认都是零值，老库读回 undefined 也要落成确定值。
  form.auto_face_analysis = Boolean(form.auto_face_analysis);
  form.face_model_mirror_url = form.face_model_mirror_url || '';
  // 清理阈值（D-PC36）：<=0 视为默认值，界面显示生效值。
  form.cleanup_short_seconds = positiveIntOr(form.cleanup_short_seconds, DEFAULT_CLEANUP_SHORT_SECONDS);
  form.cleanup_low_width = positiveIntOr(form.cleanup_low_width, DEFAULT_CLEANUP_LOW_WIDTH);
  form.cleanup_low_height = positiveIntOr(form.cleanup_low_height, DEFAULT_CLEANUP_LOW_HEIGHT);
  return form;
}

// UpdateSettings 的显式载荷。**这里漏了任何一个字段都会静默把它写成零值**：后端照白名单
// 无条件赋值，而界面上表单还显示着你刚设的值，看起来像保存成功了。保存路径只有这一条，
// 新增的可保存设置务必加进这个字面量；脏状态比较也只看这里列出的字段——专用接口写的列
// （手机端开关与 PIN、Jellyfin、桥接令牌）不算在内。
export function buildSettingsPayload(form) {
  return {
    confirm_before_delete: form.confirm_before_delete,
    delete_original_file: form.delete_original_file,
    video_extensions: form.video_extensions,
    image_extensions: form.image_extensions || '',
    scan_exclude_paths: form.scan_exclude_paths || '',
    image_scan_exclude_paths: form.image_scan_exclude_paths || '',
    play_weight: form.play_weight,
    random_half_life_days: Math.max(0, Number(form.random_half_life_days) || 0),
    auto_scan_on_startup: form.auto_scan_on_startup,
    playback_resume_mode: form.playback_resume_mode || 'resume',
    auto_technical_backfill: form.auto_technical_backfill,
    auto_perceptual_hash: form.auto_perceptual_hash,
    auto_cleanup_analysis: form.auto_cleanup_analysis,
    auto_image_exif_backfill: form.auto_image_exif_backfill,
    auto_image_perceptual_hash: form.auto_image_perceptual_hash || false,
    auto_collection_suggestions: form.auto_collection_suggestions || false,
    auto_frame_hash_sequence: form.auto_frame_hash_sequence || false,
    idle_scheduling_enabled: form.idle_scheduling_enabled !== false,
    idle_threshold_minutes: normalizeIdleThresholdMinutes(form.idle_threshold_minutes),
    idle_require_ac_power: form.idle_require_ac_power || false,
    idle_window_start: normalizeIdleWindowBound(form.idle_window_start),
    idle_window_end: normalizeIdleWindowBound(form.idle_window_end),
    desktop_notifications_enabled: form.desktop_notifications_enabled !== false,
    auto_compatibility_proxy: form.auto_compatibility_proxy || false,
    proxy_cache_limit_bytes: normalizeProxyCacheLimitBytes(form.proxy_cache_limit_bytes),
    auto_face_analysis: form.auto_face_analysis || false,
    face_model_mirror_url: form.face_model_mirror_url || '',
    // 清理中心阈值（D-PC36，P-001 评审 Minor 7）：后端对 <=0 取默认值。
    cleanup_short_seconds: positiveIntOr(form.cleanup_short_seconds, DEFAULT_CLEANUP_SHORT_SECONDS),
    cleanup_low_width: positiveIntOr(form.cleanup_low_width, DEFAULT_CLEANUP_LOW_WIDTH),
    cleanup_low_height: positiveIntOr(form.cleanup_low_height, DEFAULT_CLEANUP_LOW_HEIGHT),
    library_watch_enabled: form.library_watch_enabled || false,
    local_metadata_enabled: form.local_metadata_enabled || false,
    ai_quality_enabled: form.ai_quality_enabled || false,
    // 浏览器插件桥接三项。令牌有意不在这里传，它只由「生成令牌」按钮写，混进来的话
    // 前端某次漏带就会把它抹空、已配对的插件静默断开。
    browser_bridge_enabled: form.browser_bridge_enabled || false,
    browser_download_directory: form.browser_download_directory || '',
    browser_download_concurrency: form.browser_download_concurrency || 2,
    short_feed_max_duration_minutes: form.short_feed_max_duration_minutes || 5,
    short_feed_feedback_sync_enabled: form.short_feed_feedback_sync_enabled !== false,
    theme: form.theme,
    log_enabled: form.log_enabled,
    bilingual_enabled: form.bilingual_enabled || false,
    bilingual_lang: form.bilingual_lang || 'zh',
    deepl_api_key: form.deepl_api_key || '',
    subtitle_translation_provider: form.subtitle_translation_provider || 'deepl',
    subtitle_translation_base_url: form.subtitle_translation_base_url || '',
    subtitle_translation_api_key: form.subtitle_translation_api_key || '',
    subtitle_translation_model: form.subtitle_translation_model || '',
    subtitle_whisperx_model: form.subtitle_whisperx_model || 'medium',
    subtitle_whisperx_batch_size: form.subtitle_whisperx_batch_size || 8,
    ai_tagging_base_url: form.ai_tagging_base_url || '',
    ai_tagging_api_key: form.ai_tagging_api_key || '',
    ai_tagging_model: form.ai_tagging_model || '',
    semantic_embedding_model: form.semantic_embedding_model || '',
    ai_tagging_frame_count: 0,
    ai_tagging_images_per_request: form.ai_tagging_images_per_request || 10,
    ai_tagging_subtitle_char_limit: form.ai_tagging_subtitle_char_limit || 4000,
    ai_tagging_startup_batch_size: form.ai_tagging_startup_batch_size || 10,
    ai_tagging_max_extra_frames: form.ai_tagging_max_extra_frames || 20,
    // 在线资料源的出网代理与三家凭证：在别的分区改一项再保存，漏掉就会把配好的代理和凭证抹掉。
    metadata_proxy_url: form.metadata_proxy_url || '',
    tmdb_api_key: form.tmdb_api_key || '',
    bangumi_access_token: form.bangumi_access_token || '',
    backup_directory: form.backup_directory || '',
    backup_retention_count: form.backup_retention_count || 7,
    backup_interval_hours: Math.max(0, Number(form.backup_interval_hours) || 0)
  };
}

// 保存之后要不要叫醒 AI 打标（D-PC19 / APP-04）：只看 AI 打标自己的字段。
// 改主题、改扫描目录这类保存与 AI 无关，不该每次都触发一轮打标。
const AI_TAGGING_FIELD_PREFIX = 'ai_tagging_';

function sameValue(a, b) {
  return JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
}

// 两份表单在显式载荷上的差异字段（载荷字段名与表单字段名相同）。
export function changedSettingsFields(form, snapshot) {
  const current = buildSettingsPayload(form || {});
  const saved = buildSettingsPayload(snapshot || {});
  return Object.keys(current).filter(key => !sameValue(current[key], saved[key]));
}

import AITagSection from './settings/AITagSection.vue';
import AutomationSection from './settings/AutomationSection.vue';
import BasicSection from './settings/BasicSection.vue';
import DatabaseSection from './settings/DatabaseSection.vue';
import EnhanceSection from './settings/EnhanceSection.vue';
import FaceSection from './settings/FaceSection.vue';
import IdleSchedulingSection from './settings/IdleSchedulingSection.vue';
import IINASyncSection from './settings/IINASyncSection.vue';
import BrowserBridgeSection from './settings/BrowserBridgeSection.vue';
import JellyfinSection from './settings/JellyfinSection.vue';
import MobileSection from './settings/MobileSection.vue';
import OnlineSourceSection from './settings/OnlineSourceSection.vue';
import ProxySection from './settings/ProxySection.vue';
import RandomAndFormatsSection from './settings/RandomAndFormatsSection.vue';
import ScanDirectoriesSection from './settings/ScanDirectoriesSection.vue';
import SubtitleSection from './settings/SubtitleSection.vue';

export default {
  name: 'SettingsPage',
  components: { AITagSection, AutomationSection, BasicSection, BrowserBridgeSection, DatabaseSection, EnhanceSection, FaceSection, IdleSchedulingSection, IINASyncSection, JellyfinSection, MobileSection, OnlineSourceSection, ProxySection, RandomAndFormatsSection, ScanDirectoriesSection, SubtitleSection },
  props: {
    settings: { type: Object, required: true },
    directories: { type: Array, default: () => [] }
  },
  // update:dirty（布尔）由 App.vue 监听：有未保存修改时切页前先确认（D-PC57 / APP-08）。
  // relaunch-required({ message })：数据库进入「待重启」终态，App 层据此挂全局「立即重启」遮罩
  // （主代理裁决 2026-09-30；设置页自己不做全局遮罩）。
  emits: ['settings-saved', 'directories-changed', 'update:dirty', 'relaunch-required'],
  data() {
    const form = normalizeSettingsForm(this.settings);
    return {
      settingsForm: form,
      // 最近一次读回或保存成功时的表单快照：脏状态以它为基准。
      savedSnapshot: { ...form },
      navSections: SETTINGS_SECTIONS,
      activeSection: SETTINGS_SECTIONS[0].key,
      sectionObserver: null,
      watcherStatus: null,
      watcherStatusOff: null,
      settingsSaving: false,
      saveState: 'idle',
      saveMessage: ''
    };
  },
  computed: {
    dirtyFields() {
      return changedSettingsFields(this.settingsForm, this.savedSnapshot);
    },
    dirty() {
      return this.dirtyFields.length > 0;
    }
  },
  watch: {
    // 父组件换了一份设置（保存后回灌、别的页面改了某一项）时，只接住用户没改过的字段：
    // 用户正在改的字段保留，不被新快照整份盖掉（APP-08）。
    settings: {
      handler(val) {
        const next = normalizeSettingsForm(val);
        const merged = { ...next };
        for (const key of this.dirtyFields) merged[key] = this.settingsForm[key];
        this.settingsForm = merged;
        this.savedSnapshot = { ...next };
      },
      deep: true
    },
    dirty: {
      handler(value) {
        this.$emit('update:dirty', value);
      },
      immediate: true
    }
  },
  mounted() {
    this.loadLibraryWatcherStatus();
    this.applySectionSaveModes();
    this.observeSections();
    // 命令面板的本页动作（D-029）：保存设置。分区锚点是全局动作，由 App 注册。
    registerCommands('settings-page', [{
      id: 'action:settings-save',
      group: 'action',
      label: '保存所有设置',
      keywords: ['save settings', '保存设置'],
      enabled: () => !this.settingsSaving,
      run: () => this.saveSettings()
    }]);
    if (window.runtime?.EventsOn) {
      const off = window.runtime.EventsOn('library-watcher-status', (status) => {
        this.watcherStatus = status || null;
      });
      if (typeof off === 'function') this.watcherStatusOff = off;
    }
  },
  beforeUnmount() {
    unregisterCommands('settings-page');
    this.watcherStatusOff?.();
    this.sectionObserver?.disconnect();
  },
  methods: {
    // 分区标题旁的「即时生效 / 需保存」标记（D-PC57）。分区组件各自渲染标题，这里只按
    // SETTINGS_SECTIONS 给分区根节点打上 data-save-mode，标记由全局样式画在标题后面——
    // 清单是唯一来源，新增分区不用在每个组件里再写一遍。
    applySectionSaveModes() {
      for (const section of this.navSections) {
        const element = this.sectionElement(section.key);
        if (!element || !section.saveMode) continue;
        element.dataset.saveMode = section.saveMode;
        element.dataset.saveModeLabel = SAVE_MODE_LABELS[section.saveMode] || '';
      }
    },
    // 滚动到哪个分区就高亮哪一项。用 IntersectionObserver 而不是监听滚动：
    // 真正的滚动宿主是外层的 .main-view，这里拿不到它。
    observeSections() {
      if (typeof IntersectionObserver !== 'function') return;
      this.$nextTick(() => {
        this.sectionObserver?.disconnect();
        this.sectionObserver = new IntersectionObserver(entries => {
          const visible = entries
            .filter(entry => entry.isIntersecting)
            .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)[0];
          if (visible) this.activeSection = visible.target.id.replace('settings-', '');
        }, { rootMargin: '-52px 0px -60% 0px', threshold: 0 });
        for (const section of this.navSections) {
          const element = this.sectionElement(section.key);
          if (element) this.sectionObserver.observe(element);
        }
      });
    },
    // 在组件自己的子树里找，不走 document：设置页是 v-if 挂载的，
    // 组件树之外的同名 id 不该被误命中。
    sectionElement(key) {
      return this.$el?.querySelector(`#settings-${key}`) || null;
    },
    scrollToSection(key) {
      const element = this.sectionElement(key);
      if (!element) return;
      this.activeSection = key;
      element.scrollIntoView({ behavior: 'smooth', block: 'start' });
    },
    // 子分区里做不了的操作（选目录之类）失败时，统一报到顶部的保存状态栏。
    reportSettingsError(message) {
      this.saveState = 'error';
      this.saveMessage = message;
    },
    // 即时动作前的保存提示（D-PC57）：动作要用的字段改了还没保存时，先问要不要保存。
    // 没有相关的未保存修改就直接放行（同步返回 true）；否则返回 Promise<boolean>。
    ensureSavedFor(fields, actionLabel) {
      const pending = this.dirtyFields.filter(key => (fields || []).includes(key));
      if (!pending.length) return true;
      return confirmAction({
        title: '先保存设置',
        message: `「${actionLabel}」会使用已保存的设置，而相关设置刚改过还没保存。先保存全部设置再继续吗？`,
        confirmText: '保存并继续'
      }).then(confirmed => (confirmed ? this.saveSettings() : false));
    },
    // 返回是否保存成功，供「保存并继续」判断要不要接着执行。
    async saveSettings() {
      if (this.settingsSaving) return false;
      this.settingsSaving = true;
      this.saveState = 'saving';
      this.saveMessage = '正在保存设置...';
      const payload = buildSettingsPayload(this.settingsForm);
      const previous = buildSettingsPayload(this.savedSnapshot);
      const aiChanged = Object.keys(payload)
        .some(key => key.startsWith(AI_TAGGING_FIELD_PREFIX) && !sameValue(payload[key], previous[key]));
      const savedForm = { ...this.settingsForm };
      try {
        await UpdateSettings(payload);
        this.savedSnapshot = savedForm;
        const aiTriggered = aiChanged ? await TriggerAITagging() : false;
        await this.loadLibraryWatcherStatus();
        this.$emit('settings-saved', { ...savedForm });
        this.saveState = 'success';
        if (!aiChanged) {
          this.saveMessage = '设置保存成功。';
        } else {
          const hasAIConfig = Boolean(String(savedForm.ai_tagging_base_url || '').trim() && String(savedForm.ai_tagging_model || '').trim());
          if (!hasAIConfig) {
            this.saveMessage = '设置保存成功；AI 接口或模型未配置，自动打标已暂停。';
          } else if (!aiTriggered) {
            this.saveMessage = '设置保存成功；AI 后台任务未运行，自动打标暂未启动。';
          } else {
            this.saveMessage = '设置保存成功，已触发 AI 自动打标。';
          }
        }
        return true;
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        this.saveState = 'error';
        this.saveMessage = '设置保存失败：' + message;
        console.error('保存设置失败:', err);
        return false;
      } finally {
        this.settingsSaving = false;
      }
    },
    async loadLibraryWatcherStatus() {
      try {
        this.watcherStatus = await GetLibraryWatcherStatus();
      } catch (_err) {
        this.watcherStatus = null;
      }
    }
  }
};
</script>

<style scoped>
.settings-page h2 {
  margin: 0 0 14px;
  font-size: 22px;
  font-weight: 760;
}

.settings-shell {
  display: grid;
  grid-template-columns: 230px minmax(0, 1fr);
  gap: 0;
  align-items: start;
}

.settings-nav {
  position: sticky;
  top: 0;
  display: flex;
  flex-direction: column;
  gap: 1px;
  padding: 10px 8px;
  border-right: 1px solid var(--hairline-soft);
  background: var(--panel-subtle-bg);
}

.settings-nav__item {
  padding: 7px 12px;
  border: 0;
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-secondary);
  font-size: 12.5px;
  text-align: left;
  cursor: pointer;
}

.settings-nav__item:hover { background: var(--control-hover-bg); color: var(--text-primary); }
.settings-nav__item.active { background: var(--accent-soft); color: var(--accent-text); font-weight: 650; }

.settings-body { min-width: 0; padding: 18px 0 0 22px; }

.settings-grid-shell {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 16px;
  align-items: start;
}

.settings-actions {
  position: sticky;
  bottom: 0;
  z-index: 20;
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 12px;
  margin-top: 16px;
  padding: 12px 0 6px;
  background: linear-gradient(180deg, transparent, var(--bg-color) 32%);
}

.settings-dirty-hint {
  color: var(--warning-text);
  font-size: 12.5px;
  white-space: nowrap;
}

.settings-save-button {
  flex-shrink: 0;
  padding: 0 32px;
}

.settings-save-status {
  min-width: 0;
  padding: 8px 10px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--control-bg);
  color: var(--text-secondary);
  font-size: 13px;
  text-align: left;
}

.settings-save-status--success {
  border-color: var(--accent-border);
  color: var(--accent-color);
}

.settings-save-status--error {
  border-color: var(--danger-border);
  color: var(--danger-color);
}

@media (max-width: 980px) {
  /* 窄屏把锚点导航收起来：230px 侧栏加长表单在这个宽度下两边都放不下。 */
  .settings-shell {
    grid-template-columns: 1fr;
  }

  .settings-nav {
    position: static;
    flex-direction: row;
    flex-wrap: wrap;
    border-right: 0;
    border-bottom: 1px solid var(--hairline-soft);
  }

  .settings-body {
    padding: 14px 0 0;
  }
}

@media (max-width: 600px) {
  .settings-actions {
    align-items: stretch;
    flex-direction: column;
  }

  .settings-save-button {
    width: 100%;
  }
}
</style>
