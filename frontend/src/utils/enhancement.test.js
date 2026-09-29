import { describe, expect, it } from 'vitest';

import {
  ENHANCEMENT_ERROR_LABELS, enhancementCancellable, enhancementDetailText, enhancementReasonText,
  enhancementRetainsProgress, enhancementRetryable, enhancementStatusText
} from './enhancement.js';

describe('超分状态口径（utils/enhancement.js）', () => {
  it('MEDIA-11 后端的结束码都有中文说法，认不出的原样带出', () => {
    for (const code of [
      'runtime_unavailable', 'unsupported_input', 'output_conflict', 'disk_insufficient', 'source_changed',
      'decode_failed', 'inference_failed', 'encode_failed', 'verify_failed', 'publish_failed',
      'cancelled', 'checkpoint_discarded'
    ]) {
      expect(ENHANCEMENT_ERROR_LABELS[code], code).toBeTruthy();
      expect(enhancementReasonText(code)).not.toContain(code);
    }
    expect(enhancementReasonText('brand_new_code')).toBe('未知原因（brand_new_code）');
    expect(enhancementReasonText('')).toBe('');
  });

  it('MEDIA-11 状态文字带阶段、帧进度与失败原因', () => {
    expect(enhancementStatusText({ status: 'queued' })).toBe('排队中');
    expect(enhancementStatusText({ status: 'running', phase: 'encode', total_frames: 10, committed_frames: 4 })).toBe('处理中（编码 4/10 帧）');
    expect(enhancementStatusText({ status: 'running', phase: 'preflight' })).toBe('处理中（检查源文件）');
    expect(enhancementStatusText({ status: 'failed', error_code: 'disk_insufficient' })).toBe('失败：磁盘空间不足');
    expect(enhancementStatusText({ status: 'cancelled', error_code: 'checkpoint_discarded' })).toBe('已取消（保留的进度已放弃）');
    expect(enhancementStatusText(null)).toBe('');
  });

  it('MEDIA-03 只有取消与空间不足留着进度；放弃后不再算保留', () => {
    expect(enhancementRetainsProgress({ status: 'failed', error_code: 'disk_insufficient' })).toBe(true);
    expect(enhancementRetainsProgress({ status: 'cancelled', error_code: 'cancelled' })).toBe(true);
    expect(enhancementRetainsProgress({ status: 'failed', error_code: 'checkpoint_discarded' })).toBe(false);
    expect(enhancementRetainsProgress({ status: 'failed', error_code: 'encode_failed' })).toBe(false);
    // 正在排队或运行的任务就算码值对得上也不算：检查点正要被用。
    expect(enhancementRetainsProgress({ status: 'queued', error_code: 'disk_insufficient' })).toBe(false);
    expect(enhancementDetailText({ status: 'failed', error_code: 'disk_insufficient', error_summary: '剩余 1 GB' }))
      .toBe('剩余 1 GB；进度已保留，重试会从断点接着做');
  });

  it('取消与重试只对相应状态开放', () => {
    expect(enhancementCancellable({ status: 'queued' })).toBe(true);
    expect(enhancementCancellable({ status: 'running' })).toBe(true);
    expect(enhancementCancellable({ status: 'completed' })).toBe(false);
    expect(enhancementRetryable({ status: 'failed' })).toBe(true);
    expect(enhancementRetryable({ status: 'cancelled' })).toBe(true);
    expect(enhancementRetryable({ status: 'running' })).toBe(false);
  });
});
