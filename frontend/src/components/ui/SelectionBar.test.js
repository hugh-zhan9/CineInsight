import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';

import SelectionBar from './SelectionBar.vue';

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
});
