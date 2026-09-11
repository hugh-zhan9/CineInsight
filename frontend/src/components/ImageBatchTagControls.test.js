import { mount, flushPromises } from '@vue/test-utils';
import { describe, it, expect, vi } from 'vitest';
const api = vi.hoisted(() => ({ GetAllTags: vi.fn(), BatchAddTagToImages: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);
import ImageBatchTagControls from './ImageBatchTagControls.vue';
it('adds tags only to selected images and retains failed selections for retry', async () => {
 api.GetAllTags.mockResolvedValue([{ id: 7, name: '旅行' }, { id: 8, name: '自动', automatic_kind: 'short_video' }]);
 api.BatchAddTagToImages.mockResolvedValue({ succeeded: 1, failed: 1, errors: [{ image_id: 2 }] });
 const w = mount(ImageBatchTagControls, { props: { imageIDs: [1, 2, 3], selectedIDs: [1, 2] } });
 await w.get('[data-test="image-batch-tag-open"]').trigger('click'); await flushPromises();
 expect(w.text()).not.toContain('自动');
 await w.get('.image-batch-tags__options button').trigger('click'); await flushPromises();
 expect(api.BatchAddTagToImages).toHaveBeenCalledWith([1, 2], 7);
 expect(w.emitted('update:selectedIDs')[0]).toEqual([[2]]);
 expect(w.text()).toContain('1 张图片添加失败');
 expect(w.emitted('added')[0][0].imageIDs).toEqual([1]); w.unmount();
});
