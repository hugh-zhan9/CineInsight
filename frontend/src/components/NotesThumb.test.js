import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import NotesThumb from './NotesThumb.vue';

describe('NotesThumb', () => {
  it('shows the preview image and opens the source when clickable', async () => {
    const wrapper = mount(NotesThumb, { props: { src: '/preview/thumbnail/11', clickable: true, fallback: '原片不可用' } });
    expect(wrapper.find('[data-test="notes-thumb-image"]').attributes('src')).toBe('/preview/thumbnail/11');
    await wrapper.trigger('click');
    expect(wrapper.emitted('open')).toHaveLength(1);
  });

  it('falls back to the given label without a source and is not clickable', async () => {
    const wrapper = mount(NotesThumb, { props: { src: '', clickable: false, fallback: '片库外' } });
    expect(wrapper.find('[data-test="notes-thumb-image"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="notes-thumb-fallback"]').text()).toBe('片库外');
    expect(wrapper.attributes('disabled')).toBeDefined();
  });

  it('says 暂无预览 when an existing source fails to load, and retries for a new source', async () => {
    const wrapper = mount(NotesThumb, { props: { src: '/preview/frame/11?ms=0&w=320', clickable: true, fallback: '原片不可用' } });
    await wrapper.find('[data-test="notes-thumb-image"]').trigger('error');
    expect(wrapper.find('[data-test="notes-thumb-fallback"]').text()).toBe('暂无预览');
    await wrapper.setProps({ src: '/preview/frame/12?ms=0&w=320' });
    expect(wrapper.find('[data-test="notes-thumb-image"]').exists()).toBe(true);
  });
});
