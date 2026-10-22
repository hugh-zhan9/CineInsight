import { mount, flushPromises } from '@vue/test-utils';
import { expect,it,vi } from 'vitest';
const api=vi.hoisted(()=>({ListImageAnniversaries:vi.fn()}));
vi.mock('../../../wailsjs/go/main/App',()=>api);
vi.mock('./PhotoLightbox.vue',()=>({default:{props:['image'],template:'<div>{{image.name}}</div>'}}));
import PhotoAnniversaries from './PhotoAnniversaries.vue';
it('discards stale filter response and only reads EXIF memories',async()=>{let resolveOld;api.ListImageAnniversaries.mockImplementationOnce(()=>new Promise(resolve=>{resolveOld=resolve;})).mockResolvedValue({items:[{id:2,name:'new.jpg',taken_at:'2020-10-22'}],total:1});const wrapper=mount(PhotoAnniversaries,{props:{personIds:[1]}});await wrapper.setProps({personIds:[2]});await flushPromises();resolveOld({items:[{id:1,name:'old.jpg'}],total:1});await flushPromises();expect(wrapper.text()).toContain('new.jpg');expect(wrapper.text()).not.toContain('old.jpg');expect(api.ListImageAnniversaries.mock.lastCall[0].person_ids).toEqual([2]);wrapper.unmount();});

it('closes the lightbox and invalidates reads when its cached page becomes inactive', async () => {
  api.ListImageAnniversaries.mockResolvedValue({ items: [{ id: 4, name: 'memory.jpg' }], total: 1 });
  const wrapper = mount(PhotoAnniversaries, { props: { personIds: [], pageActive: true } });
  await flushPromises();
  await wrapper.find('button[aria-label="memory.jpg"]').trigger('click');
  expect(wrapper.findComponent({ name: 'PhotoLightbox' }).exists() || wrapper.html().includes('<div>memory.jpg</div>')).toBe(true);
  await wrapper.setProps({ pageActive: false });
  expect(wrapper.html()).not.toContain('<div>memory.jpg</div>');
  let resolveLate;
  api.ListImageAnniversaries.mockImplementationOnce(() => new Promise(resolve => { resolveLate = resolve; }));
  await wrapper.setProps({ pageActive: true });
  await wrapper.setProps({ pageActive: false });
  resolveLate({ items: [{ id: 5, name: 'late.jpg' }], total: 1 });
  await flushPromises();
  expect(wrapper.text()).not.toContain('late.jpg');
  wrapper.unmount();
});
