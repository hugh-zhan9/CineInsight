<template>
  <div class="page-content settings-page">
    <PageHeader title="设置" subtitle="标着「需保存」的分区改完点底部「保存所有设置」；「即时生效」的分区点了就生效。" />
    <button type="button" class="btn btn--secondary" data-test="settings-capability-setup" @click="$emit('open-setup')">按目标准备</button>
    <div class="settings-shell">
      <nav class="settings-nav" aria-label="设置类别">
        <button
          v-for="category in categories"
          :key="category.key"
          type="button"
          :class="['btn', 'btn--ghost', 'settings-nav__item', { active: activeCategory === category.key }]"
          :aria-current="activeCategory === category.key ? 'true' : undefined"
          @click="selectCategory(category.key)"
        >{{ category.label }}</button>
      </nav>

      <div class="settings-body" ref="settingsBody">
        <h2 class="settings-category-heading">{{ currentCategory.label }}</h2>
        <nav v-if="currentSections.length > 1" class="settings-section-nav" aria-label="当前类别分区">
          <button
            v-for="section in currentSections"
            :key="section.key"
            type="button"
            class="btn btn--ghost settings-section-nav__item"
            @click="scrollToSection(section.key)"
          >{{ section.label }}</button>
        </nav>

        <!-- First visit mounts a category. Subsequent switches preserve child drafts. -->
        <div v-if="visited.general" v-show="activeCategory === 'general'" class="settings-grid-shell" data-test="settings-category-general">
          <BasicSection :form="settingsForm" />
        </div>

        <div v-if="visited.library" v-show="activeCategory === 'library'" class="settings-grid-shell" data-test="settings-category-library">
          <ScanDirectoriesSection
            :form="settingsForm"
            :directories="directories"
            :watcher-status="watcherStatus"
            :reload-watcher-status="loadLibraryWatcherStatus"
            @directories-changed="$emit('directories-changed', $event)"
          />
          <RandomAndFormatsSection :form="settingsForm" part="formats" />
          <AutomationSection part="library" :form="settingsForm" @error="reportSettingsError" />
        </div>

        <div v-if="visited.playback" v-show="activeCategory === 'playback'" class="settings-grid-shell" data-test="settings-category-playback">
          <AutomationSection part="playback" :form="settingsForm" />
          <IINASyncSection />
          <ProxySection :form="settingsForm" />
          <EnhanceSection />
          <RandomAndFormatsSection :form="settingsForm" part="random" />
        </div>

        <div v-if="visited.tasks" v-show="activeCategory === 'tasks'" class="settings-grid-shell" data-test="settings-category-tasks">
          <AutomationSection :form="settingsForm" @error="reportSettingsError" />
          <IdleSchedulingSection :form="settingsForm" />
          <AutomaticStructureSection :form="settingsForm" />
        </div>

        <div v-if="visited.ai" v-show="activeCategory === 'ai'" class="settings-grid-shell" data-test="settings-category-ai">
          <AITagSection :form="settingsForm" :ensure-saved="ensureSavedFor" />
          <FaceSection :form="settingsForm" :ensure-saved="ensureSavedFor" />
          <SceneSection :form="settingsForm" :ensure-saved="ensureSavedFor" />
          <SubtitleSection :form="settingsForm" />
          <AutomationSection part="ai" :form="settingsForm" />
        </div>

        <div v-if="visited.connections" v-show="activeCategory === 'connections'" class="settings-grid-shell" data-test="settings-category-connections">
          <MobileSection :form="settingsForm" />
          <JellyfinSection @update:dirty="jellyfinDirty = $event" />
          <BTConnectionSection @update:dirty="btDirty = $event" @scan-settings="scrollToSection('scan-dirs')" />
          <BrowserBridgeSection :form="settingsForm" @error="reportSettingsError" />
          <OnlineSourceSection :form="settingsForm" />
          <PhotoMapSection />
        </div>

        <div v-if="visited.data" v-show="activeCategory === 'data'" class="settings-grid-shell" data-test="settings-category-data">
          <DatabaseSection
            :form="settingsForm"
            :ensure-saved="ensureSavedFor"
            @relaunch-required="$emit('relaunch-required', $event)"
          />
        </div>

        <div class="settings-actions">
          <span v-if="dirtyFields.length && !settingsSaving" class="settings-dirty-hint" data-test="settings-dirty-hint" role="status">有未保存的修改</span>
          <span v-if="connectionDirty" class="settings-dirty-hint" role="status">连接配置需在分区内独立保存</span>
          <div
            v-if="saveMessage"
            class="settings-save-status"
            :class="`settings-save-status--${saveState}`"
            :role="saveState === 'error' ? 'alert' : 'status'"
          >
            {{ saveMessage }}
          </div>
          <button @click="saveSettings" class="btn btn--primary settings-save-button" :disabled="settingsSaving">
            {{ settingsSaving ? '正在保存…' : '保存所有设置' }}
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
import { normalizeDiscoverVideoCount } from '../utils/discoverSettings.js';

import { SETTINGS_CATEGORIES, SETTINGS_SECTIONS, settingsSection } from '../utils/settingsSections.js';

export { SETTINGS_SECTIONS };

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
// 场景检索采样间隔（D-MW-SCENES）：2–30 秒，读不出数值或 <=0 时取默认 5，与后端归一化一致。
function normalizeSceneIntervalSeconds(value) {
  const seconds = Math.round(Number(value));
  if (!Number.isFinite(seconds) || seconds <= 0) return 5;
  return Math.min(30, Math.max(2, seconds));
}

export function normalizeSettingsForm(settings) {
  const form = { ...(settings || {}) };
  form.log_enabled = form.log_enabled !== false;
  form.perf_log_enabled = form.perf_log_enabled !== false;
  form.discover_video_count = normalizeDiscoverVideoCount(form.discover_video_count);
  form.desktop_player = form.desktop_player || 'system';
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
  form.backup_directory = form.backup_directory || '';
  form.backup_retention_count = form.backup_retention_count || 7;
  form.backup_interval_hours = Number.isFinite(form.backup_interval_hours) ? form.backup_interval_hours : 24;
  form.random_half_life_days = Number.isFinite(form.random_half_life_days) ? form.random_half_life_days : 90;
  // 空闲调度：老库/异常值也要落到合法区间，界面上显示的就是会生效的值。
  if (form.auto_structure_enabled == null) form.auto_structure_enabled = true;
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
  // 场景检索（D-MW-SCENES）：提供方只认 external，其余按本地；间隔 2–30 秒，读不出时取默认 5。
  form.scene_visual_provider = form.scene_visual_provider === 'external' ? 'external' : 'local';
  form.scene_visual_interval_seconds = normalizeSceneIntervalSeconds(form.scene_visual_interval_seconds);
  form.scene_model_mirror_url = form.scene_model_mirror_url || '';
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
    desktop_player: form.desktop_player || 'system',
    auto_technical_backfill: form.auto_technical_backfill,
    auto_structure_enabled: form.auto_structure_enabled !== false,
    auto_perceptual_hash: form.auto_perceptual_hash,
    auto_cleanup_analysis: form.auto_cleanup_analysis,
    auto_image_exif_backfill: form.auto_image_exif_backfill,
    auto_image_perceptual_hash: form.auto_image_perceptual_hash || false,
    auto_collection_suggestions: form.auto_collection_suggestions || false,
    auto_crop_detection: form.auto_crop_detection || false,
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
    scene_visual_provider: form.scene_visual_provider === 'external' ? 'external' : 'local',
    scene_visual_interval_seconds: normalizeSceneIntervalSeconds(form.scene_visual_interval_seconds),
    scene_model_mirror_url: form.scene_model_mirror_url || '',
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
    log_enabled: form.log_enabled !== false,
    perf_log_enabled: form.perf_log_enabled !== false,
    discover_video_count: normalizeDiscoverVideoCount(form.discover_video_count),
    bilingual_enabled: form.bilingual_enabled || false,
    bilingual_lang: form.bilingual_lang || 'zh',
    query_parse_enabled: Boolean(form.query_parse_enabled),
    chapter_summary_enabled: Boolean(form.chapter_summary_enabled),
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

import PageHeader from './ui/PageHeader.vue';
import AITagSection from './settings/AITagSection.vue';
import AutomationSection from './settings/AutomationSection.vue';
import BasicSection from './settings/BasicSection.vue';
import DatabaseSection from './settings/DatabaseSection.vue';
import EnhanceSection from './settings/EnhanceSection.vue';
import FaceSection from './settings/FaceSection.vue';
import PhotoMapSection from './settings/PhotoMapSection.vue';
import SceneSection from './settings/SceneSection.vue';
import AutomaticStructureSection from './settings/AutomaticStructureSection.vue';
import IdleSchedulingSection from './settings/IdleSchedulingSection.vue';
import IINASyncSection from './settings/IINASyncSection.vue';
import BTConnectionSection from './settings/BTConnectionSection.vue';
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
  components: { AutomaticStructureSection, PhotoMapSection, BTConnectionSection, PageHeader, AITagSection, AutomationSection, BasicSection, BrowserBridgeSection, DatabaseSection, EnhanceSection, FaceSection, IdleSchedulingSection, SceneSection, IINASyncSection, JellyfinSection, MobileSection, OnlineSourceSection, ProxySection, RandomAndFormatsSection, ScanDirectoriesSection, SubtitleSection },
  props: {
    settings: { type: Object, required: true },
    directories: { type: Array, default: () => [] },
    navigationRequest: { type: Object, default: null }
  },
  // update:dirty（布尔）由 App.vue 监听：有未保存修改时切页前先确认（D-PC57 / APP-08）。
  // relaunch-required({ message })：数据库进入「待重启」终态，App 层据此挂全局「立即重启」遮罩
  // （主代理裁决 2026-09-30；设置页自己不做全局遮罩）。
  emits: ['settings-saved', 'directories-changed', 'update:dirty', 'relaunch-required', 'open-setup'],
  data() {
    const form = normalizeSettingsForm(this.settings);
    const requested = settingsSection(this.navigationRequest?.section);
    const category = requested?.category || SETTINGS_CATEGORIES[0].key;
    return {
      settingsForm: form, btDirty: false, jellyfinDirty: false,
      // 最近一次读回或保存成功时的表单快照：脏状态以它为基准。
      savedSnapshot: { ...form },
      categories: SETTINGS_CATEGORIES,
      activeCategory: category,
      visited: { [category]: true },
      categoryRequest: 0,
      watcherStatus: null,
      watcherStatusOff: null,
      settingsSaving: false,
      saveState: 'idle',
      saveMessage: ''
    };
  },
  computed: {
    currentCategory() {
      return this.categories.find(category => category.key === this.activeCategory);
    },
    currentSections() {
      return SETTINGS_SECTIONS.filter(section => section.category === this.activeCategory);
    },
    dirtyFields() {
      return changedSettingsFields(this.settingsForm, this.savedSnapshot);
    },
    connectionDirty() {
      return this.btDirty || this.jellyfinDirty;
    },
    dirty() {
      return this.dirtyFields.length > 0 || this.connectionDirty;
    }
  },
  watch: {
    navigationRequest(request) {
      if (request?.section) this.scrollToSection(request.section);
    },
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
    this.loadCategoryStatus(this.activeCategory);
    this.applySectionSaveModes();
    if (this.navigationRequest?.section) this.scrollToSection(this.navigationRequest.section);
    // 命令面板的本页动作（D-029）：保存设置。分区锚点是全局动作，由 App 注册。
    registerCommands('settings-page', [{
      id: 'action:settings-save',
      group: 'action',
      label: '保存所有设置',
      keywords: ['save settings', '保存设置'],
      enabled: () => !this.settingsSaving,
      run: () => this.saveSettings()
    }]);
  },
  beforeUnmount() {
    this.categoryRequest++;
    unregisterCommands('settings-page');
    this.watcherStatusOff?.();
  },
  methods: {
    // 分区标题旁的「即时生效 / 需保存」标记（D-PC57）。分区组件各自渲染标题，这里只按
    // SETTINGS_SECTIONS 给分区根节点打上 data-save-mode，标记由全局样式画在标题后面——
    // 清单是唯一来源，新增分区不用在每个组件里再写一遍。
    applySectionSaveModes() {
      for (const section of this.currentSections) {
        const element = this.sectionElement(section.key);
        if (!element || !section.saveMode) continue;
        element.dataset.saveMode = section.saveMode;
        element.dataset.saveModeLabel = SAVE_MODE_LABELS[section.saveMode] || '';
      }
    },
    loadCategoryStatus(key) {
      if (key !== 'library') return;
      this.loadLibraryWatcherStatus();
      const off = window.runtime?.EventsOn?.('library-watcher-status', (status) => {
        this.watcherStatus = status || null;
      });
      if (typeof off === 'function') this.watcherStatusOff = off;
    },
    async selectCategory(key, sectionKey = null) {
      if (!this.categories.some(category => category.key === key)) return;
      const request = ++this.categoryRequest;
      const switched = this.activeCategory !== key;
      this.activeCategory = key;
      if (!this.visited[key]) {
        this.visited[key] = true;
        this.loadCategoryStatus(key);
      }
      await this.$nextTick();
      if (request !== this.categoryRequest) return;
      this.applySectionSaveModes();
      if (sectionKey) {
        this.sectionElement(sectionKey)?.scrollIntoView?.({ block: 'start' });
      } else if (switched) {
        const host = this.$refs.settingsBody?.closest('.main-view');
        if (host) host.scrollTop = 0;
      }
    },
    sectionElement(key) {
      return this.$el?.querySelector(`#settings-${key}`) || null;
    },
    scrollToSection(key) {
      const section = settingsSection(key);
      if (section) return this.selectCategory(section.category, section.key);
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
      this.saveMessage = '正在保存设置…';
      const payload = buildSettingsPayload(this.settingsForm);
      this.settingsForm.discover_video_count = payload.discover_video_count;
      const previous = buildSettingsPayload(this.savedSnapshot);
      const aiChanged = Object.keys(payload)
        .some(key => key.startsWith(AI_TAGGING_FIELD_PREFIX) && !sameValue(payload[key], previous[key]));
      const savedForm = { ...this.settingsForm };
      try {
        await UpdateSettings(payload);
        this.savedSnapshot = savedForm;
        const aiTriggered = aiChanged ? await TriggerAITagging() : false;
        if (this.visited.library) await this.loadLibraryWatcherStatus();
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
.settings-shell {
  display: grid;
  grid-template-columns: 190px minmax(0, 1fr);
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

/* 导航项是幽灵按钮（.btn .btn--ghost），这里只改成左对齐的列表项：高度随内容、常规字重。 */
.settings-nav__item {
  justify-content: flex-start;
  height: auto;
  min-height: var(--h-compact);
  padding: 6px 12px;
  font-size: var(--fs-secondary);
  font-weight: var(--fw-regular);
  text-align: left;
  white-space: normal;
}

.settings-nav__item.active { background: var(--accent-soft); color: var(--accent-text); font-weight: var(--fw-semibold); }

.settings-body { min-width: 0; padding: var(--space-4) 0 0 var(--space-5); }
.settings-category-heading { margin: 0 0 12px; }
.settings-section-nav { display: flex; flex-wrap: wrap; gap: 4px; margin-bottom: 12px; }
.settings-section-nav__item { min-height: var(--h-compact); font-size: var(--fs-secondary); }

.settings-grid-shell {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 16px;
  align-items: start;
}

.settings-actions {
  position: sticky;
  bottom: 0;
  z-index: var(--z-sticky);
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 12px;
  margin-top: 16px;
  padding: 12px 0 6px;
  background: linear-gradient(180deg, transparent, var(--bg-color) 32%);
}

.settings-dirty-hint {
  color: var(--warning-text);
  font-size: var(--fs-secondary);
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
  font-size: var(--fs-secondary);
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
  /* 窄屏用同一组选项横向排布，不额外显示未选类别的表单。 */
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
