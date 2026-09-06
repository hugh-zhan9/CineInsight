import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ ListTrashEntries: vi.fn(), RestoreTrashEntry: vi.fn(), ListImageTrashEntries: vi.fn(), RestoreImageTrashEntry: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);

import PhotoTrashDialog from './PhotoTrashDialog.vue';
import TrashRestoreDialog from './TrashRestoreDialog.vue';

// 回收站条目自带 file_size，却一直没显示：恢复与否、回收站占了多少，都靠它判断。
describe('回收站弹窗文件大小', () => {
  beforeEach(() => { vi.clearAllMocks(); });

  it('视频回收站每条记录带文件大小，0 字节的旧记录不显示', async () => {
    api.ListTrashEntries.mockResolvedValue([
      { id: 1, video_name: 'a.mp4', original_path: '/v/a.mp4', file_size: 1.5 * 1024 * 1024 * 1024, status: 'trashed', created_at: '2026-09-01T00:00:00Z' },
      { id: 2, video_name: 'b.mp4', original_path: '/v/b.mp4', file_size: 0, status: 'trashed', created_at: '2026-09-01T00:00:00Z' },
    ]);
    const wrapper = mount(TrashRestoreDialog, { props: { visible: true } });
    await wrapper.vm.loadEntries();
    await flushPromises();
    const entries = wrapper.findAll('.trash-restore-entry');
    expect(entries).toHaveLength(2);
    expect(entries[0].text()).toContain('1.5 GB');
    expect(entries[1].text()).not.toContain(' B ·');
  });

  it('图片回收站同样显示文件大小', async () => {
    api.ListImageTrashEntries.mockResolvedValue([
      { id: 1, image_name: 'a.jpg', original_path: '/p/a.jpg', file_size: 3 * 1024 * 1024, status: 'trashed', created_at: '2026-09-01T00:00:00Z' },
    ]);
    const wrapper = mount(PhotoTrashDialog, { props: { visible: true } });
    await wrapper.vm.loadEntries();
    await flushPromises();
    expect(wrapper.get('.photo-trash-entry').text()).toContain('3.0 MB');
  });
});
