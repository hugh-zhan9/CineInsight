import { mount } from '@vue/test-utils';
import { expect, it } from 'vitest';
import Groups from './SearchHitGroups.vue';
const hit = { video_id: 1, segment_id: 10, source: 'dialogue', source_version: 'old-source', text: '台词', start_ms: 100, end_ms: 1000 };
it('previews precisely the loaded hits and preserves their captured source versions', async () => {
 const wrapper = mount(Groups, { props: { groups: [{ video_id: 1, title: '片名', hit_count: 2000, hits: [hit], next_cursor: 'next' }] } });
 expect(wrapper.text()).toContain('2000 处命中'); expect(wrapper.text()).toContain('已加载的 1 段');
 await wrapper.findAll('button').find(button => button.text().startsWith('连续预览')).trigger('click');
 expect(wrapper.emitted('preview')[0][0]).toEqual([hit]);
 await wrapper.findAll('button').find(button => button.text() === '更多本片命中').trigger('click');
 expect(wrapper.emitted('more-hits')[0][0].next_cursor).toBe('next'); wrapper.unmount();
});
it('retains expansion without losing data and keeps unavailable positions out of direct actions', async () => {
 const wrapper = mount(Groups, { props: { groups: [{ video_id: 1, title: '片名', hits: [{ ...hit, source_version: '' }] }] } });
 await wrapper.find('button').trigger('click'); expect(wrapper.find('button').attributes('aria-expanded')).toBe('false');
 await wrapper.setProps({ busy: true }); expect(wrapper.vm.collapsed[1]).toBe(true);
 expect(wrapper.findAll('button').find(button => button.text() === '预览').attributes('disabled')).toBeDefined(); wrapper.unmount();
});
