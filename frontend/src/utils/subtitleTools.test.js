import { describe, expect, it } from 'vitest';
import {
  defaultRetranslateMode, fixSubtitleTimingIssues, subtitleEncodingLabel, subtitleEncodingPrompt,
  subtitleErrorText, subtitleExceptionText
} from './subtitleTools.js';
import { scrubAbsolutePaths } from './pathText.js';

const cue = (id, start, end, text = 'x') => ({ client_id: id, start_time_ms: start, end_time_ms: end, text });

describe('字幕界面共用口径', () => {
  it('MEDIA-02 编码说法：GB18030 说成 GBK，识别不了时不给转换承诺', () => {
    expect(subtitleEncodingLabel('gb18030')).toBe('GBK');
    expect(subtitleEncodingLabel('big5')).toBe('Big5');
    expect(subtitleEncodingLabel('utf-16le')).toBe('UTF-16');
    expect(subtitleEncodingPrompt('gb18030')).toBe('字幕编码为 GBK（推测），可一键转换为 UTF-8（会先备份）。');
    expect(subtitleEncodingPrompt('unknown')).toContain('无法识别');
  });

  it('MEDIA-06 文案不带绝对路径：系统错误里的路径换成文件名', () => {
    expect(scrubAbsolutePaths('open /Users/me/Movies/a.srt: no such file')).toBe('open a.srt: no such file');
    expect(scrubAbsolutePaths('目标路径已被其他记录占用: /Volumes/盘/片/b.mp4')).toBe('目标路径已被其他记录占用: b.mp4');
    // 普通的斜杠（比例、单段）不动。
    expect(scrubAbsolutePaths('进度 3/10，目录 /tmp')).toBe('进度 3/10，目录 /tmp');
    expect(subtitleExceptionText(new Error('open /Users/me/a.srt: no such file or directory'))).toBe('文件不存在或已被移动。');
  });

  it('MEDIA-08 带码失败优先用后端文案，没有文案时按码兜底', () => {
    expect(subtitleErrorText('subtitle_not_sidecar_srt', '')).toBe('该视频只有内嵌/其他格式字幕，暂不支持编辑或翻译。');
    expect(subtitleErrorText('subtitle_missing', '该视频还没有外挂字幕，请先生成字幕')).toBe('该视频还没有外挂字幕，请先生成字幕');
    expect(subtitleErrorText('unknown_code', '')).toBe('字幕操作没有完成。');
  });

  it('MEDIA-07 双语条目占多数时默认只替换译文行，否则整条替换', () => {
    expect(defaultRetranslateMode([cue('a', 0, 1, 'Hi\n你好'), cue('b', 1, 2, 'Bye\n再见'), cue('c', 2, 3, 'Solo')])).toBe('translation_line');
    expect(defaultRetranslateMode([cue('a', 0, 1, 'Hi\n你好'), cue('b', 1, 2, 'Bye')])).toBe('whole_entry');
    expect(defaultRetranslateMode([])).toBe('whole_entry');
  });

  it('MEDIA-06 一键修复：零时长补 500ms 且不越过下一条，重叠把前一条结束收到后一条开始前 1ms', () => {
    const { entries, fixed } = fixSubtitleTimingIssues([
      cue('zero', 1000, 1000),
      cue('next', 1200, 3000),
      cue('overlap', 2500, 4000),
      cue('backwards', 5000, 4800),
      cue('tail', 9000, 9000)
    ]);
    expect(fixed).toBe(4);
    expect(entries.map(entry => [entry.start_time_ms, entry.end_time_ms])).toEqual([
      [1000, 1200], // 下一条 1200 开始，封顶
      [1200, 2499], // 与后一条重叠，收到 2500 − 1
      [2500, 4000],
      [5000, 5500],
      [9000, 9500]
    ]);
  });

  it('MEDIA-06 修完会变成零时长的重叠不硬修，入参不被改动', () => {
    const input = [cue('a', 1000, 3000), cue('b', 1000, 2000)];
    const { entries, fixed } = fixSubtitleTimingIssues(input);
    expect(fixed).toBe(0);
    expect(entries[0].end_time_ms).toBe(3000);
    expect(input[0]).not.toBe(entries[0]);
  });
});
