// libraryTaskStateMixin（D-P3-05 收尾）：片库页的后台任务状态条删掉以后，「管理」菜单的进度文案与「本地资料补全完成后
// 重载片库」由它承担。原 BackgroundTaskStatusBars.test.js 里属于这两件事的用例搬到这里（启动、事件、镜像、重载），
// 状态条自己的显示（准备中、无需补全、复用 / 移出范围、失败明细、等待空闲与立即运行、取消）在任务中心，见 TaskCenterDrawer.test.js。
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const feedback = vi.hoisted(() => ({
  confirmAction: vi.fn(() => Promise.resolve(true)),
  notify: vi.fn(),
  notifyError: vi.fn()
}));
vi.mock('../../utils/feedback.js', async (importOriginal) => ({ ...(await importOriginal()), ...feedback }));

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../../wailsjs/go/main/App', () => api);

import { LIBRARY_TASKS, libraryTaskStateMixin, taskStartNotice } from './libraryTasks.js';
import { runtimeEventsMixin } from './runtimeEvents.js';

const STATUS_BINDINGS = ['GetTechnicalBackfillStatus', 'GetPerceptualHashBackfillStatus', 'GetLocalMetadataBackfillStatus', 'GetLocalMetadataExportStatus'];
let handlers;

beforeEach(() => {
  vi.clearAllMocks();
  handlers = new Map();
  window.runtime = { EventsOn: vi.fn((name, handler) => { handlers.set(name, handler); return () => handlers.delete(name); }) };
  for (const name of STATUS_BINDINGS) api[name].mockResolvedValue({ running: false, completed: false, total: 0, processed: 0 });
  feedback.confirmAction.mockResolvedValue(true);
});

const reload = vi.fn();
const Host = {
  mixins: [runtimeEventsMixin, libraryTaskStateMixin],
  emits: ['open-task-center'],
  methods: { reloadCurrentView: reload },
  template: '<div />'
};

async function mountHost() {
  const wrapper = mount(Host);
  await flushPromises();
  return wrapper;
}

describe('libraryTaskStateMixin', () => {
  it('挂载时读一次四个任务的状态，给「管理」菜单的进度文案用', async () => {
    api.GetTechnicalBackfillStatus.mockResolvedValueOnce({ running: true, total: 9, processed: 2 });
    api.GetLocalMetadataExportStatus.mockResolvedValueOnce({ running: true, total: 4, processed: 1 });
    const wrapper = await mountHost();
    for (const name of STATUS_BINDINGS) expect(api[name]).toHaveBeenCalledOnce();
    expect(wrapper.vm.technicalBackfill).toEqual(expect.objectContaining({ running: true, total: 9, processed: 2 }));
    expect(wrapper.vm.localMetadataExport).toEqual(expect.objectContaining({ running: true, total: 4, processed: 1 }));
    expect(wrapper.vm.perceptualHash.running).toBe(false);
    wrapper.unmount();
  });

  it('跟着各任务既有的运行时事件走，卸载时注销', async () => {
    const wrapper = await mountHost();
    expect([...handlers.keys()].sort()).toEqual(Object.values(LIBRARY_TASKS).map(task => task.event).sort());
    handlers.get('perceptual-hash-state')({ running: true, total: 9, processed: 3 });
    handlers.get('local-metadata-export')({ running: true, total: 5, processed: 4 });
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.perceptualHash).toEqual(expect.objectContaining({ running: true, total: 9, processed: 3 }));
    expect(wrapper.vm.localMetadataExport).toEqual(expect.objectContaining({ running: true, total: 5, processed: 4 }));
    wrapper.unmount();
    expect(handlers.size).toBe(0);
  });

  it('本地资料补全跑完且有成功项时按当前条件重载片库（原状态条的 library-changed）', async () => {
    const wrapper = await mountHost();
    handlers.get('local-metadata-backfill')({ running: true, total: 3, processed: 1 });
    handlers.get('local-metadata-backfill')({ running: false, completed: true, succeeded: 0 });
    expect(reload).not.toHaveBeenCalled();
    handlers.get('local-metadata-backfill')({ running: false, completed: true, succeeded: 2 });
    expect(reload).toHaveBeenCalledOnce();
    // 别的任务跑完不重载。
    handlers.get('technical-backfill-state')({ running: false, completed: true, succeeded: 5 });
    expect(reload).toHaveBeenCalledOnce();
    wrapper.unmount();
  });

  it('启动补全：并入返回的状态，提示「已开始」并给「查看」（打开任务中心）', async () => {
    const wrapper = await mountHost();
    api.StartTechnicalBackfill.mockResolvedValueOnce({ running: true, preparing: true, total: 0 });
    await wrapper.vm.startTechnicalBackfill();
    expect(api.StartTechnicalBackfill).toHaveBeenCalledOnce();
    expect(wrapper.vm.technicalBackfill).toEqual(expect.objectContaining({ running: true, preparing: true }));
    const [text, options] = feedback.notify.mock.calls.at(-1);
    expect(text).toBe('已开始补全技术信息，进度与结果在任务中心。');
    expect(options.action.label).toBe('查看');
    options.action.run();
    expect(wrapper.emitted('open-task-center')).toHaveLength(1);

    api.StartPerceptualHashBackfill.mockRejectedValueOnce(new Error('数据库繁忙'));
    await wrapper.vm.startPerceptualHashBackfill();
    expect(feedback.notifyError).toHaveBeenCalledWith('启动近重复指纹补全失败: Error: 数据库繁忙');
    wrapper.unmount();
  });

  it('当前筛选写出 NFO：先确认，把宿主算好的筛选整份交出去；取消确认什么也不发', async () => {
    const wrapper = await mountHost();
    feedback.confirmAction.mockResolvedValueOnce(false);
    await wrapper.vm.startLocalMetadataExport({ keyword: '导演剪辑版' });
    expect(api.StartLocalMetadataExport).not.toHaveBeenCalled();

    api.StartLocalMetadataExport.mockResolvedValueOnce({ running: true, total: 3, processed: 0 });
    await wrapper.vm.startLocalMetadataExport({ keyword: '导演剪辑版', tag_ids: [7] });
    expect(api.StartLocalMetadataExport).toHaveBeenCalledWith({ filter: { keyword: '导演剪辑版', tag_ids: [7] } });
    expect(wrapper.vm.localMetadataExport).toEqual(expect.objectContaining({ running: true, total: 3 }));
    expect(feedback.notify.mock.calls.at(-1)[0]).toBe('已开始写出 NFO，进度与结果在任务中心。');
    wrapper.unmount();
  });

  it('单个视频写出 NFO：结果直接提示，失败给中文错误', async () => {
    const wrapper = await mountHost();
    api.ExportLocalMetadataNFO.mockResolvedValueOnce({ nfo_path: 'a.nfo', warnings: ['海报没写'] });
    await wrapper.vm.exportLocalMetadataNFO({ id: 3, name: 'a.mp4' });
    expect(feedback.notify).toHaveBeenLastCalledWith('NFO 已写出：a.nfo\n海报没写');
    api.ExportLocalMetadataNFO.mockRejectedValueOnce('磁盘只读');
    await wrapper.vm.exportLocalMetadataNFO({ id: 3, name: 'a.mp4' });
    expect(feedback.notifyError).toHaveBeenLastCalledWith('写出 NFO 失败: 磁盘只读');
    wrapper.unmount();
  });

  it('启动提示：在跑说「已开始」；一开始就没有要处理的说「已是最新」；其余不提示', () => {
    expect(taskStartNotice('补全技术信息', { running: true })).toEqual({ text: '已开始补全技术信息，进度与结果在任务中心。', view: true });
    expect(taskStartNotice('补全技术信息', { running: false, completed: true, total: 0, failed: 0 })).toEqual({ text: '补全技术信息：无需处理（已是最新状态）。', view: false });
    expect(taskStartNotice('补全技术信息', { running: false, completed: true, total: 4 })).toBeNull();
    expect(taskStartNotice('补全技术信息', null)).toBeNull();
  });
});
