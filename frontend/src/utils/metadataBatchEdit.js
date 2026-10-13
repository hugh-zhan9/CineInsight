// 元数据批量编辑（D-MC-04 / D-MC-05）的字段、动作与文案。
// 取值与后端 services/metadata_batch_edit.go 的 BatchEditField* / BatchEditAction* 一一对应。

// 每种媒体可改的字段（§2.4 操作表）；kind 决定第一列下拉里有哪些。
export const BATCH_EDIT_FIELDS = {
  video: [
    { value: 'year', label: '年份', scalar: true },
    { value: 'rating', label: '个人评分', scalar: true },
    { value: 'tag', label: '标签' },
    { value: 'person', label: '人物' },
    { value: 'collection', label: '作品集' }
  ],
  image: [
    { value: 'tag', label: '标签' },
    { value: 'person', label: '人物' },
    { value: 'rating', label: '个人评分', scalar: true }
  ]
};

export function batchFieldIsScalar(field) {
  return field === 'year' || field === 'rating';
}

// 动作：标量字段是「设为 / 清空」，关联字段是「加上 / 去掉」（作品集叫「加入 / 移出」）。
export function batchActionOptions(field) {
  if (batchFieldIsScalar(field)) return [{ value: 'set', label: '设为' }, { value: 'clear', label: '清空' }];
  if (field === 'collection') return [{ value: 'add', label: '加入' }, { value: 'remove', label: '移出' }];
  return [{ value: 'add', label: '加上' }, { value: 'remove', label: '去掉' }];
}

export function batchFieldLabel(field) {
  for (const fields of Object.values(BATCH_EDIT_FIELDS)) {
    const found = fields.find(item => item.value === field);
    if (found) return found.label;
  }
  return field;
}

export function batchActionLabel(field, action) {
  return batchActionOptions(field).find(item => item.value === action)?.label || action;
}

// 评分下拉：0–10，步长 0.5。
export const BATCH_RATING_OPTIONS = Array.from({ length: 21 }, (_, index) => index / 2);

// 一个操作的一句话描述，例如「年份设为 2001」「标签加上 A、B」「个人评分清空」。
export function describeBatchOp(op) {
  const field = batchFieldLabel(op?.field);
  const action = batchActionLabel(op?.field, op?.action);
  if (batchFieldIsScalar(op?.field)) {
    return op?.action === 'set' ? `${field}${action} ${op?.value ?? ''}`.trim() : `${field}${action}`;
  }
  const names = (op?.names || []).filter(Boolean);
  return names.length ? `${field}${action} ${names.join('、')}` : `${field}${action}`;
}

// 撤销时后端回「这批已经撤销过了」（services.ErrBatchAlreadyUndone，错误文本 batch_already_undone）。
export function isBatchAlreadyUndone(err) {
  return String(err?.message || err || '').includes('batch_already_undone');
}

// 批量编辑接口的错误码 → 中文。invalid_batch_edit 后面带的是后端给的中文原因。
export function batchEditErrorMessage(err) {
  const text = String(err?.message || err || '');
  if (text.includes('batch_edit_busy')) return '另一个批量编辑或撤销正在进行，请稍后再试。';
  if (isBatchAlreadyUndone(err)) return '这次批量编辑已经撤销过了。';
  if (text.includes('batch_edit_not_found')) return '这次批量编辑的记录已不在（只保留最近 20 次）。';
  const invalid = text.match(/invalid_batch_edit:\s*(.+)$/);
  if (invalid) return `操作无法执行：${invalid[1]}`;
  if (text.includes('invalid_batch_edit')) return '操作无法执行，请检查后重试。';
  return '';
}

// 撤销结果的一句话。
export function undoResultText(result) {
  const restored = Number(result?.restored || 0);
  const skipped = Number(result?.changed_since || 0);
  const base = `已撤销：恢复 ${restored} 处`;
  const tail = skipped > 0 ? `；${skipped} 处在这之后又被改过，已跳过` : '';
  return result?.cancelled ? `撤销中途停止：已恢复 ${restored} 处${tail}，可以再点撤销继续。` : `${base}${tail}。`;
}
