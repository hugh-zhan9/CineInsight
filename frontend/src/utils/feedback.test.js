import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  ACTION_DISMISS_MS, confirmAction, confirmTextMatches, feedbackState, notify, notifyError, notifyUndo, notifyUndoable,
  resetFeedback, runToastAction, translateBackendError
} from './feedback.js';
import * as recentOperations from './recentOperations.js';

describe('后端错误翻译（G-3）', () => {
  it('APP-02 维护模式等数据库层英文错误在提示前换成中文，前后缀保留', () => {
    expect(translateBackendError('读取设置失败: database is in maintenance mode')).toBe(
      '读取设置失败: 数据库正在恢复备份或切换后端，暂时无法操作；如已提示需要重启，请重启应用'
    );
    expect(translateBackendError('sql: database is closed')).toBe('数据库连接已关闭，请重启应用');
    expect(translateBackendError('普通错误')).toBe('普通错误');
  });

  it('APP-02 notifyError 展示的是翻译后的文案', () => {
    resetFeedback();
    notifyError('database is in maintenance mode');
    expect(feedbackState.toasts.at(-1).message).toContain('数据库正在恢复备份或切换后端');
    expect(feedbackState.toasts.at(-1).message).not.toContain('maintenance');
  });
});

describe('提示动作与三级确认（D-UD-07、D-UD-12）', () => {
  it('带动作的提示默认停 8 秒，普通提示 4.5 秒，错误常驻；显式 timeout 优先', () => {
    vi.useFakeTimers();
    try {
      resetFeedback();
      const run = vi.fn();
      notify('已移到废纸篓', { action: { label: '撤销', run } });
      notify('已保存');
      notifyError('移动失败', { action: { label: '重试', run } });
      notify('自定义', { action: { label: '查看', run }, timeout: 1000 });
      expect(feedbackState.toasts.map(toast => toast.action?.label ?? null)).toEqual(['撤销', null, '重试', '查看']);
      vi.advanceTimersByTime(1000);
      expect(feedbackState.toasts.map(toast => toast.message)).toEqual(['已移到废纸篓', '已保存', '移动失败']);
      vi.advanceTimersByTime(3500);
      expect(feedbackState.toasts.map(toast => toast.message)).toEqual(['已移到废纸篓', '移动失败']);
      vi.advanceTimersByTime(ACTION_DISMISS_MS - 4500);
      expect(feedbackState.toasts.map(toast => toast.message)).toEqual(['移动失败']);
      expect(run).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it('动作缺 label 或 run 不是函数时按普通提示处理', () => {
    resetFeedback();
    notify('a', { action: { label: '  ', run: () => {} } });
    notify('b', { action: { label: '撤销' } });
    expect(feedbackState.toasts.map(toast => toast.action)).toEqual([null, null]);
  });

  it('notifyUndo 出一条带「撤销」的成功提示；动作只执行一次，执行前先收起', async () => {
    resetFeedback();
    const run = vi.fn().mockResolvedValue();
    const id = notifyUndo('已移到废纸篓：3 个文件', run);
    expect(feedbackState.toasts[0]).toMatchObject({ level: 'success', action: { label: '撤销' } });
    const first = runToastAction(id);
    expect(feedbackState.toasts).toHaveLength(0);
    expect(await first).toBe(true);
    expect(await runToastAction(id)).toBe(false);
    expect(run).toHaveBeenCalledTimes(1);
  });

  it('动作失败时另出一条错误提示', async () => {
    resetFeedback();
    const id = notifyUndo('已移到废纸篓', () => Promise.reject(new Error('回收站记录已不存在')));
    expect(await runToastAction(id)).toBe(false);
    expect(feedbackState.toasts).toHaveLength(1);
    expect(feedbackState.toasts[0]).toMatchObject({ level: 'error', message: '撤销失败：回收站记录已不存在' });
  });

  it('动作失败的提示不出现 undefined：有错误原文才接在冒号后面', async () => {
    const cases = [
      [() => Promise.reject(), '撤销失败'],
      [() => Promise.reject(new Error('')), '撤销失败'],
      [() => Promise.reject({ code: 7 }), '撤销失败'],
      [() => Promise.reject('  '), '撤销失败'],
      [() => Promise.reject('批次已恢复'), '撤销失败：批次已恢复'],
      [() => { throw new Error('回收站记录已不存在'); }, '撤销失败：回收站记录已不存在']
    ];
    for (const [run, expected] of cases) {
      resetFeedback();
      expect(await runToastAction(notifyUndo('已移到废纸篓', run))).toBe(false);
      expect(feedbackState.toasts.map(toast => toast.message)).toEqual([expected]);
    }
  });

  it('confirmAction 默认是普通确认；irreversible 推出危险样式并要求原样输入按钮文字', () => {
    resetFeedback();
    confirmAction({ title: '从片库移除', confirmText: '从片库移除', impact: '文件不动，只删记录' });
    expect(feedbackState.confirm).toMatchObject({ level: 'confirm', danger: false, requiredText: '', impact: '文件不动，只删记录' });
    expect(confirmTextMatches(feedbackState.confirm, '')).toBe(true);

    confirmAction({ title: '永久删除', confirmText: '永久删除', level: 'irreversible' });
    expect(feedbackState.confirm).toMatchObject({ level: 'irreversible', danger: true, requiredText: '永久删除' });
    expect(confirmTextMatches(feedbackState.confirm, '永久')).toBe(false);
    expect(confirmTextMatches(feedbackState.confirm, ' 永久删除 ')).toBe(true);

    // 按钮文字带首尾空白时，要输入的文字同样不算空白（两边都去掉）。
    confirmAction({ title: '清空目标库', confirmText: ' 清空 ', level: 'irreversible' });
    expect(feedbackState.confirm.requiredText).toBe('清空');
    expect(confirmTextMatches(feedbackState.confirm, '清空')).toBe(true);
    expect(confirmTextMatches({ requiredText: ' 清空 ' }, '清空 ')).toBe(true);
    expect(confirmTextMatches({ requiredText: ' 清空 ' }, '清')).toBe(false);

    confirmAction({ level: 'irreversible', confirmText: '清空', danger: false });
    expect(feedbackState.confirm.danger).toBe(false);
    confirmAction({ level: 'whatever', danger: true });
    expect(feedbackState.confirm).toMatchObject({ level: 'confirm', danger: true, requiredText: '' });
    resetFeedback();
  });
});

// D-UD-09：提示里的「撤销」与任务中心「最近操作」是同一条记录。
describe('notifyUndoable 与最近操作联动（D-UD-09）', () => {
  afterEach(() => {
    resetFeedback();
    recentOperations.clear();
  });

  it('记一条最近操作并出带「撤销」的提示；label 缺省用提示文字', () => {
    notifyUndoable('已移到废纸篓：3 个文件', vi.fn());
    notifyUndoable('已批量编辑 12 部视频', vi.fn(), { label: '批量编辑：12 部视频' });
    expect(recentOperations.list().map(item => item.label)).toEqual(['批量编辑：12 部视频', '已移到废纸篓：3 个文件']);
    expect(feedbackState.toasts.map(toast => [toast.level, toast.message, toast.action?.label])).toEqual([
      ['success', '已移到废纸篓：3 个文件', '撤销'],
      ['success', '已批量编辑 12 部视频', '撤销']
    ]);
  });

  it('从提示撤销：执行一次，最近操作里的这条随之移除，再从列表撤销不会重跑', async () => {
    const undo = vi.fn().mockResolvedValue();
    const toastID = notifyUndoable('已移到废纸篓：3 个文件', undo);
    const [operation] = recentOperations.list();

    expect(await runToastAction(toastID)).toBe(true);
    expect(undo).toHaveBeenCalledTimes(1);
    expect(recentOperations.list()).toEqual([]);
    expect(await recentOperations.undo(operation.id)).toBe(false);
    expect(undo).toHaveBeenCalledTimes(1);
  });

  it('先从最近操作撤销，再点提示里的「撤销」：不再执行，提示收起且不报错', async () => {
    const undo = vi.fn().mockResolvedValue();
    const toastID = notifyUndoable('已移到废纸篓：3 个文件', undo);
    const [operation] = recentOperations.list();

    expect(await recentOperations.undo(operation.id)).toBe(true);
    expect(undo).toHaveBeenCalledTimes(1);
    expect(await runToastAction(toastID)).toBe(true);
    expect(undo).toHaveBeenCalledTimes(1);
    expect(feedbackState.toasts).toEqual([]);
  });

  it('从提示撤销失败：报错，记录留在最近操作里可再试', async () => {
    const undo = vi.fn().mockRejectedValueOnce(new Error('批次已恢复')).mockResolvedValueOnce();
    const toastID = notifyUndoable('已移到废纸篓', undo);
    expect(await runToastAction(toastID)).toBe(false);
    expect(feedbackState.toasts.map(toast => toast.message)).toEqual(['撤销失败：批次已恢复']);
    const [operation] = recentOperations.list();
    expect(await recentOperations.undo(operation.id)).toBe(true);
    expect(undo).toHaveBeenCalledTimes(2);
  });

  it('记不进最近操作（undo 不是函数）时退回普通提示，不留记录', () => {
    expect(notifyUndoable('已移到废纸篓', null)).toEqual(expect.any(Number));
    expect(recentOperations.list()).toEqual([]);
    expect(feedbackState.toasts.map(toast => toast.message)).toEqual(['已移到废纸篓']);
  });
});
