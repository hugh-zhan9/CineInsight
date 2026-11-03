import { mount } from '@vue/test-utils';
import { describe, it, expect } from 'vitest';
import PlaybackTranscript from './PlaybackTranscript.vue';
import { projectTranscript } from '../../utils/playbackSubtitles.js';

describe('synchronized dialogue', () => {
  it('renders a bounded window, follows time and lets manual browsing suspend following', async () => {
    const rows = projectTranscript(Array.from({ length: 20000 }, (_, id) => ({ start_ms: id * 1000, end_ms: id * 1000 + 900, text: `第${id}句\n译文` })));
    const wrapper = mount(PlaybackTranscript, { props: { rows, timeMS: 0 } });
    expect(wrapper.findAll('[data-cue-id]').length).toBeLessThan(20);
    await wrapper.setProps({ timeMS: 15000500 });
    expect(wrapper.get('[aria-current=true]').attributes('data-cue-id')).toBe('15000');
    const list = wrapper.get('.playback-transcript__list');
    await list.trigger('wheel');
    const previous = list.element.scrollTop;
    await wrapper.setProps({ timeMS: 19000500 });
    expect(list.element.scrollTop).toBe(previous);
    await wrapper.get('[aria-pressed]').trigger('click');
    expect(wrapper.get('[aria-current=true]').attributes('data-cue-id')).toBe('19000');
    wrapper.unmount();
  });
  it('shows plain multiline text and refuses an out-of-clip seek', async () => {
    const rows = projectTranscript([{ start_ms: 0, end_ms: 2000, text: '<b>原文</b>\n译文' }, { start_ms: 3000, end_ms: 4000, text: 'outside' }], .5);
    const wrapper = mount(PlaybackTranscript, { props: { rows, range: { startMS: 0, endMS: 3000 } } });
    expect(wrapper.find('b').exists()).toBe(false);
    expect(wrapper.get('[data-cue-id="0"] span').text()).toBe('<b>原文</b>\n译文');
    await wrapper.get('[data-cue-id="0"]').trigger('click');
    expect(wrapper.emitted('seek')).toEqual([[.5]]);
    expect(wrapper.get('[data-cue-id="1"]').element.disabled).toBe(true);
    await wrapper.get('[data-cue-id="1"]').trigger('click');
    expect(wrapper.emitted('seek')).toHaveLength(1);
    wrapper.unmount();
  });
  it('handles empty and single cue documents and closes only itself', async () => {
    const wrapper = mount(PlaybackTranscript);
    expect(wrapper.text()).toContain('当前没有可显示的台词');
    await wrapper.setProps({ rows: projectTranscript([{ start_ms: 0, end_ms: 1000, text: 'only' }]), timeMS: 500 });
    expect(wrapper.findAll('[data-cue-id]')).toHaveLength(1);
    await wrapper.trigger('keydown', { key: 'Escape' });
    expect(wrapper.emitted('close')).toHaveLength(1);
    wrapper.unmount();
  });
});
