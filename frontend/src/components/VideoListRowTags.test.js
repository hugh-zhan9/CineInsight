import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import VideoListRow from './VideoListRow.vue';

function makeVideo(tagCount) {
  return {
    id: 1,
    name: '2 (16).mp4',
    path: '/v/2 (16).mp4',
    size: 234 * 1024 * 1024,
    resolution: '720x1280',
    tags: Array.from({ length: tagCount }, (_, index) => ({ id: index + 1, name: `标签${index + 1}` }))
  };
}

function mountRow(tagCount, layoutMode) {
  return mount(VideoListRow, { props: { video: makeVideo(tagCount), layoutMode } });
}

describe('行内标签区', () => {
  it('标签再多也全部渲染，不折叠不省略', () => {
    for (const layoutMode of ['grid', 'list']) {
      const wrapper = mountRow(12, layoutMode);
      const names = wrapper.findAll('.tag-badge__name').map(node => node.text());
      expect(names).toHaveLength(12);
      expect(names[11]).toBe('标签12');
      wrapper.unmount();
    }
  });

  // 条带横向溢出时既没有滚动条也没有提示，用户看到的就是"标签没显示全"。
  // 溢出数由几何测量得出（jsdom 里宽度都是 0，所以这里直接给状态）。
  it('溢出时给 +N 入口，点开就地展开成换行浮层', async () => {
    const wrapper = mountRow(12, 'list');
    expect(wrapper.find('[data-test="row-tag-overflow"]').exists()).toBe(false);

    await wrapper.setData({ hiddenTagCount: 7 });
    const more = wrapper.find('[data-test="row-tag-overflow"]');
    expect(more.text()).toBe('+7');
    expect(more.attributes('title')).toContain('还有 7 个标签没显示');

    await more.trigger('click');
    const strip = wrapper.find('[data-test="row-tag-strip"]');
    expect(strip.classes()).toContain('video-tags__strip--expanded');
    expect(strip.findAll('.tag-badge')).toHaveLength(12);
    expect(wrapper.find('[data-test="row-tag-overflow"]').text()).toBe('收起');

    await wrapper.find('[data-test="row-tag-overflow"]').trigger('click');
    expect(wrapper.find('[data-test="row-tag-strip"]').classes()).not.toContain('video-tags__strip--expanded');

    wrapper.unmount();
  });

  // 虚拟列表复用行组件：换了视频还挂着展开浮层，等于把上一个视频的标签盖在这一行上。
  it('行被复用到另一个视频时收起展开态', async () => {
    const wrapper = mountRow(12, 'list');
    await wrapper.setData({ hiddenTagCount: 3 });
    await wrapper.find('[data-test="row-tag-overflow"]').trigger('click');
    expect(wrapper.find('[data-test="row-tag-strip"]').classes()).toContain('video-tags__strip--expanded');

    await wrapper.setProps({ video: { ...makeVideo(2), id: 99 } });
    expect(wrapper.find('[data-test="row-tag-strip"]').classes()).not.toContain('video-tags__strip--expanded');

    wrapper.unmount();
  });

  it('标签放在独立的滚动条带里，"+ 标签"留在外面不会被挤走', () => {
    const wrapper = mountRow(12, 'list');

    const strip = wrapper.find('[data-test="row-tag-strip"]');
    expect(strip.findAll('.tag-badge')).toHaveLength(12);
    expect(strip.find('.btn-add-tag').exists()).toBe(false);
    expect(wrapper.find('.btn-add-tag').exists()).toBe(true);

    wrapper.unmount();
  });
});
