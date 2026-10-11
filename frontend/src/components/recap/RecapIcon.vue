<template>
  <svg class="recap-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
    <path v-for="(d, index) in paths" :key="index" :d="d" />
  </svg>
</template>

<script>
// 回顾页的线性图标（称号、统计卡与小标题）。只用路径，颜色跟随 currentColor，深浅主题自动适配。
const ICONS = {
  night_owl: ['M20 14.5A8 8 0 1 1 9.5 4a6.5 6.5 0 0 0 10.5 10.5z'],
  weekend_fan: ['M12 3l2.6 5.4 5.9.8-4.3 4.1 1 5.8L12 16.4 6.8 19.1l1-5.8L3.5 9.2l5.9-.8z'],
  rewatcher: ['M4 12a8 8 0 0 1 13.7-5.6', 'M18 3v4h-4', 'M20 12a8 8 0 0 1-13.7 5.6', 'M6 21v-4h4'],
  explorer: ['M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z', 'M15.5 8.5l-2 5-5 2 2-5z'],
  streak: ['M12 3c.8 3.4 4.5 5 4.5 9.4a4.5 4.5 0 0 1-9 0c0-1.9.9-3.2 1.8-4.1.1 1.8.9 2.8 1.9 2.8-.8-2.8.1-5.6.8-8.1z'],
  tag_loyal: ['M3.5 12.5V4.5a1 1 0 0 1 1-1h8l8 8-9 9z', 'M8 8h.01'],
  image_lover: ['M4 5h16v14H4z', 'M4 16l4.5-4.5 3.5 3.5 2.5-2.5L20 18', 'M15 9h.01'],
  recorder: ['M6 3.5h10.5a2 2 0 0 1 2 2v13a2 2 0 0 1-2 2H6z', 'M9.5 8h5.5', 'M9.5 12h5.5'],
  play: ['M7 4.5v15l12-7.5z'],
  plays: ['M4 6h16v12H4z', 'M10 9.5v5l4-2.5z'],
  videos: ['M3.5 7h17v11h-17z', 'M7 7l2-3', 'M12 7l2-3', 'M17 7l2-3'],
  images: ['M4 6h16v12H4z', 'M4 15l4-4 3 3 3-3 6 6'],
  days: ['M4 6h16v14H4z', 'M4 10h16', 'M8 3.5v4', 'M16 3.5v4'],
  clock: ['M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17z', 'M12 7.5V12l3 2'],
  spark: ['M12 3v4', 'M12 17v4', 'M3 12h4', 'M17 12h4', 'M6 6l2.5 2.5', 'M15.5 15.5L18 18', 'M18 6l-2.5 2.5', 'M8.5 15.5L6 18'],
  heart: ['M12 20s-7-4.4-7-10a4 4 0 0 1 7-2.6A4 4 0 0 1 19 10c0 5.6-7 10-7 10z'],
  diary: ['M5 4h11l3 3v13H5z', 'M8.5 10h7', 'M8.5 14h5'],
  live: ['M12 9.5a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5z', 'M7 7a7 7 0 0 0 0 10', 'M17 7a7 7 0 0 1 0 10'],
  person: ['M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8z', 'M4.5 20.5a7.5 7.5 0 0 1 15 0'],
  collection: ['M4 7h16v12H4z', 'M7 4h10'],
  arrow: ['M5 12h14', 'M13 6l6 6-6 6']
};

export default {
  name: 'RecapIcon',
  props: { name: { type: String, default: 'spark' } },
  computed: {
    paths() { return ICONS[this.name] || ICONS.spark; }
  }
};
</script>

<style scoped>
.recap-icon { width: 1em; height: 1em; flex: none; }
</style>
