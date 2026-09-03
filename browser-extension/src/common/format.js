// 界面用的数字格式化。纯函数。

const UNITS = ['B', 'KB', 'MB', 'GB', 'TB'];

export function formatBytes(bytes) {
  const value = Number(bytes);
  if (!Number.isFinite(value) || value <= 0) return '—';
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < UNITS.length - 1) {
    size /= 1024;
    unit += 1;
  }
  const digits = size >= 100 || unit === 0 ? 0 : 1;
  return `${size.toFixed(digits)} ${UNITS[unit]}`;
}

export function formatDuration(seconds) {
  const total = Math.round(Number(seconds));
  if (!Number.isFinite(total) || total <= 0) return '—';
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const secs = total % 60;
  const pad = (value) => String(value).padStart(2, '0');
  return hours > 0 ? `${hours}:${pad(minutes)}:${pad(secs)}` : `${minutes}:${pad(secs)}`;
}

export function formatSpeed(bytesPerSecond) {
  const value = Number(bytesPerSecond);
  if (!Number.isFinite(value) || value <= 0) return '';
  return `${formatBytes(value)}/s`;
}

export function formatBitrate(bitsPerSecond) {
  const value = Number(bitsPerSecond);
  if (!Number.isFinite(value) || value <= 0) return '';
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)} Mbps`;
  return `${Math.round(value / 1000)} kbps`;
}

// HLS 的体积只能估：码率 × 时长。估出来的数字要标成"约"，
// 让用户知道这不是服务端给的准数。
export function estimateSize({ bandwidth, durationSeconds }) {
  const bits = Number(bandwidth) * Number(durationSeconds);
  if (!Number.isFinite(bits) || bits <= 0) return 0;
  return Math.round(bits / 8);
}

export function formatPercent(done, total) {
  const totalValue = Number(total);
  if (!Number.isFinite(totalValue) || totalValue <= 0) return '0%';
  const ratio = Math.max(0, Math.min(1, Number(done) / totalValue));
  return `${Math.round(ratio * 100)}%`;
}
