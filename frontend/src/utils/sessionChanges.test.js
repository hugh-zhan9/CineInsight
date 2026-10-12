import { beforeEach, describe, expect, it } from 'vitest';
import {
  SESSION_CHANGE_LIMIT, recordBackgroundTasks, recordDownloadDuplicate, recordScanSummary, recordSessionChange,
  resetSessionChanges, scanSummaryDetail, sessionChanges
} from './sessionChanges.js';

beforeEach(() => resetSessionChanges());

describe('sessionChanges', () => {
  it('starts empty and keeps newest first', () => {
    expect(sessionChanges()).toEqual([]);
    recordSessionChange({ kind: 'scan', title: '一' });
    recordSessionChange({ kind: 'scan', title: '二' });
    expect(sessionChanges().map(item => item.title)).toEqual(['二', '一']);
    expect(new Set(sessionChanges().map(item => item.id)).size).toBe(2);
  });

  it('ignores entries without kind or title and caps the list', () => {
    expect(recordSessionChange({ kind: '', title: 'x' })).toBeNull();
    expect(recordSessionChange({ kind: 'scan', title: '' })).toBeNull();
    for (let index = 0; index < SESSION_CHANGE_LIMIT + 5; index++) recordSessionChange({ kind: 'scan', title: `#${index}` });
    expect(sessionChanges()).toHaveLength(SESSION_CHANGE_LIMIT);
    expect(sessionChanges()[0].title).toBe(`#${SESSION_CHANGE_LIMIT + 4}`);
  });

  it('summarises scan results by trigger, counts and errors', () => {
    expect(scanSummaryDetail({ added: 3, deleted: 0, stale: 1, restored: 0, relocated: 2, errors: [{}, {}] })).toBe('新增 3 · 失效 1 · 移动 2 · 2 处出错');
    expect(scanSummaryDetail({ added: 0, errors: [] })).toBe('没有变化');
    expect(recordScanSummary(null)).toBeNull();
    expect(recordScanSummary({ trigger: 'manual' })).toBeNull();
    const entry = recordScanSummary({ trigger: 'startup', result: { added: 1 } });
    expect(entry).toMatchObject({ kind: 'scan', title: '启动时扫描', detail: '新增 1', page: 'library-manage' });
    expect(recordScanSummary({ trigger: 'other', result: {} }).title).toBe('扫描完成');
  });

  it('records download duplicates with the downloads page as target', () => {
    expect(recordDownloadDuplicate({ title: '片名', kind: 'exact' })).toMatchObject({ kind: 'download-duplicate', detail: '《片名》片库里已有完全相同的文件', page: 'downloads' });
    expect(recordDownloadDuplicate({ kind: 'version' }).detail).toBe('《视频》片库里已有同一部的其他版本');
  });

  it('records a backup only when the backup task leaves the running set', () => {
    expect(recordBackgroundTasks([])).toBeNull();
    expect(recordBackgroundTasks(['backup', 'phash'])).toBeNull();
    expect(recordBackgroundTasks(['backup'])).toBeNull();
    const entry = recordBackgroundTasks(['phash']);
    expect(entry).toMatchObject({ kind: 'backup', title: '数据库备份', page: 'settings' });
    expect(recordBackgroundTasks(null)).toBeNull();
    expect(sessionChanges()).toHaveLength(1);
  });
});
