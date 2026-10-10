// 视频工作台（视频编辑合同）的展示口径与配方变换：状态、类型与错误码的中文说法，时间格式与解析，
// 帧步长，配方的纯函数修改（一律返回新对象、不改入参），未匹配区间，快速模式的实际切点，切点帧地址。
// 页面与各编辑器共用这一份，免得各写一套说法。
import { createRequestGeneration, sceneFrameURL } from './sceneSearch.js';

export { createRequestGeneration };

export const EDIT_KIND_LABELS = { merge: '顺序合并', trim_intro: '批量去片头', hd_replace: '高清替换' };

export const EDIT_STATUS_LABELS = {
  draft: '草稿',
  analyzing: '分析中',
  queued: '排队中',
  running: '导出中',
  completed: '已完成',
  partial: '部分完成',
  failed: '失败',
  cancelled: '已取消',
  interrupted: '已中断'
};

export const EDIT_PHASE_LABELS = {
  pending: '等待', check: '核对来源', encode: '编码', concat: '拼接', verify: '校验', publish: '入库', done: '完成'
};

// 导出项失败码（合同「执行与发布」第 6 步）。
export const EDIT_ITEM_ERROR_LABELS = {
  source_missing: '来源缺失',
  source_changed: '来源文件已变化',
  disk_full: '磁盘空间不足',
  encoder_unavailable: '编码器不可用',
  encode_failed: '编码失败',
  verify_failed: '成品校验失败',
  output_conflict: '输出文件名被占用',
  publish_failed: '入库失败',
  cancelled: '已取消',
  interrupted: '已中断'
};

// 项目状态 → 列表里能做的事（合同状态图）。
export const EDIT_CANCELLABLE = ['queued', 'running'];
export const EDIT_REQUEUEABLE = ['interrupted', 'failed', 'partial'];
export const EDIT_ACTIVE = ['queued', 'running', 'analyzing'];

const FIXED_ERROR_TEXT = {
  video_edit_unavailable: '视频工作台暂不可用：数据库未就绪。',
  edit_project_not_found: '这个编辑项目已不存在。',
  edit_analysis_busy: '已有一轮分析在进行，请等它结束或取消。',
  edit_service_stopping: '应用正在退出或维护，暂时不能操作。'
};

// 后端错误形如「code: 说明」（可能带 Error: 前缀）；取出 code。
export function editErrorCode(err) {
  const text = String(err?.message ?? err ?? '').replace(/^Error:\s*/, '').trim();
  const match = text.match(/^([a-z][a-z_]*[a-z]):/);
  return match ? match[1] : '';
}

export function editErrorText(err) {
  const code = editErrorCode(err);
  if (FIXED_ERROR_TEXT[code]) return FIXED_ERROR_TEXT[code];
  const text = String(err?.message ?? err ?? '').replace(/^Error:\s*/, '').trim();
  if (code) return text.slice(code.length + 1).trim() || code;
  return text || '操作失败';
}

export function isEditConflict(err) {
  return editErrorCode(err) === 'edit_project_conflict';
}

export function editStatusLabel(status) {
  return EDIT_STATUS_LABELS[status] || '状态未知';
}

export function editKindLabel(kind) {
  return EDIT_KIND_LABELS[kind] || '编辑';
}

function pad(value, width = 2) {
  return String(value).padStart(width, '0');
}

// 毫秒 → h:mm:ss.mmm 或 m:ss.mmm（切点要精确到毫秒，不像场景检索只到秒）。
export function formatEditTime(ms) {
  const value = Math.max(0, Math.round(Number(ms) || 0));
  const millis = value % 1000;
  const total = Math.floor(value / 1000);
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = total % 60;
  const tail = `${pad(seconds)}.${pad(millis, 3)}`;
  return hours > 0 ? `${hours}:${pad(minutes)}:${tail}` : `${minutes}:${tail}`;
}

// 输入 → 整数毫秒；认 h:mm:ss.mmm、m:ss.mmm 与纯秒数（可带小数）。不认得返回 null。
export function parseEditTime(text) {
  const raw = String(text ?? '').trim();
  if (!raw) return null;
  const parts = raw.split(':');
  if (parts.length > 3 || parts.some(part => !/^\d+(\.\d+)?$/.test(part))) return null;
  if (parts.slice(0, -1).some(part => part.includes('.'))) return null;
  let seconds = 0;
  for (const part of parts) seconds = seconds * 60 + Number(part);
  if (parts.length > 1 && parts.slice(1).some(part => Number(part) >= 60)) return null;
  return Math.round(seconds * 1000);
}

// 预检返回的帧率是 ffprobe 的分数形式（24000/1001）；读不出来返回 0。
export function parseFrameRate(rate) {
  const text = String(rate ?? '').trim();
  const [num, den] = text.includes('/') ? text.split('/').map(Number) : [Number(text), 1];
  if (!Number.isFinite(num) || !Number.isFinite(den) || num <= 0 || den <= 0) return 0;
  return num / den;
}

// 按帧步进：帧率未知时返回 null，调用方据此禁用 ±1 帧（不编一个默认帧率）。
export function stepByFrames(ms, frames, fps) {
  if (!(fps > 0)) return null;
  return Math.max(0, Math.round(Number(ms || 0) + (frames * 1000) / fps));
}

// 切点帧走共享的单帧预览路由（/preview/frame/{id}?ms=&w=）。
export function editFrameURL(videoID, ms, width = 240) {
  return sceneFrameURL(videoID, ms, width);
}

// 切点前一帧的时间：帧率已知按一帧，未知按 1 毫秒（只用于取图，不写进配方）。
export function frameBefore(ms, fps) {
  const step = fps > 0 ? Math.round(1000 / fps) : 1;
  return Math.max(0, Math.round(Number(ms || 0)) - step);
}

export function cloneRecipe(recipe) {
  return JSON.parse(JSON.stringify(recipe || {}));
}

// 配方引用的全部视频（按配方顺序去重），与后端 editRecipeVideoIDs 同一口径。
export function editRecipeVideoIDs(recipe) {
  const ids = [];
  const add = id => {
    const value = Number(id || 0);
    if (value > 0 && !ids.includes(value)) ids.push(value);
  };
  (recipe?.merge?.sources || []).forEach(source => add(source.video_id));
  (recipe?.trim_intro?.items || []).forEach(item => add(item.video_id));
  if (recipe?.hd_replace) {
    add(recipe.hd_replace.long_video_id);
    add(recipe.hd_replace.hd_video_id);
  }
  return ids;
}

// ===== 顺序合并 =====

export function moveMergeSource(recipe, index, delta) {
  const next = cloneRecipe(recipe);
  const sources = next.merge.sources;
  const target = index + delta;
  if (index < 0 || index >= sources.length || target < 0 || target >= sources.length) return next;
  const [moved] = sources.splice(index, 1);
  sources.splice(target, 0, moved);
  return next;
}

// 移出一个来源；被移出的恰好是规格来源时回到「取第一个来源」（0）。
export function removeMergeSource(recipe, index) {
  const next = cloneRecipe(recipe);
  const [removed] = next.merge.sources.splice(index, 1);
  if (removed && Number(next.merge.spec_source_video_id) === Number(removed.video_id)) next.merge.spec_source_video_id = 0;
  return next;
}

export function setMergeSpec(recipe, videoID) {
  const next = cloneRecipe(recipe);
  next.merge.spec_source_video_id = Number(videoID) || 0;
  return next;
}

// 规格选项之间分辨率、帧率或动态范围有差别时才需要用户选（合同「顺序合并」）。
export function specOptionsDiffer(options) {
  const list = options || [];
  if (list.length < 2) return false;
  const key = option => `${option.width}x${option.height}@${option.frame_rate}:${option.hdr ? 1 : 0}`;
  return new Set(list.map(key)).size > 1;
}

// ===== 批量去片头 =====

// 统一区间写进每一项（origin=uniform），之后仍可逐项调整；写入即取消确认。
export function applyUniformTrim(recipe, startMs, endMs) {
  const next = cloneRecipe(recipe);
  next.trim_intro.items = next.trim_intro.items.map(item => ({
    ...item, remove_start_ms: startMs, remove_end_ms: endMs, origin: 'uniform', detect_status: '', confidence: 0, confirmed: false
  }));
  return next;
}

// 逐项手改区间：origin=manual，清掉识别状态（已手填即视为处理了「未识别」），取消确认。
export function setTrimRange(recipe, index, startMs, endMs) {
  const next = cloneRecipe(recipe);
  const item = next.trim_intro.items[index];
  if (!item) return next;
  Object.assign(item, {
    remove_start_ms: Math.max(0, Math.round(startMs)), remove_end_ms: Math.max(0, Math.round(endMs)),
    origin: 'manual', detect_status: '', confidence: 0, confirmed: false
  });
  return next;
}

export function setTrimConfirmed(recipe, index, confirmed) {
  const next = cloneRecipe(recipe);
  if (next.trim_intro.items[index]) next.trim_intro.items[index].confirmed = Boolean(confirmed);
  return next;
}

export function trimRangeValid(item) {
  return Number(item?.remove_end_ms) > Number(item?.remove_start_ms) && Number(item?.remove_start_ms) >= 0;
}

// 「全部确认」只确认区间有效的项；没识别出、也没手填的项留着，返回跳过的个数。
export function confirmAllTrim(recipe) {
  const next = cloneRecipe(recipe);
  let skipped = 0;
  for (const item of next.trim_intro.items) {
    if (trimRangeValid(item)) item.confirmed = true;
    else skipped += 1;
  }
  return { recipe: next, skipped };
}

export function removeTrimItem(recipe, index) {
  const next = cloneRecipe(recipe);
  next.trim_intro.items.splice(index, 1);
  return next;
}

// 需要用户处理的识别结果：未识别 / 有歧义（合同：明确标出，必须手填或移出）。
export function trimItemFlag(item) {
  if (item?.detect_status === 'undetected') return '未识别出片头，请手填或移出';
  if (item?.detect_status === 'ambiguous') return '有多个等长候选，请手填或移出';
  return '';
}

// ===== 高清替换 =====
// 段的长版与高清时长必须相等（不变速，Q7）：改长版的起点/终点时，高清的同一端跟着平移同样的量；
// 改高清起点等于整体平移高清区间（换对应位置）。手改一律记 origin=manual 并取消确认。

function editSegment(recipe, index, mutate) {
  const next = cloneRecipe(recipe);
  const segment = next.hd_replace.segments[index];
  if (!segment) return next;
  mutate(segment);
  segment.origin = 'manual';
  segment.confirmed = false;
  return next;
}

// edge = 'start' | 'end'；delta 被夹住，保证长版与高清两侧都不为负。
export function shiftSegmentEdge(recipe, index, edge, deltaMs) {
  return editSegment(recipe, index, segment => {
    const longKey = `long_${edge}_ms`;
    const hdKey = `hd_${edge}_ms`;
    const delta = Math.max(Math.round(deltaMs), -segment[longKey], -segment[hdKey]);
    segment[longKey] += delta;
    segment[hdKey] += delta;
  });
}

export function setSegmentEdge(recipe, index, edge, longMs) {
  const segment = recipe?.hd_replace?.segments?.[index];
  if (!segment) return cloneRecipe(recipe);
  return shiftSegmentEdge(recipe, index, edge, Math.round(longMs) - segment[`long_${edge}_ms`]);
}

// 整体平移高清区间（长版区间不动）。
export function shiftSegmentHD(recipe, index, deltaMs) {
  return editSegment(recipe, index, segment => {
    const delta = Math.max(Math.round(deltaMs), -segment.hd_start_ms);
    segment.hd_start_ms += delta;
    segment.hd_end_ms += delta;
  });
}

export function setSegmentHDStart(recipe, index, hdMs) {
  const segment = recipe?.hd_replace?.segments?.[index];
  if (!segment) return cloneRecipe(recipe);
  return shiftSegmentHD(recipe, index, Math.round(hdMs) - segment.hd_start_ms);
}

export function setSegmentAudio(recipe, index, source) {
  const next = cloneRecipe(recipe);
  const segment = next.hd_replace.segments[index];
  if (segment) segment.audio_source = source === 'hd' ? 'hd' : 'long';
  return next;
}

export function setSegmentConfirmed(recipe, index, confirmed) {
  const next = cloneRecipe(recipe);
  const segment = next.hd_replace.segments[index];
  if (segment) segment.confirmed = Boolean(confirmed);
  return next;
}

export function removeSegment(recipe, index) {
  const next = cloneRecipe(recipe);
  next.hd_replace.segments.splice(index, 1);
  return next;
}

// 长版时间线上没被任何段覆盖的区间：成品在这些地方保留长版画面。
export function unmatchedLongRanges(segments, longDurationMs) {
  const sorted = [...(segments || [])]
    .filter(segment => segment.long_end_ms > segment.long_start_ms)
    .sort((a, b) => a.long_start_ms - b.long_start_ms);
  const ranges = [];
  let cursor = 0;
  for (const segment of sorted) {
    if (segment.long_start_ms > cursor) ranges.push({ start_ms: cursor, end_ms: segment.long_start_ms });
    cursor = Math.max(cursor, segment.long_end_ms);
  }
  const end = Math.round(Number(longDurationMs) || 0);
  if (end > cursor) ranges.push({ start_ms: cursor, end_ms: end });
  return ranges;
}

// 手动加一段：放在第一个未匹配区间的开头，长 10 秒（区间不足 10 秒取区间长度），两侧时间相同。
export function addManualSegment(recipe, longDurationMs) {
  const next = cloneRecipe(recipe);
  const gap = unmatchedLongRanges(next.hd_replace.segments, longDurationMs)[0];
  if (!gap) return next;
  const length = Math.min(10_000, gap.end_ms - gap.start_ms);
  next.hd_replace.segments.push({
    long_start_ms: gap.start_ms, long_end_ms: gap.start_ms + length, hd_start_ms: gap.start_ms, hd_end_ms: gap.start_ms + length,
    audio_source: 'long', origin: 'manual', match_rate: 0, status: '', confirmed: false
  });
  next.hd_replace.segments.sort((a, b) => a.long_start_ms - b.long_start_ms);
  return next;
}

// ===== 轨道映射 =====
// 对一个冲突（输出轨 output 在来源 videoID 上对不上）做明确选择（Q9）。choice=drop 表示整条输出轨
// 不导出，按后端约定 video_id 填 0，并替掉该输出轨上的其他选择。kind = 'audio' | 'subtitle'。
export function setTrackChoice(recipe, kind, output, videoID, choice, streamIndex = 0) {
  const next = cloneRecipe(recipe);
  next.tracks = next.tracks || { audio: [], subtitle: [] };
  const list = (next.tracks[kind] || []).filter(entry => {
    if (entry.output !== output) return true;
    if (choice === 'drop') return false;
    return entry.video_id !== 0 && entry.video_id !== videoID;
  });
  if (choice === 'drop') list.push({ output, video_id: 0, choice: 'drop', stream_index: 0 });
  else list.push({ output, video_id: videoID, choice, stream_index: choice === 'stream' ? Number(streamIndex) : 0 });
  next.tracks[kind] = list;
  return next;
}

export const TRACK_FILL_LABELS = { silence: '该段填静音', none: '该段不出字幕', drop: '整条轨不导出' };

export function streamOptionLabel(stream) {
  const parts = [`流 #${stream.index}`];
  if (stream.language) parts.push(stream.language);
  if (stream.codec) parts.push(stream.codec);
  if (stream.channels) parts.push(`${stream.channels} 声道`);
  if (stream.title) parts.push(stream.title);
  return parts.join(' · ');
}

// ===== 预检 =====

// 预检只对生成它时的配方版本与导出模式有效；之后改过配方或切过模式就要重跑。
export function preflightFresh(preflight, project) {
  return Boolean(preflight && project && preflight.project_id === project.id &&
    preflight.revision === project.revision && preflight.mode === project.mode);
}

export function preflightSource(preflight, videoID) {
  return (preflight?.sources || []).find(source => Number(source.video_id) === Number(videoID)) || null;
}

export function sourceFrameRate(preflight, videoID) {
  return parseFrameRate(preflightSource(preflight, videoID)?.video?.frame_rate);
}

// 快速模式下某个切点实际落到的关键帧时间；不是快速模式或预检里没有这一处时返回 null。
export function fastCutActual(preflight, seq, videoID, requestedMs) {
  const cut = (preflight?.fast?.cut_points || []).find(point => point.seq === seq &&
    Number(point.video_id) === Number(videoID) && point.requested_ms === Math.round(requestedMs));
  return cut ? cut.actual_ms : null;
}

export function unacknowledgedKeys(preflight, acknowledged) {
  const acked = new Set(acknowledged || []);
  return (preflight?.warnings || []).map(warning => warning.key).filter(key => !acked.has(key));
}

export function outputSpecText(output) {
  if (!output) return '';
  const fps = parseFrameRate(output.frame_rate);
  const parts = [];
  if (output.width && output.height) parts.push(`${output.width}×${output.height}`);
  if (fps > 0) parts.push(`${Math.round(fps * 1000) / 1000} fps`);
  if (output.hdr) parts.push('HDR');
  if (output.video_codec) parts.push(`${output.video_codec}（${output.encoder || '默认编码器'}）`);
  if (output.audio_codec) parts.push(`音频 ${output.audio_codec}`);
  if (output.container) parts.push(output.container);
  return parts.join(' · ');
}
