// 2026-09-13 裁决：已看的视频不该还挂着「看到 X / Y」当成在看。
import { mount } from '@vue/test-utils';
import { describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../wailsjs/go/main/App', () => api);

import VideoListRow from './VideoListRow.vue';

const row = (overrides = {}) => mount(VideoListRow, {
  props: {
    video: {
      id: 1, name: 'clip.mp4', path: '/tmp/clip.mp4', tags: [],
      duration: 28, watch_position_seconds: 0, is_watched: false, ...overrides
    }
  }
});

describe('行内观看进度', () => {
  it('看到一半时显示进度', () => {
    const wrapper = row({ watch_position_seconds: 14 });
    expect(wrapper.vm.watchProgressPercent).toBe(50);
    expect(wrapper.text()).toContain('看到');
    wrapper.unmount();
  });

  it('已看的不再显示进度条和「看到 X / Y」', () => {
    // 播完会把断点清零，手动标已看的断点还在，两种都不该显示成「在看」。
    const wrapper = row({ watch_position_seconds: 28, is_watched: true });
    expect(wrapper.vm.watchProgressPercent).toBe(0);
    expect(wrapper.text()).not.toContain('看到');
    expect(wrapper.find('.video-thumbnail__progress').exists()).toBe(false);
    wrapper.unmount();
  });

  it('已看但断点还在的（手动标记）同样不显示', () => {
    const wrapper = row({ watch_position_seconds: 14, is_watched: true });
    expect(wrapper.vm.watchProgressPercent).toBe(0);
    wrapper.unmount();
  });
});
