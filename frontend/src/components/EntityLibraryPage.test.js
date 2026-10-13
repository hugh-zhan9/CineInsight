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
  RestoreTrashBatch: vi.fn(),
  GetSettings: vi.fn(),
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
import { clearToasts } from '../utils/feedback.js';
import * as recentOperations from '../utils/recentOperations.js';

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
    const w = mount(EntityLibraryPage, { props: { tab: 'person' } }); await flushPromises();
    expect(w.get('.entity-card').text()).toContain(`${videos} 部视频 · ${images} 张图片`);
    expect(w.get('.entity-card').text()).not.toContain('部活跃作品'); w.unmount();
  });
  it('deletes related media after confirmation and updates the card counts without deleting the person', async () => {
    const person = { person: { id: 7, display_name: '人物' }, active_video_count: 1, active_image_count: 1 };
    api.ListPeople.mockResolvedValueOnce([person]);
    api.GetPersonDetail.mockResolvedValueOnce({ person, videos: [{ id: 21, name: 'clip.mp4', size: 100 }], images: [{ id: 11, name: 'a.jpg', size: 200 }] });
    const okResult = ids => Promise.resolve({ batch_id: 'b1', items: ids.map(id => ({ id, code: 'ok' })) });
    api.DeleteImagesWithResult.mockImplementation(okResult); api.DeleteVideosWithResult.mockImplementation(okResult);
    const w = mount(EntityLibraryPage, { props: { tab: 'person' }, global: { stubs: { teleport: true } } }); await flushPromises();
    await w.get('.entity-card').trigger('click'); await flushPromises();
    // 打开的对象：名字与已加载数量写在页头下面的分区标题上（页头本身是「人物与作品集」+ 标签）。
    expect(w.get('[data-test="entity-library-entity-head"]').text()).toContain('视频 1 / 1 部 · 图片 1 / 1 张');
    // 按钮写明后果（I-2）：从片库 / 图片库移除，不是从人物名下解绑。
    expect(w.get('[data-test="person-video-delete-21"]').text()).toBe('从片库移除…');
    expect(w.get('[data-test="person-image-delete-11"]').text()).toBe('从图片库移除…');
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
    const w = mount(EntityLibraryPage, { props: { tab: 'person' }, global: { stubs: { teleport: true } } }); await flushPromises();
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
    const w = mount(EntityLibraryPage, { props: { tab: 'person' } }); await flushPromises();
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
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'person' } });
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
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'person' } });
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
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'person' } });
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
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'collection' } });
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
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'person' } });
    await flushPromises();
    await wrapper.get('.entity-card').trigger('click');
    await flushPromises();

    expect(wrapper.findAll('.entity-video-card')).toHaveLength(1);
    await wrapper.vm.loadEntityVideos(false);
    await wrapper.vm.$nextTick();
    expect(wrapper.findAll('.entity-video-card')).toHaveLength(2);

    // 按钮换成统一按钮类（统一设计 D-UD-06）：主操作是 .btn--primary。
    await wrapper.find('.entity-video-card__actions .btn--primary').trigger('click');
    expect(api.PlayVideo).toHaveBeenCalledWith(2);
  });

  // I-3 / D-UI-07：人物与作品集属观看区，点相关视频交给 App 进整页详情，不在本页抽屉里开；
  // 人物本身的编辑抽屉（编辑与批量关联）照旧。
  it('clicking a related video hands it to App instead of opening the embedded drawer', async () => {
    api.ListPeople.mockResolvedValueOnce([{ person: { id: 7, display_name: 'Actor Seven' }, active_video_count: 1 }]);
    api.GetPersonDetail.mockResolvedValueOnce({
      person: { person: { id: 7, display_name: 'Actor Seven' }, active_video_count: 1 },
      videos: [{ id: 2, name: 'two.mp4', size: 1024, duration: 60 }], next_video_id: 0
    });
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'person' } });
    await flushPromises();
    await wrapper.get('.entity-card').trigger('click');
    await flushPromises();
    const drawers = () => wrapper.findAll('.preview-drawer-stub').map(drawer => drawer.text());

    await wrapper.get('[data-test="entity-video-detail-2"]').trigger('click');
    await wrapper.get('.entity-video-card__preview').trigger('click');
    expect(wrapper.emitted('open-video')).toEqual([[expect.objectContaining({ id: 2 })], [expect.objectContaining({ id: 2 })]]);
    expect(drawers().filter(text => text.startsWith('video:'))).toEqual([]);

    await wrapper.findAll('button').find(button => button.text() === '编辑与批量关联').trigger('click');
    expect(drawers()).toEqual(['person:7']);
    wrapper.unmount();
  });
});

describe('EntityLibraryPage 命令面板落点', () => {
  it('挂载时带着 focusEntity 就直接展开那个人物', async () => {
    const wrapper = mount(EntityLibraryPage, {
      props: { tab: 'person', focusEntity: { id: 7, name: 'Actor Seven' } }
    });
    await flushPromises();

    expect(api.GetPersonDetail).toHaveBeenCalledWith(7, 0, 30);
    expect(wrapper.text()).toContain('返回人物列表');
    // 列表本身照常加载，返回列表时不是空页。
    expect(api.ListPeople).toHaveBeenCalledWith('', '', 0, 50);
  });

  it('已挂载后 focusEntity 变化同样展开，置空不再动', async () => {
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'collection' } });
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
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'collection' } });
    await flushPromises();
    expect(commandList().map(command => command.id)).toContain('action:collection-reload');

    wrapper.unmount();
    expect(commandList().map(command => command.id)).not.toContain('action:collection-reload');
  });
});

// D-P3-04：人脸审阅是「人物与作品集」的第三个标签（原先是人物列表顶上的「待命名人脸」分区）。面板随页面一起挂载，
// 标签上的数字与摘要都来自它报上来的数量；people.openFaceReview 切到这个标签。
describe('EntityLibraryPage 人脸审阅标签', () => {
  it('三个标签：人物 · 作品集 · 人脸审阅；点标签只发 update:tab（当前标签归 App）', async () => {
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'person' } });
    await flushPromises();
    const tabs = wrapper.findComponent({ name: 'AppTabs' });
    expect(tabs.props('tabs').map(tab => [tab.key, tab.label, tab.dataTest])).toEqual([
      ['person', '人物', 'entities-manage-tab-person'],
      ['collection', '作品集', 'entities-manage-tab-collection'],
      ['faces', '人脸审阅', 'entities-manage-tab-faces']
    ]);
    expect(wrapper.get('h1').text()).toBe('人物与作品集');
    await wrapper.get('[data-test="entities-manage-tab-faces"]').trigger('click');
    expect(wrapper.emitted('update:tab')).toEqual([['faces']]);
    // App 没有接住之前什么也不变。
    expect(wrapper.vm.isFacesTab).toBe(false);
    wrapper.unmount();
  });

  it('人脸审阅标签上显示面板（全部视图）与摘要，人物列表与新建表单隐藏；面板随页面一起挂载', async () => {
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'person' } });
    await flushPromises();
    const faces = wrapper.get('[data-test="people-face-review"]');
    expect(faces.find('.face-panel-stub').exists()).toBe(true);
    expect(faces.attributes('style')).toContain('display: none');

    await wrapper.setProps({ tab: 'faces' });
    expect(wrapper.get('[data-test="people-face-review"]').attributes('style') || '').not.toContain('display: none');
    expect(wrapper.get('#entities-manage-panel-person').attributes('style')).toContain('display: none');
    // 切到人脸审阅不换实体种类：列表不重读。
    expect(api.ListPeople).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it('按面板回报的数量写摘要与标签上的数字（未命名还有下一页时带「+」，没有待处理项时不写数字）', async () => {
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'faces' } });
    await flushPromises();
    const panel = wrapper.findComponent({ name: 'FaceClusterReviewPanel' });
    const facesTab = () => wrapper.findComponent({ name: 'AppTabs' }).props('tabs').find(tab => tab.key === 'faces');
    expect(wrapper.get('[data-test="people-face-review-summary"]').text()).toBe('正在读取人脸候选…');
    expect(facesTab().count).toBeNull();

    panel.vm.$emit('loaded', { unnamed: 2, appendPending: 1 });
    await flushPromises();
    expect(wrapper.get('[data-test="people-face-review-summary"]').text()).toContain('2 组未命名 · 1 组待确认追加');
    expect(facesTab().count).toBe('3');

    panel.vm.$emit('loaded', { unnamed: 20, appendPending: 0, unnamedHasMore: true });
    await flushPromises();
    expect(wrapper.get('[data-test="people-face-review-summary"]').text()).toContain('20+ 组未命名');
    expect(facesTab().count).toBe('20+');

    panel.vm.$emit('loaded', { unnamed: 0, appendPending: 0 });
    await flushPromises();
    expect(wrapper.get('[data-test="people-face-review-summary"]').text()).toContain('暂无待处理的人脸候选');
    expect(facesTab().count).toBeNull();
    wrapper.unmount();
  });

  it('面板命名或关联成功后重读人物列表；正打开着某个人物时等回到列表再读', async () => {
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'person' } });
    await flushPromises();
    expect(api.ListPeople).toHaveBeenCalledTimes(1);
    wrapper.findComponent({ name: 'FaceClusterReviewPanel' }).vm.$emit('changed');
    await flushPromises();
    expect(api.ListPeople).toHaveBeenCalledTimes(2);

    await wrapper.setProps({ focusEntity: { id: 7, name: 'Actor Seven' } });
    await flushPromises();
    expect(wrapper.vm.selectedEntity).toEqual({ type: 'person', id: 7 });
    wrapper.findComponent({ name: 'FaceClusterReviewPanel' }).vm.$emit('changed');
    await flushPromises();
    expect(api.ListPeople).toHaveBeenCalledTimes(2);
    wrapper.vm.closeEntity();
    await flushPromises();
    expect(api.ListPeople).toHaveBeenCalledTimes(3);
    wrapper.unmount();
  });

  // META-08（D-PC27）：people.openFaceReview 由本页注册（两种实体都有），执行时切到「人脸审阅」标签。
  it('META-08 registers people.openFaceReview which switches to the face review tab, on both entity tabs', async () => {
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'collection' } });
    await flushPromises();
    const command = commandList().find(item => item.id === 'people.openFaceReview');
    expect(command).toBeTruthy();
    command.run();
    expect(wrapper.emitted('update:tab')).toEqual([['faces']]);

    wrapper.unmount();
    expect(commandList().map(item => item.id)).not.toContain('people.openFaceReview');
  });

  it('换到另一种实体：列表从头读，打开的对象、搜索与刷新命令跟着换', async () => {
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'person', focusEntity: { id: 7, name: 'Actor Seven' } } });
    await flushPromises();
    wrapper.vm.keyword = '张';
    expect(commandList().map(item => item.id)).toContain('action:person-reload');

    await wrapper.setProps({ tab: 'collection' });
    await flushPromises();
    expect(wrapper.vm.selectedEntity).toBeNull();
    expect(wrapper.vm.keyword).toBe('');
    expect(api.ListCollections).toHaveBeenLastCalledWith('', '', 0, 50);
    const ids = commandList().map(item => item.id);
    expect(ids).toContain('action:collection-reload');
    expect(ids).not.toContain('action:person-reload');

    // 去人脸审阅再回来：不换种类，不重读。
    const calls = api.ListCollections.mock.calls.length;
    await wrapper.setProps({ tab: 'faces' });
    await wrapper.setProps({ tab: 'collection' });
    await flushPromises();
    expect(api.ListCollections.mock.calls.length).toBe(calls);
    wrapper.unmount();
  });
});

// 列表与对象区的三种状态（首次读取骨架屏、读失败横幅 + 重试、读到了但没有时给下一步）。
describe('EntityLibraryPage 加载 / 空 / 出错', () => {
  it('首次读取时是骨架屏；读失败横幅 + 重试（重试从头读），不显示空状态', async () => {
    let finish;
    api.ListPeople.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'person' } });
    await flushPromises();
    expect(wrapper.find('[data-test="entity-library-loading"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="entity-library-empty"]').exists()).toBe(false);
    finish([]);
    await flushPromises();
    expect(wrapper.find('[data-test="entity-library-loading"]').exists()).toBe(false);

    api.ListPeople.mockRejectedValueOnce(new Error('数据库繁忙'));
    await wrapper.vm.reload();
    await flushPromises();
    expect(wrapper.get('[data-test="entity-library-list-error"]').text()).toContain('人物列表没读到');
    expect(wrapper.get('[data-test="entity-library-list-error"]').text()).toContain('数据库繁忙');
    expect(wrapper.find('[data-test="entity-library-empty"]').exists()).toBe(false);

    api.ListPeople.mockResolvedValueOnce([{ person: { id: 7, display_name: '张三' }, active_video_count: 1 }]);
    await wrapper.get('[data-test="entity-library-list-retry"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-test="entity-library-list-error"]').exists()).toBe(false);
    expect(wrapper.findAll('.entity-card')).toHaveLength(1);
    wrapper.unmount();
  });

  it('空状态给下一步：人物去人脸审阅、作品集聚焦新建、搜不到时清空搜索', async () => {
    const people = mount(EntityLibraryPage, { props: { tab: 'person' } });
    await flushPromises();
    await people.get('[data-test="entity-library-go-faces"]').trigger('click');
    expect(people.emitted('update:tab')).toEqual([['faces']]);
    people.vm.keyword = '没有';
    await people.vm.reload();
    await flushPromises();
    await people.get('[data-test="entity-library-clear-search"]').trigger('click');
    await flushPromises();
    expect(people.vm.keyword).toBe('');
    expect(api.ListPeople).toHaveBeenLastCalledWith('', '', 0, 50);
    people.unmount();

    const collections = mount(EntityLibraryPage, { props: { tab: 'collection' }, attachTo: document.body });
    await flushPromises();
    await collections.get('[data-test="entity-library-focus-create"]').trigger('click');
    expect(document.activeElement?.getAttribute('aria-label')).toBe('新作品集的名称');
    collections.unmount();
  });

  it('打开的对象读失败：横幅 + 重试（从头重读这个对象），读到之前是骨架屏', async () => {
    api.GetCollectionDetail.mockRejectedValueOnce(new Error('读取超时'));
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'collection', focusEntity: { id: 5, name: 'Saga' } } });
    await flushPromises();
    expect(wrapper.get('[data-test="entity-library-entity-error"]').text()).toContain('读取超时');
    expect(wrapper.find('[data-test="entity-videos-empty"]').exists()).toBe(false);

    let finish;
    api.GetCollectionDetail.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    await wrapper.get('[data-test="entity-library-entity-retry"]').trigger('click');
    expect(wrapper.find('[data-test="entity-library-entity-loading"]').exists()).toBe(true);
    finish({ collection: { collection: { id: 5, name: 'Saga' }, active_video_count: 0 }, videos: [] });
    await flushPromises();
    expect(wrapper.find('[data-test="entity-library-entity-error"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="entity-videos-empty"]').text()).toContain('当前还没有关联视频');
    wrapper.unmount();
  });
});

// META-03 / META-05（D-PC32）：人物页提供合并与显式删除，都要先确认。
describe('EntityLibraryPage 人物合并与删除', () => {
  const person = (id, name, videos = 1, images = 0) => ({ person: { id, display_name: name, original_name: '' }, avatar_url: '', active_video_count: videos, active_image_count: images, cursor_name: name });

  async function openPerson() {
    api.ListPeople.mockResolvedValueOnce([person(7, '张三', 2, 1), person(8, '张三', 1), person(9, '李四', 0)]);
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'person' } });
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
    const wrapper = mount(EntityLibraryPage, { props: { tab: 'person' } });
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
    const w = mount(EntityLibraryPage, { props: { tab: 'person' }, global: { stubs: { teleport: true } } });
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

  it('LIB-12 删除对话框关掉以后再从「最近操作」撤销：照样恢复，本页重读当前人物', async () => {
    clearToasts();
    recentOperations.clear();
    // 「删除前确认」关着也先弹确认框（破坏性流程复审 I-2）：用户确认之前不调删除；确认后只删记录。
    api.GetSettings.mockResolvedValueOnce({ confirm_before_delete: false, delete_original_file: false });
    api.DeleteImagesWithResult.mockResolvedValueOnce({ batch_id: 'b-person', items: [{ id: 11, code: 'ok' }] });
    api.RestoreTrashBatch.mockResolvedValueOnce({ items: [{ id: 11, code: 'ok' }] });
    const { w } = await openPerson();
    w.vm.personMediaDeleteTarget = { kind: 'image', media: { id: 11, name: 'a.jpg', size: 200 }, personID: 7 };
    await flushPromises();
    expect(w.find('[data-test="person-media-delete-dialog"]').exists()).toBe(true);
    expect(api.DeleteImagesWithResult).not.toHaveBeenCalled();
    await w.get('[data-test="person-media-delete-confirm"]').trigger('click');
    await flushPromises();
    expect(api.DeleteImagesWithResult).toHaveBeenCalledWith([11], false, expect.any(String));
    expect(w.vm.entityImages).toEqual([]);
    // 撤销提示到时收起：对话框关掉、卸下。
    clearToasts();
    await flushPromises();
    expect(w.findComponent({ name: 'PersonMediaDeleteDialog' }).exists()).toBe(false);

    const calls = api.GetPersonDetail.mock.calls.length;
    expect(await recentOperations.undo(recentOperations.list()[0].id)).toBe(true);
    await flushPromises();
    expect(api.RestoreTrashBatch).toHaveBeenCalledWith('image', 'b-person');
    expect(api.GetPersonDetail.mock.calls.length).toBe(calls + 1);
    expect(api.GetPersonDetail).toHaveBeenLastCalledWith(7, 0, 30);
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
