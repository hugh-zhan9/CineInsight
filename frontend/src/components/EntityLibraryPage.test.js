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
  DeleteImage: vi.fn(),
  DeleteVideo: vi.fn(),
  // P-034：人物页删除媒体改走带结果码的批量删除（PersonMediaDeleteDialog + TrashUndoBanner）。
  DeleteImagesWithResult: vi.fn(),
  DeleteVideosWithResult: vi.fn(),
  GetAllTags: vi.fn(),
  BatchAddTagToImages: vi.fn(),
  PlayVideo: vi.fn(),
  PreviewExternally: vi.fn(),
  UpdateVideoWatchProgress: vi.fn(),
  MergePeople: vi.fn(),
  DeletePerson: vi.fn(),
  GetPersonDeletionImpact: vi.fn()
}));

vi.mock('../../wailsjs/go/main/App', () => api);
// 合并、删除人物走应用内确认框：默认答「确定」，需要「取消」的用例单独设置。
const feedback = vi.hoisted(() => ({
  confirmAction: vi.fn(() => Promise.resolve(true)),
  notify: vi.fn(),
  notifyError: vi.fn(),
  notifySuccess: vi.fn()
}));
vi.mock('../utils/feedback.js', async (importOriginal) => ({ ...(await importOriginal()), ...feedback }));
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
  feedback.confirmAction.mockResolvedValue(true);
});

describe('EntityLibraryPage', () => {
  it.each([[0, 24], [3, 12], [0, 0]])('shows both person media counts (%s videos, %s images)', async (videos, images) => {
    api.ListPeople.mockResolvedValueOnce([{ person: { id: 7, display_name: '人物' }, active_video_count: videos, active_image_count: images }]);
    const w = mount(EntityLibraryPage, { props: { entityType: 'person' } }); await flushPromises();
    expect(w.get('.entity-card').text()).toContain(`${videos} 部视频 · ${images} 张图片`);
    expect(w.get('.entity-card').text()).not.toContain('部活跃作品'); w.unmount();
  });
  it('deletes related media after confirmation and updates the card counts without deleting the person', async () => {
    const person = { person: { id: 7, display_name: '人物' }, active_video_count: 1, active_image_count: 1 };
    api.ListPeople.mockResolvedValueOnce([person]);
    api.GetPersonDetail.mockResolvedValueOnce({ person, videos: [{ id: 21, name: 'clip.mp4', size: 100 }], images: [{ id: 11, name: 'a.jpg', size: 200 }] });
    const okResult = ids => Promise.resolve({ batch_id: 'b1', items: ids.map(id => ({ id, code: 'ok' })) });
    api.DeleteImagesWithResult.mockImplementation(okResult); api.DeleteVideosWithResult.mockImplementation(okResult);
    const w = mount(EntityLibraryPage, { props: { entityType: 'person' }, global: { stubs: { teleport: true } } }); await flushPromises();
    await w.get('.entity-card').trigger('click'); await flushPromises();
    expect(w.get('.entity-library__toolbar').text()).toContain('视频 1 / 1 部 · 图片 1 / 1 张');
    await w.get('[data-test="person-image-delete-11"]').trigger('click');
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled();
    await w.get('[data-test="person-media-delete-confirm"]').trigger('click'); await flushPromises();
    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([11], false, expect.any(String));
    expect(api.DeleteImage).not.toHaveBeenCalled();
    expect(w.vm.entityImages).toHaveLength(0); expect(w.vm.selectedItem.active_image_count).toBe(0);
    await w.get('[data-test="person-video-delete-21"]').trigger('click');
    await w.get('[data-test="person-media-delete-file"]').setValue(true);
    await w.get('[data-test="person-media-delete-confirm"]').trigger('click'); await flushPromises();
    expect(api.DeleteVideosWithResult).toHaveBeenCalledWith([21], true, expect.any(String));
    expect(api.DeleteVideo).not.toHaveBeenCalled();
    expect(w.vm.entityVideos).toHaveLength(0); expect(w.vm.selectedItem.active_video_count).toBe(0);
    expect(w.vm.selectedEntity.id).toBe(7);
    w.vm.closeEntity(); await w.vm.$nextTick();
    expect(w.get('.entity-card').text()).toContain('0 部视频 · 0 张图片'); w.unmount();
  });
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

  // META-11：未命名按页加载，还有下一页时摘要写成「N+」。
  it('META-11 marks the unnamed count as partial when more pages exist', async () => {
    const wrapper = mount(EntityLibraryPage, { props: { entityType: 'person' } });
    await flushPromises();
    wrapper.findComponent({ name: 'FaceClusterReviewPanel' }).vm.$emit('loaded', { unnamed: 20, appendPending: 0, unnamedHasMore: true });
    await flushPromises();
    expect(wrapper.get('[data-test="people-face-review-summary"]').text()).toContain('20+ 组未命名');
  });

  // META-08（D-PC27）：人物页注册 people.openFaceReview，待处理工作台据此跳过来并展开分区。
  it('META-08 registers people.openFaceReview which returns to the list and expands the section', async () => {
    const wrapper = mount(EntityLibraryPage, { props: { entityType: 'person', focusEntity: { id: 7, name: 'Actor Seven' } } });
    await flushPromises();
    const command = commandList().find(item => item.id === 'people.openFaceReview');
    expect(command).toBeTruthy();
    expect(wrapper.vm.selectedEntity).toEqual({ type: 'person', id: 7 });
    wrapper.vm.faceReviewCollapsed = true;

    command.run();
    await flushPromises();
    expect(wrapper.vm.selectedEntity).toBe(null);
    expect(wrapper.vm.faceReviewCollapsed).toBe(false);
    expect(wrapper.find('[data-test="people-face-review"]').exists()).toBe(true);
    // 展开算用户的选择：之后面板回报「没有待处理」也不再自动收起。
    wrapper.findComponent({ name: 'FaceClusterReviewPanel' }).vm.$emit('loaded', { unnamed: 0, appendPending: 0 });
    await flushPromises();
    expect(wrapper.vm.faceReviewCollapsed).toBe(false);

    wrapper.unmount();
    expect(commandList().map(item => item.id)).not.toContain('people.openFaceReview');
  });

  it('META-08 collections page does not register the face review command', async () => {
    const wrapper = mount(EntityLibraryPage, { props: { entityType: 'collection' } });
    await flushPromises();
    expect(commandList().map(item => item.id)).not.toContain('people.openFaceReview');
    wrapper.unmount();
  });
});

// META-03 / META-05（D-PC32）：人物页提供合并与显式删除，都要先确认。
describe('EntityLibraryPage 人物合并与删除', () => {
  const person = (id, name, videos = 1, images = 0) => ({ person: { id, display_name: name, original_name: '' }, avatar_url: '', active_video_count: videos, active_image_count: images, cursor_name: name });

  async function openPerson() {
    api.ListPeople.mockResolvedValueOnce([person(7, '张三', 2, 1), person(8, '张三', 1), person(9, '李四', 0)]);
    const wrapper = mount(EntityLibraryPage, { props: { entityType: 'person' } });
    await flushPromises();
    await wrapper.get('.entity-card').trigger('click');
    await flushPromises();
    return wrapper;
  }

  it('META-03 merges selected people into the current person after confirmation', async () => {
    const wrapper = await openPerson();
    api.ListPeople.mockResolvedValueOnce([person(7, '张三', 2, 1), person(8, '张三', 1)]);
    await wrapper.get('[data-test="person-merge-open"]').trigger('click');
    await flushPromises();
    // 不能把自己列为来源。
    expect(wrapper.find('[data-test="person-merge-option-7"]').exists()).toBe(false);
    await wrapper.get('[data-test="person-merge-option-8"] input').setValue(true);

    feedback.confirmAction.mockResolvedValueOnce(false);
    await wrapper.get('[data-test="person-merge-submit"]').trigger('click');
    await flushPromises();
    expect(api.MergePeople).not.toHaveBeenCalled();
    expect(feedback.confirmAction.mock.calls[0][0].message).toContain('「张三」 合并到「Actor Seven」');
    expect(feedback.confirmAction.mock.calls[0][0].message).toContain('此操作不能撤销');

    api.MergePeople.mockResolvedValueOnce({ target: person(7, '张三', 3, 1), merged_count: 1, video_links_moved: 1, image_links_moved: 0, warnings: ['头像复制失败'] });
    await wrapper.get('[data-test="person-merge-submit"]').trigger('click');
    await flushPromises();
    expect(api.MergePeople).toHaveBeenCalledWith(7, [8]);
    expect(feedback.notifySuccess).toHaveBeenCalledWith(expect.stringContaining('已把 1 个人物合并到'));
    expect(feedback.notify).toHaveBeenCalledWith(expect.stringContaining('头像复制失败'));
    expect(wrapper.find('[data-test="person-merge-panel"]').exists()).toBe(false);
    expect(wrapper.vm.items.map(item => item.person.id)).toEqual([7, 9]);
    // 合并后重读目标人物的详情，关系与计数以后端为准。
    expect(api.GetPersonDetail).toHaveBeenLastCalledWith(7, 0, 30);
  });

  it('META-03 explains invalid_merge and keeps the panel open', async () => {
    const wrapper = await openPerson();
    api.ListPeople.mockResolvedValueOnce([person(8, '张三', 1)]);
    await wrapper.get('[data-test="person-merge-open"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="person-merge-option-8"] input').setValue(true);
    api.MergePeople.mockRejectedValueOnce(new Error('invalid_merge'));
    await wrapper.get('[data-test="person-merge-submit"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="person-merge-panel"]').text()).toContain('不能把人物合并到它自己');
  });

  it('META-05 deletes a person after showing the relation counts', async () => {
    const wrapper = await openPerson();
    api.GetPersonDeletionImpact.mockResolvedValue({ video_count: 2, image_count: 1, face_cluster_count: 3 });
    api.DeletePerson.mockResolvedValue();

    feedback.confirmAction.mockResolvedValueOnce(false);
    await wrapper.get('[data-test="person-delete"]').trigger('click');
    await flushPromises();
    expect(api.GetPersonDeletionImpact).toHaveBeenCalledWith(7);
    const message = feedback.confirmAction.mock.calls[0][0].message;
    expect(message).toContain('2 部视频、1 张图片');
    expect(message).toContain('3 组人脸回到待命名');
    expect(message).toContain('文件和片库记录都会保留');
    expect(api.DeletePerson).not.toHaveBeenCalled();

    await wrapper.get('[data-test="person-delete"]').trigger('click');
    await flushPromises();
    expect(api.DeletePerson).toHaveBeenCalledWith(7);
    expect(wrapper.vm.selectedEntity).toBe(null);
    expect(feedback.notifySuccess).toHaveBeenCalledWith(expect.stringContaining('已删除人物'));
  });

  it('META-05 does not delete when the impact cannot be read', async () => {
    const wrapper = await openPerson();
    api.GetPersonDeletionImpact.mockRejectedValueOnce(new Error('person_not_found'));
    await wrapper.get('[data-test="person-delete"]').trigger('click');
    await flushPromises();
    expect(feedback.confirmAction).not.toHaveBeenCalled();
    expect(api.DeletePerson).not.toHaveBeenCalled();
    expect(wrapper.get('.entity-library__error').text()).toContain('有人物已经不存在了');
  });
});

// D-PC42：人物页保存观看进度改为 5 参调用（origin 取 resume/start/jump，时长未知传 0）。
describe('EntityLibraryPage 观看进度 origin', () => {
  it('passes duration and origin from the drawer, defaulting to resume with unknown duration', async () => {
    api.UpdateVideoWatchProgress.mockResolvedValue({ id: 3 });
    const wrapper = mount(EntityLibraryPage, { props: { entityType: 'person' } });
    await flushPromises();

    wrapper.vm.handleWatchProgress({ videoID: 3, positionSeconds: 42, completed: false });
    wrapper.vm.handleWatchProgress({ videoID: 3, positionSeconds: 50, completed: true, origin: 'jump', durationSeconds: 120 });
    wrapper.vm.handleWatchProgress({ videoID: 3, positionSeconds: 1, completed: false, origin: 'bogus', durationSeconds: Number.NaN });
    await flushPromises();
    expect(api.UpdateVideoWatchProgress.mock.calls).toEqual([
      [3, 42, 0, false, 'resume'],
      [3, 50, 120, true, 'jump'],
      [3, 1, 0, false, 'resume'],
    ]);
  });
});

// P-037：PersonMediaDeleteDialog 的 restored（撤销删除或从回收站恢复）由本页与抽屉接上。
describe('EntityLibraryPage 撤销删除后的刷新', () => {
  async function openPerson() {
    const person = { person: { id: 7, display_name: '人物' }, active_video_count: 1, active_image_count: 1 };
    api.ListPeople.mockResolvedValueOnce([person]);
    api.GetPersonDetail.mockResolvedValueOnce({ person, videos: [{ id: 21, name: 'clip.mp4', size: 100 }], images: [{ id: 11, name: 'a.jpg', size: 200 }] });
    const w = mount(EntityLibraryPage, { props: { entityType: 'person' }, global: { stubs: { teleport: true } } });
    await flushPromises();
    await w.get('.entity-card').trigger('click');
    await flushPromises();
    return { w, person };
  }

  it('LIB-12 本页删除后撤销：重读当前人物的视频、图片与计数，并让停在人物详情的抽屉也重读', async () => {
    const { w, person } = await openPerson();
    const drawerReload = vi.fn();
    w.vm.$refs.personDrawer.handlePersonMediaRestored = drawerReload;
    w.vm.personMediaDeleteTarget = { kind: 'image', media: { id: 11, name: 'a.jpg', size: 200 }, personID: 7 };
    await flushPromises();
    const restoredPerson = { ...person, active_image_count: 1 };
    api.GetPersonDetail.mockResolvedValueOnce({ person: restoredPerson, videos: [{ id: 21, name: 'clip.mp4', size: 100 }], images: [{ id: 11, name: 'a.jpg', size: 200 }] });
    const calls = api.GetPersonDetail.mock.calls.length;
    w.findComponent({ name: 'PersonMediaDeleteDialog' }).vm.$emit('restored', { kind: 'image', ids: [11] });
    await flushPromises();
    expect(api.GetPersonDetail.mock.calls.length).toBe(calls + 1);
    expect(api.GetPersonDetail).toHaveBeenLastCalledWith(7, 0, 30);
    expect(w.vm.entityImages.map(image => image.id)).toEqual([11]);
    expect(drawerReload).toHaveBeenCalledWith({ kind: 'image', ids: [11] }, false);
    w.unmount();
  });

  it('LIB-12 抽屉里的删除被撤销（media-restored）：本页重读当前人物', async () => {
    const { w } = await openPerson();
    const calls = api.GetPersonDetail.mock.calls.length;
    w.findComponent({ name: 'PreviewDrawer' }).vm.$emit('media-restored', { kind: 'video', ids: [21] });
    await flushPromises();
    expect(api.GetPersonDetail.mock.calls.length).toBe(calls + 1);
    w.unmount();
  });
});
