<template>
  <main :class="['entity-library', { 'entity-library--with-drawer': selectedEntity }]">
    <section class="entity-library__toolbar glass-surface">
      <div>
        <h2>{{ isPeople ? '人物' : '作品集' }}</h2>
        <p>{{ isPeople ? '本地维护的演员实体；同名人物会保持独立。' : '手工编排的视频集合；一个视频可以属于多个作品集。' }}</p>
      </div>
      <div class="entity-library__search">
        <input v-model="keyword" :placeholder="isPeople ? '搜索显示或原始姓名' : '搜索名称或简介'" @keyup.enter="reload" />
        <button type="button" class="btn-secondary" @click="reload">搜索</button>
      </div>
    </section>

    <section class="entity-library__create glass-surface">
      <input v-model="createForm.name" :placeholder="isPeople ? '显示姓名' : '作品集名称'" maxlength="200" />
      <input v-model="createForm.secondary" :placeholder="isPeople ? '原始姓名（可空）' : '简介（可空）'" :maxlength="isPeople ? 200 : 4000" />
      <button type="button" class="btn-primary" :disabled="creating" @click="createEntity">{{ creating ? '创建中...' : (isPeople ? '明确新建人物' : '新建作品集') }}</button>
    </section>

    <p v-if="error" class="entity-library__error" role="alert">{{ error }}</p>
    <section class="entity-library__grid">
      <button v-for="item in items" :key="entityID(item)" type="button" class="entity-card glass-surface" @click="openEntity(item)">
        <img v-if="assetURL(item)" :src="assetURL(item)" alt="" />
        <div v-else class="entity-card__placeholder">{{ isPeople ? '人物' : '作品集' }}</div>
        <div>
          <strong>{{ entityName(item) }}</strong>
          <span>{{ entitySecondary(item) }}</span>
          <small>{{ item.active_video_count || 0 }} 部活跃作品</small>
        </div>
      </button>
    </section>
    <div v-if="!loading && !items.length" class="empty-state">暂无{{ isPeople ? '人物' : '作品集' }}。</div>
    <button v-if="hasMore" type="button" class="btn-secondary entity-library__more" :disabled="loading" @click="loadMore">{{ loading ? '加载中...' : '加载更多' }}</button>

    <PreviewDrawer
      v-if="selectedEntity"
      :initial-entity="selectedEntity"
      @close="selectedEntity = null"
      @preview-externally="previewExternally"
      @watch-progress="handleWatchProgress"
      @collection-deleted="handleCollectionDeleted"
    />
  </main>
</template>

<script>
import { CreateCollection, CreatePerson, ListCollections, ListPeople, PreviewExternally, UpdateVideoWatchProgress } from '../../wailsjs/go/main/App';
import PreviewDrawer from './PreviewDrawer.vue';

export default {
  name: 'EntityLibraryPage',
  components: { PreviewDrawer },
  props: { entityType: { type: String, required: true } },
  data() {
    return { items: [], keyword: '', cursorName: '', cursorID: 0, hasMore: true, loading: false, creating: false, error: '', createForm: { name: '', secondary: '' }, selectedEntity: null };
  },
  computed: { isPeople() { return this.entityType === 'person'; } },
  watch: { entityType() { this.selectedEntity = null; this.reload(); } },
  mounted() { this.reload(); },
  methods: {
    async reload() {
      const requestToken = Symbol('entity-query'); this._queryToken = requestToken;
      this.items = []; this.cursorName = ''; this.cursorID = 0; this.hasMore = true;
      await this.loadMore(requestToken, true);
    },
    async loadMore(requestToken = this._queryToken, force = false) {
      if (!requestToken) { requestToken = Symbol('entity-query'); this._queryToken = requestToken; }
      if ((!force && this.loading) || !this.hasMore) return;
      this.loading = true; this.error = '';
      const isPeople = this.isPeople; const keyword = this.keyword; const cursorName = this.cursorName; const cursorID = this.cursorID;
      try {
        const page = isPeople
          ? await ListPeople(keyword, cursorName, cursorID, 50)
          : await ListCollections(keyword, cursorName, cursorID, 50);
        if (this._queryToken !== requestToken) return;
        const next = page || []; this.items.push(...next); this.hasMore = next.length === 50;
        if (next.length) { const last = next[next.length - 1]; this.cursorName = last.cursor_name; this.cursorID = this.entityID(last); }
      } catch (err) { if (this._queryToken === requestToken) this.error = String(err); }
      finally { if (this._queryToken === requestToken) this.loading = false; }
    },
    async createEntity() {
      if (!this.createForm.name.trim()) return; this.creating = true; this.error = '';
      try {
        const entity = this.isPeople
          ? await CreatePerson(this.createForm.name, this.createForm.secondary)
          : await CreateCollection(this.createForm.name, this.createForm.secondary);
        this.createForm = { name: '', secondary: '' }; await this.reload(); this.selectedEntity = { type: this.entityType, id: entity.id };
      } catch (err) { this.error = String(err); }
      finally { this.creating = false; }
    },
    openEntity(item) { this.selectedEntity = { type: this.entityType, id: this.entityID(item) }; },
    entityID(item) { return Number(this.isPeople ? item.person?.id : item.collection?.id); },
    entityName(item) { return this.isPeople ? item.person?.display_name : item.collection?.name; },
    entitySecondary(item) { return this.isPeople ? (item.person?.original_name || '无原始姓名') : (item.collection?.description || '无简介'); },
    assetURL(item) { return this.isPeople ? item.avatar_url : item.cover_url; },
    async previewExternally(video) {
      try { await PreviewExternally(video.id); }
      catch (err) { this.error = `外部预览失败：${err}`; }
    },
    handleWatchProgress(progress) {
      const videoID = Number(progress?.videoID || 0); if (!videoID) return;
      const save = () => UpdateVideoWatchProgress(videoID, Number(progress?.positionSeconds || 0), !!progress?.completed);
      this._watchProgressPromise = (this._watchProgressPromise || Promise.resolve()).then(save).catch(err => { this.error = `保存观看进度失败：${err}`; });
    },
    async handleCollectionDeleted() { this.selectedEntity = null; await this.reload(); }
  }
};
</script>

<style scoped>
.entity-library { padding: 14px 18px 28px; display: flex; flex-direction: column; gap: 14px; }.entity-library--with-drawer { padding-right: min(540px, 48vw); }
.entity-library__toolbar,.entity-library__create { padding: 14px 16px; border-radius: 14px; display: flex; gap: 14px; align-items: center; justify-content: space-between; }.entity-library__toolbar h2 { margin: 0 0 3px; font-size: 18px; }.entity-library__toolbar p { margin: 0; color: var(--text-muted); font-size: 12px; }
.entity-library__search,.entity-library__create { display: flex; gap: 8px; }.entity-library input { min-width: 180px; border: 1px solid var(--border-color); border-radius: 8px; padding: 9px 10px; color: var(--text-primary); background: var(--panel-bg); }.entity-library__create { justify-content: flex-start; }.entity-library__create input:nth-child(2) { flex: 1; }
.entity-library__grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 12px; }.entity-card { display: grid; grid-template-columns: 72px 1fr; gap: 12px; align-items: center; padding: 12px; border-radius: 13px; text-align: left; color: var(--text-primary); }.entity-card img,.entity-card__placeholder { width: 72px; height: 72px; object-fit: cover; border-radius: 11px; }.entity-card__placeholder { display: grid; place-items: center; color: var(--text-muted); background: var(--control-hover-bg); }.entity-card > div:last-child { min-width: 0; display: grid; gap: 5px; }.entity-card span,.entity-card small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text-muted); }.entity-library__more { align-self: center; }.entity-library__error { color: var(--danger-color); }
@media (max-width: 900px) { .entity-library--with-drawer { padding-right: 18px; }.entity-library__toolbar,.entity-library__create { align-items: stretch; flex-direction: column; }.entity-library__search { display: flex; } }
</style>
