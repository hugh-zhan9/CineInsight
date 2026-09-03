<template>
  <div class="page-content settings-page">
    <!-- 原型 A10：16 个分区改成左侧锚点导航 + 右侧连续长表单。
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

    <div class="settings-grid-shell">
    <BasicSection :form="settingsForm" />

    <AutomationSection :form="settingsForm" @error="reportSettingsError" />

    <IdleSchedulingSection :form="settingsForm" />

    <ProxySection :form="settingsForm" />

    <EnhanceSection />

    <FaceSection :form="settingsForm" />

    <MobileSection :form="settingsForm" />

    <AITagSection :form="settingsForm" />

    <AITagLibrarySection
      :groups="localAITagGroups"
      :loading="aiTagLibraryLoading"
      :loaded="aiTagLibraryLoaded"
      :error-message="aiTagLibraryError"
      @reload="loadAITagLibrary"
      @add-group="addAITagLibraryGroup"
      @remove-group="removeAITagLibraryGroup"
      @add-tag="addAITagToGroup"
      @remove-tag="removeAITagFromGroup"
    />

    <RandomAndFormatsSection :form="settingsForm" />

    <SubtitleSection :form="settingsForm" />

    <ScanDirectoriesSection
      :form="settingsForm"
      :directories="directories"
      :watcher-status="watcherStatus"
      :reload-watcher-status="loadLibraryWatcherStatus"
      @directories-changed="$emit('directories-changed', $event)"
    />

    <DatabaseSection :form="settingsForm" />
    </div>

    <div class="settings-actions">
      <div
        v-if="saveMessage"
        class="settings-save-status"
        :class="`settings-save-status--${saveState}`"
        :role="saveState === 'error' ? 'alert' : 'status'"
      >
        {{ saveMessage }}
      </div>
      <button @click="saveSettings" class="btn-primary settings-save-button" :disabled="settingsSaving || aiTagLibraryLoading || !aiTagLibraryLoaded">
        {{ settingsSaving ? '正在保存...' : '保存所有设置' }}
      </button>
    </div>
      </div>
    </div>
  </div>
</template>

<script>
import { UpdateSettings, GetAITagLibrary, SaveAITagLibrary, ClearAITagLibrary, TriggerAITagging, GetLibraryWatcherStatus } from '../../wailsjs/go/main/App';
import { flattenAITagGroups, groupAITagsByNamespace, validateAITagGroups } from '../utils/aiTagLibrary.js';
import { normalizeIdleThresholdMinutes, normalizeIdleWindowBound } from '../utils/idleScheduling.js';
import { normalizeProxyCacheLimitBytes } from '../utils/playbackProxy.js';
import { registerCommands, unregisterCommands } from '../utils/commandRegistry.js';

// 左侧锚点导航的分区清单，顺序与模板里的分区顺序一致。
// 命令面板的「设置 · 各分区」动作也读这份清单（D-029），因此导出而不是本地常量。
export const SETTINGS_SECTIONS = [
  { key: 'basic', label: '基本设置' },
  { key: 'automation', label: '自动化与扫描' },
  { key: 'idle-scheduling', label: '后台任务调度' },
  { key: 'playback-proxy', label: '播放代理' },
  { key: 'enhance', label: '视频超分' },
  { key: 'face', label: '人脸识别' },
  { key: 'mobile', label: '手机端浏览' },
  { key: 'ai-tags', label: 'AI 标签' },
  { key: 'ai-tag-library', label: 'AI 标签库' },
  { key: 'random', label: '智能随机播放' },
  { key: 'video-formats', label: '支持的视频格式' },
  { key: 'image-formats', label: '支持的图片格式' },
  { key: 'subtitle-translate', label: '字幕翻译' },
  { key: 'subtitle-quality', label: '字幕识别质量' },
  { key: 'scan-dirs', label: '扫描目录管理' },
  { key: 'image-dirs', label: '图片扫描目录' },
  { key: 'database', label: '数据库' },
  { key: 'backup', label: '数据库备份' }
];
import AITagLibrarySection from './settings/AITagLibrarySection.vue';
import AITagSection from './settings/AITagSection.vue';
import AutomationSection from './settings/AutomationSection.vue';
import BasicSection from './settings/BasicSection.vue';
import DatabaseSection from './settings/DatabaseSection.vue';
import EnhanceSection from './settings/EnhanceSection.vue';
import FaceSection from './settings/FaceSection.vue';
import IdleSchedulingSection from './settings/IdleSchedulingSection.vue';
import MobileSection from './settings/MobileSection.vue';
import ProxySection from './settings/ProxySection.vue';
import RandomAndFormatsSection from './settings/RandomAndFormatsSection.vue';
import ScanDirectoriesSection from './settings/ScanDirectoriesSection.vue';
import SubtitleSection from './settings/SubtitleSection.vue';
import { confirmAction } from '../utils/feedback.js';

export default {
  name: 'SettingsPage',
  components: { AITagLibrarySection, AITagSection, AutomationSection, BasicSection, DatabaseSection, EnhanceSection, FaceSection, IdleSchedulingSection, MobileSection, ProxySection, RandomAndFormatsSection, ScanDirectoriesSection, SubtitleSection },
  props: {
    settings: { type: Object, required: true },
    directories: { type: Array, default: () => [] }
  },
  emits: ['settings-saved', 'directories-changed', 'tags-changed'],
  data() {
    return {
      settingsForm: { ...this.settings },
      navSections: SETTINGS_SECTIONS,
      activeSection: SETTINGS_SECTIONS[0].key,
      sectionObserver: null,
      watcherStatus: null,
      watcherStatusOff: null,
      localAITagGroups: [],
      aiTagLibraryLoading: false,
      aiTagLibraryLoaded: false,
      aiTagLibraryBaselineCount: 0,
      aiTagLibraryError: '',
      nextAITagLibraryKey: 1,
      settingsSaving: false,
      saveState: 'idle',
      saveMessage: ''
    };
  },
  watch: {
    settings: {
      handler(val) {
        this.settingsForm = { ...val };
        if (!this.settingsForm.video_extensions || this.settingsForm.video_extensions.trim() === '') {
          this.settingsForm.video_extensions = '.mp4,.avi,.mkv,.mov,.wmv,.flv,.webm,.m4v,.ts,.3gp,.mpg,.mpeg,.rm,.rmvb,.vob,.divx,.f4v,.asf,.qt';
        }
        this.settingsForm.ai_tagging_images_per_request = this.settingsForm.ai_tagging_images_per_request || 10;
        this.settingsForm.ai_tagging_subtitle_char_limit = this.settingsForm.ai_tagging_subtitle_char_limit || 4000;
        this.settingsForm.ai_tagging_startup_batch_size = this.settingsForm.ai_tagging_startup_batch_size || 10;
        this.settingsForm.ai_tagging_max_extra_frames = this.settingsForm.ai_tagging_max_extra_frames || 20;
        this.settingsForm.semantic_embedding_model = this.settingsForm.semantic_embedding_model || '';
        this.settingsForm.short_feed_max_duration_minutes = this.settingsForm.short_feed_max_duration_minutes || 5;
		if (this.settingsForm.short_feed_feedback_sync_enabled == null) this.settingsForm.short_feed_feedback_sync_enabled = true;
        this.settingsForm.scan_exclude_paths = this.settingsForm.scan_exclude_paths || '';
        this.settingsForm.image_scan_exclude_paths = this.settingsForm.image_scan_exclude_paths || '';
        this.settingsForm.image_extensions = this.settingsForm.image_extensions || '';
        this.settingsForm.subtitle_translation_provider = this.settingsForm.subtitle_translation_provider || 'deepl';
        this.settingsForm.subtitle_whisperx_model = this.settingsForm.subtitle_whisperx_model || 'medium';
        this.settingsForm.subtitle_whisperx_batch_size = this.settingsForm.subtitle_whisperx_batch_size || 8;
        this.settingsForm.backup_directory = this.settingsForm.backup_directory || '';
        this.settingsForm.backup_retention_count = this.settingsForm.backup_retention_count || 7;
        this.settingsForm.backup_interval_hours = Number.isFinite(this.settingsForm.backup_interval_hours) ? this.settingsForm.backup_interval_hours : 24;
        this.settingsForm.random_half_life_days = Number.isFinite(this.settingsForm.random_half_life_days) ? this.settingsForm.random_half_life_days : 90;
        // 空闲调度：老库/异常值也要落到合法区间，界面上显示的就是会生效的值。
        if (this.settingsForm.idle_scheduling_enabled == null) this.settingsForm.idle_scheduling_enabled = true;
        this.settingsForm.idle_threshold_minutes = normalizeIdleThresholdMinutes(this.settingsForm.idle_threshold_minutes);
        this.settingsForm.idle_require_ac_power = Boolean(this.settingsForm.idle_require_ac_power);
        this.settingsForm.idle_window_start = normalizeIdleWindowBound(this.settingsForm.idle_window_start);
        this.settingsForm.idle_window_end = normalizeIdleWindowBound(this.settingsForm.idle_window_end);
        // 桌面通知默认开（D-013）：老库读回 undefined 时界面也要显示成开着。
        if (this.settingsForm.desktop_notifications_enabled == null) this.settingsForm.desktop_notifications_enabled = true;
        // 播放代理：0 是「不限」这个真实取值，只有读不出数值时才回落到默认 50 GiB。
        this.settingsForm.auto_compatibility_proxy = Boolean(this.settingsForm.auto_compatibility_proxy);
        this.settingsForm.proxy_cache_limit_bytes = normalizeProxyCacheLimitBytes(this.settingsForm.proxy_cache_limit_bytes);
        // 人脸识别（D-016、D-022）：两项默认都是零值，老库读回 undefined 也要落成确定值。
        this.settingsForm.auto_face_analysis = Boolean(this.settingsForm.auto_face_analysis);
        this.settingsForm.face_model_mirror_url = this.settingsForm.face_model_mirror_url || '';
      },
      immediate: true,
      deep: true
    }
  },
  mounted() {
    this.loadAITagLibrary();
    this.loadLibraryWatcherStatus();
    this.observeSections();
    // 命令面板的本页动作（D-029）：保存设置。分区锚点是全局动作，由 App 注册。
    registerCommands('settings-page', [{
      id: 'action:settings-save',
      group: 'action',
      label: '保存所有设置',
      keywords: ['save settings', '保存设置'],
      enabled: () => !this.settingsSaving && this.aiTagLibraryLoaded && !this.aiTagLibraryLoading,
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
    async saveSettings() {
      if (this.settingsSaving) return;
      if (!this.aiTagLibraryLoaded) {
        this.saveState = 'error';
        this.saveMessage = 'AI 标签库尚未成功加载，请重新加载后再保存。';
        return;
      }
      const validationError = validateAITagGroups(this.localAITagGroups);
      if (validationError) {
        this.saveState = 'error';
        this.saveMessage = '设置保存失败：' + validationError;
        return;
      }
      const tagInputs = flattenAITagGroups(this.localAITagGroups);
      const clearingExistingLibrary = tagInputs.length === 0 && this.aiTagLibraryBaselineCount > 0;
      if (clearingExistingLibrary && !await confirmAction({ title: '清空 AI 标签库', message: '这会清空整个 AI 标签库，并使相关待审候选失效。确认继续吗？', confirmText: '清空', danger: true })) {
        this.saveState = 'idle';
        this.saveMessage = '已取消保存，AI 标签库未更改。';
        return;
      }
      this.settingsSaving = true;
      this.saveState = 'saving';
      this.saveMessage = '正在保存设置...';
      try {
        const [savedTags] = await Promise.all([
          clearingExistingLibrary ? ClearAITagLibrary() : SaveAITagLibrary(tagInputs),
          UpdateSettings({
            confirm_before_delete: this.settingsForm.confirm_before_delete,
            delete_original_file: this.settingsForm.delete_original_file,
            video_extensions: this.settingsForm.video_extensions,
            image_extensions: this.settingsForm.image_extensions || '',
            scan_exclude_paths: this.settingsForm.scan_exclude_paths || '',
            image_scan_exclude_paths: this.settingsForm.image_scan_exclude_paths || '',
            play_weight: this.settingsForm.play_weight,
            random_half_life_days: Math.max(0, Number(this.settingsForm.random_half_life_days) || 0),
            auto_scan_on_startup: this.settingsForm.auto_scan_on_startup,
            playback_resume_mode: this.settingsForm.playback_resume_mode || 'resume',
            auto_technical_backfill: this.settingsForm.auto_technical_backfill,
            auto_perceptual_hash: this.settingsForm.auto_perceptual_hash,
            auto_cleanup_analysis: this.settingsForm.auto_cleanup_analysis,
            auto_image_exif_backfill: this.settingsForm.auto_image_exif_backfill,
            auto_collection_suggestions: this.settingsForm.auto_collection_suggestions || false,
            auto_frame_hash_sequence: this.settingsForm.auto_frame_hash_sequence || false,
            idle_scheduling_enabled: this.settingsForm.idle_scheduling_enabled !== false,
            idle_threshold_minutes: normalizeIdleThresholdMinutes(this.settingsForm.idle_threshold_minutes),
            idle_require_ac_power: this.settingsForm.idle_require_ac_power || false,
            idle_window_start: normalizeIdleWindowBound(this.settingsForm.idle_window_start),
            idle_window_end: normalizeIdleWindowBound(this.settingsForm.idle_window_end),
            desktop_notifications_enabled: this.settingsForm.desktop_notifications_enabled !== false,
            auto_compatibility_proxy: this.settingsForm.auto_compatibility_proxy || false,
            proxy_cache_limit_bytes: normalizeProxyCacheLimitBytes(this.settingsForm.proxy_cache_limit_bytes),
            auto_face_analysis: this.settingsForm.auto_face_analysis || false,
            face_model_mirror_url: this.settingsForm.face_model_mirror_url || '',
            library_watch_enabled: this.settingsForm.library_watch_enabled || false,
            local_metadata_enabled: this.settingsForm.local_metadata_enabled || false,
            ai_quality_enabled: this.settingsForm.ai_quality_enabled || false,
            short_feed_max_duration_minutes: this.settingsForm.short_feed_max_duration_minutes || 5,
			short_feed_feedback_sync_enabled: this.settingsForm.short_feed_feedback_sync_enabled !== false,
            theme: this.settingsForm.theme,
            log_enabled: this.settingsForm.log_enabled,
            bilingual_enabled: this.settingsForm.bilingual_enabled || false,
            bilingual_lang: this.settingsForm.bilingual_lang || 'zh',
            deepl_api_key: this.settingsForm.deepl_api_key || '',
            subtitle_translation_provider: this.settingsForm.subtitle_translation_provider || 'deepl',
            subtitle_translation_base_url: this.settingsForm.subtitle_translation_base_url || '',
            subtitle_translation_api_key: this.settingsForm.subtitle_translation_api_key || '',
            subtitle_translation_model: this.settingsForm.subtitle_translation_model || '',
            subtitle_whisperx_model: this.settingsForm.subtitle_whisperx_model || 'medium',
            subtitle_whisperx_batch_size: this.settingsForm.subtitle_whisperx_batch_size || 8,
            ai_tagging_base_url: this.settingsForm.ai_tagging_base_url || '',
            ai_tagging_api_key: this.settingsForm.ai_tagging_api_key || '',
            ai_tagging_model: this.settingsForm.ai_tagging_model || '',
            semantic_embedding_model: this.settingsForm.semantic_embedding_model || '',
            ai_tagging_frame_count: 0,
            ai_tagging_images_per_request: this.settingsForm.ai_tagging_images_per_request || 10,
            ai_tagging_subtitle_char_limit: this.settingsForm.ai_tagging_subtitle_char_limit || 4000,
            ai_tagging_startup_batch_size: this.settingsForm.ai_tagging_startup_batch_size || 10,
            ai_tagging_max_extra_frames: this.settingsForm.ai_tagging_max_extra_frames || 20,
            backup_directory: this.settingsForm.backup_directory || '',
            backup_retention_count: this.settingsForm.backup_retention_count || 7,
            backup_interval_hours: Math.max(0, Number(this.settingsForm.backup_interval_hours) || 0)
          })
        ]);
        const aiTriggered = await TriggerAITagging();
        await this.loadLibraryWatcherStatus();
        this.localAITagGroups = this.withAITagLibraryKeys(savedTags);
        this.aiTagLibraryBaselineCount = savedTags.length;
        this.$emit('settings-saved', { ...this.settingsForm });
        this.$emit('tags-changed');
        const hasActiveTags = tagInputs.some(tag => tag.is_active);
        const hasAIConfig = Boolean(String(this.settingsForm.ai_tagging_base_url || '').trim() && String(this.settingsForm.ai_tagging_model || '').trim());
        this.saveState = 'success';
        if (!hasActiveTags) {
          this.saveMessage = '设置保存成功；AI 标签库没有启用标签，自动打标已暂停。';
        } else if (!hasAIConfig) {
          this.saveMessage = '设置保存成功；AI 接口或模型未配置，自动打标已暂停。';
        } else if (!aiTriggered) {
          this.saveMessage = '设置保存成功；AI 后台任务未运行，自动打标暂未启动。';
        } else {
          this.saveMessage = '设置保存成功，已触发 AI 自动打标。';
        }
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        this.saveState = 'error';
        this.saveMessage = '设置保存失败：' + message;
        console.error('保存设置失败:', err);
      } finally {
        this.settingsSaving = false;
      }
    },
    async loadAITagLibrary() {
      this.aiTagLibraryLoading = true;
      this.aiTagLibraryLoaded = false;
      this.aiTagLibraryError = '';
      try {
        const tags = await GetAITagLibrary();
        this.localAITagGroups = this.withAITagLibraryKeys(tags);
        this.aiTagLibraryBaselineCount = tags.length;
        this.aiTagLibraryLoaded = true;
      } catch (err) {
        this.aiTagLibraryError = '加载 AI 标签库失败: ' + err;
      } finally {
        this.aiTagLibraryLoading = false;
      }
    },
    async loadLibraryWatcherStatus() {
      try {
        this.watcherStatus = await GetLibraryWatcherStatus();
      } catch (_err) {
        this.watcherStatus = null;
      }
    },
    withAITagLibraryKeys(tags) {
      return groupAITagsByNamespace(tags).map(group => ({
        ...group,
        _key: `ai-tag-group-${this.nextAITagLibraryKey++}`,
        tags: group.tags.map(tag => ({ ...tag, _key: `ai-tag-${tag.id || 'new'}-${this.nextAITagLibraryKey++}` }))
      }));
    },
    newAITagLibraryItem() {
      return {
        id: 0,
        name: '',
        color: '#0D9488',
        review_required: false,
        is_active: true,
        _key: `ai-tag-new-${this.nextAITagLibraryKey++}`
      };
    },
    addAITagLibraryGroup() {
      this.localAITagGroups = [...this.localAITagGroups, {
        namespace: '',
        tags: [this.newAITagLibraryItem()],
        _key: `ai-tag-group-new-${this.nextAITagLibraryKey++}`
      }];
    },
    removeAITagLibraryGroup(groupIndex) {
      this.localAITagGroups = this.localAITagGroups.filter((_, index) => index !== groupIndex);
    },
    addAITagToGroup(groupIndex) {
      const group = this.localAITagGroups[groupIndex];
      if (!group) return;
      group.tags = [...group.tags, this.newAITagLibraryItem()];
    },
    removeAITagFromGroup(groupIndex, tagIndex) {
      const group = this.localAITagGroups[groupIndex];
      if (!group) return;
      group.tags = group.tags.filter((_, index) => index !== tagIndex);
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
