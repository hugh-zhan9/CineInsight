import { flushPromises, mount } from '@vue/test-utils';

// 应用内确认框取代了失效的 window.confirm：默认答"确定"，需要"取消"的用例单独覆盖。
const feedback = vi.hoisted(() => ({
  confirmAction: vi.fn(() => Promise.resolve(true)),
  notify: vi.fn(),
  notifyError: vi.fn(),
  notifySuccess: vi.fn()
}));
vi.mock('../utils/feedback.js', async (importOriginal) => ({
  ...(await importOriginal()),
  ...feedback
}));
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  ConvertSubtitleToUTF8: vi.fn(),
  CreateBlankSubtitleDocument: vi.fn(),
  GetPreviewSession: vi.fn(),
  GetSubtitleEditDocument: vi.fn(),
  ListSubtitleBackups: vi.fn(),
  PreviewExternally: vi.fn(),
  RestoreSubtitleBackup: vi.fn(),
  RetranslateSubtitleEntries: vi.fn(),
  SaveSubtitleEditDocument: vi.fn()
}));

vi.mock('../../wailsjs/go/main/App', () => api);

import SubtitleWorkbench from './SubtitleWorkbench.vue';

const documentFixture = () => ({
  video_id: 7,
  fingerprint: { size: 94, mod_time_ns: 123, sha256: 'abc' },
  entries: [
    { client_id: 'cue-1', start_time_ms: 0, end_time_ms: 1000, text: 'first' },
    { client_id: 'cue-2', start_time_ms: 1000, end_time_ms: 2000, text: 'second' }
  ]
});

async function mountWorkbench() {
  api.GetSubtitleEditDocument.mockResolvedValue(documentFixture());
  api.GetPreviewSession.mockResolvedValue({ video_id: 7, mode: 'unavailable', reason_message: 'preview unavailable' });
  const wrapper = mount(SubtitleWorkbench, { props: { video: { id: 7, name: 'movie.mp4', duration: 30 } } });
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  feedback.confirmAction.mockResolvedValue(true);
  api.ListSubtitleBackups.mockResolvedValue([]);
});

async function mountWithDocument(document) {
  api.GetSubtitleEditDocument.mockResolvedValue(document);
  api.GetPreviewSession.mockResolvedValue({ video_id: 7, mode: 'unavailable', reason_message: '无法内嵌预览' });
  const wrapper = mount(SubtitleWorkbench, { props: { video: { id: 7, name: 'movie.mp4', duration: 30 } } });
  await flushPromises();
  return wrapper;
}

describe('SubtitleWorkbench', () => {
  it('loads a strict subtitle document and exposes the production tools', async () => {
    const wrapper = await mountWorkbench();

    expect(api.GetSubtitleEditDocument).toHaveBeenCalledWith(7);
    expect(wrapper.text()).toContain('movie.mp4');
    expect(wrapper.text()).toContain('拆分');
    expect(wrapper.text()).toContain('合并');
    expect(wrapper.text()).toContain('查找替换');
    expect(wrapper.findAll('[data-test="subtitle-entry"]')).toHaveLength(2);
  });

  it('edits text with undo and redo while tracking dirty state', async () => {
    const wrapper = await mountWorkbench();
    const textarea = wrapper.find('[data-test="entry-text-cue-1"]');

    await textarea.setValue('changed');
    expect(wrapper.vm.entries[0].text).toBe('changed');
    expect(wrapper.vm.isDirty).toBe(true);

    await wrapper.find('[data-test="undo"]').trigger('click');
    expect(wrapper.vm.entries[0].text).toBe('first');
    await wrapper.find('[data-test="redo"]').trigger('click');
    expect(wrapper.vm.entries[0].text).toBe('changed');
  });

  it('protects unsaved work when closing', async () => {
    const wrapper = await mountWorkbench();
    await wrapper.find('[data-test="entry-text-cue-1"]').setValue('changed');
    feedback.confirmAction.mockResolvedValue(false);

    await wrapper.find('[data-test="close-workbench"]').trigger('click');
    await flushPromises();
    expect(wrapper.emitted('close')).toBeUndefined();
    feedback.confirmAction.mockResolvedValue(true);
    await wrapper.find('[data-test="close-workbench"]').trigger('click');
    await flushPromises();
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('keeps edits dirty when the backend reports an external conflict', async () => {
    const wrapper = await mountWorkbench();
    await wrapper.find('[data-test="entry-text-cue-1"]').setValue('changed');
    api.SaveSubtitleEditDocument.mockResolvedValue({
      status: 'rejected',
      error_code: 'subtitle_conflict',
      message: 'reload before saving'
    });

    await wrapper.find('[data-test="save-subtitle"]').trigger('click');
    await flushPromises();

    expect(api.SaveSubtitleEditDocument).toHaveBeenCalledWith(expect.objectContaining({ video_id: 7 }));
    expect(wrapper.vm.isDirty).toBe(true);
    expect(wrapper.text()).toContain('reload before saving');
  });

  it('applies selected retranslation as one undoable mutation', async () => {
    const wrapper = await mountWorkbench();
    wrapper.vm.selectedIDs = ['cue-1', 'cue-2'];
    api.RetranslateSubtitleEntries.mockResolvedValue({
      entries: [
        { client_id: 'cue-1', text: '第一' },
        { client_id: 'cue-2', text: '第二' }
      ]
    });

    await wrapper.vm.retranslateSelection();
    expect(wrapper.vm.entries.map(entry => entry.text)).toEqual(['第一', '第二']);
    wrapper.vm.undo();
    expect(wrapper.vm.entries.map(entry => entry.text)).toEqual(['first', 'second']);
  });

  it('supports split, merge, offset, find/replace, insert, and delete as undoable operations', async () => {
    const wrapper = await mountWorkbench();
    wrapper.vm.selectedIDs = ['cue-1'];

    wrapper.vm.splitSelected();
    expect(wrapper.vm.entries).toHaveLength(3);
    expect(wrapper.vm.entries[0].end_time_ms).toBe(wrapper.vm.entries[1].start_time_ms);
    wrapper.vm.mergeSelected();
    expect(wrapper.vm.entries).toHaveLength(2);

    wrapper.vm.selectedIDs = ['cue-1'];
    wrapper.vm.offsetMs = 250;
    wrapper.vm.applyOffset(true);
    expect(wrapper.vm.entries[0].start_time_ms).toBe(250);
    expect(wrapper.vm.entries[1].start_time_ms).toBe(1000);

    wrapper.vm.findText = 'second';
    wrapper.vm.replaceText = 'replaced';
    wrapper.vm.replaceMatches();
    expect(wrapper.vm.entries[1].text).toBe('replaced');

    wrapper.vm.selectedIDs = ['cue-2'];
    wrapper.vm.insertEntry();
    expect(wrapper.vm.entries).toHaveLength(3);
    wrapper.vm.deleteSelected();
    expect(wrapper.vm.entries).toHaveLength(2);
    expect(wrapper.vm.history.length).toBeGreaterThan(0);
  });

  it('marks a successful explicit save as clean and updates the fingerprint', async () => {
    const wrapper = await mountWorkbench();
    await wrapper.find('[data-test="entry-text-cue-1"]').setValue('saved text');
    api.SaveSubtitleEditDocument.mockResolvedValue({
      status: 'saved',
      fingerprint: { size: 100, mod_time_ns: 456, sha256: 'def' }
    });

    await wrapper.find('[data-test="save-subtitle"]').trigger('click');
    await flushPromises();

    expect(wrapper.vm.isDirty).toBe(false);
    expect(wrapper.vm.documentFingerprint.sha256).toBe('def');
    expect(wrapper.emitted('saved')).toHaveLength(1);
  });

  it('seeks the inline player when a cue is selected', async () => {
    api.GetSubtitleEditDocument.mockResolvedValue(documentFixture());
    api.GetPreviewSession.mockResolvedValue({
      video_id: 7,
      mode: 'inline',
      inline_source: { locator_value: 'asset://movie', mime: 'video/mp4' }
    });
    const wrapper = mount(SubtitleWorkbench, { props: { video: { id: 7, name: 'movie.mp4', duration: 30 } } });
    await flushPromises();
    const video = wrapper.find('video').element;

    wrapper.vm.seekToEntry(wrapper.vm.entries[1]);
    expect(video.currentTime).toBe(1);
    expect(wrapper.vm.selectedIDs).toEqual(['cue-2']);
  });

  it('MEDIA-07 选区多数是双语两行时默认「只替换译文行」，可改成整条替换，请求带上 mode', async () => {
    const wrapper = await mountWithDocument({
      video_id: 7, fingerprint: { size: 1, mod_time_ns: 1, sha256: 'a' }, issues: [],
      entries: [
        { client_id: 'cue-1', start_time_ms: 0, end_time_ms: 1000, text: 'Hello\n你好' },
        { client_id: 'cue-2', start_time_ms: 1000, end_time_ms: 2000, text: 'Bye\n再见' }
      ]
    });
    wrapper.vm.selectedIDs = ['cue-1', 'cue-2'];
    await flushPromises();
    expect(wrapper.find('[data-test="retranslate-mode-translation_line"]').element.checked).toBe(true);
    api.RetranslateSubtitleEntries.mockResolvedValue({
      entries: [{ client_id: 'cue-1', text: 'Hello\n您好' }, { client_id: 'cue-2', text: 'Bye\n回见' }],
      warnings: ['有 1 条译文为空，已保留原文']
    });
    await wrapper.vm.retranslateSelection();
    expect(api.RetranslateSubtitleEntries).toHaveBeenLastCalledWith(expect.objectContaining({ mode: 'translation_line' }));
    expect(wrapper.vm.entries[0].text).toBe('Hello\n您好');
    expect(wrapper.vm.operationMessage).toContain('只替换了译文行');
    expect(wrapper.vm.operationMessage).toContain('有 1 条译文为空');

    await wrapper.find('[data-test="retranslate-mode-whole_entry"]').setValue(true);
    await wrapper.vm.retranslateSelection();
    expect(api.RetranslateSubtitleEntries).toHaveBeenLastCalledWith(expect.objectContaining({ mode: 'whole_entry' }));
  });

  it('MEDIA-07 单语选区默认整条替换', async () => {
    const wrapper = await mountWorkbench();
    wrapper.vm.selectedIDs = ['cue-1'];
    await flushPromises();
    expect(wrapper.find('[data-test="retranslate-mode-whole_entry"]').element.checked).toBe(true);
  });
});

describe('SubtitleWorkbench 容错（D-PC15）', () => {
  it('MEDIA-06 零时长、重叠的字幕照样打开；「下一个问题」逐条跳，「一键修复时间」修好后可保存', async () => {
    const wrapper = await mountWithDocument({
      video_id: 7, fingerprint: { size: 1, mod_time_ns: 1, sha256: 'a' },
      entries: [
        { client_id: 'cue-1', start_time_ms: 0, end_time_ms: 1000, text: 'first' },
        { client_id: 'cue-2', start_time_ms: 1500, end_time_ms: 1500, text: 'zero' },
        { client_id: 'cue-3', start_time_ms: 1800, end_time_ms: 3000, text: 'third' },
        { client_id: 'cue-4', start_time_ms: 2500, end_time_ms: 4000, text: 'overlap' }
      ],
      issues: [{ index: 2, kind: 'zero_duration' }, { index: 4, kind: 'overlap' }]
    });

    expect(wrapper.findAll('[data-test="subtitle-entry"]')).toHaveLength(4);
    expect(wrapper.text()).toContain('打开时发现 2 处时间问题');
    expect(wrapper.find('[data-test="workbench-issues"]').text()).toContain('2 条有问题');
    expect(wrapper.find('[data-test="save-subtitle"]').attributes('disabled')).toBeDefined();

    await wrapper.find('[data-test="workbench-next-issue"]').trigger('click');
    expect(wrapper.vm.selectedIDs).toEqual(['cue-2']);
    await wrapper.find('[data-test="workbench-next-issue"]').trigger('click');
    expect(wrapper.vm.selectedIDs).toEqual(['cue-4']);
    await wrapper.find('[data-test="workbench-next-issue"]').trigger('click');
    expect(wrapper.vm.selectedIDs).toEqual(['cue-2']);

    await wrapper.find('[data-test="workbench-fix-timing"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.entries[1].end_time_ms).toBe(1800);
    expect(wrapper.vm.entries[2].end_time_ms).toBe(2499);
    expect(wrapper.vm.validationIssues).toEqual([]);
    expect(wrapper.find('[data-test="workbench-issues"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="save-subtitle"]').attributes('disabled')).toBeUndefined();
    wrapper.vm.undo();
    expect(wrapper.vm.entries[1].end_time_ms).toBe(1500);
  });

  it('MEDIA-06 保存被后端拒绝时跳到第一个问题并说出是第几条', async () => {
    const wrapper = await mountWorkbench();
    await wrapper.find('[data-test="entry-text-cue-2"]').setValue('changed');
    api.SaveSubtitleEditDocument.mockResolvedValue({
      status: 'rejected', error_code: 'subtitle_validation_failed', message: '字幕校验未通过',
      issues: [{ entry_index: 2, client_id: 'cue-2', code: 'text_too_large', message: '字幕文本过长' }],
      first_issue_entry_index: 2, first_issue_client_id: 'cue-2'
    });
    await wrapper.find('[data-test="save-subtitle"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.selectedIDs).toEqual(['cue-2']);
    expect(wrapper.text()).toContain('第 2 条：字幕文本过长');
  });

  it('MEDIA-06 没有字幕时提供「新建空白字幕」，保存时带零指纹创建文件', async () => {
    const wrapper = await mountWithDocument({ video_id: 7, entries: [], issues: [], error_code: 'subtitle_missing', message: '该视频还没有外挂字幕，请先生成字幕' });
    expect(wrapper.find('[data-test="workbench-missing"]').text()).toContain('该视频还没有外挂字幕');
    api.CreateBlankSubtitleDocument.mockResolvedValue({ video_id: 7, fingerprint: { size: 0, mod_time_ns: 0, sha256: '' }, entries: [], issues: [] });

    await wrapper.find('[data-test="workbench-create-blank"]').trigger('click');
    await flushPromises();
    expect(api.CreateBlankSubtitleDocument).toHaveBeenCalledWith(7);
    expect(wrapper.findAll('[data-test="subtitle-entry"]')).toHaveLength(0);

    wrapper.vm.insertEntry();
    await flushPromises();
    api.SaveSubtitleEditDocument.mockResolvedValue({ status: 'saved', fingerprint: { size: 30, mod_time_ns: 9, sha256: 'new' } });
    await wrapper.find('[data-test="save-subtitle"]').trigger('click');
    await flushPromises();
    expect(api.SaveSubtitleEditDocument).toHaveBeenCalledWith(expect.objectContaining({
      video_id: 7, fingerprint: { size: 0, mod_time_ns: 0, sha256: '' }
    }));
    expect(wrapper.emitted('saved')).toHaveLength(1);
  });

  it('MEDIA-08 只有内嵌/其他格式字幕时说明原因，不给重试也不引导重新识别', async () => {
    const wrapper = await mountWithDocument({ video_id: 7, entries: [], issues: [], error_code: 'subtitle_not_sidecar_srt', message: '该视频只有内嵌/其他格式字幕，暂不支持编辑或翻译' });
    const state = wrapper.find('[data-test="workbench-load-error"]');
    expect(state.text()).toContain('该视频只有内嵌/其他格式字幕，暂不支持编辑或翻译');
    expect(state.text()).not.toContain('重试');
    expect(wrapper.find('[data-test="workbench-create-blank"]').exists()).toBe(false);
  });

  it('MEDIA-06 打不开时的报错是中文且不带绝对路径', async () => {
    api.GetSubtitleEditDocument.mockRejectedValue(new Error('open /Users/me/Movies/movie.srt: permission denied'));
    api.GetPreviewSession.mockResolvedValue({ video_id: 7, mode: 'unavailable' });
    const wrapper = mount(SubtitleWorkbench, { props: { video: { id: 7, name: 'movie.mp4' } } });
    await flushPromises();
    const text = wrapper.find('[data-test="workbench-load-error"]').text();
    expect(text).toContain('无法打开字幕工作台：没有读写这个文件的权限。');
    expect(text).not.toContain('/Users/');
  });

  it('MEDIA-02 非 UTF-8 字幕不直接编辑：提示一键转换（会先备份），转换后重新打开', async () => {
    const wrapper = await mountWithDocument({
      video_id: 7, entries: [], issues: [], error_code: 'subtitle_encoding_not_utf8', detected_encoding: 'gb18030',
      message: '字幕文件不是 UTF-8 编码（检测为 GB18030），请先转换为 UTF-8 后再操作'
    });
    expect(wrapper.find('[data-test="workbench-encoding"]').text()).toContain('字幕编码为 GBK（推测），可一键转换为 UTF-8（会先备份）。');
    api.ConvertSubtitleToUTF8.mockResolvedValue({ encoding: 'gb18030', backup_id: 'b1' });
    api.GetSubtitleEditDocument.mockResolvedValue(documentFixture());

    await wrapper.find('[data-test="workbench-convert-utf8"]').trigger('click');
    await flushPromises();
    expect(api.ConvertSubtitleToUTF8).toHaveBeenCalledWith(7, 'gb18030');
    expect(wrapper.findAll('[data-test="subtitle-entry"]')).toHaveLength(2);
    expect(wrapper.text()).toContain('已转换为 UTF-8（原文件已备份');
  });

  it('MEDIA-02 编码有歧义时对照预览选定，按所选编码转换', async () => {
    const wrapper = await mountWithDocument({
      video_id: 7, entries: [], issues: [], error_code: 'subtitle_encoding_not_utf8', detected_encoding: 'gb18030',
      candidates: [{ encoding: 'gb18030', preview: '预览一' }, { encoding: 'big5', preview: '預覽二' }]
    });
    expect(wrapper.text()).toContain('預覽二');
    await wrapper.find('[data-test="workbench-encoding-big5"]').setValue(true);
    api.ConvertSubtitleToUTF8.mockResolvedValue({ encoding: 'big5' });
    api.GetSubtitleEditDocument.mockResolvedValue(documentFixture());
    await wrapper.find('[data-test="workbench-convert-utf8"]').trigger('click');
    await flushPromises();
    expect(api.ConvertSubtitleToUTF8).toHaveBeenCalledWith(7, 'big5');
  });
});

describe('SubtitleWorkbench 历史版本（D-PC13）', () => {
  it('MEDIA-05 列出备份并恢复所选版本：先确认，恢复后重新载入并通知片库页', async () => {
    api.ListSubtitleBackups.mockResolvedValue([
      { id: 'b2', created_at: '2026-09-30T10:20:00+08:00', size: 2048 },
      { id: 'b1', created_at: '2026-09-29T09:00:00+08:00', size: 1024 }
    ]);
    api.RestoreSubtitleBackup.mockResolvedValue({ backup_id: 'b3' });
    const wrapper = await mountWorkbench();
    expect(wrapper.find('[data-test="workbench-backup-select"]').text()).toContain('2026-09-30 10:20 · 2.0 KB');

    await wrapper.find('[data-test="workbench-restore-backup"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).toHaveBeenCalledWith(expect.objectContaining({ title: '恢复字幕版本' }));
    expect(api.RestoreSubtitleBackup).toHaveBeenCalledWith(7, 'b2');
    expect(api.GetSubtitleEditDocument).toHaveBeenCalledTimes(2);
    expect(wrapper.text()).toContain('已恢复到 2026-09-30 10:20 的版本');
    expect(wrapper.emitted('saved')).toHaveLength(1);
  });

  it('MEDIA-05 没有备份时说明会自动备份；取消确认则不恢复', async () => {
    const empty = await mountWorkbench();
    expect(empty.find('[data-test="workbench-backups"]').text()).toContain('还没有备份');
    empty.unmount();

    api.ListSubtitleBackups.mockResolvedValue([{ id: 'b1', created_at: '2026-09-29T09:00:00+08:00', size: 10 }]);
    feedback.confirmAction.mockResolvedValue(false);
    const wrapper = await mountWorkbench();
    await wrapper.find('[data-test="workbench-restore-backup"]').trigger('click');
    await flushPromises();
    expect(api.RestoreSubtitleBackup).not.toHaveBeenCalled();
  });
});
