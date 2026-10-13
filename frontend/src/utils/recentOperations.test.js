import { computed } from 'vue';
import { afterEach, describe, expect, it, vi } from 'vitest';

import * as recentOperations from './recentOperations.js';

afterEach(() => recentOperations.clear());

describe('最近操作（D-UD-09）', () => {
  it('starts empty and lists newest first', () => {
    expect(recentOperations.list()).toEqual([]);
    const first = recentOperations.push({ label: '移到废纸篓：3 个文件', undo: vi.fn() });
    const second = recentOperations.push({ label: '批量编辑：12 部视频', undo: vi.fn() });
    expect(recentOperations.list().map(item => [item.id, item.label])).toEqual([[second, '批量编辑：12 部视频'], [first, '移到废纸篓：3 个文件']]);
    expect(recentOperations.list()[0].at).toEqual(expect.any(Number));
  });

  it('ignores entries without a label or an undo function', () => {
    expect(recentOperations.push({ label: '  ', undo: vi.fn() })).toBe(null);
    expect(recentOperations.push({ label: '删除' })).toBe(null);
    expect(recentOperations.push()).toBe(null);
    expect(recentOperations.list()).toEqual([]);
  });

  it('keeps only the most recent 20 and forgets the undo of dropped ones', async () => {
    const undos = [];
    const ids = [];
    for (let index = 1; index <= 21; index += 1) {
      const undo = vi.fn();
      undos.push(undo);
      ids.push(recentOperations.push({ label: `操作 ${index}`, undo }));
    }
    const items = recentOperations.list();
    expect(recentOperations.RECENT_OPERATIONS_LIMIT).toBe(20);
    expect(items).toHaveLength(20);
    expect(items[0].label).toBe('操作 21');
    expect(items.at(-1).label).toBe('操作 2');
    expect(await recentOperations.undo(ids[0])).toBe(false);
    expect(undos[0]).not.toHaveBeenCalled();
  });

  it('undo runs once and removes the entry; a failed undo stays for another try', async () => {
    const ok = vi.fn().mockResolvedValue();
    const failing = vi.fn().mockRejectedValueOnce(new Error('批次已恢复')).mockResolvedValueOnce();
    const okID = recentOperations.push({ label: '移到废纸篓', undo: ok });
    const failID = recentOperations.push({ label: '批量编辑', undo: failing });

    const pending = recentOperations.undo(okID);
    expect(await recentOperations.undo(okID)).toBe(false);
    expect(await pending).toBe(true);
    expect(ok).toHaveBeenCalledTimes(1);
    expect(recentOperations.list().map(item => item.id)).toEqual([failID]);

    await expect(recentOperations.undo(failID)).rejects.toThrow('批次已恢复');
    expect(recentOperations.list()).toEqual([expect.objectContaining({ id: failID, undoing: false })]);
    expect(await recentOperations.undo(failID)).toBe(true);
    expect(recentOperations.list()).toEqual([]);
  });

  it('is reactive for the task-center tab and clear() empties it like a restart', () => {
    const labels = computed(() => recentOperations.list().map(item => item.label));
    expect(labels.value).toEqual([]);
    recentOperations.push({ label: '移到废纸篓', undo: vi.fn() });
    expect(labels.value).toEqual(['移到废纸篓']);
    recentOperations.clear();
    expect(labels.value).toEqual([]);
  });
});
