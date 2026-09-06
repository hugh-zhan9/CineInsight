import { mount } from '@vue/test-utils';
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
