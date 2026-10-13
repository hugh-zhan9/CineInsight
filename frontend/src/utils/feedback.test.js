import { describe, expect, it, vi } from 'vitest';
import {
  ACTION_DISMISS_MS, confirmAction, confirmTextMatches, feedbackState, notify, notifyError, notifyUndo, resetFeedback,
  runToastAction, translateBackendError
} from './feedback.js';

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

  it('confirmAction 默认是普通确认；irreversible 推出危险样式并要求原样输入按钮文字', () => {
    resetFeedback();
    confirmAction({ title: '从片库移除', confirmText: '从片库移除', impact: '文件不动，只删记录' });
    expect(feedbackState.confirm).toMatchObject({ level: 'confirm', danger: false, requiredText: '', impact: '文件不动，只删记录' });
    expect(confirmTextMatches(feedbackState.confirm, '')).toBe(true);

    confirmAction({ title: '永久删除', confirmText: '永久删除', level: 'irreversible' });
    expect(feedbackState.confirm).toMatchObject({ level: 'irreversible', danger: true, requiredText: '永久删除' });
    expect(confirmTextMatches(feedbackState.confirm, '永久')).toBe(false);
    expect(confirmTextMatches(feedbackState.confirm, ' 永久删除 ')).toBe(true);

    confirmAction({ level: 'irreversible', confirmText: '清空', danger: false });
    expect(feedbackState.confirm.danger).toBe(false);
    confirmAction({ level: 'whatever', danger: true });
    expect(feedbackState.confirm).toMatchObject({ level: 'confirm', danger: true, requiredText: '' });
    resetFeedback();
  });
});
