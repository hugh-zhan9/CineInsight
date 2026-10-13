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
});
