// 视频超分任务的展示口径（D-PC24、MEDIA-11）：状态、阶段与结束码的中文说法只此一处。
// 超分弹窗与任务中心（最近任务里的超分行）共用，免得两处各写一套、说法对不上。
//
// 码值与后端一一对应：状态见 models/enhancement.go 的 EnhancementStatus*，阶段见
// EnhancementPhase*，结束码见 services/enhancement_pipeline.go 的 enhancementErrorCode
// 与 enhancement_service.go / enhancement_workdir.go 里的三个保留码。

export const ENHANCEMENT_STATUS_LABELS = {
  queued: '排队中',
  running: '处理中',
  cancel_requested: '取消中',
  cancelled: '已取消',
  completed: '已完成',
  failed: '失败'
};

export const ENHANCEMENT_PHASE_LABELS = {
  preflight: '检查源文件',
  extract: '抽帧',
  enhance: '超分',
  encode: '编码',
  verify: '校验产物',
  publish: '入库'
};

// 结束码的原因说法。认不出来的码原样带出去（enhancementReasonText），不让一次失败消失。
export const ENHANCEMENT_ERROR_LABELS = {
  runtime_unavailable: '超分组件不可用',
  unsupported_input: '源文件格式不支持',
  output_conflict: '同名产物已存在',
  disk_insufficient: '磁盘空间不足',
  source_changed: '源文件在任务期间被改动',
  decode_failed: '源文件解码失败',
  inference_failed: '超分处理失败',
  encode_failed: '产物编码失败',
  verify_failed: '产物校验没通过',
  publish_failed: '产物入库失败',
  cancelled: '已取消',
  checkpoint_discarded: '保留的进度已放弃'
};

// 空间不足与用户取消会把工作目录与检查点留下来，重试从断点接着做（D-PC24）。
// 这两类才有「放弃保留的进度」可做；放弃之后结束码变成 checkpoint_discarded。
const RETAINED_PROGRESS_CODES = ['disk_insufficient', 'cancelled'];
const FINISHED_STATUSES = ['failed', 'cancelled'];

export function enhancementReasonText(code) {
  if (!code) return '';
  return ENHANCEMENT_ERROR_LABELS[code] || `未知原因（${code}）`;
}

export function enhancementRetainsProgress(task) {
  return FINISHED_STATUSES.includes(task?.status) && RETAINED_PROGRESS_CODES.includes(task?.error_code);
}

export function enhancementRetryable(task) {
  return FINISHED_STATUSES.includes(task?.status);
}

export function enhancementCancellable(task) {
  return task?.status === 'queued' || task?.status === 'running';
}

// 一行任务的状态文字：处理中带阶段与帧进度，失败带中文原因。
export function enhancementStatusText(task) {
  if (!task) return '';
  const status = task.status;
  const label = ENHANCEMENT_STATUS_LABELS[status] || status || '';
  if (status === 'running') {
    const phase = ENHANCEMENT_PHASE_LABELS[task.phase] || task.phase || '';
    const frames = Number(task.total_frames) > 0 ? ` ${Number(task.committed_frames) || 0}/${Number(task.total_frames)} 帧` : '';
    return phase ? `${label}（${phase}${frames}）` : label;
  }
  if (status === 'failed') {
    const reason = enhancementReasonText(task.error_code);
    return reason ? `${label}：${reason}` : label;
  }
  if (status === 'cancelled' && task.error_code === 'checkpoint_discarded') {
    return `${label}（保留的进度已放弃）`;
  }
  return label;
}

// 行下方的补充说明：后端的 error_summary 是给人读的一句话（已去掉路径），原样展示；
// 保留了进度的任务再说明重试会接着做。
export function enhancementDetailText(task) {
  if (!task) return '';
  const parts = [];
  const summary = String(task.error_summary || '').trim();
  if (summary && task.status !== 'completed') parts.push(summary);
  if (enhancementRetainsProgress(task)) parts.push('进度已保留，重试会从断点接着做');
  return parts.join('；');
}
