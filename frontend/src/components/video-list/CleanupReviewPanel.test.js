// 断言来自 P-002 拆分前的两处：
//   - src/components/VideoListCleanupReview.test.js 的「清理候选审阅」整节；
//   - src/components/VideoListPage.test.js 的三个清理用例与「清理弹窗重排」整节。
// 面板抽成独立组件后原样搬来，只把挂载对象从 VideoListPage 换成 CleanupReviewPanel。
import { flushPromises, mount, shallowMount } from '@vue/test-utils';

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

import CleanupReviewPanel from './CleanupReviewPanel.vue';
import { feedbackState, resetFeedback } from '../../utils/feedback.js';

// 面板本身不动片库：批量删除、撤销条与列表重载都由片库页经 trashVideos 执行。
// 这里用与 VideoListPage.trashCleanupVideos 同样的调用替身。
function makeTrashVideos() {
  return vi.fn(async (ids) => {
    const result = await api.BatchDeleteVideos(ids, true);
    const failedIDs = new Set((result?.errors || []).map(item => item.video_id));
    const succeededIDs = ids.filter(id => !failedIDs.has(id));
    return { result, failedIDs, succeededIDs };
  });
}

function mountPanel(options = {}) {
  const { deep = false, ...rest } = options;
  const mounter = deep ? mount : shallowMount;
  return mounter(CleanupReviewPanel, {
    props: { trashVideos: makeTrashVideos(), afterTrashVideos: vi.fn() },
    ...rest
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.innerHTML = '';
  resetFeedback();
  feedback.confirmAction.mockResolvedValue(true);
});

async function openCleanupReview() {
  const wrapper = mountPanel({ deep: true, attachTo: document.body });
  await flushPromises();
  wrapper.vm.cleanupDialog.show = true;
  wrapper.vm.cleanupDialog.analysis = {
    duplicate_groups: [{
      original: { id: 41, name: 'IMG_4973.MOV', path: '/v/IMG_4973.MOV', duration: 1, resolution: '1920x1440' },
      candidates: [{ id: 42, name: 'IMG_4973 2.MOV', path: '/v/IMG_4973 2.MOV', duration: 1, resolution: '1920x1440' }],
      reason: '文件大小和采样哈希一致'
    }],
    near_duplicate_groups: [],
    same_source_groups: [],
    low_duration: [],
    low_resolution: []
  };
  await flushPromises();
  return wrapper;
}

describe('清理候选审阅', () => {
  it('每个候选都给出缩略图，光看文件名判断不了是不是真重复', async () => {
    const wrapper = await openCleanupReview();

    const thumbs = wrapper.findAll('[data-test="cleanup-thumb"]');
    expect(thumbs).toHaveLength(2);
    expect(thumbs.map(thumb => thumb.find('img').attributes('src')))
      .toEqual(['/preview/thumbnail/41', '/preview/thumbnail/42']);

    wrapper.unmount();
  });

  it('点缩略图交给系统播放器打开，且不计入播放次数', async () => {
    const wrapper = await openCleanupReview();

    await wrapper.findAll('[data-test="cleanup-thumb"]')[1].trigger('click');
    await flushPromises();

    expect(api.PreviewExternally).toHaveBeenCalledWith(42);
    expect(api.PlayVideo).not.toHaveBeenCalled();

    wrapper.unmount();
  });

  it('系统播放器打不开时给得出可见的错误提示', async () => {
    const wrapper = await openCleanupReview();
    api.PreviewExternally.mockRejectedValueOnce(new Error('no default app'));

    await wrapper.findAll('[data-test="cleanup-thumb"]')[0].trigger('click');
    await flushPromises();

    expect(feedback.notifyError.mock.calls.map(call => String(call[0])).join('\n')).toContain('用系统播放器打开失败');
    wrapper.unmount();
  });
});

describe('清理候选选择与分组', () => {
  it('shows near-duplicate groups without selecting either video by default', async () => {
    const wrapper = mountPanel();
    await flushPromises();
    wrapper.vm.cleanupDialog.show = true;
    wrapper.vm.cleanupDialog.analysis = {
      duplicate_groups: [],
      near_duplicate_groups: [{
        original: { id: 41, name: 'source.mkv', duration: 120, resolution: '1080p' },
        candidates: [{ id: 42, name: 'transcode.mp4', duration: 120, resolution: '720p' }],
        reason: '三帧感知哈希接近'
      }],
      same_source_groups: [],
      low_duration: [],
      low_resolution: []
    };
    await wrapper.vm.$nextTick();

    expect(wrapper.vm.cleanupSelection).toEqual([]);
    expect(wrapper.vm.getAllCleanupCandidates().map(video => video.id)).toEqual([41, 42]);

    wrapper.vm.selectAllCleanupCandidates();
    expect(wrapper.vm.cleanupSelection).toEqual([]);

    wrapper.vm.cleanupDialog.analysis.duplicate_groups = [{
      original: { id: 51, name: 'orig.mkv' },
      candidates: [{ id: 52, name: 'copy.mkv' }]
    }];
    wrapper.vm.selectAllCleanupCandidates();
    expect(wrapper.vm.cleanupSelection).toEqual([51, 52]);
    wrapper.unmount();
  });

  it('restores a background analysis on reopen and groups candidates by directory', async () => {
    const analysis = {
      duplicate_groups: [{
        original: { id: 1, name: 'keep.mp4', directory: '/lib/a', path: '/lib/a/keep.mp4' },
        candidates: [{ id: 2, name: 'copy.mp4', directory: '/lib/b', path: '/lib/b/copy.mp4' }],
        reason: '文件大小和采样哈希一致'
      }],
      near_duplicate_groups: [],
      same_source_groups: [],
      low_duration: [],
      low_resolution: []
    };
    // 后台跑完的结果（并且期间库变过被标记为过期）：重开面板必须直接看到它，不是重新分析。
    api.GetCleanupStatus.mockResolvedValue({
      running: false, completed: true, error: '', stale: true, progress: { stage: 'done' }, analysis
    });
    const wrapper = mountPanel();
    await flushPromises();

    await wrapper.vm.open();
    await flushPromises();

    expect(api.StartCleanupAnalysis).not.toHaveBeenCalled();
    expect(wrapper.vm.cleanupDialog.loading).toBe(false);
    expect(wrapper.vm.cleanupDialog.analysis).toBeTruthy();
    expect(wrapper.vm.cleanupResultStale).toBe(true);

    // 整组归到建议保留项所在目录，另一份仍在 /lib/b 但跟着组走。
    const sections = wrapper.vm.cleanupDirectorySections;
    expect(sections.map(section => section.directory)).toEqual(['/lib/a']);
    expect(sections[0].entries[0].kind).toBe('exact');
    expect(sections[0].videoCount).toBe(2);
    wrapper.unmount();
  });

  it('asks before re-analysing after trashing cleanup candidates', async () => {
    const analysis = {
      duplicate_groups: [{
        original: { id: 1, name: 'keep.mp4', directory: '/lib/a' },
        candidates: [{ id: 2, name: 'copy.mp4', directory: '/lib/a' }],
        reason: '文件大小和采样哈希一致'
      }],
      near_duplicate_groups: [], same_source_groups: [], low_duration: [], low_resolution: []
    };
    api.GetCleanupStatus.mockResolvedValue({
      running: false, completed: true, error: '', stale: false, progress: { stage: 'done' }, analysis
    });
    api.BatchDeleteVideos.mockResolvedValue({ requested: 1, succeeded: 1, failed: 0, errors: [] });
    feedback.confirmAction.mockResolvedValue(false);

    const wrapper = mountPanel();
    await flushPromises();
    await wrapper.vm.open();
    await flushPromises();

    // 顺序要求：先删除并收窄勾选，再走撤销条 + 列表重载。删除后若重载失败，
    // 勾选里也不能还留着已经进回收站的 id，否则重试会对它再删一次。
    let selectionWhenAfterTrashRan = null;
    wrapper.setProps({ afterTrashVideos: vi.fn(async () => { selectionWhenAfterTrashRan = [...wrapper.vm.cleanupSelection]; }) });
    await wrapper.vm.$nextTick();

    wrapper.vm.cleanupSelection = [2];
    await wrapper.vm.trashSelectedCleanupCandidates();
    await flushPromises();

    expect(api.BatchDeleteVideos).toHaveBeenCalledWith([2], true);
    expect(selectionWhenAfterTrashRan).toEqual([]);
    // 答"取消"：不重跑，结果留在原地继续审阅，只标记为已过期。
    expect(feedback.confirmAction).toHaveBeenCalledTimes(1);
    expect(api.StartCleanupAnalysis).not.toHaveBeenCalled();
    expect(wrapper.vm.cleanupDialog.analysis).toBeTruthy();
    expect(wrapper.vm.cleanupResultStale).toBe(true);
    // 删除结束后要把 deletingIds 的收尾交回片库页。
    expect(wrapper.emitted('trash-settled')).toEqual([[[2]]]);

    // 全选候选不能把已移入回收站的项重新选上，否则会对着已删的视频再删一次。
    wrapper.vm.selectAllCleanupCandidates();
    expect(wrapper.vm.cleanupSelection).not.toContain(2);
    expect(wrapper.vm.cleanupSelection).toContain(1);
    wrapper.unmount();
  });
});

describe('清理弹窗重排', () => {
  const analysis = {
    duplicate_groups: [{
      original: { id: 1, name: 'keep.mkv', directory: '/lib/a', path: '/lib/a/keep.mkv', size: 100 },
      candidates: [{ id: 2, name: 'copy.mkv', directory: '/lib/a', path: '/lib/a/copy.mkv', size: 100 }]
    }],
    near_duplicate_groups: [],
    same_source_groups: [],
    low_duration: [{ id: 3, name: 'short.mov', directory: '/lib/b', path: '/lib/b/short.mov', size: 10 }],
    low_resolution: []
  };

  async function openCleanup() {
    const wrapper = mountPanel();
    await flushPromises();
    wrapper.vm.cleanupDialog.show = true;
    wrapper.vm.cleanupDialog.analysis = analysis;
    await wrapper.vm.$nextTick();
    return wrapper;
  }

  // 弹窗内容在 BaseModal 的插槽里，shallowMount 下不渲染，
  // 所以这里查状态；按钮的 disabled 绑定由源码断言钉住。
  it('默认零选中，可释放空间按整组扣掉建议保留项', async () => {
    const wrapper = await openCleanup();
    expect(wrapper.vm.cleanupSelection).toEqual([]);
    // 两组各有一个建议保留项：重复组保留 1（100B），短视频组只有它自己且是保留项。
    expect(wrapper.vm.cleanupReleasableText).toBe('100 B');
    wrapper.unmount();
  });

  it('类别筛选只收窄看到的候选，不改分析结果', async () => {
    const wrapper = await openCleanup();
    expect(wrapper.vm.cleanupFilteredSections).toHaveLength(2);
    wrapper.vm.cleanupCategory = 'low-duration';
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.cleanupFilteredSections).toHaveLength(1);
    expect(wrapper.vm.cleanupFilteredSections[0].directory).toBe('/lib/b');
    // 分析结果本身没有被改动。
    expect(wrapper.vm.cleanupDialog.analysis.duplicate_groups).toHaveLength(1);
    wrapper.unmount();
  });

  it('「按建议勾选本组」只勾非保留项', async () => {
    const wrapper = await openCleanup();
    const section = wrapper.vm.cleanupDirectorySections.find(item => item.directory === '/lib/a');
    wrapper.vm.selectSuggestedInSection(section);
    // 建议保留的 1 不该被勾上，只勾副本 2。
    expect(wrapper.vm.cleanupSelection).toEqual([2]);
    wrapper.unmount();
  });

  it('底栏的可释放空间只算选中项，不含建议保留项', async () => {
    const wrapper = await openCleanup();
    expect(wrapper.vm.cleanupSelectedSizeText).toBe('');
    wrapper.vm.cleanupSelection = [2];
    await wrapper.vm.$nextTick();
    expect(wrapper.vm.cleanupSelectedSizeText).toBe('100 B');
    wrapper.unmount();
  });
});

describe('面板与片库页之间的接口', () => {
  it('候选数与分析中状态镜像给父组件，管理菜单的徽标才有数据', async () => {
    const wrapper = mountPanel();
    await flushPromises();
    wrapper.vm.cleanupDialog.analysis = {
      duplicate_groups: [{ original: { id: 1 }, candidates: [{ id: 2 }] }],
      near_duplicate_groups: [], same_source_groups: [], low_duration: [], low_resolution: []
    };
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted('badge-change').at(-1)).toEqual([1]);

    wrapper.vm.cleanupDialog.loading = true;
    await wrapper.vm.$nextTick();
    expect(wrapper.emitted('analyzing-change').at(-1)).toEqual([true]);
    wrapper.unmount();
  });

  it('「重算感知哈希」不自己发起任务，交给片库页的补全状态条', async () => {
    const wrapper = mountPanel({ deep: true, attachTo: document.body, props: { trashVideos: makeTrashVideos(), afterTrashVideos: vi.fn(), perceptualHashRunning: false } });
    await flushPromises();
    wrapper.vm.cleanupDialog.show = true;
    wrapper.vm.cleanupDialog.analysis = {
      stale_hash_count: 3,
      duplicate_groups: [], near_duplicate_groups: [], same_source_groups: [], low_duration: [], low_resolution: []
    };
    await flushPromises();

    const button = wrapper.findAll('button').find(item => item.text().includes('重算感知哈希'));
    expect(button).toBeTruthy();
    await button.trigger('click');
    expect(wrapper.emitted('start-perceptual-hash')).toHaveLength(1);
    expect(api.StartPerceptualHashBackfill).not.toHaveBeenCalled();
    wrapper.unmount();
  });
});

// 截取片段（P-008 / AC-17、AC-18）：新类别的安全边界与判断依据。
describe('截取片段类别', () => {
  const clipAnalysis = () => ({
    duplicate_groups: [],
    near_duplicate_groups: [],
    same_source_groups: [],
    clip_groups: [{
      full: { id: 11, name: 'feature.mkv', directory: '/lib/a', path: '/lib/a/feature.mkv', size: 4000, duration: 3600, resolution: '1920x1080' },
      clip: { id: 12, name: 'excerpt.mp4', directory: '/lib/a', path: '/lib/a/excerpt.mp4', size: 500, duration: 600, resolution: '1920x1080' },
      offset_seconds: 1200,
      match_rate: 0.93,
      estimated_savings: 500
    }],
    low_duration: [],
    low_resolution: []
  });

  async function openClipReview(options = {}) {
    const wrapper = mountPanel({ deep: true, attachTo: document.body, ...options });
    await flushPromises();
    wrapper.vm.cleanupDialog.show = true;
    wrapper.vm.cleanupDialog.analysis = clipAnalysis();
    await flushPromises();
    return wrapper;
  }

  it('默认不勾选，且「全选候选」也不会把截取片段选上', async () => {
    const wrapper = await openClipReview();

    expect(wrapper.vm.cleanupSelection).toEqual([]);
    const checkboxes = wrapper.findAll('[data-test="cleanup-clip-select"]');
    // 只有片段（B）有勾选框，完整片（A）是建议保留项，不给勾选框。
    expect(checkboxes).toHaveLength(1);
    expect(checkboxes[0].element.checked).toBe(false);

    wrapper.vm.selectAllCleanupCandidates();
    expect(wrapper.vm.cleanupSelection).toEqual([]);
    wrapper.unmount();
  });

  it('并排给出 A / B 与对齐偏移、命中率，判断依据不用去别处找', async () => {
    const wrapper = await openClipReview();

    expect(wrapper.find('[data-test="cleanup-clip-pair"]').exists()).toBe(true);
    const pair = wrapper.get('[data-test="cleanup-clip-pair"]').text();
    expect(pair).toContain('A · 完整片（建议保留）');
    expect(pair).toContain('feature.mkv');
    expect(pair).toContain('B · 截取片段（可清理）');
    expect(pair).toContain('excerpt.mp4');

    const evidence = wrapper.get('[data-test="cleanup-clip-evidence"]').text();
    expect(evidence).toContain('20:00');
    expect(evidence).toContain('93%');
    wrapper.unmount();
  });

  it('偏移 0 的片头截取也要显示成 00:00，而不是空白', async () => {
    const wrapper = await openClipReview();
    wrapper.vm.cleanupDialog.analysis.clip_groups[0].offset_seconds = 0;
    await flushPromises();

    expect(wrapper.get('[data-test="cleanup-clip-evidence"]').text()).toContain('00:00');
    wrapper.unmount();
  });

  it('勾选片段后走既有的回收站路径，不会另开一条删除口子', async () => {
    const wrapper = await openClipReview();
    api.BatchDeleteVideos.mockResolvedValue({ requested: 1, succeeded: 1, failed: 0, errors: [] });
    feedback.confirmAction.mockResolvedValue(false);

    await wrapper.get('[data-test="cleanup-clip-select"]').setValue(true);
    expect(wrapper.vm.cleanupSelection).toEqual([12]);
    await wrapper.vm.trashSelectedCleanupCandidates();
    await flushPromises();

    expect(api.BatchDeleteVideos).toHaveBeenCalledWith([12], true);
    wrapper.unmount();
  });

  it('「忽略」调 DismissClipCandidate 并把这条候选摘掉', async () => {
    const wrapper = await openClipReview();

    await wrapper.get('[data-test="cleanup-dismiss-clip"]').trigger('click');
    await flushPromises();

    expect(api.DismissClipCandidate).toHaveBeenCalledWith(11, 12);
    expect(wrapper.vm.cleanupDialog.analysis.clip_groups).toEqual([]);
    expect(wrapper.vm.cleanupSelection).toEqual([]);
    wrapper.unmount();
  });

  it('忽略失败时要说出来，不能静默地留在界面上', async () => {
    const wrapper = await openClipReview();
    api.DismissClipCandidate.mockRejectedValueOnce(new Error('no sequence'));

    await wrapper.get('[data-test="cleanup-dismiss-clip"]').trigger('click');
    await flushPromises();

    expect(feedback.notifyError.mock.calls.map(call => String(call[0])).join('\n')).toContain('忽略截取片段失败');
    expect(wrapper.vm.cleanupDialog.analysis.clip_groups).toHaveLength(1);
    wrapper.unmount();
  });

  it('没有截取候选时不出现这个类别，也不留空卡片', async () => {
    const wrapper = mountPanel({ deep: true, attachTo: document.body });
    await flushPromises();
    wrapper.vm.cleanupDialog.show = true;
    wrapper.vm.cleanupDialog.analysis = {
      duplicate_groups: [], near_duplicate_groups: [], same_source_groups: [],
      clip_groups: [], low_duration: [], low_resolution: []
    };
    await flushPromises();

    expect(wrapper.find('[data-test="cleanup-clip-pair"]').exists()).toBe(false);
    expect(wrapper.vm.cleanupCategoryOptions.find(option => option.key === 'clip').count).toBe(0);
    expect(wrapper.text()).toContain('当前没有命中轻量清理规则的候选项');
    wrapper.unmount();
  });

  it('「补全帧哈希」不自己发起任务，交给片库页的补全状态条', async () => {
    const wrapper = mountPanel({
      deep: true, attachTo: document.body,
      props: { trashVideos: makeTrashVideos(), afterTrashVideos: vi.fn(), frameHashRunning: false }
    });
    await flushPromises();
    wrapper.vm.cleanupDialog.show = true;
    wrapper.vm.cleanupDialog.analysis = {
      stale_frame_hash_count: 4,
      duplicate_groups: [], near_duplicate_groups: [], same_source_groups: [],
      clip_groups: [], low_duration: [], low_resolution: []
    };
    await flushPromises();

    expect(wrapper.get('[data-test="cleanup-stale-frame-hash-hint"]').text()).toContain('4 个视频还没有帧哈希');
    await wrapper.get('[data-test="cleanup-start-frame-hash"]').trigger('click');
    expect(wrapper.emitted('start-frame-hash')).toHaveLength(1);
    expect(api.StartFrameHashBackfill).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('截取候选计入管理菜单的徽标，否则新类别在菜单上是隐形的', async () => {
    const wrapper = mountPanel();
    await flushPromises();
    wrapper.vm.cleanupDialog.analysis = clipAnalysis();
    await wrapper.vm.$nextTick();

    expect(wrapper.emitted('badge-change').at(-1)).toEqual([1]);
    expect(wrapper.vm.getAllCleanupCandidates().map(video => video.id)).toEqual([12]);
    wrapper.unmount();
  });
});

// 清理面板的每一行是"留哪个删哪个"的决策现场，以前只给分辨率和时长，唯独没有大小。
describe('清理行元信息', () => {
  it('每一行都带文件大小，没有大小的行则不留空位', async () => {
    const wrapper = await openCleanupReview();
    wrapper.vm.cleanupDialog.analysis = {
      duplicate_groups: [{
        original: { id: 41, name: 'IMG_4973.MOV', path: '/v/IMG_4973.MOV', duration: 1, resolution: '1920x1440', size: 2.5 * 1024 * 1024 * 1024 },
        candidates: [{ id: 42, name: 'IMG_4973 2.MOV', path: '/v/IMG_4973 2.MOV', duration: 1, resolution: '1920x1440' }],
        reason: '文件大小和采样哈希一致'
      }],
      near_duplicate_groups: [], same_source_groups: [], low_duration: [], low_resolution: []
    };
    await flushPromises();
    const mains = wrapper.findAll('.cleanup-item-main').map(node => node.text());
    expect(mains.some(text => text === 'IMG_4973.MOV · 1920x1440 · 00:01 · 2.5 GB')).toBe(true);
    expect(mains.some(text => text === 'IMG_4973 2.MOV · 1920x1440 · 00:01')).toBe(true);
    wrapper.unmount();
  });
});
