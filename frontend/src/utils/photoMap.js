import {QueryPhotoMap,ListPhotoMapCluster,ListPhotoRecapImages,SavePhotoRecap,ListPhotoRecaps,DeletePhotoRecap,GetMapTileStatus,SetMapTilesEnabled,ClearMapTileCache} from '../../wailsjs/go/main/App';
import {experienceData} from './experienceResult.js';
export const photoMapAPI={
 query:async request=>experienceData(await QueryPhotoMap(request)),
 cluster:async request=>experienceData(await ListPhotoMapCluster(request)),
 timeline:async request=>experienceData(await ListPhotoRecapImages(request)),
 save:async request=>experienceData(await SavePhotoRecap(request)),
 list:async request=>experienceData(await ListPhotoRecaps(request)),
 remove:async(id,revision)=>experienceData(await DeletePhotoRecap(id,revision)),
 tileStatus:async()=>experienceData(await GetMapTileStatus()),
 tilePolicy:async request=>experienceData(await SetMapTilesEnabled(request)),
 clearTiles:async()=>experienceData(await ClearMapTileCache())
};
