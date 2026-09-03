// 命令面板的进程内注册表（D-029）。各页面在 mounted / unmounted 注册与注销自己的
// 命令，面板只渲染这里的内容、不认识任何业务——新增一类命令不必改面板。
//
// 命令项固定为 { id, group, label, keywords, run, enabled? }：
//   id       全局唯一，同 id 只保留先注册的那一个（不同页面注册同一条命令时不会重复出现）
//   group    'navigate' | 'action' | 'task' | 'video'
//   label    面板里显示的文字，也是过滤的第一权重来源
//   keywords 额外的匹配词（拼音缩写、英文别名等），只参与过滤、不显示
//   run()    执行；面板先关闭再调用它
//   enabled() 可选，返回 false 时灰显不可执行

import { shallowRef } from 'vue';

// 分组的固定顺序与中文名。面板按这个顺序分段渲染。
export const COMMAND_GROUPS = [
  { key: 'navigate', label: '导航' },
  { key: 'action', label: '动作' },
  { key: 'task', label: '任务' },
  { key: 'video', label: '视频' }
];

const GROUP_KEYS = COMMAND_GROUPS.map(group => group.key);

// scopes 是普通 Map，不是响应式对象。version 存在的唯一理由是让 Vue 的 computed
// 能跟着注册表变化重算：读取入口 commandList() 先碰一下它就建立了依赖。
const version = shallowRef(0);
const scopes = new Map();
// 重新注册同一个 scope（任务组随事件刷新就是这样）不改变它的排序位置，
// 否则任务组每次事件到达都会跳到列表末尾。
let nextScopeOrder = 0;

function normalize(text) {
  return String(text ?? '').trim().toLowerCase();
}

// 注册项不合法是编码错误，直接抛：静默丢掉一条命令只会变成"面板里少了一项"的谜题。
function validateCommand(command, scopeKey) {
  if (!command || typeof command !== 'object') {
    throw new TypeError(`命令面板注册项必须是对象（scope=${scopeKey}）`);
  }
  if (!command.id) throw new TypeError(`命令面板注册项缺少 id（scope=${scopeKey}）`);
  if (!GROUP_KEYS.includes(command.group)) {
    throw new TypeError(`命令面板注册项 ${command.id} 的 group 无效：${command.group}`);
  }
  if (!command.label) throw new TypeError(`命令面板注册项 ${command.id} 缺少 label`);
  if (typeof command.run !== 'function') throw new TypeError(`命令面板注册项 ${command.id} 缺少 run()`);
  if (command.enabled !== undefined && typeof command.enabled !== 'function') {
    throw new TypeError(`命令面板注册项 ${command.id} 的 enabled 必须是函数`);
  }
  return command;
}

export function registerCommands(scopeKey, commands) {
  const key = String(scopeKey ?? '');
  if (!key) throw new TypeError('registerCommands 需要非空 scopeKey');
  const list = (commands || []).map(command => validateCommand(command, key));
  const existing = scopes.get(key);
  scopes.set(key, { order: existing ? existing.order : nextScopeOrder++, commands: list });
  version.value += 1;
}

export function unregisterCommands(scopeKey) {
  if (scopes.delete(String(scopeKey ?? ''))) version.value += 1;
}

// commandList 按「分组固定顺序 → scope 注册顺序 → scope 内声明顺序」拉平，
// 与面板的渲染顺序一致，方向键的上下移动才和眼睛看到的一致。
export function commandList() {
  void version.value;
  const entries = [...scopes.values()].sort((a, b) => a.order - b.order);
  const seen = new Set();
  const result = [];
  for (const groupKey of GROUP_KEYS) {
    for (const entry of entries) {
      for (const command of entry.commands) {
        if (command.group !== groupKey || seen.has(command.id)) continue;
        seen.add(command.id);
        result.push(command);
      }
    }
  }
  return result;
}

// 权重：label 前缀 > label 包含 > keywords 包含；同权重按 commandList 的顺序稳定排列。
function matchWeight(command, needle) {
  const label = normalize(command.label);
  if (label.startsWith(needle)) return 0;
  if (label.includes(needle)) return 1;
  const keywords = Array.isArray(command.keywords) ? command.keywords : [];
  return keywords.some(keyword => normalize(keyword).includes(needle)) ? 2 : -1;
}

export function filterCommands(query) {
  const needle = normalize(query);
  const all = commandList();
  if (!needle) return all;
  const scored = [];
  all.forEach((command, index) => {
    const weight = matchWeight(command, needle);
    if (weight < 0) return;
    scored.push({ command, weight, index });
  });
  scored.sort((a, b) => (a.weight - b.weight) || (a.index - b.index));
  return scored.map(item => item.command);
}

// 没有 enabled() 就是可执行。enabled() 自己抛错不兜：那是注册方的 bug。
export function isCommandEnabled(command) {
  if (typeof command?.enabled !== 'function') return true;
  return command.enabled() !== false;
}
