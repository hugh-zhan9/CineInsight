import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import RelatedVideoItem from './RelatedVideoItem.vue';

describe('RelatedVideoItem', () => {
  it('第二行在文件名后带大小、时长与分辨率', () => {
    const wrapper = mount(RelatedVideoItem, { props: { video: { id: 1, name: 'a.mp4', size: 512 * 1024 * 1024, duration: 65, width: 1280, height: 720 } } });
    expect(wrapper.get('.related-video-card__copy small').text()).toBe('a.mp4 · 512.0 MB · 01:05 · 1280×720');
  });

  it('没有元信息时只显示文件名', () => {
    const wrapper = mount(RelatedVideoItem, { props: { video: { id: 1, name: 'a.mp4' } } });
    expect(wrapper.get('.related-video-card__copy small').text()).toBe('a.mp4');
  });
});
