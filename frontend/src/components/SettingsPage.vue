<template>
  <div class="page-content settings-page">
    <h2>设置</h2>

    <div class="settings-grid-shell">
    <div class="settings-section">
      <h3>基本设置</h3>
      <div class="setting-item">
        <label class="switch">
          <input type="checkbox" v-model="settingsForm.confirm_before_delete" />
          <span class="slider"></span>
          <span>删除前确认</span>
        </label>
      </div>
      <div class="setting-item">
        <label class="switch">
          <input type="checkbox" v-model="settingsForm.delete_original_file" />
          <span class="slider"></span>
          <span>默认将原始文件移入回收站</span>
        </label>
      </div>
      <div class="setting-item">
        <label class="switch">
          <input type="checkbox" v-model="settingsForm.log_enabled" />
          <span class="slider"></span>
          <span>启用日志记录</span>
        </label>
      </div>
      <div class="setting-item">
        <label>主题模式</label>
        <select v-model="settingsForm.theme" class="select-input">
          <option value="light">浅色模式 (Light)</option>
          <option value="dark">深色模式 (Dark)</option>
          <option value="system">跟随系统 (System)</option>
        </select>
      </div>
    </div>

    <!-- 自动化设置 -->
    <div class="settings-section">
      <h3>自动化与扫描</h3>
      <div class="setting-item">
        <label class="switch">
          <input type="checkbox" v-model="settingsForm.auto_scan_on_startup" />
          <span class="slider"></span>
          <span>启动时自动增量扫描</span>
        </label>
        <p class="help-text">开启后，每次启动应用都会自动同步磁盘文件变动并补全元数据。</p>
      </div>
    </div>

    <div class="settings-section">
      <h3>手机短视频</h3>
      <div class="short-feed-status">
        <div class="short-feed-status-main">
          <strong>{{ shortFeedStatusText }}</strong>
          <span v-if="shortFeedStatus && shortFeedStatus.running">{{ shortFeedStatus.url }}</span>
          <span v-else-if="shortFeedStatus && shortFeedStatus.startup_error">{{ shortFeedStatus.startup_error }}</span>
          <span v-else>短视频服务状态未知</span>
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
      <div v-if="shortFeedStatus && shortFeedStatus.lan_urls && shortFeedStatus.lan_urls.length" class="short-feed-lan-list">
        <div v-for="url in shortFeedStatus.lan_urls" :key="url" class="short-feed-url">{{ url }}</div>
      </div>
      <div class="setting-item short-feed-duration-setting">
        <label>短视频时长上限（分钟）</label>
        <input
          type="number"
          v-model.number="settingsForm.short_feed_max_duration_minutes"
          min="1"
          max="180"
          step="1"
          class="number-input"
        />
        <p class="help-text">只有时长小于此上限的视频会进入手机短视频，并自动维护“短视频”标签，默认 5 分钟。</p>
      </div>
      <p class="help-text">此页面仅面向本机/局域网直接访问，当前版本不启用登录或 PIN。</p>
    </div>

    <div class="settings-section">
      <h3>AI 标签</h3>
      <div class="setting-item">
        <label>接口地址</label>
        <input
          type="text"
          v-model.trim="settingsForm.ai_tagging_base_url"
          placeholder="https://api.openai.com/v1 或 http://127.0.0.1:1234/v1"
          class="text-input"
        />
      </div>
      <div class="setting-item">
        <label>API Key</label>
        <input
          type="password"
          v-model="settingsForm.ai_tagging_api_key"
          placeholder="本地 LM Studio 可留空；云端接口填写 API Key"
          class="text-input"
          autocomplete="off"
        />
      </div>
      <div class="setting-item">
        <label>模型</label>
        <input
          type="text"
          v-model.trim="settingsForm.ai_tagging_model"
          placeholder="支持图像理解的模型"
          class="text-input"
        />
      </div>
      <div class="setting-grid">
        <div class="setting-item">
          <label>单次请求图片上限</label>
          <input
            type="number"
            v-model.number="settingsForm.ai_tagging_images_per_request"
            min="1"
            max="100"
            step="1"
            class="number-input"
          />
		  <p class="help-text">视频按每分钟一帧、至少 10 帧抽取；超过此数量时拆成多个模型请求。</p>
        </div>
        <div class="setting-item">
          <label>字幕字符上限</label>
          <input
            type="number"
            v-model.number="settingsForm.ai_tagging_subtitle_char_limit"
            min="200"
            max="12000"
            step="100"
            class="number-input"
          />
        </div>
        <div class="setting-item">
          <label>后台批量数量</label>
          <input
            type="number"
            v-model.number="settingsForm.ai_tagging_startup_batch_size"
            min="1"
            max="100"
            step="1"
            class="number-input"
          />
        </div>
        <div class="setting-item">
          <label>Agent 额外补帧上限</label>
          <input
            type="number"
            v-model.number="settingsForm.ai_tagging_max_extra_frames"
            min="1"
            max="100"
            step="1"
            class="number-input"
          />
          <p class="help-text">模型决定每次补多少帧，服务端按此值限制单个视频的额外总量，默认 20。</p>
        </div>
      </div>
      <p class="help-text">AI Agent 可按证据决定补帧、临时生成字幕或查找同源视频。临时字幕只在内存中使用，不写入 SRT；只调用已经准备好的本地 WhisperX/Qwen，不会自动安装组件。</p>
      <p class="help-text">抽帧图片、临时字幕文本和同源候选帧可能发送到上方配置的外部 API；原始音频不会发送。保存后后台会自动使用新配置。</p>
    </div>

	<div class="settings-section ai-tag-library-section">
	  <div class="settings-section-heading">
		<h3>AI 标签库</h3>
		<button type="button" class="btn-secondary" @click="addAITagLibraryGroup">添加分类</button>
	  </div>
	  <p class="help-text">每个分类占一行，可在分类内维护多个标签；只有启用的标签会发送给模型。可直接填写已有普通标签的名称，保存后会保留它现有的视频关联并加入 AI 标签库。</p>
	  <div v-if="aiTagLibraryLoading" class="empty-hint">正在加载标签库...</div>
	  <div v-else-if="aiTagLibraryError" class="ai-tag-library-error">{{ aiTagLibraryError }}</div>
	  <div v-else class="ai-tag-library-list">
		<div v-for="(group, groupIndex) in localAITagGroups" :key="group._key" class="ai-tag-library-group">
		  <div class="ai-tag-library-group-heading">
			<input v-model.trim="group.namespace" type="text" class="text-input ai-tag-namespace-input" placeholder="分类名称" aria-label="标签分类名称" />
			<span class="ai-tag-count">{{ group.tags.length }} 个标签</span>
			<button type="button" class="btn-secondary btn-compact" @click="addAITagToGroup(groupIndex)">添加标签</button>
			<button type="button" class="btn-danger btn-compact" @click="removeAITagLibraryGroup(groupIndex)">删除分类</button>
		  </div>
		  <div class="ai-tag-library-group-tags">
			<div v-for="(tag, tagIndex) in group.tags" :key="tag._key" class="ai-tag-library-tag">
			  <input v-model.trim="tag.name" type="text" class="text-input" placeholder="标签名称" :aria-label="`${group.namespace || '未命名分类'}标签名称`" />
			  <input v-model="tag.color" type="color" class="ai-tag-color" aria-label="标签颜色" />
			  <label class="ai-tag-active"><input v-model="tag.is_active" type="checkbox" />启用</label>
			  <button type="button" class="btn-action btn-action-danger ai-tag-remove" title="移出 AI 标签库" :aria-label="`移出标签 ${tag.name || tagIndex + 1}`" @click="removeAITagFromGroup(groupIndex, tagIndex)">×</button>
			</div>
			<div v-if="group.tags.length === 0" class="empty-hint ai-tag-group-empty">当前分类暂无标签</div>
		  </div>
		</div>
		<div v-if="localAITagGroups.length === 0" class="empty-hint">当前未配置 AI 标签，后台分析将暂停。</div>
	  </div>
	</div>

    <!-- 智能随机播放设置 -->
    <div class="settings-section">
      <h3>智能随机播放</h3>
      <div class="setting-item">
        <label>播放权重（1次普通播放 = N次随机播放）</label>
        <div class="setting-control-row">
          <input 
            type="number" 
            v-model.number="settingsForm.play_weight" 
            min="0.1" 
            max="10" 
            step="0.1"
            class="number-input"
          />
        </div>
        <p class="help-text">建议值: 1.0-3.0，默认2.0。权重越高，普通播放对随机选择的影响越大。</p>
      </div>
    </div>

    <!-- 视频格式设置 -->
    <div class="settings-section">
      <h3>支持的视频格式</h3>
      <div class="setting-item">
        <textarea 
          v-model="settingsForm.video_extensions" 
          rows="3"
          class="text-input settings-textarea"
          placeholder=".mp4,.avi,.mkv,.mov,.wmv,.flv,.webm,.m4v,.ts,.3gp,.mpg,.mpeg,.rm,.rmvb,.vob,.divx,.f4v,.asf,.qt"
        ></textarea>
        <p class="help-text">用逗号分隔，留空则使用默认配置。</p>
      </div>
    </div>

    <!-- 字幕设置 -->
    <div class="settings-section">
      <h3>字幕翻译</h3>
      <div class="setting-item">
        <label class="switch">
          <input type="checkbox" v-model="settingsForm.bilingual_enabled" />
          <span class="slider"></span>
          <span>启用双语字幕翻译</span>
        </label>
      </div>
      <template v-if="settingsForm.bilingual_enabled">
        <div class="setting-item">
          <label>目标翻译语言</label>
          <select v-model="settingsForm.bilingual_lang" class="select-input">
            <option value="zh">中文</option>
            <option value="en">英语</option>
            <option value="ja">日语</option>
            <option value="ko">韩语</option>
            <option value="fr">法语</option>
            <option value="de">德语</option>
            <option value="es">西班牙语</option>
            <option value="pt">葡萄牙语</option>
            <option value="ru">俄语</option>
            <option value="it">意大利语</option>
          </select>
        </div>
        <div class="setting-item">
          <label>翻译服务</label>
          <select v-model="settingsForm.subtitle_translation_provider" class="select-input">
            <option value="deepl">DeepL</option>
            <option value="llm">外部 AI（OpenAI 兼容 API）</option>
          </select>
        </div>
        <div v-if="settingsForm.subtitle_translation_provider !== 'llm'" class="setting-item">
          <label>DeepL API Key</label>
          <input 
            type="password" 
            v-model="settingsForm.deepl_api_key" 
            placeholder="填入 DeepL API Key" 
            class="text-input"
            autocomplete="off"
          />
          <p class="help-text">免费版 Key 通常以 :fx 结尾。额度 50 万字符/月。</p>
        </div>
        <template v-else>
          <div class="setting-item">
            <label>AI 翻译接口地址</label>
            <input type="url" v-model.trim="settingsForm.subtitle_translation_base_url" class="text-input" placeholder="https://api.example.com/v1" />
          </div>
          <div class="setting-item">
            <label>AI 翻译 API Key</label>
            <input type="password" v-model="settingsForm.subtitle_translation_api_key" class="text-input" autocomplete="off" />
          </div>
          <div class="setting-item">
            <label>AI 翻译模型</label>
            <input type="text" v-model.trim="settingsForm.subtitle_translation_model" class="text-input" placeholder="gpt-4o-mini" />
          </div>
        </template>
      </template>
    </div>

    <div class="settings-section">
      <h3>字幕识别质量</h3>
      <div class="setting-item">
        <label>WhisperX 模型</label>
        <select v-model="settingsForm.subtitle_whisperx_model" class="select-input">
          <option value="tiny">tiny（最快）</option>
          <option value="base">base</option>
          <option value="small">small</option>
          <option value="medium">medium（推荐）</option>
          <option value="large-v2">large-v2</option>
          <option value="large-v3">large-v3（最准确）</option>
        </select>
      </div>
      <div class="setting-item">
        <label>WhisperX 批量大小</label>
        <input type="number" min="1" max="16" v-model.number="settingsForm.subtitle_whisperx_batch_size" class="number-input" />
        <p class="help-text">CPU 运行时建议使用 4-8；数值越大占用内存越多。</p>
      </div>
    </div>

    <!-- 扫描目录管理 -->
    <div class="settings-section">
      <h3>扫描目录管理</h3>
      <div class="directories-list">
        <div v-for="dir in localDirectories" :key="dir.id" class="directory-item">
          <div class="directory-main">
            <strong>{{ dir.alias || '未命名' }}</strong>
            <span>{{ dir.path }}</span>
          </div>
          <div class="directory-actions">
            <button @click="editDirectory(dir)" class="btn-action">编辑</button>
            <button @click="deleteDirectoryItem(dir.id)" class="btn-action btn-action-danger">删除</button>
          </div>
        </div>
        <div v-if="localDirectories.length === 0" class="empty-hint">暂无扫描目录配置</div>
      </div>
      <button @click="showAddDirectoryDialog = true" class="btn-primary settings-section-action">添加扫描目录</button>
    </div>
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
      <button @click="saveSettings" class="btn-primary settings-save-button" :disabled="settingsSaving">
        {{ settingsSaving ? '正在保存...' : '保存所有设置' }}
      </button>
    </div>

    <!-- Add/Edit Directory Dialog -->
    <div v-if="showAddDirectoryDialog || editingDirectory" class="modal-overlay" @click="closeDirectoryDialog">
      <div class="modal" @click.stop>
        <h2>{{ editingDirectory ? '编辑' : '添加' }}扫描目录</h2>
        <div class="setting-item">
          <label>目录路径</label>
          <div class="directory-dialog-row">
            <input type="text" v-model="directoryForm.path" placeholder="选择目录" class="text-input" readonly />
            <button @click="selectDirectoryForConfig" class="btn-secondary">选择</button>
          </div>
        </div>
        <div class="setting-item">
          <label>目录别名</label>
          <input type="text" v-model="directoryForm.alias" placeholder="给这个目录起个名字" class="text-input directory-alias-input" />
        </div>
        <div class="modal-actions">
          <button @click="saveDirectoryConfig" class="btn-primary">保存</button>
          <button @click="closeDirectoryDialog" class="btn-secondary">取消</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import { UpdateSettings, SelectDirectory, GetAllDirectories, AddDirectory, UpdateDirectory, DeleteDirectory, GetShortFeedServerStatus, GetAITagLibrary, SaveAITagLibrary, TriggerAITagging } from '../../wailsjs/go/main/App';
import { flattenAITagGroups, groupAITagsByNamespace, validateAITagGroups } from '../utils/aiTagLibrary.js';

export default {
  name: 'SettingsPage',
  props: {
    settings: { type: Object, required: true },
    directories: { type: Array, default: () => [] }
  },
  emits: ['settings-saved', 'directories-changed', 'tags-changed'],
  data() {
    return {
      settingsForm: { ...this.settings },
      localDirectories: [...this.directories],
      shortFeedStatus: null,
      showAddDirectoryDialog: false,
      editingDirectory: null,
      directoryForm: { path: '', alias: '' },
      localAITagGroups: [],
      aiTagLibraryLoading: false,
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
        this.settingsForm.short_feed_max_duration_minutes = this.settingsForm.short_feed_max_duration_minutes || 5;
        this.settingsForm.subtitle_translation_provider = this.settingsForm.subtitle_translation_provider || 'deepl';
        this.settingsForm.subtitle_whisperx_model = this.settingsForm.subtitle_whisperx_model || 'medium';
        this.settingsForm.subtitle_whisperx_batch_size = this.settingsForm.subtitle_whisperx_batch_size || 8;
      },
      immediate: true,
      deep: true
    },
    directories: {
      handler(val) {
        this.localDirectories = [...val];
      },
      immediate: true,
      deep: true
    }
  },
  computed: {
    shortFeedStatusText() {
      if (!this.shortFeedStatus) return '未加载';
      if (this.shortFeedStatus.running) {
        return this.shortFeedStatus.fallback_used ? '运行中（备用端口）' : '运行中';
      }
      return '未运行';
    }
  },
  mounted() {
    this.loadShortFeedStatus();
    this.loadAITagLibrary();
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
    async saveSettings() {
      if (this.settingsSaving) return;
      this.settingsSaving = true;
      this.saveState = 'saving';
      this.saveMessage = '正在保存设置...';
      try {
        this.aiTagLibraryError = '';
        const validationError = validateAITagGroups(this.localAITagGroups);
        if (validationError) throw new Error(validationError);
        const tagInputs = flattenAITagGroups(this.localAITagGroups);
        const [savedTags] = await Promise.all([
          SaveAITagLibrary(tagInputs),
          UpdateSettings({
            confirm_before_delete: this.settingsForm.confirm_before_delete,
            delete_original_file: this.settingsForm.delete_original_file,
            video_extensions: this.settingsForm.video_extensions,
            play_weight: this.settingsForm.play_weight,
            auto_scan_on_startup: this.settingsForm.auto_scan_on_startup,
            short_feed_max_duration_minutes: this.settingsForm.short_feed_max_duration_minutes || 5,
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
            ai_tagging_frame_count: 0,
            ai_tagging_images_per_request: this.settingsForm.ai_tagging_images_per_request || 10,
            ai_tagging_subtitle_char_limit: this.settingsForm.ai_tagging_subtitle_char_limit || 4000,
            ai_tagging_startup_batch_size: this.settingsForm.ai_tagging_startup_batch_size || 10,
            ai_tagging_max_extra_frames: this.settingsForm.ai_tagging_max_extra_frames || 20
          })
        ]);
        const aiTriggered = await TriggerAITagging();
        this.localAITagGroups = this.withAITagLibraryKeys(savedTags);
        this.$emit('settings-saved', { ...this.settingsForm });
        this.$emit('tags-changed');
        const hasActiveTags = tagInputs.some(tag => tag.is_active);
        const hasAIConfig = Boolean(String(this.settingsForm.ai_tagging_base_url || '').trim() && String(this.settingsForm.ai_tagging_model || '').trim());
        this.saveState = 'success';
        this.saveMessage = hasActiveTags && hasAIConfig && aiTriggered
          ? '设置保存成功，已触发 AI 自动打标。'
          : '设置保存成功；AI 接口、模型、启用标签或后台任务未就绪，自动打标暂未启动。';
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
      this.aiTagLibraryError = '';
      try {
        this.localAITagGroups = this.withAITagLibraryKeys(await GetAITagLibrary());
      } catch (err) {
        this.aiTagLibraryError = '加载 AI 标签库失败: ' + err;
      } finally {
        this.aiTagLibraryLoading = false;
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
    },
    async selectDirectoryForConfig() {
      try {
        const dir = await SelectDirectory();
        if (dir) this.directoryForm.path = dir;
      } catch (err) {}
    },
    editDirectory(dir) {
      this.editingDirectory = dir;
      this.directoryForm = { path: dir.path, alias: dir.alias };
    },
    async saveDirectoryConfig() {
      if (!this.directoryForm.path) return;
      try {
        if (this.editingDirectory) {
          await UpdateDirectory(this.editingDirectory.id, this.directoryForm.path, this.directoryForm.alias);
        } else {
          await AddDirectory(this.directoryForm.path, this.directoryForm.alias);
        }
        await this.refreshDirectories();
        this.closeDirectoryDialog();
      } catch (err) {}
    },
    async deleteDirectoryItem(id) {
      if (!confirm('确定要删除此目录配置吗？')) return;
      try {
        await DeleteDirectory(id);
        await this.refreshDirectories();
      } catch (err) {}
    },
    async refreshDirectories() {
      try {
        this.localDirectories = await GetAllDirectories();
        this.$emit('directories-changed', this.localDirectories);
      } catch (err) {}
    },
    closeDirectoryDialog() {
      this.showAddDirectoryDialog = false;
      this.editingDirectory = null;
      this.directoryForm = { path: '', alias: '' };
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

.settings-grid-shell {
  display: grid;
  grid-template-columns: repeat(2, minmax(320px, 1fr));
  gap: 14px;
  align-items: start;
}

.settings-section {
  margin-bottom: 0;
  padding: 18px;
  border-radius: var(--radius-lg);
  background: var(--panel-bg);
  -webkit-backdrop-filter: blur(14px);
  backdrop-filter: blur(14px);
}

.settings-section h3 {
  margin-bottom: 16px;
  padding-bottom: 10px;
}

.settings-section-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.settings-section-heading h3 {
  margin-bottom: 0;
}

.ai-tag-library-section {
  grid-column: 1 / -1;
}

.ai-tag-library-list {
  display: grid;
  gap: 0;
  max-height: 420px;
  margin-top: 12px;
  padding-right: 4px;
  overflow-y: auto;
}

.ai-tag-library-group {
  display: grid;
  grid-template-columns: minmax(220px, 0.7fr) minmax(0, 2fr);
  gap: 14px;
  padding: 12px 0;
  border-bottom: 1px solid var(--border-color);
}

.ai-tag-library-group:first-child {
  border-top: 1px solid var(--border-color);
}

.ai-tag-library-group-heading {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-content: start;
  align-items: center;
  gap: 8px;
}

.ai-tag-namespace-input {
  font-weight: 650;
}

.ai-tag-count {
  color: var(--text-secondary);
  font-size: 12px;
  white-space: nowrap;
}

.ai-tag-library-group-heading .btn-compact {
  width: 100%;
}

.ai-tag-library-group-tags {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 8px;
}

.ai-tag-library-tag {
  display: grid;
  grid-template-columns: minmax(120px, 1fr) 32px auto 32px;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.ai-tag-group-empty {
  align-self: center;
  text-align: left;
}

.ai-tag-color {
  width: 32px;
  height: 32px;
  padding: 0;
  border: 1px solid var(--border-color);
  border-radius: 4px;
  background: transparent;
}

.ai-tag-active {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  white-space: nowrap;
}

.ai-tag-remove {
  width: 32px;
  height: 32px;
  padding: 0;
}

.ai-tag-library-error {
  margin-top: 10px;
  color: var(--danger-color);
  font-size: 13px;
}

.setting-control-row {
  margin-top: 10px;
}

.settings-textarea {
  height: auto;
  min-height: 80px;
  padding: 12px;
  resize: vertical;
}

.directories-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.directory-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 11px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
  background: rgba(255, 255, 255, 0.36);
}

.directory-main {
  flex: 1;
  min-width: 0;
}

.directory-main strong,
.directory-main span {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.directory-main strong {
  margin-bottom: 4px;
  font-size: 14px;
}

.directory-main span {
  color: var(--text-secondary);
  font-size: 12px;
}

.directory-actions,
.directory-dialog-row {
  display: flex;
  gap: 8px;
}

.directory-dialog-row .text-input {
  flex: 1;
}

.directory-alias-input {
  margin-top: 8px;
}

.btn-action-danger {
  border-color: var(--danger-color);
  color: var(--danger-color);
}

.settings-section-action {
  margin-top: 15px;
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
  border-color: rgba(15, 143, 130, 0.34);
  color: var(--accent-color);
}

.settings-save-status--error {
  border-color: rgba(229, 72, 77, 0.4);
  color: var(--danger-color);
}

.short-feed-status {
  display: flex;
  gap: 12px;
  align-items: center;
  justify-content: space-between;
  padding: 14px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius);
  background: var(--bg-color);
}

.short-feed-status-main {
  display: grid;
  gap: 6px;
  min-width: 0;
}

.short-feed-status-main span,
.short-feed-url {
  overflow: hidden;
  color: var(--text-secondary);
  font-size: 13px;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.short-feed-actions {
  display: flex;
  flex-shrink: 0;
  gap: 8px;
}

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

@media (max-width: 980px) {
  .settings-grid-shell {
    grid-template-columns: 1fr;
  }

  .directory-item {
    align-items: stretch;
    flex-direction: column;
  }

  .directory-actions {
    justify-content: flex-end;
  }

  .ai-tag-library-group {
    grid-template-columns: minmax(190px, 0.65fr) minmax(0, 1.8fr);
  }
}

@media (max-width: 600px) {
  .ai-tag-library-group {
    grid-template-columns: 1fr;
  }

  .ai-tag-library-group-tags {
    grid-template-columns: 1fr;
  }

  .settings-actions {
    align-items: stretch;
    flex-direction: column;
  }

  .settings-save-button {
    width: 100%;
  }
}
</style>
