import { reactive } from 'vue';
import { formatBytes, formatDuration } from './mediaDetails.js';
import { playbackProxyStrategyLabel } from './playbackProxy.js';

// 版本对比播放器（2026-10-12）：同一部片的两个版本左右同步播放，比画质。
// 打开入口是全局的：任何地方调 openVersionCompare(左, 右)，App.vue 里常驻的 VersionCompareDialog
// 跟着这份状态挂载与卸载。session 每次打开递增，宿主用它作 key——换一对视频就是一次新会话，
// 右侧偏移等会话内状态随之清零。
export const versionCompareState = reactive({
  open: false,
  leftVideoID: 0,
  rightVideoID: 0,
  session: 0
});

// 两个 ID 都要是正整数且不相同，否则不打开（返回 false）。
export function openVersionCompare(leftVideoID, rightVideoID) {
  const left = Number(leftVideoID);
  const right = Number(rightVideoID);
  if (!Number.isInteger(left) || !Number.isInteger(right) || left <= 0 || right <= 0 || left === right) return false;
  versionCompareState.leftVideoID = left;
  versionCompareState.rightVideoID = right;
  versionCompareState.session += 1;
  versionCompareState.open = true;
  return true;
}

export function closeVersionCompare() {
  versionCompareState.open = false;
  versionCompareState.leftVideoID = 0;
  versionCompareState.rightVideoID = 0;
}

// ===== 同步规则（纯函数，组件只负责把它们接到两个 <video> 上）=====
// 左侧是主导方，右侧跟随：右侧应处的位置 = 左侧位置 + 偏移。每隔 COMPARE_SYNC_INTERVAL_MS
// 检查一次，偏差超过 COMPARE_DRIFT_THRESHOLD 秒才拖动右侧；任一侧在拖动或缓冲时不纠偏。
export const COMPARE_DRIFT_THRESHOLD = 0.15;
export const COMPARE_SYNC_INTERVAL_MS = 300;
// 右侧偏移的上限：片头差异再大也到不了十分钟，超出多半是误操作。
export const COMPARE_OFFSET_LIMIT = 600;

function finiteOr(value, fallback) {
  const number = Number(value);
  return Number.isFinite(number) ? number : fallback;
}

// 右侧应处的位置。time 夹在 [0, 右侧时长] 内；inRange 为 false 表示这一刻右侧还没开始
// （负偏移时左侧还在右侧片头之前）或已经播完，右侧应停在 time 上等。时长未知时不设上限。
export function compareFollowerTarget(leaderTime, offset, followerDuration) {
  const raw = finiteOr(leaderTime, 0) + finiteOr(offset, 0);
  const duration = finiteOr(followerDuration, 0);
  const max = duration > 0 ? duration : Infinity;
  return { time: Math.min(Math.max(raw, 0), max), inRange: raw >= 0 && raw < max };
}

// 需要纠偏时返回右侧要拖到的位置，否则返回 null。阈值以内不纠，免得来回抖。
export function compareDriftSeek({ leaderTime, followerTime, offset = 0, followerDuration, threshold = COMPARE_DRIFT_THRESHOLD }) {
  const target = compareFollowerTarget(leaderTime, offset, followerDuration);
  return Math.abs(finiteOr(followerTime, 0) - target.time) > threshold ? target.time : null;
}

// 调整右侧偏移：按 0.1 秒取整（避免 0.1 累加出 0.30000000000000004），并夹在上限内。
export function applyCompareOffset(offset, delta) {
  const next = Math.round((finiteOr(offset, 0) + finiteOr(delta, 0)) * 10) / 10;
  const clamped = Math.min(Math.max(next, -COMPARE_OFFSET_LIMIT), COMPARE_OFFSET_LIMIT);
  return Object.is(clamped, -0) ? 0 : clamped;
}

export function formatCompareOffset(offset) {
  const value = finiteOr(offset, 0);
  if (value === 0) return '0.0 秒';
  return `${value > 0 ? '+' : '-'}${Math.abs(value).toFixed(1)} 秒`;
}

// 同一时刻只有一侧出声：audioSide 那一侧不静音，另一侧静音。
export function compareMuteState(audioSide) {
  return { left: audioSide !== 'left', right: audioSide !== 'right' };
}

// 滑动对比的分界线位置（0–100，左侧画面占的百分比）。
export function compareSliderPercent(clientX, rect) {
  const width = finiteOr(rect?.width, 0);
  if (width <= 0) return 50;
  const percent = ((finiteOr(clientX, 0) - finiteOr(rect?.left, 0)) / width) * 100;
  return Math.min(Math.max(percent, 0), 100);
}

// 叠在上层的左侧画面只露出分界线左边那一段。
export function compareClipPath(percent) {
  const value = Math.min(Math.max(finiteOr(percent, 50), 0), 100);
  return `inset(0 ${Number((100 - value).toFixed(2))}% 0 0)`;
}

export function formatCompareTime(seconds) {
  return formatDuration(Math.max(0, finiteOr(seconds, 0))) || '00:00';
}

// ===== 播放源与标注 =====
// 播放源与详情抽屉的内嵌播放器同一条路：GetPreviewSession。有可用播放代理时会话已经指向代理
// （mode=inline，带 proxy 标注），否则只有容器白名单内的文件能内嵌；其余返回一句可读的原因。
export function compareSourceFromSession(session) {
  const source = session?.mode === 'inline' ? session.inline_source : null;
  if (source?.locator_value) {
    return { src: source.locator_value, proxy: session.proxy || null, error: '' };
  }
  const reason = String(session?.reason_message || '').trim() || '当前视频暂不支持在应用内预览。';
  const hint = session?.mode === 'external-preview' ? '可在详情抽屉里「生成播放代理」后再对比。' : '';
  return { src: '', proxy: null, error: `${reason}${hint}` };
}

// 内嵌 <video> 报错时的说法：已经在经代理播放就别再建议生成代理。
export function compareMediaErrorText(proxy) {
  return proxy
    ? '播放代理也无法在应用内播放，请检查代理文件或改用系统播放器。'
    : '无法在应用内播放此文件，可在详情抽屉里「生成播放代理」后再对比。';
}

// 一侧的标注：标题、分辨率（宽×高）、文件大小、视频编码，都来自已经取到的 GetVideoDetails 与预览会话。
// 经代理播放时注明：重编码过的代理画面不代表原片画质，比画质时必须知道这一点。
export function compareSideLabel(details, session, videoID) {
  const video = details?.video || {};
  const stream = (details?.streams || []).find(item => item?.stream_type === 'video') || null;
  const title = String(details?.effective_title || video.display_title || video.name || session?.display_name || '').trim()
    || `视频 #${Number(videoID) || ''}`;
  const width = Number(stream?.width || video.width || 0);
  const height = Number(stream?.height || video.height || 0);
  const facts = [];
  if (width > 0 && height > 0) facts.push(`${width}×${height}`);
  if (Number(video.size) > 0) facts.push(formatBytes(video.size));
  const codec = String(stream?.codec_name || '').trim();
  if (codec) facts.push(codec);
  let proxyNote = '';
  if (session?.proxy) {
    proxyNote = session.proxy.strategy === 'transcode'
      ? '经播放代理播放（已重编码），画面不代表原片画质'
      : `经播放代理播放（${playbackProxyStrategyLabel(session.proxy.strategy)}）`;
  }
  return { title, facts: facts.join(' · '), proxyNote };
}
