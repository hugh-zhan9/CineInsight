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
    // 宿主照常接住 update:modelValue（v-model）。
    const wrapper = mountTabs({ 'onUpdate:modelValue': value => wrapper.setProps({ modelValue: value }) });
    const press = async key => {
      await wrapper.get('[role="tablist"]').trigger('keydown', { key });
      return document.activeElement?.dataset.tab;
    };
    expect(await press('ArrowRight')).toBe('health');
    expect(await press('ArrowRight')).toBe('lost');
    expect(await press('ArrowLeft')).toBe('health');
    expect(await press('ArrowLeft')).toBe('complete');
    expect(await press('End')).toBe('health');
    expect(await press('Home')).toBe('lost');
    expect(await press('ArrowDown')).toBe('lost');
    expect(wrapper.emitted('update:modelValue')).toEqual([['health'], ['lost'], ['health'], ['complete'], ['health'], ['lost']]);
    expect(wrapper.props('modelValue')).toBe('lost');
    wrapper.unmount();
  });

  it('arrows move from the focused tab, so a host that ignores the emitted value still walks a → b → c', async () => {
    const wrapper = mount(AppTabs, {
      props: { tabs: [{ key: 'a', label: 'A' }, { key: 'b', label: 'B' }, { key: 'c', label: 'C' }], modelValue: 'a' },
      attachTo: document.body
    });
    const tabs = wrapper.findAll('[role="tab"]');
    tabs[0].element.focus();
    await tabs[0].trigger('keydown', { key: 'ArrowRight' });
    expect(document.activeElement).toBe(tabs[1].element);
    await tabs[1].trigger('keydown', { key: 'ArrowRight' });
    expect(document.activeElement).toBe(tabs[2].element);
    expect(wrapper.emitted('update:modelValue')).toEqual([['b'], ['c']]);
    expect(tabs.map(tab => tab.attributes('aria-selected'))).toEqual(['true', 'false', 'false']);
    wrapper.unmount();
  });

  it('a disabled selected tab hands the tab stop to the first enabled one; arrows go on from there', async () => {
    const wrapper = mountTabs({ modelValue: 'rules' });
    const tabs = wrapper.findAll('[role="tab"]');
    expect(tabs.map(tab => tab.attributes('tabindex'))).toEqual(['0', '-1', '-1', '-1']);
    tabs[0].element.focus();
    await tabs[0].trigger('keydown', { key: 'ArrowRight' });
    await tabs[1].trigger('keydown', { key: 'ArrowRight' });
    expect(wrapper.emitted('update:modelValue')).toEqual([['complete'], ['health']]);
    expect(document.activeElement).toBe(tabs[3].element);
    wrapper.unmount();
  });

  it('← on the first tab wraps to the last enabled one', async () => {
    const wrapper = mountTabs({ modelValue: 'lost' });
    await wrapper.get('[role="tablist"]').trigger('keydown', { key: 'ArrowLeft' });
    expect(wrapper.emitted('update:modelValue')).toEqual([['health']]);
    wrapper.unmount();
  });

  it('each tab carries its own dataTest as data-test; tabs without one get none', () => {
    const wrapper = mountTabs({
      tabs: [{ key: 'video', label: '视频', dataTest: 'trash-tab-video' }, { key: 'image', label: '图片', dataTest: 'trash-tab-image' }, { key: 'hidden', label: '扫描隐藏' }],
      modelValue: 'video'
    });
    const tabs = wrapper.findAll('[role="tab"]');
    expect(tabs.map(tab => tab.attributes('data-test'))).toEqual(['trash-tab-video', 'trash-tab-image', undefined]);
    expect(wrapper.attributes('data-test')).toBe('care-tabs');
    wrapper.unmount();
  });

  it('a string count is shown as given (no 99+ cap), an empty string shows no count', () => {
    const wrapper = mountTabs({
      tabs: [{ key: 'pending', label: '待处理', count: '21+' }, { key: 'tags', label: 'AI 标签待审', count: '137' }, { key: 'named', label: '已命名', count: '' }],
      modelValue: 'pending'
    });
    const tabs = wrapper.findAll('[role="tab"]');
    expect(tabs[0].get('.ui-tabs__count').text()).toBe('21+');
    expect(tabs[1].get('.ui-tabs__count').text()).toBe('137');
    expect(tabs[2].find('.ui-tabs__count').exists()).toBe(false);
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
