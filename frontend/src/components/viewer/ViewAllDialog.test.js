import { flushPromises, mount, enableAutoUnmount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vitest';
import ViewAllDialog, { VIEW_ALL_PAGE_SIZE } from './ViewAllDialog.vue';

enableAutoUnmount(afterEach);

const toCard = item => (item.skip ? null : { key: item.id, videoID: item.id + 100, title: `片${item.id}`, cover: '', subtitle: '', badge: '' });

async function mountDialog(fetchPage) {
  const wrapper = mount(ViewAllDialog, { props: { title: '追到哪了', fetchPage, toCard } });
  await flushPromises();
  return wrapper;
}

describe('ViewAllDialog', () => {
  it('pages by raw item count, drops null cards and duplicates that shifted between pages', async () => {
    const fetchPage = vi.fn()
      .mockResolvedValueOnce({ items: [{ id: 1 }, { id: 2, skip: true }, { id: 3 }], total: 5, has_more: true })
      .mockResolvedValueOnce({ items: [{ id: 3 }, { id: 4 }], total: 5, has_more: false });
    const wrapper = await mountDialog(fetchPage);
    expect(fetchPage).toHaveBeenCalledWith(0, VIEW_ALL_PAGE_SIZE);
    expect(wrapper.findAll('[data-test="view-all-item"]')).toHaveLength(2);
    await wrapper.get('[data-test="view-all-more"]').trigger('click');
    await flushPromises();
    expect(fetchPage).toHaveBeenLastCalledWith(3, VIEW_ALL_PAGE_SIZE);
    expect(wrapper.findAll('[data-test="view-all-item"]').map(item => item.text())).toEqual(['片1', '片3', '片4']);
    expect(wrapper.find('[data-test="view-all-more"]').exists()).toBe(false);
  });

  it('keeps what it has on failure and retries the same page', async () => {
    const fetchPage = vi.fn()
      .mockResolvedValueOnce({ items: [{ id: 1 }], total: 2, has_more: true })
      .mockRejectedValueOnce(new Error('boom'))
      .mockResolvedValueOnce({ items: [{ id: 2 }], total: 2, has_more: false });
    const wrapper = await mountDialog(fetchPage);
    await wrapper.get('[data-test="view-all-more"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-test="view-all-error"]').text()).toContain('读取失败');
    expect(wrapper.findAll('[data-test="view-all-item"]')).toHaveLength(1);
    await wrapper.get('[data-test="view-all-retry"]').trigger('click');
    await flushPromises();
    expect(fetchPage).toHaveBeenLastCalledWith(1, VIEW_ALL_PAGE_SIZE);
    expect(wrapper.findAll('[data-test="view-all-item"]')).toHaveLength(2);
    expect(wrapper.find('[data-test="view-all-error"]').exists()).toBe(false);
  });

  it('says so when the set emptied out', async () => {
    const wrapper = await mountDialog(vi.fn().mockResolvedValue({ items: [], total: 0, has_more: false }));
    expect(wrapper.find('[data-test="view-all-empty"]').exists()).toBe(true);
    expect(wrapper.get('[data-test="view-all-count"]').text()).toBe('共 0 个');
  });
});
