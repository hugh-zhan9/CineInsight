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

  it('messageDataTest puts a hook on the message line; the host data-test stays on the root', () => {
    const wrapper = mount(EmptyState, { props: { title: '没有候选', message: '换个阈值再试。', messageDataTest: 'cleanup-empty-detail' }, attrs: { 'data-test': 'cleanup-empty' } });
    expect(wrapper.attributes('data-test')).toBe('cleanup-empty');
    expect(wrapper.get('[data-test="cleanup-empty-detail"]').text()).toBe('换个阈值再试。');
    expect(mount(EmptyState, { props: { title: 'a', message: 'b' } }).get('.ui-empty__message').attributes('data-test')).toBeUndefined();
  });
});
