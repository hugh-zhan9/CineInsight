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
        <p class="help-text">只有时长小于此上限的视频会进入手机短视频，默认 5 分钟。</p>
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
          <label>抽帧数量</label>
          <input
            type="number"
            v-model.number="settingsForm.ai_tagging_frame_count"
            min="1"
            max="8"
            step="1"
            class="number-input"
          />
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
      </div>
      <p class="help-text">保存后后台会自动使用新配置；本地 LM Studio 通常可用 http://127.0.0.1:1234/v1，API Key 可填任意非空值。</p>
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
      </template>
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
      <button @click="saveSettings" class="btn-primary settings-save-button">保存所有设置</button>
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
import { UpdateSettings, SelectDirectory, GetAllDirectories, AddDirectory, UpdateDirectory, DeleteDirectory, GetShortFeedServerStatus } from '../../wailsjs/go/main/App';

export default {
  name: 'SettingsPage',
  props: {
    settings: { type: Object, required: true },
    directories: { type: Array, default: () => [] }
  },
  emits: ['settings-saved', 'directories-changed'],
  data() {
    return {
      settingsForm: { ...this.settings },
      localDirectories: [...this.directories],
      shortFeedStatus: null,
      showAddDirectoryDialog: false,
      editingDirectory: null,
      directoryForm: { path: '', alias: '' }
    };
  },
  watch: {
    settings: {
      handler(val) {
        this.settingsForm = { ...val };
        if (!this.settingsForm.video_extensions || this.settingsForm.video_extensions.trim() === '') {
          this.settingsForm.video_extensions = '.mp4,.avi,.mkv,.mov,.wmv,.flv,.webm,.m4v,.ts,.3gp,.mpg,.mpeg,.rm,.rmvb,.vob,.divx,.f4v,.asf,.qt';
        }
        this.settingsForm.ai_tagging_frame_count = this.settingsForm.ai_tagging_frame_count || 5;
        this.settingsForm.ai_tagging_subtitle_char_limit = this.settingsForm.ai_tagging_subtitle_char_limit || 4000;
        this.settingsForm.ai_tagging_startup_batch_size = this.settingsForm.ai_tagging_startup_batch_size || 10;
        this.settingsForm.short_feed_max_duration_minutes = this.settingsForm.short_feed_max_duration_minutes || 5;
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
      try {
        await UpdateSettings({
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
          ai_tagging_base_url: this.settingsForm.ai_tagging_base_url || '',
          ai_tagging_api_key: this.settingsForm.ai_tagging_api_key || '',
          ai_tagging_model: this.settingsForm.ai_tagging_model || '',
          ai_tagging_frame_count: this.settingsForm.ai_tagging_frame_count || 5,
          ai_tagging_subtitle_char_limit: this.settingsForm.ai_tagging_subtitle_char_limit || 4000,
          ai_tagging_startup_batch_size: this.settingsForm.ai_tagging_startup_batch_size || 10
        });
        this.$emit('settings-saved', { ...this.settingsForm });
        alert('设置保存成功！');
      } catch (err) {
        console.error('保存设置失败:', err);
        alert('保存设置失败: ' + err);
      }
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
  margin-top: 16px;
  padding: 12px 0 6px;
  text-align: right;
  background: linear-gradient(180deg, transparent, var(--bg-color) 32%);
}

.settings-save-button {
  padding: 0 32px;
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
}
</style>
