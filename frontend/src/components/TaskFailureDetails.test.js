import { mount, flushPromises } from '@vue/test-utils';
import { beforeEach, afterEach, expect, it, vi } from 'vitest';
const api=vi.hoisted(()=>({ListTaskFailureRuns:vi.fn(),ListTaskFailures:vi.fn(),PreviewTaskFailureRetry:vi.fn(),ConfirmTaskFailureRetry:vi.fn(),GetTaskFailureRetryResults:vi.fn(),CancelTaskFailureRetry:vi.fn()}));
vi.mock('../../wailsjs/go/main/App',()=>api);
import TaskFailureDetails from './TaskFailureDetails.vue';
let wrapper;
beforeEach(()=>{vi.clearAllMocks();api.ListTaskFailureRuns.mockResolvedValue({runs:[{id:1,state:'completed',started_at:'2026-01-01T00:00:00Z'}],warning:''});api.ListTaskFailures.mockImplementation(({cursor_id})=>Promise.resolve({items:Array.from({length:cursor_id?23:50},(_,i)=>({id:i+1+(cursor_id?50:0),media_id:i+1+(cursor_id?50:0),error_summary:'失败',failed_at:'2026-01-01T00:00:00Z'})),total:73,next_cursor:cursor_id?0:50,records_complete:true}));api.PreviewTaskFailureRetry.mockResolvedValue({token:'frozen',eligible_count:1,skipped_count:0});api.ConfirmTaskFailureRetry.mockResolvedValue('operation');api.GetTaskFailureRetryResults.mockResolvedValue({state:'completed',accepted:1,skipped:0,failed:0,total:1,items:[],next_offset:0});});
afterEach(()=>wrapper?.unmount());
const button=(text)=>wrapper.findAll('button').find(b=>b.text()===text);
it('reaches the 51st failure and freezes only selected IDs',async()=>{wrapper=mount(TaskFailureDetails,{props:{taskKey:'technical'}});await button('查看完整失败记录').trigger('click');await flushPromises();expect(wrapper.findAll('input[type=checkbox]')).toHaveLength(50);await button('加载更多失败').trigger('click');await flushPromises();expect(wrapper.findAll('input[type=checkbox]')).toHaveLength(73);await wrapper.findAll('input[type=checkbox]')[50].setValue(true);await button('重试所选（1）').trigger('click');await flushPromises();expect(api.PreviewTaskFailureRetry).toHaveBeenCalledWith({run_id:1,item_ids:[51]});await button('确认重试').trigger('click');await flushPromises();expect(api.ConfirmTaskFailureRetry).toHaveBeenCalledTimes(1);expect(wrapper.text()).toContain('不代表处理成功');});
it('omits item_ids for all and blocks incomplete runs',async()=>{wrapper=mount(TaskFailureDetails,{props:{taskKey:'proxy'}});await button('查看完整失败记录').trigger('click');await flushPromises();await button('重试本次全部失败').trigger('click');await flushPromises();expect(api.PreviewTaskFailureRetry).toHaveBeenCalledWith({run_id:1});wrapper.unmount();api.ListTaskFailures.mockResolvedValue({items:[{id:1,media_id:1}],total:73,next_cursor:0,records_complete:false});wrapper=mount(TaskFailureDetails,{props:{taskKey:'proxy'}});await button('查看完整失败记录').trigger('click');await flushPromises();expect(button('重试本次全部失败').attributes('disabled')).toBeDefined();});

it('discards a late page after changing diagnostic run', async () => {
  let resolveOld;
  api.ListTaskFailureRuns.mockResolvedValue({ runs: [{ id: 1 }, { id: 2 }], warning: '' });
  api.ListTaskFailures.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve; }))
    .mockResolvedValue({ items: [{ id: 2, media_id: 2, error_summary: 'new failure' }], total: 1, next_cursor: 0, records_complete: true });
  wrapper = mount(TaskFailureDetails, { props: { taskKey: 'technical' } });
  await button('查看完整失败记录').trigger('click');
  await flushPromises();
  await wrapper.find('select').setValue(2);
  await flushPromises();
  resolveOld({ items: [{ id: 1, media_id: 1, error_summary: 'old failure' }], total: 1, next_cursor: 0, records_complete: true });
  await flushPromises();
  expect(wrapper.text()).toContain('new failure');
  expect(wrapper.text()).not.toContain('old failure');
});

it('keeps expanded result pages while polling progress', async () => {
  vi.useFakeTimers();
  try {
    api.GetTaskFailureRetryResults.mockImplementation((_id, offset) => Promise.resolve({
      state: 'running', total: 73, accepted: 73, skipped: 0, failed: 0,
      items: [{ item_id: offset + 1, media_id: offset + 1, status: 'accepted', reason: 'queued' }],
      next_offset: offset ? 0 : 50
    }));
    wrapper = mount(TaskFailureDetails, { props: { taskKey: 'technical' } });
    await button('查看完整失败记录').trigger('click');
    await flushPromises();
    await button('重试本次全部失败').trigger('click');
    await flushPromises();
    await button('确认重试').trigger('click');
    await flushPromises();
    await button('更多提交结果').trigger('click');
    await flushPromises();
    await vi.advanceTimersByTimeAsync(500);
    expect(wrapper.text()).toContain('视频 #51 · 已接受');
    expect(wrapper.text()).toContain('视频 #1 · 已接受');
  } finally { vi.useRealTimers(); }
});
