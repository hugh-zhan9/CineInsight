import { mount } from '@vue/test-utils';
import { describe, expect, it, vi } from 'vitest';

import SegmentedControl from './SegmentedControl.vue';

const OPTIONS = [
  { value: 'list', label: '列表', icon: 'filter' },
  { value: 'grid', label: '网格' },
  { value: 'wall', label: '海报墙' }
];

function mountControl(modelValue = 'grid', props = {}) {
  return mount(SegmentedControl, {
    props: { options: OPTIONS, modelValue, label: '视图', ...props },
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
    // 宿主照常接住 update:modelValue（v-model）。
    const wrapper = mountControl('wall', { 'onUpdate:modelValue': value => wrapper.setProps({ modelValue: value }) });
    const group = wrapper.get('[role="radiogroup"]');
    await group.trigger('keydown', { key: 'ArrowRight' });
    await group.trigger('keydown', { key: 'ArrowDown' });
    await group.trigger('keydown', { key: 'ArrowUp' });
    await group.trigger('keydown', { key: 'End' });
    await group.trigger('keydown', { key: 'Home' });
    await group.trigger('keydown', { key: 'Enter' });
    expect(wrapper.emitted('update:modelValue')).toEqual([['list'], ['grid'], ['list'], ['wall'], ['list']]);
    expect(document.activeElement).toBe(wrapper.findAll('[role="radio"]')[0].element);
    wrapper.unmount();
  });

  it('arrows move from the focused option, so a host that ignores the emitted value still walks a → b → c', async () => {
    const wrapper = mountControl('list');
    const radios = wrapper.findAll('[role="radio"]');
    radios[0].element.focus();
    await radios[0].trigger('keydown', { key: 'ArrowRight' });
    expect(document.activeElement).toBe(radios[1].element);
    await radios[1].trigger('keydown', { key: 'ArrowRight' });
    expect(document.activeElement).toBe(radios[2].element);
    expect(wrapper.emitted('update:modelValue')).toEqual([['grid'], ['wall']]);
    wrapper.unmount();
  });

  it('disabled options are skipped and cannot be clicked; a disabled selected option hands the tab stop to the first enabled one', async () => {
    const options = [
      { value: 'list', label: '列表', disabled: true },
      { value: 'grid', label: '网格' },
      { value: 'wall', label: '海报墙', disabled: true },
      { value: 'map', label: '地图' }
    ];
    const wrapper = mountControl('list', { options });
    const radios = wrapper.findAll('[role="radio"]');
    expect(radios.map(radio => radio.attributes('tabindex'))).toEqual(['-1', '0', '-1', '-1']);
    expect(radios[0].attributes('disabled')).toBeDefined();
    await radios[2].trigger('click');
    radios[1].element.focus();
    await radios[1].trigger('keydown', { key: 'ArrowRight' });
    await radios[3].trigger('keydown', { key: 'ArrowRight' });
    expect(wrapper.emitted('update:modelValue')).toEqual([['map'], ['grid']]);
    wrapper.unmount();
  });

  it('icon-only options take their accessible name from ariaLabel or title; a missing name warns in development', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const options = [
      { value: 'asc', icon: 'chevron-up', ariaLabel: '升序' },
      { value: 'desc', icon: 'chevron-down', title: '降序' },
      { value: 'grid', label: '网格', ariaLabel: '不该用上' },
      { value: 'bare', icon: 'filter' }
    ];
    const wrapper = mountControl('asc', { options, label: '排序方向' });
    const radios = wrapper.findAll('[role="radio"]');
    expect(radios.map(radio => radio.attributes('aria-label'))).toEqual(['升序', '降序', undefined, undefined]);
    expect(radios[1].attributes('title')).toBe('降序');
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn.mock.calls[0][0]).toContain('bare');
    warn.mockRestore();
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
