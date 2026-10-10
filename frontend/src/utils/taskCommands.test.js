import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../wailsjs/go/main/App', () => api);

import { buildTaskCommands, taskActionRunner, taskBindingActions } from './taskCommands.js';

// 后端任务中心的适配表（app_tasks.go 的 taskCenterAdapters）：每个 key 的适配函数里写了 canStart /
// canCancel 的，后端就可能给出 start / cancel 动作。vitest 的根是 frontend/，后端源码在上一级。
function backendAdapterCapabilities() {
  const registry = readFileSync(resolve(process.cwd(), '../services/background_task_registry.go'), 'utf8');
  const keyOf = Object.fromEntries(
    [...registry.matchAll(/(BackgroundTask\w+)\s+BackgroundTaskKey\s*=\s*"([^"]+)"/g)].map(match => [match[1], match[2]])
  );
  const tasks = readFileSync(resolve(process.cwd(), '../app_tasks.go'), 'utf8');
  const table = /var taskCenterAdapters = map\[services\.BackgroundTaskKey\]taskCenterAdapter\{([\s\S]*?)\n\}/.exec(tasks);
  if (!table) throw new Error('app_tasks.go 里找不到 taskCenterAdapters');
  return [...table[1].matchAll(/services\.(BackgroundTask\w+):\s*\(\*App\)\.(\w+),/g)].map(([, constName, fn]) => {
    const start = tasks.indexOf(`func (a *App) ${fn}(`);
    if (start < 0) throw new Error(`app_tasks.go 里找不到适配函数 ${fn}`);
    const end = tasks.indexOf('\nfunc ', start + 1);
    // 只看代码，不看注释：注释里提到字段名不代表会给出动作。
    const body = tasks.slice(start, end < 0 ? undefined : end)
      .split('\n').filter(line => !line.trim().startsWith('//')).join('\n');
    return { key: keyOf[constName], canStart: /\bcanStart:/.test(body), canCancel: /\bcanCancel:/.test(body) };
  });
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe('任务中心动作与绑定对齐（APP-03）', () => {
  it('cancels the current review batch of the requested media kind', async () => {
    expect(taskActionRunner('ai_review', 'start')).toBeNull();
    await taskActionRunner('ai_review', 'cancel')();
    expect(api.CancelAIReviewApproval).toHaveBeenLastCalledWith('video', '');
    await taskActionRunner('image_ai_review', 'cancel')();
    expect(api.CancelAIReviewApproval).toHaveBeenLastCalledWith('image', '');
  });

  it('APP-03 后端适配表覆盖 26 个 key；给出 start / cancel 的 key，前端都有对应的零参绑定', () => {
    const capabilities = backendAdapterCapabilities();
    expect(capabilities).toHaveLength(26);
    const missing = [];
    for (const { key, canStart, canCancel } of capabilities) {
      expect(key, '适配表里的常量应当能在登记表里找到取值').toBeTruthy();
      if (canStart && !taskActionRunner(key, 'start')) missing.push(`${key}:start`);
      if (canCancel && !taskActionRunner(key, 'cancel')) missing.push(`${key}:cancel`);
    }
    expect(missing).toEqual([]);
  });

  it('APP-03 前端绑定只登记零参的启动与取消，run_now 统一走 RunGatedTaskNow(key)', async () => {
    for (const actions of Object.values(taskBindingActions())) {
      for (const action of actions) expect(['start', 'cancel']).toContain(action);
    }
    await taskActionRunner('exif', 'run_now')();
    expect(api.RunGatedTaskNow).toHaveBeenCalledWith('exif');
  });

  it('APP-03 清理分析、图片清理、建议作品集、榜单抓取可从任务中心启动或取消', async () => {
    await taskActionRunner('cleanup', 'start')();
    expect(api.StartCleanupAnalysisFromSettings).toHaveBeenCalledTimes(1);
    await taskActionRunner('cleanup', 'cancel')();
    expect(api.CancelCleanupAnalysis).toHaveBeenCalledTimes(1);
    await taskActionRunner('image_cleanup', 'start')();
    expect(api.StartImageCleanupAnalysis).toHaveBeenCalledTimes(1);
    await taskActionRunner('collection_suggest', 'cancel')();
    expect(api.CancelCollectionSuggestionAnalysis).toHaveBeenCalledTimes(1);
    await taskActionRunner('movie_chart', 'cancel')();
    expect(api.CancelMovieChartRefresh).toHaveBeenCalledTimes(1);
  });

  it('没有绑定的动作返回 null，调用方据此不渲染按钮', () => {
    expect(taskActionRunner('enhancement', 'start')).toBeNull();
    expect(taskActionRunner('watchlist_enrich', 'cancel')).toBeNull();
    expect(taskActionRunner('phash', 'retry_failed')).toBeNull();
  });

  it('立即运行撞上「任务已被放行」不算失败', async () => {
    api.RunGatedTaskNow.mockRejectedValueOnce(new Error('idle_gate_task_not_waiting: 该后台任务当前没有在等待空闲'));
    await expect(taskActionRunner('phash', 'run_now')()).resolves.toBeUndefined();
  });
});

describe('命令面板任务组的任务名（APP-03）', () => {
  it('APP-03 运行中但前端还没有标签的 key 显示中文兜底说法，不显示原始 key', () => {
    const commands = buildTaskCommands({ running: ['brand_new_task'], waiting: [] });
    const command = commands.find(item => item.id === 'task:brand_new_task');
    expect(command.label).toBe('其他后台任务 · 运行中');
    expect(command.label).not.toContain('brand_new_task');
  });

  it('APP-03 新补标签的三个 key 在运行中时显示中文名', () => {
    const commands = buildTaskCommands({ running: ['image_cleanup', 'watchlist_enrich', 'movie_chart'], waiting: [] });
    const labels = Object.fromEntries(commands.map(item => [item.id, item.label]));
    expect(labels['task:image_cleanup']).toBe('图片清理分析 · 运行中 · 取消');
    expect(labels['task:watchlist_enrich']).toBe('片单补全 · 运行中');
    expect(labels['task:movie_chart']).toBe('榜单抓取 · 运行中 · 取消');
  });
});
