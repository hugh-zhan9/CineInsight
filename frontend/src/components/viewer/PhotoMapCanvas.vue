<template>
 <div class="photo-map-canvas"><div ref="map" class="photo-map-canvas__surface" role="region" aria-label="照片地图，可用方向键平移、加减键缩放"></div><div class="photo-map-canvas__attribution"><button type="button" class="btn-plain" @click="attribution">© OpenStreetMap contributors</button></div></div>
</template>
<script>
import * as L from 'leaflet';
import 'leaflet/dist/leaflet.css';
import {BrowserOpenURL} from '../../../wailsjs/runtime/runtime';
import {photoViewportBounds,longitudeNearCenter} from '../../utils/photoMapGeometry.js';
export default{
 name:'PhotoMapCanvas',props:{clusters:{type:Array,default:()=>[]},tileStatus:{type:Object,default:null},pageActive:{type:Boolean,default:true},busy:Boolean},emits:['bounds','select','tile-error','tiles-loaded'],
 watch:{clusters(){this._interacting=false;this.draw();},tileStatus(){this.tiles();},pageActive(active){if(active)this.$nextTick(()=>{this._map?.invalidateSize();this.tiles();this.report();});else{clearTimeout(this._timer);this.removeTiles();}}},
 mounted(){this._map=L.map(this.$refs.map,{zoomControl:false,attributionControl:false,minZoom:0,maxZoom:19,worldCopyJump:true,scrollWheelZoom:true}).setView([0,0],0);L.control.zoom({zoomInTitle:'放大',zoomOutTitle:'缩小'}).addTo(this._map);this._markers=L.layerGroup().addTo(this._map);this._map.on('movestart',()=>{this._interacting=true;});this._map.on('moveend',()=>{this.draw();clearTimeout(this._timer);this._timer=setTimeout(()=>this.report(),200);});this.draw();this.tiles();this.report();},
 beforeUnmount(){clearTimeout(this._timer);this._map?.remove();this._map=null;},
 methods:{
  attribution(){BrowserOpenURL('https://www.openstreetmap.org/copyright');},
  report(){if(!this.pageActive||!this._map)return;const b=this._map.getBounds();this.$emit('bounds',{bounds:photoViewportBounds({west:b.getWest(),east:b.getEast(),south:b.getSouth(),north:b.getNorth()}),zoom:this._map.getZoom()});},
  draw(){if(!this._markers||!this._map)return;this._markers.clearLayers();const center=this._map.getCenter().lng;for(const cluster of this.clusters){const span=document.createElement('span');span.textContent=String(Number(cluster.count));const marker=L.marker([cluster.center_lat,longitudeNearCenter(cluster.center_lon,center)],{icon:L.divIcon({html:span,className:'photo-map-cluster',iconSize:[44,32]}),title:`${cluster.count} 张照片`,alt:`${cluster.count} 张照片`});marker.on('click',()=>{if(this.pageActive&&!this.busy&&!this._interacting)this.$emit('select',cluster);});marker.addTo(this._markers);}},
  refreshTiles(){this.removeTiles();this.tiles();},
  removeTiles(){if(this._tiles){this._tiles.remove();this._tiles=null;this._tileURL='';}},
  tiles(){if(!this._map)return;const state=this.tileStatus;if(!this.pageActive||!state||state.cache_error||(!state.enabled&&!state.cache_bytes)){this.removeTiles();return;}const url=`/preview/map-tile/osm/{z}/{x}/{y}.png?generation=${encodeURIComponent(state.generation)}`;if(url===this._tileURL)return;this.removeTiles();this._tileURL=url;const failures=new Set();const layer=L.tileLayer(url,{minZoom:0,maxZoom:19,keepBuffer:0,updateWhenIdle:true,updateWhenZooming:false,detectRetina:false});this._tiles=layer;layer.on('tileerror',event=>{if(this._tiles===layer&&event.tile?.isConnected){failures.add(event.tile);this.$emit('tile-error');}}).on('tileunload',event=>failures.delete(event.tile)).on('tileload',event=>failures.delete(event.tile)).on('load',()=>{if(this._tiles===layer)this.$emit('tiles-loaded',{failed:[...failures].filter(tile=>tile.isConnected).length});}).addTo(this._map);}
 }
};
</script>
<style scoped>
.photo-map-canvas { position:relative; }
.photo-map-canvas__surface { isolation:isolate;height:420px;max-height:70vh;border:1px solid var(--hairline);border-radius:var(--radius);background:var(--control-bg); }
.photo-map-canvas__attribution { position:absolute;bottom:0;right:0;z-index:2;padding:var(--space-1) var(--space-2);background:var(--panel-bg);font-size:var(--fs-caption); }
.photo-map-canvas :deep(.photo-map-cluster) { display:grid;place-items:center;color:var(--accent-text);background:var(--panel-bg);border:2px solid var(--accent-color);border-radius:var(--radius);font-weight:var(--fw-medium); }
</style>
