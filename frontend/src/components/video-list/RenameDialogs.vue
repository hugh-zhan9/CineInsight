<template>
  <!-- 重命名弹窗 -->
  <BaseModal v-if="renameDialog.show" class="download-modal">
      <h3>重命名视频</h3>
      <input
        v-model="renameDialog.newName"
        type="text"
        class="search-input rename-input"
        placeholder="输入新文件名"
        @keyup.enter="executeRename"
        ref="renameInput"
      />
      <p class="rename-hint">扩展名会自动保留（{{ renameDialog.ext }}）</p>
      <div class="modal-actions">
        <button @click="renameDialog.show = false" class="btn-secondary">取消</button>
        <button @click="executeRename" class="btn-primary">确认</button>
      </div>
  </BaseModal>

  <BaseModal v-if="folderRenameDialog.show" class="download-modal">
      <h3>重命名文件夹</h3>
      <p class="folder-rename-source" :title="folderRenameDialog.source">{{ folderRenameDialog.source }}</p>
      <input
        ref="folderRenameInput"
        v-model="folderRenameDialog.newName"
        type="text"
        maxlength="255"
        class="search-input rename-input"
        placeholder="输入新的文件夹名称"
        :disabled="migrationRunning"
        @keyup.enter="executeFolderRename"
      />
      <p class="rename-hint">只修改当前文件夹名称，内部目录结构和视频关联保持不变。</p>
      <p v-if="folderRenameDialog.error" class="cleanup-error">{{ folderRenameDialog.error }}</p>
      <div class="modal-actions">
        <button type="button" class="btn-secondary" :disabled="migrationRunning" @click="folderRenameDialog.show = false">取消</button>
        <button type="button" class="btn-primary" :disabled="migrationRunning || !folderRenameDialog.newName.trim()" @click="executeFolderRename">{{ migrationRunning ? '重命名中...' : '确认重命名' }}</button>
      </div>
  </BaseModal>
</template>

<script>
import { RenameVideo, RenameDirectory, SelectFolderToRename, GetSettings } from '../../../wailsjs/go/main/App';
import BaseModal from '../ui/BaseModal.vue';
import { notify, notifyError } from '../../utils/feedback.js';

// 重命名视频与重命名文件夹两个弹窗。片库页通过 ref 调 openRenameVideo() / openRenameFolder()；
// 文件夹重命名会动扫描目录与设置，所以完成后把三件事交回片库页处理。
export default {
  name: 'RenameDialogs',
  components: { BaseModal },
  props: {
    migrationRunning: { type: Boolean, default: false },
    reloadView: { type: Function, required: true }
  },
  emits: ['update:migrationRunning', 'video-renamed', 'reload-directories', 'update-settings'],
  data() {
    return {
      // 重命名弹窗
      renameDialog: { show: false, video: null, newName: '', ext: '' },
      folderRenameDialog: { show: false, source: '', currentName: '', newName: '', error: '' }
    };
  },
  methods: {
    openRenameVideo(video) {
      return this.renameVideo(video);
    },
    openRenameFolder() {
      return this.renameFolder();
    },
    async renameVideo(video) {
      const ext = video.name.lastIndexOf('.') > 0 ? video.name.substring(video.name.lastIndexOf('.')) : '';
      const baseName = ext ? video.name.slice(0, -ext.length) : video.name;
      this.renameDialog = { show: true, video, newName: baseName, ext: ext || '(无)' };
      this.$nextTick(() => {
        if (this.$refs.renameInput) this.$refs.renameInput.focus();
      });
    },
    async executeRename() {
      const { video, newName, ext } = this.renameDialog;
      if (!newName.trim()) return;
      try {
        await RenameVideo(video.id, newName.trim());
        const finalName = newName.trim() + (ext !== '(无)' ? ext : '');
        this.$emit('video-renamed', { video, finalName });
        this.renameDialog.show = false;
      } catch (err) {
        console.error('重命名失败:', err);
        notifyError('重命名失败: ' + err);
      }
    },
    async renameFolder() {
      if (this.migrationRunning) return;
      try {
        const source = await SelectFolderToRename();
        if (!source) return;
        const currentName = String(source).replace(/[\\/]+$/, '').split(/[\\/]/).pop() || '';
        this.folderRenameDialog = { show: true, source, currentName, newName: currentName, error: '' };
        this.$nextTick(() => this.$refs.folderRenameInput?.focus());
      } catch (err) {
        notifyError('选择文件夹失败: ' + err);
      }
    },
    async executeFolderRename() {
      const source = this.folderRenameDialog.source;
      const newName = this.folderRenameDialog.newName.trim();
      if (!source || !newName || this.migrationRunning) return;
      if (newName === this.folderRenameDialog.currentName) {
        this.folderRenameDialog.error = '请输入不同于当前名称的新名称。';
        return;
      }
      this.$emit('update:migrationRunning', true);
      this.folderRenameDialog.error = '';
      try {
        const result = await RenameDirectory(source, newName);
        this.folderRenameDialog.show = false;
        this.$emit('reload-directories');
        try {
          const refreshedSettings = await GetSettings();
          this.$emit('update-settings', refreshedSettings);
        } catch (settingsErr) {
          console.warn('文件夹重命名后刷新设置失败:', settingsErr);
        }
        await this.reloadView();
        notify(`文件夹重命名完成：更新 ${result?.videos_updated || 0} 个视频、${result?.directories_updated || 0} 个扫描目录。`);
      } catch (err) {
        console.error('重命名文件夹失败:', err);
        this.folderRenameDialog.error = '重命名失败：' + err;
      } finally {
        this.$emit('update:migrationRunning', false);
      }
    },
  }
};
</script>

<style scoped>
:deep(.download-modal) {
  width: 400px;
  text-align: center;
  padding: 30px;
}
.rename-input {
  margin: 15px 0;
}
.rename-hint {
  color: var(--text-muted);
  font-size: 12px;
}
.folder-rename-source {
  margin: 12px 0 4px;
  color: var(--text-secondary);
  font-size: 12px;
  line-height: 1.5;
  overflow-wrap: anywhere;
}
.cleanup-error,
.cleanup-intro,
.cleanup-loading,
.cleanup-empty {
  color: var(--review-text-muted);
  font-size: 13px;
}
</style>
