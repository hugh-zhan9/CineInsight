// 播放代理逐项结果码的中文文案（D-006）。后端返回的是稳定的机器码，
// 界面上的说法集中在这里一份，详情抽屉、批量提示与设置页共用。
export const PLAYBACK_PROXY_CODE_LABELS = {
  created: '已生成',
  already_exists: '已有可用代理',
  in_progress: '已在进行中',
  source_changed: '源文件已变化，未生成',
  encode_failed: '转封装失败',
  disk_full: '磁盘空间不足',
  file_missing: '源文件不存在',
  probe_failed: '读不出技术信息'
};

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
