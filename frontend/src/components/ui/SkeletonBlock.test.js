import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import SkeletonBlock from './SkeletonBlock.vue';

describe('SkeletonBlock', () => {
  it('draws three lines by default and the requested number otherwise, hidden from assistive tech', () => {
    const wrapper = mount(SkeletonBlock);
    expect(wrapper.attributes('aria-hidden')).toBe('true');
    expect(wrapper.findAll('.ui-skeleton__line')).toHaveLength(3);
    expect(mount(SkeletonBlock, { props: { lines: 5 } }).findAll('.ui-skeleton__line')).toHaveLength(5);
    expect(mount(SkeletonBlock, { props: { lines: 0 } }).findAll('.ui-skeleton__line')).toHaveLength(0);
  });

  it('card and poster have media plus two lines; thumb is media only', () => {
    for (const shape of ['card', 'poster']) {
      const wrapper = mount(SkeletonBlock, { props: { shape } });
      expect(wrapper.classes()).toContain(`ui-skeleton--${shape}`);
      expect(wrapper.findAll('.ui-skeleton__media')).toHaveLength(1);
      expect(wrapper.findAll('.ui-skeleton__line')).toHaveLength(2);
    }
    const thumb = mount(SkeletonBlock, { props: { shape: 'thumb', lines: 4 } });
    expect(thumb.findAll('.ui-skeleton__media')).toHaveLength(1);
    expect(thumb.findAll('.ui-skeleton__line')).toHaveLength(0);
    expect(SkeletonBlock.props.shape.validator('circle')).toBe(false);
  });
});
