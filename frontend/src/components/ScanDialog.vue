<template>
  <div v-if="visible" class="modal-overlay" @click="$emit('close')">
    <div class="modal" @click.stop>
      <h2>扫描视频目录</h2>
      <div class="form-group">
        <button @click="selectDir" class="btn-primary">选择目录</button>
        <p v-if="scanDirectory" class="selected-dir">{{ scanDirectory }}</p>
      </div>

      <div v-if="scanProgress.scanning" class="scan-progress">
        <p>正在扫描... 已发现 {{ scanProgress.found }} 个视频</p>
        <p>正在处理 {{ scanProgress.processed }}/{{ scanProgress.total }}</p>
        <p>新增 {{ scanProgress.imported }} 个，删除 {{ scanProgress.deleted }} 个，跳过 {{ scanProgress.skipped }} 个</p>
      </div>
      <div v-if="!scanProgress.scanning && scanProgress.statusMessage" class="scan-result" style="margin-top: 15px; color: #4caf50; font-weight: bold;">
        <p>{{ scanProgress.statusMessage }}</p>
      </div>
      <div class="modal-actions">
        <button @click="startScan" :disabled="!scanDirectory || scanProgress.scanning" class="btn-primary">
          {{ scanProgress.statusMessage ? '重新扫描' : '开始扫描' }}
        </button>
        <button @click="$emit('close')" class="btn-secondary">
          {{ scanProgress.statusMessage ? '关闭' : '取消' }}
        </button>
      </div>
    </div>
  </div>
</template>

<script>
import { SelectDirectory, ScanDirectory, AddVideo, DeleteVideo, GetVideosByDirectory, AddDirectory } from '../../wailsjs/go/main/App';

export default {
  name: 'ScanDialog',
  props: {
    visible: { type: Boolean, default: false },
    directories: { type: Array, default: () => [] }
  },
  emits: ['close', 'scan-complete'],
  data() {
    return {
      scanDirectory: '',
      scanProgress: {
        scanning: false,
        found: 0,
        processed: 0,
        imported: 0,
        deleted: 0,
        skipped: 0,
        total: 0,
        statusMessage: ''
      }
    };
  },
  watch: {
    visible(val) {
      if (val) {
        this.scanDirectory = '';
        this.resetProgress();
      }
    }
  },
  methods: {
    resetProgress() {
      this.scanProgress = {
        scanning: false, found: 0, processed: 0,
        imported: 0, deleted: 0, skipped: 0, total: 0,
        statusMessage: ''
      };
    },
    async selectDir() {
      try {
        this.scanDirectory = await SelectDirectory();
      } catch (err) {
        console.error('选择目录失败:', err);
        alert('选择目录失败: ' + err);
      }
    },
    async startScan() {
      if (!this.scanDirectory) {
        alert('请先选择目录');
        return;
      }

      this.scanProgress.scanning = true;
      this.scanProgress.found = 0;
      this.scanProgress.processed = 0;
      this.scanProgress.imported = 0;
      this.scanProgress.deleted = 0;
      this.scanProgress.skipped = 0;
      this.scanProgress.total = 0;

      try {
        const files = await ScanDirectory(this.scanDirectory) || [];
        const existingVideos = await GetVideosByDirectory(this.scanDirectory) || [];
        const scannedSet = new Set(files);
        const keptByPath = new Map();
        const duplicateVideos = [];

        for (const video of existingVideos) {
          if (!keptByPath.has(video.path)) {
            keptByPath.set(video.path, video);
          } else {
            duplicateVideos.push(video);
          }
        }

        const duplicateIDSet = new Set(duplicateVideos.map(video => video.id));
        const staleVideos = existingVideos.filter(video => !duplicateIDSet.has(video.id) && !scannedSet.has(video.path));
        const toDelete = [...duplicateVideos, ...staleVideos];
        const toAdd = files.filter(file => !keptByPath.has(file));

        this.scanProgress.found = files.length;
        this.scanProgress.total = toAdd.length + toDelete.length;

        for (const file of toAdd) {
          try {
            await AddVideo(file);
            this.scanProgress.imported++;
          } catch (err) {
            this.scanProgress.skipped++;
          } finally {
            this.scanProgress.processed++;
            await this.flushProgress();
          }
        }

        for (const video of toDelete) {
          try {
            await DeleteVideo(video.id, false);
            this.scanProgress.deleted++;
          } catch (err) {
            this.scanProgress.skipped++;
          } finally {
            this.scanProgress.processed++;
            await this.flushProgress();
          }
        }

        // 自动加入目录配置
        const exists = (this.directories || []).some(d => d.path === this.scanDirectory);
        if (!exists) {
          const alias = this.scanDirectory.split(/[/\\]/).filter(Boolean).pop() || this.scanDirectory;
          try {
            await AddDirectory(this.scanDirectory, alias);
          } catch (err) {
            console.warn('保存扫描目录失败:', err);
            alert('保存扫描目录失败: ' + err);
          }
        }

        if (this.scanProgress.total === 0) {
          this.scanProgress.statusMessage = '扫描完成：未发现新视频或变动。';
        } else {
          this.scanProgress.statusMessage = `扫描完成：新增 ${this.scanProgress.imported} 个，删除 ${this.scanProgress.deleted} 个。`;
        }
        this.$emit('scan-complete');
        // 不自动关闭，让用户确认结果
      } catch (err) {
        this.scanProgress.statusMessage = '扫描失败: ' + err;
        console.error('扫描失败:', err);
      } finally {
        this.scanProgress.scanning = false;
      }
    },
    async flushProgress() {
      await this.$nextTick();
      await new Promise(resolve => setTimeout(resolve, 0));
    }
  }
};
</script>
