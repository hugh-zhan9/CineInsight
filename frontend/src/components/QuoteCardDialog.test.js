import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ SaveQuoteCard: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);

import QuoteCardDialog from './QuoteCardDialog.vue';

// jsdom 没有 2D 画布也不真的加载图片：画布换成记录调用的假上下文，Image 换成设了 src 就回调的假图。
let fillTexts;
let frameFails;
let requestedSources;
const BLOB = { type: 'image/png', size: 3 };

class FakeImage {
  constructor() {
    this.naturalWidth = 960;
    this.naturalHeight = 540;
  }

  set src(value) {
    requestedSources.push(value);
    Promise.resolve().then(() => (frameFails ? this.onerror?.() : this.onload?.()));
  }
}

function fakeContext() {
  return new Proxy({ measureText: text => ({ width: [...text].length * 20 }), createLinearGradient: () => ({ addColorStop: () => {} }) }, {
    get(target, prop) {
      if (prop in target) return target[prop];
      if (prop === 'fillText') return text => fillTexts.push(text);
      return () => {};
    },
    set(target, prop, value) { target[prop] = value; return true; }
  });
}

const wrappers = [];
async function mountDialog(props = {}) {
  const wrapper = mount(QuoteCardDialog, {
    props: { videoID: 7, atMS: 3_723_000, quote: '<i>我们走吧</i>', title: '长片', ...props },
    global: { stubs: { teleport: true } }
  });
  wrappers.push(wrapper);
  await flushPromises();
  return wrapper;
}

function setClipboard(value) {
  Object.defineProperty(window.navigator, 'clipboard', { value, configurable: true });
}

beforeEach(() => {
  vi.clearAllMocks();
  fillTexts = [];
  frameFails = false;
  requestedSources = [];
  vi.stubGlobal('Image', FakeImage);
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(() => fakeContext());
  vi.spyOn(HTMLCanvasElement.prototype, 'toBlob').mockImplementation(callback => callback(BLOB));
  vi.spyOn(HTMLCanvasElement.prototype, 'toDataURL').mockReturnValue('data:image/png;base64,iVBORw0KGgo=');
  window.ClipboardItem = class { constructor(items) { this.items = items; } };
  setClipboard({ write: vi.fn(() => Promise.resolve()) });
});

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  delete window.ClipboardItem;
  setClipboard(undefined);
});

describe('台词卡弹窗', () => {
  it('取那一刻的大尺寸单帧，按字幕条版式画出台词、片名与时间', async () => {
    const wrapper = await mountDialog();
    expect(requestedSources).toEqual(['/preview/frame/7?ms=3723000&w=960']);
    const canvas = wrapper.get('[data-test="quote-card-canvas"]');
    expect(canvas.attributes('data-template')).toBe('subtitle');
    expect([canvas.element.width, canvas.element.height]).toEqual([1920, 1080]);
    expect(fillTexts).toEqual(['我们走吧', '《长片》  1:02:03']);
    expect(wrapper.get('[data-test="quote-card-quote"]').element.value).toBe('我们走吧');
    expect(wrapper.get('[data-test="quote-card-meta"]').text()).toContain('《长片》 · 1:02:03');
    expect(wrapper.get('[data-test="quote-card-copy"]').attributes('disabled')).toBeUndefined();
  });

  it('切换到海报版式：画面下面多出文字面板，画布变高', async () => {
    const wrapper = await mountDialog();
    await wrapper.get('[data-test="quote-card-template-poster"]').trigger('click');
    const canvas = wrapper.get('[data-test="quote-card-canvas"]');
    expect(canvas.attributes('data-template')).toBe('poster');
    expect(canvas.element.height).toBeGreaterThan(1080);
    expect(wrapper.get('[data-test="quote-card-template-poster"]').attributes('aria-checked')).toBe('true');
    await wrapper.get('[data-test="quote-card-template-subtitle"]').trigger('click');
    expect(canvas.element.height).toBe(1080);
  });

  it('改台词后实时重画；太长时提示只显示前几行', async () => {
    const wrapper = await mountDialog({ quote: '' });
    expect(fillTexts).toEqual(['《长片》  1:02:03']);
    fillTexts = [];
    await wrapper.get('[data-test="quote-card-quote"]').setValue('第一行\n第二行');
    expect(fillTexts).toEqual(['第一行', '第二行', '《长片》  1:02:03']);
    expect(wrapper.find('[data-test="quote-card-truncated"]').exists()).toBe(false);
    await wrapper.get('[data-test="quote-card-quote"]').setValue('一\n二\n三\n四\n五');
    expect(wrapper.get('[data-test="quote-card-truncated"]').text()).toContain('只显示前 4 行');
  });

  it('画面读不出来：说明原因并禁用导出', async () => {
    frameFails = true;
    const wrapper = await mountDialog();
    expect(wrapper.get('[data-test="quote-card-frame-error"]').text()).toContain('读取这一刻的画面失败');
    expect(wrapper.get('[data-test="quote-card-copy"]').attributes('disabled')).toBeDefined();
    expect(wrapper.get('[data-test="quote-card-save"]').attributes('disabled')).toBeDefined();
    expect(fillTexts).toEqual([]);
  });

  it('复制图片：把 PNG 交给系统剪贴板', async () => {
    const write = vi.fn(() => Promise.resolve());
    setClipboard({ write });
    const wrapper = await mountDialog();
    await wrapper.get('[data-test="quote-card-copy"]').trigger('click');
    await flushPromises();
    expect(write).toHaveBeenCalledTimes(1);
    const [item] = write.mock.calls[0][0];
    await expect(item.items['image/png']).resolves.toBe(BLOB);
    expect(wrapper.get('[data-test="quote-card-status"]').text()).toBe('已复制到剪贴板。');
  });

  it('剪贴板写入失败：报出错误，保存为 PNG 仍可用', async () => {
    setClipboard({ write: vi.fn(() => Promise.reject(new Error('NotAllowedError: denied'))) });
    const wrapper = await mountDialog();
    await wrapper.get('[data-test="quote-card-copy"]').trigger('click');
    await flushPromises();
    const status = wrapper.get('[data-test="quote-card-status"]');
    expect(status.text()).toContain('复制失败：NotAllowedError: denied');
    expect(status.attributes('role')).toBe('alert');
    expect(wrapper.get('[data-test="quote-card-save"]').attributes('disabled')).toBeUndefined();
    expect(api.SaveQuoteCard).not.toHaveBeenCalled();
  });

  it('当前环境没有剪贴板图片接口：直接说明，不悄悄改走保存', async () => {
    setClipboard(undefined);
    const wrapper = await mountDialog();
    await wrapper.get('[data-test="quote-card-copy"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="quote-card-status"]').text()).toContain('当前环境不支持复制图片');
    expect(api.SaveQuoteCard).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="quote-card-save"]').attributes('disabled')).toBeUndefined();
  });

  it('生成 PNG 本身失败（例如画布不可导出）也按复制失败报出', async () => {
    HTMLCanvasElement.prototype.toBlob.mockImplementation(callback => callback(null));
    const write = vi.fn(items => items[0].items['image/png']);
    setClipboard({ write });
    const wrapper = await mountDialog();
    await wrapper.get('[data-test="quote-card-copy"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="quote-card-status"]').text()).toContain('复制失败：生成图片失败');
  });

  it('保存为 PNG：去掉 data: 前缀交给后端，显示保存位置', async () => {
    api.SaveQuoteCard.mockResolvedValue('/Users/me/Desktop/长片 1-02-03.png');
    const wrapper = await mountDialog();
    await wrapper.get('[data-test="quote-card-save"]').trigger('click');
    await flushPromises();
    expect(api.SaveQuoteCard).toHaveBeenCalledWith('iVBORw0KGgo=', '长片 1-02-03.png');
    expect(wrapper.get('[data-test="quote-card-status"]').text()).toBe('已保存到 /Users/me/Desktop/长片 1-02-03.png');
  });

  it('保存框里取消：不提示成功也不报错', async () => {
    api.SaveQuoteCard.mockResolvedValue('');
    const wrapper = await mountDialog();
    await wrapper.get('[data-test="quote-card-save"]').trigger('click');
    await flushPromises();
    expect(api.SaveQuoteCard).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[data-test="quote-card-status"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="quote-card-save"]').attributes('disabled')).toBeUndefined();
  });

  it('保存失败：报出后端的原因', async () => {
    api.SaveQuoteCard.mockRejectedValue('保存台词卡失败，请检查目录权限和磁盘空间');
    const wrapper = await mountDialog();
    await wrapper.get('[data-test="quote-card-save"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="quote-card-status"]').text()).toBe('保存失败：保存台词卡失败，请检查目录权限和磁盘空间');
  });

  it('关闭按钮通知宿主收起', async () => {
    const wrapper = await mountDialog();
    await wrapper.get('[data-test="quote-card-close"]').trigger('click');
    expect(wrapper.emitted('close')).toHaveLength(1);
  });
});
