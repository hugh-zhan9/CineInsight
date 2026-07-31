<template>
  <div v-if="visible" class="local-metadata-overlay" @click.self="$emit('close')">
    <section class="local-metadata-dialog" role="dialog" aria-label="本地元数据导入">
      <header>
        <div><h2>导入本地资料</h2><p>只读取视频同目录的 NFO、poster 和 fanart，不访问网络。</p></div>
        <button type="button" class="btn-secondary" @click="$emit('close')">关闭</button>
      </header>

      <div v-if="loading" class="local-metadata-empty">正在解析本地资料...</div>
      <div v-else-if="error" class="local-metadata-error" role="alert">{{ error }}</div>
      <template v-else>
        <div v-if="forms.length === 0" class="local-metadata-empty">所选视频没有可预览的本地资料。</div>
        <article v-for="form in forms" :key="form.diff.video_id" class="local-metadata-video">
          <h3>视频 #{{ form.diff.video_id }}</h3>
          <p v-if="form.diff.status === 'missing'" class="local-metadata-empty">未发现同名 NFO 或本地图片。</p>
          <template v-else>
            <label v-for="field in scalarFields(form)" :key="field.name" class="local-metadata-field" :data-test="`metadata-field-${form.diff.video_id}-${field.name}`">
              <input type="checkbox" v-model="form.selected[field.name]" :disabled="!isExecutable(field.diff)" />
              <span><strong>{{ field.label }}</strong><small>当前：{{ field.diff.current_value || '空' }}</small><small>来源：{{ field.diff.source_value || '空' }}</small></span>
              <label v-if="field.diff.requires_overwrite && form.selected[field.name]" class="local-metadata-overwrite"><input type="checkbox" v-model="form.overwrite[field.name]" />确认覆盖</label>
            </label>

            <div v-for="relation in relationFields(form)" :key="relation.name" class="local-metadata-relation">
              <label class="local-metadata-field">
                <input type="checkbox" v-model="form.selected[relation.name]" :disabled="!isExecutable(relation.diff)" />
                <span><strong>{{ relation.label }}</strong><small>当前 {{ relation.diff.current?.length || 0 }} 项，来源 {{ relation.diff.source?.length || 0 }} 项</small></span>
                <label v-if="relation.diff.requires_overwrite && form.selected[relation.name]" class="local-metadata-overwrite"><input type="checkbox" v-model="form.overwrite[relation.name]" />确认覆盖</label>
              </label>
              <div v-if="form.selected[relation.name]" class="local-metadata-mappings">
                <label v-for="candidate in relation.diff.source" :key="candidate.normalized_name">
                  <span>{{ candidate.source_name }}</span>
                  <select v-model="form.resolutions[relation.name][candidate.normalized_name]" :data-test="`metadata-resolution-${form.diff.video_id}-${relation.name}-${candidate.normalized_name}`">
                    <option value="">请选择映射</option>
                    <option v-for="match in candidate.matches" :key="match.id" :value="`existing:${match.id}`">匹配：{{ match.name }}</option>
                    <option value="create_new">新建本地实体</option>
                  </select>
                </label>
              </div>
            </div>

            <label v-for="artwork in artworkFields(form)" :key="artwork.name" class="local-metadata-field">
              <input type="checkbox" v-model="form.selected[artwork.name]" :disabled="!isExecutable(artwork.diff)" />
              <span><strong>{{ artwork.label }}</strong><small>来源：{{ artwork.diff.source_name || '无' }}</small></span>
              <label v-if="artwork.diff.requires_overwrite && form.selected[artwork.name]" class="local-metadata-overwrite"><input type="checkbox" v-model="form.overwrite[artwork.name]" />确认覆盖</label>
            </label>
            <ul v-if="form.diff.warnings?.length" class="local-metadata-warnings"><li v-for="warning in form.diff.warnings" :key="warning">{{ warning }}</li></ul>
          </template>
        </article>

        <div v-if="previewFailures.length" class="local-metadata-error">{{ previewFailures.length }} 个视频解析失败：{{ previewFailures[0].message }}</div>
        <div v-if="result" class="local-metadata-result" role="status">导入完成：成功 {{ result.succeeded }}，失败 {{ result.failed }}。</div>
      </template>

      <footer>
        <button data-test="apply-local-metadata" type="button" class="btn-primary" :disabled="loading || applying || !hasSelectedChanges" @click="apply">{{ applying ? '正在导入...' : '应用所选变更' }}</button>
      </footer>
    </section>
  </div>
</template>

<script>
import { ApplyLocalMetadataBatch, PreviewLocalMetadataBatch } from '../../wailsjs/go/main/App';

export default {
  name: 'LocalMetadataDialog',
  props: { visible: { type: Boolean, default: false }, videoIds: { type: Array, default: () => [] } },
  emits: ['close', 'applied'],
  data() { return { loading: false, applying: false, error: '', forms: [], previewFailures: [], result: null }; },
  computed: {
    hasSelectedChanges() { return this.forms.some(form => Object.values(form.selected).some(Boolean)); }
  },
  watch: {
    visible: { immediate: true, handler(value) { if (value) this.load(); } },
    videoIds: { deep: true, handler() { if (this.visible) this.load(); } }
  },
  methods: {
    async load() {
      this.loading = true; this.error = ''; this.result = null;
      try {
        const preview = await PreviewLocalMetadataBatch([...new Set(this.videoIds.map(Number).filter(Boolean))]);
        this.previewFailures = preview?.failures || [];
        this.forms = (preview?.diffs || []).map(diff => this.createForm(diff));
      } catch (err) { this.error = String(err); this.forms = []; }
      finally { this.loading = false; }
    },
    createForm(diff) {
      const selected = {}, overwrite = {}, resolutions = { people: {}, collection: {} };
      for (const [name, value] of [...this.scalarFields({ diff }), ...this.relationFields({ diff }), ...this.artworkFields({ diff })].map(item => [item.name, item.diff])) {
        selected[name] = Boolean(value?.default_selected); overwrite[name] = false;
      }
      for (const relation of ['people', 'collection']) {
        for (const candidate of diff[relation]?.source || []) {
          resolutions[relation][candidate.normalized_name] = candidate.default_mode === 'existing'
            ? `existing:${candidate.default_entity_id}` : candidate.default_mode || '';
        }
      }
      return { diff, selected, overwrite, resolutions };
    },
    scalarFields(form) { return [
      { name: 'title', label: '显示标题', diff: form.diff.title },
      { name: 'original_title', label: '原始标题', diff: form.diff.original_title },
      { name: 'description', label: '简介', diff: form.diff.description }
    ]; },
    relationFields(form) { return [
      { name: 'people', label: '人物', diff: form.diff.people },
      { name: 'collection', label: '作品集', diff: form.diff.collection }
    ]; },
    artworkFields(form) { return [
      { name: 'poster', label: 'Poster', diff: form.diff.poster },
      { name: 'fanart', label: 'Fanart', diff: form.diff.fanart }
    ]; },
    isExecutable(diff) { return diff?.change_type === 'fill' || diff?.change_type === 'overwrite'; },
    buildResolutions(form, relation) {
      return (form.diff[relation]?.source || []).map(candidate => {
        const value = form.resolutions[relation][candidate.normalized_name];
        if (!value) throw new Error(`${candidate.source_name} 尚未选择映射`);
        if (value.startsWith('existing:')) return { normalized_name: candidate.normalized_name, mode: 'existing', entity_id: Number(value.slice(9)) };
        return { normalized_name: candidate.normalized_name, mode: 'create_new', entity_id: 0 };
      });
    },
    buildRequest(form) {
      const selectedFields = Object.entries(form.selected).filter(([, selected]) => selected).map(([name]) => name);
      const overwriteFields = selectedFields.filter(name => form.overwrite[name]);
      for (const name of selectedFields) {
        const item = [...this.scalarFields(form), ...this.relationFields(form), ...this.artworkFields(form)].find(field => field.name === name);
        if (item?.diff?.requires_overwrite && !form.overwrite[name]) throw new Error(`${item.label} 需要确认覆盖`);
      }
      return {
        video_id: form.diff.video_id, manifest_sha256: form.diff.manifest_sha256, current_sha256: form.diff.current_sha256,
        selected_fields: selectedFields, overwrite_fields: overwriteFields,
        people_resolutions: selectedFields.includes('people') ? this.buildResolutions(form, 'people') : [],
        collection_resolutions: selectedFields.includes('collection') ? this.buildResolutions(form, 'collection') : []
      };
    },
    async apply() {
      this.applying = true; this.error = '';
      try {
        const requests = this.forms.filter(form => Object.values(form.selected).some(Boolean)).map(form => this.buildRequest(form));
		const result = await ApplyLocalMetadataBatch({ requests });
		if (result?.succeeded) this.$emit('applied', result);
        await this.load();
		this.result = result;
      } catch (err) { this.error = String(err); }
      finally { this.applying = false; }
    }
  }
};
</script>

<style scoped>
.local-metadata-overlay { position: fixed; inset: 0; z-index: 1300; display: grid; place-items: center; padding: 24px; background: rgba(15, 23, 42, .58); }
.local-metadata-dialog { width: min(880px, 96vw); max-height: 92vh; overflow: auto; padding: 20px; border-radius: 16px; background: var(--panel-bg); color: var(--text-primary); }
.local-metadata-dialog header,.local-metadata-dialog footer,.local-metadata-field { display: flex; align-items: flex-start; justify-content: space-between; gap: 14px; }
.local-metadata-dialog header { position: sticky; top: -20px; z-index: 2; padding: 16px 0; background: var(--panel-bg); }
.local-metadata-dialog h2,.local-metadata-dialog h3,.local-metadata-dialog p { margin: 0; }.local-metadata-dialog header p { color: var(--text-secondary); }
.local-metadata-video { margin: 14px 0; padding: 14px; border: 1px solid var(--border-color); border-radius: 12px; }
.local-metadata-field { margin-top: 10px; padding: 10px; border-radius: 8px; background: var(--bg-color); }.local-metadata-field > span { flex: 1; min-width: 0; }.local-metadata-field strong,.local-metadata-field small { display: block; }.local-metadata-field small { margin-top: 3px; color: var(--text-secondary); overflow-wrap: anywhere; }
.local-metadata-overwrite { color: var(--danger-color); font-size: 12px; white-space: nowrap; }.local-metadata-mappings { display: grid; gap: 8px; margin: 8px 0 0 30px; }.local-metadata-mappings label { display: grid; grid-template-columns: minmax(120px, 1fr) minmax(180px, 1.5fr); gap: 10px; }.local-metadata-mappings select { min-width: 0; }
.local-metadata-error { margin: 12px 0; color: var(--danger-color); }.local-metadata-empty { padding: 18px; color: var(--text-secondary); text-align: center; }.local-metadata-result { margin: 12px 0; color: #15803d; }.local-metadata-warnings { color: #b45309; font-size: 12px; }.local-metadata-dialog footer { position: sticky; bottom: -20px; justify-content: flex-end; padding: 14px 0; background: var(--panel-bg); }
@media (max-width: 640px) { .local-metadata-overlay { padding: 0; }.local-metadata-dialog { width: 100vw; max-height: 100vh; min-height: 100vh; border-radius: 0; }.local-metadata-field { flex-wrap: wrap; }.local-metadata-mappings label { grid-template-columns: 1fr; } }
</style>
