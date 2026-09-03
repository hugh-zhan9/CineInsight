// OPFS（源私有文件系统）落盘。只在浏览器里跑，单测用假 writer 代替。
//
// 为什么不攒在内存里最后一次成文件：一部两小时的片子几个 GB，堆在内存里
// 要么直接崩，要么把整个浏览器拖垮。OPFS 让分片边取边落盘，内存里只留
// 还没轮到写的那几片。断点续传也靠它——文件本身就是已完成部分的证据。

const DOWNLOAD_DIR = 'cineinsight-downloads';

async function downloadDirectory() {
  const root = await navigator.storage.getDirectory();
  return root.getDirectoryHandle(DOWNLOAD_DIR, { create: true });
}

// variant 用来区分同一个任务的两份产物：原始下载（空）与转封装输出（'mp4'）。
// 转封装要边读原始文件边写新文件，两份必须同时存在。
function taskFileName(taskId, variant) {
  return variant ? `${taskId}.${variant}.part` : `${taskId}.part`;
}

export async function getTaskFileHandle(taskId, { create = true, variant = '' } = {}) {
  const dir = await downloadDirectory();
  return dir.getFileHandle(taskFileName(taskId, variant), { create });
}

// 同步访问句柄只能在专用 Worker 里拿，写入是同步的，速度也最好。
export async function openTaskWriter(taskId, { truncateTo = null, variant = '' } = {}) {
  const fileHandle = await getTaskFileHandle(taskId, { create: true, variant });
  const handle = await fileHandle.createSyncAccessHandle();

  // 续跑时把文件截到账本记的字节数：文件里可能有上次崩溃时写了一半的分片，
  // 不截掉就会在续传处多出一段垃圾，而且是静默的。
  if (truncateTo !== null) handle.truncate(truncateTo);
  let offset = handle.getSize();

  return {
    get size() {
      return offset;
    },
    write(bytes) {
      const written = handle.write(bytes, { at: offset });
      // 短写必须当成错误抛出去，不能只把偏移推进实际写入的字节数：
      // 账本按"这一片写成功了"记账，而磁盘上少了一截，两边一旦对不上，
      // 续传就会在错误的位置接着写，产出的坏文件还带着"已完成"。
      if (written !== bytes.length) {
        throw new Error(`落盘不完整：应写 ${bytes.length} 字节，实际 ${written}（磁盘空间可能不够）`);
      }
      offset += written;
      return written;
    },
    flush() {
      handle.flush();
    },
    close() {
      try {
        handle.flush();
      } finally {
        handle.close();
      }
    }
  };
}

// 任务成文件：句柄关闭之后在主线程（offscreen 文档）里调，拿到 File 再转 blob URL。
export async function readTaskFile(taskId, variant = '') {
  const fileHandle = await getTaskFileHandle(taskId, { create: false, variant });
  return fileHandle.getFile();
}

export async function removeTaskFile(taskId, variant = '') {
  try {
    const dir = await downloadDirectory();
    await dir.removeEntry(taskFileName(taskId, variant));
  } catch {
    // 文件本来就不在（任务从未开始、或已经清理过）不是错误。
  }
}

// 清掉一个任务的全部产物（原始 + 转封装输出）。
export async function removeAllTaskFiles(taskId) {
  await removeTaskFile(taskId, '');
  await removeTaskFile(taskId, 'mp4');
}

// 任务列表里没有的残留文件清掉：崩溃、强制关浏览器都会留下 .part。
export async function pruneOrphanFiles(liveTaskIds) {
  const keep = new Set();
  for (const id of liveTaskIds) {
    keep.add(taskFileName(id, ''));
    keep.add(taskFileName(id, 'mp4'));
  }
  const dir = await downloadDirectory();
  const removed = [];
  for await (const [name, handle] of dir.entries()) {
    if (handle.kind !== 'file' || keep.has(name)) continue;
    try {
      await dir.removeEntry(name);
      removed.push(name);
    } catch {
      // 正被别的句柄占着，下次再说。
    }
  }
  return removed;
}

export async function storageUsage() {
  if (!navigator.storage || !navigator.storage.estimate) return { usage: 0, quota: 0 };
  const estimate = await navigator.storage.estimate();
  return { usage: estimate.usage || 0, quota: estimate.quota || 0 };
}
