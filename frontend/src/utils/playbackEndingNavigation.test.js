import { describe, expect, it, vi } from 'vitest';
import { selectPlaybackEnding } from './playbackEndingNavigation.js';
describe('ending source handoff',()=>{
 it('rejects changed source and lost visibility during retirement',async()=>{
  for(const failure of ['changed','hidden']){
   const source=vi.fn().mockResolvedValueOnce({video_id:2,source_version:'old'});
   if(failure==='changed')source.mockResolvedValueOnce({video_id:2,source_version:'new'});else source.mockRejectedValueOnce(new Error('not visible'));
   const navigate=vi.fn(),retire=vi.fn();
   await expect(selectPlaybackEnding(2,{resolveSource:source,retire,isCurrent:()=>true,navigate})).rejects.toThrow();
   expect(retire).toHaveBeenCalledOnce();expect(navigate).not.toHaveBeenCalled();
  }
 });
});
