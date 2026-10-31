import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import BTResourcePoster from './BTResourcePoster.vue';
const path = '/preview/bt-poster/ci-bt-' + 'a'.repeat(48);
describe('resource posters', () => {
 it('loads only bound local paths lazily, preserves the card when a poster fails, resets for a new result', async () => {
  const w = mount(BTResourcePoster, { props: { path } });
  expect(w.get('img').attributes()).toMatchObject({ src: path, loading: 'lazy', referrerpolicy: 'no-referrer' });
  await w.get('img').trigger('error'); expect(w.find('img').exists()).toBe(false); expect(w.text()).toContain('暂无封面');
  await w.setProps({ path: path.replace(/a/g, 'b') }); expect(w.find('img').exists()).toBe(true); w.unmount();
 });
 it.each(['', 'https://images.example/private', '/preview/bt-poster/invalid', path + '?url=http://127.0.0.1'])('never renders an external or malformed source: %s', value => {
  const w = mount(BTResourcePoster, { props: { path: value } }); expect(w.find('img').exists()).toBe(false); w.unmount();
 });
});
