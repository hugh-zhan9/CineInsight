// 看完与续播的唯一前端判定（D-PC41、D-PC42）。与 services/library_service.go 的
// watchCompletionTail / isWatchCompleted / resumable 逐字对应，样例与 Go 测试
// TestWatchCompletionSamplesPLAY11、TestResumableMatchesResumableSQLPLAY10 共用（见 watchState.test.js）。
// 改其中任何一边，另一边与两组样例要一起改。

// 片尾区间：时长的 5%，最多 180 秒。
export function watchCompletionTail(duration) {
  return Math.min(Number(duration) * 0.05, 180);
}

// 「算看完」：位置必须真的往前走过，时长未知时一律不算看完。
export function isWatchCompleted(position, duration) {
  const pos = Number(position);
  const dur = Number(duration);
  return dur > 0 && pos > 0 && pos >= dur - watchCompletionTail(dur);
}

// 断点有效、可以续播：有断点，且没看完，或者这次断点是在最近一次标已看之后写的（重看）。
export function resumable(video) {
  if (!video || !(Number(video.watch_position_seconds) > 0)) return false;
  const watchedAt = parseTimestamp(video.watched_at);
  if (!video.is_watched || !watchedAt) return true;
  const progressAt = parseTimestamp(video.watch_progress_updated_at);
  return !!progressAt && compareTimestamps(progressAt, watchedAt) > 0;
}

// 续播位置：断点有效且还没落进片尾区间才续播，否则从头播。行组件的进度条与抽屉的起播位置共用它。
export function resumePosition(video) {
  if (!resumable(video)) return 0;
  const position = Number(video.watch_position_seconds);
  if (!Number.isFinite(position) || position <= 0) return 0;
  if (isWatchCompleted(position, video.duration)) return 0;
  return position;
}

// Go 的 time.Time 比较到纳秒，JS 的 Date 只到毫秒：后端下发的 RFC3339Nano 时间在同一毫秒内
// 仍可能有先后。小数秒自己拆出来（各家 JS 引擎对多于 3 位的小数是截断还是进位不一致），
// 整秒交给 Date.parse，毫秒与毫秒以下分开比，与 Go 的 After 同一结果。
function parseTimestamp(value) {
  if (value === null || value === undefined || value === '') return null;
  const text = String(value);
  const fraction = text.match(/(T\d{2}:\d{2}:\d{2})\.(\d+)/);
  const seconds = Date.parse(fraction ? text.replace(fraction[0], fraction[1]) : text);
  if (!Number.isFinite(seconds)) return null;
  const digits = fraction ? fraction[2].slice(0, 9).padEnd(9, '0') : '000000000';
  return { ms: seconds + Number(digits.slice(0, 3)), subMillisecond: Number(digits.slice(3)) };
}

function compareTimestamps(a, b) {
  if (a.ms !== b.ms) return a.ms - b.ms;
  return a.subMillisecond - b.subMillisecond;
}
