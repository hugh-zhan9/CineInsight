import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  SearchLibraryVideoPage: vi.fn(),
  ListPeople: vi.fn(),
  ListCollections: vi.fn(),
  SearchScenes: vi.fn(),
  SearchScenesByImage: vi.fn()
}));
vi.mock('../../wailsjs/go/main/App', () => api);

import {
  TEXT_GROUP_KEYS,
  UNIFIED_IMAGE_MAX_EDGE,
  UNIFIED_SEARCH_GROUPS,
  createGroupSequencer,
  firstImageFile,
  fitLongEdge,
  groupErrorText,
  groupNoticeText,
  normalizeCollectionResults,
  normalizePeopleResults,
  normalizeSceneResults,
  normalizeVideoResults,
  prepareSearchImage,
  searchGroup,
  searchImageProblem,
  searchableKeyword,
  viewAllTarget
} from './unifiedSearch.js';

const MB = 1024 * 1024;

function fakeFile(type, size, name = 'shot.png') {
  return { type, size, name };
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe('统一搜索的分组与门槛（D-P2-05）', () => {
  it('组的固定顺序：视频、人物、作品集、台词、画面，以图最后；文字组不含以图', () => {
    expect(UNIFIED_SEARCH_GROUPS.map(group => group.label)).toEqual(['视频', '人物', '作品集', '台词', '画面', '以图']);
    expect(TEXT_GROUP_KEYS).toEqual(['video', 'person', 'collection', 'dialogue', 'visual']);
  });

  it('去掉首尾空白后至少 2 个字才搜命令以外的组', () => {
    expect(searchableKeyword('')).toBe('');
    expect(searchableKeyword(' 海 ')).toBe('');
    expect(searchableKeyword(' 海边 ')).toBe('海边');
    expect(searchableKeyword(null)).toBe('');
  });

  it('请求序号按组记：换号后旧的作废，一组换号不影响别的组', () => {
    const sequencer = createGroupSequencer();
    const video = sequencer.next('video');
    const person = sequencer.next('person');
    expect(sequencer.isCurrent('video', video)).toBe(true);
    const retry = sequencer.next('video');
    expect(sequencer.isCurrent('video', video)).toBe(false);
    expect(sequencer.isCurrent('video', retry)).toBe(true);
    expect(sequencer.isCurrent('person', person)).toBe(true);
    sequencer.invalidate(['person']);
    expect(sequencer.isCurrent('person', person)).toBe(false);
    expect(sequencer.isCurrent('video', retry)).toBe(true);
  });
});

describe('各组的请求', () => {
  it('视频：片名搜索、版本合并、每组 5 条', async () => {
    api.SearchLibraryVideoPage.mockResolvedValue({ videos: [] });
    await searchGroup('video', { keyword: '海边' });
    expect(api.SearchLibraryVideoPage).toHaveBeenCalledWith({
      filter: {
        search_mode: 'file',
        keyword: '海边',
        smart_view: '',
        tag_ids: [],
        min_size: 0,
        max_size: 0,
        min_height: 0,
        max_height: 0,
        min_rating: null,
        max_rating: null,
        sort_mode: 'balanced',
        collapse_versions: true
      },
      limit: 5
    });
  });

  it('人物、作品集：按关键词取首页 5 条', async () => {
    api.ListPeople.mockResolvedValue([]);
    api.ListCollections.mockResolvedValue([]);
    await searchGroup('person', { keyword: '张三' });
    await searchGroup('collection', { keyword: '三部曲' });
    expect(api.ListPeople).toHaveBeenCalledWith('张三', '', 0, 5);
    expect(api.ListCollections).toHaveBeenCalledWith('三部曲', '', 0, 5);
  });

  it('台词、画面：场景检索的对白 / 画面模式，各 5 条，全部视频范围', async () => {
    api.SearchScenes.mockResolvedValue({ hits: [], notices: [] });
    await searchGroup('dialogue', { keyword: '告白' });
    await searchGroup('visual', { keyword: '日落' });
    expect(api.SearchScenes).toHaveBeenNthCalledWith(1, { query: '告白', mode: 'dialogue', limit: 5 });
    expect(api.SearchScenes).toHaveBeenNthCalledWith(2, { query: '日落', mode: 'visual', limit: 5 });
  });

  it('以图：只发 base64，取 8 条', async () => {
    api.SearchScenesByImage.mockResolvedValue({ hits: [], notices: [] });
    await searchGroup('image', { image: { base64: 'QUJD', dataURL: 'data:image/jpeg;base64,QUJD' } });
    expect(api.SearchScenesByImage).toHaveBeenCalledWith({ image_base64: 'QUJD', video_id: 0, at_ms: 0, limit: 8 });
  });

  it('后端出错原样抛给调用方（由面板记成这一组的错误）', async () => {
    api.ListPeople.mockRejectedValue(new Error('数据库不可用'));
    await expect(searchGroup('person', { keyword: '张三' })).rejects.toThrow('数据库不可用');
    await expect(searchGroup('nope', {})).rejects.toThrow('未知的搜索分组');
  });
});

describe('结果整理', () => {
  it('视频：最多 5 条，显示标题优先、年份与时长，海报走 artwork 路由，目标是这部片', () => {
    const videos = Array.from({ length: 7 }, (_, index) => ({ id: index + 1, name: `f${index + 1}.mp4`, display_title: index === 0 ? '海边落日' : '' }));
    videos[0].year = 2016;
    videos[0].duration = 3725;
    const items = normalizeVideoResults({ videos });
    expect(items).toHaveLength(5);
    expect(items[0]).toMatchObject({
      id: 'video:1',
      title: '海边落日',
      subtitle: '2016 · 01:02:05',
      media: { url: '/preview/video-artwork/1?kind=poster', shape: 'poster' },
      target: { type: 'video', video: videos[0] }
    });
    expect(items[1]).toMatchObject({ title: 'f2.mp4', subtitle: '' });
    expect(normalizeVideoResults(null)).toEqual([]);
  });

  it('人物与作品集：名称、作品数、头像 / 封面，目标是实体', () => {
    const people = normalizePeopleResults([
      { person: { id: 3, display_name: '张三' }, avatar_url: '/preview/person-avatar/3', active_video_count: 4, active_image_count: 2 },
      { person: { id: 0, display_name: '坏数据' } }
    ]);
    expect(people).toEqual([{
      id: 'person:3',
      title: '张三',
      subtitle: '4 部视频 · 2 张图片',
      media: { url: '/preview/person-avatar/3', shape: 'avatar' },
      target: { type: 'entity', entityType: 'person', id: 3, name: '张三' }
    }]);
    const collections = normalizeCollectionResults([{ collection: { id: 5, name: '三部曲' }, cover_url: '/preview/collection-cover/5', active_video_count: 3 }]);
    expect(collections[0]).toMatchObject({
      id: 'collection:5',
      title: '三部曲',
      subtitle: '3 部视频',
      target: { type: 'entity', entityType: 'collection', id: 5, name: '三部曲' }
    });
  });

  it('台词以那句话为主；画面以片名为主；打开时提前 2 秒；同一时刻的重复命中只留一条；最多 5 条', () => {
    const hits = [
      { video_id: 7, title: '长片', start_ms: 7_080_000, end_ms: 7_083_500, text: '雨夜里的告白' },
      { video_id: 7, title: '长片', start_ms: 7_080_000, end_ms: 7_083_500, text: '雨夜里的告白' },
      { video_id: 9, title: '', start_ms: 1000, end_ms: 4000, text: '' },
      ...Array.from({ length: 6 }, (_, index) => ({ video_id: 20 + index, title: `片${index}`, start_ms: 0, end_ms: 1000, text: `第${index}句` }))
    ];
    const dialogue = normalizeSceneResults('dialogue', { hits, notices: [] });
    expect(dialogue.items).toHaveLength(5);
    expect(dialogue.items[0]).toMatchObject({
      id: 'dialogue:7:7080000',
      title: '雨夜里的告白',
      subtitle: '长片 · 1:58:00',
      label: '长片 1:58:00',
      media: { url: '/preview/frame/7?ms=7080000&w=160', shape: 'frame' },
      target: { type: 'scene', videoID: 7, startMs: 7_078_000 }
    });
    // 没有台词文字的对白命中退回片名。
    expect(dialogue.items[1]).toMatchObject({ title: '视频 9', target: { startMs: 0 } });

    const visual = normalizeSceneResults('visual', { hits: [hits[0]], notices: ['visual_index_empty'] });
    expect(visual.items[0]).toMatchObject({ id: 'visual:7:7080000', title: '长片', subtitle: '1:58:00 – 1:58:03' });
    expect(visual.notices).toEqual(['visual_index_empty']);
    expect(normalizeSceneResults('image', null)).toEqual({ items: [], notices: [] });
  });

  it('错误与提示的中文说法：场景三组认得错误代码，其余给后端原话；提示只取第一条', () => {
    expect(groupErrorText('visual', new Error('scene_runtime_unsupported'))).toBe('本地画面模型仅支持 macOS Apple Silicon。');
    expect(groupErrorText('image', new Error('scene_image_invalid'))).toContain('20 MB');
    expect(groupErrorText('video', new Error('数据库不可用'))).toBe('数据库不可用');
    expect(groupErrorText('person', '')).toBe('搜索失败');
    expect(groupNoticeText(['subtitle_index_empty', 'visual_index_empty'])).toContain('字幕索引');
    expect(groupNoticeText([])).toBe('');
  });
});

describe('「查看全部」去哪', () => {
  it('视频 → 视频页搜索；人物 / 作品集 → 观看页带关键词；台词 / 画面 → 场景检索对应模式；以图 → 场景检索带图', () => {
    const image = { dataURL: 'data:image/jpeg;base64,QUJD', base64: 'QUJD', name: 'shot.png' };
    expect(viewAllTarget('video', { keyword: '海边' })).toEqual({ page: 'videos', params: { keyword: '海边' } });
    expect(viewAllTarget('person', { keyword: '张三' })).toEqual({ page: 'people', params: { keyword: '张三' } });
    expect(viewAllTarget('collection', { keyword: '三部' })).toEqual({ page: 'collections', params: { keyword: '三部' } });
    expect(viewAllTarget('dialogue', { keyword: '告白' })).toEqual({ page: 'scenes', params: { query: '告白', mode: 'dialogue' } });
    expect(viewAllTarget('visual', { keyword: '日落' })).toEqual({ page: 'scenes', params: { query: '日落', mode: 'visual' } });
    expect(viewAllTarget('image', { image })).toEqual({ page: 'scenes', params: { image: { dataURL: image.dataURL, name: 'shot.png' } } });
    expect(viewAllTarget('image', {})).toBeNull();
    expect(viewAllTarget('nope')).toBeNull();
  });
});

describe('以图：图片校验与缩放', () => {
  it('长边缩到 1024，等比、不放大、至少 1 像素', () => {
    expect(UNIFIED_IMAGE_MAX_EDGE).toBe(1024);
    expect(fitLongEdge(4000, 3000)).toEqual({ width: 1024, height: 768 });
    expect(fitLongEdge(1080, 1920)).toEqual({ width: 576, height: 1024 });
    expect(fitLongEdge(800, 600)).toEqual({ width: 800, height: 600 });
    expect(fitLongEdge(1024, 1024)).toEqual({ width: 1024, height: 1024 });
    expect(fitLongEdge(5000, 2)).toEqual({ width: 1024, height: 1 });
  });

  it('超过 20 MB 直接拒绝并说明；格式不对、空文件也拒绝', () => {
    expect(searchImageProblem(fakeFile('image/png', 20 * MB))).toBe('');
    expect(searchImageProblem(fakeFile('image/png', 20 * MB + 1))).toContain('超过 20 MB');
    expect(searchImageProblem(fakeFile('image/heic', 100))).toContain('JPEG、PNG');
    expect(searchImageProblem(fakeFile('image/png', 0))).toContain('JPEG、PNG');
    expect(searchImageProblem(null)).toBe('没有读到图片。');
  });

  it('大图不解码就拒绝；能用的图解码后按长边 1024 渲染，返回 data URL 与 base64', async () => {
    const decode = vi.fn(async () => ({ naturalWidth: 4000, naturalHeight: 3000 }));
    const render = vi.fn(() => 'data:image/jpeg;base64,QUJD');

    await expect(prepareSearchImage(fakeFile('image/png', 21 * MB), { decode, render })).rejects.toThrow('超过 20 MB');
    expect(decode).not.toHaveBeenCalled();

    const prepared = await prepareSearchImage(fakeFile('image/png', 2 * MB, 'a.png'), { decode, render });
    expect(render).toHaveBeenCalledWith({ naturalWidth: 4000, naturalHeight: 3000 }, 1024);
    expect(prepared).toEqual({ dataURL: 'data:image/jpeg;base64,QUJD', base64: 'QUJD', name: 'a.png' });
  });

  it('解码失败或编码出空数据时给出可读的原因', async () => {
    const file = fakeFile('image/jpeg', 100);
    await expect(prepareSearchImage(file, { decode: () => Promise.reject(new Error('x')), render: vi.fn() })).rejects.toThrow('读不出来');
    await expect(prepareSearchImage(file, { decode: async () => ({}), render: () => 'data:,' })).rejects.toThrow('读不出来');
  });

  it('剪贴板 / 拖放里取第一张图片文件，没有就是 null', () => {
    const text = { type: 'text/plain' };
    const png = { type: 'image/png' };
    expect(firstImageFile({ files: [text, png] })).toBe(png);
    expect(firstImageFile({ files: [text] })).toBeNull();
    expect(firstImageFile(null)).toBeNull();
  });
});
