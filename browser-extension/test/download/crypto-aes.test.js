import test from 'node:test';
import assert from 'node:assert/strict';

import { importAesKey, deriveIV, ivFromHex, decryptSegment } from '../../src/download/crypto-aes.js';

const RAW_KEY = new Uint8Array([1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16]);

async function encryptPadded(key, iv, plain) {
  return new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-CBC', iv }, key, plain));
}

test('按规范补齐的分片能原样解出来', async () => {
  const key = await importAesKey(RAW_KEY);
  const iv = deriveIV(3);
  const plain = new Uint8Array(1000).map((_, index) => index % 251);
  const cipher = await encryptPadded(key, iv, plain);

  const decrypted = await decryptSegment(key, iv, cipher);
  assert.deepEqual(decrypted, plain);
});

test('没有补齐的分片走第二条路，数据一个字节不少', async () => {
  const key = await importAesKey(RAW_KEY);
  const iv = deriveIV(0);
  // 全零明文：末块解出来最后一字节是 0x00，PKCS#7 校验必然失败，
  // 这样才真的走到不校验补齐的那条路上。
  const plain = new Uint8Array(64);
  const padded = await encryptPadded(key, iv, plain);
  // 去掉 WebCrypto 自己补的那一整块，得到"没有补齐"的密文
  const cipher = padded.slice(0, padded.length - 16);
  assert.equal(cipher.length, 64);

  // 先确认规范路径确实解不开，否则这个测试就没测到东西
  await assert.rejects(crypto.subtle.decrypt({ name: 'AES-CBC', iv }, key, cipher));

  const decrypted = await decryptSegment(key, iv, cipher);
  assert.equal(decrypted.length, 64);
  assert.deepEqual(decrypted, plain);
});

test('IV 按媒体序号推导，大端 128 位', () => {
  assert.deepEqual(deriveIV(0), new Uint8Array(16));
  const iv1 = deriveIV(1);
  assert.equal(iv1[15], 1);
  assert.equal(iv1[14], 0);
  const iv258 = deriveIV(258);
  assert.equal(iv258[15], 2);
  assert.equal(iv258[14], 1);
});

test('IV 十六进制串解析，长度不对返回 null', () => {
  assert.deepEqual(ivFromHex('0x000102030405060708090a0b0c0d0e0f'), new Uint8Array([0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15]));
  assert.equal(ivFromHex('0x00ff'), null);
  assert.equal(ivFromHex(''), null);
  assert.equal(ivFromHex(null), null);
});

test('密钥长度不对时明确报错，不静默降级', async () => {
  await assert.rejects(() => importAesKey(new Uint8Array(8)), /16 字节/);
});

test('长度不是 16 倍数的密文判为数据不完整', async () => {
  const key = await importAesKey(RAW_KEY);
  await assert.rejects(() => decryptSegment(key, deriveIV(0), new Uint8Array(17)), /不是 16 的整数倍/);
});

test('空分片直接返回空，不当成错误', async () => {
  const key = await importAesKey(RAW_KEY);
  const result = await decryptSegment(key, deriveIV(0), new Uint8Array(0));
  assert.equal(result.length, 0);
});
