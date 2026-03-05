<template>
  <div class="page-content settings-page">
    <h2>设置</h2>
    
    <!-- 基本设置 -->
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
          <span>默认删除原始文件</span>
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

    <!-- 智能随机播放设置 -->
    <div class="settings-section">
      <h3>智能随机播放</h3>
      <div class="setting-item">
        <label>播放权重（1次普通播放 = N次随机播放）</label>
        <div style="margin-top: 10px;">
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
      <div class="directories-list" style="display: flex; flex-direction: column; gap: 10px;">
        <div v-for="dir in localDirectories" :key="dir.id" class="directory-item" style="background: var(--bg-color); padding: 12px; border-radius: var(--radius-md); display: flex; justify-content: space-between; align-items: center; border: 1px solid var(--border-color);">
          <div style="flex: 1; min-width: 0; margin-right: 15px;">
            <strong style="display: block; font-size: 14px; margin-bottom: 4px;">{{ dir.alias || '未命名' }}</strong>
            <span style="font-size: 12px; color: var(--text-secondary); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; display: block;">{{ dir.path }}</span>
          </div>
          <div style="display: flex; gap: 8px;">
            <button @click="editDirectory(dir)" class="btn-action">编辑</button>
            <button @click="deleteDirectoryItem(dir.id)" class="btn-action" style="color: var(--danger-color); border-color: var(--danger-color);">删除</button>
          </div>
        </div>
        <div v-if="localDirectories.length === 0" class="empty-hint">暂无扫描目录配置</div>
      </div>
      <button @click="showAddDirectoryDialog = true" class="btn-primary" style="margin-top: 15px;">添加扫描目录</button>
    </div>

    <div class="settings-actions" style="margin-top: 40px; text-align: right; padding-bottom: 40px;">
      <button @click="saveSettings" class="btn-primary" style="padding: 0 32px;">保存所有设置</button>
    </div>

    <!-- Add/Edit Directory Dialog -->
    <div v-if="showAddDirectoryDialog || editingDirectory" class="modal-overlay" @click="closeDirectoryDialog">
      <div class="modal" @click.stop>
        <h2>{{ editingDirectory ? '编辑' : '添加' }}扫描目录</h2>
        <div class="setting-item">
          <label>目录路径</label>
          <div style="display: flex; gap: 8px; margin-top: 8px;">
            <input type="text" v-model="directoryForm.path" placeholder="选择目录" class="text-input" readonly style="flex: 1;" />
            <button @click="selectDirectoryForConfig" class="btn-secondary">选择</button>
          </div>
        </div>
        <div class="setting-item">
          <label>目录别名</label>
          <input type="text" v-model="directoryForm.alias" placeholder="给这个目录起个名字" class="text-input" style="margin-top: 8px;" />
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
import { UpdateSettings, SelectDirectory, GetAllDirectories, AddDirectory, UpdateDirectory, DeleteDirectory } from '../../wailsjs/go/main/App';

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
  methods: {
    async saveSettings() {
      try {
        await UpdateSettings({
          confirm_before_delete: this.settingsForm.confirm_before_delete,
          delete_original_file: this.settingsForm.delete_original_file,
          video_extensions: this.settingsForm.video_extensions,
          play_weight: this.settingsForm.play_weight,
          auto_scan_on_startup: this.settingsForm.auto_scan_on_startup,
          theme: this.settingsForm.theme,
          log_enabled: this.settingsForm.log_enabled,
          bilingual_enabled: this.settingsForm.bilingual_enabled || false,
          bilingual_lang: this.settingsForm.bilingual_lang || 'zh',
          deepl_api_key: this.settingsForm.deepl_api_key || ''
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
