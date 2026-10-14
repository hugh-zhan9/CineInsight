import { describe, it, expect, afterEach, vi } from 'vitest';
import { mount } from '@vue/test-utils';

// 页签面板挂载时会调后端：这里一律给空结果，只测页面骨架本身。
const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../wailsjs/go/main/App', () => api);

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

  // 面板组件的根节点带 role=tabpanel、id 与 aria-labelledby，和页签的 id / aria-controls 互相指向；← → 切页签时跟着换。
  it('当前页签与面板互相指向，← → 切换后面板跟着换', async () => {
    const wrapper = mountPage();
    for (const [key, next] of [['lost', 'ArrowRight'], ['completeness', 'ArrowRight'], ['rules', 'ArrowLeft'], ['completeness', null]]) {
      const panel = wrapper.get('[role="tabpanel"]');
      expect(panel.attributes('id')).toBe(`data-care-panel-${key}`);
      expect(panel.attributes('aria-labelledby')).toBe(`data-care-tab-${key}`);
      expect(panel.attributes('data-test')).toBe(`data-care-panel-${key}`);
      expect(wrapper.get(`#data-care-tab-${key}`).attributes('aria-controls')).toBe(`data-care-panel-${key}`);
      expect(wrapper.get(`#data-care-tab-${key}`).attributes('aria-selected')).toBe('true');
      if (next) await wrapper.get('[role="tablist"]').trigger('keydown', { key: next });
    }
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
