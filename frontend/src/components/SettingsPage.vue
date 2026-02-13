<template>
  <div class="page-content settings-page">
    <h2>设置</h2>
    
    <!-- 基本设置 -->
    <div class="settings-section">
      <h3>基本设置</h3>
      <div class="setting-item">
        <label>
          <input type="checkbox" v-model="settingsForm.confirm_before_delete" />
          删除前确认
        </label>
      </div>
      <div class="setting-item">
        <label>
          <input type="checkbox" v-model="settingsForm.delete_original_file" />
          默认删除原始文件
        </label>
      </div>
      <div class="setting-item">
        <label>
          <input type="checkbox" v-model="settingsForm.auto_scan_on_startup" />
          启动时自动增量扫描配置的目录
        </label>
      </div>
      <div class="setting-item">
        <label>
          <input type="checkbox" v-model="settingsForm.log_enabled" />
          启用日志记录（写入 ~/.video-master/app.log）
        </label>
      </div>
    </div>

    <!-- 智能随机播放设置 -->
    <div class="settings-section">
      <h3>智能随机播放</h3>
      <div class="setting-item">
        <label>播放权重（1次普通播放 = N次随机播放）</label>
        <input 
          type="number" 
          v-model.number="settingsForm.play_weight" 
          min="0.1" 
          max="10" 
          step="0.1"
          class="number-input"
        />
        <p class="hint-text">
          当前设置: 1次普通播放 = {{ settingsForm.play_weight }}次随机播放<br/>
          <span class="help-text">
            权重越高，普通播放对随机选择的影响越大。<br/>
            建议值: 1.0-3.0，默认2.0
          </span>
        </p>
      </div>
    </div>

    <!-- 视频格式设置 -->
    <div class="settings-section">
      <h3>支持的视频格式</h3>
      <div class="setting-item">
        <label>格式列表（用逗号分隔，如: .mp4,.avi,.mkv,.rmvb,.qt）</label>
        <textarea 
          v-model="settingsForm.video_extensions" 
          rows="3"
          placeholder=".mp4,.avi,.mkv,.mov,.wmv,.flv,.webm,.m4v,.ts,.3gp,.mpg,.mpeg,.rm,.rmvb,.vob,.divx,.f4v,.asf,.qt"
        ></textarea>
        <p class="hint-text settings-current">当前支持: {{ settingsForm.video_extensions }}</p>
      </div>
    </div>

    <!-- 扫描目录管理 -->
    <div class="settings-section">
      <h3>扫描目录管理</h3>
      <div class="directories-list">
        <div v-for="dir in localDirectories" :key="dir.id" class="directory-item">
          <div class="directory-info">
            <strong>{{ dir.alias || '未命名' }}</strong>
            <span class="directory-path">{{ dir.path }}</span>
          </div>
          <div class="directory-actions">
            <button @click="editDirectory(dir)" class="btn-small">编辑</button>
            <button @click="deleteDirectoryItem(dir.id)" class="btn-small btn-danger">删除</button>
          </div>
        </div>
        <div v-if="localDirectories.length === 0" class="empty-hint">
          暂无扫描目录配置
        </div>
      </div>
      <button @click="showAddDirectoryDialog = true" class="btn-primary mt-10">添加扫描目录</button>
    </div>

    <!-- 保存按钮 -->
    <div class="settings-actions">
      <button @click="saveSettings" class="btn-primary">保存设置</button>
    </div>

    <!-- Add/Edit Directory Dialog -->
    <div v-if="showAddDirectoryDialog || editingDirectory" class="modal-overlay" @click="closeDirectoryDialog">
      <div class="modal" @click.stop>
        <h2>{{ editingDirectory ? '编辑' : '添加' }}扫描目录</h2>
        <div class="form-group">
          <label>目录路径</label>
          <div class="input-with-button">
            <input type="text" v-model="directoryForm.path" placeholder="选择目录" readonly />
            <button @click="selectDirectoryForConfig" class="btn-secondary">选择</button>
          </div>
        </div>
        <div class="form-group">
          <label>目录别名</label>
          <input type="text" v-model="directoryForm.alias" placeholder="给这个目录起个名字" />
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
          log_enabled: this.settingsForm.log_enabled
        });
        this.$emit('settings-saved', { ...this.settingsForm });
        alert('设置保存成功！智能随机播放已更新。');
      } catch (err) {
        console.error('保存设置失败:', err);
        alert('保存设置失败: ' + err);
      }
    },
    async selectDirectoryForConfig() {
      try {
        this.directoryForm.path = await SelectDirectory();
      } catch (err) {
        console.error('选择目录失败:', err);
        alert('选择目录失败: ' + err);
      }
    },
    editDirectory(dir) {
      this.editingDirectory = dir;
      this.directoryForm = { path: dir.path, alias: dir.alias };
    },
    async saveDirectoryConfig() {
      if (!this.directoryForm.path) {
        alert('请选择目录');
        return;
      }

      try {
        if (this.editingDirectory) {
          await UpdateDirectory(this.editingDirectory.id, this.directoryForm.path, this.directoryForm.alias);
        } else {
          await AddDirectory(this.directoryForm.path, this.directoryForm.alias);
        }
        await this.refreshDirectories();
        this.closeDirectoryDialog();
      } catch (err) {
        console.error('保存目录失败:', err);
        alert('保存目录失败: ' + err);
      }
    },
    async deleteDirectoryItem(id) {
      if (!confirm('确定要删除此目录配置吗？')) return;

      try {
        await DeleteDirectory(id);
        await this.refreshDirectories();
      } catch (err) {
        console.error('删除目录失败:', err);
        alert('删除目录失败: ' + err);
      }
    },
    async refreshDirectories() {
      try {
        this.localDirectories = await GetAllDirectories();
        this.$emit('directories-changed', this.localDirectories);
      } catch (err) {
        console.error('加载目录失败:', err);
        alert('加载目录失败: ' + err);
      }
    },
    closeDirectoryDialog() {
      this.showAddDirectoryDialog = false;
      this.editingDirectory = null;
      this.directoryForm = { path: '', alias: '' };
    }
  }
};
</script>
