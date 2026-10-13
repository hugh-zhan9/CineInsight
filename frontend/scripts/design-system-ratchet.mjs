// 统一设计守卫的「待迁移清单」棘轮（D-UD-15）。design-system.test.mjs（规则 1–7）与
// src/components/unstyledControls.test.js（buttons）共用这里的读取与比对，口径只有一份。
//
// 清单在 design-system-pending/*.json，一个文件对应一个负责组；每条规则下按文件记「还剩几处」：
//   "pending": { "colors": { "components/VideoListPage.vue": 14, … }, … }
// 比对规则（每个文件、每条规则单独算）：
//   - 有违规却不在清单里 → 失败：新代码必须直接合规，不许往清单里加；
//   - 实际处数 > 清单记的 → 失败：这个文件又新增了违规，改掉新增的那几处；
//   - 实际处数 < 清单记的 → 失败：提示把数字降到实际值（只降不升，换装成果就此锁住）；
//   - 实际为 0 而清单里还有 → 失败：提示把这一项删掉；清单里的文件已不存在 → 同样要删。
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// 不写成 new URL('./…', import.meta.url)：vitest（Vite）会把这种写法改成资源地址，unstyledControls 里就读不到了。
export const PENDING_DIR = join(dirname(fileURLToPath(import.meta.url)), 'design-system-pending');
// 清单里允许出现的规则名：design-system.test.mjs 的七条 + unstyledControls.test.js 的 buttons。
export const PENDING_KEYS = ['colors', 'font-scale', 'z-index', 'vars', 'dialogs', 'page-header', 'wording', 'buttons'];

// 读全部清单：lists 为 规则 → (文件 → { count, owner })；problems 是清单本身的格式问题。
export function loadPendingLists(srcDir) {
  const known = new Set(PENDING_KEYS);
  const lists = new Map(PENDING_KEYS.map(key => [key, new Map()]));
  const problems = [];
  for (const name of readdirSync(PENDING_DIR).filter(entry => entry.endsWith('.json')).sort()) {
    const data = JSON.parse(readFileSync(join(PENDING_DIR, name), 'utf8'));
    for (const [key, entries] of Object.entries(data.pending || {})) {
      if (!known.has(key)) {
        problems.push(`${name}: 未知的清单 ${key}`);
        continue;
      }
      if (!entries || typeof entries !== 'object' || Array.isArray(entries)) {
        problems.push(`${name}: ${key} 要写成 { "文件": 处数 }`);
        continue;
      }
      const files = Object.keys(entries);
      // 与 Array.prototype.sort() 的默认顺序一致（按 UTF-16 码位），不按语言排序。
      if (files.some((file, index) => index > 0 && !(files[index - 1] < file))) {
        problems.push(`${name}: ${key} 要按文件名字母序（码位序）排列`);
      }
      for (const file of files) {
        const count = entries[file];
        if (!Number.isInteger(count) || count < 1) problems.push(`${name}: ${key} 里 ${file} 的处数要是正整数（已清零的请删掉这一项）`);
        if (!existsSync(join(srcDir, file))) problems.push(`${name}: ${key} 里的 ${file} 不存在（已删除的文件请从清单划掉）`);
        if (lists.get(key).has(file)) problems.push(`${name}: ${key} 里的 ${file} 同时出现在 ${lists.get(key).get(file).owner}`);
        lists.get(key).set(file, { count, owner: name });
      }
    }
  }
  return { lists, problems };
}

// 一条规则的棘轮比对。actual：文件 → 违规明细数组（处数 = 长度）；recorded：loadPendingLists 给的该规则清单。
export function ratchetFailures(rule, actual, recorded) {
  const failures = [];
  const sample = details => details.slice(0, 8).join('\n    ');
  for (const [file, details] of actual) {
    const entry = recorded.get(file);
    if (!entry) {
      failures.push(`[${rule}] ${file} 有 ${details.length} 处不合规（新代码不能进待迁移清单，请直接改）：\n    ${sample(details)}`);
    } else if (details.length > entry.count) {
      failures.push(`[${rule}] ${file} 现有 ${details.length} 处不合规，比 design-system-pending/${entry.owner} 记的 ${entry.count} 处多，请改掉新增的（清单只减不增）：\n    ${sample(details)}`);
    } else if (details.length < entry.count) {
      failures.push(`[${rule}] ${file} 只剩 ${details.length} 处了，请把 design-system-pending/${entry.owner} 里 ${rule} 的这一项从 ${entry.count} 改成 ${details.length}`);
    }
  }
  for (const [file, entry] of recorded) {
    if (!actual.has(file)) failures.push(`[${rule}] ${file} 已合规，请从 design-system-pending/${entry.owner} 的 ${rule} 清单删除`);
  }
  return failures;
}

// 清单里某条规则还剩的总处数（摘要用）。
export function pendingTotal(recorded) {
  let total = 0;
  for (const { count } of recorded.values()) total += count;
  return total;
}
