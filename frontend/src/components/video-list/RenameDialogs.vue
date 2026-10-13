<template>
  <!-- 重命名弹窗 -->
  <DialogShell v-if="renameDialog.show" title="重命名视频" size="sm" class="rename-dialog" @close="renameDialog.show = false">
      <input
        v-model="renameDialog.newName"
        type="text"
        class="text-input rename-input"
        placeholder="输入新文件名"
        @keyup.enter="executeRename"
        ref="renameInput"
        data-test="rename-video-input"
      />
      <!-- 与后端 RenameVideo 同一规则（D-PC12、LIB-03）：只有「视频扩展名」里的后缀才算扩展名，
           其余一律补回原扩展名，所以 The.Matrix.1999.1080p 不会被当成「.1080p」文件。 -->
      <p class="rename-hint" data-test="rename-video-preview">{{ renamePreviewText }}</p>
      <template #footer>
        <button type="button" class="btn btn--secondary" @click="renameDialog.show = false">取消</button>
        <button type="button" class="btn btn--primary" @click="executeRename">重命名</button>
      </template>
  </DialogShell>

  <DialogShell v-if="folderRenameDialog.show" title="重命名文件夹" size="sm" :busy="migrationRunning" class="rename-dialog" @close="folderRenameDialog.show = false">
      <p class="folder-rename-source" :title="folderRenameDialog.source">{{ folderRenameDialog.source }}</p>
      <input
        ref="folderRenameInput"
        v-model="folderRenameDialog.newName"
        type="text"
        maxlength="255"
        class="text-input rename-input"
        placeholder="输入新的文件夹名称"
        :disabled="migrationRunning"
        @keyup.enter="executeFolderRename"
      />
      <p class="rename-hint">只修改当前文件夹名称，内部目录结构和视频关联保持不变。</p>
      <p v-if="folderRenameDialog.error" class="rename-error" role="alert">{{ folderRenameDialog.error }}</p>
      <template #footer>
        <button type="button" class="btn btn--secondary" :disabled="migrationRunning" @click="folderRenameDialog.show = false">取消</button>
        <button type="button" class="btn btn--primary" :disabled="migrationRunning || !folderRenameDialog.newName.trim()" @click="executeFolderRename">{{ migrationRunning ? '重命名中…' : '确认重命名' }}</button>
      </template>
  </DialogShell>
</template>

<script>
import { RenameVideo, RenameDirectory, SelectFolderToRename, GetSettings } from '../../../wailsjs/go/main/App';
import DialogShell from '../ui/DialogShell.vue';
import { notify, notifyError } from '../../utils/feedback.js';

// 「视频扩展名」设置为空时后端用的默认集合（services/video_scan.go 的 defaultVideoExtensions，
// RenameDialogs.test.js 读 Go 源码核对两边一致）。
export const DEFAULT_VIDEO_EXTENSIONS = '.mp4,.avi,.mkv,.mov,.wmv,.flv,.webm,.m4v,.ts,.3gp,.mpg,.mpeg,.rm,.rmvb,.vob,.divx,.f4v,.asf,.qt';

// Go filepath.Ext：从最后一个点开始的后缀（含点），没有点时为空。
function fileExtension(name) {
  const index = String(name).lastIndexOf('.');
  return index === -1 ? '' : String(name).slice(index);
}

function isConfiguredVideoExtension(ext, videoExtensions) {
  const raw = String(videoExtensions || '').trim() ? videoExtensions : DEFAULT_VIDEO_EXTENSIONS;
  const wanted = ext.toLowerCase();
  return raw.split(',').some(item => {
    let candidate = item.trim().toLowerCase();
    if (!candidate) return false;
    if (!candidate.startsWith('.')) candidate = '.' + candidate;
    return candidate === wanted;
  });
}

// 重命名后的实际文件名，与后端 RenameVideo 逐步对应：去掉首尾空白；新名字的后缀不在视频扩展名里
// （或者根本没有后缀）就补回原扩展名。
export function renamedVideoFileName(currentName, input, videoExtensions) {
  const newName = String(input || '').trim();
  if (!newName) return '';
  const ext = fileExtension(newName);
  if (!ext || !isConfiguredVideoExtension(ext, videoExtensions)) return newName + fileExtension(currentName);
  return newName;
}

// 重命名视频与重命名文件夹两个弹窗。片库页通过 ref 调 openRenameVideo() / openRenameFolder()；
// 文件夹重命名会动扫描目录与设置，所以完成后把三件事交回片库页处理。
export default {
  name: 'RenameDialogs',
  components: { DialogShell },
  props: {
    migrationRunning: { type: Boolean, default: false },
    reloadView: { type: Function, required: true },
    // 设置里的「视频扩展名」（逗号分隔）；为空时按后端默认集合。
    videoExtensions: { type: String, default: '' }
  },
  emits: ['update:migrationRunning', 'video-renamed', 'reload-directories', 'update-settings'],
  data() {
    return {
      // 重命名弹窗
      renameDialog: { show: false, video: null, newName: '', ext: '' },
      folderRenameDialog: { show: false, source: '', currentName: '', newName: '', error: '' }
    };
  },
  computed: {
    renameFinalName() {
      const video = this.renameDialog.video;
      if (!video) return '';
      return renamedVideoFileName(video.name, this.renameDialog.newName, this.videoExtensions);
    },
    renamePreviewText() {
      if (!this.renameFinalName) return '请输入新文件名';
      if (this.renameFinalName === this.renameDialog.video?.name) return '文件名没有变化';
      return `将重命名为：${this.renameFinalName}`;
    }
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
      const { video, newName } = this.renameDialog;
      if (!newName.trim()) return;
      const finalName = this.renameFinalName;
      // 规范化后与原名相同（例如预填的名字没改就确认）：后端本来也什么都不做，直接关掉。
      if (finalName === video.name) {
        this.renameDialog.show = false;
        return;
      }
      try {
        await RenameVideo(video.id, newName.trim());
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
.rename-input {
  margin: var(--space-2) 0;
}
.rename-hint {
  margin: 0;
  color: var(--text-muted);
  font-size: var(--fs-caption);
}
.folder-rename-source {
  margin: 0 0 var(--space-1);
  color: var(--text-secondary);
  font-size: var(--fs-caption);
  line-height: 1.5;
  overflow-wrap: anywhere;
}
.rename-error {
  margin: var(--space-2) 0 0;
  color: var(--danger-text);
  font-size: var(--fs-secondary);
}
</style>
