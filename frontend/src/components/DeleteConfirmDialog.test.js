import { flushPromises, mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import DeleteConfirmDialog from './DeleteConfirmDialog.vue';

// 删除确认是最后一道闸：把文件大小放进提示里，是最便宜的"删对了没"自检。
describe('DeleteConfirmDialog', () => {
  const settings = { confirm_before_delete: true, delete_original_file: false };

  it('单个视频的提示带文件大小', () => {
    const wrapper = mount(DeleteConfirmDialog, { props: { visible: true, settings, video: { id: 1, name: 'a.mp4', size: 2 * 1024 * 1024 * 1024 } } });
    expect(wrapper.text()).toContain('确定要删除视频 "a.mp4"（2.0 GB）吗？');
  });

  it('没有大小时提示保持原样，批量删除只报数量', () => {
    const single = mount(DeleteConfirmDialog, { props: { visible: true, settings, video: { id: 1, name: 'a.mp4' } } });
    expect(single.text()).toContain('确定要删除视频 "a.mp4" 吗？');
    const batch = mount(DeleteConfirmDialog, { props: { visible: true, settings, videoCount: 3 } });
    expect(batch.text()).toContain('确定要删除选中的 3 个视频吗？');
  });
});

// D-PC01 / D-PC03：确认框按所选模式说明后果；「只删记录」要讲清以后不会再收录、在哪里解除。
describe('DeleteConfirmDialog 删除后果说明', () => {
  it('LIB-04 只删记录时说明扫描不会再收录，可在回收站「允许重新收录」', async () => {
    const wrapper = mount(DeleteConfirmDialog, { props: { visible: false, settings: { confirm_before_delete: true, delete_original_file: false }, video: { id: 1, name: 'a.mp4' } } });
    await wrapper.setProps({ visible: true });
    const hint = wrapper.get('[data-test="delete-consequence"]').text();
    expect(hint).toContain('原文件保留在磁盘上');
    expect(hint).toContain('以后扫描到同一个文件不会再收录，可在回收站「允许重新收录」');
    expect(wrapper.text()).toContain('同时把原文件移到废纸篓');
  });

  it('LIB-05 移到废纸篓时说明在访达中清空废纸篓才会释放空间，并随勾选切换', async () => {
    const wrapper = mount(DeleteConfirmDialog, { props: { visible: false, settings: { confirm_before_delete: true, delete_original_file: true }, videoCount: 2 } });
    await wrapper.setProps({ visible: true });
    expect(wrapper.get('[data-test="delete-consequence"]').text()).toContain('在访达中清空废纸篓才会释放空间');

    await wrapper.get('[data-test="delete-file-choice"]').setValue(false);
    await flushPromises();
    expect(wrapper.get('[data-test="delete-consequence"]').text()).toContain('仅删除记录');

    await wrapper.get('.btn-danger').trigger('click');
    expect(wrapper.emitted('confirm-delete')[0][0]).toEqual({ video: null, deleteFile: false, dontAskAgain: false });
  });
});
