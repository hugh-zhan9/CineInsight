import { mount,flushPromises } from '@vue/test-utils';
import { expect,it,vi } from 'vitest';
const api=vi.hoisted(()=>({GetCollectionEpisodeReport:vi.fn(),SetCollectionEpisodeAssignments:vi.fn(),SetCollectionSeasonExpectation:vi.fn(),PrepareEpisodeResourceSearch:vi.fn(),ListEpisodeAcquisitions:vi.fn(async()=>({status:'ok',data:{items:[],next_cursor:null}}))}));
vi.mock('../../wailsjs/go/main/App',()=>api);
import CollectionEpisodePanel from './CollectionEpisodePanel.vue';
it('uses revision and preserves unknown season instead of S00',async()=>{api.GetCollectionEpisodeReport.mockResolvedValue({total:1,uncertain:1,seasons:[],members:[{video_id:8,name:'E03.mkv',season:null,episode:3,revision:4,excluded:false}],next_cursor:0});api.SetCollectionEpisodeAssignments.mockResolvedValue();const wrapper=mount(CollectionEpisodePanel,{props:{collectionId:7}});await wrapper.find('button').trigger('click');await flushPromises();await wrapper.findAll('button').find(b=>b.text()==='确认归属').trigger('click');await flushPromises();expect(api.SetCollectionEpisodeAssignments).toHaveBeenCalledWith(7,[{video_id:8,season:null,episode:3,excluded:false,expected_revision:4}]);wrapper.unmount();});

it('does not apply a late report or reload the new collection after an old save', async () => {
  let resolveReport;
  api.GetCollectionEpisodeReport.mockImplementationOnce(() => new Promise(resolve => { resolveReport = resolve; }));
  const wrapper = mount(CollectionEpisodePanel, { props: { collectionId: 1 } });
  await wrapper.find('button').trigger('click');
  await wrapper.setProps({ collectionId: 2 });
  resolveReport({ total: 1, uncertain: 0, seasons: [], members: [{ video_id: 1, name: 'old.mkv', revision: 1 }], next_cursor: 0 });
  await flushPromises();
  expect(wrapper.text()).not.toContain('old.mkv');
  api.GetCollectionEpisodeReport.mockResolvedValue({ total: 1, uncertain: 0, seasons: [], members: [{ video_id: 2, name: 'new.mkv', revision: 1 }], next_cursor: 0 });
  await wrapper.find('button').trigger('click');
  await flushPromises();
  let resolveSave;
  api.SetCollectionEpisodeAssignments.mockImplementationOnce(() => new Promise(resolve => { resolveSave = resolve; }));
  await wrapper.findAll('button').find(button => button.text() === '确认归属').trigger('click');
  const calls = api.GetCollectionEpisodeReport.mock.calls.length;
  await wrapper.setProps({ collectionId: 3 });
  resolveSave();
  await flushPromises();
  expect(api.GetCollectionEpisodeReport.mock.calls).toHaveLength(calls);
  expect(wrapper.text()).not.toContain('new.mkv');
  wrapper.unmount();
});

it('prepares only explicitly chosen missing episodes and keeps searches local until navigation', async () => {
  api.GetCollectionEpisodeReport.mockResolvedValue({total:3,uncertain:0,mixed_series:false,seasons:[{season:1,missing_ranges:[{start:3,end:3}],expected_count:4}],members:[],next_cursor:0});
  const source={origin_token:'origin',collection_id:7,season:1,targets:[3],query:'Series S01E03'};
  api.PrepareEpisodeResourceSearch.mockResolvedValue({status:'ok',data:source});
  const w=mount(CollectionEpisodePanel,{props:{collectionId:7},global:{stubs:{teleport:true}}});
  await w.find('button').trigger('click');await flushPromises();await w.get('[data-test="episode-find-missing"]').trigger('click');await flushPromises();
  expect(w.vm.searchChoice).toEqual({season:1,episodes:'3',confirmed:false});
  expect(api.PrepareEpisodeResourceSearch).not.toHaveBeenCalled();
  await w.get('[data-test="episode-prepare-search"]').trigger('click');await flushPromises();
  expect(api.PrepareEpisodeResourceSearch).toHaveBeenCalledWith({collection_id:7,season:1,episodes:[3],series_confirmed:false});
  expect(w.emitted('resource-search')[0]).toEqual([source]);w.unmount();
});
