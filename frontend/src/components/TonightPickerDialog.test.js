import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
const api = vi.hoisted(() => ({ SuggestTonightVideos: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
import TonightPickerDialog from './TonightPickerDialog.vue';
import RelatedVideoItem from './RelatedVideoItem.vue';
enableAutoUnmount(afterEach);
const suggestion = id => ({ video: { id, name: `video ${id}`, duration: 60 }, reasons: ['符合本次时间预算'] });
beforeEach(() => { api.SuggestTonightVideos.mockReset().mockResolvedValue([]); });

describe('TonightPickerDialog', () => {
  it('sends the entire library filter, the shared random conditions and recent exclusions', async () => {
    const filter = { keyword: '海边', tag_ids: [3, 5], person_ids: [7], smart_view: 'watched', min_rating: 7 };
    const wrapper = mount(TonightPickerDialog, { props: { filter, conditions: { watchState: 'watched', favoritesOnly: false, maxMinutes: 90 }, excludeIds: [4, 9] } });
    await flushPromises();
    expect(api.SuggestTonightVideos).toHaveBeenLastCalledWith(
      { filter, watch_state: 'watched', favorites_only: false, max_duration_seconds: 5400, exclude_ids: [4, 9] }, 6);
    expect(wrapper.text()).toContain('没有符合全部条件');
    await wrapper.get('[data-test="tonight-minutes"]').setValue(30);
    await wrapper.get('[data-test="tonight-favorites"]').setValue(true);
    await wrapper.get('form').trigger('submit');
    expect(api.SuggestTonightVideos).toHaveBeenLastCalledWith(expect.objectContaining({ max_duration_seconds: 1800, favorites_only: true }), 6);
  });

  it('starts from the defaults when no conditions are given', async () => {
    mount(TonightPickerDialog, { props: { filter: {} } });
    await flushPromises();
    expect(api.SuggestTonightVideos).toHaveBeenLastCalledWith(
      { filter: {}, watch_state: 'any', favorites_only: false, max_duration_seconds: 0, exclude_ids: [] }, 6);
  });

  it('clears previous candidates on input changes and ignores the previous response', async () => {
    let first;
    api.SuggestTonightVideos.mockImplementationOnce(() => new Promise(resolve => { first = resolve; }));
    const wrapper = mount(TonightPickerDialog, { props: { filter: {} } });
    await wrapper.get('[data-test="tonight-minutes"]').setValue(20);
    api.SuggestTonightVideos.mockResolvedValueOnce([suggestion(2)]);
    await wrapper.vm.load();
    await flushPromises();
    first([suggestion(1)]); await flushPromises();
    expect(wrapper.text()).toContain('video 2'); expect(wrapper.text()).not.toContain('video 1');
    await wrapper.get('[data-test="tonight-watch-state"]').setValue('watched');
    expect(wrapper.findAll('[data-test="tonight-result"]')).toHaveLength(0);
    expect(wrapper.find('[data-test="tonight-empty"]').exists()).toBe(false);
  });

  it('distinguishes read failures from empty results and rejects invalid minutes', async () => {
    api.SuggestTonightVideos.mockRejectedValueOnce(new Error('unavailable'));
    const wrapper = mount(TonightPickerDialog, { props: { filter: {} } });
    await flushPromises();
    expect(wrapper.get('[data-test="tonight-error"]').text()).toContain('unavailable');
    expect(wrapper.find('[data-test="tonight-empty"]').exists()).toBe(false);
    await wrapper.get('[data-test="tonight-minutes"]').setValue(-1);
    await wrapper.vm.load();
    expect(api.SuggestTonightVideos).toHaveBeenCalledTimes(1);
    expect(wrapper.text()).toContain('整数分钟');
    // 非法分钟数不回写共用条件，免得随机播放主键拿到坏值。
    expect(wrapper.emitted('update:conditions')).toBeUndefined();
  });

  it('uses explicit preview/play actions without launching when results arrive', async () => {
    api.SuggestTonightVideos.mockResolvedValue([suggestion(3)]);
    const wrapper = mount(TonightPickerDialog, { props: { filter: {} } });
    await flushPromises();
    expect(wrapper.emitted('play')).toBeUndefined();
    expect(wrapper.emitted('play-random')).toBeUndefined();
    const card = wrapper.getComponent(RelatedVideoItem);
    card.vm.$emit('open'); card.vm.$emit('action');
    expect(wrapper.emitted('preview')[0]).toEqual([suggestion(3).video]);
    expect(wrapper.emitted('play')[0]).toEqual([suggestion(3).video]);
  });

  it('「随机播放一部」只交给片库页的随机播放，不自己抽', async () => {
    const wrapper = mount(TonightPickerDialog, { props: { filter: {} } });
    await flushPromises();
    await wrapper.get('[data-test="tonight-play-random"]').trigger('click');
    expect(wrapper.emitted('play-random')).toHaveLength(1);
    expect(api.SuggestTonightVideos).toHaveBeenCalledTimes(1);
  });
});

describe('TonightPickerDialog 换一批', () => {
  it('excludes the current batch on top of the recent random exclusions', async () => {
    api.SuggestTonightVideos.mockResolvedValueOnce([suggestion(1), suggestion(2)]).mockResolvedValueOnce([suggestion(3)]);
    const wrapper = mount(TonightPickerDialog, { props: { filter: {}, excludeIds: [9, 1] } });
    await flushPromises();
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    expect(api.SuggestTonightVideos).toHaveBeenLastCalledWith(expect.objectContaining({ exclude_ids: [9, 1, 2] }), 6);
    expect(wrapper.findAll('[data-test="tonight-result"]').map(item => item.text())).toEqual([expect.stringContaining('video 3')]);
  });

  it('keeps the current batch with a notice when nothing else qualifies', async () => {
    api.SuggestTonightVideos.mockResolvedValueOnce([suggestion(1), suggestion(2)]).mockResolvedValueOnce([]);
    const wrapper = mount(TonightPickerDialog, { props: { filter: {} } });
    await flushPromises();
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    expect(wrapper.findAll('[data-test="tonight-result"]')).toHaveLength(2);
    expect(wrapper.get('[data-test="tonight-notice"]').text()).toContain('没有更多可换的');
    expect(wrapper.find('[data-test="tonight-empty"]').exists()).toBe(false);
  });
});

describe('TonightPickerDialog tags', () => {
  it('shows each candidate tags with the tag colour', async () => {
    api.SuggestTonightVideos.mockResolvedValue([{
      video: { id: 7, name: 'tagged.mp4', tags: [{ id: 1, name: '科幻', color: '#3366ff' }, { id: 2, name: '收藏夹', color: '' }] },
      reasons: ['未看']
    }, { video: { id: 8, name: 'plain.mp4', tags: [] }, reasons: ['未看'] }]);
    const wrapper = mount(TonightPickerDialog, { props: { filter: {} } });
    await flushPromises();
    const groups = wrapper.findAll('[data-test="tonight-tags"]');
    expect(groups).toHaveLength(1);
    const badges = groups[0].findAll('.tag-badge');
    expect(badges.map(badge => badge.text())).toEqual(['科幻', '收藏夹']);
    expect(badges[0].attributes('style')).toContain('rgba(51, 102, 255, 0.35)');
  });
});

describe('TonightPickerDialog live filters', () => {
  it('re-queries automatically when conditions change, labelled 探索/不限/重温, and writes them back', async () => {
    vi.useFakeTimers();
    try {
      const wrapper = mount(TonightPickerDialog, { props: { filter: {} } });
      await flushPromises();
      const options = wrapper.findAll('[data-test="tonight-watch-state"] option').map(option => option.text());
      expect(options).toEqual(['探索', '不限', '重温']);
      const calls = api.SuggestTonightVideos.mock.calls.length;
      await wrapper.get('[data-test="tonight-watch-state"]').setValue('watched');
      await vi.advanceTimersByTimeAsync(350);
      expect(api.SuggestTonightVideos.mock.calls.length).toBe(calls + 1);
      expect(api.SuggestTonightVideos).toHaveBeenLastCalledWith(expect.objectContaining({ watch_state: 'watched' }), 6);
      await wrapper.get('[data-test="tonight-favorites"]').setValue(true);
      await vi.advanceTimersByTimeAsync(350);
      expect(api.SuggestTonightVideos).toHaveBeenLastCalledWith(expect.objectContaining({ favorites_only: true }), 6);
      await wrapper.get('[data-test="tonight-minutes"]').setValue(3);
      await wrapper.get('[data-test="tonight-minutes"]').setValue(30);
      await vi.advanceTimersByTimeAsync(350);
      expect(api.SuggestTonightVideos).toHaveBeenLastCalledWith(expect.objectContaining({ max_duration_seconds: 1800 }), 6);
      expect(api.SuggestTonightVideos.mock.calls.length).toBe(calls + 3);
      expect(wrapper.emitted('update:conditions').at(-1)).toEqual([{ watchState: 'watched', favoritesOnly: true, maxMinutes: 30 }]);
    } finally {
      vi.useRealTimers();
    }
  });
});
