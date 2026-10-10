import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

import { describe, expect, it } from 'vitest';

import { BACKGROUND_TASK_LABELS, backgroundTaskLabel } from './idleScheduling.js';

// 后端登记表的固定 key 集合与面板顺序（services/background_task_registry.go 的 backgroundTaskKeyOrder）。
// vitest 的根是 frontend/，后端源码在上一级。直接读源码而不是再抄一份清单：后端增减 key 时这里会跟着变。
function backendTaskKeyOrder() {
  const source = readFileSync(resolve(process.cwd(), '../services/background_task_registry.go'), 'utf8');
  const values = Object.fromEntries(
    [...source.matchAll(/(BackgroundTask\w+)\s+BackgroundTaskKey\s*=\s*"([^"]+)"/g)].map(match => [match[1], match[2]])
  );
  const block = /var backgroundTaskKeyOrder = \[\]BackgroundTaskKey\{([\s\S]*?)\n\}/.exec(source);
  if (!block) throw new Error('services/background_task_registry.go 里找不到 backgroundTaskKeyOrder');
  return [...block[1].matchAll(/(BackgroundTask\w+),/g)].map(match => {
    if (!values[match[1]]) throw new Error(`登记表常量 ${match[1]} 没有取值`);
    return values[match[1]];
  });
}

describe('后台任务标签与后端登记表对齐（APP-03）', () => {
  it('APP-03 登记表的 26 个 key 在前端都有中文标签，顺序与任务中心面板一致', () => {
    const keys = backendTaskKeyOrder();
    expect(keys).toHaveLength(26);
    expect(Object.keys(BACKGROUND_TASK_LABELS)).toEqual(keys);
    for (const key of keys) {
      const label = BACKGROUND_TASK_LABELS[key];
      expect(label, key).toMatch(/[一-龥]/);
      expect(label, key).not.toBe(key);
    }
  });

  it('APP-03 补齐 image_cleanup、watchlist_enrich、movie_chart 三个标签', () => {
    expect(backgroundTaskLabel('image_cleanup')).toBe('图片清理分析');
    expect(backgroundTaskLabel('watchlist_enrich')).toBe('片单补全');
    expect(backgroundTaskLabel('movie_chart')).toBe('榜单抓取');
  });

  it('P-007 场景画面索引有中文标签', () => {
    expect(backgroundTaskLabel('scene_index')).toBe('场景画面索引');
  });

  it('P-008 视频工作台有中文标签（退出确认与任务中心共用）', () => {
    expect(backgroundTaskLabel('video_edit')).toBe('视频工作台');
  });

  it('APP-03 不认识的 key 显示中文兜底说法，不把原始 key 露给用户', () => {
    expect(backgroundTaskLabel('some_future_task')).toBe('其他后台任务');
    expect(backgroundTaskLabel(undefined)).toBe('其他后台任务');
  });
});
