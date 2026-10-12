// 失联数据治理（docs/loopx/design/2026-10-12-data-care，D-LD-*）的界面文案与小工具。
// 原因码、配对结果与跳过原因都是后端的固定码，这里只负责翻成中文。

export const LOST_PURGE_CONFIRM_TEXT = '清除记录';

export const LOST_REASON_LABELS = {
  offline: '卷未挂载',
  read_error: '读取出错',
  missing: '文件不见',
  removed_root: '目录已移除',
  outside_roots: '不在扫描目录',
  unknown: '原因未知'
};

// 报告里原因 chip 的固定顺序：先是只能等的两类，再是可处理的四类。
export const LOST_REASON_ORDER = ['offline', 'read_error', 'missing', 'removed_root', 'outside_roots', 'unknown'];

export const LOST_MATCH_LABELS = {
  exact: '相对路径与大小一致',
  size_mismatch: '大小不同',
  name_size: '同名同大小',
  size_only: '可能（仅大小相同）',
  ambiguous: '多个候选'
};

export const LOST_SKIP_LABELS = {
  no_longer_lost: '已不再失联',
  volume_offline: '所在的卷没挂载',
  file_present: '文件还在原处',
  enhancement_active: '有进行中的超分任务',
  target_missing: '新位置的文件不见了',
  size_mismatch: '大小与记录不一致',
  outside_roots: '新位置不在扫描目录内',
  occupied: '新位置已被另一条记录占用',
  error: '处理失败'
};

const ERROR_TEXT = {
  lost_media_busy: '已有找回、清除或回收在进行，请等它结束或取消。',
  lost_purge_unconfirmed: `请输入「${LOST_PURGE_CONFIRM_TEXT}」确认。`
};

// lostMediaErrorText 只把后端固定码的说明交给界面；其余错误（可能带路径或底层原文）一律用 fallback（G-3）。
export function lostMediaErrorText(err, fallback) {
  const text = String(err?.message ?? err ?? '');
  const match = /^(lost_[a-z_]+): (.+)$/s.exec(text);
  if (!match) return fallback;
  return ERROR_TEXT[match[1]] || match[2];
}

// reasonChips 把 { reason: count } 按固定顺序排成 chip 列表。
export function reasonChips(reasons) {
  return LOST_REASON_ORDER
    .filter(key => (reasons?.[key] || 0) > 0)
    .map(key => ({ key, label: LOST_REASON_LABELS[key], count: reasons[key], actionable: key !== 'offline' && key !== 'read_error' }));
}

// skippedSummary 把批结果里跳过与失败的条目按原因归并：[{ reason, label, count }]。
export function skippedSummary(result) {
  const counts = new Map();
  for (const item of result?.items || []) {
    counts.set(item.reason, (counts.get(item.reason) || 0) + 1);
  }
  return [...counts].map(([reason, count]) => ({ reason, label: LOST_SKIP_LABELS[reason] || '其他原因', count }));
}

// collectLostItems 逐页读出一个目录分组的全部条目（ListLostMediaItems 按 id 键集分页）。
export async function collectLostItems(list, kind, directory, pageSize = 500) {
  const items = [];
  let afterID = 0;
  for (;;) {
    const page = await list({ kind, directory, after_id: afterID, limit: pageSize });
    const rows = page?.items || [];
    items.push(...rows);
    if (!page?.has_more || rows.length === 0) return items;
    afterID = rows[rows.length - 1].id;
  }
}
