import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  GetPreviewSession: vi.fn(),
  GetSubtitleEditDocument: vi.fn(),
  PreviewExternally: vi.fn(),
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
});

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
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);

    await wrapper.find('[data-test="close-workbench"]').trigger('click');
    expect(wrapper.emitted('close')).toBeUndefined();
    confirm.mockReturnValue(true);
    await wrapper.find('[data-test="close-workbench"]').trigger('click');
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
});
