// P-002 的行为保持网：巨型组件拆分会把大段模板搬到子组件里，搬漏一段的最直接
// 症状就是某个 data-test 钩子消失，而挂在它上面的测试要么找不到元素、要么静默
// 变成空断言。这里把拆分前的钩子集合钉成基线：允许新增，不允许缺失。
import assert from 'node:assert/strict';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const srcDir = fileURLToPath(new URL('../src', import.meta.url));
const baselinePath = fileURLToPath(
  new URL('../../.loopx/workspace/2026-09-02-capability-batch/baseline/data-test-set-2026-09-02.txt', import.meta.url)
);

function collectVueFiles(dir) {
  const found = [];
  for (const entry of readdirSync(dir).sort()) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      found.push(...collectVueFiles(full));
      continue;
    }
    if (entry.endsWith('.vue')) found.push(full);
  }
  return found;
}

const present = new Map();
for (const file of collectVueFiles(srcDir)) {
  const source = readFileSync(file, 'utf8');
  for (const match of source.matchAll(/data-test="[^"]*"/g)) {
    if (!present.has(match[0])) present.set(match[0], file);
  }
}

const baseline = readFileSync(baselinePath, 'utf8')
  .split('\n')
  .map(line => line.trim())
  .filter(Boolean);

assert.ok(baseline.length > 0, 'data-test baseline should not be empty');

const missing = baseline.filter(hook => !present.has(hook));
assert.deepEqual(
  missing,
  [],
  `拆分后丢失了 ${missing.length} 个 data-test 钩子（基线 ${baseline.length} 个）：\n${missing.join('\n')}`
);

console.log(`data-test set tests passed (${baseline.length} baseline hooks, ${present.size} present)`);
