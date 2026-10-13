import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import AppBadge, { badgeCountText } from './AppBadge.vue';

describe('AppBadge', () => {
  it('shows a count (capped at 99+) when given one, otherwise the slot text', () => {
    const counted = mount(AppBadge, { props: { tone: 'accent', count: 3 }, attrs: { 'data-test': 'badge' } });
    expect(counted.text()).toBe('3');
    expect(counted.classes()).toEqual(expect.arrayContaining(['ui-badge', 'ui-badge--accent', 'ui-badge--count']));
    expect(counted.attributes('data-test')).toBe('badge');
    expect(mount(AppBadge, { props: { count: 120 } }).text()).toBe('99+');
    expect(mount(AppBadge, { props: { count: 0 } }).text()).toBe('0');

    const labelled = mount(AppBadge, { props: { tone: 'warning' }, slots: { default: '待确认' } });
    expect(labelled.text()).toBe('待确认');
    expect(labelled.classes()).not.toContain('ui-badge--count');
  });

  it('is not interactive and only accepts the five tones', () => {
    const wrapper = mount(AppBadge, { props: { count: 1 } });
    expect(wrapper.element.tagName).toBe('SPAN');
    expect(wrapper.find('button').exists()).toBe(false);
    expect(AppBadge.props.tone.validator('danger')).toBe(true);
    expect(AppBadge.props.tone.validator('primary')).toBe(false);
  });

  it('badgeCountText matches the header badge rule', () => {
    expect(badgeCountText(99)).toBe('99');
    expect(badgeCountText(100)).toBe('99+');
    expect(badgeCountText(-2)).toBe('0');
    expect(badgeCountText('7')).toBe('7');
  });
});
