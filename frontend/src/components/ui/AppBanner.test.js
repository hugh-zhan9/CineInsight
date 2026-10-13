import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import AppBanner, { BANNER_TONES } from './AppBanner.vue';

describe('AppBanner', () => {
  it('renders a persistent status with title, message and actions', () => {
    const wrapper = mount(AppBanner, {
      props: { tone: 'warning', title: '外置硬盘已离线', message: '重新接上后会自动恢复。' },
      attrs: { 'data-test': 'offline-banner' },
      slots: { actions: '<button type="button" class="btn btn--secondary btn--sm">刷新</button>' }
    });
    expect(wrapper.attributes('role')).toBe('status');
    expect(wrapper.attributes('data-test')).toBe('offline-banner');
    expect(wrapper.classes()).toContain('ui-banner--warning');
    expect(wrapper.get('.ui-banner__title').text()).toBe('外置硬盘已离线');
    expect(wrapper.get('.ui-banner__message').text()).toBe('重新接上后会自动恢复。');
    expect(wrapper.get('.ui-banner__actions button').text()).toBe('刷新');
    expect(wrapper.get('svg').attributes('data-icon')).toBe('warning');
  });

  it('picks the icon from the tone and accepts only the five tones', () => {
    expect(mount(AppBanner, { props: { title: 'a', tone: 'success' } }).get('svg').attributes('data-icon')).toBe('check');
    expect(mount(AppBanner, { props: { title: 'a' } }).get('svg').attributes('data-icon')).toBe('info');
    expect(BANNER_TONES).toEqual(['neutral', 'accent', 'warning', 'danger', 'success']);
    expect(AppBanner.props.tone.validator('info')).toBe(false);
    expect(mount(AppBanner, { props: { title: 'a' } }).find('.ui-banner__message').exists()).toBe(false);
  });

  it('a title slot replaces the plain title and the default slot adds lines under the message', () => {
    const wrapper = mount(AppBanner, {
      props: { tone: 'accent', message: '重启后生效。' },
      slots: {
        title: '字幕索引 <b class="count">3</b> 条待重建',
        default: '<p class="extra">恢复备份后应用会自动退出。</p><p class="extra">再打开即用恢复的数据。</p>'
      }
    });
    expect(wrapper.get('.ui-banner__title').text()).toBe('字幕索引 3 条待重建');
    expect(wrapper.get('.ui-banner__title .count').text()).toBe('3');
    const parts = wrapper.findAll('.ui-banner__text > *');
    expect(parts.map(part => part.classes()[0])).toEqual(['ui-banner__title', 'ui-banner__message', 'ui-banner__extra']);
    expect(wrapper.findAll('.ui-banner__extra .extra').map(line => line.text())).toEqual(['恢复备份后应用会自动退出。', '再打开即用恢复的数据。']);
    expect(mount(AppBanner, { props: { title: 'a' } }).findAll('.ui-banner__text > *')).toHaveLength(1);
  });
});
