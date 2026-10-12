// 本地规则（数据维护 · 规则，D-RA-01..05）前端用的纯函数：条件/动作的文案、编辑表单与入参互转、错误解析。
// 界面单位：大小用 MB、时长用分钟、高度用像素；后端一律字节 / 秒 / 像素。

import { translateBackendError } from './feedback.js';

const MB = 1024 * 1024;

export const MEDIA_KIND_LABELS = { video: '视频', image: '图片' };

// 条件类型与适用的媒体类型（需求设计文档 §3.2）。
export const CONDITION_TYPES = [
  { type: 'under_directory', label: '所在目录', kinds: ['video', 'image'] },
  { type: 'name_contains', label: '文件名包含', kinds: ['video', 'image'] },
  { type: 'name_glob', label: '文件名通配', kinds: ['video', 'image'] },
  { type: 'name_regex', label: '文件名正则', kinds: ['video', 'image'] },
  { type: 'extension_in', label: '扩展名', kinds: ['video', 'image'] },
  { type: 'size_between', label: '文件大小', kinds: ['video', 'image'], unit: 'MB', scale: MB },
  { type: 'duration_between', label: '时长', kinds: ['video'], unit: '分钟', scale: 60 },
  { type: 'height_between', label: '画面高度', kinds: ['video'], unit: '像素', scale: 1 },
  { type: 'video_codec_in', label: '视频编码', kinds: ['video'] },
  { type: 'has_gps', label: '地理位置', kinds: ['image'] }
];

export const DISABLED_REASON_LABELS = {
  target_deleted: '引用的标签或作品集已删除，已自动停用',
  invalid_condition: '条件已损坏，已自动停用，请编辑后重新保存'
};

export function conditionTypesFor(kind) {
  return CONDITION_TYPES.filter(item => item.kinds.includes(kind));
}

function conditionMeta(type) {
  return CONDITION_TYPES.find(item => item.type === type) || null;
}

function trimNumber(value) {
  return Number.isInteger(value) ? String(value) : String(Number(value.toFixed(2)));
}

function rangeText(min, max, unit) {
  const lo = min == null ? '' : trimNumber(min);
  const hi = max == null ? '' : trimNumber(max);
  if (lo && hi) return `${lo}–${hi} ${unit}`;
  if (lo) return `≥ ${lo} ${unit}`;
  return `≤ ${hi} ${unit}`;
}

// conditionSummary 把一条条件写成一句话（列表摘要用）。目录只显示最后一段，避免整条路径挤满一行。
export function conditionSummary(condition) {
  const meta = conditionMeta(condition?.type);
  if (!meta) return '未知条件';
  switch (condition.type) {
    case 'under_directory': {
      const parts = String(condition.path || '').split('/').filter(Boolean);
      return `在「${parts[parts.length - 1] || condition.path}」目录下`;
    }
    case 'name_contains': return `文件名包含「${condition.text}」`;
    case 'name_glob': return `文件名匹配 ${condition.pattern}`;
    case 'name_regex': return `文件名符合正则 ${condition.pattern}`;
    case 'extension_in': return `扩展名为 ${(condition.exts || []).join('、')}`;
    case 'video_codec_in': return `视频编码为 ${(condition.codecs || []).join('、')}`;
    case 'has_gps': return condition.value ? '有地理位置' : '没有地理位置';
    default: {
      const min = condition.min == null ? null : condition.min / meta.scale;
      const max = condition.max == null ? null : condition.max / meta.scale;
      return `${meta.label} ${rangeText(min, max, meta.unit)}`;
    }
  }
}

export function actionSummary(action) {
  const name = action?.target_name || '（已删除）';
  return action?.type === 'add_to_collection' ? `加入作品集「${name}」` : `加标签「${name}」`;
}

export function ruleSummary(rule) {
  return {
    conditions: (rule?.conditions || []).map(conditionSummary).join('，且'),
    actions: (rule?.actions || []).map(actionSummary).join('；')
  };
}

// ===== 编辑表单 =====

let formKeySeed = 0;
const nextKey = () => `k${++formKeySeed}`;

export function blankCondition(type = 'under_directory') {
  return { key: nextKey(), type, path: '', text: '', pattern: '', list: '', min: '', max: '', value: true };
}

export function blankAction(type = 'add_tag') {
  return { key: nextKey(), type, target_id: '' };
}

// ruleToForm 把列表里的规则（或 null，新建）转成编辑表单。数值按界面单位换算。
export function ruleToForm(rule, kind = 'video') {
  if (!rule) {
    return { id: 0, name: '', media_kind: kind, enabled: true, conditions: [blankCondition()], actions: [blankAction()] };
  }
  const conditions = (rule.conditions || []).map(condition => {
    const meta = conditionMeta(condition.type);
    const form = blankCondition(condition.type);
    form.path = condition.path || '';
    form.text = condition.text || '';
    form.pattern = condition.pattern || '';
    form.list = (condition.exts || condition.codecs || []).join(', ');
    if (meta?.scale) {
      form.min = condition.min == null ? '' : trimNumber(condition.min / meta.scale);
      form.max = condition.max == null ? '' : trimNumber(condition.max / meta.scale);
    }
    form.value = condition.value !== false;
    return form;
  });
  const actions = (rule.actions || []).map(action => ({
    key: nextKey(), type: action.type, target_id: action.type === 'add_to_collection' ? action.collection_id : action.tag_id
  }));
  return {
    id: rule.id, name: rule.name, media_kind: rule.media_kind, enabled: rule.enabled,
    conditions: conditions.length ? conditions : [blankCondition()],
    actions: actions.length ? actions : [blankAction()]
  };
}

function scaledNumber(text, scale) {
  if (text === '' || text == null) return undefined;
  const value = Number(text);
  if (!Number.isFinite(value)) return NaN;
  return scale === MB ? Math.round(value * scale) : value * scale;
}

function splitList(text) {
  return String(text || '').split(/[\s,，、;；]+/).map(item => item.trim()).filter(Boolean);
}

// formToInput 把表单转成 SaveAutomationRule 的入参；校验交给后端（invalid_rule 带字段说明）。
export function formToInput(form) {
  const conditions = form.conditions.map(row => {
    const meta = conditionMeta(row.type);
    const condition = { type: row.type };
    switch (row.type) {
      case 'under_directory': condition.path = row.path; break;
      case 'name_contains': condition.text = row.text; break;
      case 'name_glob':
      case 'name_regex': condition.pattern = row.pattern; break;
      case 'extension_in': condition.exts = splitList(row.list); break;
      case 'video_codec_in': condition.codecs = splitList(row.list); break;
      case 'has_gps': condition.value = Boolean(row.value); break;
      default: {
        const min = scaledNumber(row.min, meta?.scale || 1);
        const max = scaledNumber(row.max, meta?.scale || 1);
        if (min !== undefined) condition.min = min;
        if (max !== undefined) condition.max = max;
      }
    }
    return condition;
  });
  const actions = form.actions.map(row => {
    const id = Number(row.target_id) || 0;
    return row.type === 'add_to_collection' ? { type: row.type, collection_id: id } : { type: row.type, tag_id: id };
  });
  return { id: form.id || 0, name: form.name, media_kind: form.media_kind, enabled: Boolean(form.enabled), conditions, actions };
}

const MAINTENANCE_ERROR = 'database is in maintenance mode';
export const RULE_ERROR_FALLBACK = '操作没有完成，请稍后重试';

// parseRuleError 解析后端错误：「invalid_rule: 字段: 说明」、rule_not_found、automation_run_busy。
// 其余错误一律给固定文案，不把后端原文（可能带路径或底层错误）摆到界面上（G-3）；数据库维护中单独说明。
export function parseRuleError(err) {
  const text = String(err?.message ?? err ?? '').trim();
  const match = /^invalid_rule: ([^:]+): (.*)$/s.exec(text);
  if (match) return { code: 'invalid_rule', field: match[1], message: match[2] };
  if (text.includes('rule_not_found')) return { code: 'rule_not_found', field: '', message: '这条规则已不存在，请刷新' };
  if (text.includes('automation_run_busy')) return { code: 'automation_run_busy', field: '', message: '已有一次规则运行在进行中' };
  if (text.includes(MAINTENANCE_ERROR)) return { code: '', field: '', message: translateBackendError(MAINTENANCE_ERROR) };
  return { code: '', field: '', message: RULE_ERROR_FALLBACK };
}

// fieldRow 从字段说明里取行号：conditions[2].pattern → { list: 'conditions', index: 2 }。
export function fieldRow(field) {
  const match = /^(conditions|actions)\[(\d+)\]/.exec(String(field || ''));
  return match ? { list: match[1], index: Number(match[2]) } : null;
}
