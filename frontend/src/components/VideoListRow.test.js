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

import VideoListRow, { STALE_REASON_LABELS, staleReasonLabel } from './VideoListRow.vue';

const row = (overrides = {}, props = {}) => mount(VideoListRow, {
  props: {
    video: {
      id: 1, name: 'clip.mp4', path: '/tmp/clip.mp4', tags: [],
      duration: 28, watch_position_seconds: 0, is_watched: false, ...overrides
    },
    ...props
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
    // 手动标已看会写 watched_at，断点是在那之前留下的：不是有效断点（D-PC42）。
    const wrapper = row({ watch_position_seconds: 14, is_watched: true, watched_at: '2026-09-30T12:00:00+08:00', watch_progress_updated_at: '2026-09-30T11:00:00+08:00' });
    expect(wrapper.vm.watchProgressPercent).toBe(0);
    wrapper.unmount();
  });

  it('PLAY-10 标已看之后又在看（重看）时照常显示进度，文案写「重看到」', () => {
    const wrapper = row({ watch_position_seconds: 7, is_watched: true, watched_at: '2026-09-30T12:00:00+08:00', watch_progress_updated_at: '2026-09-30T13:00:00+08:00' });
    expect(wrapper.vm.watchProgressPercent).toBe(25);
    expect(wrapper.text()).toContain('重看到 00:07 / 00:28');
    expect(wrapper.find('.video-thumbnail__progress').exists()).toBe(true);
    wrapper.unmount();
  });

  it('PLAY-11 落进片尾区间（时长的 5%）就不再挂进度条', () => {
    const wrapper = row({ duration: 7200, watch_position_seconds: 7050 });
    expect(wrapper.vm.watchProgressPercent).toBe(0);
    wrapper.unmount();
  });
});

describe('失效原因（D-PC06）', () => {
  it('LIB-10 失效徽标写明原因，悬停给出下一步', () => {
    const wrapper = row({ is_stale: true, stale_reason: 'offline_root' });
    const badge = wrapper.get('[data-test="row-stale-reason"]');
    expect(badge.text()).toBe('路径失效 · 磁盘未连接');
    expect(badge.attributes('title')).toContain('接上磁盘后会自动恢复');
    wrapper.unmount();
  });

  it('LIB-10 历史失效行没有原因时显示「原因未记录」', () => {
    const wrapper = row({ is_stale: true, stale_reason: '' });
    expect(wrapper.get('[data-test="row-stale-reason"]').text()).toBe('路径失效 · 原因未记录');
    wrapper.unmount();
  });

  it('staleReasonLabel 覆盖后端的全部原因取值', () => {
    for (const reason of ['offline_root', 'missing_file', 'removed_root', 'outside_roots', 'play_failed', 'read_error', 'watcher_missing', 'unknown']) {
      expect(STALE_REASON_LABELS[reason]).toBeTruthy();
    }
    expect(staleReasonLabel('removed_root')).toBe('目录已移除');
    expect(staleReasonLabel('')).toBe('原因未记录');
  });
});

describe('点赞（D-PC40）', () => {
  it('PLAY-02 列表行有点赞开关，点了发 toggle-liked；窄行与网格卡里不放', async () => {
    const wrapper = row({ is_liked: true });
    const like = wrapper.get('[data-test="row-like"]');
    expect(like.attributes('aria-pressed')).toBe('true');
    expect(like.attributes('aria-label')).toBe('取消点赞');
    await like.trigger('click');
    expect(wrapper.emitted('toggle-liked')[0][0]).toEqual(expect.objectContaining({ id: 1 }));
    wrapper.unmount();

    const narrow = row({}, { narrow: true });
    expect(narrow.find('[data-test="row-like"]').exists()).toBe(false);
    narrow.unmount();
    const grid = row({}, { layoutMode: 'grid' });
    expect(grid.find('[data-test="row-like"]').exists()).toBe(false);
    grid.unmount();
  });
});

describe('自动标签的人工覆盖（D-PC36）', () => {
  it('META-13 被人工覆盖的自动标签带「手动」角标，其余标签不带', () => {
    const tags = [
      { id: 1, name: '短视频', automatic_kind: 'short_video' },
      { id: 2, name: '低清', automatic_kind: 'low_resolution' },
      { id: 3, name: '动作' }
    ];
    const wrapper = row({ tags }, { overrideKinds: ['short_video'] });
    const badges = wrapper.findAll('[data-test="row-tag-manual"]');
    expect(badges).toHaveLength(1);
    expect(badges[0].classes()).toContain('tag-manual-badge');
    expect(badges[0].element.closest('.tag-badge').textContent).toContain('短视频');
    wrapper.unmount();
  });
});
