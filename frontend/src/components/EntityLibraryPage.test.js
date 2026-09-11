import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  CreateCollection: vi.fn(),
  CreatePerson: vi.fn(),
  GetCollectionDetail: vi.fn(),
  GetPersonDetail: vi.fn(),
  GetPersonImages: vi.fn(),
  ListCollections: vi.fn(),
  ListPeople: vi.fn(),
  OpenDirectory: vi.fn(),
  RevealImage: vi.fn(),
  RemovePersonImage: vi.fn(),
  GetAllTags: vi.fn(),
  BatchAddTagToImages: vi.fn(),
  PlayVideo: vi.fn(),
  PreviewExternally: vi.fn(),
  UpdateVideoWatchProgress: vi.fn()
}));

vi.mock('../../wailsjs/go/main/App', () => api);
vi.mock('./FaceClusterReviewPanel.vue', () => ({
  default: {
    name: 'FaceClusterReviewPanel',
    emits: ['changed', 'loaded'],
    template: '<div class="face-panel-stub" />'
  }
}));
vi.mock('./PreviewDrawer.vue', () => ({
  default: {
    name: 'PreviewDrawer',
    props: ['initialEntity'],
    template: '<aside class="preview-drawer-stub">{{ initialEntity.type }}:{{ initialEntity.id }}</aside>'
  }
}));

import EntityLibraryPage from './EntityLibraryPage.vue';
import { commandList } from '../utils/commandRegistry.js';

beforeEach(() => {
  vi.clearAllMocks();
  api.ListPeople.mockResolvedValue([]);
  api.ListCollections.mockResolvedValue([]);
  api.GetPersonDetail.mockResolvedValue({
    person: { person: { id: 7, display_name: 'Actor Seven', original_name: 'Seven' }, avatar_url: '', active_video_count: 0 },
    videos: [],
    next_video_id: 0,
    images: [],
    next_image_id: 0
  });
  api.GetPersonImages.mockResolvedValue({ images: [], next_image_id: 0 });
  api.GetCollectionDetail.mockResolvedValue({
    collection: { collection: { id: 5, name: 'Saga', description: 'Local set' }, cover_url: '', active_video_count: 0 },
    videos: []
  });
  api.PlayVideo.mockResolvedValue({ dispatch_succeeded: true });
});

describe('EntityLibraryPage', () => {
  it('enlarges and unlinks a person image while preserving the other media', async () => {
    api.ListPeople.mockResolvedValueOnce([{ person: { id: 7, display_name: '人物' }, active_video_count: 1, active_image_count: 2 }]);
    api.GetPersonDetail.mockResolvedValueOnce({ person: { person: { id: 7, display_name: '人物' }, active_video_count: 1, active_image_count: 2 }, videos: [], images: [{ id: 11, name: 'a.jpg' }, { id: 12, name: 'b.jpg' }] });
    api.RemovePersonImage.mockResolvedValueOnce(false);
    const w = mount(EntityLibraryPage, { props: { entityType: 'person' }, global: { stubs: { teleport: true } } }); await flushPromises();
    await w.get('.entity-card').trigger('click'); await flushPromises();
    await w.findAll('.entity-image-card__preview')[0].trigger('click');
    expect(w.findComponent({ name: 'ImageSourceDialog' }).props('image').id).toBe(11);
    await w.get('[data-test="image-source-unlink"]').trigger('click'); await flushPromises();
    expect(api.RemovePersonImage).toHaveBeenCalledWith(7, 11);
    expect(w.vm.entityImages.map(i => i.id)).toEqual([12]); expect(w.vm.selectedItem.active_image_count).toBe(1);
    expect(w.vm.selectedEntity.id).toBe(7); w.unmount();
  });
  it('offers batch tags for selected related pictures', async () => {
    api.ListPeople.mockResolvedValueOnce([{ person: { id: 7, display_name: '人物' } }]);
    api.GetPersonDetail.mockResolvedValueOnce({ person: { person: { id: 7 }, active_image_count: 2 }, videos: [], images: [{ id: 11 }, { id: 12 }] });
    api.GetAllTags.mockResolvedValueOnce([{ id: 3, name: '合照' }]);
    api.BatchAddTagToImages.mockResolvedValueOnce({ succeeded: 2, failed: 0 });
    const w = mount(EntityLibraryPage, { props: { entityType: 'person' } }); await flushPromises();
    await w.get('.entity-card').trigger('click'); await flushPromises();
    await w.get('[data-test="image-batch-select-all"]').trigger('click');
    await w.get('[data-test="image-batch-tag-open"]').trigger('click'); await flushPromises();
    await w.get('.image-batch-tags__options button').trigger('click'); await flushPromises();
    expect(api.BatchAddTagToImages).toHaveBeenCalledWith([11, 12], 3); w.unmount();
  });

  it('loads people and opens the selected entity drawer', async () => {
    api.ListPeople.mockResolvedValueOnce([{
      person: { id: 7, display_name: 'Actor Seven', original_name: 'Seven' },
      avatar_url: '',
      active_video_count: 2,
      cursor_name: 'actor seven'
    }]);
    const wrapper = mount(EntityLibraryPage, { props: { entityType: 'person' } });
    await flushPromises();

    expect(api.ListPeople).toHaveBeenCalledWith('', '', 0, 50);
    expect(wrapper.text()).toContain('Actor Seven');
    await wrapper.get('.entity-card').trigger('click');

    expect(wrapper.vm.selectedEntity).toEqual({ type: 'person', id: 7 });
    expect(wrapper.get('.preview-drawer-stub').text()).toBe('person:7');
    expect(api.GetPersonDetail).toHaveBeenCalledWith(7, 0, 30);
  });

  // D-021：人物详情的视频与图片是两个区块、各自分页，不混排。
  it('renders the person image section with its own pagination', async () => {
    api.ListPeople.mockResolvedValueOnce([{
      person: { id: 7, display_name: 'Actor Seven', original_name: '' },
      avatar_url: '', active_video_count: 1, active_image_count: 3, cursor_name: 'actor seven'
    }]);
    api.GetPersonDetail.mockResolvedValueOnce({
      person: { person: { id: 7, display_name: 'Actor Seven', original_name: '' }, avatar_url: '', active_video_count: 1, active_image_count: 3 },
      videos: [{ id: 2, name: 'two.mp4', size: 1024, duration: 60 }], next_video_id: 0,
      images: [{ id: 11, name: 'a.jpg' }, { id: 12, name: 'b.jpg' }], next_image_id: 12
    });
    api.GetPersonImages.mockResolvedValueOnce({ images: [{ id: 13, name: 'c.jpg' }], next_image_id: 0 });
    const wrapper = mount(EntityLibraryPage, { props: { entityType: 'person' } });
    await flushPromises();
    await wrapper.get('.entity-card').trigger('click');
    await flushPromises();

    const section = wrapper.get('[data-test="person-image-section"]');
    expect(section.text()).toContain('图片（3）');
    expect(section.findAll('.entity-image-card')).toHaveLength(2);
    expect(section.find('img[src="/preview/image-thumbnail/11"]').exists()).toBe(true);
    // 视频卡片留在自己的区块里，两者不混排。
    expect(section.findAll('.entity-video-card')).toHaveLength(0);

    await wrapper.get('[data-test="person-image-more"]').trigger('click');
    await flushPromises();
    expect(api.GetPersonImages).toHaveBeenCalledWith(7, 12, 30);
    expect(wrapper.findAll('.entity-image-card')).toHaveLength(3);
    expect(wrapper.find('[data-test="person-image-more"]').exists()).toBe(false);
  });

  it('keeps the loaded images while paging videos', async () => {
    api.ListPeople.mockResolvedValueOnce([{
      person: { id: 7, display_name: 'Actor Seven', original_name: '' },
      avatar_url: '', active_video_count: 2, active_image_count: 1, cursor_name: 'actor seven'
    }]);
    const personPage = (videos, nextVideoID) => ({
      person: { person: { id: 7, display_name: 'Actor Seven', original_name: '' }, avatar_url: '', active_video_count: 2, active_image_count: 1 },
      videos, next_video_id: nextVideoID,
      images: [{ id: 11, name: 'a.jpg' }], next_image_id: 0
    });
    api.GetPersonDetail
      .mockResolvedValueOnce(personPage([{ id: 2, name: 'two.mp4', size: 1, duration: 1 }], 2))
      .mockResolvedValueOnce(personPage([{ id: 1, name: 'one.mp4', size: 1, duration: 1 }], 0));
    const wrapper = mount(EntityLibraryPage, { props: { entityType: 'person' } });
    await flushPromises();
    await wrapper.get('.entity-card').trigger('click');
    await flushPromises();

    await wrapper.vm.loadEntityVideos(false);
    await wrapper.vm.$nextTick();
    expect(wrapper.findAll('.entity-video-card')).toHaveLength(2);
    expect(wrapper.findAll('.entity-image-card')).toHaveLength(1);
  });

  it('creates a collection, reloads the list, and opens its drawer', async () => {
    api.CreateCollection.mockResolvedValueOnce({ id: 5, name: 'Saga' });
    const wrapper = mount(EntityLibraryPage, { props: { entityType: 'collection' } });
    await flushPromises();
    wrapper.vm.createForm = { name: 'Saga', secondary: 'Local set' };

    await wrapper.vm.createEntity();
    await flushPromises();

    expect(api.CreateCollection).toHaveBeenCalledWith('Saga', 'Local set');
    expect(api.ListCollections).toHaveBeenCalledTimes(2);
    expect(wrapper.vm.selectedEntity).toEqual({ type: 'collection', id: 5 });
    expect(wrapper.get('.preview-drawer-stub').text()).toBe('collection:5');
  });

  it('loads additional related person videos and exposes playback actions', async () => {
    api.ListPeople.mockResolvedValueOnce([{
      person: { id: 7, display_name: 'Actor Seven', original_name: '' },
      avatar_url: '', active_video_count: 2, cursor_name: 'actor seven'
    }]);
    api.GetPersonDetail
      .mockResolvedValueOnce({
        person: { person: { id: 7, display_name: 'Actor Seven', original_name: '' }, avatar_url: '', active_video_count: 2 },
        videos: [{ id: 2, name: 'two.mp4', size: 1024, duration: 60 }], next_video_id: 2
      })
      .mockResolvedValueOnce({
        person: { person: { id: 7, display_name: 'Actor Seven', original_name: '' }, avatar_url: '', active_video_count: 2 },
        videos: [{ id: 1, name: 'one.mp4', size: 2048, duration: 120 }], next_video_id: 0
      });
    const wrapper = mount(EntityLibraryPage, { props: { entityType: 'person' } });
    await flushPromises();
    await wrapper.get('.entity-card').trigger('click');
    await flushPromises();

    expect(wrapper.findAll('.entity-video-card')).toHaveLength(1);
    await wrapper.vm.loadEntityVideos(false);
    await wrapper.vm.$nextTick();
    expect(wrapper.findAll('.entity-video-card')).toHaveLength(2);

    await wrapper.find('.entity-video-card__actions .btn-primary').trigger('click');
    expect(api.PlayVideo).toHaveBeenCalledWith(2);
  });
});

describe('EntityLibraryPage 命令面板落点', () => {
  it('挂载时带着 focusEntity 就直接展开那个人物', async () => {
    const wrapper = mount(EntityLibraryPage, {
      props: { entityType: 'person', focusEntity: { id: 7, name: 'Actor Seven' } }
    });
    await flushPromises();

    expect(api.GetPersonDetail).toHaveBeenCalledWith(7, 0, 30);
    expect(wrapper.text()).toContain('返回人物列表');
    // 列表本身照常加载，返回列表时不是空页。
    expect(api.ListPeople).toHaveBeenCalledWith('', '', 0, 50);
  });

  it('已挂载后 focusEntity 变化同样展开，置空不再动', async () => {
    const wrapper = mount(EntityLibraryPage, { props: { entityType: 'collection' } });
    await flushPromises();
    expect(api.GetCollectionDetail).not.toHaveBeenCalled();

    await wrapper.setProps({ focusEntity: { id: 5, name: 'Saga' } });
    await flushPromises();
    expect(api.GetCollectionDetail).toHaveBeenCalledWith(5);

    await wrapper.setProps({ focusEntity: null });
    await flushPromises();
    expect(api.GetCollectionDetail).toHaveBeenCalledTimes(1);
    expect(wrapper.text()).toContain('返回作品集列表');
  });

  it('挂载与卸载会注册与注销本页的刷新命令', async () => {
    const wrapper = mount(EntityLibraryPage, { props: { entityType: 'collection' } });
    await flushPromises();
    expect(commandList().map(command => command.id)).toContain('action:collection-reload');

    wrapper.unmount();
    expect(commandList().map(command => command.id)).not.toContain('action:collection-reload');
  });
});

// 用户裁决（2026-09-07）：人脸分析认出的面孔要在人物页顶部就能看到、命名，不必去 AI 标签管理里找。
describe('EntityLibraryPage 待命名人脸分区', () => {
  it('只在人物列表视图渲染，作品集页没有', async () => {
    const people = mount(EntityLibraryPage, { props: { entityType: 'person' } });
    await flushPromises();
    expect(people.find('[data-test="people-face-review"]').exists()).toBe(true);
    expect(people.find('.face-panel-stub').exists()).toBe(true);

    const collections = mount(EntityLibraryPage, { props: { entityType: 'collection' } });
    await flushPromises();
    expect(collections.find('[data-test="people-face-review"]').exists()).toBe(false);
  });

  it('按面板回报的数量写摘要，没有待处理项时默认收起，手动展开后不再自动收', async () => {
    const wrapper = mount(EntityLibraryPage, { props: { entityType: 'person' } });
    await flushPromises();
    const panel = wrapper.findComponent({ name: 'FaceClusterReviewPanel' });
    // v-show 只改内联 display；这里直接看它，不依赖 test-utils 对脱离文档节点的可见性推断。
    const panelHidden = () => String(wrapper.get('.face-panel-stub').attributes('style') || '').includes('display: none');

    panel.vm.$emit('loaded', { unnamed: 2, appendPending: 1 });
    await flushPromises();
    expect(wrapper.get('[data-test="people-face-review-summary"]').text()).toContain('2 组未命名 · 1 组待确认追加');
    expect(panelHidden()).toBe(false);

    panel.vm.$emit('loaded', { unnamed: 0, appendPending: 0 });
    await flushPromises();
    expect(wrapper.get('[data-test="people-face-review-summary"]').text()).toContain('暂无待处理的人脸候选');
    expect(panelHidden()).toBe(true);

    await wrapper.get('[data-test="people-face-review-toggle"]').trigger('click');
    expect(panelHidden()).toBe(false);
    panel.vm.$emit('loaded', { unnamed: 0, appendPending: 0 });
    await flushPromises();
    expect(panelHidden()).toBe(false);
  });

  it('面板命名或关联成功后重新拉人物列表', async () => {
    const wrapper = mount(EntityLibraryPage, { props: { entityType: 'person' } });
    await flushPromises();
    expect(api.ListPeople).toHaveBeenCalledTimes(1);

    wrapper.findComponent({ name: 'FaceClusterReviewPanel' }).vm.$emit('changed');
    await flushPromises();
    expect(api.ListPeople).toHaveBeenCalledTimes(2);
  });
});
