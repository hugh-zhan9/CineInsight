// 目标输入和显示统一采用十进制单位，避免100GB目标与二进制GB标签混用。
export function formatGoalBytes(value) {
  const bytes = Math.max(0, Number(value) || 0);
  for (const [unit, divisor] of [['PB', 1e15], ['TB', 1e12], ['GB', 1e9], ['MB', 1e6], ['KB', 1e3]]) if (bytes >= divisor) return `${(bytes / divisor).toFixed(2)} ${unit}`;
  return `${Math.round(bytes)} B`;
}
