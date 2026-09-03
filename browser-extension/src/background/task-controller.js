// 下载任务的账本与调度。
//
// service worker 会被回收，所以任务状态必须落 chrome.storage.local：重启后要能
// 把"正在跑什么、跑到哪了"重新拼出来。真正的传输在 offscreen 文档里，这里只管
// 派活、收进度、把完成的文件交给 chrome.downloads。

import { MSG, TASK_STATE, TERMINAL_STATES, STORAGE_KEYS } from '../common/constants.js';
import { buildFilename, uniqueFilename, hostOf } from '../common/url.js';
import { loadSettings } from '../common/settings.js';
import { HeaderRuleManager } from './dnr-headers.js';

// 同时跑几个任务。分片本身已经并发，任务再多只会互相抢带宽，还更容易触发 CDN 限流。
const MAX_ACTIVE_TASKS = 2;
const MAX_TASK_HISTORY = 100;

export class TaskController {
  constructor(chromeApi) {
    this.chromeApi = chromeApi;
    this.tasks = new Map(); // taskId -> task
    this.headerRules = new HeaderRuleManager(chromeApi);
    this.offscreenReady = null;
    this.listeners = new Set();
    this.loaded = false;
  }

  async load() {
    if (this.loaded) return;
    const stored = await this.chromeApi.storage.local.get(STORAGE_KEYS.TASKS);
    const list = Array.isArray(stored[STORAGE_KEYS.TASKS]) ? stored[STORAGE_KEYS.TASKS] : [];
    for (const task of list) {
      // 进程重启时正在跑的任务不会自己接着跑：worker 已经没了。落成暂停，
      // 让用户自己决定要不要续——悄悄替他重启一个吃带宽的任务不合适。
      if (task.state === TASK_STATE.RUNNING || task.state === 'writing') {
        task.state = TASK_STATE.PAUSED;
        task.error = '浏览器重启或扩展重载，任务已暂停，可继续';
      }
      this.tasks.set(task.id, task);
    }
    this.loaded = true;
    await this.headerRules.clearAll();
  }

  onChange(listener) {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  notify() {
    const snapshot = this.list();
    for (const listener of this.listeners) {
      try {
        listener(snapshot);
      } catch {
        // 某个订阅方炸了不能连累其他订阅方。
      }
    }
  }

  list() {
    return [...this.tasks.values()].sort((a, b) => b.createdAt - a.createdAt);
  }

  get(taskId) {
    return this.tasks.get(taskId) || null;
  }

  async persist() {
    const list = this.list().slice(0, MAX_TASK_HISTORY);
    await this.chromeApi.storage.local.set({ [STORAGE_KEYS.TASKS]: list });
  }

  // 新建任务。filename 在这里定死：同一批任务之间不能重名，
  // 否则浏览器会给第二个自动加后缀，界面上显示的名字就和落盘的对不上。
  async create({ kind, url, title, variantLabel, extension, headers, totalBytes, pageUrl }) {
    await this.load();
    // 只有"真的产出了文件"的任务才占用文件名。失败与取消的任务没落下任何东西，
    // 让它们继续占着名字的话，用户改完名重试会莫名其妙拿到一个 (2)。
    const taken = new Set(
      this.list()
        .filter((task) => task.state !== TASK_STATE.FAILED && task.state !== TASK_STATE.CANCELED)
        .map((task) => task.filename)
    );
    const filename = uniqueFilename(
      buildFilename({ title, url, variantLabel, extension }),
      taken
    );

    const task = {
      id: `t${Date.now().toString(36)}${Math.random().toString(36).slice(2, 7)}`,
      kind,
      url,
      title: title || '',
      variantLabel: variantLabel || '',
      filename,
      pageUrl: pageUrl || '',
      headers: headers || {},
      totalBytes: totalBytes || 0,
      state: TASK_STATE.QUEUED,
      progress: { completedItems: 0, totalItems: 0, bytesWritten: 0 },
      resume: null,
      error: '',
      createdAt: Date.now(),
      updatedAt: Date.now(),
      startedAt: 0,
      finishedAt: 0
    };
    this.tasks.set(task.id, task);
    await this.persist();
    this.notify();
    await this.pump();
    return task;
  }

  activeCount() {
    return this.list().filter((task) => task.state === TASK_STATE.RUNNING || task.state === TASK_STATE.REMUXING || task.state === 'writing').length;
  }

  // 队列推进：有空位就把最早排队的任务派出去。
  async pump() {
    const queued = this.list()
      .filter((task) => task.state === TASK_STATE.QUEUED)
      .sort((a, b) => a.createdAt - b.createdAt);
    for (const task of queued) {
      if (this.activeCount() >= MAX_ACTIVE_TASKS) return;
      await this.dispatch(task);
    }
  }

  async dispatch(task) {
    const settings = await loadSettings();
    task.state = TASK_STATE.RUNNING;
    task.error = '';
    task.startedAt = task.startedAt || Date.now();
    task.updatedAt = Date.now();
    await this.persist();
    this.notify();

    // 先把请求头规则装上再派活：分片请求一发出就得带着 Referer。
    await this.headerRules.apply(task.id, [hostOf(task.url)], task.headers);
    await this.ensureOffscreen();
    await this.chromeApi.runtime.sendMessage({
      type: MSG.OFFSCREEN_START,
      target: 'offscreen',
      task: {
        id: task.id,
        kind: task.kind,
        url: task.url,
        // filename 是给 offscreen 定 blob 的 MIME 用的：没有正确的类型，
        // 产物会被 chrome.downloads 当成文本存成 .txt。
        filename: task.filename,
        totalBytes: task.totalBytes,
        resume: task.resume,
        settings
      }
    });
  }

  // worker 解析出播放列表之后回报真实的主机集合，这里把规则重装全。
  // 派活时只知道播放列表主机，分片与密钥常常在别的域名下。
  async applyHeaderHosts(taskId, hosts) {
    const task = this.tasks.get(taskId);
    if (!task) return;
    await this.headerRules.apply(taskId, [hostOf(task.url), ...hosts], task.headers);
  }

  async pause(taskId) {
    const task = this.tasks.get(taskId);
    if (!task || task.state !== TASK_STATE.RUNNING) return;
    await this.chromeApi.runtime.sendMessage({
      type: MSG.OFFSCREEN_CONTROL,
      target: 'offscreen',
      taskId,
      action: 'pause'
    });
  }

  async resume(taskId) {
    const task = this.tasks.get(taskId);
    if (!task || (task.state !== TASK_STATE.PAUSED && task.state !== TASK_STATE.FAILED)) return;
    task.state = TASK_STATE.QUEUED;
    task.error = '';
    task.updatedAt = Date.now();
    await this.persist();
    this.notify();
    await this.pump();
  }

  async cancel(taskId) {
    const task = this.tasks.get(taskId);
    if (!task) return;
    if (task.state === TASK_STATE.RUNNING) {
      await this.chromeApi.runtime.sendMessage({
        type: MSG.OFFSCREEN_CONTROL,
        target: 'offscreen',
        taskId,
        action: 'cancel'
      });
      return;
    }
    task.state = TASK_STATE.CANCELED;
    task.resume = null;
    task.finishedAt = Date.now();
    await this.headerRules.clear(taskId);
    await this.releaseArtifacts(taskId);
    await this.persist();
    this.notify();
    await this.pump();
  }

  async remove(taskId) {
    const task = this.tasks.get(taskId);
    if (!task) return;
    if (!TERMINAL_STATES.includes(task.state)) await this.cancel(taskId);
    this.tasks.delete(taskId);
    await this.headerRules.clear(taskId);
    await this.releaseArtifacts(taskId);
    await this.persist();
    this.notify();
  }

  // offscreen 报上来的每一次状态变化都走这里。
  async handleReport(report) {
    await this.load();
    const task = this.tasks.get(report.taskId);
    if (!task) return;
    task.updatedAt = Date.now();

    if (report.progress) {
      task.progress = report.progress;
      task.resume = {
        nextIndex: report.progress.nextIndex ?? report.progress.completedItems ?? 0,
        bytesWritten: report.progress.bytesWritten || 0,
        totalItems: report.progress.totalItems || 0
      };
    }

    switch (report.state) {
      case 'running':
        task.state = TASK_STATE.RUNNING;
        break;
      case 'remuxing':
        // 转封装阶段：分片已经下完，正在转成 mp4。断点在这一步之后不再有意义，
        // 但也不该把任务显示成"下载中"——那会让人以为还在拉数据。
        task.state = TASK_STATE.REMUXING;
        if (report.remux) task.remux = report.remux;
        break;
      case 'paused':
        task.state = TASK_STATE.PAUSED;
        await this.headerRules.clear(task.id);
        break;
      case 'canceled':
        task.state = TASK_STATE.CANCELED;
        task.resume = null;
        task.finishedAt = Date.now();
        await this.headerRules.clear(task.id);
        break;
      case 'failed':
        task.state = TASK_STATE.FAILED;
        task.error = report.error || '下载失败';
        task.finishedAt = Date.now();
        await this.headerRules.clear(task.id);
        break;
      case 'writing':
        task.state = 'writing';
        task.totalBytes = report.totalBytes || task.totalBytes;
        await this.headerRules.clear(task.id);
        await this.deliver(task, report.blobUrl);
        break;
      default:
        break;
    }

    await this.persist();
    this.notify();
    if (task.state !== TASK_STATE.RUNNING && task.state !== TASK_STATE.REMUXING && task.state !== 'writing') await this.pump();
  }

  // 交给浏览器下载器落到用户的下载目录。
  async deliver(task, blobUrl) {
    if (!blobUrl) {
      task.state = TASK_STATE.FAILED;
      task.error = '落盘文件地址丢失';
      return;
    }
    try {
      const downloadId = await this.chromeApi.downloads.download({
        url: blobUrl,
        filename: task.filename,
        saveAs: false
      });
      task.downloadId = downloadId;
    } catch (error) {
      task.state = TASK_STATE.FAILED;
      task.error = `交给浏览器下载失败：${error.message || error}`;
      await this.releaseArtifacts(task.id);
    }
  }

  // 浏览器下载器完成后回收：撤销 blob 地址、删掉 OPFS 上的分片文件。
  // 这一步必须等下载真的结束，提前撤销会把下载打断。
  async handleDownloadChanged(delta) {
    if (!delta || !delta.state) return;
    await this.load();
    const task = this.list().find((item) => item.downloadId === delta.id);
    if (!task) return;

    if (delta.state.current === 'complete') {
      task.state = TASK_STATE.DONE;
      task.finishedAt = Date.now();
      task.resume = null;
      await this.releaseArtifacts(task.id);
    } else if (delta.state.current === 'interrupted') {
      task.state = TASK_STATE.FAILED;
      task.error = '浏览器下载器中断了这次保存';
      await this.releaseArtifacts(task.id);
    } else {
      return;
    }
    task.updatedAt = Date.now();
    await this.persist();
    this.notify();
    await this.pump();
  }

  async releaseArtifacts(taskId) {
    try {
      await this.chromeApi.runtime.sendMessage({ type: 'revoke-blob', target: 'offscreen', taskId });
    } catch {
      // offscreen 文档已经关了，blob 地址随文档一起没了，OPFS 文件由启动时的清理兜底。
    }
  }

  async ensureOffscreen() {
    if (this.offscreenReady) return this.offscreenReady;
    this.offscreenReady = (async () => {
      const existing = await this.chromeApi.offscreen.hasDocument?.();
      if (existing) return;
      try {
        await this.chromeApi.offscreen.createDocument({
          url: 'src/offscreen/offscreen.html',
          reasons: ['WORKERS', 'BLOBS'],
          justification: '在后台持续下载媒体分片并合并落盘，service worker 会被回收，无法承担长任务。'
        });
      } catch (error) {
        // 并发创建时另一路已经建好了，这不是错误。
        if (!String(error && error.message).includes('Only a single offscreen')) throw error;
      }
    })();
    try {
      await this.offscreenReady;
    } catch (error) {
      this.offscreenReady = null;
      throw error;
    }
    return this.offscreenReady;
  }
}
