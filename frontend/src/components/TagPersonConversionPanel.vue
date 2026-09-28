<template>
  <section class="tag-person-conversion" aria-label="标签转为人物">
    <h2>转为人物</h2>
    <p v-if="loading" role="status">正在读取标签和关联媒体…</p>
    <template v-else-if="preview">
      <p>将姓名标签「{{ preview.tag_name }}」转为人物，关联 {{ preview.video_count }} 部视频、{{ preview.image_count }} 张图片。</p>
      <p class="help-text">成功后删除原标签及其打标关系，改用人物关联。包含回收站中保留的媒体关系；原视频、图片文件不变。</p>
      <fieldset class="tag-person-conversion__choices" :disabled="saving">
        <legend>{{ preview.people.length ? '发现同名人物，请选择' : '目标人物' }}</legend>
        <label v-for="person in preview.people" :key="person.id">
          <input v-model="target" type="radio" name="tag-person-target" :value="String(person.id)" />
          <span>关联已有的「{{ person.display_name }}」<small v-if="person.original_name"> · {{ person.original_name }}</small><small> · 人物 #{{ person.id }}</small></span>
        </label>
        <label><input v-model="target" type="radio" name="tag-person-target" value="new" /><span>新建人物「{{ preview.tag_name }}」</span></label>
      </fieldset>
    </template>
    <p v-if="error" class="tag-person-conversion__error" role="alert">{{ error }}</p>
    <div class="modal-actions">
      <button type="button" class="btn-secondary" :disabled="saving" @click="$emit('back')">返回标签管理</button>
      <button v-if="!loading && !preview" type="button" class="btn-secondary" @click="loadPreview">重试</button>
      <button type="button" class="btn-primary" :disabled="loading || saving || !preview || !target" @click="convert">{{ saving ? '转换中…' : '转换并删除原标签' }}</button>
    </div>
  </section>
</template>

<script>
import { PreviewTagPersonConversion, ConvertTagToPerson } from '../../wailsjs/go/main/App';

export default {
  name: 'TagPersonConversionPanel',
  props: { tagId: { type: Number, required: true } },
  emits: ['back', 'busy', 'converted'],
  data() { return { loading: true, saving: false, preview: null, target: '', error: '', loadSequence: 0 }; },
  mounted() { this.loadPreview(); },
  beforeUnmount() { this.loadSequence++; },
  methods: {
    async loadPreview() {
      const sequence = ++this.loadSequence;
      this.loading = true;
      this.error = '';
      this.preview = null;
      this.target = '';
      try {
        const preview = await PreviewTagPersonConversion(this.tagId);
        if (sequence !== this.loadSequence) return;
        this.preview = preview;
        this.target = preview.people.length ? '' : 'new';
      } catch (err) {
        if (sequence === this.loadSequence) this.error = '读取转换信息失败：' + err;
      } finally {
        if (sequence === this.loadSequence) this.loading = false;
      }
    },
    async convert() {
      if (this.loading || this.saving || !this.preview || !this.target) return;
      this.saving = true;
      this.error = '';
      this.$emit('busy', true);
      try {
        const result = await ConvertTagToPerson({
          tag_id: this.preview.tag_id,
          tag_name: this.preview.tag_name,
          target_person_id: this.target === 'new' ? 0 : Number(this.target),
          create_new: this.target === 'new'
        });
        this.$emit('converted', { ...result, tag_id: this.preview.tag_id });
      } catch (err) {
        this.error = '转换失败，请重试或刷新确认结果：' + err;
      } finally {
        this.saving = false;
        this.$emit('busy', false);
      }
    }
  }
};
</script>

<style scoped>
.tag-person-conversion > p { margin: 12px 0; line-height: 1.6; }
.tag-person-conversion__choices { display: flex; flex-direction: column; gap: 12px; max-height: 40vh; overflow-y: auto; margin-top: 18px; padding: 14px; border: 1px solid var(--border-color); border-radius: var(--radius); }
.tag-person-conversion__choices label { display: flex; align-items: baseline; gap: 8px; overflow-wrap: anywhere; }
.tag-person-conversion__choices small { color: var(--text-muted); }
.tag-person-conversion__error { color: var(--danger-color); }
</style>
