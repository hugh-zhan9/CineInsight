import { afterEach, describe, expect, it, vi } from 'vitest';

import {
  COMMAND_GROUPS, commandList, filterCommands, isCommandEnabled, registerCommands, unregisterCommands
} from './commandRegistry.js';

// 注册表是模块级单例：每条用例自己清干净，免得互相看见对方的命令。
const scopesUsed = new Set();

function register(scopeKey, commands) {
  scopesUsed.add(scopeKey);
  registerCommands(scopeKey, commands);
}

function command(id, overrides = {}) {
  return { id, group: 'action', label: id, keywords: [], run: vi.fn(), ...overrides };
}

afterEach(() => {
  for (const scope of scopesUsed) unregisterCommands(scope);
  scopesUsed.clear();
});

describe('命令注册表', () => {
  it('注册后可读到，注销后立刻消失', () => {
    register('page-a', [command('a:one'), command('a:two')]);
    expect(commandList().map(item => item.id)).toEqual(['a:one', 'a:two']);

    unregisterCommands('page-a');
    expect(commandList()).toEqual([]);
  });

  it('重复注册同一个 scope 覆盖旧内容而不叠加，且不改变排序位置', () => {
    register('page-a', [command('a:one')]);
    register('page-b', [command('b:one')]);
    register('page-a', [command('a:two')]);

    expect(commandList().map(item => item.id)).toEqual(['a:two', 'b:one']);
  });

  it('按分组固定顺序拉平：导航 → 动作 → 任务 → 视频', () => {
    register('page-a', [
      command('x:video', { group: 'video' }),
      command('x:task', { group: 'task' }),
      command('x:action', { group: 'action' }),
      command('x:nav', { group: 'navigate' })
    ]);

    expect(commandList().map(item => item.id)).toEqual(['x:nav', 'x:action', 'x:task', 'x:video']);
    expect(COMMAND_GROUPS.map(group => group.key)).toEqual(['navigate', 'action', 'task', 'video']);
  });

  it('同 id 只保留先注册的那一条', () => {
    register('page-a', [command('dup', { label: '先来的' })]);
    register('page-b', [command('dup', { label: '后来的' })]);

    expect(commandList().map(item => item.label)).toEqual(['先来的']);
  });

  it('注册项缺字段直接抛错，不静默丢掉', () => {
    expect(() => register('bad', [{ id: 'no-run', group: 'action', label: '缺 run' }]))
      .toThrow(/缺少 run/);
    expect(() => register('bad', [{ id: 'bad-group', group: 'nope', label: 'x', run: () => {} }]))
      .toThrow(/group 无效/);
    expect(() => registerCommands('', [])).toThrow(/非空 scopeKey/);
  });
});

describe('命令过滤与权重', () => {
  it('空查询返回全部', () => {
    register('page-a', [command('a:one'), command('a:two')]);
    expect(filterCommands('').map(item => item.id)).toEqual(['a:one', 'a:two']);
    expect(filterCommands('   ').map(item => item.id)).toEqual(['a:one', 'a:two']);
  });

  it('label 前缀优先于 label 包含，label 包含优先于 keywords 命中', () => {
    register('page-a', [
      command('by-keyword', { label: '毫不相干', keywords: ['扫描目录'] }),
      command('by-contains', { label: '立即扫描' }),
      command('by-prefix', { label: '扫描新目录' })
    ]);

    expect(filterCommands('扫描').map(item => item.id)).toEqual(['by-prefix', 'by-contains', 'by-keyword']);
  });

  it('同权重按注册顺序稳定排列，不匹配的被排除', () => {
    register('page-a', [
      command('p1', { label: '扫描 A' }),
      command('p2', { label: '扫描 B' }),
      command('p3', { label: '无关项' })
    ]);

    expect(filterCommands('扫描').map(item => item.id)).toEqual(['p1', 'p2']);
  });

  it('大小写不敏感', () => {
    register('page-a', [command('p1', { label: 'Backup Now', keywords: ['DataBase'] })]);
    expect(filterCommands('backup').map(item => item.id)).toEqual(['p1']);
    expect(filterCommands('database').map(item => item.id)).toEqual(['p1']);
  });
});

describe('isCommandEnabled', () => {
  it('没有 enabled() 视为可执行；返回 false 才不可执行', () => {
    expect(isCommandEnabled(command('a'))).toBe(true);
    expect(isCommandEnabled(command('b', { enabled: () => false }))).toBe(false);
    expect(isCommandEnabled(command('c', { enabled: () => true }))).toBe(true);
  });
});
