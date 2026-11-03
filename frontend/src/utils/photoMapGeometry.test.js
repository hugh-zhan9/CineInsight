import {expect,it} from 'vitest';
import {photoViewportBounds,longitudeNearCenter,appendPhotoMapItems,photoRecapGroups} from './photoMapGeometry.js';
it('preserves a dateline viewport and normalizes adjacent world copies',()=>{
 expect(photoViewportBounds({west:170,east:190,south:-20,north:20})).toEqual({west:170,east:-170,south:-20,north:20});
 expect(photoViewportBounds({west:-190,east:-170,south:-20,north:20})).toEqual({west:170,east:-170,south:-20,north:20});
 expect(photoViewportBounds({west:-400,east:400,south:-95,north:95})).toEqual({west:-180,east:180,south:-90,north:90});
 expect(longitudeNearCenter(-179,179)).toBe(181);
});
it('rejects nonfinite or reversed viewports and never fabricates an EXIF date',()=>{
 expect(()=>photoViewportBounds({west:NaN,east:1,south:0,north:1})).toThrow();
 expect(()=>photoViewportBounds({west:0,east:1,south:2,north:1})).toThrow();
 const image={id:1,created_at:'2026-11-04',taken_date:''};expect(photoRecapGroups([image])[0].label).toBe('未标拍摄日期');
 expect(appendPhotoMapItems([image],[image,{id:2}]).map(row=>row.id)).toEqual([1,2]);
});
