import { mount, flushPromises } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vitest';
import BTDownloadsPanel from './BTDownloadsPanel.vue';
const pending = () => { let resolve, reject; const promise = new Promise((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; };
const file = (extra = {}) => ({ id: 'f', path: 'film.mkv', size: '9007199254740993', selected: true, selectable: true, kind: 'video', progress: 0, import_state: 'pending', ...extra });
const task = (extra = {}) => ({ id: 't', revision: 1, title: '影片', stage: 'draft', save_path: '/movies/CineInsight/t', files: [file()], actions: ['cancel_draft'], expires_at: '2099-01-01T00:00:00Z', ...extra });
function api() { return { GetBTConnection: vi.fn().mockResolvedValue({ enabled: true }), ListBTIndexers: vi.fn().mockResolvedValue([{ id: 1, name: '站点', enabled: true }]), SearchBTResources: vi.fn().mockResolvedValue({ resources: [{ result_token: 'opaque', title: '影片', size: '5', indexer: '站点' }], indexers: [{ indexer_id: 1, status: 'failed' }] }), PreviewBTResource: vi.fn().mockResolvedValue(task()), SubmitBTDownload: vi.fn().mockResolvedValue(task({ stage: 'active', revision: 2, actions: ['stop'] })), ListBTTasks: vi.fn().mockResolvedValue({ tasks: [], next_cursor: '' }), GetBTTask: vi.fn().mockResolvedValue(task()), ControlBTTask: vi.fn().mockResolvedValue(task({ stage: 'cancelled', revision: 2 })) }; }
const wrappers = [];
async function setup(overrides = {}, props = {}) { const service = { ...api(), ...overrides }; const w = mount(BTDownloadsPanel, { props: { api: service, initialQuery: '影片', ...props }, global: { stubs: { teleport: true, DialogShell: { template: '<div><slot/><slot name="footer"/></div>' } } } }); wrappers.push(w); await flushPromises(); return { w, service }; }
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.useRealTimers(); });
describe('BT downloads panel orchestration', () => {
  it('automatically loads latest only for an enabled empty-query view; keyword searches remain explicit', async () => {
    const { w, service } = await setup({}, { initialQuery: '' });
    expect(service.SearchBTResources).toHaveBeenCalledExactlyOnceWith('', []);
    expect(w.text()).toContain('最新资源');
    await w.get('[data-test="bt-query"]').setValue('film');
    await w.get('form').trigger('submit'); await flushPromises();
    expect(service.SearchBTResources).toHaveBeenLastCalledWith('film', []);
    await w.get('[data-test="bt-show-latest"]').trigger('click'); await flushPromises();
    expect(service.SearchBTResources).toHaveBeenLastCalledWith('', []);
    expect(w.get('[data-test="bt-query"]').element.value).toBe('');
  });
  it('does not browse latest while hidden, unconfigured or without enabled stations', async () => {
    for (const [overrides, props] of [
      [{}, { active: false }],
      [{ GetBTConnection: vi.fn().mockResolvedValue({ enabled: false }) }, {}],
      [{ ListBTIndexers: vi.fn().mockResolvedValue([]) }, {}],
    ]) {
      const { service } = await setup(overrides, { initialQuery: '', ...props });
      expect(service.SearchBTResources).not.toHaveBeenCalled();
    }
  });
  it('ignores an automatic latest response after a newer manual query or hiding', async () => {
    const old = pending();
    const search = vi.fn().mockReturnValueOnce(old.promise).mockResolvedValue({ resources: [], indexers: [] });
    const { w } = await setup({ SearchBTResources: search }, { initialQuery: '' });
    await w.vm.search({ query: 'new', indexer_ids: [1] });
    old.resolve({ resources: [{ title: 'stale' }], indexers: [] }); await flushPromises();
    expect(w.vm.resultQuery).toBe('new'); expect(w.vm.result.resources).toEqual([]);
  });
  it('searches explicitly, preserves per-site failure and submits selected IDs exactly', async () => {
    const { w, service } = await setup();
    expect(service.SearchBTResources).not.toHaveBeenCalled();
    await w.get('[data-test="bt-search-submit"]').trigger('submit'); await flushPromises();
    expect(service.SearchBTResources).toHaveBeenCalledWith('影片', []);
    expect(w.text()).toContain('搜索失败');
    await w.get('[data-test="bt-preview-opaque"]').trigger('click'); await flushPromises();
    expect(w.text()).toContain('/movies/CineInsight/t');
    await w.get('[data-test="bt-files-submit"]').trigger('click'); await flushPromises();
    expect(service.SubmitBTDownload).toHaveBeenCalledWith({ draft_id: 't', expected_revision: 1, selected_file_ids: ['f'] });
    expect(w.emitted('changed')[0]).toEqual(['t']); expect(w.vm.draft).toBeNull();
  });
  it('does not turn service failures into a successful empty search or reveal raw errors', async () => {
    const { w } = await setup({ SearchBTResources: vi.fn().mockRejectedValue(new Error('search_failed: https://secret/passkey')) });
    await w.vm.search({ query: '影片', indexer_ids: [] });
    expect(w.text()).toContain('搜索失败'); expect(w.text()).not.toContain('passkey'); expect(w.find('[data-test="bt-no-results"]').exists()).toBe(false);
  });
  it('shows initial task loading failures without claiming there are no tasks', async () => {
    const { w } = await setup({ ListBTTasks: vi.fn().mockRejectedValue(new Error('service_unavailable')) });
    expect(w.text()).toContain('暂时不可用'); expect(w.text()).not.toContain('还没有从这里创建');
  });
  it('discards cancelled searches and older detail responses', async () => {
    const first = pending(), second = pending(), detailA = pending(), detailB = pending();
    const { w } = await setup({ SearchBTResources: vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise), GetBTTask: vi.fn().mockReturnValueOnce(detailA.promise).mockReturnValueOnce(detailB.promise) });
    const a = w.vm.search({ query: 'a', indexer_ids: [] }); w.vm.cancelSearch();
    const b = w.vm.search({ query: 'b', indexer_ids: [1] }); second.resolve({ resources: [], indexers: [] }); await b;
    first.resolve({ resources: [{ title: 'old' }], indexers: [] }); await a; expect(w.vm.resultQuery).toBe('b');
    const da = w.vm.showDetail('a'), db = w.vm.showDetail('b'); detailA.resolve(task({ id: 'a' })); await flushPromises(); detailB.resolve(task({ id: 'b' })); await db; await da; expect(w.vm.detail.id).toBe('b');
  });
  it('invalidates a pending preview when the search context changes', async () => {
    const old = pending(); const { w } = await setup({ PreviewBTResource: vi.fn().mockReturnValue(old.promise) });
    const preview = w.vm.preview('old'); await w.vm.search({ query: 'new', indexer_ids: [] }); old.resolve(task()); await preview;
    expect(w.vm.draft).toBeNull(); expect(w.vm.previewBusy).toBe(false);
  });
  it('shows unknown selected totals for task summaries without files', async () => {
    const { w } = await setup({ ListBTTasks: vi.fn().mockResolvedValue({ tasks: [task({ files: null, imported_count: 1 })], next_cursor: '' }) });
    expect(w.text()).toContain('入库：1/待核实');
  });
  it('blocks empty, invalid and expired selections without submitting', async () => {
    const { w, service } = await setup(); await w.vm.preview('opaque');
    await w.vm.submit({ draft_id: 't', expected_revision: 1, selected_file_ids: [] });
    await w.vm.submit({ draft_id: 't', expected_revision: 1, selected_file_ids: ['foreign'] });
    expect(service.SubmitBTDownload).not.toHaveBeenCalled();
    w.vm.draft.expires_at = '2000-01-01T00:00:00Z';
    await w.vm.submit({ draft_id: 't', expected_revision: 1, selected_file_ids: ['f'] });
    expect(w.vm.draftError).toContain('重新搜索'); expect(service.SubmitBTDownload).not.toHaveBeenCalled();
  });
  it('waits for metadata and cancels through the backend; failed cancellation remains visible', async () => {
    const { w, service } = await setup({ PreviewBTResource: vi.fn().mockResolvedValue(task({ stage: 'metadata_wait' })), ControlBTTask: vi.fn().mockRejectedValue(new Error('service_unavailable')) });
    await w.vm.preview('opaque'); expect(w.text()).toContain('尚未开始下载影片');
    await w.vm.submit({ draft_id: 't', expected_revision: 1, selected_file_ids: ['f'] }); expect(service.SubmitBTDownload).not.toHaveBeenCalled();
    await w.vm.cancelDraft(); expect(service.ControlBTTask).toHaveBeenCalledWith({ task_id: 't', expected_revision: 1, action: 'cancel_draft', confirmed: true });
    expect(w.vm.draft).not.toBeNull(); expect(w.vm.draftError).toContain('暂时不可用');
    await w.get('[data-test="bt-files-close"]').trigger('click'); expect(w.vm.draft).toBeNull(); expect(service.ControlBTTask).toHaveBeenCalledTimes(1);
  });
  it('requires confirmation for uncertain resubmission and guards playback by import state', async () => {
    const row = task({ stage: 'uncertain', actions: ['reconcile', 'reconfirm_submit', 'abandon'], files: [file({ video_id: 9 }), file({ id: 'done', video_id: 8, import_state: 'imported' })] });
    const { w, service } = await setup({ ListBTTasks: vi.fn().mockResolvedValue({ tasks: [row], next_cursor: '' }), GetBTTask: vi.fn().mockResolvedValue(row) });
    await w.vm.showDetail('t'); expect(w.findAll('button').filter(b => b.text() === '播放')).toHaveLength(1);
    w.vm.play(9); expect(w.emitted('play')).toBeUndefined(); w.vm.play(8); expect(w.emitted('play')[0]).toEqual([8]);
    await w.get('[data-test="bt-task-t-reconfirm_submit"]').trigger('click'); expect(service.ControlBTTask).not.toHaveBeenCalled();
    await w.get('[data-test="bt-task-confirm"]').trigger('click'); await flushPromises(); expect(service.ControlBTTask).toHaveBeenCalledWith({ task_id: 't', expected_revision: 1, action: 'reconfirm_submit', confirmed: true });
  });
  it('keeps a failed action visible after a successful list refresh', async () => {
    const row = task({ stage: 'active', actions: ['stop'] });
    const { w } = await setup({ ListBTTasks: vi.fn().mockResolvedValue({ tasks: [row], next_cursor: '' }), ControlBTTask: vi.fn().mockRejectedValue(new Error('auth_failed: private')) });
    await w.vm.control({ task_id: 't', expected_revision: 1, action: 'stop' });
    expect(w.text()).toContain('认证失败'); expect(w.text()).not.toContain('private');
  });
  it('preserves visible tasks on failure and bounds pagination to 200 rows', async () => {
    const page = start => ({ tasks: Array.from({ length: 50 }, (_, i) => task({ id: String(i + start) })), next_cursor: String(start + 50) });
    const list = vi.fn().mockResolvedValueOnce(page(0)).mockResolvedValueOnce(page(50)).mockResolvedValueOnce(page(100)).mockResolvedValueOnce(page(150));
    const { w, service } = await setup({ ListBTTasks: list });
    await w.vm.loadMore(); await w.vm.loadMore(); await w.vm.loadMore(); await w.vm.loadMore();
    expect(w.vm.tasks).toHaveLength(200); expect(list).toHaveBeenCalledTimes(4); expect(w.find('[data-test="bt-next-window"]').exists()).toBe(true);
    list.mockResolvedValueOnce({ tasks: [task({ id: '201' })], next_cursor: '' }); await w.vm.nextWindow(); expect(w.vm.tasks[0].id).toBe('201'); expect(w.vm.previousWindows).toEqual(['']);
    list.mockResolvedValueOnce(page(0)); await w.vm.previousWindow(); expect(w.vm.tasks).toHaveLength(50);
    service.ListBTTasks.mockRejectedValue(new Error('service_unavailable')); await w.vm.refresh(); expect(w.vm.tasks).toHaveLength(50); expect(w.vm.taskError).toContain('暂时不可用');
  });
  it('discards stale list snapshots after control and responses after hiding or unmounting', async () => {
    const stale = pending(), preview = pending(); const row = task({ stage: 'active', actions: ['stop'] });
    const service = api(); service.ListBTTasks.mockResolvedValueOnce({ tasks: [row], next_cursor: '' }).mockReturnValueOnce(stale.promise).mockResolvedValue({ tasks: [task({ ...row, revision: 3 })], next_cursor: '' }); service.ControlBTTask.mockResolvedValue(task({ ...row, revision: 3 })); service.PreviewBTResource.mockReturnValue(preview.promise);
    const { w } = await setup(service); const refresh = w.vm.refresh();
    const control = w.vm.control({ task_id: 't', expected_revision: 1, action: 'stop' }); await flushPromises(); stale.resolve({ tasks: [row], next_cursor: '' }); await control; await refresh; expect(w.vm.tasks[0].revision).toBe(3);
    const call = w.vm.preview('opaque'); await w.setProps({ active: false }); preview.resolve(task()); await call; expect(w.vm.draft).toBeNull();
    const get = pending(); service.GetBTTask.mockReturnValue(get.promise); await w.setProps({ active: true }); await flushPromises(); const detail = w.vm.showDetail('t'); w.unmount(); wrappers.splice(wrappers.indexOf(w), 1); get.resolve(row); await detail; expect(w.vm.detail).toBeNull();
  });
  it('polls only while active and clears timers on unmount', async () => {
    vi.useFakeTimers(); const { w, service } = await setup(); expect(vi.getTimerCount()).toBe(1);
    await vi.advanceTimersByTimeAsync(5000); expect(service.ListBTTasks).toHaveBeenCalledTimes(2);
    await w.setProps({ active: false }); expect(vi.getTimerCount()).toBe(0); await vi.advanceTimersByTimeAsync(10000); expect(service.ListBTTasks).toHaveBeenCalledTimes(2);
    await w.setProps({ active: true }); await flushPromises(); expect(vi.getTimerCount()).toBe(1); w.unmount(); wrappers.splice(wrappers.indexOf(w), 1); expect(vi.getTimerCount()).toBe(0);
  });
});

it('explains actual unconfigured and authentication service literals without leaking raw bodies', async () => {
 const { w, service } = await setup({ ListBTIndexers: vi.fn().mockRejectedValue('bt_connection_unavailable: private-url') });
 expect(w.text()).toContain('请先在连接设置中配置并启用'); expect(w.text()).not.toContain('private-url');
 service.SearchBTResources.mockRejectedValue('bt_auth_failed: private-key'); await w.vm.search({ query: '影片', indexer_ids: [] });
 expect(w.text()).toContain('连接认证失败'); expect(w.text()).not.toContain('private-key');
 service.SearchBTResources.mockRejectedValue('bt_credentials_unavailable: private-path'); await w.vm.search({ query: '影片', indexer_ids: [] });
 expect(w.text()).toContain('系统钥匙串不可用'); expect(w.text()).not.toContain('private-path');
});
it('shows uncertainty and historical write holds without describing every action as submission', async () => {
 const { w } = await setup({ ListBTTasks: vi.fn().mockResolvedValue({ tasks: [task({ stage: 'uncertain', error_code: 'write_history_unresolved', actions: [] }), task({ id: 'restart', stage: 'uncertain', error_code: 'restart_reconcile', actions: [] })], next_cursor: '' }) });
 expect(w.text()).toContain('操作结果待核实'); expect(w.text()).toContain('应用暂停后续操作'); expect(w.text()).toContain('需要核实上次操作结果，不会自动重发'); expect(w.text()).not.toContain('write_history_unresolved');
});

it('lets a failed cancellation close locally while retaining the task and blocking stale submission', async () => {
 const { w, service } = await setup({ ControlBTTask: vi.fn().mockRejectedValue('bt_service_unavailable: private') });
 await w.vm.preview('opaque');
 await w.get('[data-test="bt-files-close"]').trigger('click'); await flushPromises();
 expect(w.text()).toContain('未能确认取消，任务仍保留在列表中');
 expect(w.get('[data-test="bt-files-close"]').text()).toBe('关闭窗口');
 expect(w.get('[data-test="bt-files-submit"]').element.disabled).toBe(true);
 await w.vm.submit({ draft_id: 't', expected_revision: 1, selected_file_ids: ['f'] }); expect(service.SubmitBTDownload).not.toHaveBeenCalled();
 await w.get('[data-test="bt-files-close"]').trigger('click'); await flushPromises();
 expect(w.vm.draft).toBeNull(); expect(w.vm.tasks.some(t => t.id === 't')).toBe(true); expect(service.ControlBTTask).toHaveBeenCalledTimes(1);
 expect(w.emitted('changed')).toBeUndefined(); expect(w.find('[data-test="bt-open-selection"]').exists()).toBe(false);
 await w.vm.showDetail('t'); expect(service.GetBTTask).toHaveBeenCalledWith('t');
 await w.get('[data-test="bt-open-selection"]').trigger('click'); expect(w.vm.draft.id).toBe('t');
});

it('coalesces burst events and polling into one flight plus one follow-up round', async () => {
 const first = pending(), second = pending(); const { w, service } = await setup();
 service.ListBTTasks.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
 const run = w.vm.refresh(); for (let n = 0; n < 30; n++) { await w.setProps({ refreshKey: n + 1 }); w.vm.refresh(); }
 expect(service.ListBTTasks).toHaveBeenCalledTimes(2);
 first.resolve({ tasks: [task({ id: 'old' })], next_cursor: '' }); await flushPromises();
 expect(service.ListBTTasks).toHaveBeenCalledTimes(3);
 second.resolve({ tasks: [task({ id: 'fresh' })], next_cursor: '' }); await run;
 expect(service.ListBTTasks).toHaveBeenCalledTimes(3); expect(w.vm.tasks[0].id).toBe('fresh'); expect(w.vm.listBusy).toBe(false);
});
it('serializes pagination with event refreshes and preserves the loaded page', async () => {
 const { w, service } = await setup({ ListBTTasks: vi.fn().mockResolvedValue({ tasks: [task({ id: 'first' })], next_cursor: 'cursor' }) });
 const page = pending(); service.ListBTTasks.mockReturnValueOnce(page.promise).mockResolvedValue({ tasks: [task({ id: 'first' }), task({ id: 'second' })], next_cursor: '' });
 const loading = w.vm.loadMore(); const refresh = w.vm.refresh(); w.vm.refresh();
 expect(service.ListBTTasks).toHaveBeenCalledTimes(2); expect(service.ListBTTasks).toHaveBeenLastCalledWith('cursor', 50);
 page.resolve({ tasks: [task({ id: 'second' })], next_cursor: '' }); await loading; await refresh;
 expect(service.ListBTTasks).toHaveBeenCalledTimes(3); expect(w.vm.tasks.map(t => t.id)).toEqual(['first', 'second']); expect(w.vm.listBusy).toBe(false);
});
it('does not start queued detail, draft or follow-up reads after unmount', async () => {
 const { w, service } = await setup(); const list = pending(); service.ListBTTasks.mockReturnValueOnce(list.promise);
 const run = w.vm.refresh(); w.vm.showDetail('t'); w.vm.openSelection(task()); w.vm.refresh();
 w.unmount(); wrappers.splice(wrappers.indexOf(w), 1); list.resolve({ tasks: [], next_cursor: '' }); await run;
 expect(service.ListBTTasks).toHaveBeenCalledTimes(2); expect(service.GetBTTask).not.toHaveBeenCalled();
});
it('uses the server-selected video total for summaries with no file details', async () => {
 const { w } = await setup({ ListBTTasks: vi.fn().mockResolvedValue({ tasks: [task({ files: [], selected_video_count: 3, imported_count: 1 })], next_cursor: '' }) }); expect(w.text()).toContain('入库：1/3');
});

it('explains the restore gate at download-page entrypoints without exposing raw errors', async () => {
 const error = 'bt_restore_pending: private-location';
 const { w, service } = await setup({ ListBTIndexers: vi.fn().mockRejectedValue(error), ListBTTasks: vi.fn().mockRejectedValue(error) });
 const expected = '下载任务的恢复核实尚未完成，请稍后重试；如持续未完成，请重新打开应用。';
 expect(w.vm.connectionError).toBe(expected); expect(w.vm.taskError).toBe(expected);
 service.SearchBTResources.mockRejectedValue(error); await w.vm.search({ query: '影片', indexer_ids: [] }); expect(w.vm.searchError).toBe(expected);
 service.PreviewBTResource.mockRejectedValue(error); await w.vm.preview('opaque'); expect(w.vm.searchError).toBe(expected);
 expect(w.text()).not.toContain('private-location'); expect(w.text()).not.toContain('检查连接设置');
});
