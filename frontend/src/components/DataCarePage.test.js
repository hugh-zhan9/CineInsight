import { describe, it, expect, afterEach } from 'vitest';
import { mount } from '@vue/test-utils';
import DataCarePage, { DATA_CARE_TABS } from './DataCarePage.vue';

const wrappers = [];
function mountPage(props = {}) {
  const wrapper = mount(DataCarePage, { props });
  wrappers.push(wrapper);
  return wrapper;
}

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
});

describe('数据维护页（D-DC-01）', () => {
  it('四个页签：失联 · 完整度 · 规则 · 体检，默认失联', () => {
    const wrapper = mountPage();
    expect(wrapper.findAll('[role="tab"]').map(tab => tab.text())).toEqual(['失联', '完整度', '规则', '体检']);
    expect(wrapper.find('[data-test="data-care-panel-lost"]').exists()).toBe(true);
  });

  it('点页签切换面板', async () => {
    const wrapper = mountPage();
    await wrapper.get('[data-test="data-care-tab-health"]').trigger('click');
    expect(wrapper.find('[data-test="data-care-panel-health"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="data-care-panel-lost"]').exists()).toBe(false);
  });

  it('命令面板带进来的页签请求：初始与 token 变化时切换，未知页签忽略', async () => {
    const wrapper = mountPage({ tabRequest: { tab: 'rules', token: 1 } });
    expect(wrapper.find('[data-test="data-care-panel-rules"]').exists()).toBe(true);
    await wrapper.setProps({ tabRequest: { tab: 'completeness', token: 2 } });
    expect(wrapper.find('[data-test="data-care-panel-completeness"]').exists()).toBe(true);
    await wrapper.setProps({ tabRequest: { tab: 'nope', token: 3 } });
    expect(wrapper.find('[data-test="data-care-panel-completeness"]').exists()).toBe(true);
    expect(DATA_CARE_TABS.map(item => item.key)).toEqual(['lost', 'completeness', 'rules', 'health']);
  });
});
