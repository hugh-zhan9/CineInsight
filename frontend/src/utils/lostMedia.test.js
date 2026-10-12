import { describe, expect, it, vi } from 'vitest';
import { collectLostItems, lostMediaErrorText, reasonChips, skippedSummary } from './lostMedia.js';

describe('失联数据工具', () => {
  it('只展示后端固定码的说明，其余错误用兜底文案（不带路径）', () => {
    expect(lostMediaErrorText(new Error('lost_media_busy: x'), '兜底')).toContain('已有找回');
    expect(lostMediaErrorText('lost_media_invalid_request: 所选文件夹不可用', '兜底')).toBe('所选文件夹不可用');
    expect(lostMediaErrorText(new Error('open /Volumes/A/x: permission denied'), '兜底')).toBe('兜底');
  });

  it('原因 chip 按固定顺序，只能等的两类标为不可处理', () => {
    expect(reasonChips({ missing: 2, offline: 1, unknown: 0 })).toEqual([
      { key: 'offline', label: '卷未挂载', count: 1, actionable: false },
      { key: 'missing', label: '文件不见', count: 2, actionable: true }
    ]);
  });

  it('跳过原因归并计数', () => {
    const summary = skippedSummary({ items: [{ id: 1, reason: 'volume_offline' }, { id: 2, reason: 'volume_offline' }, { id: 3, reason: 'error' }] });
    expect(summary).toEqual([{ reason: 'volume_offline', label: '所在的卷没挂载', count: 2 }, { reason: 'error', label: '处理失败', count: 1 }]);
  });

  it('逐页读完一个目录分组', async () => {
    const list = vi.fn()
      .mockResolvedValueOnce({ items: [{ id: 3 }, { id: 5 }], has_more: true })
      .mockResolvedValueOnce({ items: [{ id: 9 }], has_more: false });
    const items = await collectLostItems(list, 'video', '/a', 2);
    expect(items.map(item => item.id)).toEqual([3, 5, 9]);
    expect(list).toHaveBeenLastCalledWith({ kind: 'video', directory: '/a', after_id: 5, limit: 2 });
  });
});
