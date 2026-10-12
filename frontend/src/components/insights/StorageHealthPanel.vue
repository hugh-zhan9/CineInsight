<template>
  <section class="storage-health" data-test="storage-health">
    <!-- 删除走片库同一个删除宿主：进度与取消、磁盘不支持废纸篓时的二选一、按批次撤销。 -->
    <TrashUndoBanner ref="trashUndo" kind="video" :after-restore="afterTrashRestore" />

    <p v-if="error" class="storage-alert" role="alert" data-test="storage-error">{{ error }}</p>
    <div v-if="loading && !report" class="storage-empty" data-test="storage-loading">正在体检…</div>

    <template v-else-if="report">
      <div class="storage-tiles" data-test="storage-tiles">
        <StatTile
          v-for="tile in tiles"
          :key="tile.key"
          size="sm"
          :tone="tile.tone"
          :icon="tile.icon"
          :value="tile.value"
          :label="tile.label"
          :note="tile.note"
          :data-test="`storage-tile-${tile.key}`"
        />
      </div>

      <section class="storage-section" data-test="storage-reclaim">
        <header class="storage-section__head">
          <h3>可回收空间</h3>
          <small>来自清理中心最近一次分析。精确重复可以放心删；其余类别要到清理中心逐组确认</small>
        </header>
        <div class="storage-reclaim">
          <article v-for="media in reclaimMedia" :key="media.key" class="storage-panel" :data-test="`storage-reclaim-${media.key}`">
            <div class="storage-panel__head">
              <strong>{{ media.title }}</strong>
              <span :class="['storage-chip', `storage-chip--${media.tone}`]">{{ media.stateText }}</span>
              <span class="storage-spacer"></span>
              <button
                type="button"
                class="btn-secondary btn-compact"
                :disabled="processing"
                :data-test="`storage-open-${media.key}-cleanup`"
                @click="$emit('run-command', media.command, media.commandLabel)"
              >{{ media.commandLabel }}</button>
            </div>
            <ul v-if="media.rows.length" class="storage-reclaim__rows">
              <li v-for="row in media.rows" :key="row.key" data-test="storage-reclaim-row">
                <span class="storage-reclaim__label">{{ row.label }}</span>
                <span :class="['storage-chip', row.certain ? 'storage-chip--safe' : 'storage-chip--review']">{{ row.certain ? '确定' : '待确认' }}</span>
                <small>{{ row.count }} {{ media.unit }}</small>
                <strong>{{ formatBytes(row.bytes) }}</strong>
              </li>
            </ul>
            <p v-else class="storage-muted">{{ media.emptyText }}</p>
          </article>
        </div>
      </section>

      <section class="storage-section" data-test="storage-unwatched">
        <header class="storage-section__head">
          <h3>久未观看的大文件</h3>
          <small>从没播放过的按入库时间算；收藏和评分过的会标出来</small>
          <span class="storage-spacer"></span>
          <label class="storage-filter">
            至少
            <select class="select-input" :value="minGiB" :disabled="processing" data-test="storage-unwatched-size" @change="setMinGiB($event.target.value)">
              <option v-for="value in SIZE_OPTIONS" :key="value" :value="value">{{ value }} GB</option>
            </select>
          </label>
          <label class="storage-filter">
            超过
            <select class="select-input" :value="months" :disabled="processing" data-test="storage-unwatched-months" @change="setMonths($event.target.value)">
              <option v-for="value in MONTH_OPTIONS" :key="value" :value="value">{{ value }} 个月</option>
            </select>
            没看
          </label>
        </header>
        <div class="storage-panel">
          <div v-if="unwatchedItems.length" class="storage-list__bar">
            <label class="storage-check">
              <input
                type="checkbox"
                :checked="allListedSelected"
                :indeterminate.prop="selection.length > 0 && !allListedSelected"
                :disabled="selectionLocked"
                data-test="storage-select-all"
                @change="toggleAllListed"
              />
              全选已列出的 {{ unwatchedItems.length }} 部
            </label>
            <span class="storage-muted">共 {{ report.unwatched.count }} 部 · {{ formatBytes(report.unwatched.bytes) }}</span>
            <span class="storage-spacer"></span>
            <span v-if="selection.length" class="storage-list__picked" data-test="storage-selected-summary">已选 {{ selection.length }} 部 · {{ formatBytes(selectedBytes) }}</span>
            <button
              type="button"
              class="btn-danger btn-compact"
              :disabled="selection.length === 0 || selectionLocked"
              :title="loading && !processing ? '正在重新取数，完成后再删除' : ''"
              data-test="storage-delete-selected"
              @click="askDelete"
            >{{ processing ? '处理中…' : `移到废纸篓 (${selection.length})` }}</button>
          </div>
          <ul v-if="unwatchedItems.length" class="storage-list">
            <li
              v-for="item in unwatchedItems"
              :key="item.id"
              :class="['storage-row', { 'storage-row--marked': selectedIDs.has(item.id) }]"
              data-test="storage-unwatched-row"
            >
              <input
                type="checkbox"
                class="storage-row__check"
                :checked="selectedIDs.has(item.id)"
                :disabled="selectionLocked"
                :aria-label="`选择 ${item.title}`"
                data-test="storage-unwatched-toggle"
                @change="toggleSelection(item.id)"
              />
              <img class="storage-row__thumb" :src="`/preview/thumbnail/${item.id}`" alt="" loading="lazy" />
              <div class="storage-row__main">
                <button type="button" class="storage-row__title" :title="item.title" :disabled="processing" @click="$emit('open-video-id', item.id)">{{ item.title }}</button>
                <small class="storage-row__meta">{{ itemFacts(item) }} · {{ watchedText(item) }}</small>
                <small class="storage-row__path" :title="item.path">{{ item.path }}</small>
              </div>
              <span v-if="item.is_favorite" class="storage-chip storage-chip--keep">★ 收藏</span>
              <span v-if="item.personal_rating != null" class="storage-chip storage-chip--keep">评分 {{ item.personal_rating }}</span>
              <strong class="storage-row__size">{{ formatBytes(item.size) }}</strong>
              <button type="button" class="btn-secondary btn-compact" data-test="storage-reveal" @click="reveal(item)">打开目录</button>
            </li>
          </ul>
          <p v-else class="storage-muted" data-test="storage-unwatched-empty">没有至少 {{ minGiB }} GB、超过 {{ months }} 个月没看的视频。</p>
          <button
            v-if="canShowMore('unwatched')"
            type="button"
            class="btn-secondary btn-compact storage-more"
            :disabled="loading"
            data-test="storage-unwatched-more"
            @click="showMore('unwatched')"
          >显示更多（还有 {{ report.unwatched.count - unwatchedItems.length }} 部）</button>
          <p v-else-if="report.unwatched.has_more" class="storage-muted storage-more" data-test="storage-unwatched-capped">最多列出前 {{ MAX_LIST }} 部，可以调高门槛缩小范围。</p>
        </div>
      </section>

      <section class="storage-section" data-test="storage-transcode">
        <header class="storage-section__head">
          <h3>转码可省空间</h3>
          <small>老编码（H.264 等）转成 HEVC 的粗略估算：通常能省一半，但不低于该分辨率的常用码率（1080p 约 6 Mbps）。只是估算，这里不做转码</small>
        </header>
        <p v-if="unprobedCount > 0" class="storage-note" data-test="storage-probe-note">
          还有 {{ unprobedCount }} 部视频没有技术信息（还没探测或探测失败），不参与编码分布与转码估算。
          <button
            type="button"
            class="btn-secondary btn-compact"
            :disabled="probeStarting"
            data-test="storage-start-probe"
            @click="startProbe"
          >{{ probeStarting ? '启动中…' : '补全技术信息' }}</button>
        </p>
        <div class="storage-panel">
          <ul v-if="transcodeItems.length" class="storage-list">
            <li v-for="item in transcodeItems" :key="item.id" class="storage-row" data-test="storage-transcode-row">
              <img class="storage-row__thumb" :src="`/preview/thumbnail/${item.id}`" alt="" loading="lazy" />
              <div class="storage-row__main">
                <button type="button" class="storage-row__title" :title="item.title" :disabled="processing" @click="$emit('open-video-id', item.id)">{{ item.title }}</button>
                <small class="storage-row__meta">{{ itemFacts(item) }} · {{ formatBitRate(item.bit_rate) }}</small>
                <small class="storage-row__path" :title="item.path">{{ item.path }}</small>
              </div>
              <span class="storage-row__estimate">{{ formatBytes(item.size) }} → ≈ {{ formatBytes(item.estimated_size) }}</span>
              <span class="storage-chip storage-chip--safe">可省 {{ formatBytes(item.estimated_savings) }}</span>
              <button type="button" class="btn-secondary btn-compact" @click="reveal(item)">打开目录</button>
            </li>
          </ul>
          <p v-else class="storage-muted" data-test="storage-transcode-empty">没有转码能明显省空间的视频。</p>
          <button
            v-if="canShowMore('transcode')"
            type="button"
            class="btn-secondary btn-compact storage-more"
            :disabled="loading"
            data-test="storage-transcode-more"
            @click="showMore('transcode')"
          >显示更多（还有 {{ report.transcode.count - transcodeItems.length }} 部）</button>
          <p v-else-if="report.transcode.has_more" class="storage-muted storage-more" data-test="storage-transcode-capped">最多列出可省空间最多的前 {{ MAX_LIST }} 部。</p>
        </div>
      </section>

      <section class="storage-section">
        <header class="storage-section__head"><h3>视频占用分布</h3><small>按编码、分辨率、目录</small></header>
        <div class="storage-charts">
          <BucketChart title="按编码" :items="report.by_codec || []" value-key="bytes" :format-value="formatBytes" />
          <BucketChart title="按分辨率" :items="report.by_resolution || []" value-key="bytes" :format-value="formatBytes" />
          <BucketChart title="按目录" :items="report.by_directory || []" value-key="bytes" :format-value="formatBytes" />
        </div>
      </section>
    </template>

    <BaseModal
      v-if="confirm.show"
      aria-labelledby="storage-delete-confirm-title"
      data-test="storage-delete-confirm-dialog"
      @close="answerDelete(false)"
    >
      <h2 id="storage-delete-confirm-title">移到废纸篓</h2>
      <p data-test="storage-delete-summary">将把 {{ confirm.ids.length }} 个视频移到废纸篓，共 {{ formatBytes(confirm.bytes) }}。</p>
      <p v-if="confirm.curated > 0" class="storage-confirm__warn" data-test="storage-delete-curated">其中 {{ confirm.curated }} 个已收藏或评过分，请确认确实不要了。</p>
      <p class="storage-confirm__help">移到废纸篓后可在回收站撤销；在访达清空废纸篓才会释放空间。所在磁盘不支持废纸篓时，会先问你怎么处理。</p>
      <div class="modal-actions">
        <button type="button" class="btn-secondary" data-test="storage-delete-cancel" @click="answerDelete(false)">取消</button>
        <button type="button" class="btn-danger" data-test="storage-delete-confirm" @click="answerDelete(true)">移到废纸篓</button>
      </div>
    </BaseModal>
  </section>
</template>

<script>
import { markRaw } from 'vue';
import { GetCleanupStatus, GetImageCleanupStatus, GetStorageReport, OpenDirectory, StartTechnicalBackfill } from '../../../wailsjs/go/main/App';
import BucketChart from './BucketChart.vue';
import BaseModal from '../ui/BaseModal.vue';
import StatTile from '../ui/StatTile.vue';
import TrashUndoBanner from '../video-list/TrashUndoBanner.vue';
import { summarizeTrashFailures } from '../TrashCenterDialog.vue';
import { formatBytes } from '../../utils/mediaDetails.js';
import { reclaimHeadline, reclaimSummary } from '../../utils/storageReclaim.js';

const GIB = 1024 ** 3;
const PAGE = 50;
// 与后端 storageMaxListLimit 一致：每个清单最多列这么多项，再多就不给「显示更多」了。
const MAX_LIST = 500;
const SIZE_OPTIONS = [1, 2, 5, 10, 20];
const MONTH_OPTIONS = [6, 12, 24, 36];
const ICONS = {
  total: ['M4 7c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3z', 'M4 7v10c0 1.7 3.6 3 8 3s8-1.3 8-3V7', 'M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3'],
  reclaim: ['M4 7h16', 'M9.5 7V4.5h5V7', 'M6.5 7l1 13h9l1-13', 'M10.5 11v5.5', 'M13.5 11v5.5'],
  unwatched: ['M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17z', 'M12 7.5V12l3 2'],
  transcode: ['M4 8h12l-3-3', 'M20 16H8l3 3']
};
const STATE_TEXT = {
  ready: ['已分析', 'safe'],
  stale: ['结果可能已过期', 'review'],
  running: ['分析中', 'review'],
  cancelled: ['上次分析已取消', 'muted'],
  missing: ['还没分析过', 'muted']
};

export default {
  name: 'StorageHealthPanel',
  components: { BucketChart, BaseModal, StatTile, TrashUndoBanner },
  // open-video-id：打开片库详情；run-command：按固定命令 ID 打开清理中心；
  // videos-changed：这里删除或撤销删除了视频，片库与顶栏计数据此刷新；
  // status-change：{ loading, processing }，宿主据此显示刷新状态、删除进行中不让离开。
  emits: ['open-video-id', 'run-command', 'videos-changed', 'status-change'],
  data() {
    return {
      SIZE_OPTIONS,
      MONTH_OPTIONS,
      MAX_LIST,
      loading: false,
      error: '',
      report: null,
      videoCleanup: null,
      imageCleanup: null,
      minGiB: 2,
      months: 12,
      limits: { unwatched: PAGE, transcode: PAGE },
      selection: [],
      // 本次在这里移到废纸篓的视频：清理分析结果要重新分析才会去掉它们，「可回收」先自己扣掉。
      removedIDs: [],
      processing: false,
      // 撤销条正在整批撤销（TrashUndoBanner.undoing）：和删除一样不能切走，否则恢复结果传不回片库。
      undoing: false,
      cleanupToken: 0,
      probeStarting: false,
      confirm: { show: false, ids: [], bytes: 0, curated: 0 },
      loadToken: 0
    };
  },
  computed: {
    unwatchedItems() { return this.report?.unwatched?.items || []; },
    transcodeItems() { return this.report?.transcode?.items || []; },
    selectedIDs() { return new Set(this.selection); },
    // 取数中也不能改勾选或删除：取回来的清单可能已经不含勾着的行（审查 I-2）；撤销进行中同理，
    // 撤销收尾会清掉撤销条，新的删除就丢了自己的撤销入口。
    selectionLocked() { return this.processing || this.loading || this.undoing; },
    itemsByID() { return new Map(this.unwatchedItems.map(item => [item.id, item])); },
    selectedBytes() { return this.selection.reduce((sum, id) => sum + Number(this.itemsByID.get(id)?.size || 0), 0); },
    allListedSelected() { return this.unwatchedItems.length > 0 && this.unwatchedItems.every(item => this.selectedIDs.has(item.id)); },
    unprobedCount() { return Math.max(0, Number(this.report?.probe?.total || 0) - Number(this.report?.probe?.probed || 0)); },
    reclaim() { return reclaimSummary(this.videoCleanup, this.imageCleanup, { excludedVideoIDs: this.removedIDs }); },
    reclaimMedia() {
      const describe = (key, media, title, unit, command, commandLabel) => {
        const [stateText, tone] = STATE_TEXT[media.state] || STATE_TEXT.missing;
        const analyzed = media.state === 'ready' || media.state === 'stale';
        return {
          key, title, unit, command, commandLabel, stateText, tone, rows: media.rows,
          emptyText: analyzed ? '这次分析没有发现可回收的文件。' : '打开清理中心会自动开始分析，分析完回来刷新就能看到。'
        };
      };
      return [
        describe('video', this.reclaim.video, '视频', '个', 'library.openCleanup', '打开视频清理中心'),
        describe('image', this.reclaim.image, '图片', '张', 'photos.openCleanup', '打开图片清理审阅')
      ];
    },
    tiles() {
      const report = this.report;
      const reclaim = this.reclaim;
      return [
        {
          key: 'total', tone: 'accent', icon: ICONS.total, label: '总占用',
          value: formatBytes(Number(report.videos.bytes || 0) + Number(report.images.bytes || 0)),
          note: `视频 ${report.videos.count} 部 · 图片 ${report.images.count} 张`
        },
        { key: 'reclaim', tone: 'danger', icon: ICONS.reclaim, label: '可回收', ...reclaimHeadline(reclaim) },
        {
          key: 'unwatched', tone: 'warning', icon: ICONS.unwatched, label: '久未观看',
          value: formatBytes(report.unwatched.bytes),
          note: `${report.unwatched.count} 部 · ≥${this.minGiB} GB、${this.months} 个月没看`
        },
        {
          key: 'transcode', tone: 'accent', icon: ICONS.transcode, label: '转码可省',
          value: `≈ ${formatBytes(report.transcode.savings)}`,
          note: `${report.transcode.count} 部老编码视频`
        }
      ];
    }
  },
  watch: {
    loading() { this.emitStatus(); },
    processing() { this.emitStatus(); },
    undoing() { this.emitStatus(); }
  },
  mounted() {
    // 撤销条常驻在本面板里，挂载后就有：跟着它的撤销状态走。
    this.$watch(() => Boolean(this.$refs.trashUndo?.undoing), value => { this.undoing = value; });
    this.refresh();
  },
  beforeUnmount() {
    if (this._resolveDelete) this._resolveDelete(false);
  },
  methods: {
    formatBytes,
    emitStatus() {
      this.$emit('status-change', { loading: this.loading, processing: this.processing || this.undoing });
    },
    // 打开、点「刷新」、删除或撤销之后：体检数据与两份清理结果一起重取。
    refresh() {
      return Promise.all([this.load(), this.loadCleanup()]);
    },
    // 清理分析结果可能很大（整组视频对象），只在需要时取，也不做深层响应。
    async loadCleanup() {
      const token = ++this.cleanupToken;
      try {
        const [videoCleanup, imageCleanup] = await Promise.all([GetCleanupStatus(), GetImageCleanupStatus()]);
        if (token !== this.cleanupToken) return;
        this.videoCleanup = videoCleanup ? markRaw(videoCleanup) : null;
        this.imageCleanup = imageCleanup ? markRaw(imageCleanup) : null;
      } catch (err) {
        if (token === this.cleanupToken) this.error = `读取清理分析结果失败：${err}`;
      }
    },
    // 换门槛、显示更多只重取体检数据；序号丢掉过期响应（连续改门槛时）。
    async load() {
      const token = ++this.loadToken;
      this.loading = true;
      this.error = '';
      try {
        const report = await GetStorageReport({
          unwatched_min_bytes: this.minGiB * GIB,
          unwatched_months: this.months,
          unwatched_limit: this.limits.unwatched,
          transcode_limit: this.limits.transcode
        });
        if (token !== this.loadToken) return;
        this.report = report;
        // 勾选只留在当前清单里看得到的：换了门槛后看不到的不能悄悄跟着删掉。
        const listed = new Set((report?.unwatched?.items || []).map(item => item.id));
        this.selection = this.selection.filter(id => listed.has(id));
      } catch (err) {
        if (token === this.loadToken) this.error = `读取存储体检失败：${err}`;
      } finally {
        if (token === this.loadToken) this.loading = false;
      }
    },
    setMinGiB(value) {
      this.minGiB = Number(value);
      this.limits = { ...this.limits, unwatched: PAGE };
      this.load();
    },
    setMonths(value) {
      this.months = Number(value);
      this.limits = { ...this.limits, unwatched: PAGE };
      this.load();
    },
    canShowMore(list) {
      const data = this.report?.[list];
      return Boolean(data?.has_more) && (data.items || []).length < MAX_LIST;
    },
    showMore(list) {
      this.limits = { ...this.limits, [list]: this.limits[list] + PAGE };
      this.load();
    },
    toggleSelection(id) {
      if (this.selectionLocked) return;
      this.selection = this.selectedIDs.has(id) ? this.selection.filter(item => item !== id) : [...this.selection, id];
    },
    toggleAllListed() {
      if (this.selectionLocked) return;
      this.selection = this.allListedSelected ? [] : this.unwatchedItems.map(item => item.id);
    },
    itemFacts(item) {
      const resolution = item.width && item.height ? `${item.width}×${item.height}` : '分辨率未知';
      return `${resolution} · ${item.codec || '编码未探测'}`;
    },
    formatBitRate(value) {
      const bps = Number(value || 0);
      return bps > 0 ? `${(bps / 1e6).toFixed(1)} Mbps` : '码率未知';
    },
    // 最后观看取桌面播放、内嵌与 Jellyfin 进度、标已看与播放账本里最晚的一次（后端 storageLastWatched）。
    watchedText(item) {
      if (item.last_watched_at) return `上次观看 ${this.dateLabel(item.last_watched_at)}`;
      return `没看过 · ${this.dateLabel(item.added_at)} 入库`;
    },
    dateLabel(value) {
      const time = new Date(value);
      return Number.isFinite(time.getTime()) ? time.toLocaleDateString('zh-CN') : '时间未知';
    },
    async reveal(item) {
      try {
        await OpenDirectory(item.id);
      } catch (err) {
        this.error = `定位文件失败：${err}`;
      }
    },
    async startProbe() {
      this.probeStarting = true;
      try {
        await StartTechnicalBackfill();
      } catch (err) {
        this.error = `启动技术信息补全失败：${err}`;
      } finally {
        this.probeStarting = false;
      }
    },
    askDelete() {
      if (this.selectionLocked || this.selection.length === 0) return;
      const items = this.selection.map(id => this.itemsByID.get(id)).filter(Boolean);
      this.confirm = {
        show: true,
        ids: items.map(item => item.id),
        bytes: items.reduce((sum, item) => sum + Number(item.size || 0), 0),
        curated: items.filter(item => item.is_favorite || item.personal_rating != null).length
      };
      return new Promise(resolve => { this._resolveDelete = resolve; });
    },
    async answerDelete(confirmed) {
      const ids = this.confirm.ids;
      const resolve = this._resolveDelete;
      this._resolveDelete = null;
      this.confirm = { show: false, ids: [], bytes: 0, curated: 0 };
      // 确认期间清单或勾选变了（例如取数完成、看不到的勾选被放掉）：按确认框里的名单删就可能删到
      // 看不到的行，中止，让用户重新确认（与图片清理页 deletionPlanUnchanged 同一做法）。
      const stillListed = ids.every(id => this.itemsByID.has(id) && this.selectedIDs.has(id));
      if (confirmed && (this.loading || !stillListed)) {
        this.error = '确认期间清单或勾选有变化，请重新确认。这次没有删除任何视频。';
        if (resolve) resolve(false);
        return;
      }
      if (confirmed) await this.deleteIDs(ids);
      if (resolve) resolve(confirmed);
    },
    async deleteIDs(ids) {
      if (!ids.length || this.processing) return;
      this.processing = true;
      this.error = '';
      let outcome = null;
      let failureNotice = '';
      try {
        const names = Object.fromEntries(ids.map(id => [id, this.itemsByID.get(id)?.title || `视频 ${id}`]));
        const banner = this.$refs.trashUndo;
        outcome = await banner.runDelete({ ids, deleteFile: true, names });
        banner.showDeleteNotice(outcome, { reportFailures: false });
        if (outcome.failures.length) {
          failureNotice = `有 ${outcome.failures.length} 个视频没有移到废纸篓：${summarizeTrashFailures(outcome.failures)}`;
          this.error = failureNotice;
        }
      } catch (err) {
        this.error = `移到废纸篓失败：${err}`;
      } finally {
        this.processing = false;
      }
      const removed = (outcome?.removedIDs || []).map(Number);
      if (!removed.length) return;
      const gone = new Set(removed);
      this.selection = this.selection.filter(id => !gone.has(id));
      this.removedIDs = [...new Set([...this.removedIDs, ...removed])];
      this.$emit('videos-changed', { removedIDs: removed });
      await this.refresh();
      // 重取会清空提示：部分失败的原因要留给用户看（撤销条只报成功的那部分）。
      if (failureNotice) this.error = failureNotice;
    },
    // 撤销条整批撤销、或在它打开的回收站里恢复之后：视频回来了，体检与片库都重取。
    // 拿不到具体 id 时整体放掉「本次已删」的扣减，宁可少扣也不要长期扣错。
    async afterTrashRestore(videoIDs) {
      const restored = new Set((videoIDs || []).map(Number));
      this.removedIDs = restored.size ? this.removedIDs.filter(id => !restored.has(id)) : [];
      this.$emit('videos-changed', { removedIDs: [] });
      await this.refresh();
    }
  }
};
</script>

<style scoped>
.storage-health { display: grid; gap: 22px; }
.storage-alert { margin: 0; padding: 9px 14px; border: 1px solid var(--danger-color); border-radius: 10px; color: var(--danger-color); font-size: 12px; }
.storage-empty { padding: 48px 8px; color: var(--text-secondary); text-align: center; }
.storage-muted { margin: 0; color: var(--text-muted); font-size: 12.5px; }
.storage-spacer { flex: 1; }
.storage-note { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; margin: 0; padding: 9px 14px; border: 1px solid var(--border-color); border-radius: 10px; background: var(--control-bg); color: var(--text-secondary); font-size: 12px; }

/* 统计卡本身是共用的 StatTile（D-UI-08），这里只排网格。 */
.storage-tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: 14px; }

.storage-section { display: grid; gap: 12px; }
.storage-section__head { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 12px; }
.storage-section__head h3 { margin: 0; font-size: 17px; font-weight: 750; }
.storage-section__head small { color: var(--text-muted); font-size: 12px; }
.storage-filter { display: inline-flex; flex: none; align-items: center; gap: 6px; color: var(--text-secondary); font-size: 12px; white-space: nowrap; }
.storage-filter .select-input { width: auto; min-width: 88px; }

.storage-panel { display: grid; align-content: start; gap: 10px; min-width: 0; padding: 14px 16px; border: 1px solid var(--hairline); border-radius: var(--radius-lg); background: var(--panel-bg); }
.storage-panel__head { display: flex; align-items: center; gap: 10px; }
.storage-reclaim { display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 14px; }
.storage-reclaim__rows { display: grid; gap: 8px; margin: 0; padding: 0; list-style: none; }
.storage-reclaim__rows li { display: grid; grid-template-columns: minmax(0, 1fr) auto 72px 90px; align-items: center; gap: 10px; font-size: 13px; }
.storage-reclaim__rows small { color: var(--text-muted); text-align: right; font-variant-numeric: tabular-nums; }
.storage-reclaim__rows strong { text-align: right; font-variant-numeric: tabular-nums; }
.storage-reclaim__label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.storage-chip { padding: 1px 8px; border-radius: 999px; background: var(--control-bg); color: var(--text-secondary); font-size: 11px; white-space: nowrap; }
.storage-chip--safe { background: var(--accent-soft); color: var(--accent-text); }
.storage-chip--review { background: var(--warning-soft); color: var(--warning-text); }
.storage-chip--keep { border: 1px solid var(--accent-color); background: transparent; color: var(--accent-text); }
.storage-chip--muted { color: var(--text-muted); }

.storage-list__bar { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 14px; font-size: 12.5px; }
.storage-check { display: inline-flex; align-items: center; gap: 6px; color: var(--text-secondary); cursor: pointer; }
.storage-list__picked { color: var(--text-primary); font-weight: 600; }
.storage-list { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
.storage-row { display: flex; align-items: center; gap: 12px; padding: 8px 10px; border: 1px solid transparent; border-radius: 10px; }
.storage-row:hover { background: var(--control-hover-bg); }
.storage-row--marked { border-color: var(--danger-color); }
.storage-row__check { flex: none; }
.storage-row__thumb { flex: none; width: 96px; height: 54px; border-radius: 6px; background: var(--thumb-bg); object-fit: cover; }
.storage-row__main { display: grid; flex: 1; gap: 2px; min-width: 0; }
.storage-row__title { overflow: hidden; padding: 0; border: 0; background: none; color: var(--text-primary); font: inherit; font-size: 13.5px; font-weight: 600; text-align: left; text-overflow: ellipsis; white-space: nowrap; cursor: pointer; }
.storage-row__title:hover { color: var(--accent-text); }
.storage-row__meta { color: var(--text-secondary); font-size: 12px; }
.storage-row__path { overflow: hidden; color: var(--text-muted); font-size: 11px; font-family: var(--font-mono, monospace); text-overflow: ellipsis; white-space: nowrap; }
.storage-row__size { flex: none; min-width: 72px; text-align: right; font-variant-numeric: tabular-nums; }
.storage-row__estimate { flex: none; color: var(--text-secondary); font-size: 12px; font-variant-numeric: tabular-nums; }
.storage-more { justify-self: center; }

.storage-charts { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 14px; }

.storage-confirm__warn { color: var(--warning-text); }
.storage-confirm__help { margin: 6px 0 0; color: var(--text-muted); font-size: 12px; }
</style>
