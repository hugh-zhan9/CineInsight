// 断言原在 src/components/VideoListPage.test.js 的 'renames a selected managed folder
// and refreshes paths'：两个重命名弹窗抽成独立组件后原样搬来，只换挂载对象；
// 会动片库页的三件事（扫描目录、设置、列表重载）改成对 emit / 函数 prop 的断言。
import { flushPromises, mount } from '@vue/test-utils';

const feedback = vi.hoisted(() => ({
  confirmAction: vi.fn(() => Promise.resolve(true)),
  notify: vi.fn(),
  notifyError: vi.fn(),
  notifySuccess: vi.fn()
}));
vi.mock('../../utils/feedback.js', async (importOriginal) => ({
  ...(await importOriginal()),
  ...feedback
}));

import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));

vi.mock('../../../wailsjs/go/main/App', () => api);

import RenameDialogs from './RenameDialogs.vue';

beforeEach(() => {
  vi.clearAllMocks();
});

describe('重命名弹窗', () => {
  it('renames a selected managed folder and refreshes paths', async () => {
    const reloadView = vi.fn().mockResolvedValue();
    const wrapper = mount(RenameDialogs, { props: { reloadView } });

    api.SelectFolderToRename.mockResolvedValueOnce('/library/Old Name');
    api.RenameDirectory.mockResolvedValueOnce({ videos_updated: 3, directories_updated: 1 });
    api.GetSettings.mockResolvedValueOnce({ scan_exclude_paths: '/library/New Name/private' });

    await wrapper.vm.renameFolder();
    expect(wrapper.vm.folderRenameDialog).toEqual(expect.objectContaining({
      show: true, source: '/library/Old Name', currentName: 'Old Name', newName: 'Old Name'
    }));

    wrapper.vm.folderRenameDialog.newName = 'New Name';
    await wrapper.vm.executeFolderRename();

    expect(api.RenameDirectory).toHaveBeenCalledWith('/library/Old Name', 'New Name');
    expect(wrapper.emitted('reload-directories')).toHaveLength(1);
    expect(wrapper.emitted('update-settings')[0][0]).toEqual({ scan_exclude_paths: '/library/New Name/private' });
    expect(reloadView).toHaveBeenCalledOnce();
    expect(wrapper.vm.folderRenameDialog.show).toBe(false);
    expect(feedback.notify).toHaveBeenCalledWith('文件夹重命名完成：更新 3 个视频、1 个扫描目录。');
    // 迁移互斥标记由片库页持有，这里只报告进入与退出。
    expect(wrapper.emitted('update:migrationRunning')).toEqual([[true], [false]]);
    wrapper.unmount();
  });

  it('重命名视频后把新文件名交回片库页，由它就地改那一行', async () => {
    const wrapper = mount(RenameDialogs, { props: { reloadView: vi.fn() } });
    await wrapper.vm.renameVideo({ id: 5, name: 'old.mkv', path: '/v/old.mkv' });
    expect(wrapper.vm.renameDialog).toEqual(expect.objectContaining({ show: true, newName: 'old', ext: '.mkv' }));

    wrapper.vm.renameDialog.newName = 'new';
    await wrapper.vm.executeRename();
    await flushPromises();

    expect(api.RenameVideo).toHaveBeenCalledWith(5, 'new');
    expect(wrapper.emitted('video-renamed')[0][0]).toEqual({
      video: expect.objectContaining({ id: 5 }),
      finalName: 'new.mkv'
    });
    expect(wrapper.vm.renameDialog.show).toBe(false);
    wrapper.unmount();
  });
});
