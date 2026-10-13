import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import AppTabs from './AppTabs.vue';

const TABS = [
  { key: 'lost', label: '失联', count: 12 },
  { key: 'complete', label: '完整度' },
  { key: 'rules', label: '规则', disabled: true },
  { key: 'health', label: '体检', count: 140 }
];

function mountTabs(props = {}) {
  return mount(AppTabs, {
    props: { tabs: TABS, modelValue: 'complete', ...props },
    attrs: { 'data-test': 'care-tabs' },
    attachTo: document.body
  });
}

describe('AppTabs', () => {
  it('renders a tablist with aria-selected and a roving tab stop', () => {
    const wrapper = mountTabs();
    expect(wrapper.attributes('role')).toBe('tablist');
    expect(wrapper.attributes('data-test')).toBe('care-tabs');
    const tabs = wrapper.findAll('[role="tab"]');
    expect(tabs.map(tab => tab.attributes('aria-selected'))).toEqual(['false', 'true', 'false', 'false']);
    expect(tabs.map(tab => tab.attributes('tabindex'))).toEqual(['-1', '0', '-1', '-1']);
    expect(tabs[2].attributes('disabled')).toBeDefined();
    expect(tabs[0].get('.ui-tabs__count').text()).toBe('12');
    expect(tabs[3].get('.ui-tabs__count').text()).toBe('99+');
    expect(tabs[1].find('.ui-tabs__count').exists()).toBe(false);
    expect(tabs[0].attributes('id')).toBeUndefined();
    wrapper.unmount();
  });

  it('click switches tabs but not to a disabled or the current one', async () => {
    const wrapper = mountTabs();
    const tabs = wrapper.findAll('[role="tab"]');
    await tabs[1].trigger('click');
    await tabs[2].trigger('click');
    await tabs[3].trigger('click');
    expect(wrapper.emitted('update:modelValue')).toEqual([['health']]);
    wrapper.unmount();
  });

  it('← / → skip disabled tabs and wrap; Home / End go to the first / last enabled tab', async () => {
    const wrapper = mountTabs();
    const list = wrapper.get('[role="tablist"]');
    await list.trigger('keydown', { key: 'ArrowRight' });
    await wrapper.setProps({ modelValue: 'health' });
    await list.trigger('keydown', { key: 'ArrowRight' });
    await list.trigger('keydown', { key: 'ArrowLeft' });
    await list.trigger('keydown', { key: 'Home' });
    await list.trigger('keydown', { key: 'End' });
    await list.trigger('keydown', { key: 'ArrowDown' });
    expect(wrapper.emitted('update:modelValue')).toEqual([['health'], ['lost'], ['complete'], ['lost']]);
    await wrapper.vm.$nextTick();
    expect(document.activeElement).toBe(wrapper.findAll('[role="tab"]')[3].element);
    wrapper.unmount();
  });

  it('← on the first tab wraps to the last enabled one', async () => {
    const wrapper = mountTabs({ modelValue: 'lost' });
    await wrapper.get('[role="tablist"]').trigger('keydown', { key: 'ArrowLeft' });
    expect(wrapper.emitted('update:modelValue')).toEqual([['health']]);
    wrapper.unmount();
  });

  it('idPrefix wires each tab to its panel', () => {
    const wrapper = mountTabs({ idPrefix: 'care' });
    const first = wrapper.findAll('[role="tab"]')[0];
    expect(first.attributes('id')).toBe('care-tab-lost');
    expect(first.attributes('aria-controls')).toBe('care-panel-lost');
    wrapper.unmount();
  });
});
