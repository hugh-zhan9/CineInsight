<template>
  <div class="bt-poster">
    <img v-if="safePath && !failed" :src="safePath" alt="资源封面" loading="lazy" decoding="async" referrerpolicy="no-referrer" @error="failed = true" />
    <span v-else>暂无封面</span>
  </div>
</template>
<script>
export default {
  name: 'BTResourcePoster',
  props: { path: { type: String, default: '' } },
  data: () => ({ failed: false }),
  computed: { safePath() { return /^\/preview\/bt-poster\/ci-bt-[a-f0-9]{48}$/.test(this.path) ? this.path : ''; } },
  watch: { path() { this.failed = false; } }
};
</script>
<style scoped>
.bt-poster { width: 112px; height: 148px; flex: 0 0 112px; display: grid; place-items: center; overflow: hidden; border-radius: var(--radius-sm); background: var(--bg-secondary); color: var(--text-secondary); font-size: var(--fs-caption); }
.bt-poster img { width: 100%; height: 100%; object-fit: contain; }
@media (max-width: 600px) { .bt-poster { width: 80px; height: 108px; flex-basis: 80px; } }
</style>
