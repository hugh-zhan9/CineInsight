// 建议作品集面板（D-023..D-025）：候选列表、成员预览、去成员、改名、确认、
// 忽略、分析进度与空态。面板只发指令，作品集写入与忽略记忆都在后端。
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

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

const api = vi.hoisted(() => new Proxy({}, {
  get(target, prop) {
    if (typeof prop === 'symbol' || prop === 'then') return undefined;
    if (!target[prop]) target[prop] = vi.fn(() => Promise.resolve(null));
    return target[prop];
  }
}));
vi.mock('../../wailsjs/go/main/App', () => api);

import CollectionSuggestionPanel from './CollectionSuggestionPanel.vue';

function suggestionStatus(overrides = {}) {
  return {
    running: false,
    cancelled: false,
    completed: true,
    total: 3,
    scanned: 3,
    matched: 3,
    pending: 1,
    last_error: '',
    ...overrides
  };
}

function member(videoID, episode, overrides = {}) {
  return {
    video_id: videoID,
    name: `Show.S01E0${episode}.mkv`,
    path: `/library/Show.S01E0${episode}.mkv`,
    season: 1,
    episode,
    position: episode,
    thumbnail_url: `/preview/thumbnail/${videoID}`,
    size: 700 * 1024 * 1024,
    multiple_versions: false,
    ...overrides
  };
}

function suggestion(overrides = {}) {
  return {
    id: 7,
    scan_root: '/library',
    series_name: 'Show',
    status: 'pending',
    member_count: 3,
    members: [member(11, 1), member(12, 2), member(13, 3)],
    created_at: '2026-09-02T00:00:00Z',
    ...overrides
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  delete window.runtime;
  feedback.confirmAction.mockResolvedValue(true);
  api.GetCollectionSuggestionStatus.mockResolvedValue(suggestionStatus());
  api.ListCollectionSuggestions.mockResolvedValue([suggestion()]);
  api.ConfirmCollectionSuggestion.mockResolvedValue({ collection: { collection: { id: 3, name: 'Show' } }, videos: [] });
  api.DismissCollectionSuggestion.mockResolvedValue(null);
  api.StartCollectionSuggestionAnalysis.mockResolvedValue(suggestionStatus({ running: true, scanned: 0, matched: 0 }));
  api.CancelCollectionSuggestionAnalysis.mockResolvedValue(null);
});

async function mountPanel() {
  const wrapper = mount(CollectionSuggestionPanel, { attachTo: document.body });
  await flushPromises();
  return wrapper;
}

describe('建议作品集面板', () => {
  it('列出候选与成员预览，名称默认取系列名', async () => {
    const wrapper = await mountPanel();
    expect(wrapper.findAll('[data-test="collection-suggestion-item"]').length).toBe(1);
    const members = wrapper.findAll('[data-test="collection-suggestion-member"]');
    expect(members.length).toBe(3);
    expect(members[0].find('img').attributes('src')).toBe('/preview/thumbnail/11');
    expect(members[0].text()).toContain('Show.S01E01.mkv');
    expect(members[0].text()).toContain('S01E01');
    expect(wrapper.get('[data-test="collection-suggestion-name"]').element.value).toBe('Show');
    expect(wrapper.get('[data-test="collection-suggestion-selection"]').text()).toContain('3 / 3');
  });

  it('确认时把改过的名称与保留的成员按顺序发回后端', async () => {
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="collection-suggestion-name"]').setValue('我的剧集');
    await wrapper.get('[data-test="collection-suggestion-confirm"]').trigger('click');
    await flushPromises();
    expect(api.ConfirmCollectionSuggestion).toHaveBeenCalledWith(7, '我的剧集', [11, 12, 13]);
    expect(feedback.notifySuccess).toHaveBeenCalled();
    // 确认过的候选立刻从列表里消失，不用重开面板。
    expect(wrapper.findAll('[data-test="collection-suggestion-item"]').length).toBe(0);
    expect(wrapper.find('[data-test="collection-suggestion-empty"]').exists()).toBe(true);
  });

  it('去掉的成员不写入作品集，也不允许只剩一集', async () => {
    const wrapper = await mountPanel();
    const removeButtons = wrapper.findAll('[data-test="collection-suggestion-remove-member"]');
    await removeButtons[1].trigger('click');
    expect(wrapper.get('[data-test="collection-suggestion-selection"]').text()).toContain('2 / 3');
    await wrapper.get('[data-test="collection-suggestion-confirm"]').trigger('click');
    await flushPromises();
    expect(api.ConfirmCollectionSuggestion).toHaveBeenCalledWith(7, 'Show', [11, 13]);

    api.ConfirmCollectionSuggestion.mockClear();
    api.ListCollectionSuggestions.mockResolvedValue([suggestion()]);
    const second = await mountPanel();
    const buttons = second.findAll('[data-test="collection-suggestion-remove-member"]');
    await buttons[0].trigger('click');
    await buttons[1].trigger('click');
    expect(second.get('[data-test="collection-suggestion-confirm"]').attributes('disabled')).toBeDefined();
    // 去掉的成员可以放回。
    await second.findAll('[data-test="collection-suggestion-remove-member"]')[0].trigger('click');
    expect(second.get('[data-test="collection-suggestion-selection"]').text()).toContain('2 / 3');
  });

  it('忽略先确认再提交，取消确认框就什么都不做', async () => {
    feedback.confirmAction.mockResolvedValue(false);
    const wrapper = await mountPanel();
    await wrapper.get('[data-test="collection-suggestion-dismiss"]').trigger('click');
    await flushPromises();
    expect(api.DismissCollectionSuggestion).not.toHaveBeenCalled();
    expect(wrapper.findAll('[data-test="collection-suggestion-item"]').length).toBe(1);

    feedback.confirmAction.mockResolvedValue(true);
    await wrapper.get('[data-test="collection-suggestion-dismiss"]').trigger('click');
    await flushPromises();
    expect(api.DismissCollectionSuggestion).toHaveBeenCalledWith(7);
    expect(wrapper.findAll('[data-test="collection-suggestion-item"]').length).toBe(0);
  });

  it('空态说明成组规则，不假装有候选', async () => {
    api.ListCollectionSuggestions.mockResolvedValue([]);
    const wrapper = await mountPanel();
    const empty = wrapper.get('[data-test="collection-suggestion-empty"]');
    expect(empty.text()).toContain('还没有可确认的剧集候选');
    expect(empty.text()).toContain('凑够两集');
    expect(wrapper.find('[data-test="collection-suggestion-confirm"]').exists()).toBe(false);
  });

  it('分析中显示进度并给出取消入口，结束后自动重读候选', async () => {
    api.GetCollectionSuggestionStatus.mockResolvedValue(suggestionStatus({ running: true, scanned: 1, total: 4, matched: 1 }));
    const handlers = {};
    window.runtime = {
      EventsOn: (event, handler) => {
        handlers[event] = handler;
        return () => { delete handlers[event]; };
      }
    };
    const wrapper = await mountPanel();
    const progress = wrapper.get('[data-test="collection-suggestion-progress"]');
    expect(progress.text()).toContain('已扫 1 / 4');
    expect(wrapper.find('[data-test="collection-suggestion-analyze"]').exists()).toBe(false);
    await wrapper.get('[data-test="collection-suggestion-cancel"]').trigger('click');
    await flushPromises();
    expect(api.CancelCollectionSuggestionAnalysis).toHaveBeenCalled();

    api.ListCollectionSuggestions.mockClear();
    handlers['collection-suggestion-state']({ running: false, completed: true, scanned: 4, total: 4, matched: 2, pending: 1 });
    await flushPromises();
    expect(api.ListCollectionSuggestions).toHaveBeenCalled();
    expect(wrapper.find('[data-test="collection-suggestion-progress"]').exists()).toBe(false);
    await wrapper.get('[data-test="collection-suggestion-analyze"]').trigger('click');
    await flushPromises();
    expect(api.StartCollectionSuggestionAnalysis).toHaveBeenCalled();
  });

  it('上一轮分析失败时如实说明原因', async () => {
    api.GetCollectionSuggestionStatus.mockResolvedValue(suggestionStatus({ completed: false, last_error: '读取视频失败' }));
    const wrapper = await mountPanel();
    expect(wrapper.get('[data-test="collection-suggestion-error"]').text()).toContain('读取视频失败');
  });

  it('同集多版本在成员上标出来，让用户自己去掉一个', async () => {
    api.ListCollectionSuggestions.mockResolvedValue([suggestion({
      member_count: 2,
      members: [
        member(21, 3, { name: 'Show - 03.mkv', position: 1, multiple_versions: true }),
        member(22, 3, { name: 'Show - 03v2.mkv', position: 1, multiple_versions: true })
      ]
    })]);
    const wrapper = await mountPanel();
    const members = wrapper.findAll('[data-test="collection-suggestion-member"]');
    expect(members.length).toBe(2);
    expect(members[0].text()).toContain('同集多版本');
    expect(members[1].text()).toContain('同集多版本');
    // 多版本要挑一个去掉，体积是最直接的依据。
    expect(members[0].text()).toContain('700.0 MB');
  });
});
