<template><section id="settings-photo-map" class="settings-section"><h3>照片地图</h3><MapConnectionControls /><p class="help-text">地图组件随应用打包；缓存只服务当前视口，不提供区域预下载，不进入片库备份。最多同时请求两张瓦片，单次最多等待十秒；底图不可用时可继续浏览本地照片。</p></section></template>
<script>
import MapConnectionControls from '../MapConnectionControls.vue';
export default{name:'PhotoMapSection',components:{MapConnectionControls}};
</script>
