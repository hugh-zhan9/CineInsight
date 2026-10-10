import { flushPromises, mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import ReviewCandidateWindow from './ReviewCandidateWindow.vue';
import VirtualVideoList from './VirtualVideoList.vue';

describe.each(['video', 'image'])('bounded %s review projection', kind => {
  it('keeps all group members available to actions while rendering a bounded candidate window', async () => {
    const items = Array.from({ length: 100000 }, (_, i) => ({ id: i + 1, suggested_name: `候选 ${i + 1}` }));
    const group = { videoId: 7, imageID: 7, videoName: 'same media', name: 'same media', candidates: items, items };
    const host = document.createElement('div');
    host.className = 'review-host';
    document.body.appendChild(host);
    const wrapper = mount(ReviewCandidateWindow, {
      attachTo: host,
      props: { groups: [group], kind, scrollOwnerSelector: '.review-host' },
      slots: { header: '<div>header</div>', candidate: '<div>candidate</div>' }
    });
    try {
      await flushPromises();
      expect(wrapper.vm.rows).toHaveLength(100001);
      expect(wrapper.vm.rows[0].group.items).toHaveLength(100000);
      expect(wrapper.vm.rows.at(-1).id).toBe('candidate:100000');
      expect(wrapper.findAll('[data-virtual-row-id]').length).toBeLessThan(20);
      const list = wrapper.findComponent(VirtualVideoList);
      const index = list.vm.layoutIndex;
      const last = index.window(index.totalHeight - 200, index.totalHeight, 5);
      expect(last.endIndex).toBe(100001);
      expect(last.endIndex - last.startIndex).toBeLessThan(10);
      expect(list.props('scrollOwnerSelector')).toBe('.review-host');
    } finally { wrapper.unmount(); host.remove(); }
  });

  it('preserves header/candidate order and stable IDs when an earlier candidate is removed', async () => {
    const groups = [1, 2].map(id => ({ videoId: id, imageID: id, items: [{ id: id * 10 }, { id: id * 10 + 1 }] }));
    const wrapper = mount(ReviewCandidateWindow, { props: { groups, kind, active: false, scrollOwnerSelector: '.review-host' } });
    try {
      expect(wrapper.vm.rows.map(row => row.id)).toEqual(['group:1', 'candidate:10', 'candidate:11', 'group:2', 'candidate:20', 'candidate:21']);
      await wrapper.setProps({ groups: [{ ...groups[0], items: groups[0].items.slice(1) }, groups[1]] });
      expect(wrapper.vm.rows.map(row => row.id)).toEqual(['group:1', 'candidate:11', 'group:2', 'candidate:20', 'candidate:21']);
    } finally { wrapper.unmount(); }
  });
});
