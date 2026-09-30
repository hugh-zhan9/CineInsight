// 字幕界面共用的纯函数：错误码文案、编码说法、工作台的重译默认模式与时间问题一键修复。
// 生成、翻译、工作台三个入口共用同一套口径（D-PC13~D-PC17）。
import { scrubAbsolutePaths } from './pathText.js';

// 覆盖了旧字幕之后的统一说法（D-PC13、MEDIA-05）：写入器先备份，行菜单可以找回。
export const SUBTITLE_BACKUP_NOTE = '原字幕已备份，可在行菜单「恢复上一版字幕」找回。';

const ENCODING_LABELS = {
  'utf-8': 'UTF-8',
  'utf-16le': 'UTF-16',
  'utf-16be': 'UTF-16',
  gb18030: 'GBK',
  big5: 'Big5'
};

// 后端编码代码 → 界面说法。GB18030 是 GBK 的超集，用户认得的是「GBK」。
export function subtitleEncodingLabel(encoding) {
  const key = String(encoding || '').trim().toLowerCase();
  if (!key || key === 'unknown') return '未知编码';
  return ENCODING_LABELS[key] || key.toUpperCase();
}

// D-PC14 的提示原文：「字幕编码为 GBK（推测），可一键转换为 UTF-8（会先备份）」。
export function subtitleEncodingPrompt(encoding) {
  const key = String(encoding || '').trim().toLowerCase();
  if (!key || key === 'unknown') return '字幕编码无法识别，暂时不能编辑或翻译。';
  return `字幕编码为 ${subtitleEncodingLabel(key)}（推测），可一键转换为 UTF-8（会先备份）。`;
}

const SUBTITLE_ERROR_TEXTS = {
  subtitle_missing: '这个视频还没有同名 .srt 字幕。',
  subtitle_not_sidecar_srt: '该视频只有内嵌/其他格式字幕，暂不支持编辑或翻译。',
  subtitle_encoding_not_utf8: '字幕文件不是 UTF-8 编码，需要先转换为 UTF-8。',
  subtitle_encoding_ambiguous: '无法确定字幕的编码，请对照预览选择正确的一项。',
  subtitle_replace_failed: '写回字幕文件失败，原字幕没有改动。',
  subtitle_conflict: '字幕在编辑器之外被修改，请重新加载后再保存。',
  subtitle_pending_missing: '待确认的临时字幕已经不在了，请重新生成。',
  subtitle_job_conflict: '字幕任务状态已变化，请刷新后重试。'
};

// 带错误码的字幕失败的中文文案：后端给了文案就用后端的（已是中文、已擦路径），否则按码兜底。
export function subtitleErrorText(code, message) {
  const text = scrubAbsolutePaths(message).trim();
  if (text) return text;
  return SUBTITLE_ERROR_TEXTS[code] || '字幕操作没有完成。';
}

// 未带错误码的异常（系统错误、绑定抛错）：擦掉路径，文件不存在时换成一句能看懂的话。
export function subtitleExceptionText(err) {
  const raw = String(err?.message || err || '').trim();
  if (/no such file or directory/i.test(raw)) return '文件不存在或已被移动。';
  if (/permission denied|operation not permitted/i.test(raw)) return '没有读写这个文件的权限。';
  return scrubAbsolutePaths(raw) || '操作失败。';
}

function nonEmptyLineCount(text) {
  return String(text || '').split(/\r?\n/).filter(line => line.trim()).length;
}

// 工作台重译的默认范围（D-PC16）：选区里多数条目是「原文 + 译文」两行时只替换译文行，否则整条替换。
export function defaultRetranslateMode(entries) {
  const list = (entries || []).filter(Boolean);
  if (list.length === 0) return 'whole_entry';
  const bilingual = list.filter(entry => nonEmptyLineCount(entry.text) >= 2).length;
  return bilingual * 2 > list.length ? 'translation_line' : 'whole_entry';
}

export const ZERO_DURATION_FIX_MS = 500;

// 一键修复（D-PC15）：零时长（含结束早于开始）→ 结束 = 开始 + 500ms，且不越过下一条的开始；
// 重叠 → 前一条的结束 = 后一条的开始 − 1ms。修不了的（例如修完会变成零时长）原样留着，交给用户。
// 返回新数组与修复的处数，不改入参。
export function fixSubtitleTimingIssues(entries) {
  const next = (entries || []).map(entry => ({ ...entry }));
  let fixed = 0;
  for (let index = 0; index < next.length; index += 1) {
    const entry = next[index];
    const start = Number(entry.start_time_ms);
    const end = Number(entry.end_time_ms);
    if (!Number.isFinite(start) || !Number.isFinite(end) || start < 0 || end > start) continue;
    let target = start + ZERO_DURATION_FIX_MS;
    const following = next[index + 1];
    const followingStart = Number(following?.start_time_ms);
    if (following && Number.isFinite(followingStart) && followingStart > start) target = Math.min(target, followingStart);
    entry.end_time_ms = target;
    fixed += 1;
  }
  for (let index = 1; index < next.length; index += 1) {
    const previous = next[index - 1];
    const start = Number(next[index].start_time_ms);
    if (!Number.isFinite(start) || start >= Number(previous.end_time_ms)) continue;
    const target = start - 1;
    if (target <= Number(previous.start_time_ms)) continue;
    previous.end_time_ms = target;
    fixed += 1;
  }
  return { entries: next, fixed };
}

// 时间的界面说法（备份、同步时间）：YYYY-MM-DD HH:mm（本地时间）。
export function formatLocalTime(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '未知时间';
  const pad = number => String(number).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}
