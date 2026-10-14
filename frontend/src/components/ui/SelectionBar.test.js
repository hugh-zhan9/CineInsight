import { mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { h, vShow, withDirectives } from 'vue';

import SelectionBar, { selectAllMatchingLabel, selectionBarZone } from './SelectionBar.vue';

const mounted = [];
function mountBar(props) {
  const wrapper = mount(SelectionBar, {
    props,
    attrs: { 'data-test': 'library-selection' },
    slots: { actions: '<button type="button" class="btn btn--danger btn--sm">移到废纸篓</button>' },
    attachTo: document.body
  });
  mounted.push(wrapper);
  return wrapper;
}
function pressEscape(target = document.body) {
  const event = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
  target.dispatchEvent(event);
  return event;
}

afterEach(() => {
  while (mounted.length) mounted.pop().unmount();
  document.body.innerHTML = '';
});

describe('SelectionBar', () => {
  it('is absent when nothing is selected, so there are no batch actions on the page', () => {
    const wrapper = mountBar({ count: 0, totalMatching: 30 });
    expect(wrapper.find('[role="toolbar"]').exists()).toBe(false);
    expect(wrapper.text()).toBe('');
  });

  it('shows the fixed wording, the count and the actions slot', () => {
    const wrapper = mountBar({ count: 3, totalMatching: 120 });
    const bar = wrapper.get('[role="toolbar"]');
    expect(bar.attributes('data-test')).toBe('library-selection');
    expect(wrapper.get('[data-test="selection-bar-count"]').text()).toBe('已选 3 项');
    expect(wrapper.get('[data-test="selection-bar-select-all-matching"]').text()).toBe('全选当前结果（120）');
    expect(wrapper.get('[data-test="selection-bar-clear"]').text()).toBe('取消选择Esc');
    expect(wrapper.get('.ui-selection-bar__actions button').text()).toBe('移到废纸篓');
  });

  it('emits select-all-matching and clear; the select-all link only appears when it adds something', async () => {
    const wrapper = mountBar({ count: 3, totalMatching: 120 });
    await wrapper.get('[data-test="selection-bar-select-all-matching"]').trigger('click');
    await wrapper.get('[data-test="selection-bar-clear"]').trigger('click');
    expect(wrapper.emitted('select-all-matching')).toHaveLength(1);
    expect(wrapper.emitted('clear')).toHaveLength(1);

    await wrapper.setProps({ count: 120 });
    expect(wrapper.find('[data-test="selection-bar-select-all-matching"]').exists()).toBe(false);
    await wrapper.setProps({ count: 3, totalMatching: null });
    expect(wrapper.find('[data-test="selection-bar-select-all-matching"]').exists()).toBe(false);
  });

  it('after 全选当前结果 the count covers the whole result', async () => {
    const wrapper = mountBar({ count: 3, totalMatching: 120, allMatchingSelected: true });
    expect(wrapper.get('[data-test="selection-bar-count"]').text()).toBe('已选当前结果全部 120 项');
    expect(wrapper.find('[data-test="selection-bar-select-all-matching"]').exists()).toBe(false);
  });

  it('a dialog left mounted inside a hidden page (v-show) does not block Esc here', () => {
    const wrapper = mountBar({ count: 2 });
    const hiddenPage = document.createElement('section');
    hiddenPage.style.display = 'none';
    const drawer = document.createElement('aside');
    drawer.setAttribute('role', 'dialog');
    hiddenPage.appendChild(drawer);
    document.body.appendChild(hiddenPage);
    expect(pressEscape().defaultPrevented).toBe(true);
    expect(wrapper.emitted('clear')).toHaveLength(1);
  });

  it('Esc clears, except inside inputs, under an open dialog, or when already handled', () => {
    const wrapper = mountBar({ count: 2 });
    expect(pressEscape().defaultPrevented).toBe(true);
    expect(wrapper.emitted('clear')).toHaveLength(1);

    const input = document.createElement('input');
    document.body.appendChild(input);
    expect(pressEscape(input).defaultPrevented).toBe(false);

    const dialog = document.createElement('div');
    dialog.setAttribute('role', 'dialog');
    document.body.appendChild(dialog);
    pressEscape();
    dialog.remove();

    const claimed = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
    claimed.preventDefault();
    document.body.dispatchEvent(claimed);
    expect(wrapper.emitted('clear')).toHaveLength(1);
  });

  it('Esc does nothing while hidden', () => {
    const wrapper = mountBar({ count: 0 });
    pressEscape();
    expect(wrapper.emitted('clear')).toBeUndefined();
  });

  // M-6：宿主页面被 v-show 藏起来（切到别的页面）时，那页的勾选还在，但 Esc 不能替看不见的页面清空。
  it('Esc does nothing while the host page is hidden with v-show, and works again once it is shown', async () => {
    const onClear = [];
    const Host = {
      props: { shown: Boolean },
      render() {
        return h('div', { style: this.shown ? {} : { display: 'none' } }, [
          h('section', [h(SelectionBar, { count: 2, onClear: () => onClear.push(1) })])
        ]);
      }
    };
    const wrapper = mount(Host, { props: { shown: false }, attachTo: document.body });
    mounted.push(wrapper);
    expect(pressEscape().defaultPrevented).toBe(false);
    expect(onClear).toHaveLength(0);

    await wrapper.setProps({ shown: true });
    expect(pressEscape().defaultPrevented).toBe(true);
    expect(onClear).toHaveLength(1);
  });

  // M-6：清理审阅的勾选是审阅结果——宿主关掉 Esc 清空，「取消选择」照旧，也不再提示 Esc。
  it('clear-on-escape=false ignores Esc and drops the Esc hint; 取消选择 still clears', async () => {
    const wrapper = mountBar({ count: 2, clearOnEscape: false });
    expect(pressEscape().defaultPrevented).toBe(false);
    expect(wrapper.emitted('clear')).toBeUndefined();
    expect(wrapper.get('[data-test="selection-bar-clear"]').text()).toBe('取消选择');
    expect(wrapper.find('.ui-selection-bar__kbd').exists()).toBe(false);
    await wrapper.get('[data-test="selection-bar-clear"]').trigger('click');
    expect(wrapper.emitted('clear')).toHaveLength(1);
  });

  // M-2：宿主补取剩余的页时按钮禁用、写「正在选择…」。
  it('while the host is selecting all, the link is disabled and says 正在选择…', () => {
    const wrapper = mountBar({ count: 3, totalMatching: 120, selectingAll: true });
    const link = wrapper.get('[data-test="selection-bar-select-all-matching"]');
    expect(link.text()).toBe('正在选择…');
    expect(link.attributes('disabled')).toBeDefined();
  });

  it('the two built-in links take the host hooks through select-all-data-test / clear-data-test', async () => {
    const wrapper = mountBar({ count: 1, totalMatching: 5, selectAllDataTest: 'photo-select-all', clearDataTest: 'photo-clear-selection' });
    expect(wrapper.find('[data-test="selection-bar-select-all-matching"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="selection-bar-clear"]').exists()).toBe(false);
    const selectAll = wrapper.get('[data-test="photo-select-all"]');
    expect(selectAll.text()).toBe('全选当前结果（5）');
    await selectAll.trigger('click');
    await wrapper.get('[data-test="photo-clear-selection"]').trigger('click');
    expect(wrapper.emitted('select-all-matching')).toHaveLength(1);
    expect(wrapper.emitted('clear')).toHaveLength(1);
    expect(wrapper.get('[data-test="selection-bar-count"]').text()).toBe('已选 1 项');
  });

  it('selectAllMatchingLabel is the one wording for the bar and the library toolbar', () => {
    expect(selectAllMatchingLabel(20)).toBe('全选当前结果（20）');
    expect(selectAllMatchingLabel(null)).toBe('全选当前结果');
    expect(selectAllMatchingLabel(20, true)).toBe('正在选择…');
  });
});

// 看得见的操作条在窗口里的竖直范围（离窗口底边：top 上沿、bottom 下沿），给全局提示判断放在它下面还是抬到它上面
// （AppFeedback，判断在 layering.test.js 里核对）。jsdom 不排版、没有 ResizeObserver：这里换一个能手动通知的
// ResizeObserver、把 requestAnimationFrame 攒起来手动放，并替浏览器给出操作条的位置（页面被 v-show 藏起来时高度是 0）。
describe('SelectionBar 报出自己在窗口里的竖直范围', () => {
  const VIEWPORT = 768;
  let observers;
  let frames;
  beforeEach(() => {
    observers = [];
    frames = [];
    vi.stubGlobal('ResizeObserver', class {
      constructor(callback) {
        this.callback = callback;
        this.targets = new Set();
        observers.push(this);
      }
      observe(target) { this.targets.add(target); }
      unobserve(target) { this.targets.delete(target); }
      disconnect() { this.targets.clear(); }
    });
    vi.stubGlobal('requestAnimationFrame', callback => frames.push(callback));
    vi.stubGlobal('cancelAnimationFrame', id => { frames[id - 1] = null; });
    vi.stubGlobal('innerHeight', VIEWPORT);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const zone = () => ({ ...selectionBarZone });
  const NONE = { visible: false, top: 0, bottom: 0 };
  const flushFrames = () => {
    const pending = frames;
    frames = [];
    for (const callback of pending) callback?.();
  };
  // 替浏览器排版：操作条上沿在 top、高 height（0 = 藏起来）。返回量位置的次数，用来核对一帧只量一次。
  function place(bar, top, height) {
    bar.reads = 0;
    bar.getBoundingClientRect = () => {
      bar.reads += 1;
      return { top, bottom: top + height, left: 0, right: height ? 600 : 0, width: height ? 600 : 0, height };
    };
  }
  const resized = bar => {
    for (const observer of observers) if (observer.targets.has(bar)) observer.callback([{ target: bar }]);
  };
  // 片库的样子：内容区（.main-view）自己滚，操作条在页面区块的末尾吸底，和整页一起挂载——挂载时操作条所在的子树
  // 还没挂进内容区（以前在那时往上找滚动区域，找不到就落到 window 上，内容区滚动时不再重新量）。
  function mountInScroller(props = { count: 3 }) {
    const scroller = document.createElement('div');
    scroller.style.overflowY = 'auto';
    document.body.appendChild(scroller);
    const host = document.createElement('div');
    scroller.appendChild(host);
    const Page = {
      props: { count: { type: Number, default: 0 } },
      render() { return h('section', [h('div', { class: 'rows' }), h(SelectionBar, { count: this.count })]); }
    };
    const wrapper = mount(Page, { props, attachTo: host });
    mounted.push(wrapper);
    return { wrapper, scroller, bar: () => wrapper.get('.ui-selection-bar').element };
  }

  it('吸底时下沿离底 16；折成三行时上沿跟着变', () => {
    const { bar } = mountInScroller();
    place(bar(), VIEWPORT - 16 - 46, 46);
    flushFrames();
    expect(zone()).toEqual({ visible: true, top: 62, bottom: 16 });
    place(bar(), VIEWPORT - 16 - 134, 134);
    resized(bar());
    flushFrames();
    expect(zone()).toEqual({ visible: true, top: 150, bottom: 16 });
  });

  it('列表短、操作条跟在最后一行后面停在半屏处：报出它真实的位置', () => {
    const { bar } = mountInScroller();
    place(bar(), 300, 46);
    flushFrames();
    expect(zone()).toEqual({ visible: true, top: 468, bottom: 422 });
  });

  it('所在区域滚动时重新量（一帧只量一次）；窗口改大小时也重新量', () => {
    const { bar, scroller } = mountInScroller();
    place(bar(), 300, 46);
    flushFrames();
    // 往下滚：操作条从半屏处落到底部吸住。一帧里来三次 scroll，只量一次。
    place(bar(), VIEWPORT - 16 - 46, 46);
    for (let i = 0; i < 3; i++) scroller.dispatchEvent(new Event('scroll'));
    expect(frames).toHaveLength(1);
    flushFrames();
    expect(bar().reads).toBe(1);
    expect(zone()).toEqual({ visible: true, top: 62, bottom: 16 });
    place(bar(), 200, 46);
    window.dispatchEvent(new Event('resize'));
    flushFrames();
    expect(zone()).toEqual({ visible: true, top: 568, bottom: 522 });
  });

  it('选中清空（操作条消失）与卸载时撤掉监听和范围', async () => {
    const { wrapper, scroller, bar } = mountInScroller();
    const element = bar();
    place(element, 300, 46);
    flushFrames();
    const scrollerRemove = vi.spyOn(scroller, 'removeEventListener');
    const windowRemove = vi.spyOn(window, 'removeEventListener');
    await wrapper.setProps({ count: 0 });
    expect(zone()).toEqual(NONE);
    expect(scrollerRemove).toHaveBeenCalledWith('scroll', expect.any(Function));
    expect(windowRemove).toHaveBeenCalledWith('resize', expect.any(Function));
    expect(observers.every(observer => !observer.targets.has(element))).toBe(true);
    scroller.dispatchEvent(new Event('scroll'));
    window.dispatchEvent(new Event('resize'));
    expect(frames.filter(Boolean)).toHaveLength(0);

    await wrapper.setProps({ count: 2 });
    place(bar(), 300, 46);
    flushFrames();
    expect(zone().visible).toBe(true);
    scrollerRemove.mockClear();
    mounted.splice(mounted.indexOf(wrapper), 1);
    wrapper.unmount();
    expect(zone()).toEqual(NONE);
    expect(scrollerRemove).toHaveBeenCalledWith('scroll', expect.any(Function));
  });

  // 片库、图片管理、清理中心切走时只隐藏不卸载，各自的操作条还挂着：只算看得见的那条。
  it('所在页面被 v-show 藏起来时不算；几个页面各有一条时只算看得见的，都看得见时合成一个范围', async () => {
    const Pages = {
      props: { active: { type: String, default: 'library' } },
      render() {
        return h('div', ['library', 'photos'].map(page => withDirectives(
          h('section', { 'data-page': page }, [h(SelectionBar, { count: 2 })]),
          [[vShow, this.active === page]]
        )));
      }
    };
    const wrapper = mount(Pages, { attachTo: document.body });
    mounted.push(wrapper);
    const barIn = page => wrapper.get(`[data-page="${page}"] .ui-selection-bar`).element;
    place(barIn('library'), VIEWPORT - 16 - 90, 90);
    place(barIn('photos'), 0, 0);
    flushFrames();
    expect(zone()).toEqual({ visible: true, top: 106, bottom: 16 });

    // 切到图片管理：片库那条藏起来量出 0（ResizeObserver 报这一下），图片管理那条量出实际位置。
    await wrapper.setProps({ active: 'photos' });
    place(barIn('library'), 0, 0);
    place(barIn('photos'), 300, 46);
    resized(barIn('library'));
    resized(barIn('photos'));
    flushFrames();
    expect(zone()).toEqual({ visible: true, top: 468, bottom: 422 });

    // 两条都看得见：合成一个范围（最高的上沿、最低的下沿）。
    place(barIn('library'), VIEWPORT - 16 - 46, 46);
    resized(barIn('library'));
    flushFrames();
    expect(zone()).toEqual({ visible: true, top: 468, bottom: 16 });
    await wrapper.setProps({ active: 'none' });
    place(barIn('library'), 0, 0);
    place(barIn('photos'), 0, 0);
    resized(barIn('library'));
    resized(barIn('photos'));
    flushFrames();
    expect(zone()).toEqual(NONE);
  });
});
