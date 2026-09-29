import { describe, expect, it } from 'vitest';
import { feedbackState, notifyError, resetFeedback, translateBackendError } from './feedback.js';

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
