// G-5 绑定守卫（P-040）：前端从 wailsjs/go/main/App 取用的每个名字都必须是 App.d.ts 里真实导出的函数，
// 反过来 App.d.ts 的每个导出也必须至少被前端非测试代码用到一次——不留死绑定。
// 识别的写法：import { A, B as C }、import * as ns 之后的 ns.X、const { X } = await import(...)。
// 测试文件（*.test.*，含 vi.mock）不算使用。出现其他写法时直接失败，免得守卫被新写法悄悄绕过。
import assert from 'node:assert/strict';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const frontendDir = fileURLToPath(new URL('..', import.meta.url));
const declarations = readFileSync(join(frontendDir, 'wailsjs/go/main/App.d.ts'), 'utf8');
const exported = new Set([...declarations.matchAll(/^export function (\w+)\(/gm)].map(m => m[1]));
assert.ok(exported.size > 0, 'App.d.ts should export bindings');

// 白名单只放「已裁决保留、界面随后接入」的绑定，每项写明原因；目前为空（2026-09-30：「取消关联」已接入）。
const allowedUnused = new Map([]);
// 待裁决：前端已不用、去留还没定的绑定；目前为空（SearchSubtitleMatchesWithFilters 已由主代理裁决删除）。
const pendingRuling = new Map([]);

function collectSources(dir) {
  const found = [];
  for (const entry of readdirSync(dir).sort()) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) found.push(...collectSources(full));
    else if (/\.(vue|js|mjs|ts)$/.test(entry) && !/\.test\.[cm]?[jt]s$/.test(entry)) found.push(full);
  }
  return found;
}

const APP_MODULE = String.raw`['"][^'"]*wailsjs\/go\/main\/App['"]`;
const namedImport = new RegExp(String.raw`import\s*\{([^}]*)\}\s*from\s*` + APP_MODULE, 'g');
const namespaceImport = new RegExp(String.raw`import\s*\*\s*as\s+(\w+)\s+from\s*` + APP_MODULE, 'g');
const dynamicImport = new RegExp(String.raw`const\s*\{([^}]*)\}\s*=\s*await\s+import\(\s*` + APP_MODULE + String.raw`\s*\)`, 'g');
const anyMention = /wailsjs\/go\/main\/App['"]/g;

const used = new Map(); // name -> [file]
function record(name, file) {
  if (!used.has(name)) used.set(name, []);
  used.get(name).push(file);
}
function namesIn(list, separator) {
  return list.split(',').map(part => part.trim().split(separator)[0].trim()).filter(Boolean);
}

const sources = [...collectSources(join(frontendDir, 'src')), join(frontendDir, 'short.html')];
for (const file of sources) {
  const source = readFileSync(file, 'utf8');
  const rel = relative(frontendDir, file);
  let recognised = 0;
  for (const m of source.matchAll(namedImport)) {
    recognised++;
    for (const name of namesIn(m[1], /\s+as\s+/)) record(name, rel);
  }
  for (const m of source.matchAll(dynamicImport)) {
    recognised++;
    for (const name of namesIn(m[1], /\s*:\s*/)) record(name, rel);
  }
  for (const m of source.matchAll(namespaceImport)) {
    recognised++;
    for (const call of source.matchAll(new RegExp(String.raw`\b${m[1]}\.(\w+)\b`, 'g'))) record(call[1], rel);
  }
  const mentions = [...source.matchAll(anyMention)].length;
  assert.equal(recognised, mentions, `${rel}: 有 ${mentions - recognised} 处 App 绑定导入不是守卫能识别的写法`);
}
assert.ok(used.size > 0, 'the guard should find at least one binding import');

const missing = [...used.keys()].filter(name => !exported.has(name)).sort();
assert.deepEqual(missing, [], `前端导入了 App.d.ts 里不存在的绑定：\n${missing.map(n => `${n}（${used.get(n).join(', ')}）`).join('\n')}`);

for (const name of [...allowedUnused.keys(), ...pendingRuling.keys()]) {
  assert.ok(exported.has(name), `白名单里的 ${name} 已不在 App.d.ts 中，请同步删掉白名单项`);
  assert.ok(!used.has(name), `${name} 已经有前端调用，请把它移出白名单`);
}
const unused = [...exported].filter(name => !used.has(name) && !allowedUnused.has(name) && !pendingRuling.has(name)).sort();
assert.deepEqual(unused, [], `App.d.ts 里有前端从未使用的死绑定（${unused.length} 个）：\n${unused.join('\n')}`);

console.log(`bindings usage tests passed (${exported.size} exports, ${used.size} used, ${allowedUnused.size} allow-listed, ${pendingRuling.size} pending ruling)`);
