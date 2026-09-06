import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import TaskFailureList from './TaskFailureList.vue';

// 后端每轮最多带 50 条失败、每条最长 500 字符：平铺会把状态条撑成一屏。
// 默认只露前几条、单行截断、其余折进"展开全部"。
const failures = (count) => Array.from({ length: count }, (_, index) => ({
  video_id: index + 1, name: `clip-${index + 1}.mkv`, error: `ffprobe failed ${'x'.repeat(400)}`,
}));

describe('TaskFailureList', () => {
  it('renders nothing for an empty list', () => {
    const wrapper = mount(TaskFailureList, { props: { failures: [] } });
    expect(wrapper.find('.task-failure-list').exists()).toBe(false);
  });

  it('shows every item without a toggle when the list fits the limit', () => {
    const wrapper = mount(TaskFailureList, { props: { failures: failures(3), limit: 3 } });
    expect(wrapper.findAll('li')).toHaveLength(3);
    expect(wrapper.find('[data-test="task-failure-toggle"]').exists()).toBe(false);
    expect(wrapper.text()).toContain('clip-1.mkv：ffprobe failed');
    // 全文放在 title 里，悬停可读；行内靠 CSS 单行截断。
    expect(wrapper.findAll('li')[0].attributes('title')).toContain('clip-1.mkv：ffprobe failed');
  });

  it('collapses beyond the limit and expands on demand', async () => {
    const wrapper = mount(TaskFailureList, { props: { failures: failures(7), limit: 3 } });
    expect(wrapper.findAll('li')).toHaveLength(3);
    const toggle = wrapper.get('[data-test="task-failure-toggle"]');
    expect(toggle.text()).toBe('展开全部 7 条');

    await toggle.trigger('click');
    expect(wrapper.findAll('li')).toHaveLength(7);
    expect(wrapper.get('[data-test="task-failure-toggle"]').text()).toBe('收起');
  });

  it('stays expanded while a running task appends failures, and resets when a new round starts over', async () => {
    const wrapper = mount(TaskFailureList, { props: { failures: failures(4), limit: 3 } });
    await wrapper.get('[data-test="task-failure-toggle"]').trigger('click');
    expect(wrapper.findAll('li')).toHaveLength(4);

    // 每个进度事件都是一份新数组：内容只增不减，展开状态要保住。
    await wrapper.setProps({ failures: failures(6) });
    expect(wrapper.findAll('li')).toHaveLength(6);

    // 新一轮从空开始，再长回来时应回到折叠态。
    await wrapper.setProps({ failures: [] });
    await wrapper.setProps({ failures: failures(5) });
    expect(wrapper.findAll('li')).toHaveLength(3);
    expect(wrapper.get('[data-test="task-failure-toggle"]').text()).toBe('展开全部 5 条');
  });

  it('treats a null list from the backend as empty', () => {
    const wrapper = mount(TaskFailureList, { props: { failures: null } });
    expect(wrapper.find('.task-failure-list').exists()).toBe(false);
  });

  it('falls back to an id label when the failure has no name', () => {
    const wrapper = mount(TaskFailureList, { props: { failures: [{ media_id: 12, error: 'boom' }], fallbackLabel: '图片' } });
    expect(wrapper.text()).toBe('图片 #12：boom');
  });
});
