// 空闲调度（D-030..D-032）的展示口径：任务名与等待原因的中文说法只此一处，
// 设置页分区与片库页状态条共用，免得两处各写一套、说法对不上。

// 与后端 services.BackgroundTaskKeys() 的固定集合一一对应。
export const BACKGROUND_TASK_LABELS = {
  subtitle: '字幕生成',
  enhancement: '视频超分',
  proxy: '播放代理',
  face: '人脸分析',
  frame_hash: '帧哈希',
  phash: '近重复指纹',
  technical: '技术信息',
  local_metadata: '本地资料',
  semantic: '语义索引',
  image_semantic: '图片语义索引',
  ai_tagging: 'AI 打标',
  image_ai_tagging: '图片 AI 打标',
  exif: '图片 EXIF',
  cleanup: '清理分析',
  collection_suggest: '建议作品集',
  backup: '数据库备份'
};

const IDLE_WAIT_REASON_LABELS = {
  user_active: '你正在用电脑',
  on_battery: '当前使用电池',
  outside_window: '不在允许的时间段内',
  probe_failed: '空闲状态探测失败'
};

// IDLE_GATE_TASK_NOT_WAITING 与后端 services.IdleGateTaskNotWaitingCode 是同一个标识：
// 「立即运行」恰好落在任务刚被放行之后就会收到它，那不是失败，只是没什么可放行的了。
// 按标识判而不是匹配整句中文——文案改一个字就会静默失效。
export const IDLE_GATE_TASK_NOT_WAITING = 'idle_gate_task_not_waiting';

export function isIdleGateNotWaitingError(err) {
  return String(err?.message || err || '').includes(IDLE_GATE_TASK_NOT_WAITING);
}

export function idleWaitReasonLabel(reason) {
  return IDLE_WAIT_REASON_LABELS[reason] || reason || '等待空闲';
}

// gate 是各任务状态里的 {waiting_idle, reason}；没有这个字段就是没被门挡着。
export function idleGateWaitingText(gate) {
  if (!gate || !gate.waiting_idle) return '';
  return `等待空闲（${idleWaitReasonLabel(gate.reason)}）`;
}

// 与后端 services.NormalizeIdleThresholdMinutes 同口径：非正取默认 5，超过 120 收到 120。
export function normalizeIdleThresholdMinutes(value) {
  const minutes = Math.trunc(Number(value));
  if (!Number.isFinite(minutes) || minutes <= 0) return 5;
  if (minutes > 120) return 120;
  return minutes;
}

// 与后端 services.NormalizeIdleWindowBound 同口径：只认 HH:MM，其余归一成空。
export function normalizeIdleWindowBound(value) {
  const text = String(value ?? '').trim();
  if (!text) return '';
  const match = /^(\d{1,2}):(\d{1,2})$/.exec(text);
  if (!match) return '';
  const hour = Number(match[1]);
  const minute = Number(match[2]);
  if (hour > 23 || minute > 59) return '';
  return `${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`;
}
