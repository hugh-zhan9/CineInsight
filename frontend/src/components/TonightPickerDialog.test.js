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
  it('sends the entire library filter and the current budget/preferences', async () => {
    const filter = { keyword: '海边', tag_ids: [3, 5], person_ids: [7], smart_view: 'watched', min_rating: 7 };
    const wrapper = mount(TonightPickerDialog, { props: { filter } });
    await flushPromises();
    expect(api.SuggestTonightVideos).toHaveBeenLastCalledWith({ filter, max_duration_seconds: 5400, unwatched_only: false, favorites_only: false, limit: 6 });
    expect(wrapper.text()).toContain('没有符合全部条件');
    await wrapper.get('[data-test="tonight-minutes"]').setValue(30);
    await wrapper.get('[data-test="tonight-favorites"]').setValue(true);
    await wrapper.get('form').trigger('submit');
    expect(api.SuggestTonightVideos).toHaveBeenLastCalledWith(expect.objectContaining({ max_duration_seconds: 1800, favorites_only: true }));
  });

  it('clears previous candidates on input changes and ignores the previous response', async () => {
    let first;
    api.SuggestTonightVideos.mockImplementationOnce(() => new Promise(resolve => { first = resolve; }));
    const wrapper = mount(TonightPickerDialog, { props: { filter: {} } });
    await wrapper.get('[data-test="tonight-minutes"]').setValue(20);
    api.SuggestTonightVideos.mockResolvedValueOnce([suggestion(2)]);
    await wrapper.get('form').trigger('submit');
    await flushPromises();
    first([suggestion(1)]); await flushPromises();
    expect(wrapper.text()).toContain('video 2'); expect(wrapper.text()).not.toContain('video 1');
    await wrapper.get('[data-test="tonight-unwatched"]').setValue(false);
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
  });

  it('uses explicit preview/play actions without launching when results arrive', async () => {
    api.SuggestTonightVideos.mockResolvedValue([suggestion(3)]);
    const wrapper = mount(TonightPickerDialog, { props: { filter: {} } });
    await flushPromises();
    expect(wrapper.emitted('play')).toBeUndefined();
    const card = wrapper.getComponent(RelatedVideoItem);
    card.vm.$emit('open'); card.vm.$emit('action');
    expect(wrapper.emitted('preview')[0]).toEqual([suggestion(3).video]);
    expect(wrapper.emitted('play')[0]).toEqual([suggestion(3).video]);
  });
});
