import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  GetImageDetail: vi.fn(),
  GetWallpaperPreflight: vi.fn(),
  LogFrontend: vi.fn(),
  RecordImageView: vi.fn(),
  SetImageFavorite: vi.fn(),
  SetImageRating: vi.fn(),
  SetWallpaper: vi.fn()
}));
vi.mock('../../../wailsjs/go/main/App', async importOriginal => ({ ...(await importOriginal()), ...api }));

import PhotoLightbox, { photoFolderName, photoLightboxFacts, photoOriginalURL } from './PhotoLightbox.vue';
import { VIEW_RECORD_DELAY_MS } from '../PhotoLibraryPage.vue';
import DialogShell from '../ui/DialogShell.vue';

enableAutoUnmount(afterEach);

const image = (id, extra = {}) => ({
  id,
  name: `p${id}.jpg`,
  directory: '/Volumes/Photos/2026/旅行',
  width: 4032,
  height: 3024,
  is_favorite: false,
  personal_rating: null,
  taken_at: '2026-10-03T14:05:00',
  created_at: '2026-10-04T00:00:00Z',
  camera_make: 'Apple',
  camera_model: 'iPhone 15 Pro',
  lens_model: '后置主摄',
  iso: 100,
  f_number: 1.78,
  exposure_time: '1/120',
  focal_length: 6.86,
  ...extra
});
const person = (id, name) => ({ person: { id, display_name: name }, active_image_count: 3 });

beforeEach(() => {
  vi.clearAllMocks();
  api.GetImageDetail.mockReset().mockImplementation(id => Promise.resolve({ image: image(id, { view_count: 2 }), people: [person(5, '甲'), person(6, '乙')] }));
  api.RecordImageView.mockReset().mockResolvedValue({ recorded: true, view_count: 3, last_viewed_at: '2026-10-14T10:00:00Z' });
  api.SetImageFavorite.mockReset().mockImplementation((id, value) => Promise.resolve(image(id, { is_favorite: value, favorited_at: value ? '2026-10-14T10:00:00Z' : null })));
  api.SetImageRating.mockReset().mockImplementation((id, value) => Promise.resolve(image(id, { personal_rating: value })));
  api.GetWallpaperPreflight.mockReset().mockResolvedValue({ eligible: true, message: '' });
  api.LogFrontend.mockReset().mockResolvedValue(undefined);
});

async function mountLightbox(props = {}, options = {}) {
  const wrapper = mount(PhotoLightbox, {
    props: { image: image(1), hasPrev: true, hasNext: true, ...props },
    global: { stubs: { teleport: true } },
    ...options
  });
  await flushPromises();
  return wrapper;
}

const patches = wrapper => (wrapper.emitted('patch') || []).map(([fields]) => fields);

describe('PhotoLightbox 纯函数', () => {
  it('信息面板：拍摄时间、相机、镜头、参数、尺寸、文件夹；没有值的不列，没有拍摄时间写明', () => {
    const facts = photoLightboxFacts(image(1));
    expect(facts.map(fact => fact.label)).toEqual(['拍摄时间', '相机', '镜头', '参数', '尺寸', '文件夹']);
    expect(facts.find(fact => fact.key === 'camera').value).toBe('Apple iPhone 15 Pro');
    expect(facts.find(fact => fact.key === 'exposure').value).toBe('f/1.8 · 1/120 秒 · ISO 100 · 7 mm');
    expect(facts.find(fact => fact.key === 'size').value).toBe('4032 × 3024');
    expect(facts.find(fact => fact.key === 'folder')).toMatchObject({ value: '旅行', title: '/Volumes/Photos/2026/旅行' });

    const bare = photoLightboxFacts(image(2, { taken_at: null, camera_make: '', camera_model: '', lens_model: '', iso: 0, f_number: 0, exposure_time: '', focal_length: 0, width: 0, height: 0 }));
    expect(bare).toEqual([
      { key: 'taken', label: '拍摄时间', value: '没有记录' },
      { key: 'folder', label: '文件夹', value: '旅行', title: '/Volumes/Photos/2026/旅行' }
    ]);
    expect(photoLightboxFacts(null)).toEqual([]);
  });

  it('文件夹只取最后一级，原图走 /preview/image/{id}', () => {
    expect(photoFolderName('/a/b/c/')).toBe('c');
    expect(photoFolderName('/a')).toBe('a');
    expect(photoFolderName('')).toBe('');
    expect(photoOriginalURL('7')).toBe('/preview/image/7');
  });
});

describe('PhotoLightbox 看图', () => {
  it('全屏、标题是文件名；原图、信息、人物、设为壁纸、「在图片管理中打开」都在；读到的详情并回宿主', async () => {
    const wrapper = await mountLightbox();
    const shell = wrapper.findComponent(DialogShell);
    expect(shell.props()).toMatchObject({ size: 'full', title: 'p1.jpg' });
    expect(wrapper.get('[data-test="photo-lightbox-img"]').attributes('src')).toBe('/preview/image/1');
    expect(wrapper.get('[data-test="photo-lightbox-facts"]').text()).toContain('Apple iPhone 15 Pro');
    expect(api.GetImageDetail).toHaveBeenCalledWith(1);
    expect(patches(wrapper)[0]).toMatchObject({ id: 1, view_count: 2 });
    expect(wrapper.find('[data-test="wallpaper-set"]').exists()).toBe(true);
    expect(api.GetWallpaperPreflight).toHaveBeenCalledWith('image', 1);

    await wrapper.get('[data-test="photo-lightbox-person-6"]').trigger('click');
    expect(wrapper.emitted('open-person')).toEqual([[{ id: 6, name: '乙' }]]);
    await wrapper.get('[data-test="photo-lightbox-open-manage"]').trigger('click');
    expect(wrapper.emitted('open-in-manage')[0][0]).toMatchObject({ id: 1, name: 'p1.jpg' });
    await wrapper.get('[data-test="photo-lightbox-delete"]').trigger('click');
    expect(wrapper.emitted('delete-image')[0][0].id).toBe(1);
  });

  it('提供移除入口，其余整理操作留在图片管理', async () => {
    const wrapper = await mountLightbox();
    const text = wrapper.text();
    for (const word of ['删除', '废纸篓', '标签', 'AI', '批量', '在访达中显示', '重新分析', '解除']) {
      expect(text, word).not.toContain(word);
    }
    expect(wrapper.findAll('input, textarea, select')).toHaveLength(0);
  });

  it('人物读失败只在人物这一块说明；没有标出人物时写明', async () => {
    api.GetImageDetail.mockRejectedValueOnce(new Error('数据库忙'));
    const failed = await mountLightbox();
    expect(failed.get('[data-test="photo-lightbox-people"]').text()).toContain('人物没有读到：数据库忙');
    api.GetImageDetail.mockResolvedValueOnce({ image: image(1), people: [] });
    const empty = await mountLightbox();
    expect(empty.get('[data-test="photo-lightbox-people"]').text()).toContain('这张图片还没有标出人物');
  });

  it('上一张 / 下一张按宿主给的首尾禁用；续载下一批时下一张转圈禁用；续载失败给重试', async () => {
    const wrapper = await mountLightbox({ hasPrev: false, hasNext: true });
    expect(wrapper.get('[data-test="photo-lightbox-prev"]').attributes('disabled')).toBeDefined();
    await wrapper.get('[data-test="photo-lightbox-next"]').trigger('click');
    expect(wrapper.emitted('next')).toHaveLength(1);

    await wrapper.setProps({ hasPrev: true, hasNext: false });
    expect(wrapper.get('[data-test="photo-lightbox-next"]').attributes('disabled')).toBeDefined();
    await wrapper.get('[data-test="photo-lightbox-prev"]').trigger('click');
    expect(wrapper.emitted('prev')).toHaveLength(1);

    await wrapper.setProps({ hasNext: true, loadingNext: true });
    expect(wrapper.get('[data-test="photo-lightbox-next"]').attributes('disabled')).toBeDefined();
    expect(wrapper.get('[data-test="photo-lightbox-next"]').attributes('aria-label')).toBe('正在加载下一批');

    await wrapper.setProps({ loadingNext: false, nextError: '网络断开' });
    expect(wrapper.get('[data-test="photo-lightbox-next-error"]').text()).toContain('网络断开');
    await wrapper.get('[data-test="photo-lightbox-next-retry"]').trigger('click');
    expect(wrapper.emitted('next')).toHaveLength(2);
  });

  it('键盘：← → 切换（到头不发），F 收藏；焦点在输入框里或上面另有对话框时不接；Esc 关闭', async () => {
    const wrapper = await mountLightbox({ hasPrev: false, hasNext: true }, { attachTo: document.body });
    const press = key => document.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }));
    press('ArrowLeft');
    press('ArrowRight');
    expect(wrapper.emitted('prev')).toBeUndefined();
    expect(wrapper.emitted('next')).toHaveLength(1);
    press('f');
    await flushPromises();
    expect(api.SetImageFavorite).toHaveBeenCalledWith(1, true);

    const input = document.createElement('input');
    document.body.append(input);
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
    expect(wrapper.emitted('next')).toHaveLength(1);
    input.remove();

    // 叠在上面的对话框（例如确认框）是最上面一层：方向键与 Esc 都归它。
    const above = mount(DialogShell, { props: { title: '确认' }, attachTo: document.body });
    press('ArrowRight');
    expect(wrapper.emitted('next')).toHaveLength(1);
    press('Escape');
    expect(above.emitted('close')).toHaveLength(1);
    expect(wrapper.emitted('close')).toBeUndefined();
    above.unmount();

    press('Escape');
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  // 2026-10-14：「焦点在文本框里」与全站共用 utils/keyboardShortcuts.js 的 isTextEntryTarget——role=textbox 与各种可编辑区域
  // 也算；按钮照旧不算（← → F 照常），勾选框等不收文字的 input 也不再算（以前任何 input 都挡）。
  it('键盘：焦点在任何文本框里不接；焦点在按钮、勾选框上照常切换', async () => {
    const wrapper = await mountLightbox({ hasPrev: true, hasNext: true }, { attachTo: document.body });
    const pressOn = (html, key = 'ArrowRight') => {
      const holder = document.createElement('div');
      holder.innerHTML = html;
      document.body.append(holder);
      holder.firstElementChild.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }));
      holder.remove();
    };
    for (const html of [
      '<input type="search">', '<textarea></textarea>', '<select></select>', '<div role="textbox"></div>',
      '<div contenteditable="plaintext-only"></div>', '<div contenteditable="true"></div>'
    ]) {
      pressOn(html);
      pressOn(html, 'f');
    }
    expect(wrapper.emitted('next')).toBeUndefined();
    expect(api.SetImageFavorite).not.toHaveBeenCalled();
    pressOn('<button type="button">按钮</button>');
    expect(wrapper.emitted('next')).toHaveLength(1);
    pressOn('<input type="checkbox">', 'ArrowLeft');
    expect(wrapper.emitted('prev')).toHaveLength(1);
  });

  it('收藏先改后存：成功并回库里的值并发 changed；失败改回原值、在面板上说原因', async () => {
    const wrapper = await mountLightbox();
    await wrapper.get('[data-test="photo-lightbox-favorite"]').trigger('click');
    await flushPromises();
    expect(api.SetImageFavorite).toHaveBeenCalledWith(1, true);
    expect(patches(wrapper).slice(-2)).toEqual([
      { id: 1, is_favorite: true },
      { id: 1, is_favorite: true, favorited_at: '2026-10-14T10:00:00Z' }
    ]);
    expect(wrapper.emitted('changed')).toEqual([[{ id: 1, is_favorite: true, favorited_at: '2026-10-14T10:00:00Z' }]]);

    api.SetImageFavorite.mockRejectedValueOnce(new Error('database is in maintenance mode'));
    await wrapper.setProps({ image: image(1, { is_favorite: true }) });
    await flushPromises();
    await wrapper.get('[data-test="photo-lightbox-favorite"]').trigger('click');
    await flushPromises();
    expect(patches(wrapper).slice(-2)).toEqual([{ id: 1, is_favorite: false }, { id: 1, is_favorite: true }]);
    expect(wrapper.emitted('changed')).toHaveLength(1);
    expect(wrapper.get('[data-test="photo-lightbox-error"]').text()).toContain('收藏没有改成：数据库正在恢复备份或切换后端');
    expect(wrapper.get('[data-test="photo-lightbox-favorite"]').attributes('disabled')).toBeUndefined();
  });

  it('评分点星即存：成功发 changed；失败改回原来的分数并说原因；点同一个分数不发请求', async () => {
    const wrapper = await mountLightbox({ image: image(1, { personal_rating: 6 }) });
    await wrapper.get('[data-test="detail-star-6"]').trigger('click');
    expect(api.SetImageRating).not.toHaveBeenCalled();

    await wrapper.get('[data-test="detail-star-8.5"]').trigger('click');
    await flushPromises();
    expect(api.SetImageRating).toHaveBeenCalledWith(1, 8.5);
    expect(patches(wrapper).slice(-2)).toEqual([{ id: 1, personal_rating: 8.5 }, { id: 1, personal_rating: 8.5 }]);
    expect(wrapper.emitted('changed')).toEqual([[{ id: 1, personal_rating: 8.5 }]]);

    api.SetImageRating.mockRejectedValueOnce(new Error('写入失败'));
    await wrapper.get('[data-test="detail-star-3"]').trigger('click');
    await flushPromises();
    expect(patches(wrapper).slice(-2)).toEqual([{ id: 1, personal_rating: 3 }, { id: 1, personal_rating: 6 }]);
    expect(wrapper.get('[data-test="photo-lightbox-error"]').text()).toContain('评分没有保存：写入失败');
    expect(wrapper.emitted('changed')).toHaveLength(1);
  });

  it('换一张时重读人物并清掉上一张的错误与打不开状态', async () => {
    api.SetImageFavorite.mockRejectedValueOnce(new Error('坏了'));
    const wrapper = await mountLightbox();
    await wrapper.get('[data-test="photo-lightbox-favorite"]').trigger('click');
    await flushPromises();
    await wrapper.get('[data-test="photo-lightbox-img"]').trigger('error');
    expect(wrapper.find('[data-test="photo-lightbox-fallback"]').exists()).toBe(true);
    expect(wrapper.find('[data-test="photo-lightbox-error"]').exists()).toBe(true);

    await wrapper.setProps({ image: image(2) });
    await flushPromises();
    expect(api.GetImageDetail).toHaveBeenLastCalledWith(2);
    expect(wrapper.find('[data-test="photo-lightbox-fallback"]').exists()).toBe(false);
    expect(wrapper.find('[data-test="photo-lightbox-error"]').exists()).toBe(false);
    expect(wrapper.get('[data-test="photo-lightbox-img"]').attributes('src')).toBe('/preview/image/2');
  });
});

describe('PhotoLightbox 查看记录（与图片管理同口径）', () => {
  it('同一张显示满 1.5 秒才记一次；换一张或关闭就取消；记到的次数并回宿主', async () => {
    vi.useFakeTimers();
    try {
      const wrapper = await mountLightbox();
      vi.advanceTimersByTime(VIEW_RECORD_DELAY_MS - 500);
      await wrapper.setProps({ image: image(2) });
      vi.advanceTimersByTime(VIEW_RECORD_DELAY_MS - 1);
      expect(api.RecordImageView).not.toHaveBeenCalled();
      vi.advanceTimersByTime(1);
      await flushPromises();
      expect(api.RecordImageView).toHaveBeenCalledTimes(1);
      expect(api.RecordImageView).toHaveBeenCalledWith(2);
      expect(patches(wrapper).at(-1)).toEqual({ id: 2, view_count: 3, last_viewed_at: '2026-10-14T10:00:00Z' });

      await wrapper.setProps({ image: image(3) });
      vi.advanceTimersByTime(500);
      wrapper.unmount();
      vi.advanceTimersByTime(5000);
      await flushPromises();
      expect(api.RecordImageView).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it('原图打不开不算看过；记录失败只记日志', async () => {
    vi.useFakeTimers();
    try {
      const wrapper = await mountLightbox();
      await wrapper.get('[data-test="photo-lightbox-img"]').trigger('error');
      vi.advanceTimersByTime(VIEW_RECORD_DELAY_MS * 2);
      await flushPromises();
      expect(api.RecordImageView).not.toHaveBeenCalled();

      api.RecordImageView.mockRejectedValueOnce(new Error('写不进去'));
      await wrapper.setProps({ image: image(2) });
      vi.advanceTimersByTime(VIEW_RECORD_DELAY_MS);
      await flushPromises();
      expect(api.RecordImageView).toHaveBeenCalledWith(2);
      expect(api.LogFrontend).toHaveBeenCalled();
      expect(wrapper.find('[data-test="photo-lightbox-error"]').exists()).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });
});
