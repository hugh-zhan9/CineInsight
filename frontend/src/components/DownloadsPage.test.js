vi.mock('../utils/btAPI.js', () => ({ btAPI: { GetBTConnection: async () => ({ enabled: false, credential_status: 'unconfigured' }), ListBTIndexers: async () => [], ListBTTasks: async () => ({ tasks: [], next_cursor: '' }) }, btConnectionError: () => '连接失败' }));
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

// 按需生成的绑定替身：查重提示用到的删除宿主（TrashUndoBanner）还会引入其他绑定。
const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../wailsjs/go/main/App', () => api);
const feedback = vi.hoisted(() => ({ confirmAction: vi.fn(() => Promise.resolve(true)), notify: vi.fn(), notifyError: vi.fn(), notifySuccess: vi.fn() }));
vi.mock('../utils/feedback.js', async (importOriginal) => ({ ...(await importOriginal()), ...feedback }));
const compare = vi.hoisted(() => ({ openVersionCompare: vi.fn(() => true) }));
vi.mock('../utils/versionCompare.js', async (importOriginal) => ({ ...(await importOriginal()), ...compare }));

import DownloadsPage from './DownloadsPage.vue';

async function mountPage(tasks = []) {
  // 下载页改读 ListDownloadTasks（本次会话 + 表里的历史，D-PC21）；旧绑定由 P-040 删除。
  api.ListDownloadTasks.mockResolvedValue(tasks);
  const wrapper = mount(DownloadsPage);
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  delete window.runtime;
});

describe('下载任务页', () => {
  it('没有任务时给出去哪里产生任务的说明', async () => {
    const wrapper = await mountPage([]);
    expect(wrapper.get('[data-test="downloads-empty"]').text()).toContain('发送到 CineInsight');
    wrapper.unmount();
  });

  it('区分下载失败与入库失败', async () => {
    const wrapper = await mountPage([
      { id: 'a', filename: '甲.mp4', state: 'failed', error: 'ffmpeg 失败：取不到分片' },
      { id: 'b', filename: '乙.mp4', state: 'done', import_error: '下载目录不在片库扫描目录里' }
    ]);
    const rows = wrapper.findAll('[data-test="download-item"]');
    expect(rows).toHaveLength(2);
    expect(rows[0].text()).toContain('取不到分片');
    // 文件已经在盘上了，不能说成下载失败
    expect(rows[1].text()).toContain('文件已保存，但没有入库');
    wrapper.unmount();
  });

  it('运行中的任务可以取消，已完成与入库中的不给取消按钮', async () => {
    api.CancelBrowserDownloadTask.mockResolvedValue(undefined);
    const wrapper = await mountPage([
      { id: 'running-1', filename: '丙.mp4', state: 'running' },
      { id: 'done-1', filename: '丁.mp4', state: 'done' },
      // 入库那一步是扫描，不接受取消
      { id: 'importing-1', filename: '戊.mp4', state: 'importing' }
    ]);

    // 已完成的行现在有「在访达中显示」等动作按钮（D-PC25），这里只数取消按钮。
    const buttons = wrapper.findAll('[data-test="download-item"] button').filter(button => button.text() === '取消');
    expect(buttons).toHaveLength(1);
    await buttons[0].trigger('click');
    await flushPromises();
    expect(api.CancelBrowserDownloadTask).toHaveBeenCalledWith('running-1');
    wrapper.unmount();
  });

  // 后端从不发出 remuxing（MEDIA-15），前端这条分支删掉了：认不出的状态原样显示，
  // 不再被说成「转封装 / 不重新编码」。
  it('MEDIA-15 不再有 remuxing 分支，未入库原因只说一句', async () => {
    const wrapper = await mountPage([
      { id: 'r', filename: '甲.mp4', state: 'remuxing', bytes_written: 1024 },
      { id: 'd', filename: '乙.mp4', state: 'done', import_status: 'not_in_scan_roots', import_error: '下载目录不在片库扫描目录里' }
    ]);
    expect(wrapper.text()).not.toContain('转封装');
    expect(wrapper.text()).not.toContain('不重新编码');
    expect(wrapper.get('[data-test="downloads-summary"]').text()).not.toContain('含转封装');
    const row = wrapper.findAll('[data-test="download-item"]').find(item => item.text().includes('乙.mp4'));
    const warning = row.get('[data-test="download-import-warning"]').text();
    expect(warning).toBe('文件已保存，但没有入库：下载目录不在片库扫描目录里');
    expect(row.text().split('下载目录不在片库扫描目录里')).toHaveLength(2);
    wrapper.unmount();
  });

  it('概览按状态分别计数，入库失败单独点出来', async () => {
    const wrapper = await mountPage([
      { id: 'a', filename: '甲.mp4', state: 'running' },
      { id: 'b', filename: '乙.mp4', state: 'queued' },
      { id: 'c', filename: '丙.mp4', state: 'done' },
      { id: 'd', filename: '丁.mp4', state: 'done', import_error: '目录不在扫描范围' },
      { id: 'e', filename: '戊.mp4', state: 'failed', error: 'ffmpeg 失败' }
    ]);
    const summary = wrapper.get('[data-test="downloads-summary"]').text();
    expect(summary).toContain('进行中');
    expect(summary).toContain('1 个没入库');
    wrapper.unmount();
  });

  // 这条是这次改动的要点：进度靠后端推事件，不该再要用户手动刷新。
  it('后端推来的任务快照直接刷新界面，不需要手动刷新', async () => {
    const handlers = {};
    window.runtime = { EventsOn: (name, fn) => { handlers[name] = fn; return () => {}; } };
    const wrapper = await mountPage([{ id: 'a', filename: '甲.mp4', state: 'queued' }]);

    expect(wrapper.text()).toContain('排队中');
    handlers['browser-download-tasks']([
      { id: 'a', filename: '甲.mp4', state: 'running', processed_seconds: 65, bytes_written: 2048 }
    ]);
    await flushPromises();

    const row = wrapper.get('[data-test="download-item"]');
    expect(row.text()).toContain('下载中');
    expect(row.text()).toContain('1:05');
    // 界面没有再去查一次后端——刷新完全靠推送
    expect(api.ListDownloadTasks).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });
});

describe('下载完成后的入库出口与重试（D-PC25 / D-PC21）', () => {
  const notInScan = (extra = {}) => ({
    id: 't1', filename: '甲.mp4', state: 'done', video_id: 0,
    import_status: 'not_in_scan_roots', import_error: '下载目录不在片库扫描目录里', ...extra
  });
  const action = (wrapper, key) => wrapper.get(`[data-test="download-action-${key}"]`);

  it('MEDIA-09 没入库的任务给出加入扫描目录、重新入库、在访达中显示三个出口', async () => {
    const wrapper = await mountPage([notInScan()]);
    const labels = wrapper.findAll('[data-test="download-item"] .download-row__actions button').map(button => button.text());
    expect(labels).toEqual(['把下载目录加入扫描目录', '重新入库', '在访达中显示']);

    api.AddDownloadDirectoryToScan.mockResolvedValue({ code: 'ok', task: notInScan({ import_status: 'imported', import_error: '', video_id: 9 }) });
    await action(wrapper, 'add-directory').trigger('click');
    await flushPromises();
    expect(api.AddDownloadDirectoryToScan).toHaveBeenCalledWith('t1');
    expect(wrapper.get('[data-test="download-action-notice"]').text()).toContain('已把下载目录加入扫描目录');
    // 带回来的任务快照就地换上：已入库之后不再显示「没有入库」与入库按钮。
    expect(wrapper.find('[data-test="download-import-warning"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="download-action-add-directory"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('MEDIA-09 加入扫描目录的三个新结果码各有说法，不当成普通失败', async () => {
    const cases = [
      ['directory_excluded', '黑名单', 'download-row__notice--warn'],
      ['directory_nested', '互相嵌套', 'download-row__notice--warn'],
      ['directory_missing', '已不存在', 'download-row__notice--error']
    ];
    for (const [code, phrase, level] of cases) {
      // 后端没带 message 时用前端的兜底说法，仍能区分三种情况。
      api.AddDownloadDirectoryToScan.mockResolvedValueOnce({ code });
      const wrapper = await mountPage([notInScan()]);
      await action(wrapper, 'add-directory').trigger('click');
      await flushPromises();
      const notice = wrapper.get('[data-test="download-action-notice"]');
      expect(notice.text(), code).toContain(phrase);
      expect(notice.classes(), code).toContain(level);
      wrapper.unmount();
    }
  });

  it('MEDIA-09 重新入库仍失败时不把原因再说一遍，在访达中显示走 RevealDownload', async () => {
    const wrapper = await mountPage([notInScan()]);
    api.ReimportDownload.mockResolvedValue({ code: 'not_in_scan_roots', message: '下载目录不在片库扫描目录里', task: notInScan() });
    await action(wrapper, 'reimport').trigger('click');
    await flushPromises();
    expect(api.ReimportDownload).toHaveBeenCalledWith('t1');
    const row = wrapper.get('[data-test="download-item"]');
    expect(row.text().split('下载目录不在片库扫描目录里')).toHaveLength(2);
    expect(row.get('[data-test="download-action-notice"]').text()).toContain('仍没有成功');

    api.RevealDownload.mockResolvedValue({ code: 'reveal_failed', message: '无法在访达中定位文件' });
    await action(wrapper, 'reveal').trigger('click');
    await flushPromises();
    expect(api.RevealDownload).toHaveBeenCalledWith('t1');
    expect(row.get('[data-test="download-action-notice"]').text()).toContain('无法在访达中定位文件');
    wrapper.unmount();
  });

  it('MEDIA-09 同一会话里失败的任务可以重试，重试结果按行显示', async () => {
    const wrapper = await mountPage([{ id: 'f1', filename: '乙.mp4', state: 'failed', error: 'ffmpeg 失败', retryable: true }]);
    expect(wrapper.find('[data-test="download-resend-hint"]').exists()).toBe(false);
    api.RetryDownload.mockResolvedValue({ code: 'queue_full', message: '下载队列已满，等前面的任务跑完再试' });
    await action(wrapper, 'retry').trigger('click');
    await flushPromises();
    expect(api.RetryDownload).toHaveBeenCalledWith('f1');
    expect(wrapper.get('[data-test="download-action-notice"]').text()).toContain('下载队列已满');

    api.RetryDownload.mockResolvedValue({ code: 'ok', task: { id: 'f1', filename: '乙.mp4', state: 'queued', retryable: false } });
    await action(wrapper, 'retry').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="download-action-notice"]').text()).toContain('已重新排队');
    wrapper.unmount();
  });

  it('MEDIA-10 重启后遗留的任务显示「已中断」，只提示回浏览器重新推送，不给重试', async () => {
    const wrapper = await mountPage([{ id: 'i1', filename: '丙.mp4', state: 'interrupted', retryable: false }]);
    const row = wrapper.get('[data-test="download-item"]');
    expect(row.text()).toContain('已中断');
    expect(row.get('[data-test="download-resend-hint"]').text()).toContain('重新推送');
    expect(row.find('[data-test="download-action-retry"]').exists()).toBe(false);
    // 中断是已结束的一种，归到「已结束」组，也能被「清掉已结束」收起。
    expect(wrapper.text()).toContain('已结束');
    wrapper.unmount();
  });
});

// ===== 下载完自动查重（2026-10-12 D-DD-04）=====
describe('下载查重提示', () => {
  const done = (duplicate, extra = {}) => ({ id: 't1', filename: '新.mp4', state: 'done', video_id: 50, import_status: 'imported', duplicate, ...extra });
  const exact = { video_id: 7, kind: 'exact', relation: 'same', title: '老片', width: 1920, height: 1080, size: 2 * 1024 ** 3 };
  const version = { video_id: 8, kind: 'version', relation: 'higher', title: '高清版', width: 3840, height: 2160, size: 9 * 1024 ** 3 };

  it('相同文件与其他版本分别说清楚，并给对应的动作', async () => {
    const wrapper = await mountPage([done({ status: 'found', matches: [exact, version], unhashed_videos: 0 })]);
    const matches = wrapper.findAll('[data-test="download-duplicate-match"]');
    expect(matches).toHaveLength(2);
    expect(matches[0].text()).toContain('片库里已有完全相同的文件：《老片》（1920×1080，2.0 GB）');
    expect(matches[0].find('[data-test="download-dup-version-group"]').exists()).toBe(false);
    expect(matches[0].get('[data-test="download-dup-dismiss"]').text()).toBe('保留两份');
    expect(matches[1].text()).toContain('片库里有同一部的其他版本：《高清版》（3840×2160，9.0 GB，比这次的更清楚）');
    expect(matches[1].get('[data-test="download-dup-dismiss"]').text()).toBe('不是同一部');
    expect(matches[1].find('[data-test="download-dup-version-group"]').exists()).toBe(true);
    wrapper.unmount();
  });

  it('没入库的下载不给移到废纸篓和版本组；查重失败与指纹覆盖不全时说明原因', async () => {
    const wrapper = await mountPage([
      done({ status: 'found', matches: [exact], unhashed_videos: 0 }, { id: 'a', video_id: 0, import_status: 'not_in_scan_roots' }),
      done({ status: 'failed', matches: [], error: '读取失败' }, { id: 'b' }),
      done({ status: 'none', matches: [], unhashed_videos: 12 }, { id: 'c' })
    ]);
    // 没入库的那条照样提示相同文件，但只能「保留两份」：没有片库记录可删、可建版本组。
    const match = wrapper.get('[data-test="download-duplicate-match"]');
    expect(match.text()).toContain('片库里已有完全相同的文件：《老片》');
    expect(match.find('[data-test="download-dup-trash"]').exists()).toBe(false);
    expect(match.find('[data-test="download-dup-version-group"]').exists()).toBe(false);
    expect(match.get('[data-test="download-dup-dismiss"]').text()).toBe('保留两份');
    const notes = wrapper.findAll('[data-test="download-duplicate-note"]').map(node => node.text());
    expect(notes).toEqual([
      '这次下载还没进片库，不能从这里删除；可以在访达中显示后手动处理。',
      '查重没有完成：读取失败',
      '片库里还有 12 部视频没算视频指纹，没参与「其他版本」的比对。'
    ]);
    wrapper.unmount();
  });

  it('「不是同一部」调用后端并就地换上新结果；建立版本组带上两部视频', async () => {
    api.DismissDownloadDuplicate.mockResolvedValue({ code: 'ok', task: done({ status: 'none', matches: [], unhashed_videos: 0 }) });
    api.CreateVersionGroup.mockResolvedValue({ id: 3 });
    const wrapper = await mountPage([done({ status: 'found', matches: [version], unhashed_videos: 0 })]);
    await wrapper.get('[data-test="download-dup-version-group"]').trigger('click');
    await flushPromises();
    expect(api.CreateVersionGroup).toHaveBeenCalledWith([50, 8], '');
    expect(wrapper.get('[data-test="download-action-notice"]').text()).toContain('已和《高清版》建立版本组');
    await wrapper.get('[data-test="download-dup-dismiss"]').trigger('click');
    await flushPromises();
    expect(api.DismissDownloadDuplicate).toHaveBeenCalledWith('t1', 8);
    expect(wrapper.find('[data-test="download-duplicate"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('把这次下载移到废纸篓：「删除前确认」开着（默认）先确认，再走删除宿主删这次下载的那条记录；取消时不删', async () => {
    api.DeleteVideosWithResult.mockImplementation(ids => Promise.resolve({ batch_id: 'b1', requested: 1, succeeded: 1, failed: 0, cancelled: 0, items: ids.map(id => ({ id, code: 'ok' })) }));
    const wrapper = await mountPage([done({ status: 'found', matches: [exact], unhashed_videos: 0 })]);
    feedback.confirmAction.mockResolvedValueOnce(false);
    await wrapper.get('[data-test="download-dup-trash"]').trigger('click');
    await flushPromises();
    expect(api.DeleteVideosWithResult).not.toHaveBeenCalled();
    await wrapper.get('[data-test="download-dup-trash"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction.mock.calls.at(-1)[0]).toMatchObject({ level: 'confirm', confirmText: '移到废纸篓' });
    expect(feedback.confirmAction.mock.calls.at(-1)[0].message).toContain('片库里保留《老片》');
    expect(api.DeleteVideosWithResult).toHaveBeenCalledWith([50], true, expect.any(String));
    // 挂载一次、两次点击前各按当前片库重取一次、删完再取一次。
    expect(api.ListDownloadTasks).toHaveBeenCalledTimes(4);
    wrapper.unmount();
  });

  it('「删除前确认」关了：不弹确认，直接移到废纸篓（可撤销）', async () => {
    api.GetSettings.mockResolvedValueOnce({ confirm_before_delete: false });
    api.DeleteVideosWithResult.mockImplementation(ids => Promise.resolve({ batch_id: 'b1', requested: 1, succeeded: 1, failed: 0, cancelled: 0, items: ids.map(id => ({ id, code: 'ok' })) }));
    const wrapper = await mountPage([done({ status: 'found', matches: [exact], unhashed_videos: 0 })]);
    await wrapper.get('[data-test="download-dup-trash"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).not.toHaveBeenCalled();
    expect(api.DeleteVideosWithResult).toHaveBeenCalledWith([50], true, expect.any(String));
    wrapper.unmount();
  });

  it('删除前重取发现提示已过期（对方已不在）：不删，说明已刷新', async () => {
    const wrapper = await mountPage([done({ status: 'found', matches: [exact], unhashed_videos: 0 })]);
    api.ListDownloadTasks.mockResolvedValue([done({ status: 'none', matches: [], unhashed_videos: 0 })]);
    await wrapper.get('[data-test="download-dup-trash"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).not.toHaveBeenCalled();
    expect(api.DeleteVideosWithResult).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('建立版本组之后把这一项从提示里拿掉（不写「不是同一部」）', async () => {
    api.CreateVersionGroup.mockResolvedValue({ id: 3 });
    api.ClearDownloadDuplicate.mockResolvedValue({ code: 'ok', task: done({ status: 'none', matches: [], unhashed_videos: 0 }) });
    const wrapper = await mountPage([done({ status: 'found', matches: [version], unhashed_videos: 0 })]);
    await wrapper.get('[data-test="download-dup-version-group"]').trigger('click');
    await flushPromises();
    expect(api.ClearDownloadDuplicate).toHaveBeenCalledWith('t1', 8);
    expect(api.DismissDownloadDuplicate).not.toHaveBeenCalled();
    expect(wrapper.find('[data-test="download-duplicate"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('其他版本可以对比播放：片库那一份在左、这次下载在右；相同文件不给对比', async () => {
    const wrapper = await mountPage([done({ status: 'found', matches: [exact, version], unhashed_videos: 0 })]);
    const matches = wrapper.findAll('[data-test="download-duplicate-match"]');
    expect(matches[0].find('[data-test="download-dup-compare"]').exists()).toBe(false);
    await matches[1].get('[data-test="download-dup-compare"]').trigger('click');
    expect(compare.openVersionCompare).toHaveBeenCalledWith(8, 50);
    wrapper.unmount();
  });

  it('「其他版本」没比完时说明原因，相同文件照样提示；关系文案不冒称分辨率相同', async () => {
    const similar = { ...version, video_id: 9, relation: 'larger', width: 0, height: 0, size: 3 * 1024 ** 3 };
    const wrapper = await mountPage([done({ status: 'found', matches: [exact, similar], unhashed_videos: 4, version_error: '计算视频指纹失败' })]);
    expect(wrapper.get('[data-test="download-duplicate-note"]').text()).toBe('「其他版本」的比对没有完成：计算视频指纹失败。');
    expect(wrapper.findAll('[data-test="download-duplicate-match"]')[1].text()).toContain('（3.0 GB，文件比这次的大）');
    wrapper.unmount();
  });
});
