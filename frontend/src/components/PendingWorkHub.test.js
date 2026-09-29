import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({ GetPendingWorkSummary: vi.fn(), LogFrontend: vi.fn() }));
vi.mock('../../wailsjs/go/main/App', () => api);

import PendingWorkHub, { PENDING_WORK_ROWS } from './PendingWorkHub.vue';

const SUMMARY = {
  ai_video_candidates: 12,
  ai_video_failed_runs: 3,
  ai_image_candidates: 4,
  same_source_unconfirmed: 2,
  face_unnamed: 5,
  face_append_pending: 1,
  collection_suggestions: 6,
  cleanup_candidates: 7,
  local_metadata_updates: 8,
  total: 45
};

const wrappers = [];

function mountHub(props = { open: true }) {
  const wrapper = mount(PendingWorkHub, { props });
  wrappers.push(wrapper);
  return wrapper;
}

beforeEach(() => {
  vi.clearAllMocks();
  api.GetPendingWorkSummary.mockResolvedValue({ ...SUMMARY });
});

afterEach(() => {
  while (wrappers.length) wrappers.pop().unmount();
  vi.useRealTimers();
});

describe('待处理工作台（META-08）', () => {
  it('META-08 列出各类待处理事项与数量，总数不含失败的 AI 打标', async () => {
    const wrapper = mountHub();
    await flushPromises();

    expect(wrapper.get('[data-test="pending-work-total"]').text()).toBe('共 45 项');
    const counts = Object.fromEntries(PENDING_WORK_ROWS.map(row => [row.field, wrapper.get(`[data-test="pending-count-${row.field}"]`).text()]));
    expect(counts).toEqual({
      ai_video_candidates: '12',
      same_source_unconfirmed: '2',
      ai_image_candidates: '4',
      face_unnamed: '5',
      face_append_pending: '1',
      collection_suggestions: '6',
      cleanup_candidates: '7',
      local_metadata_updates: '8'
    });
    expect(wrapper.get('[data-test="pending-ai-failed"]').text()).toContain('3 个视频 AI 打标失败（不计入待处理）');
  });

  it('META-08 关着也会拉数量并报给顶栏角标，之后定时刷新', async () => {
    vi.useFakeTimers();
    const wrapper = mountHub({ open: false });
    await flushPromises();

    expect(wrapper.emitted('badge-change')[0]).toEqual([45]);
    expect(wrapper.find('[data-test="pending-work-hub"]').exists()).toBe(false);

    api.GetPendingWorkSummary.mockResolvedValue({ ...SUMMARY, total: 44 });
    vi.advanceTimersByTime(60000);
    await flushPromises();
    expect(wrapper.emitted('badge-change').at(-1)).toEqual([44]);
  });

  it('META-08 打开时重新拉一次', async () => {
    const wrapper = mountHub({ open: false });
    await flushPromises();
    api.GetPendingWorkSummary.mockClear();

    await wrapper.setProps({ open: true });
    await flushPromises();
    expect(api.GetPendingWorkSummary).toHaveBeenCalledTimes(1);
  });

  it('META-08 维护或待重启等终态下显示后端给的中文说明，角标清零', async () => {
    api.GetPendingWorkSummary.mockRejectedValue('数据库后端已切换，请重启应用后再查看');
    const wrapper = mountHub();
    await flushPromises();

    expect(wrapper.get('[data-test="pending-work-error"]').text()).toBe('数据库后端已切换，请重启应用后再查看');
    expect(wrapper.find('[data-test="pending-row-cleanup_candidates"]').exists()).toBe(false);
    expect(wrapper.emitted('badge-change').at(-1)).toEqual([0]);
  });
});

describe('工作台只做跳转（APP-11）', () => {
  it('APP-11 每一项「处理」都交出约定的命令 ID，并关闭工作台', async () => {
    const expected = {
      ai_video_candidates: 'library.openAIReview',
      same_source_unconfirmed: 'library.openAIReview',
      ai_image_candidates: 'photos.openAIReview',
      face_unnamed: 'people.openFaceReview',
      face_append_pending: 'people.openFaceReview',
      collection_suggestions: 'library.openCollectionSuggestions',
      cleanup_candidates: 'library.openCleanup',
      local_metadata_updates: 'library.openLocalMetadataUpdates'
    };
    const wrapper = mountHub();
    await flushPromises();

    const emitted = [];
    for (const field of Object.keys(expected)) {
      await wrapper.get(`[data-test="pending-go-${field}"]`).trigger('click');
      emitted.push([field, wrapper.emitted('run-command').at(-1)[0]]);
    }
    expect(Object.fromEntries(emitted)).toEqual(expected);
    expect(wrapper.emitted('close')).toHaveLength(Object.keys(expected).length);
    expect(PENDING_WORK_ROWS.map(row => row.field).sort()).toEqual(Object.keys(expected).sort());
  });

  it('APP-11 数量为 0 的项「处理」置灰；清理候选标出 ⌘K', async () => {
    api.GetPendingWorkSummary.mockResolvedValue({ ...SUMMARY, same_source_unconfirmed: 0, total: 43 });
    const wrapper = mountHub();
    await flushPromises();

    expect(wrapper.get('[data-test="pending-go-same_source_unconfirmed"]').attributes('disabled')).toBeDefined();
    expect(wrapper.get('[data-test="pending-row-cleanup_candidates"]').text()).toContain('⌘K');
  });

  it('APP-11 失败的 AI 打标引到任务中心查看', async () => {
    const wrapper = mountHub();
    await flushPromises();

    await wrapper.get('[data-test="pending-open-task-center"]').trigger('click');
    expect(wrapper.emitted('open-task-center')).toHaveLength(1);
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('全部处理完时给出空态说明', async () => {
    api.GetPendingWorkSummary.mockResolvedValue(Object.fromEntries(Object.keys(SUMMARY).map(key => [key, 0])));
    const wrapper = mountHub();
    await flushPromises();

    expect(wrapper.get('[data-test="pending-work-empty"]').text()).toBe('暂无待处理事项。');
    expect(wrapper.find('[data-test="pending-ai-failed"]').exists()).toBe(false);
  });
});
