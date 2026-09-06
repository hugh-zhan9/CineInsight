<template>
  <!-- 自动化设置 -->
  <div :id="`settings-automation`" class="settings-section">
    <h3>自动化与扫描</h3>
    <div class="setting-item">
      <label class="switch">
        <input type="checkbox" v-model="form.auto_scan_on_startup" />
        <span class="slider"></span>
        <span>启动时自动增量扫描</span>
      </label>
      <p class="help-text">开启后，每次启动应用都会自动同步磁盘文件变动并补全元数据。</p>
    </div>
    <div class="setting-item">
      <label>点「播放」时的续播口径</label>
      <select v-model="form.playback_resume_mode" class="select-input" data-test="playback-resume-mode">
        <option value="resume">接着上次的位置播（交给播放器决定）</option>
        <option value="restart">总是从头播</option>
        <option value="restart_watched">已看的从头播，没看完的接着播</option>
      </select>
      <p class="help-text">
        断点是 IINA 自己记的，它默认会接着上次播。选「从头播」时应用会用 iina-cli 启动并显式关掉续播，
        断点记录仍然保留（应用内的进度条不受影响）。没装 IINA 时用系统默认播放器打开，续播与否由那个播放器决定。
      </p>
    </div>

    <!-- 扫描完成后接着跑的杂活：默认全关，这些都吃 CPU/IO，什么时候跑由用户决定。 -->
    <div class="setting-item">
      <label class="switch">
        <input data-test="auto-technical-backfill-toggle" type="checkbox" v-model="form.auto_technical_backfill" />
        <span class="slider"></span>
        <span>扫描后自动补全技术元数据</span>
      </label>
      <p class="help-text">补全分辨率、时长这些从文件里读出来的信息；缺了它们筛选和清理判断都会失准。</p>
    </div>
    <div class="setting-item">
      <label class="switch">
        <input data-test="auto-perceptual-hash-toggle" type="checkbox" v-model="form.auto_perceptual_hash" />
        <span class="slider"></span>
        <span>扫描后自动补全感知哈希</span>
      </label>
      <p class="help-text">近似重复检测的前提。要抽帧计算，视频多时比较慢，建议在不用电脑时开着扫描。</p>
    </div>
    <div class="setting-item">
      <label class="switch">
        <input data-test="auto-cleanup-analysis-toggle" type="checkbox" v-model="form.auto_cleanup_analysis" />
        <span class="slider"></span>
        <span>扫描后自动重算清理候选</span>
      </label>
      <p class="help-text">扫描改变了片库就重跑一次清理分析，打开清理审阅时直接看到最新结果。</p>
    </div>
    <div class="setting-item">
      <label class="switch">
        <input data-test="auto-image-exif-toggle" type="checkbox" v-model="form.auto_image_exif_backfill" />
        <span class="slider"></span>
        <span>图片扫描后自动补全 EXIF / GPS</span>
      </label>
      <p class="help-text">读拍摄时间与定位信息，时间线分组和地点筛选靠它。</p>
    </div>
    <div class="setting-item">
      <label class="switch">
        <input data-test="auto-collection-suggestions-toggle" type="checkbox" v-model="form.auto_collection_suggestions" />
        <span class="slider"></span>
        <span>扫描后自动分析剧集，生成建议作品集</span>
      </label>
      <p class="help-text">按文件名认出同一部剧的多集，只生成候选放进「建议作品集」面板；作品集要你确认才会建，标题与文件名一个字都不会改。</p>
    </div>
    <div class="setting-item">
      <label class="switch">
        <input data-test="auto-frame-hash-sequence-toggle" type="checkbox" v-model="form.auto_frame_hash_sequence" />
        <span class="slider"></span>
        <span>扫描后自动补全帧哈希</span>
      </label>
      <p class="help-text">截取片段识别的前提：按 2 秒一帧算出整部片的画面指纹序列，清理中心才能认出「这一段是从那部片里剪出来的」。要逐帧抽整部片，是最吃 CPU 的一类任务，建议在不用电脑时开着扫描。</p>
    </div>
    <div class="setting-item">
      <label class="switch">
        <input data-test="auto-compatibility-proxy-toggle" type="checkbox" v-model="form.auto_compatibility_proxy" />
        <span class="slider"></span>
        <span>自动为不能内嵌播放、或手机端直连会卡的视频生成播放代理</span>
      </label>
      <p class="help-text">扫描后对本次新增的 mkv、avi 这类视频，以及长边超过 1920 或码率超过 8 Mbps 的视频生成一份 ≤1080p 的 mp4 代理；手机端实际播到这类视频而代理还没有时也会自动排队。源文件不会被改写或替换；被体积上限淘汰过的代理不会自动重建。上限与占用在「播放代理」分区。</p>
    </div>
    <div class="setting-item">
      <label class="switch">
        <input data-test="library-watch-toggle" type="checkbox" v-model="form.library_watch_enabled" />
        <span class="slider"></span>
        <span>实时同步片库</span>
      </label>
      <p class="help-text">监听可靠的本地或直连磁盘；网络盘仍使用启动或手动扫描。</p>
    </div>
    <div class="setting-item">
      <label class="switch">
        <input data-test="local-metadata-toggle" type="checkbox" v-model="form.local_metadata_enabled" />
        <span class="slider"></span>
        <span>本地元数据自动补全</span>
      </label>
      <p class="help-text">控制新视频自动填空与后台补全任务；详情页手工导入仍可使用。</p>
    </div>
    <div class="setting-item">
      <label class="switch">
        <input data-test="ai-quality-toggle" type="checkbox" v-model="form.ai_quality_enabled" />
        <span class="slider"></span>
        <span>显示 AI 质量评估</span>
      </label>
      <p class="help-text">只控制质量视图入口；已有归因和审核记录不会删除。</p>
    </div>
    <div class="setting-item scan-blacklist-setting">
      <div class="settings-section-heading">
        <label>扫描目录黑名单</label>
        <button type="button" class="btn-secondary btn-compact" @click="addScanExcludeDirectory">选择目录</button>
      </div>
      <div v-if="scanExcludePaths.length" class="scan-blacklist-list">
        <div v-for="path in scanExcludePaths" :key="path" class="scan-blacklist-item">
          <span :title="path">{{ path }}</span>
          <button type="button" class="btn-secondary btn-compact btn-danger-outline" @click="removeScanExcludeDirectory(path)">移除</button>
        </div>
      </div>
      <p v-else class="help-text">尚未设置黑名单目录。</p>
      <p class="help-text">黑名单目录及其全部子目录不会被后续扫描收录；已有视频记录不会自动删除。</p>
    </div>
    <div class="setting-item scan-blacklist-setting">
      <div class="settings-section-heading">
        <label>图片扫描黑名单</label>
        <button type="button" class="btn-secondary btn-compact" @click="addImageScanExcludeDirectory">选择目录</button>
      </div>
      <div v-if="imageScanExcludePaths.length" class="scan-blacklist-list">
        <div v-for="path in imageScanExcludePaths" :key="path" class="scan-blacklist-item">
          <span :title="path">{{ path }}</span>
          <button type="button" class="btn-secondary btn-compact btn-danger-outline" @click="removeImageScanExcludeDirectory(path)">移除</button>
        </div>
      </div>
      <p v-else class="help-text">未单独设置，沿用上方扫描目录黑名单。</p>
      <p class="help-text">图片扫描专用黑名单；设置后仅对图片扫描生效，与视频扫描互不影响。</p>
    </div>
  </div>
</template>

<script>
import { SelectDirectory } from '../../../wailsjs/go/main/App';

// 自动化与扫描分区。两份扫描黑名单以换行分隔存在设置字段里，这里带着它们的
// 增删逻辑一起搬过来；选目录失败的提示交回页面顶部的保存状态栏。
export default {
  name: 'AutomationSection',
  props: {
    form: { type: Object, required: true }
  },
  emits: ['error'],
  computed: {
    scanExcludePaths() {
      return [...new Set(String(this.form.scan_exclude_paths || '').split(/\r?\n/).map(path => path.trim()).filter(Boolean))];
    },
    imageScanExcludePaths() {
      return [...new Set(String(this.form.image_scan_exclude_paths || '').split(/\r?\n/).map(path => path.trim()).filter(Boolean))];
    }
  },
  methods: {
    async addScanExcludeDirectory() {
      try {
        const path = await SelectDirectory();
        if (!path) return;
        this.form.scan_exclude_paths = [...new Set([...this.scanExcludePaths, path])].join('\n');
      } catch (err) {
        this.$emit('error', '选择黑名单目录失败：' + err);
      }
    },
    removeScanExcludeDirectory(path) {
      this.form.scan_exclude_paths = this.scanExcludePaths.filter(item => item !== path).join('\n');
    },
    async addImageScanExcludeDirectory() {
      try {
        const path = await SelectDirectory();
        if (!path) return;
        this.form.image_scan_exclude_paths = [...new Set([...this.imageScanExcludePaths, path])].join('\n');
      } catch (err) {
        this.$emit('error', '选择图片黑名单目录失败：' + err);
      }
    },
    removeImageScanExcludeDirectory(path) {
      this.form.image_scan_exclude_paths = this.imageScanExcludePaths.filter(item => item !== path).join('\n');
    }
  }
};
</script>

<style scoped>
.scan-blacklist-setting { margin-top: 18px; padding-top: 16px; border-top: 1px solid var(--border-color); }.scan-blacklist-list { display: grid; gap: 7px; margin-top: 10px; }.scan-blacklist-item { display: flex; align-items: center; gap: 8px; padding: 7px 8px; border: 1px solid var(--border-color); border-radius: 8px; background: var(--control-bg); }.scan-blacklist-item span { min-width: 0; flex: 1; overflow: hidden; color: var(--text-secondary); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }

</style>
