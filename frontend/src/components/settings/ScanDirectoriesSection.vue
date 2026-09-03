<template>
  <!-- 扫描目录管理 -->
  <div :id="`settings-scan-dirs`" class="settings-section">
    <h3>扫描目录管理</h3>
    <div class="directories-list">
      <div v-for="dir in localDirectories" :key="dir.id" class="directory-item">
        <div class="directory-main">
          <strong>{{ dir.alias || '未命名' }}</strong>
          <span>{{ dir.path }}</span>
          <div class="directory-watch-status" :class="`directory-watch-status--${directoryWatchState(dir.id)}`">
            <span>{{ directoryWatchText(dir.id) }}</span>
            <button
              v-if="form.library_watch_enabled && ['unavailable', 'error'].includes(directoryWatchState(dir.id))"
              type="button"
              class="btn-link"
              :data-test="`retry-library-watch-${dir.id}`"
              @click="retryDirectoryWatch(dir.id)"
            >重试</button>
          </div>
        </div>
        <div class="directory-actions">
          <button @click="editDirectory(dir)" class="btn-secondary">编辑</button>
          <button @click="deleteDirectoryItem(dir.id)" class="btn-secondary btn-danger-outline">删除</button>
        </div>
      </div>
      <div v-if="localDirectories.length === 0" class="empty-hint">暂无扫描目录配置</div>
    </div>
    <button @click="showAddDirectoryDialog = true" class="btn-primary settings-section-action">添加扫描目录</button>
  </div>

  <!-- 图片扫描目录管理 -->
  <div :id="`settings-image-dirs`" class="settings-section">
    <h3>图片扫描目录</h3>
    <div class="directories-list">
      <div v-for="dir in localImageDirectories" :key="dir.id" class="directory-item" data-test="image-directory-item">
        <div class="directory-main">
          <strong>{{ dir.alias || '未命名' }}</strong>
          <span>{{ dir.path }}</span>
        </div>
        <div class="directory-actions">
          <button @click="editImageDirectory(dir)" class="btn-secondary">编辑</button>
          <button @click="deleteImageDirectoryItem(dir.id)" class="btn-secondary btn-danger-outline">删除</button>
        </div>
      </div>
      <div v-if="localImageDirectories.length === 0" class="empty-hint">暂无图片扫描目录配置</div>
    </div>
    <button @click="showAddImageDirectoryDialog = true" class="btn-primary settings-section-action" data-test="add-image-directory">添加图片目录</button>
  </div>

  <!-- Add/Edit Directory Dialog -->
  <BaseModal v-if="showAddDirectoryDialog || editingDirectory" close-on-overlay stop-modal-clicks @close="closeDirectoryDialog">
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
  </BaseModal>

  <!-- Add/Edit Image Directory Dialog -->
  <BaseModal v-if="showAddImageDirectoryDialog || editingImageDirectory" close-on-overlay stop-modal-clicks @close="closeImageDirectoryDialog">
      <h2>{{ editingImageDirectory ? '编辑' : '添加' }}图片扫描目录</h2>
      <div class="setting-item">
        <label>目录路径</label>
        <div class="directory-dialog-row">
          <input type="text" v-model="imageDirectoryForm.path" placeholder="选择目录" class="text-input" readonly data-test="image-directory-path" />
          <button @click="selectDirectoryForImageConfig" class="btn-secondary">选择</button>
        </div>
      </div>
      <div class="setting-item">
        <label>目录别名</label>
        <input type="text" v-model="imageDirectoryForm.alias" placeholder="给这个目录起个名字" class="text-input directory-alias-input" data-test="image-directory-alias" />
      </div>
      <div class="modal-actions">
        <button @click="saveImageDirectoryConfig" class="btn-primary" data-test="save-image-directory">保存</button>
        <button @click="closeImageDirectoryDialog" class="btn-secondary">取消</button>
      </div>
  </BaseModal>
</template>

<script>
import { SelectDirectory, GetAllDirectories, AddDirectory, UpdateDirectory, DeleteDirectory, RetryLibraryWatcherRoot, GetAllImageDirectories, AddImageDirectory, UpdateImageDirectory, DeleteImageDirectory } from '../../../wailsjs/go/main/App';
import BaseModal from '../ui/BaseModal.vue';
import { confirmAction } from '../../utils/feedback.js';

// 视频与图片两组扫描目录，以及它们的添加/编辑弹窗。目录变更要让整个应用刷新，
// 所以往上发 directories-changed；实时监听状态由设置页统一拉取后传进来。
export default {
  name: 'ScanDirectoriesSection',
  components: { BaseModal },
  props: {
    form: { type: Object, required: true },
    directories: { type: Array, default: () => [] },
    watcherStatus: { type: Object, default: null },
    // 实时监听状态归设置页统一拉取（保存流程也会用），这里改完目录后请它重取一次。
    reloadWatcherStatus: { type: Function, required: true }
  },
  emits: ['directories-changed'],
  data() {
    return {
      localDirectories: [...this.directories],
      showAddDirectoryDialog: false,
      editingDirectory: null,
      directoryForm: { path: '', alias: '' },
      localImageDirectories: [],
      showAddImageDirectoryDialog: false,
      editingImageDirectory: null,
      imageDirectoryForm: { path: '', alias: '' }
    };
  },
  watch: {
    directories: {
      handler(val) {
        this.localDirectories = [...val];
      },
      immediate: true,
      deep: true
    }
  },
  mounted() {
    this.loadImageDirectories();
  },
  methods: {
    directoryWatchStatus(directoryID) {
      return this.watcherStatus?.roots?.find(root => Number(root.directory_id) === Number(directoryID)) || null;
    },
    directoryWatchState(directoryID) {
      if (!this.form.library_watch_enabled) return 'disabled';
      return this.directoryWatchStatus(directoryID)?.state || 'error';
    },
    directoryWatchText(directoryID) {
      if (!this.form.library_watch_enabled) return '实时同步已关闭';
      const status = this.directoryWatchStatus(directoryID);
      if (!status) return '实时同步状态未知';
      if (status.state === 'watching') return `实时同步中（${status.watch_count || 0} 个目录）`;
      return status.message || (status.state === 'unavailable' ? '当前不可用' : '监听错误');
    },
    async retryDirectoryWatch(directoryID) {
      try {
        await RetryLibraryWatcherRoot(directoryID);
      } finally {
        await this.reloadWatcherStatus();
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
      if (!await confirmAction({ title: '删除扫描目录', message: '确定要删除此目录配置吗？该目录下的视频记录会保留但从片库列表隐藏（可在「路径失效」视图查看），磁盘文件不受影响。把同一路径再加回来时数据会自动恢复。', confirmText: '删除', danger: true })) return;
      try {
        await DeleteDirectory(id);
        await this.refreshDirectories();
      } catch (err) {}
    },
    async refreshDirectories() {
      try {
        this.localDirectories = await GetAllDirectories();
        await this.reloadWatcherStatus();
        this.$emit('directories-changed', this.localDirectories);
      } catch (err) {}
    },
    closeDirectoryDialog() {
      this.showAddDirectoryDialog = false;
      this.editingDirectory = null;
      this.directoryForm = { path: '', alias: '' };
    },
    async loadImageDirectories() {
      try {
        this.localImageDirectories = await GetAllImageDirectories() || [];
      } catch (err) {
        this.localImageDirectories = [];
      }
    },
    async selectDirectoryForImageConfig() {
      try {
        const dir = await SelectDirectory();
        if (dir) this.imageDirectoryForm.path = dir;
      } catch (err) {}
    },
    editImageDirectory(dir) {
      this.editingImageDirectory = dir;
      this.imageDirectoryForm = { path: dir.path, alias: dir.alias };
    },
    async saveImageDirectoryConfig() {
      if (!this.imageDirectoryForm.path) return;
      try {
        if (this.editingImageDirectory) {
          await UpdateImageDirectory(this.editingImageDirectory.id, this.imageDirectoryForm.path, this.imageDirectoryForm.alias);
        } else {
          await AddImageDirectory(this.imageDirectoryForm.path, this.imageDirectoryForm.alias);
        }
        await this.loadImageDirectories();
        this.closeImageDirectoryDialog();
      } catch (err) {}
    },
    async deleteImageDirectoryItem(id) {
      if (!await confirmAction({ title: '删除图片目录', message: '确定要删除此图片目录配置吗？该目录下的图片记录会保留但从图库隐藏，磁盘文件不受影响。把同一路径再加回来时数据会自动恢复。', confirmText: '删除', danger: true })) return;
      try {
        await DeleteImageDirectory(id);
        await this.loadImageDirectories();
      } catch (err) {}
    },
    closeImageDirectoryDialog() {
      this.showAddImageDirectoryDialog = false;
      this.editingImageDirectory = null;
      this.imageDirectoryForm = { path: '', alias: '' };
    },
  }
};
</script>

<style scoped>
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
  background: var(--surface-faint);
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

.directory-watch-status {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 7px;
}

.directory-watch-status::before {
  width: 7px;
  height: 7px;
  flex: 0 0 7px;
  border-radius: 50%;
  background: var(--text-secondary);
  content: '';
}

.directory-watch-status--watching::before {
  background: var(--success-color);
}

.directory-watch-status--error::before,
.directory-watch-status--unavailable::before {
  background: var(--danger-color);
}

.directory-watch-status .btn-link {
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--accent-color);
  cursor: pointer;
  font-size: 12px;
}

.directory-actions {
  display: flex;
  gap: 8px;
}

.directory-dialog-row .text-input {
  flex: 1;
}

.directory-alias-input {
  margin-top: 8px;
}


/* 窄屏下目录行改成上下堆叠（规则随模板从 SettingsPage 搬过来）。 */
@media (max-width: 980px) {
  .directory-item {
    align-items: stretch;
    flex-direction: column;
  }

  .directory-actions {
    justify-content: flex-end;
  }
}
</style>
