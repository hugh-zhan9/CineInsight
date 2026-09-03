// HLS AES-128 解密。用 WebCrypto，不引入任何第三方密码学实现。
//
// 只支持 METHOD=AES-128（KEYFORMAT=identity）。SAMPLE-AES 与 DRM 在解析阶段
// 就被判为不可下载（D-B02），不会走到这里。

const BLOCK_SIZE = 16;

export async function importAesKey(rawKey) {
  const bytes = toUint8(rawKey);
  if (bytes.length !== BLOCK_SIZE) {
    throw new Error(`AES-128 密钥长度应为 16 字节，实际 ${bytes.length} 字节`);
  }
  // encrypt 用途是"无补齐解密"那条路要用的，见 decryptWithoutPadding。
  return crypto.subtle.importKey('raw', bytes, { name: 'AES-CBC' }, false, ['encrypt', 'decrypt']);
}

// EXT-X-KEY 没写 IV 时，IV 是该分片媒体序号的 128 位大端表示。
export function deriveIV(mediaSequence) {
  const iv = new Uint8Array(BLOCK_SIZE);
  let value = BigInt(Math.max(0, Math.trunc(Number(mediaSequence) || 0)));
  for (let index = BLOCK_SIZE - 1; index >= 0 && value > 0n; index -= 1) {
    iv[index] = Number(value & 0xffn);
    value >>= 8n;
  }
  return iv;
}

export function ivFromHex(hex) {
  const text = String(hex || '').replace(/^0x/i, '');
  if (!/^[0-9a-f]{32}$/i.test(text)) return null;
  const iv = new Uint8Array(BLOCK_SIZE);
  for (let index = 0; index < BLOCK_SIZE; index += 1) {
    iv[index] = Number.parseInt(text.slice(index * 2, index * 2 + 2), 16);
  }
  return iv;
}

// 解密一个分片。
//
// 规范要求分片按 PKCS#7 补齐，WebCrypto 也按这个假设去剥补齐位。现实里存在没补齐的
// 分片（长度正好是 16 的整数倍且末块就是数据），那种情况下 WebCrypto 会因为补齐校验
// 失败直接抛错。所以先按规范解，失败了再走一条不校验补齐的路——不是"猜一个结果"，
// 而是同一份密文的另一种同样确定的解法。两条都失败才算这条流解不开。
export async function decryptSegment(cryptoKey, iv, data) {
  const bytes = toUint8(data);
  if (bytes.length === 0) return bytes;
  if (bytes.length % BLOCK_SIZE !== 0) {
    throw new Error(`加密分片长度 ${bytes.length} 不是 16 的整数倍，数据不完整`);
  }
  try {
    const plain = await crypto.subtle.decrypt({ name: 'AES-CBC', iv }, cryptoKey, bytes);
    return new Uint8Array(plain);
  } catch {
    return decryptWithoutPadding(cryptoKey, iv, bytes);
  }
}

// 在密文末尾补一个"解出来正好是一整块 0x10"的块，让 WebCrypto 的补齐校验必然通过、
// 并且剥掉的正是这个补上去的块，原始数据一个字节不少地留下来。
//
// C' = E(0x10×16 XOR Cn) 时，D(C') XOR Cn = 0x10×16，即一整块合法的 PKCS#7 补齐。
async function decryptWithoutPadding(cryptoKey, iv, bytes) {
  const lastBlock = bytes.slice(bytes.length - BLOCK_SIZE);
  const padPlain = new Uint8Array(BLOCK_SIZE).fill(BLOCK_SIZE);
  // AES-CBC 加密会自己再补一块，只取前 16 字节就是我们要的 C'。
  const encrypted = new Uint8Array(
    await crypto.subtle.encrypt({ name: 'AES-CBC', iv: lastBlock }, cryptoKey, padPlain)
  );
  const combined = new Uint8Array(bytes.length + BLOCK_SIZE);
  combined.set(bytes, 0);
  combined.set(encrypted.slice(0, BLOCK_SIZE), bytes.length);
  const plain = await crypto.subtle.decrypt({ name: 'AES-CBC', iv }, cryptoKey, combined);
  return new Uint8Array(plain);
}

function toUint8(value) {
  if (value instanceof Uint8Array) return value;
  if (value instanceof ArrayBuffer) return new Uint8Array(value);
  if (ArrayBuffer.isView(value)) return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  throw new TypeError('需要 ArrayBuffer 或 TypedArray');
}
