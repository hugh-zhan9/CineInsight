import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import FilterChip from './FilterChip.vue';

describe('FilterChip', () => {
  it('toggles through a pressed-state button and keeps no state of its own', async () => {
    const wrapper = mount(FilterChip, { props: { label: '未看过' }, attrs: { 'data-test': 'chip' } });
    const toggle = wrapper.get('.ui-filter-chip__toggle');
    expect(wrapper.attributes('data-test')).toBe('chip');
    expect(toggle.attributes('type')).toBe('button');
    expect(toggle.attributes('aria-pressed')).toBe('false');
    await toggle.trigger('click');
    expect(wrapper.emitted('toggle')).toHaveLength(1);
    expect(toggle.attributes('aria-pressed')).toBe('false');

    await wrapper.setProps({ active: true });
    expect(toggle.attributes('aria-pressed')).toBe('true');
    expect(wrapper.classes()).toContain('ui-filter-chip--active');
    expect(wrapper.find('.ui-filter-chip__remove').exists()).toBe(false);
  });

  it('removable chips get a labelled × and Delete / Backspace remove them', async () => {
    const wrapper = mount(FilterChip, { props: { label: '4K', removable: true } });
    const remove = wrapper.get('.ui-filter-chip__remove');
    expect(remove.attributes('aria-label')).toBe('移除筛选：4K');
    await remove.trigger('click');
    await wrapper.get('.ui-filter-chip__toggle').trigger('keydown', { key: 'Delete' });
    await wrapper.get('.ui-filter-chip__toggle').trigger('keydown', { key: 'Backspace' });
    expect(wrapper.emitted('remove')).toHaveLength(3);
    expect(wrapper.emitted('toggle')).toBeUndefined();
  });

  it('Delete does nothing on a chip that cannot be removed', async () => {
    const wrapper = mount(FilterChip, { props: { label: '4K' } });
    await wrapper.get('.ui-filter-chip__toggle').trigger('keydown', { key: 'Delete' });
    expect(wrapper.emitted('remove')).toBeUndefined();
  });

  it('toggleDataTest lands on the clickable toggle; the host data-test stays on the chip', async () => {
    const wrapper = mount(FilterChip, { props: { label: '精确重复', toggleDataTest: 'cleanup-category' }, attrs: { 'data-test': 'chip', title: '大小 + 采样哈希一致' } });
    expect(wrapper.attributes('data-test')).toBe('chip');
    expect(wrapper.attributes('title')).toBe('大小 + 采样哈希一致');
    const toggle = wrapper.get('[data-test="cleanup-category"]');
    expect(toggle.classes()).toContain('ui-filter-chip__toggle');
    await toggle.trigger('click');
    expect(wrapper.emitted('toggle')).toHaveLength(1);
    expect(mount(FilterChip, { props: { label: 'a' } }).get('.ui-filter-chip__toggle').attributes('data-test')).toBeUndefined();
  });

  it('shows a count after the label (written in full, zero included) and lets a slot replace it', () => {
    const counted = mount(FilterChip, { props: { label: '近似重复', count: 1234 } });
    expect(counted.get('.ui-filter-chip__count').text()).toBe('1234');
    expect(counted.get('.ui-filter-chip__toggle').text()).toBe('近似重复1234');
    expect(mount(FilterChip, { props: { label: 'a', count: 0 } }).get('.ui-filter-chip__count').text()).toBe('0');
    expect(mount(FilterChip, { props: { label: 'a', count: '12+' } }).get('.ui-filter-chip__count').text()).toBe('12+');
    expect(mount(FilterChip, { props: { label: 'a' } }).find('.ui-filter-chip__count').exists()).toBe(false);
    expect(mount(FilterChip, { props: { label: 'a', count: '' } }).find('.ui-filter-chip__count').exists()).toBe(false);
    const slotted = mount(FilterChip, { props: { label: '同源视频', count: 3 }, slots: { count: '<b>3</b><i class="coverage">已算 3 / 10</i>' } });
    expect(slotted.get('.ui-filter-chip__count .coverage').text()).toBe('已算 3 / 10');
    expect(slotted.get('.ui-filter-chip__count').text()).toBe('3已算 3 / 10');
  });
});
