// 有效观看的前端判定（D-PC43、PLAY-07）。阈值与 services/play_view_events.go 的 viewThreshold
// 同一公式，样例与 Go 测试 TestPLAY07ViewThresholdIsMinOfSixtySecondsAndHalfDuration 逐条对应
// （见 viewThreshold.test.js）。改其中任何一边，另一边与样例要一起改。
// 内嵌播放器（inline_view）与手机端（mobile_feed）共用这一份：累计播放首次越过阈值才记一条。

// 阈值上限，也是时长未知时的阈值（秒）。
export const VIEW_THRESHOLD_CAP_SECONDS = 60;

// 有效观看阈值：min(60, 时长 × 50%)；时长未知（≤0、NaN、Infinity）取 60。
export function viewThreshold(duration) {
  const value = Number(duration);
  if (!(value > 0) || !Number.isFinite(value)) return VIEW_THRESHOLD_CAP_SECONDS;
  return Math.min(VIEW_THRESHOLD_CAP_SECONDS, value * 0.5);
}

// 两次采样之间允许媒体时间比墙钟多走的量：timeupdate 的间隔并不均匀。
const SAMPLE_SLACK_SECONDS = 0.5;

// 累计播放时长：只算往前走的部分，且一段不超过两次采样之间的墙钟（按播放速率放大）。
// 拖动进度条、字幕跳转、循环回到片头都不算播放；暂停与拖动时调用 breakSegment 断开当前一段，
// 否则「暂停很久后往前拖」会被墙钟放行成一大段播放。
export function createPlaybackAccumulator() {
  let total = 0;
  let lastPosition = null;
  let lastAt = null;
  return {
    sample(position, atMs, rate = 1) {
      const pos = Number(position);
      const at = Number(atMs);
      if (!Number.isFinite(pos) || !Number.isFinite(at)) return total;
      if (lastPosition !== null && lastAt !== null) {
        const delta = pos - lastPosition;
        const speed = Number(rate) > 0 ? Number(rate) : 1;
        const allowed = Math.max(0, (at - lastAt) / 1000) * speed + SAMPLE_SLACK_SECONDS;
        if (delta > 0) total += Math.min(delta, allowed);
      }
      lastPosition = pos;
      lastAt = at;
      return total;
    },
    breakSegment() {
      lastPosition = null;
      lastAt = null;
    },
    get seconds() {
      return total;
    }
  };
}

// 本次打开播放器的会话标识：32 位十六进制，后端按 (来源, 视频, 会话) 去重，同一会话只记一次。
export function newViewSessionID() {
  const bytes = new Uint8Array(16);
  globalThis.crypto.getRandomValues(bytes);
  return Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('');
}
