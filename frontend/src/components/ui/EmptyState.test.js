import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import EmptyState from './EmptyState.vue';

describe('EmptyState', () => {
  it('shows title, message, icon and the next-step actions', () => {
    const wrapper = mount(EmptyState, {
      props: { title: '还没有视频', message: '先添加扫描目录。', icon: 'folder' },
      attrs: { 'data-test': 'library-empty' },
      slots: { actions: '<button type="button" class="btn btn--primary">添加扫描目录</button>' }
    });
    expect(wrapper.attributes('data-test')).toBe('library-empty');
    expect(wrapper.get('.ui-empty__title').text()).toBe('还没有视频');
    expect(wrapper.get('.ui-empty__message').text()).toBe('先添加扫描目录。');
    expect(wrapper.get('svg').attributes('data-icon')).toBe('folder');
    expect(wrapper.get('.ui-empty__actions button').text()).toBe('添加扫描目录');
  });

  it('leaves out the optional parts when they are not given', () => {
    const wrapper = mount(EmptyState, { props: { title: '没有结果' } });
    expect(wrapper.find('.ui-empty__message').exists()).toBe(false);
    expect(wrapper.find('svg').exists()).toBe(false);
    expect(wrapper.find('.ui-empty__actions').exists()).toBe(false);
  });
});
