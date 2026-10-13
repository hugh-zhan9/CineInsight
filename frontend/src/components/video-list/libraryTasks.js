// 片库管理「管理」菜单发起的后台任务（D-P3-05 收尾）：补全技术信息、补全近重复指纹、补全本地资料、当前筛选写出 NFO，
// 以及行菜单的单个视频写出 NFO。原来由片库页常驻的 BackgroundTaskStatusBars 承担，状态条收进任务中心后它删掉了，
// 剩下的两件事搬到这里：
// - 给「管理」菜单的进度文案（「补全技术信息（p/t）」「当前筛选写出 NFO（p/t）」……）供状态：挂载时读一次各任务的
//   Get*Status，之后跟着各自既有的运行时事件走（与任务中心同一批事件，不另起轮询）；
// - 本地资料补全跑完且有成功项时按当前条件重载片库（原来的 library-changed）。
// 运行中的进度、上一轮结果、跳过 / 失败明细、取消与「忽略空闲立即运行」都在任务中心；从这里发起一轮时给一条
// 带「查看」的提示（打开任务中心），页内不再有状态条。
//
// 宿主（VideoListPage）提供：registerRuntimeEvent(name, handler)、reloadCurrentView()、$emit('open-task-center')。
import {
  ExportLocalMetadataNFO, GetLocalMetadataBackfillStatus, GetLocalMetadataExportStatus, GetPerceptualHashBackfillStatus,
  GetTechnicalBackfillStatus, StartLocalMetadataBackfill, StartLocalMetadataExport, StartPerceptualHashBackfill, StartTechnicalBackfill
} from '../../../wailsjs/go/main/App';
import { confirmAction, notify, notifyError } from '../../utils/feedback.js';
import { logFrontend } from '../../utils/frontendLog.js';

const idleState = () => ({ running: false, preparing: false, cancelled: false, completed: false, total: 0, processed: 0, succeeded: 0, skipped: 0, failed: 0 });

// 每个任务：data 里的字段名、读状态（调用时才取绑定）、运行时事件、菜单上的说法（「已开始…」提示用）。
export const LIBRARY_TASKS = Object.freeze({
  technicalBackfill: { status: () => GetTechnicalBackfillStatus(), event: 'technical-backfill-state', label: '补全技术信息' },
  perceptualHash: { status: () => GetPerceptualHashBackfillStatus(), event: 'perceptual-hash-state', label: '补全近重复指纹' },
  localMetadataBackfill: { status: () => GetLocalMetadataBackfillStatus(), event: 'local-metadata-backfill', label: '补全本地资料' },
  localMetadataExport: { status: () => GetLocalMetadataExportStatus(), event: 'local-metadata-export', label: '写出 NFO' }
});

// 启动之后的提示：真的开始跑了说「已开始」并给「查看」（打开任务中心）；一开始就算完、总数为 0 的说「已是最新」。
export function taskStartNotice(label, status) {
  if (status?.running) return { text: `已开始${label}，进度与结果在任务中心。`, view: true };
  if (status?.completed && !Number(status?.total) && !Number(status?.failed)) return { text: `${label}：无需处理（已是最新状态）。`, view: false };
  return null;
}

export const libraryTaskStateMixin = {
  data() {
    return Object.fromEntries(Object.keys(LIBRARY_TASKS).map(key => [key, idleState()]));
  },
  mounted() {
    for (const [key, task] of Object.entries(LIBRARY_TASKS)) {
      this.refreshLibraryTaskStatus(key);
      this.registerRuntimeEvent(task.event, data => this.applyLibraryTaskEvent(key, data));
    }
  },
  methods: {
    async refreshLibraryTaskStatus(key) {
      try {
        this[key] = { ...this[key], ...(await LIBRARY_TASKS[key].status()) };
      } catch (err) {
        logFrontend('VideoListPage', `${key} status failed`, { err: String(err) }, true);
      }
    },
    applyLibraryTaskEvent(key, data) {
      this[key] = { ...this[key], ...(data || {}) };
      // 本地资料补全把资料写进了片库：跑完且有成功项时按当前条件重载（原状态条的 library-changed）。
      if (key === 'localMetadataBackfill' && data?.completed && data?.succeeded) this.reloadCurrentView();
    },
    // 启动一轮并把返回的状态并进来；成功给「已开始」提示，失败给中文错误（文案沿用原状态条）。
    async startLibraryTask(key, start, failureText) {
      try {
        const status = await start();
        this[key] = { ...this[key], ...(status || {}) };
        const notice = taskStartNotice(LIBRARY_TASKS[key].label, status);
        if (notice) notify(notice.text, notice.view ? { action: { label: '查看', run: () => this.$emit('open-task-center') } } : {});
      } catch (err) {
        notifyError(`${failureText}: ${err}`);
      }
    },
    startTechnicalBackfill() {
      return this.startLibraryTask('technicalBackfill', () => StartTechnicalBackfill(), '启动技术信息补全失败');
    },
    startPerceptualHashBackfill() {
      return this.startLibraryTask('perceptualHash', () => StartPerceptualHashBackfill(), '启动近重复指纹补全失败');
    },
    startLocalMetadataBackfill() {
      return this.startLibraryTask('localMetadataBackfill', () => StartLocalMetadataBackfill(), '启动本地资料补全失败');
    },
    // 当前筛选写出 NFO：先确认（文案沿用原状态条），筛选由宿主算好交进来。取消在任务中心（local_metadata_export）。
    async startLocalMetadataExport(filter) {
      if (!await confirmAction({ title: '写出 NFO', message: '将当前筛选结果逐个写出为同名 NFO；已有 NFO 会保留未知字段并合并应用管理字段。继续吗？', confirmText: '开始写出' })) return;
      await this.startLibraryTask('localMetadataExport', () => StartLocalMetadataExport({ filter }), '启动 NFO 写出失败');
    },
    // 行菜单与详情抽屉的「写出 NFO」：单个视频同步写出，结果直接提示。
    async exportLocalMetadataNFO(video) {
      if (!video?.id) return;
      try {
        const result = await ExportLocalMetadataNFO(video.id);
        const warning = Array.isArray(result?.warnings) && result.warnings.length ? `\n${result.warnings.join('\n')}` : '';
        notify(`NFO 已写出：${result?.nfo_path || video.name}${warning}`);
      } catch (err) {
        notifyError('写出 NFO 失败: ' + err);
      }
    }
  }
};
