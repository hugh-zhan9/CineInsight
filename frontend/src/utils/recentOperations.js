import { reactive } from 'vue';

// 「最近操作」（统一设计 D-UD-09）：任务中心事后撤销的来源。只在前端内存里，留最近 20 条可撤销的操作，
// 新的在前；不写后端，应用重启后清空（任务中心要把这一点写在页签上）。
// 生产方（删除批次、元数据批量编辑，B 组接入）在操作成功后 push({ label, undo })：undo 调既有的恢复接口
// （RestoreTrashBatch / UndoMetadataBatchEdit），返回 Promise。用法：import * as recentOperations from …。
export const RECENT_OPERATIONS_LIMIT = 20;

const state = reactive({ items: [] });
const undoers = new Map();
let nextID = 1;

// 记一条可撤销的操作，返回它的 id；label 为空或 undo 不是函数时不记，返回 null。
export function push({ label, undo } = {}) {
  const text = String(label ?? '').trim();
  if (!text || typeof undo !== 'function') return null;
  const id = nextID++;
  undoers.set(id, undo);
  state.items.unshift({ id, label: text, at: Date.now(), undoing: false });
  for (const dropped of state.items.splice(RECENT_OPERATIONS_LIMIT)) undoers.delete(dropped.id);
  return id;
}

// 新的在前。读的是响应式状态，组件里放在 computed 里会跟着变。
export function list() {
  return state.items.map(item => ({ ...item }));
}

// 撤销一条：执行一次，成功后从列表移除；失败留在列表里（可再试）并把错误抛给调用方提示。
// 已不在列表里或正在撤销时返回 false，不重复执行。
export async function undo(id) {
  const item = state.items.find(entry => entry.id === id);
  const run = undoers.get(id);
  if (!item || !run || item.undoing) return false;
  item.undoing = true;
  try {
    await run();
  } catch (err) {
    item.undoing = false;
    throw err;
  }
  const index = state.items.findIndex(entry => entry.id === id);
  if (index !== -1) state.items.splice(index, 1);
  undoers.delete(id);
  return true;
}

// 清空（测试用；也等价于应用重启）。
export function clear() {
  state.items.splice(0, state.items.length);
  undoers.clear();
}
