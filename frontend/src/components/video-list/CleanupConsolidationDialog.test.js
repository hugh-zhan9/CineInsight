import { mount, flushPromises } from '@vue/test-utils';
import { beforeEach, afterEach, describe, it, expect, vi } from 'vitest';
const api = vi.hoisted(() => ({ PreviewCleanupConsolidation: vi.fn(), StartCleanupConsolidation: vi.fn(), GetCleanupConsolidationStatus: vi.fn(), CancelCleanupConsolidation: vi.fn(), SelectMigrationDestinationDirectory: vi.fn(), GetBackgroundTasks: vi.fn() }));
vi.mock('../../../wailsjs/go/main/App', () => api);
import Dialog from './CleanupConsolidationDialog.vue';
const request = { groups: [{ kind: 'near', member_ids: [1, 2], keeper_id: 1, selected_ids: [2], keeper_pinned: true }], protections: [] };
const item = id => ({ video_id: id, source_path: `/old/${id}.mp4`, destination_path: `/target/${id}（保留版）.mp4`, files: [{ kind: 'subtitle', source: { path: `/old/${id}.zh.srt` }, destination: `/target/${id}（保留版）.zh.srt`, copy_only: true }] });
const preview = (overrides = {}) => ({ preview_id: 'token', destination: '/target', items: [item(1)], warnings: ['未处理封面'], errors: [], groups: request.groups, ...overrides });
const status = (overrides = {}) => ({ id: 8, version: 1, status: 'running', completed: 0, total: 1, preview: preview(), items: [], ...overrides });
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };
let wrappers;
beforeEach(() => { vi.resetAllMocks(); wrappers = []; api.PreviewCleanupConsolidation.mockResolvedValue(preview()); api.GetCleanupConsolidationStatus.mockResolvedValue(status()); api.GetBackgroundTasks.mockResolvedValue([]); });
afterEach(() => wrappers.forEach(wrapper => wrapper.unmount()));
function mounted(props = {}) { const wrapper = mount(Dialog, { props: { request, ...props }, global: { stubs: { BaseModal: { template: '<div><slot /></div>' } } } }); wrappers.push(wrapper); return wrapper; }

describe('集中整理确认与后台回看', () => {
  it('显示精确落点、字幕复制/来源保留与提示；只提交当前令牌及显式扫描选择', async () => {
    const wrapper = mounted(); await wrapper.vm.open();
    expect(api.PreviewCleanupConsolidation).toHaveBeenCalledWith({ ...request, destination: '' });
    expect(wrapper.text()).toContain('（保留版）'); expect(wrapper.text()).toContain('复制，共享来源保留'); expect(wrapper.text()).toContain('字幕：');
    expect(wrapper.text()).toContain('未处理封面');
    expect(wrapper.text()).toContain('保留 1 个视频，实际移动 1 个');
    expect(wrapper.text()).toContain('后续待清理意向 1 个（本步不执行）');
    await wrapper.get('[data-test="consolidation-add-root"]').setValue(true);
    api.StartCleanupConsolidation.mockResolvedValue(status());
    await wrapper.get('[data-test="consolidation-start"]').trigger('click'); await flushPromises();
    expect(api.StartCleanupConsolidation).toHaveBeenCalledWith('token', true);
    expect(wrapper.emitted('review')).toBeUndefined();
  });
  it('目录与范围改变使旧预览失效，迟到响应不能恢复旧令牌', async () => {
    const old = deferred(); api.PreviewCleanupConsolidation.mockReturnValueOnce(old.promise);
    const wrapper = mounted(); const opening = wrapper.vm.open();
    await wrapper.vm.previewDestination('/new');
    old.resolve(preview({ preview_id: 'old' })); await opening;
    expect(wrapper.vm.preview.preview_id).toBe('token');
    await wrapper.setProps({ request: { ...request, groups: [] } });
    expect(wrapper.vm.preview).toBeNull(); expect(wrapper.vm.canStart).toBe(false);
    await wrapper.vm.start(); expect(api.StartCleanupConsolidation).not.toHaveBeenCalled();
  });
  it('关闭/重开忽略旧状态，切换历史任务后忽略原任务事件', async () => {
    const old = deferred(); api.GetCleanupConsolidationStatus.mockReturnValueOnce(old.promise).mockResolvedValueOnce(status({ id: 9 }));
    const wrapper = mounted(); const opening = wrapper.vm.open(8); wrapper.vm.close(); await wrapper.vm.open(9);
    old.resolve(status({ status: 'completed', version: 20 })); await opening;
    wrapper.vm.onProgress(status({ status: 'completed', version: 21 }));
    expect(wrapper.vm.summary.id).toBe(9); expect(wrapper.vm.summary.status).toBe('running');
  });
  it('防重复提交；终态事件先于Start响应仍由提交后一次读取对账', async () => {
    const start = deferred(); api.StartCleanupConsolidation.mockReturnValue(start.promise);
    api.GetCleanupConsolidationStatus.mockResolvedValue(status({ status: 'completed', version: 12, completed: 1 }));
    const wrapper = mounted(); await wrapper.vm.open();
    const pending = wrapper.vm.start(); await wrapper.vm.start();
    wrapper.vm.onProgress(status({ status: 'completed', version: 12, completed: 1 }));
    start.resolve(status()); await pending;
    expect(api.StartCleanupConsolidation).toHaveBeenCalledTimes(1);
    expect(api.GetCleanupConsolidationStatus).toHaveBeenCalledTimes(1);
    expect(wrapper.vm.summary.status).toBe('completed');
    expect(wrapper.emitted('moved')).toHaveLength(1); expect(wrapper.emitted('review')).toBeUndefined();
    await wrapper.get('[data-test="consolidation-review"]').trigger('click'); expect(wrapper.emitted('review')).toEqual([[8]]);
  });
  it('10k文件只渲染40项；运行进度不读完整详情，旧version被忽略；关闭后台仍通知完成', async () => {
    api.GetCleanupConsolidationStatus.mockResolvedValue(status({ preview: preview({ items: Array.from({ length: 10000 }, (_, i) => item(i + 1)) }) }));
    const wrapper = mounted(); await wrapper.vm.open(8);
    expect(wrapper.findAll('[data-test="consolidation-item"]')).toHaveLength(40);
    for (let version = 2; version < 40; version++) wrapper.vm.onProgress({ id: 8, version, status: 'running', total: 10000, completed: version });
    wrapper.vm.onProgress({ id: 8, version: 2, status: 'failed' });
    expect(wrapper.vm.summary.status).toBe('running'); expect(api.GetCleanupConsolidationStatus).toHaveBeenCalledTimes(1);
    wrapper.vm.close(); wrapper.vm.onProgress({ id: 8, version: 40, status: 'completed', completed: 10000 });
    expect(api.GetCleanupConsolidationStatus).toHaveBeenCalledTimes(1); expect(wrapper.emitted('moved')).toHaveLength(1);
  });
  it.each(['failed', 'cancelled', 'interrupted'])('%s只显示部分结果和保留路径，不提供清理入口', async state => {
    api.GetCleanupConsolidationStatus.mockResolvedValue(status({ status: state, completed: 1, items: [{ video_id: 1, phase: 'committed', retained_paths: ['/retained/file.mp4'] }] }));
    const wrapper = mounted(); await wrapper.vm.open(8);
    expect(wrapper.text()).toContain('/retained/file.mp4'); expect(wrapper.text()).toContain('路径已更新');
    expect(wrapper.find('[data-test="consolidation-review"]').exists()).toBe(false);
    wrapper.vm.review(); expect(wrapper.emitted('review')).toBeUndefined();
  });
  it('外部运行任务不可取消；本机登记运行时才允许取消，关闭不取消', async () => {
    const wrapper = mounted(); await wrapper.vm.open(8); await flushPromises();
    expect(wrapper.find('[data-test="consolidation-cancel"]').exists()).toBe(false); await wrapper.vm.cancel(); expect(api.CancelCleanupConsolidation).not.toHaveBeenCalled();
    api.GetBackgroundTasks.mockResolvedValue(['cleanup_consolidation']); await wrapper.vm.open(8); await flushPromises();
    await wrapper.get('[data-test="consolidation-cancel"]').trigger('click'); expect(api.CancelCleanupConsolidation).toHaveBeenCalledTimes(1);
    wrapper.vm.close(); expect(api.CancelCleanupConsolidation).toHaveBeenCalledTimes(1);
  });
  it('空详情与读取失败可见；阻塞预览不能启动', async () => {
    const wrapper = mounted(); api.GetCleanupConsolidationStatus.mockResolvedValue(null); await wrapper.vm.open(8);
    expect(wrapper.text()).toContain('没有找到');
    api.GetCleanupConsolidationStatus.mockRejectedValue(new Error('磁盘断开')); await wrapper.vm.loadTask(8); expect(wrapper.text()).toContain('磁盘断开');
    api.PreviewCleanupConsolidation.mockResolvedValue(preview({ preview_id: '', errors: ['空间不足'] })); await wrapper.vm.open();
    expect(wrapper.text()).toContain('空间不足'); expect(wrapper.vm.canStart).toBe(false);
  });
});
