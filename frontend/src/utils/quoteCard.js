// 台词卡片（2026-10-12）：选一句台词，配上那一刻的画面合成一张可分享的图（画面 + 台词 + 片名 + 时间）。
// 这里放纯计算——台词清洗、当前字幕条目、按宽度折行、两种版式的排版——以及把排版画到 canvas 上的那一步；
// 弹窗 QuoteCardDialog.vue 只管状态与导出。场景检索页与详情抽屉共用。

import { formatSceneTime, sceneFrameURL } from './sceneSearch.js';

// 卡片上的台词最多 120 个字；字幕条版式最多显示 4 行、海报 6 行，再多的以「…」截断。
export const QUOTE_MAX_LENGTH = 120;

// 单帧路由允许的最大宽度（后端 frameAtMaxWidth）；卡片再把长边放大到 1920，让文字足够清晰。
export const QUOTE_CARD_FRAME_WIDTH = 960;
export const QUOTE_CARD_LONG_EDGE = 1920;

export const QUOTE_CARD_TEMPLATES = [
  { value: 'subtitle', label: '字幕条', maxLines: 4 },
  { value: 'poster', label: '海报', maxLines: 6 }
];

// 与应用正文同一套系统字体（tokens.css 的 body font-family）。
const QUOTE_FONT_FAMILY = '"PingFang SC", "Hiragino Sans GB", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif';

// 不能出现在行首的标点：折行时跟前一个字走，避免一行以逗号、句号开头；开引号、开括号反过来跟后一个字走。
const NO_LINE_START = '，。、；：！？）」』】》〉’”…—,.;:!?)]}%';
const NO_LINE_END = '（「『【《〈‘“([{';
// 中日韩文字与全角符号逐字可断；其余连续字符（西文单词、数字）整体折行。
const CJK_CHAR = /[⺀-鿿가-힯豈-﫿＀-￯　-〿]/u;

export function quoteCardFrameURL(videoID, atMS) {
  return sceneFrameURL(videoID, atMS, QUOTE_CARD_FRAME_WIDTH);
}

export function quoteCardTemplate(value) {
  return QUOTE_CARD_TEMPLATES.find(item => item.value === value) || QUOTE_CARD_TEMPLATES[0];
}

// 字幕文本 → 卡片台词：去掉 <i> 一类 HTML 标签与 {\an8} 一类 ASS 覆盖码，逐行去空白、丢空行，截到上限。
export function cleanQuoteText(text) {
  const lines = String(text || '')
    .replace(/\r\n?/g, '\n')
    .replace(/<[^>\n]*>/g, '')
    .replace(/\{\\[^}\n]*\}/g, '')
    .split('\n')
    .map(line => line.trim())
    .filter(Boolean);
  return [...lines.join('\n')].slice(0, QUOTE_MAX_LENGTH).join('').trim();
}

// 对白命中取哪一刻的画面：字幕区间的正中间。字幕起点常常正好卡在切镜头上，取起点容易拿到上一个镜头。
export function quoteCardMomentMS(startMS, endMS) {
  const start = Math.max(0, Math.round(Number(startMS) || 0));
  const end = Math.round(Number(endMS) || 0);
  return end > start ? Math.round((start + end) / 2) : start;
}

// 当前播放位置所在的字幕条目（GetSubtitleSegments 的结果，毫秒闭区间）；没有就返回空串。
export function subtitleCueAt(segments, ms) {
  const position = Number(ms);
  if (!Number.isFinite(position)) return '';
  const cue = (segments || []).find(item => Number(item?.start_time_ms) <= position && position <= Number(item?.end_time_ms));
  return cue ? cleanQuoteText(cue.text || (cue.lines || []).join('\n')) : '';
}

// 保存框里的默认文件名：「片名 1-02-03.png」（时间里的冒号换成短横线）。后端还会再清理一遍非法字符。
export function quoteCardFileName(title, atMS) {
  const name = String(title || '').trim() || '台词卡';
  return `${name} ${formatSceneTime(atMS).replace(/:/g, '-')}.png`;
}

function tokenizeParagraph(paragraph) {
  const tokens = [];
  let word = '';
  const flush = () => {
    if (word) tokens.push(word);
    word = '';
  };
  const onlyOpeners = text => [...text].every(ch => NO_LINE_END.includes(ch));
  for (const ch of paragraph) {
    if (/\s/.test(ch)) {
      flush();
      if (tokens.length && tokens[tokens.length - 1] !== ' ') tokens.push(' ');
    } else if (NO_LINE_START.includes(ch)) {
      if (word) word += ch;
      else if (tokens.length && tokens[tokens.length - 1] !== ' ') tokens[tokens.length - 1] += ch;
      else tokens.push(ch);
    } else if (NO_LINE_END.includes(ch)) {
      if (!onlyOpeners(word)) flush();
      word += ch;
    } else if (CJK_CHAR.test(ch)) {
      if (word && onlyOpeners(word)) {
        word += ch;
        flush();
      } else {
        flush();
        tokens.push(ch);
      }
    } else {
      word += ch;
    }
  }
  flush();
  return tokens;
}

// 按宽度折行（纯函数）：measure(text) 返回文字宽度。显式换行保留、空行丢弃；中文逐字可断，
// 西文按单词断，单个单词比一行还宽时按字符硬断；行首标点跟前一个字走。返回全部行，不截断。
export function wrapQuoteLines(text, maxWidth, measure) {
  const lines = [];
  for (const paragraph of String(text || '').split('\n')) {
    let line = '';
    let space = '';
    const place = token => {
      if (measure(token) <= maxWidth) {
        line = token;
        return;
      }
      let chunk = '';
      for (const ch of token) {
        if (chunk && measure(chunk + ch) > maxWidth) {
          lines.push(chunk);
          chunk = ch;
        } else {
          chunk += ch;
        }
      }
      line = chunk;
    };
    for (const token of tokenizeParagraph(paragraph.trim())) {
      if (token === ' ') {
        if (line) space = ' ';
        continue;
      }
      if (!line) {
        place(token);
        continue;
      }
      const candidate = line + space + token;
      space = '';
      if (measure(candidate) <= maxWidth) {
        line = candidate;
        continue;
      }
      lines.push(line);
      place(token);
    }
    if (line) lines.push(line);
  }
  return lines;
}

// 超过 maxLines 时只留前 maxLines 行，最后一行收短到放得下「…」。
export function clampQuoteLines(lines, maxLines, maxWidth, measure) {
  if (lines.length <= maxLines) return lines;
  const kept = lines.slice(0, maxLines);
  let last = [...kept[maxLines - 1]];
  while (last.length && measure(`${last.join('')}…`) > maxWidth) last = last.slice(0, -1);
  kept[maxLines - 1] = `${last.join('').trimEnd()}…`;
  return kept;
}

// 两种版式的排版（纯函数）：输入画面原始尺寸与文字，输出画布尺寸、画面/底条/文字面板的位置与每行文字的坐标。
// measure(text, font) 量字宽。字号按卡片短边定，横竖画面都合适。
//  - 字幕条：画布 = 画面；台词居中贴近底部，背后一条半透明渐变底条，片名与时间小字跟在台词下面。
//  - 海报：画面在上，下面接一块深色文字面板，台词左对齐，片名与时间在台词下方。
export function layoutQuoteCard({ template, frameWidth, frameHeight, quote, title, atMS, measure }) {
  const spec = quoteCardTemplate(template);
  const scale = QUOTE_CARD_LONG_EDGE / Math.max(1, frameWidth, frameHeight);
  const width = Math.round(frameWidth * scale);
  const frameH = Math.round(frameHeight * scale);
  const unit = Math.min(width, frameH);
  const metaText = [String(title || '').trim() ? `《${String(title).trim()}》` : '', formatSceneTime(atMS)].filter(Boolean).join('  ');
  const poster = spec.value === 'poster';

  const quoteSize = Math.round(unit * (poster ? 0.05 : 0.056));
  const metaSize = Math.round(unit * (poster ? 0.028 : 0.026));
  const lineHeight = Math.round(quoteSize * 1.4);
  const metaHeight = Math.round(metaSize * 1.5);
  const quoteFont = `600 ${quoteSize}px ${QUOTE_FONT_FAMILY}`;
  const metaFont = `400 ${metaSize}px ${QUOTE_FONT_FAMILY}`;
  const padX = Math.round(width * (poster ? 0.06 : 0.08));
  const maxTextWidth = width - padX * 2;
  const measureQuote = text => measure(text, quoteFont);
  const allLines = wrapQuoteLines(quote, maxTextWidth, measureQuote);
  const lines = clampQuoteLines(allLines, spec.maxLines, maxTextWidth, measureQuote);
  const gap = lines.length ? Math.round(quoteSize * 0.6) : 0;
  const textHeight = lines.length * lineHeight + gap + metaHeight;

  const layout = {
    template: spec.value,
    width,
    height: frameH,
    frame: { x: 0, y: 0, w: width, h: frameH },
    band: null,
    panel: null,
    align: poster ? 'left' : 'center',
    quoteFont,
    metaFont,
    quoteSize,
    lines: [],
    meta: null,
    truncated: allLines.length > lines.length
  };

  let top;
  let textX;
  if (poster) {
    const padY = Math.round(unit * 0.07);
    layout.panel = { x: 0, y: frameH, w: width, h: padY * 2 + textHeight };
    layout.height = frameH + layout.panel.h;
    top = frameH + padY;
    textX = padX;
  } else {
    const bottom = Math.round(unit * 0.07);
    top = frameH - bottom - textHeight;
    // 底条从文字上方再留出一段渐变，保证亮画面上也压得住白字。
    const bandTop = Math.max(0, top - Math.round(unit * 0.12));
    layout.band = { x: 0, y: bandTop, w: width, h: frameH - bandTop };
    textX = Math.round(width / 2);
  }
  layout.lines = lines.map((text, index) => ({ text, x: textX, y: top + index * lineHeight + Math.round((lineHeight - quoteSize) / 2) }));
  layout.meta = { text: metaText, x: textX, y: top + lines.length * lineHeight + gap };
  return layout;
}

// 把排版画到 canvas 上（canvas 颜色用字面量即可，它们是图片内容而不是界面配色）。
export function paintQuoteCard(canvas, ctx, image, layout) {
  canvas.width = layout.width;
  canvas.height = layout.height;
  ctx.fillStyle = '#000';
  ctx.fillRect(0, 0, layout.width, layout.height);
  ctx.drawImage(image, layout.frame.x, layout.frame.y, layout.frame.w, layout.frame.h);
  if (layout.band) {
    const { x, y, w, h } = layout.band;
    const gradient = ctx.createLinearGradient(0, y, 0, y + h);
    gradient.addColorStop(0, 'rgba(0, 0, 0, 0)');
    gradient.addColorStop(0.35, 'rgba(0, 0, 0, 0.55)');
    gradient.addColorStop(1, 'rgba(0, 0, 0, 0.78)');
    ctx.fillStyle = gradient;
    ctx.fillRect(x, y, w, h);
  }
  if (layout.panel) {
    const { x, y, w, h } = layout.panel;
    ctx.fillStyle = '#141517';
    ctx.fillRect(x, y, w, h);
  }
  ctx.textAlign = layout.align;
  ctx.textBaseline = 'top';
  if (layout.band) {
    // 字幕条叠在画面上：再加一层文字阴影，白底画面上也看得清。
    ctx.shadowColor = 'rgba(0, 0, 0, 0.85)';
    ctx.shadowBlur = Math.round(layout.quoteSize * 0.18);
    ctx.shadowOffsetY = Math.max(1, Math.round(layout.quoteSize * 0.04));
  }
  ctx.font = layout.quoteFont;
  ctx.fillStyle = '#ffffff';
  for (const line of layout.lines) ctx.fillText(line.text, line.x, line.y);
  ctx.font = layout.metaFont;
  ctx.fillStyle = layout.band ? 'rgba(255, 255, 255, 0.82)' : '#a3a6ab';
  ctx.fillText(layout.meta.text, layout.meta.x, layout.meta.y);
  ctx.shadowColor = 'transparent';
  ctx.shadowBlur = 0;
  ctx.shadowOffsetY = 0;
}

export function loadQuoteCardFrame(url) {
  return new Promise((resolve, reject) => {
    const image = new Image();
    image.onload = () => resolve(image);
    image.onerror = () => reject(new Error('frame_unavailable'));
    image.src = url;
  });
}

export function canvasToPNGBlob(canvas) {
  return new Promise((resolve, reject) => {
    try {
      canvas.toBlob(blob => (blob ? resolve(blob) : reject(new Error('生成图片失败'))), 'image/png');
    } catch (err) {
      reject(err);
    }
  });
}
