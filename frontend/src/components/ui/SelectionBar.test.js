import { mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { h, vShow, withDirectives } from 'vue';

import SelectionBar, { selectAllMatchingLabel } from './SelectionBar.vue';

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

// 吸底的操作条把自己从窗口底边往上占的高度（自身高度 + 吸底的 16）写在根节点的 --selection-bar-inset 上，
// 全局提示据此抬到它上面（AppFeedback，位置在 layering.test.js 里核对）。jsdom 没有 ResizeObserver 也不排版：
// 这里换一个能手动通知的 ResizeObserver，并替浏览器给出操作条量出来的高度（页面被 v-show 藏起来时是 0）。
describe('SelectionBar 把吸底占的高度写在根节点上', () => {
  let observers;
  beforeEach(() => {
    observers = [];
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
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    document.documentElement.style.removeProperty('--selection-bar-inset');
  });

  const inset = () => document.documentElement.style.getPropertyValue('--selection-bar-inset');
  const observed = bar => observers.some(observer => observer.targets.has(bar));
  // 浏览器排版后 ResizeObserver 报的那一下。jsdom 不加载组件样式，照样式表给操作条写上吸底的 16。
  function layout(bar, height) {
    bar.style.bottom = '16px';
    bar.getBoundingClientRect = () => ({ top: 0, left: 0, right: height ? 600 : 0, bottom: height, width: height ? 600 : 0, height });
    for (const observer of observers) if (observer.targets.has(bar)) observer.callback([{ target: bar }]);
  }

  it('看得见时写上高度 + 16，折成两三行时跟着变；选中清空（操作条消失）时去掉', async () => {
    const wrapper = mountBar({ count: 3, totalMatching: 120 });
    const bar = wrapper.get('.ui-selection-bar').element;
    expect(observed(bar)).toBe(true);
    expect(inset()).toBe('');
    layout(bar, 46);
    expect(inset()).toBe('62px');
    layout(bar, 90);
    expect(inset()).toBe('106px');
    layout(bar, 134);
    expect(inset()).toBe('150px');
    await wrapper.setProps({ count: 0 });
    expect(wrapper.find('.ui-selection-bar').exists()).toBe(false);
    expect(observed(bar)).toBe(false);
    expect(inset()).toBe('');
  });

  it('卸载时去掉', () => {
    const wrapper = mountBar({ count: 2 });
    layout(wrapper.get('.ui-selection-bar').element, 46);
    expect(inset()).toBe('62px');
    mounted.splice(mounted.indexOf(wrapper), 1);
    wrapper.unmount();
    expect(inset()).toBe('');
  });

  // 片库、图片管理、清理中心切走时只隐藏不卸载，各自的操作条还挂着：只算看得见的那条。
  it('所在页面被 v-show 藏起来时不算；几个页面各有一条时只算看得见的，都看得见取最高的', async () => {
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
    // 图片管理藏着：它的操作条量出来是 0。
    layout(barIn('library'), 90);
    layout(barIn('photos'), 0);
    expect(inset()).toBe('106px');

    // 切到图片管理：片库那条变 0，图片管理那条量出实际高度。
    await wrapper.setProps({ active: 'photos' });
    layout(barIn('library'), 0);
    layout(barIn('photos'), 46);
    expect(inset()).toBe('62px');

    // 两条都看得见（同一页上）时取最高的；都藏起来时去掉。
    layout(barIn('library'), 134);
    expect(inset()).toBe('150px');
    await wrapper.setProps({ active: 'none' });
    layout(barIn('library'), 0);
    layout(barIn('photos'), 0);
    expect(inset()).toBe('');
  });
});
