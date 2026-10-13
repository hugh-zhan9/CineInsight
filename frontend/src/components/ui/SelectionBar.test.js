import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';
import { h } from 'vue';

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

  it('selectAllMatchingLabel is the one wording for the bar and the library toolbar', () => {
    expect(selectAllMatchingLabel(20)).toBe('全选当前结果（20）');
    expect(selectAllMatchingLabel(null)).toBe('全选当前结果');
    expect(selectAllMatchingLabel(20, true)).toBe('正在选择…');
  });
});
