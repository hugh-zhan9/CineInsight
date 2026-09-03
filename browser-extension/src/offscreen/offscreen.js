// offscreen 文档：下载任务的宿主。
//
// service worker 随时会被浏览器回收，长下载放在里面必然半途而废。offscreen 文档
// 只要不关就一直活着，每个运行中的任务在它下面挂一个专用 Worker（OPFS 的同步访问
// 句柄只能在专用 Worker 里拿）。
//
// 这里只做三件事：转发控制指令、转发进度、任务完成后把 OPFS 文件变成 blob 地址
// 交回 service worker（chrome.downloads 只能在 service worker 里调）。

import { MSG } from '../common/constants.js';
import { readTaskFile, removeTaskFile, removeAllTaskFiles } from '../download/opfs-store.js';
import { mimeForFilename } from '../common/mime.js';

const workers = new Map(); // taskId -> Worker
const blobUrls = new Map(); // taskId -> blob url

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (!message || message.target !== 'offscreen') return false;

  switch (message.type) {
    case MSG.OFFSCREEN_START:
      startTask(message.task);
      sendResponse({ ok: true });
      return false;
    case MSG.OFFSCREEN_CONTROL:
      controlTask(message.taskId, message.action);
      sendResponse({ ok: true });
      return false;
    case 'revoke-blob':
      revokeBlob(message.taskId);
      sendResponse({ ok: true });
      return false;
    case 'ping':
      sendResponse({ ok: true, running: [...workers.keys()] });
      return false;
    default:
      return false;
  }
});

function startTask(task) {
  if (!task || !task.id) return;
  stopWorker(task.id);

  const worker = new Worker(chrome.runtime.getURL('src/offscreen/download-worker.js'), { type: 'module' });
  workers.set(task.id, worker);

  worker.onmessage = (event) => handleWorkerMessage(task, event.data);
  worker.onerror = (event) => {
    report(task.id, { state: 'failed', error: event.message || 'worker 异常退出' });
    stopWorker(task.id);
  };
  worker.postMessage({ type: 'start', task });
}

function controlTask(taskId, action) {
  const worker = workers.get(taskId);
  if (worker) worker.postMessage({ type: 'control', action });
  if (action === 'cancel' && !worker) {
    // worker 已经不在了（暂停后又取消），残留的分片文件还得清掉。
    removeTaskFile(taskId).catch(() => {});
  }
}

async function handleWorkerMessage(task, data) {
  if (!data || !data.type) return;

  switch (data.type) {
    case 'hosts': {
      // 等 service worker 把请求头规则装好再放行分片请求。
      const worker = workers.get(task.id);
      try {
        await chrome.runtime.sendMessage({
          type: MSG.OFFSCREEN_HOSTS,
          target: 'background',
          taskId: task.id,
          hosts: data.hosts || []
        });
      } catch {
        // service worker 正在重启：不能把任务卡死，让它按原有规则继续，
        // 取不到分片会以 403 明确失败。
      }
      if (worker) worker.postMessage({ type: 'hosts-ready' });
      return;
    }
    case 'progress':
      report(task.id, { state: 'running', progress: data.progress });
      return;
    case 'remuxing':
      report(task.id, { state: 'remuxing' });
      return;
    case 'remux-progress':
      report(task.id, { state: 'remuxing', remux: data.progress });
      return;
    case 'stopped':
      stopWorker(task.id);
      report(task.id, {
        state: data.reason === 'paused' ? 'paused' : 'canceled',
        progress: data.progress
      });
      if (data.reason !== 'paused') await removeAllTaskFiles(task.id).catch(() => {});
      return;
    case 'error':
      stopWorker(task.id);
      report(task.id, { state: 'failed', error: data.message, progress: data.progress });
      return;
    case 'done':
      stopWorker(task.id);
      // variant 指明交付哪一份产物：TS 转封装之后交的是 mp4 那一份。
      await finishTask(task, data, data.variant || '');
      return;
    default:
  }
}

// 落盘完成：OPFS 文件转成 blob 地址交回 service worker，由它调 chrome.downloads
// 交给浏览器的下载器。blob 地址在下载真正完成之前不能撤销，否则下载会中断，
// 所以撤销由 service worker 在下载结束后回头通知。
async function finishTask(task, data, variant = '') {
  try {
    const file = await readTaskFile(task.id, variant);
    // slice 带上 contentType 会返回一个标了类型的 Blob，且不复制数据（几个 GB
    // 的文件也是 O(1)）。不标类型的话浏览器按 text/plain 处理，产物就成了 .txt。
    const typed = file.slice(0, file.size, mimeForFilename(task.filename));
    const url = URL.createObjectURL(typed);
    blobUrls.set(task.id, url);
    report(task.id, {
      state: 'writing',
      progress: data.progress,
      blobUrl: url,
      totalBytes: file.size
    });
  } catch (error) {
    report(task.id, { state: 'failed', error: `落盘文件读取失败：${error.message || error}` });
  }
}

function revokeBlob(taskId) {
  const url = blobUrls.get(taskId);
  if (url) {
    URL.revokeObjectURL(url);
    blobUrls.delete(taskId);
  }
  removeAllTaskFiles(taskId).catch(() => {});
}

function stopWorker(taskId) {
  const worker = workers.get(taskId);
  if (!worker) return;
  worker.terminate();
  workers.delete(taskId);
}

function report(taskId, payload) {
  chrome.runtime.sendMessage({
    type: MSG.OFFSCREEN_PROGRESS,
    target: 'background',
    taskId,
    ...payload
  }).catch(() => {
    // service worker 正好在重启，下一次进度会补上。
  });
}

// 文档一就绪就告诉 service worker，它可能在等着派任务。
chrome.runtime.sendMessage({ type: MSG.OFFSCREEN_READY, target: 'background' }).catch(() => {});
