// 场景检索（D-MW-SCENES）的展示口径：模式、来源、提示与错误代码的中文说法、时间格式、
// 命中分组与「请求代次」。场景检索页与设置页「场景检索」分区共用，免得各写一套。

export const SCENE_MODES = [
  { value: 'all', label: '全部' },
  { value: 'dialogue', label: '对白' },
  { value: 'visual', label: '画面' }
];

export const SCENE_SOURCE_LABELS = { dialogue: '对白', visual: '画面' };

export const SCENE_QUERY_MAX_LENGTH = 200;

// 点击命中后提前 2 秒起播（不小于 0），让人看得到命中之前的那一点上下文。
export const SCENE_PLAYBACK_LEAD_MS = 2000;

const NOTICE_TEXT = {
  scene_runtime_unavailable: '本地画面模型尚未就绪，画面部分没有执行。请先「准备模型」。',
  visual_index_empty: '当前范围还没有画面索引，画面部分没有可检索的内容；请先建立画面索引。',
  scene_visual_failed: '本地画面模型推理出错，画面部分没有结果；可稍后重试或重新准备模型。',
  scene_external_not_configured: '外部描述需要先在「AI 标签」设置里填写接口地址与模型，画面部分没有执行。',
  subtitle_index_empty: '当前范围没有可检索的 .srt 字幕索引，对白部分没有可检索的内容。'
};

const ERROR_TEXT = {
  scene_runtime_unavailable: '本地画面模型尚未就绪，请先在本页或设置里「准备模型」。',
  scene_runtime_unsupported: '本地画面模型仅支持 macOS Apple Silicon。',
  scene_external_not_enabled: '设置里没有启用外部描述，不会向外部接口发送任何内容。',
  scene_external_not_configured: '外部描述需要先在「AI 标签」设置里填写接口地址与模型。',
  scene_query_invalid: '请输入 1–200 个字的检索内容。',
  scene_mode_invalid: '不支持的检索模式。',
  scene_index_running: '画面索引正在建立，完成或取消后再清理旧索引。',
  scene_index_not_running: '当前没有正在建立的画面索引。',
  scene_index_stopping: '应用正在退出。',
  scene_index_cancelling: '上一轮画面索引正在取消，等它收尾后再建立。',
  scene_external_revoked: '外部描述已关闭或接口配置已变更，剩余的外部描述没有发送。',
  scene_runtime_not_preparing: '当前没有正在进行的模型准备。'
};

// 这些提示表示画面部分根本没有执行：此时不能说"没有找到"（M-5）。
export const SCENE_VISUAL_SKIPPED_NOTICES = ['scene_runtime_unavailable', 'scene_visual_failed', 'scene_external_not_configured'];

export function sceneVisualSkipped(notices) {
  return (notices || []).some(code => SCENE_VISUAL_SKIPPED_NOTICES.includes(code));
}

export function sceneNoticeText(code) {
  return NOTICE_TEXT[code] || '部分检索没有执行。';
}

// 后端错误以代码开头（例如 scene_runtime_unavailable）；认得的换成中文，认不得的原样给出。
export function sceneErrorText(err) {
  const message = String(err?.message || err || '').trim();
  for (const [code, text] of Object.entries(ERROR_TEXT)) {
    if (message.includes(code)) return text;
  }
  return message || '操作失败';
}

function pad(value) {
  return String(value).padStart(2, '0');
}

// 毫秒 → m:ss 或 h:mm:ss。
export function formatSceneTime(ms) {
  const total = Math.max(0, Math.floor(Number(ms || 0) / 1000));
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = total % 60;
  return hours > 0 ? `${hours}:${pad(minutes)}:${pad(seconds)}` : `${minutes}:${pad(seconds)}`;
}

export function formatSceneRange(startMs, endMs) {
  return `${formatSceneTime(startMs)} – ${formatSceneTime(endMs)}`;
}

export function scenePlaybackStartMs(startMs) {
  return Math.max(0, Math.round(Number(startMs || 0)) - SCENE_PLAYBACK_LEAD_MS);
}

// 命中缩略帧走既有的单帧预览路由（/preview/frame/{id}?ms=&w=），不暴露源路径。
export function sceneFrameURL(videoID, ms, width = 320) {
  return `/preview/frame/${Number(videoID)}?ms=${Math.max(0, Math.round(Number(ms || 0)))}&w=${width}`;
}

// 按视频分组，组的顺序取该视频第一条命中出现的位置（即融合后的排名）。
export function groupSceneHits(hits) {
  const groups = [];
  const byVideo = new Map();
  for (const hit of hits || []) {
    let group = byVideo.get(hit.video_id);
    if (!group) {
      group = { videoID: hit.video_id, title: hit.title || `视频 ${hit.video_id}`, hits: [] };
      byVideo.set(hit.video_id, group);
      groups.push(group);
    }
    group.hits.push(hit);
  }
  return groups;
}

export function sceneCoverageParts(coverage) {
  const total = Number(coverage?.total_videos || 0);
  const parts = [`范围内 ${total} 部视频`];
  parts.push(`字幕索引 ${Number(coverage?.subtitle_indexed || 0)} 部`);
  const unindexed = Number(coverage?.subtitle_unindexed || 0);
  if (unindexed > 0) parts.push(`另有 ${unindexed} 部只有其他格式或内嵌字幕（未索引）`);
  parts.push(`画面已索引 ${Number(coverage?.visual_indexed || 0)} 部`);
  return parts;
}

export function sceneVisualCoveragePercent(coverage) {
  const total = Number(coverage?.total_videos || 0);
  if (total <= 0) return 0;
  return Math.min(100, Math.round((Number(coverage?.visual_indexed || 0) / total) * 100));
}

// 请求代次：每发一次检索取一个新代次，响应回来时只认最新的那一次，晚到的旧结果直接丢弃。
export function createRequestGeneration() {
  let current = 0;
  return {
    next() { current += 1; return current; },
    isCurrent(generation) { return generation === current; }
  };
}
