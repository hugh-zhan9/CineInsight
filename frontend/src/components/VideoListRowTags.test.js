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

// 用户裁决（2026-09-07）：列表行标签不能只给一行——多了被遮挡、+N 浮层也不行。
// 标签照实换行铺开，行随内容长高，虚拟列表按实测行高定位。
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

  it('没有溢出入口，也没有展开态：条带就是普通的换行流', () => {
    const wrapper = mountRow(30, 'list');
    expect(wrapper.find('[data-test="row-tag-overflow"]').exists()).toBe(false);
    const strip = wrapper.get('[data-test="row-tag-strip"]');
    expect(strip.classes()).toEqual(['video-tags__strip']);
    expect(strip.findAll('.tag-badge')).toHaveLength(30);
    wrapper.unmount();
  });

  it('"+ 标签"留在条带外面，不会被标签挤走', () => {
    const wrapper = mountRow(12, 'list');
    const strip = wrapper.find('[data-test="row-tag-strip"]');
    expect(strip.find('.btn-add-tag').exists()).toBe(false);
    expect(wrapper.find('.btn-add-tag').exists()).toBe(true);
    wrapper.unmount();
  });
});
