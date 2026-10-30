import { mount } from '@vue/test-utils';
import { describe, expect, it, vi } from 'vitest';
import BTConnectionPanel from './BTConnectionPanel.vue';
import BTResourceSearch from './BTResourceSearch.vue';
import BTFileSelectionDialog from './BTFileSelectionDialog.vue';
import BTTaskList from './BTTaskList.vue';
import { btBytes, btSelectedBytes } from '../../utils/btFormat.js';
const global = { stubs: { teleport: true, DialogShell: { template: '<div><slot/><slot name="footer"/></div>' } } };
const config = { revision: 2, prowlarr_base: 'http://localhost:9696', qb_base: 'http://localhost:8081', qb_username: 'u', download_root: '/movies', enabled: true, prowlarr_key_configured: true };
const files = [{ id: 'one', relative_path: 'a.mkv', size: '9007199254740993', selectable: true, selected: true }, { id: 'two', relative_path: 'b.srt', size: '7', selectable: true }, { id: 'pad', relative_path: '', size: '9', padding: true, selectable: false }];
const draft = () => ({ draft_id: 'd', revision: 4, title: 'film', files, save_root: '/movies/CineInsight/d', preparation: 'ready', expires_at: '2099-01-01T00:00:00Z' });
describe('BT connection configuration', () => {
 it('emits explicit secret operation, clears only after success, never imports returned secrets', async () => {
  const w = mount(BTConnectionPanel, { props: { connection: { ...config, prowlarr_key: 'must-not-show' } } });
  expect(w.html()).not.toContain('must-not-show');
  await w.get('[data-test="bt-prowlarr_key-op"]').setValue('replace');
  await w.get('[data-test="bt-prowlarr_key-value"]').setValue('private');
  await w.get('form').trigger('submit');
  expect(w.emitted('save')[0][0]).toMatchObject({ expected_revision: 2, prowlarr_key: { operation: 'replace', value: 'private' }, qb_password: { operation: 'keep', value: '' } });
  await w.setProps({ error: '保存失败' });
  expect(w.get('[data-test="bt-prowlarr_key-value"]').element.value).toBe('private');
  await w.setProps({ connection: { ...config } });
  expect(w.get('[data-test="bt-prowlarr_key-value"]').element.value).toBe('private');
  await w.setProps({ saveSucceeded: 1, error: '' });
  expect(w.find('[data-test="bt-prowlarr_key-value"]').exists()).toBe(false);
  expect(w.vm.secrets.prowlarr_key.value).toBe('');
  expect(w.text()).toContain('测试结果单独显示');
  w.unmount();
 });
 it('clear is explicit, readonly and busy cannot save; test does not save', async () => {
  const w = mount(BTConnectionPanel, { props: { connection: config } });
  await w.get('[data-test="bt-qb_password-op"]').setValue('clear');
  await w.get('form').trigger('submit');
  expect(w.emitted('save')[0][0].qb_password).toEqual({ operation: 'clear', value: '' });
  await w.get('[data-test="bt-test-connection"]').trigger('click');
  expect(w.emitted('test')).toHaveLength(1);
  await w.setProps({ readonly: true }); await w.get('form').trigger('submit');
  expect(w.emitted('save')).toHaveLength(1); w.unmount();
 });
});
describe('BT resource search', () => {
 it('only emits opaque result token and prevents stale query results being selected', async () => {
  const result = { resources: [{ result_token: 'opaque', title: '<script>bad</script>', size: '42', seeders: 2, indexer: 'site', downloadUrl: 'private' }], indexers: [{ indexer_id: 1, status: 'ok', coverage: 'unknown' }], truncated: true };
  const w = mount(BTResourceSearch, { props: { initialQuery: 'film', resultQuery: 'film', indexers: [{ id: 1, name: 'site', enabled: true }], result } });
  expect(w.html()).not.toContain('private'); expect(w.find('script').exists()).toBe(false);
  expect(w.text()).toContain('结果可能不完整');
  await w.get('[data-test="bt-preview-opaque"]').trigger('click'); expect(w.emitted('preview')[0]).toEqual(['opaque']);
  await w.get('[data-test="bt-query"]').setValue('new');
  expect(w.find('[data-test="bt-preview-opaque"]').exists()).toBe(false);
  await w.get('form').trigger('submit'); expect(w.emitted('search')[0]).toEqual([{ query: 'new', indexer_ids: [] }]); w.unmount();
 });
 it('empty and station failure remain visible, busy search cannot submit', async () => {
  const w = mount(BTResourceSearch, { props: { resultQuery: 'x', initialQuery: 'x', result: { resources: [], indexers: [{ indexer_id: 1, status: 'failed' }] }, busy: true } });
  expect(w.text()).toContain('搜索失败'); expect(w.text()).toContain('本轮未返回资源');
  await w.get('form').trigger('submit'); expect(w.emitted('search')).toBeUndefined(); w.unmount();
 });
});
describe('BT file selection', () => {
 it('exactly totals large integers, excludes padding, emits a frozen selected-ID snapshot', async () => {
  const w = mount(BTFileSelectionDialog, { global, props: { visible: true, draft: draft() } });
  expect(w.get('[data-test="bt-files-total"]').text()).toContain('9,007,199,254,740,993');
  await w.get('[data-test="bt-files-all"]').trigger('click');
  expect(w.get('[data-test="bt-files-total"]').text()).toContain('9,007,199,254,741,000');
  expect(w.get('[data-test="bt-file-pad"]').element.disabled).toBe(true);
  await w.get('[data-test="bt-files-submit"]').trigger('click');
  expect(w.emitted('submit')[0][0]).toEqual({ draft_id: 'd', expected_revision: 4, selected_file_ids: ['one', 'two'] });
  await w.get('[data-test="bt-files-none"]').trigger('click');
  expect(w.get('[data-test="bt-files-submit"]').element.disabled).toBe(true);
  expect(w.emitted('submit')[0][0].selected_file_ids).toEqual(['one', 'two']); w.unmount();
 });
 it('new draft revision resets selection, unready/read-only/empty cannot submit', async () => {
  const w = mount(BTFileSelectionDialog, { global, props: { visible: true, draft: draft() } });
  await w.get('[data-test="bt-files-all"]').trigger('click');
  await w.setProps({ draft: { ...draft(), revision: 5, files: [{ ...files[1], selected: false }] } });
  expect(w.vm.selected).toEqual([]);
  await w.setProps({ draft: { ...draft(), preparation: 'metadata_pending' } });
  expect(w.text()).toContain('尚未开始下载影片'); expect(w.get('[data-test="bt-files-submit"]').element.disabled).toBe(true);
  await w.setProps({ draft: draft(), readonly: true }); expect(w.get('[data-test="bt-files-submit"]').element.disabled).toBe(true);
  await w.setProps({ draft: { ...draft(), files: [] }, readonly: false }); expect(w.get('[data-test="bt-files-submit"]').element.disabled).toBe(true); w.unmount();
 });
 it('paginates large manifests but selects all pages without losing choices on identical refresh', async () => {
  const many = Array.from({ length: 201 }, (_, i) => ({ id: String(i), path: `${i}.mkv`, size: '1', selectable: true }));
  const w = mount(BTFileSelectionDialog, { global, props: { visible: true, draft: { ...draft(), files: many } } });
  expect(w.findAll('input[type=checkbox]')).toHaveLength(200);
  await w.get('[data-test="bt-files-all"]').trigger('click'); expect(w.vm.selected).toHaveLength(201);
  await w.setProps({ draft: { ...draft(), files: many.map(f => ({ ...f })) } }); expect(w.vm.selected).toHaveLength(201);
  await w.get('[data-test="bt-files-next"]').trigger('click'); expect(w.findAll('input[type=checkbox]')).toHaveLength(1);
  w.unmount();
 });
 it('blocks expired or missing deadlines on open and expires while visible, clearing timers when hidden/unmounted', async () => {
  vi.useFakeTimers(); vi.setSystemTime(new Date('2026-10-30T00:00:00Z'));
  let w;
  try {
   w = mount(BTFileSelectionDialog, { global, props: { visible: true, draft: { ...draft(), expires_at: '2026-10-29T23:59:59Z' } } });
   expect(w.get('[data-test="bt-files-expired"]').text()).toContain('已过期');
   expect(w.get('[data-test="bt-files-submit"]').element.disabled).toBe(true);
   await w.setProps({ draft: { ...draft(), expires_at: null } });
   expect(w.get('[data-test="bt-files-submit"]').element.disabled).toBe(true);
   await w.setProps({ draft: { ...draft(), expires_at: '2026-10-30T00:00:01Z' } });
   expect(w.get('[data-test="bt-files-submit"]').element.disabled).toBe(false);
   expect(vi.getTimerCount()).toBe(1);
   await vi.advanceTimersByTimeAsync(1000);
   expect(w.get('[data-test="bt-files-submit"]').element.disabled).toBe(true);
   await w.setProps({ draft: { ...draft(), expires_at: '2026-10-30T00:00:10Z' } });
   expect(vi.getTimerCount()).toBe(1);
   await w.setProps({ visible: false }); expect(vi.getTimerCount()).toBe(0);
   await w.setProps({ visible: true }); expect(vi.getTimerCount()).toBe(1);
   w.unmount(); w = null; expect(vi.getTimerCount()).toBe(0);
  } finally { w?.unmount(); vi.useRealTimers(); }
 });
 it('rechecks wall clock on submit before a delayed timer can run', () => {
  vi.useFakeTimers(); vi.setSystemTime(new Date('2026-10-30T00:00:00Z'));
  const w = mount(BTFileSelectionDialog, { global, props: { visible: true, draft: { ...draft(), expires_at: '2026-10-30T00:00:01Z' } } });
  try {
   expect(w.get('[data-test="bt-files-submit"]').element.disabled).toBe(false);
   vi.setSystemTime(new Date('2026-10-30T00:00:02Z'));
   w.vm.submit(); expect(w.emitted('submit')).toBeUndefined();
  } finally { w.unmount(); vi.useRealTimers(); }
 });
 it('rejects unsafe number sizes instead of silently rounding', () => {
  expect(btBytes(Number.MAX_SAFE_INTEGER + 1)).toBeNull();
  expect(btSelectedBytes([{ id: 'x', size: Number.MAX_SAFE_INTEGER + 1 }], ['x'])).toBeNull();
  expect(btBytes('-1')).toBeNull(); expect(btBytes('1.2')).toBeNull();
 });
});
describe('BT task actions', () => {
 const task = () => ({ id: 't', revision: 3, title: 'film', stage: 'uncertain', progress: 0.2, remote_state: 'stoppedDL', imported_count: 0, selected_video_count: 1, allowed_actions: ['reconcile', 'reconfirm_submit', 'abandon', 'deleteFiles'] });
 it('uses server capabilities; risky resubmission requires confirmation and current revision', async () => {
  const w = mount(BTTaskList, { global, props: { tasks: [task()] } });
  expect(w.find('[data-test="bt-task-t-deleteFiles"]').exists()).toBe(false);
  await w.get('[data-test="bt-task-t-reconcile"]').trigger('click');
  expect(w.emitted('control')[0][0]).toEqual({ task_id: 't', expected_revision: 3, action: 'reconcile' });
  await w.get('[data-test="bt-task-t-reconfirm_submit"]').trigger('click');
  expect(w.text()).toContain('可能与迟到的原请求重复'); expect(w.emitted('control')).toHaveLength(1);
  await w.setProps({ tasks: [{ ...task(), revision: 4 }] });
  expect(w.get('[data-test="bt-task-confirm"]').element.disabled).toBe(true); w.unmount();
 });
 it('shows per-file partial failures without exposing arbitrary codes or upstream bodies', () => {
  const w = mount(BTTaskList, { global, props: { tasks: [task()], detail: { id: 't', files: [
   { id: 'ok', path: 'a.mkv', selected: true, import_state: 'imported', progress: 1 },
   { id: 'message', path: 'b.mkv', selected: true, import_state: 'failed', error_message: '磁盘暂时不可访问' },
   { id: 'code', path: 'c.mkv', selected: true, import_state: 'failed', error_code: 'path_not_in_library' },
   { id: 'unknown', path: 'd.mkv', import_state: 'failed', error_code: 'private-technical-code', upstream_body: 'secret-body' }
  ] } } });
  expect(w.get('[data-test="bt-file-error-message"]').text()).toBe('磁盘暂时不可访问');
  expect(w.get('[data-test="bt-file-error-code"]').text()).toContain('扫描范围');
  expect(w.get('[data-test="bt-file-error-unknown"]').text()).toContain('重新核实');
  expect(w.text()).not.toContain('private-technical-code'); expect(w.text()).not.toContain('secret-body');
  expect(w.find('[data-test="bt-file-error-ok"]').exists()).toBe(false); w.unmount();
 });
 it('confirmed action carries explicit confirmation; readonly does not act; detail and playback are host events', async () => {
  const w = mount(BTTaskList, { global, props: { tasks: [task()], detail: { id: 't', files: [{ id: 'f', selected: true, path: 'a.mkv', import_state: 'imported', video_id: 7 }] } } });
  await w.get('[data-test="bt-task-t-abandon"]').trigger('click'); await w.get('[data-test="bt-task-confirm"]').trigger('click');
  expect(w.emitted('control')[0][0]).toMatchObject({ action: 'abandon', confirmed: true });
  await w.findAll('button').find(b => b.text() === '播放').trigger('click'); expect(w.emitted('play')[0]).toEqual([7]);
  await w.setProps({ readonly: true }); await w.get('[data-test="bt-task-t-reconcile"]').trigger('click'); expect(w.emitted('control')).toHaveLength(1); w.unmount();
 });
});

it('retired connections remain read-only with a separate explanation and abandonment admits late effects', async () => {
 const w = mount(BTTaskList, { global, props: { tasks: [{ id: 'old', revision: 1, read_only_reason: 'connection_retired', allowed_actions: [], error_message: '文件需要核实' }] } });
 expect(w.get('[data-test="bt-retired-connection"]').text()).toBe('这是旧连接的任务，仅供查看');
 expect(w.text()).toContain('文件需要核实'); expect(w.find('[data-test="bt-task-old-start"]').exists()).toBe(false);
 await w.setProps({ tasks: [{ id: 't', revision: 1, stage: 'uncertain', allowed_actions: ['abandon'] }] });
 await w.get('[data-test="bt-task-t-abandon"]').trigger('click');
 expect(w.text()).toContain('已发送的操作可能稍后生效'); expect(w.text()).not.toContain('不会自动启动'); w.unmount();
});

it.each([
 ['stop', '停止该任务'],
 ['metadata', '准备文件清单（不会启动影片下载）'],
 ['add', '继续下载已选文件'],
 ['priority_zero', '继续下载已选文件'],
 ['priority_select', '继续下载已选文件'],
 ['start', '继续下载已选文件'],
 ['cancel_stop', '取消清单准备且保留文件'],
 ['cancel_delete', '取消清单准备且保留文件'],
 ['restore', '核实恢复记录并在需要时重新选择文件'],
])('reconfirmation explains the concrete %s operation', async (action, description) => {
 const row = { id: 't', revision: 3, stage: 'uncertain', pending_action: action, allowed_actions: ['reconfirm_submit'] };
 const w = mount(BTTaskList, { global, props: { tasks: [row] } });
 await w.get('[data-test="bt-task-t-reconfirm_submit"]').trigger('click');
 expect(w.text()).toContain(description); expect(w.text()).toContain('先核实下载器状态'); expect(w.text()).toContain('可能与迟到的原请求重复');
 expect(w.text()).not.toContain(`pending_action`);
 await w.setProps({ tasks: [{ ...row, revision: 4 }] }); expect(w.get('[data-test="bt-task-confirm"]').element.disabled).toBe(true); w.unmount();
});
