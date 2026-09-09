import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { nextTick } from 'vue';
import FeedSheet from '../short-feed/components/FeedSheet.vue';
import FeedMeta from '../short-feed/components/FeedMeta.vue';
import ShortFeedApp from '../short-feed/ShortFeedApp.vue';

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('手机标签布局', () => {
  it('面板随软键盘和视口平移调整，关闭后释放监听', async () => {
    const viewport = new EventTarget();
    Object.assign(viewport, { height: 844, width: 390, offsetTop: 0, offsetLeft: 0 });
    vi.stubGlobal('visualViewport', viewport);
    const remove = vi.spyOn(viewport, 'removeEventListener');
    const host = document.createElement('main');
    host.className = 'short-feed';
    document.body.append(host);
    const wrapper = mount(FeedSheet, {
      attachTo: host, props: { title: '标签' },
      slots: { default: '<input class="sheet-input">', footer: '<button>完成</button>' }
    });
    const layer = document.querySelector('.sheet-layer');
    await nextTick();
    expect(layer.parentElement).toBe(document.body);
    expect(layer.style.height).toBe('844px');
    Object.assign(viewport, { height: 370, offsetTop: 55 });
    viewport.dispatchEvent(new Event('resize'));
    await nextTick();
    expect(layer.style.height).toBe('370px');
    expect(layer.style.top).toBe('55px');
    viewport.offsetTop = 80;
    viewport.dispatchEvent(new Event('scroll'));
    await nextTick();
    expect(layer.style.top).toBe('80px');
    Object.assign(viewport, { height: 844, offsetTop: 0 });
    viewport.dispatchEvent(new Event('resize'));
    await nextTick();
    expect(layer.style.height).toBe('844px');
    document.querySelector('[data-test="sheet-close"]').click();
    expect(wrapper.emitted('close')).toHaveLength(1);
    wrapper.unmount();
    expect(remove.mock.calls.map(([name]) => name)).toEqual(['resize', 'scroll']);
    expect(document.querySelector('.sheet-layer')).toBeNull();
  });

  it('没有 visualViewport 仍能打开关闭', () => {
    vi.stubGlobal('visualViewport', undefined);
    const wrapper = mount(FeedSheet, { props: { title: '标签' } });
    expect(document.querySelector('.sheet-layer').style.height).toBe('');
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(wrapper.emitted('close')).toHaveLength(1);
    wrapper.unmount();
  });

  it.each([0, 1, 80])('保留 %i 个标签及最后的添加入口', async count => {
    const tags = Array.from({ length: count }, (_, id) => ({ id, name: `标签${id}${'长'.repeat(64)}` }));
    const wrapper = mount(FeedMeta, { props: { item: { name: '视频', tags } } });
    expect(wrapper.findAll('span.tag-chip').map(tag => tag.text())).toEqual(tags.map(tag => tag.name));
    await wrapper.get('button').trigger('click');
    expect(wrapper.emitted('open-tags')).toHaveLength(1);
    const target = wrapper.get('.tag-row').element;
    expect(ShortFeedApp.methods.isInteractiveControl.call({ photoZoomed: false }, target)).toBe(true);
    const preventDefault = vi.fn();
    ShortFeedApp.methods.onTouchStart.call({ photoZoomed: false, isInteractiveControl: () => true }, { target, preventDefault });
    expect(preventDefault).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('面板内容超出可视区时提示还有更多，滚到底消失', async () => {
    vi.stubGlobal('visualViewport', undefined);
    const wrapper = mount(FeedSheet, { props: { title: '标签', tall: true }, slots: { default: '<div class="tag-picker"></div>' } });
    const sheet = document.querySelector('.sheet');
    expect(sheet.classList.contains('sheet--tall')).toBe(true);
    const body = document.querySelector('.sheet__body');
    expect(document.querySelector('[data-test="sheet-more"]')).toBeNull();
    Object.defineProperty(body, 'scrollHeight', { configurable: true, value: 1200 });
    Object.defineProperty(body, 'clientHeight', { configurable: true, value: 500 });
    body.scrollTop = 0;
    body.dispatchEvent(new Event('scroll'));
    await nextTick();
    expect(document.querySelector('[data-test="sheet-more"]')).not.toBeNull();
    Object.defineProperty(body, 'scrollTop', { configurable: true, value: 700 });
    body.dispatchEvent(new Event('scroll'));
    await nextTick();
    expect(document.querySelector('[data-test="sheet-more"]')).toBeNull();
    wrapper.unmount();
  });

  it('舞台标签行溢出时提示更多标签', async () => {
    const tags = Array.from({ length: 30 }, (_, id) => ({ id, name: `标签${id}` }));
    const wrapper = mount(FeedMeta, { props: { item: { name: '视频', tags } } });
    const row = wrapper.get('.tag-row').element;
    expect(wrapper.find('[data-test="tag-row-more"]').exists()).toBe(false);
    Object.defineProperty(row, 'scrollHeight', { configurable: true, value: 600 });
    Object.defineProperty(row, 'clientHeight', { configurable: true, value: 200 });
    await wrapper.get('.tag-row').trigger('scroll');
    expect(wrapper.find('[data-test="tag-row-more"]').exists()).toBe(true);
    Object.defineProperty(row, 'scrollTop', { configurable: true, value: 400 });
    await wrapper.get('.tag-row').trigger('scroll');
    expect(wrapper.find('[data-test="tag-row-more"]').exists()).toBe(false);
    wrapper.unmount();
  });
});
