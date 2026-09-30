// 播放代理逐项结果码的中文文案（D-006）。后端返回的是稳定的机器码，
// 界面上的说法集中在这里一份，详情抽屉、批量提示与设置页共用。
export const PLAYBACK_PROXY_CODE_LABELS = {
  created: '已生成',
  already_exists: '已有可用代理',
  // in_progress 只说明这一项已经在队列里或正在处理，不是失败（D-PC26、PLAY-04）。
  in_progress: '生成中',
  source_changed: '源文件已变化，未生成',
  encode_failed: '转封装失败',
  disk_full: '磁盘空间不足',
  file_missing: '源文件不存在',
  probe_failed: '读不出技术信息'
};

// playbackProxyCodeOutcome 把逐项结果码归成四类（D-PC26）：ready 可以切到内嵌播放、
// pending 还在排队或处理、cancelled 被用户取消、failed 真失败。空码返回空串。
export function playbackProxyCodeOutcome(code) {
  if (code === 'created' || code === 'already_exists') return 'ready';
  if (code === 'in_progress') return 'pending';
  if (code === 'cancelled') return 'cancelled';
  return code ? 'failed' : '';
}

// playbackProxyItemState 从一份 playback-proxy-state 快照里读出某个视频现在走到哪一步（D-PC26、PLAY-04）：
// - processing：正在处理（current_video_id）；
// - queued：排队中，position 为「第 N 个」（queued_video_ids 的下标 + 1）；排位只带前若干项，
//   tracked（调用方知道它已入队）且队列比排位表长时 position 为 0，表示「排队中」但不知道第几个；
// - done：本轮已有这一项的结果（取最后一条，跳过只表示「已在进行中」的 in_progress）；
// - idle：快照里没有这一项。
export function playbackProxyItemState(status, videoID, { tracked = false } = {}) {
  const id = Number(videoID);
  if (!status || !id) return { phase: 'idle' };
  if (status.running && Number(status.current_video_id) === id) return { phase: 'processing' };
  const queued = (status.queued_video_ids || []).map(Number);
  const index = queued.indexOf(id);
  if (index >= 0) return { phase: 'queued', position: index + 1 };
  const results = status.results || [];
  for (let i = results.length - 1; i >= 0; i -= 1) {
    const result = results[i];
    if (Number(result?.video_id) !== id || result.code === 'in_progress') continue;
    return { phase: 'done', result, outcome: playbackProxyCodeOutcome(result.code) };
  }
  if (tracked && status.running && Number(status.queued || 0) > queued.length) return { phase: 'queued', position: 0 };
  return { phase: 'idle' };
}

// playbackProxyItemStateText 是抽屉里显示的那一句：「排队中（第 N 个）」「生成中」。
export function playbackProxyItemStateText(state) {
  if (state?.phase === 'processing') return '生成中';
  if (state?.phase === 'queued') return state.position > 0 ? `排队中（第 ${state.position} 个）` : '排队中';
  return '';
}

// playbackProxyBatchSummary 把一轮任务的结果压成一句人话，供批量入口提示用。
export function playbackProxyBatchSummary(status) {
  if (!status) return '';
  if (status.running) return `正在生成播放代理 ${status.processed}/${status.total}…`;
  if (status.cancelled) return `播放代理任务已取消，已处理 ${status.processed}/${status.total}。`;
  return `播放代理完成：成功 ${status.succeeded}，跳过 ${status.skipped}，失败 ${status.failed}。`;
}

// playbackProxyStrategyLabel 说明这份代理是怎么做出来的。
export function playbackProxyStrategyLabel(strategy) {
  if (strategy === 'remux') return '换容器（未重编码）';
  if (strategy === 'transcode') return '已重编码';
  return strategy || '未知策略';
}

// DEFAULT_PROXY_CACHE_LIMIT_BYTES 与后端 database.DefaultProxyCacheLimitBytes 同值（50 GiB）。
export const DEFAULT_PROXY_CACHE_LIMIT_BYTES = 50 * 1024 * 1024 * 1024;

// normalizeProxyCacheLimitBytes 与后端 NormalizeProxyCacheLimitBytes 同口径：
// 0 是「不限」这个真实取值，原样保留；负数与读不出来的值回落到默认 50 GiB。
export function normalizeProxyCacheLimitBytes(value) {
  const bytes = Number(value);
  if (!Number.isFinite(bytes)) return DEFAULT_PROXY_CACHE_LIMIT_BYTES;
  if (bytes < 0) return DEFAULT_PROXY_CACHE_LIMIT_BYTES;
  return Math.round(bytes);
}
