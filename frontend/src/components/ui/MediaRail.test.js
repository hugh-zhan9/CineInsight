import { mount } from '@vue/test-utils';
import { h, Fragment } from 'vue';
import { describe, it, expect, vi } from 'vitest';
import MediaRail from './MediaRail.vue';

describe('MediaRail groups', () => {
  it('reaches every item, stops at both ends, and fits on resize', async () => {
    let resize;
    const disconnect = vi.fn();
    vi.stubGlobal('ResizeObserver', class { constructor(callback) { resize = callback; } observe() {} disconnect = disconnect; });
    const wrapper = mount(MediaRail, { slots: { default: () => h(Fragment, {}, Array.from({ length: 9 }, (_, n) => h('span', { key: n }, `item${n}`))) } });
    expect(wrapper.get('[aria-label="上一组"]').attributes('disabled')).toBeDefined();
    expect(wrapper.findAll('span')).toHaveLength(6);
    await wrapper.get('[aria-label="下一组"]').trigger('click');
    expect(wrapper.findAll('span').map(x => x.text())).toEqual(['item6', 'item7', 'item8']);
    expect(wrapper.get('[aria-label="下一组"]').attributes('disabled')).toBeDefined();
    resize([{ contentRect: { width: 1500, height: 240 } }]);
    await wrapper.vm.$nextTick();
    expect(wrapper.findAll('span').map(x => x.text())).toEqual(['item6', 'item7', 'item8']);
    resize([{ contentRect: { width: 400 } }]);
    await wrapper.vm.$nextTick();
    expect(wrapper.findAll('span').map(x => x.text())).toEqual(['item0']);
    wrapper.unmount();
    expect(disconnect).toHaveBeenCalled();
    vi.unstubAllGlobals();
  });
  it('empty and single rows have no paging controls', () => {
    for (const children of [[], [h('span', 'only')]]) {
      const wrapper = mount(MediaRail, { slots: { default: () => children } });
      expect(wrapper.find('button').exists()).toBe(false);
      wrapper.unmount();
    }
  });
});
