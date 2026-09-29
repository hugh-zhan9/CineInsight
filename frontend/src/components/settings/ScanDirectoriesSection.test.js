import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => Object.fromEntries([
  'SelectDirectory', 'GetAllDirectories', 'AddDirectory', 'UpdateDirectory', 'UpdateDirectoryWithMode', 'ValidateScanDirectory',
  'DeleteDirectory', 'RetryLibraryWatcherRoot', 'GetAllImageDirectories', 'AddImageDirectory', 'UpdateImageDirectory', 'DeleteImageDirectory'
].map(name => [name, vi.fn()])));
vi.mock('../../../wailsjs/go/main/App', () => api);
const feedback = vi.hoisted(() => ({ confirmAction: vi.fn(() => Promise.resolve(true)), notify: vi.fn(), notifyError: vi.fn() }));
vi.mock('../../utils/feedback.js', async (importOriginal) => ({ ...(await importOriginal()), ...feedback }));

import ScanDirectoriesSection from './ScanDirectoriesSection.vue';

const movies = { id: 1, path: '/Volumes/Movies', alias: '电影' };

function mountSection(directories = [movies]) {
  return mount(ScanDirectoriesSection, {
    props: { form: { library_watch_enabled: false }, directories, watcherStatus: null, reloadWatcherStatus: vi.fn().mockResolvedValue() }
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  feedback.confirmAction.mockResolvedValue(true);
  api.GetAllImageDirectories.mockResolvedValue([]);
  api.GetAllDirectories.mockResolvedValue([movies]);
});

async function editTo(wrapper, newPath) {
  await wrapper.findAll('.directory-actions button')[0].trigger('click');
  api.SelectDirectory.mockResolvedValueOnce(newPath);
  await wrapper.get('[data-test="select-scan-directory"]').trigger('click');
  await flushPromises();
}

describe('编辑扫描目录路径 = 重映射还是替换（D-PC07）', () => {
  it('LIB-02 旧路径已经找不到（挂载名变了）时默认选「搬到了新位置」，保存走 remap 并报改写了多少条', async () => {
    api.ValidateScanDirectory.mockImplementation(async path => ({ exists: path !== '/Volumes/Movies', duplicate_of: '', nested_in: '', contains: [] }));
    api.UpdateDirectoryWithMode.mockResolvedValueOnce({ mode: 'remap', path_changed: true, new_path: '/Volumes/Movies 1', rewritten: { videos: 120, images: 8 }, marked_stale: 0 });
    const wrapper = mountSection();
    await editTo(wrapper, '/Volumes/Movies 1');

    const modes = wrapper.get('[data-test="directory-edit-mode"]');
    // 说明写清楚图片路径同样改写。
    expect(modes.text()).toContain('图片库里同一路径下的图片记录');
    expect(wrapper.get('[data-test="directory-edit-remap"]').element.checked).toBe(true);
    expect(modes.text()).toContain('原来的位置现在找不到');

    await wrapper.get('[data-test="save-scan-directory"]').trigger('click');
    await flushPromises();
    expect(api.UpdateDirectoryWithMode).toHaveBeenCalledWith(1, '/Volumes/Movies 1', '电影', 'remap');
    expect(api.UpdateDirectory).not.toHaveBeenCalled();
    expect(feedback.notify).toHaveBeenCalledWith('已把「电影」改到新位置：改写了 120 个视频、8 张图片的路径，标签与观看进度都保留。');
    expect(wrapper.emitted('directories-changed')).toHaveLength(1);
    wrapper.unmount();
  });

  it('LIB-02 旧路径还在时要用户自己选；选「换成另一个目录」走 replace', async () => {
    api.ValidateScanDirectory.mockResolvedValue({ exists: true, duplicate_of: '', nested_in: '', contains: [] });
    api.UpdateDirectoryWithMode.mockResolvedValueOnce({ mode: 'replace', path_changed: true, new_path: '/Volumes/Other', rewritten: {}, marked_stale: 30 });
    const wrapper = mountSection();
    await editTo(wrapper, '/Volumes/Other');
    expect(wrapper.get('[data-test="directory-edit-remap"]').element.checked).toBe(false);
    expect(wrapper.get('[data-test="directory-edit-replace"]').element.checked).toBe(false);

    await wrapper.get('[data-test="save-scan-directory"]').trigger('click');
    await flushPromises();
    expect(api.UpdateDirectoryWithMode).not.toHaveBeenCalled();
    expect(wrapper.get('[data-test="scan-directory-dialog-error"]').text()).toContain('请先选择');

    await wrapper.get('[data-test="directory-edit-replace"]').setValue(true);
    await wrapper.get('[data-test="save-scan-directory"]').trigger('click');
    await flushPromises();
    expect(api.UpdateDirectoryWithMode).toHaveBeenCalledWith(1, '/Volumes/Other', '电影', 'replace');
    expect(feedback.notify).toHaveBeenCalledWith('已换成新目录「电影」：旧目录下 30 个视频进入「路径失效 · 目录已移除」。');
    wrapper.unmount();
  });

  it('LIB-02 只改别名不问含义', async () => {
    api.UpdateDirectoryWithMode.mockResolvedValueOnce({ mode: '', path_changed: false });
    const wrapper = mountSection();
    await wrapper.findAll('.directory-actions button')[0].trigger('click');
    expect(wrapper.find('[data-test="directory-edit-mode"]').exists()).toBe(false);
    await wrapper.get('[data-test="scan-directory-alias"]').setValue('片库');
    await wrapper.get('[data-test="save-scan-directory"]').trigger('click');
    await flushPromises();
    expect(api.UpdateDirectoryWithMode).toHaveBeenCalledWith(1, '/Volumes/Movies', '片库', '');
    expect(feedback.notify).not.toHaveBeenCalled();
    wrapper.unmount();
  });
});

describe('添加目录的去重与嵌套提示、错误显示（D-PC09）', () => {
  it('LIB-14 已在扫描目录里的挡住保存；嵌套只提示；提示里只写别名', async () => {
    const wrapper = mountSection();
    await wrapper.get('[data-test="add-scan-directory"]').trigger('click');
    api.ValidateScanDirectory.mockResolvedValueOnce({ exists: true, duplicate_of: '/Volumes/Movies', nested_in: '', contains: [] });
    api.SelectDirectory.mockResolvedValueOnce('/Volumes/Movies');
    await wrapper.get('[data-test="select-scan-directory"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="scan-directory-hint"]').text()).toBe('它已经在扫描目录里了（「电影」）。');
    await wrapper.get('[data-test="save-scan-directory"]').trigger('click');
    await flushPromises();
    expect(api.AddDirectory).not.toHaveBeenCalled();

    api.ValidateScanDirectory.mockResolvedValueOnce({ exists: true, duplicate_of: '', nested_in: '/Volumes/Movies', contains: [] });
    api.SelectDirectory.mockResolvedValueOnce('/Volumes/Movies/Kids');
    await wrapper.get('[data-test="select-scan-directory"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="scan-directory-hint"]').text()).toContain('它位于扫描目录「电影」之内');
    await wrapper.get('[data-test="save-scan-directory"]').trigger('click');
    await flushPromises();
    expect(api.AddDirectory).toHaveBeenCalledWith('/Volumes/Movies/Kids', '');
    wrapper.unmount();
  });

  it('LIB-14 增删目录失败时把原因显示在分区里，而不是吞掉', async () => {
    api.DeleteDirectory.mockRejectedValueOnce(new Error('database is in maintenance mode'));
    const wrapper = mountSection();
    await wrapper.findAll('.directory-actions button')[1].trigger('click');
    await flushPromises();
    const error = wrapper.get('[data-test="scan-dirs-error"]');
    expect(error.text()).toContain('删除扫描目录失败');
    // 数据库层的英文哨兵错误先翻成中文（G-3）。
    expect(error.text()).toContain('数据库正在恢复备份或切换后端');
    expect(wrapper.emitted('directories-changed')).toBeUndefined();

    await wrapper.get('[data-test="add-scan-directory"]').trigger('click');
    api.ValidateScanDirectory.mockResolvedValueOnce({ exists: true, duplicate_of: '', nested_in: '', contains: [] });
    api.SelectDirectory.mockResolvedValueOnce('/Volumes/New');
    await wrapper.get('[data-test="select-scan-directory"]').trigger('click');
    await flushPromises();
    api.AddDirectory.mockRejectedValueOnce('磁盘不可写');
    await wrapper.get('[data-test="save-scan-directory"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="scan-directory-dialog-error"]').text()).toBe('保存扫描目录失败：磁盘不可写');
    wrapper.unmount();
  });

  it('LIB-14 图片目录的保存失败同样显示出来', async () => {
    const wrapper = mountSection();
    await wrapper.get('[data-test="add-image-directory"]').trigger('click');
    api.SelectDirectory.mockResolvedValueOnce('/Volumes/Photos');
    const row = wrapper.findAll('.directory-dialog-row').find(item => item.find('[data-test="image-directory-path"]').exists());
    await row.find('button').trigger('click');
    await flushPromises();
    api.AddImageDirectory.mockRejectedValueOnce('目录不存在');
    await wrapper.get('[data-test="save-image-directory"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="image-directory-dialog-error"]').text()).toBe('保存图片目录失败：目录不存在');
    wrapper.unmount();
  });
});
