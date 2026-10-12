import { describe, expect, it } from 'vitest';
import {
  QUOTE_MAX_LENGTH,
  clampQuoteLines,
  cleanQuoteText,
  layoutQuoteCard,
  paintQuoteCard,
  quoteCardFileName,
  quoteCardFrameURL,
  quoteCardMomentMS,
  subtitleCueAt,
  wrapQuoteLines
} from './quoteCard.js';

// 每个字 10 宽：折行结果可以直接按字数推算。
const measure = text => [...text].length * 10;
// 排版用的量字函数：宽度 = 字数 × 字号的一半，字号从 font 串里取。
const measureFont = (text, font) => [...text].length * Number(/(\d+)px/.exec(font)[1]) * 0.5;

describe('台词卡：台词清洗与取帧时刻', () => {
  it('去掉字幕里的 HTML 标签与 ASS 覆盖码，逐行去空白、丢空行，并截到上限', () => {
    expect(cleanQuoteText('<i>  你好 </i>\r\n\r\n{\\an8}世界  ')).toBe('你好\n世界');
    expect(cleanQuoteText(null)).toBe('');
    expect([...cleanQuoteText('长'.repeat(200))]).toHaveLength(QUOTE_MAX_LENGTH);
  });

  it('当前位置所在的字幕条目（闭区间）；没有就返回空串', () => {
    const segments = [
      { start_time_ms: 1000, end_time_ms: 2000, text: '第一句' },
      { start_time_ms: 3000, end_time_ms: 4000, text: '', lines: ['<b>第二句</b>', 'second'] }
    ];
    expect(subtitleCueAt(segments, 1000)).toBe('第一句');
    expect(subtitleCueAt(segments, 2000)).toBe('第一句');
    expect(subtitleCueAt(segments, 3500)).toBe('第二句\nsecond');
    expect(subtitleCueAt(segments, 2500)).toBe('');
    expect(subtitleCueAt(null, 1000)).toBe('');
    expect(subtitleCueAt(segments, Number.NaN)).toBe('');
  });

  it('对白命中取字幕区间正中间那一刻，区间无效时退回起点', () => {
    expect(quoteCardMomentMS(7_080_000, 7_083_500)).toBe(7_081_750);
    expect(quoteCardMomentMS(5000, 5000)).toBe(5000);
    expect(quoteCardMomentMS(5000, undefined)).toBe(5000);
    expect(quoteCardMomentMS(-20, 0)).toBe(0);
  });

  it('取帧走单帧路由的最大宽度；默认文件名带片名与时间、不含冒号', () => {
    expect(quoteCardFrameURL(7, 7_081_750.4)).toBe('/preview/frame/7?ms=7081750&w=960');
    expect(quoteCardFileName('长片', 3_723_000)).toBe('长片 1-02-03.png');
    expect(quoteCardFileName('  ', 65_000)).toBe('台词卡 1-05.png');
  });
});

describe('台词卡：按宽度折行（纯函数）', () => {
  it('中文逐字可断', () => {
    expect(wrapQuoteLines('一二三四五六七', 30, measure)).toEqual(['一二三', '四五六', '七']);
  });

  it('行首不出现逗号句号：标点跟前一个字一起换行', () => {
    const lines = wrapQuoteLines('一二三，四', 30, measure);
    expect(lines).toEqual(['一二', '三，四']);
    expect(lines.some(line => line.startsWith('，'))).toBe(false);
  });

  it('开引号跟后一个字走，不留在行尾', () => {
    expect(wrapQuoteLines('一二「三」', 30, measure)).toEqual(['一二', '「三」']);
  });

  it('西文按单词折行，空格不带到行首行尾', () => {
    expect(wrapQuoteLines('hello world foo', 110, measure)).toEqual(['hello world', 'foo']);
    expect(wrapQuoteLines('Hello, world', 60, measure)).toEqual(['Hello,', 'world']);
  });

  it('比一行还宽的单词按字符硬断', () => {
    expect(wrapQuoteLines('abcdefghij', 40, measure)).toEqual(['abcd', 'efgh', 'ij']);
  });

  it('保留显式换行、丢掉空行；空台词没有行', () => {
    expect(wrapQuoteLines('甲\n\n  乙  ', 100, measure)).toEqual(['甲', '乙']);
    expect(wrapQuoteLines('', 100, measure)).toEqual([]);
    expect(wrapQuoteLines('   ', 100, measure)).toEqual([]);
  });

  it('宽度连一个字都放不下时每行至少一个字，不会死循环', () => {
    expect(wrapQuoteLines('一二', 5, measure)).toEqual(['一', '二']);
  });

  it('超过行数上限时截断，最后一行收短到放得下省略号', () => {
    expect(clampQuoteLines(['aaaa', 'bbbb', 'cccc'], 2, 40, measure)).toEqual(['aaaa', 'bbb…']);
    const lines = ['aa', 'bb'];
    expect(clampQuoteLines(lines, 2, 40, measure)).toBe(lines);
  });
});

describe('台词卡：两种版式的排版', () => {
  const base = { frameWidth: 960, frameHeight: 540, quote: '我们走吧', title: '长片', atMS: 3_723_000, measure: measureFont };

  it('字幕条：画布就是画面（长边放大到 1920），台词居中贴近底部，压在半透明底条上', () => {
    const layout = layoutQuoteCard({ ...base, template: 'subtitle' });
    expect(layout).toMatchObject({ template: 'subtitle', width: 1920, height: 1080, panel: null, align: 'center' });
    expect(layout.frame).toEqual({ x: 0, y: 0, w: 1920, h: 1080 });
    expect(layout.lines).toEqual([expect.objectContaining({ text: '我们走吧', x: 960 })]);
    expect(layout.lines[0].y).toBeGreaterThan(1080 * 0.6);
    expect(layout.meta.text).toBe('《长片》  1:02:03');
    expect(layout.meta.y).toBeGreaterThan(layout.lines[0].y);
    expect(layout.meta.y).toBeLessThan(1080);
    expect(layout.band.y).toBeLessThan(layout.lines[0].y);
    expect(layout.band.y + layout.band.h).toBe(1080);
  });

  it('海报：画面在上，下面接文字面板，台词左对齐在面板里', () => {
    const layout = layoutQuoteCard({ ...base, template: 'poster' });
    expect(layout).toMatchObject({ template: 'poster', width: 1920, band: null, align: 'left' });
    expect(layout.panel.y).toBe(1080);
    expect(layout.height).toBe(1080 + layout.panel.h);
    expect(layout.lines[0].y).toBeGreaterThan(1080);
    expect(layout.lines[0].x).toBeLessThan(960);
    expect(layout.meta.y + 10).toBeLessThan(layout.height);
  });

  it('竖画面按长边放大；台词太长时截断并标记', () => {
    const portrait = layoutQuoteCard({ ...base, frameWidth: 540, frameHeight: 960, template: 'subtitle' });
    expect([portrait.width, portrait.height]).toEqual([1080, 1920]);
    const long = layoutQuoteCard({ ...base, template: 'subtitle', quote: '很长的一句台词'.repeat(60) });
    expect(long.lines).toHaveLength(4);
    expect(long.truncated).toBe(true);
    expect(long.lines[3].text.endsWith('…')).toBe(true);
    expect(layoutQuoteCard({ ...base, template: 'poster', quote: '很长的一句台词'.repeat(60) }).lines).toHaveLength(6);
  });

  it('没有台词时只排片名与时间；未知版式按字幕条处理', () => {
    const layout = layoutQuoteCard({ ...base, quote: '', title: '', template: 'unknown' });
    expect(layout.template).toBe('subtitle');
    expect(layout.lines).toEqual([]);
    expect(layout.meta.text).toBe('1:02:03');
  });

  it('画到 canvas：设定画布尺寸、画面、底条与每行文字，最后清掉阴影', () => {
    const calls = [];
    const ctx = new Proxy({ createLinearGradient: () => ({ addColorStop: () => {} }) }, {
      get(target, prop) {
        if (prop in target) return target[prop];
        return (...args) => calls.push([prop, ...args]);
      },
      set(target, prop, value) { target[prop] = value; return true; }
    });
    const canvas = {};
    const image = { naturalWidth: 960, naturalHeight: 540 };
    const layout = layoutQuoteCard({ ...base, template: 'subtitle' });
    paintQuoteCard(canvas, ctx, image, layout);
    expect(canvas).toEqual({ width: 1920, height: 1080 });
    expect(calls).toContainEqual(['drawImage', image, 0, 0, 1920, 1080]);
    expect(calls.filter(call => call[0] === 'fillText').map(call => call[1])).toEqual(['我们走吧', '《长片》  1:02:03']);
    expect(ctx.shadowBlur).toBe(0);
    expect(ctx.shadowColor).toBe('transparent');
  });
});
