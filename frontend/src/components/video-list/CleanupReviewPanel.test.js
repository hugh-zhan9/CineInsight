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
// 替身按 VideoListPage.trashCleanupVideos 的契约：trashVideos(ids, { names }) →
// { result: { requested, succeeded, failed, errors }, failedIDs: Set, succeededIDs: [] }，默认全部成功。
// 断言直接看 trashVideos 收到了什么（P-032 评审 Minor 9：不再经旧的 BatchDeleteVideos）。
function makeTrashVideos() {
  return vi.fn(async (ids) => ({
    result: { requested: ids.length, succeeded: ids.length, failed: 0, errors: [] },
    failedIDs: new Set(),
    succeededIDs: [...ids]
  }));
}

// MergeMediaMetadata 的第 4 个参数（§9.1）：截取片段组两项都跳过，其余类别都不跳过。
const FULL_MERGE = { skip_playback_state: false, skip_subtitle: false };
const CLIP_MERGE = { skip_playback_state: true, skip_subtitle: true };

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
  api.GetCleanupStatus.mockResolvedValue(null);
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
  it.each(['near', 'same-source', 'clip'])('保存 %s 决策后，重开与迟到的旧回读都不能恢复候选', async (kind) => {
    const wrapper = mountPanel();
    await flushPromises();
    const a = { id: 11, name: 'full.mp4' };
    const b = { id: 12, name: 'clip.mp4' };
    const group = kind === 'near' ? { original: a, candidates: [b] }
      : kind === 'clip' ? { full: a, clip: b }
        : { relation_id: 8, preferred: a, alternative: b };
    const key = kind === 'near' ? 'near_duplicate_groups' : kind === 'clip' ? 'clip_groups' : 'same_source_groups';
    const oldStatus = { completed: true, analysis: { [key]: [group] } };
    wrapper.vm.applyCleanupStatus(oldStatus);
    wrapper.vm.cleanupSelection = [12];

    let resolveOld;
    api.GetCleanupStatus.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve; }));
    const pendingRead = wrapper.vm.refreshStatus();
    // 服务端已按落库决策过滤，成功后的回读以及重新打开拿到同一份结果。
    api.GetCleanupStatus.mockResolvedValue({ completed: true, analysis: { [key]: [] } });
    const method = kind === 'near' ? 'dismissNearDuplicateGroup' : kind === 'clip' ? 'dismissClipCandidate' : 'rejectCleanupSameSource';
    await wrapper.vm[method](group);
    expect(wrapper.vm.cleanupDialog.analysis[key]).toEqual([]);
    expect(feedback.notifySuccess).toHaveBeenCalledOnce();

    resolveOld(oldStatus);
    await pendingRead;
    await wrapper.vm.open();
    expect(wrapper.vm.cleanupDialog.analysis[key]).toEqual([]);
    expect(wrapper.vm.cleanupSelection).toEqual([]);
    expect(api.StartCleanupAnalysisFromSettings).not.toHaveBeenCalled();
    wrapper.unmount();
  });

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
  // 2026-09-30 P-032 起按 D-PC49 统一勾选规则：旧用例钉住的「全选候选连保留项一起勾」已退役，
  // 精确重复默认勾非保留项、保留项锁定。
  it('IMG-05 近似重复默认一项都不勾；精确重复默认只勾非保留项，保留项锁定', async () => {
    const wrapper = mountPanel();
    await flushPromises();
    wrapper.vm.cleanupDialog.show = true;
    wrapper.vm.applyCleanupStatus({ completed: true, started_at: 'run-1', analysis: {
      duplicate_groups: [],
      near_duplicate_groups: [{
        original: { id: 41, name: 'source.mkv', duration: 120, resolution: '1080p' },
        candidates: [{ id: 42, name: 'transcode.mp4', duration: 120, resolution: '720p' }],
        reason: '三帧感知哈希接近'
      }],
      same_source_groups: [],
      low_duration: [],
      low_resolution: []
    } });
    await wrapper.vm.$nextTick();

    expect(wrapper.vm.cleanupSelection).toEqual([]);
    expect(wrapper.vm.getAllCleanupCandidates().map(video => video.id)).toEqual([41, 42]);

    wrapper.vm.applyCleanupStatus({ completed: true, started_at: 'run-2', analysis: {
      duplicate_groups: [{ original: { id: 51, name: 'orig.mkv' }, candidates: [{ id: 52, name: 'copy.mkv' }] }],
      near_duplicate_groups: [], same_source_groups: [], low_duration: [], low_resolution: []
    } });
    expect(wrapper.vm.cleanupSelection).toEqual([52]);
    // 保留项锁定：直接去勾也勾不上。
    wrapper.vm.toggleCleanupSelection(51);
    expect(wrapper.vm.cleanupSelection).toEqual([52]);
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

    expect(api.StartCleanupAnalysisFromSettings).not.toHaveBeenCalled();
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
    feedback.confirmAction.mockResolvedValue(false);

    const wrapper = mountPanel();
    const trashVideos = wrapper.props('trashVideos');
    await flushPromises();
    await wrapper.vm.open();
    await flushPromises();

    // 顺序要求：先删除并收窄勾选，再走撤销条 + 列表重载。删除后若重载失败，
    // 勾选里也不能还留着已经进回收站的 id，否则重试会对它再删一次。
    let selectionWhenAfterTrashRan = null;
    wrapper.setProps({ afterTrashVideos: vi.fn(async () => { selectionWhenAfterTrashRan = [...wrapper.vm.cleanupSelection]; }) });
    await wrapper.vm.$nextTick();

    // 精确重复的非保留项默认勾上（D-PC49）。
    expect(wrapper.vm.cleanupSelection).toEqual([2]);
    const pending = wrapper.vm.trashSelectedCleanupCandidates();
    await flushPromises();
    // 删除前先有汇总确认（D-PC49），确认之后才开始写。
    expect(trashVideos).not.toHaveBeenCalled();
    wrapper.vm.answerCleanupDelete(true);
    await pending;
    await flushPromises();

    expect(trashVideos).toHaveBeenCalledWith([2], { names: { 2: 'copy.mp4' } });
    expect(selectionWhenAfterTrashRan).toEqual([]);
    // 答"取消"：不重跑，结果留在原地继续审阅，只标记为已过期。
    expect(feedback.confirmAction).toHaveBeenCalledTimes(1);
    expect(api.StartCleanupAnalysisFromSettings).not.toHaveBeenCalled();
    expect(wrapper.vm.cleanupDialog.analysis).toBeTruthy();
    expect(wrapper.vm.cleanupResultStale).toBe(true);
    // 删除结束后要把 deletingIds 的收尾交回片库页。
    expect(wrapper.emitted('trash-settled')).toEqual([[[2]]]);

    // 按建议勾选不能把已移到废纸篓的项重新选上，否则会对着已删的视频再删一次；保留项 1 始终锁定。
    const entry = wrapper.vm.cleanupDirectorySections[0].entries[0];
    expect(wrapper.vm.entrySuggestedIDs(entry)).toEqual([]);
    wrapper.vm.toggleEntrySuggestion(entry);
    wrapper.vm.toggleCleanupSelection(2);
    wrapper.vm.toggleCleanupSelection(1);
    expect(wrapper.vm.cleanupSelection).toEqual([]);
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
  // 旧用例「默认零选中」随 D-PC49 改为：精确重复的非保留项默认勾选，极短片段默认不勾。
  it('IMG-05 默认只勾精确重复的非保留项，可释放空间按整组扣掉建议保留项', async () => {
    const wrapper = await openCleanup();
    wrapper.vm.applyCleanupStatus({ completed: true, analysis });
    expect(wrapper.vm.cleanupSelection).toEqual([2]);
    // 两组各有一个建议保留项：重复组保留 1（100B），短视频组只有它自己且是保留项。
    expect(wrapper.vm.cleanupReleasableText).toBe('100 B');
    // 同一批结果再回读一次，不能把用户取消的勾重新勾上。
    wrapper.vm.toggleCleanupSelection(2);
    wrapper.vm.applyCleanupStatus({ completed: true, analysis });
    expect(wrapper.vm.cleanupSelection).toEqual([]);
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

  // 旧版按钮作用于整个目录分区（IMG-05），现在只作用于这一组。
  it('IMG-05「按建议勾选本组」只作用于这一组，只勾非保留项，再点一次取消本组勾选', async () => {
    const wrapper = await openCleanup();
    wrapper.vm.cleanupSelection = [3];
    const entry = wrapper.vm.cleanupDirectorySections.find(item => item.directory === '/lib/a').entries[0];
    wrapper.vm.toggleEntrySuggestion(entry);
    // 建议保留的 1 不该被勾上，只勾副本 2；别的组里已勾的 3 不受影响。
    expect(wrapper.vm.cleanupSelection).toEqual([3, 2]);
    expect(wrapper.vm.isEntryFullySuggested(entry)).toBe(true);
    wrapper.vm.toggleEntrySuggestion(entry);
    expect(wrapper.vm.cleanupSelection).toEqual([3]);
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

  // 外置盘没挂载时本轮会静默跳过大半个库。没有这行提示，界面和插着盘跑出来的
  // 结果长得一模一样，用户只会觉得"检测不准"。
  it('本轮跳过的条目要有可见计数', async () => {
    const wrapper = mountPanel({ deep: true, attachTo: document.body, props: { trashVideos: makeTrashVideos(), afterTrashVideos: vi.fn() } });
    await flushPromises();
    wrapper.vm.cleanupDialog.show = true;
    wrapper.vm.cleanupDialog.analysis = {
      skipped_unavailable: 1316, skipped_metadata: 4, skipped_clip_verification: 2,
      duplicate_groups: [], near_duplicate_groups: [], same_source_groups: [], low_duration: [], low_resolution: []
    };
    await flushPromises();

    const hint = wrapper.find('[data-test="cleanup-skipped-hint"]');
    expect(hint.exists()).toBe(true);
    expect(hint.text()).toContain('1316');
    expect(hint.text()).toContain('4');
    expect(wrapper.find('[data-test="cleanup-clip-verification-skipped"]').text()).toContain('2 组');
    wrapper.unmount();
  });

  it('两个跳过计数都是 0 时不出提示', async () => {
    const wrapper = mountPanel({ deep: true, attachTo: document.body, props: { trashVideos: makeTrashVideos(), afterTrashVideos: vi.fn() } });
    await flushPromises();
    wrapper.vm.cleanupDialog.show = true;
    wrapper.vm.cleanupDialog.analysis = {
      skipped_unavailable: 0, skipped_metadata: 0,
      duplicate_groups: [], near_duplicate_groups: [], same_source_groups: [], low_duration: [], low_resolution: []
    };
    await flushPromises();

    expect(wrapper.find('[data-test="cleanup-skipped-hint"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="cleanup-clip-verification-skipped"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('「补全感知哈希」不自己发起任务，交给片库页的补全状态条', async () => {
    const wrapper = mountPanel({ deep: true, attachTo: document.body, props: { trashVideos: makeTrashVideos(), afterTrashVideos: vi.fn(), perceptualHashRunning: false } });
    await flushPromises();
    wrapper.vm.cleanupDialog.show = true;
    wrapper.vm.cleanupDialog.analysis = {
      stale_hash_count: 3,
      duplicate_groups: [], near_duplicate_groups: [], same_source_groups: [], low_duration: [], low_resolution: []
    };
    await flushPromises();

    const button = wrapper.findAll('button').find(item => item.text().includes('补全感知哈希'));
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

  // 「全选候选」已随 D-PC49 退役（界面上本来没有入口），这里改为钉住默认勾选规则。
  it('IMG-05 默认不勾选，按默认规则套用新结果也不会把截取片段选上', async () => {
    const wrapper = await openClipReview();
    wrapper.vm.applyCleanupStatus({ completed: true, started_at: 'clip-run', analysis: clipAnalysis() });
    await flushPromises();

    expect(wrapper.vm.cleanupSelection).toEqual([]);
    const checkboxes = wrapper.findAll('[data-test="cleanup-clip-select"]');
    // 只有片段（B）有勾选框，完整片（A）是建议保留项，不给勾选框。
    expect(checkboxes).toHaveLength(1);
    expect(checkboxes[0].element.checked).toBe(false);
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
    feedback.confirmAction.mockResolvedValue(false);

    await wrapper.get('[data-test="cleanup-clip-select"]').setValue(true);
    expect(wrapper.vm.cleanupSelection).toEqual([12]);
    await wrapper.get('[data-test="cleanup-trash-selected"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();

    expect(wrapper.props('trashVideos')).toHaveBeenCalledWith([12], { names: { 12: 'excerpt.mp4' } });
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

// ===== P-032 清理中心（前端）：D-PC31 / D-PC36 / D-PC48~D-PC51 =====

function video(id, extra = {}) {
  return { id, name: `v${id}.mp4`, directory: '/lib/a', path: `/lib/a/v${id}.mp4`, size: 100 * id, duration: 60, resolution: '1920x1080', ...extra };
}

function baseAnalysis(extra = {}) {
  return {
    duplicate_groups: [], near_duplicate_groups: [], same_source_groups: [], clip_groups: [],
    low_duration: [], low_resolution: [], ...extra
  };
}

async function openWith(analysis, { status = {}, props = {} } = {}) {
  api.GetCleanupStatus.mockResolvedValue({ running: false, completed: true, error: '', stale: false, started_at: 'run-a', progress: { stage: 'done' }, analysis, ...status });
  const trashVideos = props.trashVideos || makeTrashVideos();
  const wrapper = mountPanel({ deep: true, attachTo: document.body, props: { trashVideos, afterTrashVideos: vi.fn(), ...props } });
  await flushPromises();
  await wrapper.vm.open();
  await flushPromises();
  return { wrapper, trashVideos };
}

async function clickTrash(wrapper) {
  // 勾选刚改过时按钮的 disabled 要等下一次渲染才更新。
  await wrapper.vm.$nextTick();
  await wrapper.get('[data-test="cleanup-trash-selected"]').trigger('click');
  await flushPromises();
}

describe('IMG-03 保留建议与合并元数据', () => {
  const exactAnalysis = () => baseAnalysis({
    duplicate_groups: [{ original: video(1), candidates: [video(2)], reason: '文件大小和采样哈希一致' }],
    curation: { 1: { favorite: true, people: true, subtitle: true }, 2: { rating: true } }
  });

  it('IMG-03 卡片显示整理成果标记，保留项标「建议保留」', async () => {
    const { wrapper } = await openWith(exactAnalysis());
    const rows = wrapper.findAll('[data-test="cleanup-member-row"]');
    expect(rows).toHaveLength(2);
    expect(rows[0].get('[data-test="cleanup-keeper-label"]').text()).toBe('建议保留：');
    expect(rows[0].get('[data-test="cleanup-curation"]').text()).toContain('★ 收藏');
    expect(rows[0].get('[data-test="cleanup-curation"]').text()).toContain('人物');
    expect(rows[0].get('[data-test="cleanup-curation"]').text()).toContain('字幕');
    expect(rows[1].get('[data-test="cleanup-curation"]').text()).toBe('评分');
    wrapper.unmount();
  });

  it('IMG-03 删除确认默认勾选「合并元数据」，先合并到保留项，再删除', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    const calls = [];
    api.MergeMediaMetadata.mockImplementation(async (...args) => { calls.push(['merge', ...args]); return { warnings: [] }; });
    const trashVideos = vi.fn(async (ids) => { calls.push(['trash', ids]); return { result: { succeeded: ids.length, failed: 0, errors: [] }, failedIDs: new Set(), succeededIDs: ids }; });
    const { wrapper } = await openWith(exactAnalysis(), { props: { trashVideos } });
    expect(wrapper.vm.cleanupSelection).toEqual([2]);

    await clickTrash(wrapper);
    const dialog = wrapper.get('[data-test="cleanup-delete-confirm-dialog"]');
    expect(dialog.get('[data-test="cleanup-delete-summary"]').text()).toContain('将把 1 个视频移到废纸篓，共 200 B');
    expect(dialog.get('[data-test="cleanup-delete-kinds"]').text()).toContain('精确重复 1 个');
    expect(dialog.get('[data-test="cleanup-merge-toggle"]').element.checked).toBe(true);
    await dialog.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();

    expect(calls).toEqual([['merge', 'video', 1, [2], FULL_MERGE], ['trash', [2]]]);
    // 不支持废纸篓时的二选一弹窗要列文件名：面板把名字一并交给片库页。
    expect(trashVideos).toHaveBeenCalledWith([2], { names: { 2: 'v2.mp4' } });
    wrapper.unmount();
  });

  it('IMG-03 合并失败时不调用删除，并说清楚一个都没删', async () => {
    api.MergeMediaMetadata.mockRejectedValue(new Error('数据库繁忙'));
    const { wrapper, trashVideos } = await openWith(exactAnalysis());

    await clickTrash(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();

    expect(api.MergeMediaMetadata).toHaveBeenCalledTimes(1);
    expect(trashVideos).not.toHaveBeenCalled();
    expect(feedback.notifyError.mock.calls.map(call => String(call[0])).join('\n')).toContain('合并元数据失败，没有删除任何视频');
    expect(wrapper.vm.cleanupSelection).toEqual([2]);
    expect(wrapper.vm.cleanupDialog.processing).toBe(false);
    wrapper.unmount();
  });

  // P-032 评审 Minor 4：按组逐个合并，第二组失败时前一组已经合并（不回滚），一个都不删。
  it('IMG-03 多组合并中途失败：说明前面几组已合并，没有删除任何视频', async () => {
    api.MergeMediaMetadata
      .mockResolvedValueOnce({ warnings: [] })
      .mockRejectedValueOnce(new Error('数据库繁忙'));
    const { wrapper, trashVideos } = await openWith(baseAnalysis({
      duplicate_groups: [
        { original: video(1), candidates: [video(2)], reason: '一致' },
        { original: video(3), candidates: [video(4)], reason: '一致' }
      ]
    }));
    expect(wrapper.vm.cleanupSelection).toEqual([2, 4]);

    await clickTrash(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();

    expect(api.MergeMediaMetadata.mock.calls).toEqual([['video', 1, [2], FULL_MERGE], ['video', 3, [4], FULL_MERGE]]);
    expect(trashVideos).not.toHaveBeenCalled();
    const message = feedback.notifyError.mock.calls.map(call => String(call[0])).join('\n');
    expect(message).toContain('没有删除任何视频');
    expect(message).toContain('共 2 组，前 1 组已经合并到各自的保留项');
    expect(wrapper.vm.cleanupSelection).toEqual([2, 4]);
    expect(wrapper.vm.cleanupDialog.processing).toBe(false);
    wrapper.unmount();
  });

  // P-032 评审 I-1：锁定的 ID 即使出现在勾选里（界面之外改的、或结果换过），删除前也按锁定规则裁掉。
  it('IMG-03 保留项被强行放进勾选时不会送进删除：先裁剪，再合并与删除其余项', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    api.MergeMediaMetadata.mockResolvedValue({ warnings: [] });
    const { wrapper, trashVideos } = await openWith(exactAnalysis());
    wrapper.vm.cleanupSelection = [1, 2];

    await clickTrash(wrapper);
    expect(wrapper.get('[data-test="cleanup-delete-summary"]').text()).toContain('将把 1 个视频移到废纸篓');
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();

    expect(api.MergeMediaMetadata).toHaveBeenCalledWith('video', 1, [2], FULL_MERGE);
    expect(trashVideos).toHaveBeenCalledTimes(1);
    expect(trashVideos).toHaveBeenCalledWith([2], { names: { 2: 'v2.mp4' } });
    wrapper.unmount();
  });

  // §9.1 主代理裁决（P-032 评审 I-2）：截取片段组不合并观看状态与字幕；同一保留项的两组按组分别调用。
  it('D-PC48 截取片段组只合并标签、人物、作品集、收藏 / 点赞 / 评分，与同一保留项的精确重复组分开调用', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    api.MergeMediaMetadata.mockResolvedValue({ warnings: [] });
    const { wrapper, trashVideos } = await openWith(baseAnalysis({
      duplicate_groups: [{ original: video(1), candidates: [video(2)], reason: '一致' }],
      clip_groups: [{ full: video(1), clip: video(3), offset_seconds: 30, match_rate: 0.95, estimated_savings: 300 }]
    }));
    wrapper.vm.toggleCleanupSelection(3);
    expect(wrapper.vm.cleanupSelection).toEqual([2, 3]);

    await clickTrash(wrapper);
    const dialog = wrapper.get('[data-test="cleanup-delete-confirm-dialog"]');
    expect(dialog.get('[data-test="cleanup-merge-scope-full"]').text()).toContain('观看状态（已看、断点）');
    expect(dialog.get('[data-test="cleanup-merge-scope-full"]').text()).toContain('复制一份给保留项');
    expect(dialog.get('[data-test="cleanup-merge-scope-clip"]').text()).toBe('截取片段组只合并标签、人物、作品集、收藏 / 点赞 / 评分，不合并观看状态和字幕。');
    const note = dialog.get('[data-test="cleanup-merge-note"]').text();
    expect(note).toContain('标签只合并手动标签');
    expect(note).toContain('撤销删除不会撤回合并');
    await dialog.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();

    expect(api.MergeMediaMetadata.mock.calls).toEqual([['video', 1, [2], FULL_MERGE], ['video', 1, [3], CLIP_MERGE]]);
    expect(trashVideos).toHaveBeenCalledWith([2, 3], { names: { 2: 'v2.mp4', 3: 'v3.mp4' } });
    wrapper.unmount();
  });

  it('D-PC48 只删截取片段时，确认框只说截取片段组的合并范围', async () => {
    const { wrapper } = await openWith(baseAnalysis({
      clip_groups: [{ full: video(1), clip: video(3), offset_seconds: 30, match_rate: 0.95, estimated_savings: 300 }]
    }));
    wrapper.vm.toggleCleanupSelection(3);
    await clickTrash(wrapper);
    expect(wrapper.find('[data-test="cleanup-merge-scope-full"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="cleanup-merge-scope-clip"]').exists()).toBe(true);
    await wrapper.get('[data-test="cleanup-delete-cancel"]').trigger('click');
    wrapper.unmount();
  });

  it('IMG-03 取消勾选「合并元数据」时只删除、不合并', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    const { wrapper, trashVideos } = await openWith(exactAnalysis());
    await clickTrash(wrapper);
    await wrapper.get('[data-test="cleanup-merge-toggle"]').setValue(false);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.MergeMediaMetadata).not.toHaveBeenCalled();
    expect(trashVideos).toHaveBeenCalledWith([2], expect.anything());
    wrapper.unmount();
  });

  it('IMG-03 字幕迁移失败只作提示：删除照常进行，提示告诉用户', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    api.MergeMediaMetadata.mockResolvedValue({ warnings: ['字幕迁移失败：目标文件已存在'] });
    const { wrapper, trashVideos } = await openWith(exactAnalysis());
    await clickTrash(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(trashVideos).toHaveBeenCalled();
    expect(feedback.notify.mock.calls.map(call => String(call[0])).join('\n')).toContain('字幕迁移失败');
    wrapper.unmount();
  });

  it('IMG-03 取消确认时不调用任何写入绑定', async () => {
    const { wrapper, trashVideos } = await openWith(exactAnalysis());
    await clickTrash(wrapper);
    await wrapper.get('[data-test="cleanup-delete-cancel"]').trigger('click');
    await flushPromises();

    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(false);
    expect(api.MergeMediaMetadata).not.toHaveBeenCalled();
    expect(trashVideos).not.toHaveBeenCalled();
    expect(wrapper.emitted('trash-settled')).toBeUndefined();
    wrapper.unmount();
  });

  it('IMG-03 只勾了极短 / 极低这类没有保留项的候选时，不出现合并选项', async () => {
    const { wrapper } = await openWith(baseAnalysis({ low_duration: [video(7)] }));
    wrapper.vm.toggleCleanupSelection(7);
    await clickTrash(wrapper);
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="cleanup-merge-toggle"]').exists()).toBe(false);
    await wrapper.get('[data-test="cleanup-delete-cancel"]').trigger('click');
    wrapper.unmount();
  });

  it('IMG-05 删除汇总报出条数、体积与近似 / 同源 / 截取各几条，并提醒按相似度判断', async () => {
    const { wrapper } = await openWith(baseAnalysis({
      near_duplicate_groups: [{ original: video(1), candidates: [video(2)], reason: '感知哈希接近' }],
      same_source_groups: [{ relation_id: 9, preferred: video(3), alternative: video(4), reason: 'AI 判断同源', estimated_savings: 400 }],
      clip_groups: [{ full: video(5), clip: video(6), offset_seconds: 0, match_rate: 0.9, estimated_savings: 600 }]
    }));
    expect(wrapper.vm.cleanupSelection).toEqual([]);
    for (const id of [2, 4, 6]) wrapper.vm.toggleCleanupSelection(id);
    await clickTrash(wrapper);
    const dialog = wrapper.get('[data-test="cleanup-delete-confirm-dialog"]');
    expect(dialog.get('[data-test="cleanup-delete-summary"]').text()).toContain('将把 3 个视频移到废纸篓，共 1.2 KB');
    expect(dialog.get('[data-test="cleanup-delete-kinds"]').text()).toBe('其中近似重复 1 个、同源视频 1 个、截取片段 1 个。');
    expect(dialog.get('[data-test="cleanup-delete-similarity"]').text()).toContain('3 个是按画面相似度判断的');
    await dialog.get('[data-test="cleanup-delete-cancel"]').trigger('click');
    wrapper.unmount();
  });
});

describe('IMG-05 统一的勾选与锁定规则', () => {
  // §9.2 主代理裁决（P-032 评审 Minor 2）：原保留项往往整理成果最多，换保留项后只解除锁定，不自动勾上。
  it('D-PC49 保留项的勾选框禁用；「设为保留」换保留项后原保留项可勾但不自动勾上，其余成员按新保留项重算', async () => {
    const { wrapper } = await openWith(baseAnalysis({
      duplicate_groups: [{ original: video(1), candidates: [video(2), video(3)], reason: '一致' }]
    }));
    const checkbox = index => wrapper.findAll('[data-test="cleanup-member-select"]')[index].element;
    expect(wrapper.vm.cleanupSelection).toEqual([2, 3]);
    expect(checkbox(0).disabled).toBe(true);
    expect(checkbox(1).disabled).toBe(false);

    await wrapper.findAll('[data-test="cleanup-member-row"]')[1].get('[data-test="cleanup-set-keeper"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.cleanupSelection).toEqual([3]);
    expect(checkbox(0).disabled).toBe(false);
    expect(checkbox(0).checked).toBe(false);
    expect(checkbox(1).disabled).toBe(true);
    expect(wrapper.findAll('[data-test="cleanup-member-row"]')[1].get('[data-test="cleanup-keeper-label"]').text()).toBe('保留：');
    wrapper.unmount();
  });

  // P-032 评审 I-1：删除进行中换保留项、改勾选、移出本组或忽略，都可能让正在删的那一份变成「要保留的」。
  it('D-PC49 删除进行中禁用勾选、「设为保留」「移出本组」和各类忽略，结束后恢复', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    api.MergeMediaMetadata.mockResolvedValue({ warnings: [] });
    let finishTrash;
    const trashVideos = vi.fn(ids => new Promise(resolve => {
      finishTrash = () => resolve({ result: { requested: ids.length, succeeded: ids.length, failed: 0, errors: [] }, failedIDs: new Set(), succeededIDs: [...ids] });
    }));
    const { wrapper } = await openWith(baseAnalysis({
      near_duplicate_groups: [{ original: video(1), candidates: [video(2), video(3)], reason: '接近' }],
      same_source_groups: [{ relation_id: 9, preferred: video(4), alternative: video(5), reason: 'AI 判断同源', estimated_savings: 500 }],
      clip_groups: [{ full: video(6), clip: video(7), offset_seconds: 0, match_rate: 0.9, estimated_savings: 700 }],
      low_duration: [video(8)]
    }), { props: { trashVideos } });
    wrapper.vm.toggleCleanupSelection(2);
    await clickTrash(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(trashVideos).toHaveBeenCalledWith([2], expect.anything());
    expect(wrapper.vm.cleanupDialog.processing).toBe(true);

    const controls = () => wrapper.findAll([
      'input[type="checkbox"]:not([data-test="cleanup-merge-toggle"])',
      '[data-test="cleanup-set-keeper"]', '[data-test="cleanup-remove-member"]', '[data-test="cleanup-suggest-group"]',
      '[data-test="cleanup-dismiss-near-group"]', '[data-test="cleanup-reject-same-source"]',
      '[data-test="cleanup-dismiss-clip"]', '[data-test="cleanup-dismiss-video"]'
    ].join(', '));
    expect(controls().length).toBeGreaterThanOrEqual(12);
    expect(controls().every(node => node.element.disabled)).toBe(true);

    // 界面之外直接调用也不生效。
    const entry = wrapper.vm.cleanupDirectorySections[0].entries.find(item => item.kind === 'near');
    wrapper.vm.setCleanupKeeper(entry, entry.members[1]);
    wrapper.vm.toggleCleanupSelection(3);
    await wrapper.vm.removeNearDuplicateMember(entry.group, entry.members[2]);
    await wrapper.vm.dismissNearDuplicateGroup(entry.group);
    expect(wrapper.vm.cleanupKeepOverrides).toEqual({});
    expect(wrapper.vm.cleanupSelection).toEqual([2]);
    expect(api.DismissNearDuplicateMember).not.toHaveBeenCalled();
    expect(api.DismissNearDuplicateGroup).not.toHaveBeenCalled();

    finishTrash();
    await flushPromises();
    expect(wrapper.vm.cleanupDialog.processing).toBe(false);
    expect(wrapper.findAll('[data-test="cleanup-set-keeper"]').some(node => !node.element.disabled)).toBe(true);
    wrapper.unmount();
  });

  // P-032 评审 I-1：移出的是原保留项时，本地接替的保留项（rest[0]）不能还勾着。
  it('IMG-05「移出本组」移走原保留项时，接替的保留项移出勾选', async () => {
    const { wrapper } = await openWith(baseAnalysis({
      near_duplicate_groups: [{ original: video(1), candidates: [video(2), video(3)], reason: '接近' }]
    }));
    wrapper.vm.toggleCleanupSelection(2);
    wrapper.vm.toggleCleanupSelection(3);
    expect(wrapper.vm.cleanupSelection).toEqual([2, 3]);
    // 回读拿不到新结果，只看本地的处理。
    api.GetCleanupStatus.mockResolvedValue({ running: false, completed: false });

    const entry = wrapper.vm.cleanupDirectorySections[0].entries[0];
    await wrapper.vm.removeNearDuplicateMember(entry.group, entry.members[0]);
    await flushPromises();

    expect(api.DismissNearDuplicateMember).toHaveBeenCalledWith([1, 2, 3], 1);
    expect(wrapper.vm.isCleanupLocked(2)).toBe(true);
    expect(wrapper.vm.cleanupSelection).toEqual([3]);
    wrapper.unmount();
  });

  it('D-PC49「已忽略」页签下不能移到废纸篓', async () => {
    const { wrapper, trashVideos } = await openWith(baseAnalysis({
      duplicate_groups: [{ original: video(1), candidates: [video(2)], reason: '一致' }]
    }));
    api.ListCleanupDismissals.mockResolvedValue({ items: [], next_cursor: 0, has_more: false });
    expect(wrapper.vm.cleanupSelection).toEqual([2]);
    await wrapper.get('[data-test="cleanup-dismissed-tab"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-trash-selected"]').element.disabled).toBe(true);
    await wrapper.vm.trashSelectedCleanupCandidates();
    await flushPromises();
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(false);
    expect(trashVideos).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('D-PC49 目录栏标出每个目录已勾选的数量', async () => {
    const { wrapper } = await openWith(baseAnalysis({
      duplicate_groups: [
        { original: video(1), candidates: [video(2)], reason: '一致' },
        { original: video(3, { directory: '/lib/b' }), candidates: [video(4, { directory: '/lib/b' }), video(5, { directory: '/lib/b' })], reason: '一致' }
      ]
    }));
    const badges = () => wrapper.findAll('[data-test="cleanup-dir-section"]')
      .map(node => node.find('[data-test="cleanup-dir-selected"]'))
      .map(node => (node.exists() ? node.text() : ''));
    expect(badges()).toEqual(['已勾 1', '已勾 2']);
    wrapper.vm.toggleCleanupSelection(2);
    await flushPromises();
    expect(badges()).toEqual(['', '已勾 2']);
    wrapper.unmount();
  });

  it('IMG-05 没全勾的组换保留项时不自动勾选，只解除原保留项的锁定', async () => {
    const { wrapper } = await openWith(baseAnalysis({
      near_duplicate_groups: [{ original: video(1), candidates: [video(2)], reason: '接近' }]
    }));
    const entry = wrapper.vm.cleanupDirectorySections[0].entries[0];
    wrapper.vm.setCleanupKeeper(entry, entry.members[1]);
    expect(wrapper.vm.cleanupSelection).toEqual([]);
    expect(wrapper.vm.isCleanupLocked(1)).toBe(false);
    expect(wrapper.vm.isCleanupLocked(2)).toBe(true);
    wrapper.unmount();
  });

  it('IMG-05 一组的保留项在别的组里也锁定，并标出「另一组要保留它」', async () => {
    const { wrapper } = await openWith(baseAnalysis({
      duplicate_groups: [{ original: video(1), candidates: [video(2)], reason: '一致' }],
      low_resolution: [video(1)]
    }));
    wrapper.vm.cleanupCategory = 'low-resolution';
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-member-locked"]').text()).toBe('另一组要保留它');
    wrapper.vm.toggleCleanupSelection(1);
    expect(wrapper.vm.cleanupSelection).toEqual([2]);
    wrapper.unmount();
  });

  it('IMG-05 近似重复的忽略按钮统一叫「不是重复」', async () => {
    const { wrapper } = await openWith(baseAnalysis({
      near_duplicate_groups: [{ original: video(1), candidates: [video(2)], reason: '接近' }]
    }));
    expect(wrapper.get('[data-test="cleanup-dismiss-near-group"]').text()).toBe('不是重复');
    expect(wrapper.text()).not.toContain('不是同片');
    wrapper.unmount();
  });
});

describe('IMG-07 忽略可控：确认、移出单个成员、「已忽略」与撤销', () => {
  const nearAnalysis = () => baseAnalysis({
    near_duplicate_groups: [{ original: video(1), candidates: [video(2), video(3)], reason: '接近' }]
  });

  it('IMG-07「不是重复」先确认，取消时不写忽略', async () => {
    feedback.confirmAction.mockResolvedValueOnce(false);
    const { wrapper } = await openWith(nearAnalysis());
    await wrapper.get('[data-test="cleanup-dismiss-near-group"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).toHaveBeenCalledWith(expect.objectContaining({ title: '不是重复' }));
    expect(api.DismissNearDuplicateGroup).not.toHaveBeenCalled();
    expect(wrapper.vm.cleanupDialog.analysis.near_duplicate_groups).toHaveLength(1);
    wrapper.unmount();
  });

  it('IMG-07「移出本组」只否决这个成员，其余成员留在组里', async () => {
    const { wrapper } = await openWith(nearAnalysis());
    api.GetCleanupStatus.mockResolvedValue({ completed: true, started_at: 'run-a', analysis: baseAnalysis({
      near_duplicate_groups: [{ original: video(1), candidates: [video(2)], reason: '接近' }]
    }) });
    const rows = wrapper.findAll('[data-test="cleanup-member-row"]');
    expect(rows).toHaveLength(3);
    await rows[2].get('[data-test="cleanup-remove-member"]').trigger('click');
    await flushPromises();

    expect(feedback.confirmAction).toHaveBeenCalledWith(expect.objectContaining({ title: '移出本组' }));
    expect(api.DismissNearDuplicateMember).toHaveBeenCalledWith([1, 2, 3], 3);
    const groups = wrapper.vm.cleanupDialog.analysis.near_duplicate_groups;
    expect(groups).toHaveLength(1);
    expect([groups[0].original.id, ...groups[0].candidates.map(item => item.id)]).toEqual([1, 2]);
    // 两个成员的组不再给「移出本组」（等同于「不是重复」）。
    expect(wrapper.find('[data-test="cleanup-remove-member"]').exists()).toBe(false);
    wrapper.unmount();
  });

  it('IMG-07「移出本组」取消确认时什么都不写', async () => {
    feedback.confirmAction.mockResolvedValueOnce(false);
    const { wrapper } = await openWith(nearAnalysis());
    await wrapper.findAll('[data-test="cleanup-remove-member"]')[0].trigger('click');
    await flushPromises();
    expect(api.DismissNearDuplicateMember).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('IMG-07「已忽略」页签按类别列出记录，可撤销、可加载更多', async () => {
    api.ListCleanupDismissals.mockImplementation(async (kind, cursor) => {
      if (kind === 'near_duplicate' && cursor === 0) {
        return { items: [{ id: 30, kind, media: [{ id: 1, name: 'a.mp4' }, { id: 2, name: 'b.mp4', missing: true }], created_at: '2026-09-29T10:00:00Z' }], next_cursor: 30, has_more: true };
      }
      if (kind === 'near_duplicate') {
        return { items: [{ id: 12, kind, media: [{ id: 3, name: 'c.mp4' }, { id: 4, name: 'd.mp4' }], created_at: '2026-09-28T10:00:00Z' }], next_cursor: 12, has_more: false };
      }
      return { items: [{ id: 5, kind, media: [{ id: 9, name: 'short.mov' }], created_at: '' }], next_cursor: 5, has_more: false };
    });
    api.UndoCleanupDismissals.mockResolvedValue({ kind: 'near_duplicate', removed: 1 });
    const { wrapper } = await openWith(nearAnalysis());

    await wrapper.get('[data-test="cleanup-dismissed-tab"]').trigger('click');
    await flushPromises();
    expect(api.ListCleanupDismissals).toHaveBeenCalledWith('near_duplicate', 0, 50);
    const items = () => wrapper.findAll('[data-test="cleanup-dismissal-item"]');
    expect(items()).toHaveLength(1);
    expect(items()[0].text()).toContain('「a.mp4」与「b.mp4（已删除）」');

    await wrapper.get('[data-test="cleanup-dismissed-more"]').trigger('click');
    await flushPromises();
    expect(api.ListCleanupDismissals).toHaveBeenLastCalledWith('near_duplicate', 30, 50);
    expect(items()).toHaveLength(2);

    await items()[0].get('[data-test="cleanup-dismissal-undo"]').trigger('click');
    await flushPromises();
    expect(api.UndoCleanupDismissals).toHaveBeenCalledWith('near_duplicate', [30]);
    expect(items()).toHaveLength(1);
    expect(feedback.notifySuccess.mock.calls.map(call => String(call[0])).join('\n')).toContain('已撤销忽略');

    const shortKind = wrapper.findAll('[data-test="cleanup-dismissed-kind"]').find(node => node.text() === '极短片段');
    await shortKind.trigger('click');
    await flushPromises();
    expect(api.ListCleanupDismissals).toHaveBeenLastCalledWith('short', 0, 50);
    expect(items()[0].text()).toContain('「short.mov」');
    wrapper.unmount();
  });

  it('IMG-07 撤销失败要报出来，记录留在列表里', async () => {
    api.ListCleanupDismissals.mockResolvedValue({ items: [{ id: 30, kind: 'clip', media: [{ id: 1, name: 'full.mkv' }, { id: 2, name: 'clip.mp4' }] }], next_cursor: 30, has_more: false });
    api.UndoCleanupDismissals.mockRejectedValue(new Error('数据库繁忙'));
    const { wrapper } = await openWith(nearAnalysis());
    await wrapper.get('[data-test="cleanup-dismissed-tab"]').trigger('click');
    await flushPromises();
    wrapper.vm.switchDismissalKind('clip');
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-dismissal-item"]').text()).toContain('片段「clip.mp4」 · 完整片「full.mkv」');
    await wrapper.get('[data-test="cleanup-dismissal-undo"]').trigger('click');
    await flushPromises();
    expect(feedback.notifyError.mock.calls.map(call => String(call[0])).join('\n')).toContain('撤销忽略失败');
    expect(wrapper.findAll('[data-test="cleanup-dismissal-item"]')).toHaveLength(1);
    wrapper.unmount();
  });
});

describe('APP-11 极短片段 / 极低分辨率也可以忽略', () => {
  it('APP-11 忽略极短片段：确认后写 short 类忽略，候选与徽标随之消退', async () => {
    const { wrapper } = await openWith(baseAnalysis({ low_duration: [video(7)], low_resolution: [video(8)] }));
    expect(wrapper.emitted('badge-change').at(-1)).toEqual([2]);
    wrapper.vm.cleanupCategory = 'low-duration';
    await flushPromises();
    await wrapper.get('[data-test="cleanup-dismiss-video"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).toHaveBeenCalledWith(expect.objectContaining({ title: '忽略极短片段候选' }));
    expect(api.DismissCleanupVideo).toHaveBeenCalledWith(7, 'short');
    expect(wrapper.vm.cleanupDialog.analysis.low_duration).toEqual([]);
    expect(wrapper.emitted('badge-change').at(-1)).toEqual([1]);

    wrapper.vm.cleanupCategory = 'low-resolution';
    await flushPromises();
    await wrapper.get('[data-test="cleanup-dismiss-video"]').trigger('click');
    await flushPromises();
    expect(api.DismissCleanupVideo).toHaveBeenCalledWith(8, 'low');
    wrapper.unmount();
  });
});

describe('IMG-11 覆盖率与空态', () => {
  const coverage = (near, clip, same) => ({
    perceptual_hash: { done: near[0], total: near[1] },
    frame_hash: { done: clip[0], total: clip[1] },
    same_source: { evaluated: same[0], total: same[1] }
  });

  it('IMG-11 只算了一部分时类别上标「已算 X / Y」，完全没算时标「尚未计算」', async () => {
    const { wrapper } = await openWith(baseAnalysis({ coverage: coverage([3, 10], [0, 10], [10, 10]) }));
    const chip = key => wrapper.vm.cleanupCategoryOptions.find(option => option.key === key);
    expect(chip('near').coverage).toBe('已算 3 / 10');
    expect(chip('clip').coverage).toBe('尚未计算');
    expect(chip('same-source').coverage).toBe('');
    expect(wrapper.findAll('[data-test="cleanup-coverage-chip"]').map(node => node.text())).toEqual(['已算 3 / 10', '尚未计算']);
    wrapper.unmount();
  });

  it('IMG-11 某类别一个都没算时空态写「尚未计算」并给出补全入口，同源说明来自 AI 打标', async () => {
    const { wrapper } = await openWith(baseAnalysis({ coverage: coverage([0, 10], [0, 10], [0, 10]) }));
    wrapper.vm.cleanupCategory = 'near';
    await flushPromises();
    const empty = () => wrapper.get('[data-test="cleanup-empty-state"]');
    expect(empty().text()).toContain('尚未计算');
    await empty().get('[data-test="cleanup-empty-start-perceptual-hash"]').trigger('click');
    expect(wrapper.emitted('start-perceptual-hash')).toHaveLength(1);

    wrapper.vm.cleanupCategory = 'clip';
    await flushPromises();
    await empty().get('[data-test="cleanup-empty-start-frame-hash"]').trigger('click');
    expect(wrapper.emitted('start-frame-hash')).toHaveLength(1);

    wrapper.vm.cleanupCategory = 'same-source';
    await flushPromises();
    expect(empty().text()).toContain('AI 打标的「查找同源」');

    wrapper.vm.cleanupCategory = 'all';
    await flushPromises();
    expect(empty().text()).toContain('当前没有命中轻量清理规则的候选项');
    expect(empty().text()).toContain('近似重复尚未计算');
    wrapper.unmount();
  });

  it('IMG-11 全部算完仍为空时才说「没有发现」，部分覆盖时附上已算比例', async () => {
    const { wrapper } = await openWith(baseAnalysis({ coverage: coverage([10, 10], [4, 10], [10, 10]) }));
    wrapper.vm.cleanupCategory = 'near';
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-empty-state"]').text()).toBe('没有发现近似重复候选。');
    wrapper.vm.cleanupCategory = 'clip';
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-empty-state"]').text()).toContain('帧哈希已算 4 / 10');
    wrapper.unmount();
  });
});

describe('META-10 类别改名并标出设置里的阈值', () => {
  it('META-10 标题与说明按本轮阈值显示「极低分辨率 / 极短片段」', async () => {
    const { wrapper } = await openWith(baseAnalysis({
      low_resolution: [video(7)], low_duration: [video(8)],
      thresholds: { short_seconds: 12, low_width: 640, low_height: 360 }
    }));
    const option = key => wrapper.vm.cleanupCategoryOptions.find(item => item.key === key);
    expect(option('low-resolution').label).toBe('极低分辨率（< 640×360）');
    expect(option('low-duration').label).toBe('极短片段（< 12 秒）');
    expect(option('low-resolution').hint).toContain('分辨率低于 640×360');
    expect(wrapper.findAll('.cleanup-card-kind').map(node => node.text())).toEqual(['极低分辨率（< 640×360）', '极短片段（< 12 秒）']);
    wrapper.unmount();
  });

  it('META-10 重新分析改用设置里的阈值启动，前端不再写死 5 / 480 / 320', async () => {
    api.StartCleanupAnalysisFromSettings.mockResolvedValue({ running: true, progress: { stage: 'load' } });
    const { wrapper } = await openWith(baseAnalysis());
    await wrapper.vm.reanalyzeCleanupCandidates();
    expect(api.StartCleanupAnalysisFromSettings).toHaveBeenCalledTimes(1);
    expect(api.StartCleanupAnalysisFromSettings).toHaveBeenCalledWith();
    wrapper.unmount();
  });
});

describe('IMG-12 清理分析可以取消', () => {
  it('IMG-12 分析中点「取消分析」调用 CancelCleanupAnalysis，停下后显示「已取消」，重开也不自动重跑', async () => {
    api.GetCleanupStatus.mockResolvedValue({ running: true, completed: false, progress: { stage: 'hash', current: 1, total: 4 } });
    const wrapper = mountPanel({ deep: true, attachTo: document.body });
    await flushPromises();
    await wrapper.vm.open();
    await flushPromises();

    await wrapper.get('[data-test="cleanup-cancel-analysis"]').trigger('click');
    await flushPromises();
    expect(api.CancelCleanupAnalysis).toHaveBeenCalledTimes(1);
    expect(wrapper.get('[data-test="cleanup-cancel-analysis"]').text()).toBe('正在取消…');

    api.GetCleanupStatus.mockResolvedValue({ running: false, completed: false, cancelled: true, progress: { stage: 'done' } });
    await wrapper.vm.open();
    await flushPromises();
    expect(wrapper.find('[data-test="cleanup-cancelled"]').exists()).toBe(true);
    expect(api.StartCleanupAnalysisFromSettings).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it('IMG-12 取消失败时说出来，按钮恢复可点', async () => {
    api.GetCleanupStatus.mockResolvedValue({ running: true, completed: false, progress: { stage: 'hash' } });
    api.CancelCleanupAnalysis.mockRejectedValue(new Error('清理分析未在运行'));
    const wrapper = mountPanel({ deep: true, attachTo: document.body });
    await flushPromises();
    await wrapper.vm.open();
    await flushPromises();
    await wrapper.get('[data-test="cleanup-cancel-analysis"]').trigger('click');
    await flushPromises();
    expect(feedback.notifyError.mock.calls.map(call => String(call[0])).join('\n')).toContain('取消清理分析失败');
    expect(wrapper.get('[data-test="cleanup-cancel-analysis"]').element.disabled).toBe(false);
    wrapper.unmount();
  });
});

describe('「去清理」定位同源对与 D-PC01 文案', () => {
  const sameSourceAnalysis = () => baseAnalysis({
    duplicate_groups: [{ original: video(1, { directory: '/lib/a' }), candidates: [video(2, { directory: '/lib/a' })], reason: '一致' }],
    same_source_groups: [{ relation_id: 42, preferred: video(5, { directory: '/lib/z' }), alternative: video(6, { directory: '/lib/z' }), reason: 'AI 判断同源', estimated_savings: 600 }]
  });

  it('APP-11 open({ relationId }) 切到同源类别与那一对所在的目录，并标出这张卡', async () => {
    api.GetCleanupStatus.mockResolvedValue({ completed: true, started_at: 'run-f', analysis: sameSourceAnalysis() });
    const wrapper = mountPanel({ deep: true, attachTo: document.body });
    await flushPromises();
    await wrapper.vm.open({ relationId: 42, videoIds: [5, 6] });
    await flushPromises();
    expect(wrapper.vm.cleanupCategory).toBe('same-source');
    expect(wrapper.vm.activeCleanupDirectory).toBe('/lib/z');
    expect(wrapper.get('[data-test="cleanup-group-card"][data-focused="true"]').attributes('data-kind')).toBe('same-source');
    wrapper.unmount();
  });

  it('META-08 已在 AI 审阅里确认的同源组标出「已确认同源」，未确认的不标', async () => {
    const analysis = sameSourceAnalysis();
    analysis.same_source_groups.push({ relation_id: 43, preferred: video(7, { directory: '/lib/z' }), alternative: video(8, { directory: '/lib/z' }), reason: 'AI 判断同源', estimated_savings: 100, confirmed: true });
    api.GetCleanupStatus.mockResolvedValue({ completed: true, started_at: 'run-g', analysis });
    const wrapper = mountPanel({ deep: true, attachTo: document.body });
    await flushPromises();
    await wrapper.vm.open({ relationId: 43 });
    await flushPromises();
    const badges = wrapper.findAll('[data-test="cleanup-same-source-confirmed"]');
    expect(badges).toHaveLength(1);
    expect(badges[0].text()).toBe('已确认同源');
    wrapper.unmount();
  });

  it('APP-11 要定位的那一对不在当前结果里时给出说明，不静默', async () => {
    api.GetCleanupStatus.mockResolvedValue({ completed: true, started_at: 'run-f', analysis: sameSourceAnalysis() });
    const wrapper = mountPanel({ deep: true, attachTo: document.body });
    await flushPromises();
    await wrapper.vm.open({ relationId: 77 });
    await flushPromises();
    expect(wrapper.get('[data-test="cleanup-focus-missing"]').text()).toContain('重新分析');
    wrapper.unmount();
  });

  it('LIB-05 底栏与按钮说「移到废纸篓」，可释放空间写明要在访达清空废纸篓', async () => {
    const { wrapper } = await openWith(sameSourceAnalysis());
    const text = wrapper.text();
    expect(text).toContain('移到废纸篓后，在访达清空废纸篓即可释放约');
    expect(wrapper.get('[data-test="cleanup-trash-selected"]').text()).toBe('移到废纸篓');
    expect(text).not.toContain('移入回收站');
    wrapper.unmount();
  });
});

// ===== P-032 复审（修复 Q）：确认框打开期间与改分组请求进行中（I-1）、移出本组的保留项覆盖（m1）、
// 删除后裁剪与合并失败提示（m2 / m3） =====
describe('D-PC49 确认框打开期间与改分组请求进行中（P-032 复审 I-1）', () => {
  const CHANGED = '确认期间清理结果、勾选或保留项有变化，请重新确认';
  const errorText = () => feedback.notifyError.mock.calls.map(call => String(call[0])).join('\n');
  const nearEntryOf = wrapper => wrapper.vm.cleanupDirectorySections
    .flatMap(section => section.entries).find(entry => entry.kind === 'near');
  const selectionControls = wrapper => wrapper.findAll([
    'input[type="checkbox"]:not([data-test="cleanup-merge-toggle"])',
    '[data-test="cleanup-set-keeper"]', '[data-test="cleanup-remove-member"]', '[data-test="cleanup-suggest-group"]',
    '[data-test="cleanup-dismiss-near-group"]', '[data-test="cleanup-reject-same-source"]',
    '[data-test="cleanup-dismiss-clip"]', '[data-test="cleanup-dismiss-video"]'
  ].join(', '));

  it('D-PC49 确认框打开期间勾选、「设为保留」「移出本组」与各类忽略都禁用并拦截；状态没变时确认后照常按原名单删除', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    api.MergeMediaMetadata.mockResolvedValue({ warnings: [] });
    const { wrapper, trashVideos } = await openWith(baseAnalysis({
      near_duplicate_groups: [{ original: video(1), candidates: [video(2), video(3)], reason: '接近' }],
      same_source_groups: [{ relation_id: 9, preferred: video(4), alternative: video(5), reason: 'AI 判断同源', estimated_savings: 500 }],
      clip_groups: [{ full: video(6), clip: video(7), offset_seconds: 0, match_rate: 0.9, estimated_savings: 700 }],
      low_duration: [video(8)]
    }));
    wrapper.vm.toggleCleanupSelection(2);
    await clickTrash(wrapper);
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(true);

    expect(selectionControls(wrapper).length).toBeGreaterThanOrEqual(12);
    expect(selectionControls(wrapper).every(node => node.element.disabled)).toBe(true);
    // 确认框打开时底栏按钮仍写「移到废纸篓」，还没有开始处理。
    expect(wrapper.get('[data-test="cleanup-trash-selected"]').text()).toBe('移到废纸篓');

    // 界面之外直接调用同样被拦下：「设为保留」、勾选、按建议勾选、移出本组、各类忽略。
    const entry = nearEntryOf(wrapper);
    wrapper.vm.setCleanupKeeper(entry, entry.members[1]);
    wrapper.vm.toggleCleanupSelection(3);
    wrapper.vm.toggleEntrySuggestion(entry);
    wrapper.vm.clearCleanupSelection();
    await wrapper.vm.removeNearDuplicateMember(entry.group, entry.members[2]);
    await wrapper.vm.dismissNearDuplicateGroup(entry.group);
    await wrapper.vm.dismissCleanupVideo(wrapper.vm.cleanupDirectorySections[0].entries.find(item => item.kind === 'low-duration'));
    expect(wrapper.vm.cleanupKeepOverrides).toEqual({});
    expect(wrapper.vm.cleanupSelection).toEqual([2]);
    expect(feedback.confirmAction).not.toHaveBeenCalled();
    expect(api.DismissNearDuplicateMember).not.toHaveBeenCalled();
    expect(api.DismissNearDuplicateGroup).not.toHaveBeenCalled();
    expect(api.DismissCleanupVideo).not.toHaveBeenCalled();

    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.MergeMediaMetadata.mock.calls).toEqual([['video', 1, [2], FULL_MERGE]]);
    expect(trashVideos).toHaveBeenCalledWith([2], { names: { 2: 'v2.mp4' } });
    expect(errorText()).not.toContain(CHANGED);
    expect(wrapper.vm.cleanupDialog.processing).toBe(false);
    wrapper.unmount();
  });

  // 场景 A：确认框打开期间，保留项被后台回读替换（同一轮分析，建议保留项换了）。
  it('D-PC49 确认框打开期间分析结果被替换、保留项变了：确认后不合并、不删除，提示重新确认，勾选保持新的', async () => {
    const { wrapper, trashVideos } = await openWith(baseAnalysis({
      duplicate_groups: [{ original: video(1), candidates: [video(2), video(3)], reason: '一致' }]
    }));
    expect(wrapper.vm.cleanupSelection).toEqual([2, 3]);
    await clickTrash(wrapper);

    wrapper.vm.applyCleanupStatus({ completed: true, started_at: 'run-a', analysis: baseAnalysis({
      duplicate_groups: [{ original: video(2), candidates: [video(1), video(3)], reason: '一致' }]
    }) });
    await flushPromises();
    expect(wrapper.vm.cleanupSelection).toEqual([3]);

    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.MergeMediaMetadata).not.toHaveBeenCalled();
    expect(trashVideos).not.toHaveBeenCalled();
    expect(errorText()).toContain(CHANGED);
    expect(wrapper.vm.cleanupSelection).toEqual([3]);
    expect(wrapper.vm.cleanupDialog.processing).toBe(false);
    expect(wrapper.emitted('trash-settled')).toBeUndefined();
    wrapper.unmount();
  });

  // 场景 A：「设为保留」在确认期间生效（绕过界面锁直接写入覆盖）。待删名单没变，但保留项变了，同样中止。
  it('D-PC49 确认框打开期间「设为保留」生效（待删名单不变、保留项换了）：确认后不合并、不删除', async () => {
    const { wrapper, trashVideos } = await openWith(baseAnalysis({
      duplicate_groups: [{ original: video(1), candidates: [video(2), video(3)], reason: '一致' }]
    }));
    wrapper.vm.toggleCleanupSelection(2);
    expect(wrapper.vm.cleanupSelection).toEqual([3]);
    await clickTrash(wrapper);
    expect(wrapper.get('[data-test="cleanup-delete-summary"]').text()).toContain('将把 1 个视频移到废纸篓');

    wrapper.vm.cleanupKeepOverrides = { 'exact-1': 2 };
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.MergeMediaMetadata).not.toHaveBeenCalled();
    expect(trashVideos).not.toHaveBeenCalled();
    expect(errorText()).toContain(CHANGED);
    expect(wrapper.vm.cleanupSelection).toEqual([3]);
    wrapper.unmount();
  });

  // 场景 B：「移出本组」的请求还没返回。
  it('D-PC49「移出本组」请求没返回时「移到废纸篓」禁用，直接调用删除入口也被拦截；返回后恢复', async () => {
    const { wrapper, trashVideos } = await openWith(baseAnalysis({
      duplicate_groups: [{ original: video(10), candidates: [video(11)], reason: '一致' }],
      near_duplicate_groups: [{ original: video(1), candidates: [video(2), video(3)], reason: '接近' }]
    }));
    expect(wrapper.vm.cleanupSelection).toEqual([11]);
    api.GetCleanupStatus.mockResolvedValue({ running: false, completed: false });
    let finishDismiss;
    api.DismissNearDuplicateMember.mockImplementationOnce(() => new Promise(resolve => { finishDismiss = resolve; }));

    const entry = nearEntryOf(wrapper);
    const pending = wrapper.vm.removeNearDuplicateMember(entry.group, entry.members[2]);
    await flushPromises();
    expect(api.DismissNearDuplicateMember).toHaveBeenCalledWith([1, 2, 3], 3);
    const button = () => wrapper.get('[data-test="cleanup-trash-selected"]');
    expect(button().element.disabled).toBe(true);
    expect(button().attributes('title')).toBe('正在更新分组，完成后再删除');

    await wrapper.vm.trashSelectedCleanupCandidates();
    await flushPromises();
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(false);
    expect(trashVideos).not.toHaveBeenCalled();

    finishDismiss();
    await pending;
    await flushPromises();
    expect(button().element.disabled).toBe(false);
    await clickTrash(wrapper);
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(true);
    await wrapper.get('[data-test="cleanup-delete-cancel"]').trigger('click');
    wrapper.unmount();
  });

  it('D-PC49 等「移出本组」确认的时候打开了删除确认框：之后再确认移出也不发请求', async () => {
    const { wrapper } = await openWith(baseAnalysis({
      duplicate_groups: [{ original: video(10), candidates: [video(11)], reason: '一致' }],
      near_duplicate_groups: [{ original: video(1), candidates: [video(2), video(3)], reason: '接近' }]
    }));
    let answerRemove;
    feedback.confirmAction.mockImplementationOnce(() => new Promise(resolve => { answerRemove = resolve; }));
    const entry = nearEntryOf(wrapper);
    const pending = wrapper.vm.removeNearDuplicateMember(entry.group, entry.members[2]);
    await clickTrash(wrapper);
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(true);

    answerRemove(true);
    await pending;
    await flushPromises();
    expect(api.DismissNearDuplicateMember).not.toHaveBeenCalled();
    expect(wrapper.vm.cleanupGroupRequests).toBe(0);
    await wrapper.get('[data-test="cleanup-delete-cancel"]').trigger('click');
    wrapper.unmount();
  });

  // P-032 复审 m1：组 key 跟着原保留项走，移出原保留项后用户的「设为保留」不能丢。
  it('IMG-05「移出本组」移走原保留项后，用户「设为保留」的那一份仍是保留项并锁定，按建议勾选不会勾上它', async () => {
    const { wrapper } = await openWith(baseAnalysis({
      near_duplicate_groups: [{ original: video(1), candidates: [video(2), video(3), video(4)], reason: '接近' }]
    }));
    api.GetCleanupStatus.mockResolvedValue({ running: false, completed: false });
    const entry = () => nearEntryOf(wrapper);
    wrapper.vm.setCleanupKeeper(entry(), entry().members[2]);
    expect(wrapper.vm.cleanupKeepOverrides).toEqual({ 'near-1': 3 });

    await wrapper.vm.removeNearDuplicateMember(entry().group, entry().members[0]);
    await flushPromises();
    expect(api.DismissNearDuplicateMember).toHaveBeenCalledWith([1, 2, 3, 4], 1);
    expect(entry().key).toBe('near-2');
    expect(wrapper.vm.cleanupKeepOverrides).toEqual({ 'near-2': 3 });
    expect(wrapper.vm.isEntryKeeper(entry(), { id: 3 })).toBe(true);
    expect(wrapper.vm.isCleanupLocked(3)).toBe(true);
    expect(wrapper.vm.isCleanupLocked(2)).toBe(false);

    wrapper.vm.toggleEntrySuggestion(entry());
    expect(wrapper.vm.cleanupSelection).toEqual([2, 4]);
    wrapper.unmount();
  });

  it('IMG-05「移出本组」移走的正是用户设的保留项时，覆盖作废，回到建议保留项', async () => {
    const { wrapper } = await openWith(baseAnalysis({
      near_duplicate_groups: [{ original: video(1), candidates: [video(2), video(3)], reason: '接近' }]
    }));
    api.GetCleanupStatus.mockResolvedValue({ running: false, completed: false });
    const entry = () => nearEntryOf(wrapper);
    wrapper.vm.setCleanupKeeper(entry(), entry().members[2]);
    await wrapper.vm.removeNearDuplicateMember(entry().group, entry().members[2]);
    await flushPromises();
    expect(wrapper.vm.cleanupKeepOverrides).toEqual({});
    expect(wrapper.vm.isEntryKeeper(entry(), { id: 1 })).toBe(true);
    wrapper.unmount();
  });

  // P-032 复审 m2：删除期间结果被替换，失败项成了保留项；删除后的勾选要按当前的组裁掉它。
  it('IMG-03 删除有失败项、删除期间结果被替换让失败项成了保留项：删除后的勾选裁掉它', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    api.MergeMediaMetadata.mockResolvedValue({ warnings: [] });
    let mounted = null;
    const trashVideos = vi.fn(async (ids) => {
      mounted.vm.applyCleanupStatus({ completed: true, started_at: 'run-a', analysis: baseAnalysis({
        duplicate_groups: [{ original: video(2), candidates: [video(1), video(3)], reason: '一致' }]
      }) });
      return {
        result: { requested: ids.length, succeeded: 1, failed: 1, errors: [{ video_id: 2, error: '文件被占用' }] },
        failedIDs: new Set([2]),
        succeededIDs: [3]
      };
    });
    const { wrapper } = await openWith(baseAnalysis({
      duplicate_groups: [{ original: video(1), candidates: [video(2), video(3)], reason: '一致' }]
    }), { props: { trashVideos } });
    mounted = wrapper;
    expect(wrapper.vm.cleanupSelection).toEqual([2, 3]);

    await clickTrash(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(trashVideos).toHaveBeenCalledWith([2, 3], expect.anything());
    expect(wrapper.vm.isCleanupLocked(2)).toBe(true);
    expect(wrapper.vm.cleanupSelection).toEqual([]);
    expect(errorText()).toContain('失败 1 个');
    wrapper.unmount();
  });

  // P-032 复审 m3：第二组合并已提交、同步已看失败；第一组的字幕提示也要给出。
  it('IMG-03 第二组合并已提交但同步已看失败：用保留项文件名说明那一组已合并、字幕未复制，第 1 组的提示也列出，一个都不删', async () => {
    api.MergeMediaMetadata
      .mockResolvedValueOnce({ warnings: ['字幕迁移失败：目标文件已存在（字幕仍在被合并项旁边）'] })
      .mockRejectedValueOnce('merge_committed: 合并已看状态失败：数据库繁忙');
    const { wrapper, trashVideos } = await openWith(baseAnalysis({
      duplicate_groups: [
        { original: video(1), candidates: [video(2)], reason: '一致' },
        { original: video(3), candidates: [video(4)], reason: '一致' }
      ]
    }));
    await clickTrash(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();

    expect(api.MergeMediaMetadata).toHaveBeenCalledTimes(2);
    expect(trashVideos).not.toHaveBeenCalled();
    const message = errorText();
    expect(message).toContain('保留「v3.mp4」的那一组的整理成果已合并到保留项，但同步已看状态失败（合并已看状态失败：数据库繁忙），字幕未复制');
    expect(message).not.toContain('第 2 组');
    expect(message).not.toContain('/lib/a');
    expect(message).toContain('没有删除任何视频');
    expect(message).toContain('共 2 组，前 1 组已经合并到各自的保留项');
    expect(message).toContain('已合并的组另有 1 条提示：字幕迁移失败：目标文件已存在');
    expect(message).not.toContain('merge_committed');
    expect(wrapper.vm.cleanupSelection).toEqual([2, 4]);
    expect(wrapper.vm.cleanupDialog.processing).toBe(false);
    wrapper.unmount();
  });
});

// ===== P-032 复审（修复 R）：「移出本组」后接替的保留项不能是已经移到废纸篓的那一份（I-a） =====
describe('D-PC49「移出本组」后的接替保留项（P-032 复审 I-a）', () => {
  const nearEntryOf = wrapper => wrapper.vm.cleanupDirectorySections
    .flatMap(section => section.entries).find(entry => entry.kind === 'near');

  // 勾上这几个、删掉；删除后的「重新分析」由调用方的 confirmAction 替身选取消，结果留着继续审阅。
  async function trashSelection(wrapper, ids) {
    wrapper.vm.cleanupSelection = ids;
    await clickTrash(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
  }

  it('IMG-05 近似组 {1,2,3,4} 先删 2 再移出 1：接替者是 3 而不是 2，合并调用的保留项是 3', async () => {
    api.MergeMediaMetadata.mockResolvedValue({ warnings: [] });
    feedback.confirmAction.mockResolvedValue(false).mockResolvedValueOnce(false).mockResolvedValueOnce(true);
    const { wrapper, trashVideos } = await openWith(baseAnalysis({
      near_duplicate_groups: [{ original: video(1), candidates: [video(2), video(3), video(4)], reason: '接近' }]
    }));
    await trashSelection(wrapper, [2]);
    expect(api.MergeMediaMetadata.mock.calls).toEqual([['video', 1, [2], FULL_MERGE]]);
    expect(wrapper.vm.cleanupTrashedIDs).toEqual([2]);

    // 回读拿到的是后端拆组后的结果：原片（组 key）按推荐顺序是 2，与本地更新一致；保留项由前端接替。
    api.GetCleanupStatus.mockResolvedValue({ running: false, completed: true, started_at: 'run-a', analysis: baseAnalysis({
      near_duplicate_groups: [{ original: video(2), candidates: [video(3), video(4)], reason: '接近' }]
    }) });
    await wrapper.vm.removeNearDuplicateMember(nearEntryOf(wrapper).group, { id: 1, name: 'v1.mp4' });
    await flushPromises();
    expect(api.DismissNearDuplicateMember).toHaveBeenCalledWith([1, 2, 3, 4], 1);
    const entry = nearEntryOf(wrapper);
    expect(entry.key).toBe('near-2');
    expect(wrapper.vm.isEntryKeeper(entry, { id: 3 })).toBe(true);
    expect(wrapper.vm.isEntryKeeper(entry, { id: 2 })).toBe(false);
    expect(wrapper.vm.isCleanupLocked(3)).toBe(true);
    expect(wrapper.find('[data-test="cleanup-group-exhausted"]').exists()).toBe(false);
    const labels = wrapper.findAll('[data-test="cleanup-keeper-label"]');
    expect(labels.map(node => node.text())).toEqual(['建议保留：']);
    expect(labels[0].element.closest('[data-test="cleanup-member-row"]').textContent).toContain('v3.mp4');

    api.MergeMediaMetadata.mockClear();
    trashVideos.mockClear();
    await trashSelection(wrapper, [4]);
    expect(api.MergeMediaMetadata.mock.calls).toEqual([['video', 3, [4], FULL_MERGE]]);
    expect(trashVideos).toHaveBeenCalledWith([4], expect.anything());
    wrapper.unmount();
  });

  it('IMG-05 用户设的保留项被移出、原片已删：接替者是仍在库的第一个成员', async () => {
    api.MergeMediaMetadata.mockResolvedValue({ warnings: [] });
    feedback.confirmAction.mockResolvedValue(false).mockResolvedValueOnce(false).mockResolvedValueOnce(true);
    const { wrapper } = await openWith(baseAnalysis({
      near_duplicate_groups: [{ original: video(1), candidates: [video(2), video(3), video(4)], reason: '接近' }]
    }));
    api.GetCleanupStatus.mockResolvedValue({ running: false, completed: false });
    wrapper.vm.setCleanupKeeper(nearEntryOf(wrapper), { id: 3 });
    await trashSelection(wrapper, [1]);
    expect(api.MergeMediaMetadata.mock.calls).toEqual([['video', 3, [1], FULL_MERGE]]);

    await wrapper.vm.removeNearDuplicateMember(nearEntryOf(wrapper).group, { id: 3, name: 'v3.mp4' });
    await flushPromises();
    const entry = nearEntryOf(wrapper);
    expect(entry.key).toBe('near-1');
    expect(wrapper.vm.cleanupKeepOverrides).toEqual({});
    expect(wrapper.vm.isEntryKeeper(entry, { id: 2 })).toBe(true);
    expect(wrapper.vm.isEntryKeeper(entry, { id: 1 })).toBe(false);
    expect(wrapper.vm.isCleanupLocked(2)).toBe(true);
    wrapper.unmount();
  });

  it('IMG-05 移出后本组剩下的都已移到废纸篓：组上说明原因，成员不可勾，不进入合并与删除', async () => {
    api.MergeMediaMetadata.mockResolvedValue({ warnings: [] });
    feedback.confirmAction.mockResolvedValue(false).mockResolvedValueOnce(false).mockResolvedValueOnce(true);
    const exactGroup = { original: video(10), candidates: [video(11)], reason: '一致' };
    const { wrapper, trashVideos } = await openWith(baseAnalysis({
      duplicate_groups: [exactGroup],
      near_duplicate_groups: [{ original: video(1), candidates: [video(2), video(3)], reason: '接近' }]
    }));
    await trashSelection(wrapper, [2, 3]);
    expect(api.MergeMediaMetadata.mock.calls).toEqual([['video', 1, [2, 3], FULL_MERGE]]);

    api.GetCleanupStatus.mockResolvedValue({ running: false, completed: true, started_at: 'run-a', analysis: baseAnalysis({
      duplicate_groups: [exactGroup],
      near_duplicate_groups: [{ original: video(2), candidates: [video(3)], reason: '接近' }]
    }) });
    await wrapper.vm.removeNearDuplicateMember(nearEntryOf(wrapper).group, { id: 1, name: 'v1.mp4' });
    await flushPromises();
    const entry = nearEntryOf(wrapper);
    expect(entry.members.map(member => member.id)).toEqual([2, 3]);
    expect(wrapper.vm.isEntryExhausted(entry)).toBe(true);
    const card = wrapper.findAll('[data-test="cleanup-group-card"]').find(node => node.find('[data-test="cleanup-group-exhausted"]').exists());
    expect(card.get('[data-test="cleanup-group-exhausted"]').text()).toContain('本组剩下的都已移到废纸篓');
    expect(card.findAll('[data-test="cleanup-member-select"]').every(node => node.element.disabled)).toBe(true);
    expect(card.get('[data-test="cleanup-suggest-group"]').element.disabled).toBe(true);
    expect(card.find('[data-test="cleanup-keeper-label"]').exists()).toBe(false);
    expect(card.find('[data-test="cleanup-member-locked"]').exists()).toBe(false);
    expect(wrapper.vm.isCleanupLocked(2) && wrapper.vm.isCleanupLocked(3)).toBe(true);

    // 强行把这组成员放回勾选：删除前被裁掉，只删别的组，合并也只按别的组。
    api.MergeMediaMetadata.mockClear();
    trashVideos.mockClear();
    await trashSelection(wrapper, [2, 3, 11]);
    expect(api.MergeMediaMetadata.mock.calls).toEqual([['video', 10, [11], FULL_MERGE]]);
    expect(trashVideos).toHaveBeenCalledWith([11], expect.anything());
    wrapper.unmount();
  });
});

// ===== P-032 复审（修复 R）Minor：确认框打开或删除进行中不能关掉外层面板 =====
describe('D-PC49 删除流程中的面板关闭保护（P-032 复审 Minor）', () => {
  const LOCKED = '正在移到废纸篓，完成后再关闭';
  const pressEscape = async () => {
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await flushPromises();
  };
  const lockNotices = () => feedback.notify.mock.calls.filter(call => String(call[0]).includes(LOCKED));

  it('D-PC49 确认框打开或删除进行中：✕ 与「取消」禁用，关闭方法与 Esc 被拦下并提示；结束后恢复', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    const { wrapper } = await openWith(baseAnalysis({
      duplicate_groups: [{ original: video(1), candidates: [video(2)], reason: '一致' }]
    }));
    const close = () => wrapper.get('[data-test="cleanup-close"]');
    const cancel = () => wrapper.get('[data-test="cleanup-cancel"]');
    expect(close().element.disabled).toBe(false);

    // 确认框打开：按钮禁用；直接调用关闭方法被拦下并提示。
    await clickTrash(wrapper);
    expect(close().element.disabled).toBe(true);
    expect(cancel().element.disabled).toBe(true);
    expect(close().attributes('title')).toBe(LOCKED);
    expect(wrapper.vm.closeCleanupDialog()).toBe(false);
    expect(wrapper.vm.cleanupDialog.show).toBe(true);
    expect(lockNotices()).toHaveLength(1);

    // Esc 只取消确认框，面板留着，也不再提示。
    feedback.notify.mockClear();
    await pressEscape();
    expect(wrapper.find('[data-test="cleanup-delete-confirm-dialog"]').exists()).toBe(false);
    expect(wrapper.vm.cleanupDialog.show).toBe(true);
    expect(wrapper.vm.cleanupDialog.processing).toBe(false);
    expect(lockNotices()).toHaveLength(0);

    // 删除进行中（合并还没返回）：Esc 被拦下并提示，按钮禁用。
    let finishMerge;
    api.MergeMediaMetadata.mockImplementationOnce(() => new Promise(resolve => { finishMerge = resolve; }));
    await clickTrash(wrapper);
    await wrapper.get('[data-test="cleanup-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(wrapper.vm.cleanupDialog.processing).toBe(true);
    await pressEscape();
    expect(wrapper.vm.cleanupDialog.show).toBe(true);
    expect(lockNotices()).toHaveLength(1);
    expect(close().element.disabled).toBe(true);
    expect(cancel().element.disabled).toBe(true);

    finishMerge({ warnings: [] });
    await flushPromises();
    expect(wrapper.vm.cleanupDialog.processing).toBe(false);
    expect(close().element.disabled).toBe(false);
    await close().trigger('click');
    expect(wrapper.vm.cleanupDialog.show).toBe(false);
    wrapper.unmount();
  });

  it('D-PC49 没有删除流程时 Esc 关闭面板；应用确认框开着时 Esc 只关确认框', async () => {
    const { wrapper } = await openWith(baseAnalysis({
      duplicate_groups: [{ original: video(1), candidates: [video(2)], reason: '一致' }]
    }));
    feedbackState.confirm = { title: '移出本组' };
    await pressEscape();
    expect(wrapper.vm.cleanupDialog.show).toBe(true);
    feedbackState.confirm = null;
    await pressEscape();
    expect(wrapper.vm.cleanupDialog.show).toBe(false);
    expect(lockNotices()).toHaveLength(0);
    wrapper.unmount();
  });
});
