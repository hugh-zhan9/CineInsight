import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import StatTile from './StatTile.vue';
import { ICON_PATHS } from './icons.js';

describe('StatTile', () => {
  it('renders icon paths, value with unit, label, note and hint as an article by default', () => {
    const wrapper = mount(StatTile, {
      props: { icon: ['M1 1h2', 'M3 3h4'], tone: 'danger', value: 3, unit: '张', label: '待删', note: '合计 1 GB', hint: '提示' },
      attrs: { 'data-test': 'tile' }
    });
    const root = wrapper.get('[data-test="tile"]');
    expect(root.element.tagName).toBe('ARTICLE');
    expect(root.classes()).toEqual(expect.arrayContaining(['stat-tile', 'stat-tile--md']));
    expect(root.classes()).not.toContain('stat-tile--action');
    expect(wrapper.get('.stat-tile__icon').classes()).toContain('stat-tile__icon--danger');
    expect(wrapper.findAll('.stat-tile__icon path')).toHaveLength(2);
    expect(wrapper.get('[data-test="stat-tile-value"]').text()).toBe('3张');
    expect(wrapper.get('[data-test="stat-tile-label"]').text()).toBe('待删');
    expect(wrapper.get('[data-test="stat-tile-note"]').text()).toBe('合计 1 GB');
    expect(wrapper.get('.stat-tile__hint').text()).toBe('提示');
  });

  it('omits the icon box, unit, note and hint when not given', () => {
    const wrapper = mount(StatTile, { props: { value: '—', label: '可回收', size: 'sm' } });
    expect(wrapper.find('.stat-tile__icon').exists()).toBe(false);
    expect(wrapper.find('.stat-tile__value small').exists()).toBe(false);
    expect(wrapper.find('[data-test="stat-tile-note"]').exists()).toBe(false);
    expect(wrapper.find('.stat-tile__hint').exists()).toBe(false);
    expect(wrapper.classes()).toContain('stat-tile--sm');
    expect(wrapper.get('[data-test="stat-tile-value"]').text()).toBe('—');
  });

  it('acts as a clickable button with a selected state and passes attributes through', async () => {
    const wrapper = mount(StatTile, {
      props: { tag: 'button', selected: true, value: 2, label: '精确重复' },
      attrs: { role: 'tab', 'aria-selected': 'true' }
    });
    expect(wrapper.element.tagName).toBe('BUTTON');
    expect(wrapper.attributes('type')).toBe('button');
    expect(wrapper.attributes('role')).toBe('tab');
    expect(wrapper.classes()).toEqual(expect.arrayContaining(['stat-tile--action', 'stat-tile--on']));
    await wrapper.trigger('click');
    expect(wrapper.emitted('click')).toHaveLength(1);
  });

  it('accepts icon, value and extra content through slots', () => {
    const wrapper = mount(StatTile, {
      props: { size: 'lg', unit: '次', label: '播放次数' },
      slots: { icon: '<i class="custom-icon"></i>', value: '<b>42</b>', default: '<span class="delta">↑ 5%</span>' }
    });
    expect(wrapper.classes()).toContain('stat-tile--lg');
    expect(wrapper.find('.stat-tile__icon .custom-icon').exists()).toBe(true);
    expect(wrapper.get('[data-test="stat-tile-value"]').text()).toBe('42次');
    expect(wrapper.get('.delta').text()).toBe('↑ 5%');
    expect(wrapper.find('[type]').exists()).toBe(false);
  });

  it('also takes an icon name from the shared icon set (D-UD-06); unknown names draw nothing', () => {
    const named = mount(StatTile, { props: { icon: 'trash', label: '可回收' } });
    expect(named.findAll('.stat-tile__icon path').map(path => path.attributes('d'))).toEqual(ICON_PATHS.trash);
    const unknown = mount(StatTile, { props: { icon: 'no-such-icon', label: '可回收' } });
    expect(unknown.find('.stat-tile__icon').exists()).toBe(false);
  });
});
