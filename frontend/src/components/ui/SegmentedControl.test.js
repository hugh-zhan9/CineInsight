import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import SegmentedControl from './SegmentedControl.vue';

const OPTIONS = [
  { value: 'list', label: '列表', icon: 'filter' },
  { value: 'grid', label: '网格' },
  { value: 'wall', label: '海报墙' }
];

function mountControl(modelValue = 'grid') {
  return mount(SegmentedControl, {
    props: { options: OPTIONS, modelValue, label: '视图' },
    attrs: { 'data-test': 'view-switch' },
    attachTo: document.body
  });
}

describe('SegmentedControl', () => {
  it('is a labelled radiogroup with only the checked option in the tab order', () => {
    const wrapper = mountControl();
    expect(wrapper.attributes('role')).toBe('radiogroup');
    expect(wrapper.attributes('aria-label')).toBe('视图');
    expect(wrapper.attributes('data-test')).toBe('view-switch');
    const radios = wrapper.findAll('[role="radio"]');
    expect(radios.map(radio => radio.attributes('aria-checked'))).toEqual(['false', 'true', 'false']);
    expect(radios.map(radio => radio.attributes('tabindex'))).toEqual(['-1', '0', '-1']);
    expect(radios[0].find('svg').attributes('data-icon')).toBe('filter');
    wrapper.unmount();
  });

  it('click selects; clicking the current option emits nothing', async () => {
    const wrapper = mountControl();
    const radios = wrapper.findAll('[role="radio"]');
    await radios[1].trigger('click');
    expect(wrapper.emitted('update:modelValue')).toBeUndefined();
    await radios[2].trigger('click');
    expect(wrapper.emitted('update:modelValue')).toEqual([['wall']]);
    wrapper.unmount();
  });

  it('arrow keys move the selection with wrap-around; Home / End jump to the ends', async () => {
    const wrapper = mountControl('wall');
    const group = wrapper.get('[role="radiogroup"]');
    await group.trigger('keydown', { key: 'ArrowRight' });
    await group.trigger('keydown', { key: 'ArrowUp' });
    await group.trigger('keydown', { key: 'Home' });
    await group.trigger('keydown', { key: 'End' });
    await group.trigger('keydown', { key: 'Enter' });
    expect(wrapper.emitted('update:modelValue')).toEqual([['list'], ['grid'], ['list']]);
    await wrapper.vm.$nextTick();
    expect(document.activeElement).toBe(wrapper.findAll('[role="radio"]')[2].element);
    wrapper.unmount();
  });

  it('← on the first option wraps to the last one', async () => {
    const wrapper = mountControl('list');
    await wrapper.get('[role="radiogroup"]').trigger('keydown', { key: 'ArrowLeft' });
    expect(wrapper.emitted('update:modelValue')).toEqual([['wall']]);
    wrapper.unmount();
  });

  it('with nothing selected the first option is focusable', () => {
    const wrapper = mountControl(null);
    expect(wrapper.findAll('[role="radio"]').map(radio => radio.attributes('tabindex'))).toEqual(['0', '-1', '-1']);
    wrapper.unmount();
  });
});
