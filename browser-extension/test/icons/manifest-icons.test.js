import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const manifest = JSON.parse(readFileSync(path.join(root, 'manifest.json'), 'utf8'));

function readPngHeader(buf) {
  const signature = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  assert.ok(buf.subarray(0, 8).equals(signature), 'PNG 文件头不对');
  assert.equal(buf.toString('latin1', 12, 16), 'IHDR');
  return { width: buf.readUInt32BE(16), height: buf.readUInt32BE(20), colorType: buf[25] };
}

test('manifest 声明的每一档图标都存在，像素尺寸与声明一致，且带透明通道', () => {
  assert.deepEqual(Object.keys(manifest.icons), ['16', '32', '48', '128']);
  for (const [size, rel] of Object.entries(manifest.icons)) {
    const { width, height, colorType } = readPngHeader(readFileSync(path.join(root, rel)));
    assert.equal(width, Number(size), `${rel} 宽度`);
    assert.equal(height, Number(size), `${rel} 高度`);
    assert.equal(colorType, 6, `${rel} 应为 RGBA`);
  }
});

test('工具栏按钮与扩展本体用同一套图标文件', () => {
  assert.deepEqual(manifest.action.default_icon, manifest.icons);
});
