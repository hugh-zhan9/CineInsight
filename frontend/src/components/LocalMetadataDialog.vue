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
        <!-- D-PC32 / META-03：同一来源名在一个批次里只决策一次；选「新建」时只建一个人物，后续视频复用。 -->
        <section v-if="peopleDecisions.length" class="local-metadata-batch-people" data-test="metadata-batch-people">
          <h3>人物映射</h3>
          <p>同一个来源名在这一批里只需选一次，勾选了「人物」的视频都按这里的选择关联；选「新建本地人物」只会建一个。</p>
          <label v-for="decision in peopleDecisions" :key="decision.normalized_name" class="local-metadata-batch-people__row">
            <span>{{ decision.source_name }}<small>出现在 {{ decision.video_ids.length }} 个视频</small></span>
            <select v-model="batchResolutions[decision.normalized_name]" :data-test="`metadata-batch-person-${decision.normalized_name}`">
              <option value="">请选择映射</option>
              <option v-for="match in decision.matches" :key="match.id" :value="`existing:${match.id}`">匹配：{{ match.name }}</option>
              <option value="create_new">新建本地人物</option>
            </select>
          </label>
        </section>

        <article v-for="form in forms" :key="form.diff.video_id" class="local-metadata-video">
          <!-- META-09：显示片名而不是「视频 #123」，完整路径放在悬停提示里。 -->
          <h3 :title="form.diff.video_path || ''" :data-test="`metadata-video-title-${form.diff.video_id}`">{{ form.diff.video_name || `视频 #${form.diff.video_id}` }}</h3>
          <p v-if="form.diff.status === 'missing'" class="local-metadata-empty">未发现同名 NFO 或本地图片。</p>
          <template v-else>
            <!-- 外层不能是 label：里面还有一个「确认覆盖」的 label，嵌套时点内层会把两个勾选框一起翻。 -->
            <div v-for="field in scalarFields(form)" :key="field.name" class="local-metadata-field" :data-test="`metadata-field-${form.diff.video_id}-${field.name}`">
              <label class="local-metadata-field__main">
                <input type="checkbox" v-model="form.selected[field.name]" :disabled="!isExecutable(field.diff)" />
                <span><strong>{{ field.label }}</strong><small>当前：{{ field.diff.current_value || '空' }}</small><small>来源：{{ field.diff.source_value || '空' }}</small></span>
              </label>
              <label v-if="field.diff.requires_overwrite && form.selected[field.name]" class="local-metadata-overwrite"><input type="checkbox" v-model="form.overwrite[field.name]" />确认覆盖</label>
            </div>

            <div v-for="relation in relationFields(form)" :key="relation.name" class="local-metadata-relation">
              <div class="local-metadata-field">
                <label class="local-metadata-field__main">
                  <input type="checkbox" v-model="form.selected[relation.name]" :disabled="!isExecutable(relation.diff)" />
                  <span><strong>{{ relation.label }}</strong><small :data-test="`metadata-relation-diff-${form.diff.video_id}-${relation.name}`">{{ relationDiffText(relation.diff) }}</small></span>
                </label>
                <label v-if="relation.diff.requires_overwrite && form.selected[relation.name]" class="local-metadata-overwrite"><input type="checkbox" v-model="form.overwrite[relation.name]" />确认覆盖</label>
              </div>
              <p v-if="relation.name === 'people' && form.selected.people && relation.diff.source?.length" class="local-metadata-mapping-hint">人物映射在上方「人物映射」里统一选择。</p>
              <div v-if="relation.name !== 'people' && form.selected[relation.name]" class="local-metadata-mappings">
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
  data() { return { loading: false, applying: false, error: '', forms: [], previewFailures: [], result: null, peopleDecisions: [], batchResolutions: {} }; },
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
        this.peopleDecisions = preview?.people_decisions || [];
        this.batchResolutions = Object.fromEntries(this.peopleDecisions.map(decision => [
          decision.normalized_name,
          decision.default_mode === 'existing' ? `existing:${decision.default_entity_id}` : decision.default_mode || ''
        ]));
      } catch (err) { this.error = String(err); this.forms = []; this.peopleDecisions = []; this.batchResolutions = {}; }
      finally { this.loading = false; }
    },
    createForm(diff) {
      const selected = {}, overwrite = {}, resolutions = { collection: {} };
      for (const [name, value] of [...this.scalarFields({ diff }), ...this.relationFields({ diff }), ...this.artworkFields({ diff })].map(item => [item.name, item.diff])) {
        selected[name] = Boolean(value?.default_selected); overwrite[name] = false;
      }
      // 人物映射按批次统一决策（batchResolutions），这里只保留逐视频的作品集映射。
      for (const candidate of diff.collection?.source || []) {
        resolutions.collection[candidate.normalized_name] = candidate.default_mode === 'existing'
          ? `existing:${candidate.default_entity_id}` : candidate.default_mode || '';
      }
      return { diff, selected, overwrite, resolutions };
    },
    // META-09：人物、作品集的差异列出具体名字，而不是「当前 N 项」。
    relationDiffText(diff) {
      const join = names => (names || []).filter(Boolean).join('、');
      const parts = [];
      if (diff?.added?.length) parts.push(`新增：${join(diff.added)}`);
      if (diff?.removed?.length) parts.push(`移除：${join(diff.removed)}`);
      if (diff?.kept?.length) parts.push(`保留：${join(diff.kept)}`);
      return parts.length ? parts.join(' · ') : '没有变化';
    },
    toResolution(normalizedName, value) {
      if (value.startsWith('existing:')) return { normalized_name: normalizedName, mode: 'existing', entity_id: Number(value.slice(9)) };
      return { normalized_name: normalizedName, mode: 'create_new', entity_id: 0 };
    },
    // 勾选了人物的视频需要的批次决策；有来源名还没选映射就拦下，不发半截请求。
    buildBatchResolutions(forms) {
      const resolutions = {};
      for (const form of forms) {
        if (!form.selected.people) continue;
        for (const candidate of form.diff.people?.source || []) {
          const value = this.batchResolutions[candidate.normalized_name];
          if (!value) throw new Error(`${candidate.source_name} 尚未选择映射`);
          resolutions[candidate.normalized_name] = this.toResolution(candidate.normalized_name, value);
        }
      }
      return resolutions;
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
        return this.toResolution(candidate.normalized_name, value);
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
        // 人物映射走批次级 batch_resolutions（后端逐请求补进来），请求自己不再各带一份。
        people_resolutions: [],
        collection_resolutions: selectedFields.includes('collection') ? this.buildResolutions(form, 'collection') : []
      };
    },
    async apply() {
      this.applying = true; this.error = '';
      try {
        const selectedForms = this.forms.filter(form => Object.values(form.selected).some(Boolean));
        const requests = selectedForms.map(form => this.buildRequest(form));
        const batchResolutions = this.buildBatchResolutions(selectedForms);
        const payload = Object.keys(batchResolutions).length ? { requests, batch_resolutions: batchResolutions } : { requests };
		const result = await ApplyLocalMetadataBatch(payload);
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
.local-metadata-overlay { position: fixed; inset: 0; z-index: 1300; display: grid; place-items: center; padding: 24px; background: var(--overlay-bg); }
.local-metadata-dialog { width: min(880px, 96vw); max-height: 92vh; overflow: auto; padding: 20px; border-radius: 16px; background: var(--panel-bg); color: var(--text-primary); }
.local-metadata-dialog header,.local-metadata-dialog footer,.local-metadata-field { display: flex; align-items: flex-start; justify-content: space-between; gap: 14px; }
.local-metadata-dialog header { position: sticky; top: -20px; z-index: 2; padding: 16px 0; background: var(--panel-bg); }
.local-metadata-dialog h2,.local-metadata-dialog h3,.local-metadata-dialog p { margin: 0; }.local-metadata-dialog header p { color: var(--text-secondary); }
.local-metadata-video { margin: 14px 0; padding: 14px; border: 1px solid var(--border-color); border-radius: 12px; }
.local-metadata-video h3 { overflow-wrap: anywhere; }
.local-metadata-batch-people { display: grid; gap: 8px; margin: 14px 0; padding: 14px; border: 1px solid var(--accent-border); border-radius: 12px; background: var(--accent-soft); }
.local-metadata-batch-people > p { color: var(--text-secondary); font-size: 12px; }
.local-metadata-batch-people__row { display: grid; grid-template-columns: minmax(120px, 1fr) minmax(180px, 1.5fr); align-items: center; gap: 10px; }
.local-metadata-batch-people__row small { display: block; color: var(--text-secondary); font-size: 11px; }
.local-metadata-batch-people__row select { min-width: 0; }
.local-metadata-mapping-hint { margin: 6px 0 0 30px !important; color: var(--text-secondary); font-size: 12px; }
.local-metadata-field { margin-top: 10px; padding: 10px; border-radius: 8px; background: var(--bg-color); }.local-metadata-field__main { display: flex; flex: 1; min-width: 0; align-items: flex-start; gap: 14px; cursor: pointer; }.local-metadata-field__main > span { flex: 1; min-width: 0; }.local-metadata-field strong,.local-metadata-field small { display: block; }.local-metadata-field small { margin-top: 3px; color: var(--text-secondary); overflow-wrap: anywhere; }
.local-metadata-overwrite { color: var(--danger-color); font-size: 12px; white-space: nowrap; }.local-metadata-mappings { display: grid; gap: 8px; margin: 8px 0 0 30px; }.local-metadata-mappings label { display: grid; grid-template-columns: minmax(120px, 1fr) minmax(180px, 1.5fr); gap: 10px; }.local-metadata-mappings select { min-width: 0; }
.local-metadata-error { margin: 12px 0; color: var(--danger-color); }.local-metadata-empty { padding: 18px; color: var(--text-secondary); text-align: center; }.local-metadata-result { margin: 12px 0; color: var(--success-color); }.local-metadata-warnings { color: var(--warning-strong); font-size: 12px; }.local-metadata-dialog footer { position: sticky; bottom: -20px; justify-content: flex-end; padding: 14px 0; background: var(--panel-bg); }
@media (max-width: 640px) { .local-metadata-overlay { padding: 0; }.local-metadata-dialog { width: 100vw; max-height: 100vh; min-height: 100vh; border-radius: 0; }.local-metadata-field { flex-wrap: wrap; }.local-metadata-mappings label { grid-template-columns: 1fr; } }
</style>
