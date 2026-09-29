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

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import RenameDialogs, { DEFAULT_VIDEO_EXTENSIONS, renamedVideoFileName } from './RenameDialogs.vue';

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

// D-PC12：只有「视频扩展名」里的后缀才算扩展名，其余一律补回原扩展名；名字没变就直接关掉。
describe('重命名视频的扩展名规则', () => {
  it('LIB-03 预填 The.Matrix.1999.1080p 直接确认：不调后端，只关掉弹窗', async () => {
    const wrapper = mount(RenameDialogs, { props: { reloadView: vi.fn() } });
    await wrapper.vm.renameVideo({ id: 5, name: 'The.Matrix.1999.1080p.mkv', path: '/v/The.Matrix.1999.1080p.mkv' });
    expect(wrapper.vm.renameDialog.newName).toBe('The.Matrix.1999.1080p');
    expect(wrapper.get('[data-test="rename-video-preview"]').text()).toBe('文件名没有变化');
    await wrapper.vm.executeRename();
    expect(api.RenameVideo).not.toHaveBeenCalled();
    expect(wrapper.emitted('video-renamed')).toBeUndefined();
    expect(wrapper.vm.renameDialog.show).toBe(false);
    wrapper.unmount();
  });

  it('LIB-03 名字里带点也补回原扩展名，显示的新文件名与后端一致', async () => {
    const wrapper = mount(RenameDialogs, { props: { reloadView: vi.fn() } });
    await wrapper.vm.renameVideo({ id: 5, name: 'The.Matrix.1999.1080p.mkv', path: '/v/The.Matrix.1999.1080p.mkv' });
    wrapper.vm.renameDialog.newName = 'The.Matrix.1999.2160p';
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-test="rename-video-preview"]').text()).toBe('将重命名为：The.Matrix.1999.2160p.mkv');
    await wrapper.vm.executeRename();
    await flushPromises();
    expect(api.RenameVideo).toHaveBeenCalledWith(5, 'The.Matrix.1999.2160p');
    expect(wrapper.emitted('video-renamed')[0][0].finalName).toBe('The.Matrix.1999.2160p.mkv');
    wrapper.unmount();
  });

  it('LIB-03 三种情况：带点的名字、a.b.c、显式改成 .mp4', () => {
    expect(renamedVideoFileName('The.Matrix.1999.1080p.mkv', 'The.Matrix.1999.1080p', '')).toBe('The.Matrix.1999.1080p.mkv');
    expect(renamedVideoFileName('clip.mkv', 'a.b.c', '')).toBe('a.b.c.mkv');
    expect(renamedVideoFileName('clip.mkv', 'clip.mp4', '')).toBe('clip.mp4');
    // 大小写不敏感，与后端 strings.ToLower 一致；设置里的扩展名可以不带点。
    expect(renamedVideoFileName('clip.mkv', 'clip.MP4', 'mp4, mkv')).toBe('clip.MP4');
    // 自定义了「视频扩展名」时只认设置里的：.avi 不在里面就补回原扩展名。
    expect(renamedVideoFileName('clip.mkv', 'clip.avi', '.mp4,.mkv')).toBe('clip.avi.mkv');
    // 原文件没有扩展名时什么都不补。
    expect(renamedVideoFileName('README', 'notes', '')).toBe('notes');
  });

  it('LIB-03 默认视频扩展名与后端 defaultVideoExtensions 一致', () => {
    const goSource = readFileSync(resolve(process.cwd(), '../services/video_scan.go'), 'utf8');
    const match = goSource.match(/const defaultVideoExtensions = "([^"]+)"/);
    expect(match).toBeTruthy();
    expect(DEFAULT_VIDEO_EXTENSIONS).toBe(match[1]);
  });

  it('LIB-03 片库页把设置里的视频扩展名交给弹窗', async () => {
    const wrapper = mount(RenameDialogs, { props: { reloadView: vi.fn(), videoExtensions: '.mp4' } });
    await wrapper.vm.renameVideo({ id: 5, name: 'clip.mkv', path: '/v/clip.mkv' });
    wrapper.vm.renameDialog.newName = 'clip.mkv';
    await wrapper.vm.$nextTick();
    // .mkv 不在自定义列表里，所以当作名字的一部分，补回原扩展名。
    expect(wrapper.vm.renameFinalName).toBe('clip.mkv.mkv');
    wrapper.unmount();
  });
});
