import { mount, flushPromises } from '@vue/test-utils';
import { describe, it, expect, vi } from 'vitest';
import BTResourceDetail from './BTResourceDetail.vue';
const token = `ci-bt-${'a'.repeat(48)}`;
const resource = { result_token: token, title: '资源', size: '10' };
const pending = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };
describe('PT resource detail loading', () => {
 it('loads once by token and renders text, MediaInfo and only safe image paths without download side effects', async () => {
  const request = pending(), loadDetail = vi.fn().mockReturnValue(request.promise);
  const w = mount(BTResourceDetail, { props: { resource, loadDetail } });
  await flushPromises();
  expect(w.get('[data-test="bt-detail-loading"]').text()).toContain('正在读取');
  expect(loadDetail).toHaveBeenCalledExactlyOnceWith(token);
  request.resolve({ description: '<script>原站正文</script>', media_info: 'Video\nHEVC', image_paths: [`/preview/bt-detail-image/${token}/1/0`, 'https://evil.example/a.png', `/preview/bt-detail-image/${token}/1/16`] });
  await flushPromises();
  expect(w.get('[data-test="bt-detail-description"]').text()).toBe('<script>原站正文</script>');
  expect(w.find('script').exists()).toBe(false);
  expect(w.get('[data-test="bt-detail-mediainfo"]').text()).toBe('Video\nHEVC');
  expect(w.findAll('img')).toHaveLength(1);
  expect(w.emitted('preview')).toBeUndefined();
  await w.get('img').trigger('error'); expect(w.text()).toContain('截图加载失败');
  w.unmount();
 });
 it('shows a safe auth error and explicitly retries, distinguishing an empty successful response', async () => {
  const loadDetail = vi.fn().mockRejectedValueOnce('bt_auth_failed: private-token').mockResolvedValue({ description: '', media_info: '', image_paths: [] });
  const w = mount(BTResourceDetail, { props: { resource, loadDetail } }); await flushPromises();
  expect(w.get('[data-test="bt-detail-error"]').text()).toContain('鉴权失败'); expect(w.text()).not.toContain('private-token');
  await w.get('[data-test="bt-detail-retry"]').trigger('click'); await flushPromises();
  expect(loadDetail).toHaveBeenCalledTimes(2); expect(w.get('[data-test="bt-detail-empty"]').exists()).toBe(true); w.unmount();
 });
 it('discards old resource and hidden responses and resumes from the current resource', async () => {
  const old = pending(), hidden = pending();
  const loadDetail = vi.fn().mockReturnValueOnce(old.promise).mockResolvedValueOnce({ description: '当前正文' }).mockReturnValueOnce(hidden.promise).mockResolvedValue({ description: '重新打开' });
  const w = mount(BTResourceDetail, { props: { resource, loadDetail } });
  await w.setProps({ resource: { ...resource, result_token: 'new' } }); await flushPromises();
  old.resolve({ description: '过时正文' }); await flushPromises();
  expect(w.text()).toContain('当前正文'); expect(w.text()).not.toContain('过时正文');
  await w.get('[data-test="bt-detail-retry"]').trigger('click'); await w.setProps({ active: false });
  hidden.resolve({ description: '隐藏时迟到' }); await flushPromises(); expect(w.text()).not.toContain('隐藏时迟到');
  expect(w.get('[data-test="bt-detail-retry"]').element.disabled).toBe(true);
  await w.setProps({ active: true }); await flushPromises(); expect(w.text()).toContain('重新打开'); w.unmount();
 });
});
