export const MAP_MAX_LATITUDE = 85.0511287798066;
const wrapLongitude = value => ((value + 180) % 360 + 360) % 360 - 180;
// Leaflet may report a viewport in an adjacent world copy. Keep dateline bounds
// as west > east; a viewport wider than one world covers all longitudes once.
export function photoViewportBounds({west,east,south,north}) {
  if (![west,east,south,north].every(Number.isFinite) || north < south || east < west) throw new Error('地图范围无效');
  return {west:east-west>=360?-180:wrapLongitude(west),east:east-west>=360?180:wrapLongitude(east),south:Math.max(-90,south),north:Math.min(90,north)};
}
export function longitudeNearCenter(longitude,center) { return longitude+360*Math.round((center-longitude)/360); }
export function appendPhotoMapItems(previous,incoming) {
  const result=[...previous],seen=new Set(previous.map(image=>image.id));
  for(const image of incoming){if(!seen.has(image.id)){seen.add(image.id);result.push(image);}}
  return result;
}
export function photoRecapGroups(items) {
  const groups=[];
  for(const image of items){const date=image.taken_date||'';let last=groups.at(-1);if(!last||last.date!==date){last={date,label:date||'未标拍摄日期',items:[]};groups.push(last);}last.items.push(image);}
  return groups;
}
