<template>
  <!-- 规则编辑（D-RA-02）：媒体类型 + 一组同时满足的条件 + 一到多个动作。校验以后端为准，错误按字段落到对应的行。 -->
  <DialogShell :title="title" size="lg" data-test="rule-editor" @close="$emit('cancel')">
    <div class="rule-editor">
      <div class="rule-editor__head">
        <label class="rule-editor__field">
          <span>名称</span>
          <input v-model="form.name" class="text-input" maxlength="100" placeholder="例如：下载目录打「待整理」" data-test="rule-name" />
        </label>
        <label class="rule-editor__field">
          <span>媒体类型</span>
          <select v-model="form.media_kind" class="select-input" :disabled="form.id > 0" data-test="rule-kind" @change="onKindChange">
            <option value="video">视频</option>
            <option value="image">图片</option>
          </select>
        </label>
        <label class="checkbox-label rule-editor__enabled">
          <input v-model="form.enabled" type="checkbox" data-test="rule-enabled" />
          启用（新入库的条目自动套用）
        </label>
      </div>
      <p v-if="fieldMessage('name') || fieldMessage('media_kind')" class="rule-editor__error" data-test="rule-error-head">{{ fieldMessage('name') || fieldMessage('media_kind') }}</p>

      <section class="rule-editor__section">
        <h3>条件 <small>全部满足才算；技术信息还没读出来的条目先不处理，读出来后再判断</small></h3>
        <div v-for="(row, index) in form.conditions" :key="row.key" class="rule-editor__row" data-test="rule-condition">
          <select v-model="row.type" class="select-input rule-editor__type" data-test="rule-condition-type">
            <option v-for="item in conditionTypes" :key="item.type" :value="item.type">{{ item.label }}</option>
          </select>
          <template v-if="row.type === 'under_directory'">
            <input v-model="row.path" class="text-input rule-editor__grow" placeholder="选择文件夹（含子文件夹）" data-test="rule-condition-path" />
            <button type="button" class="btn btn--secondary btn--sm" data-test="rule-condition-pick" @click="pickFolder(row)">选择…</button>
          </template>
          <input v-else-if="row.type === 'name_contains'" v-model="row.text" class="text-input rule-editor__grow" placeholder="文件名里包含的文字，不分大小写" data-test="rule-condition-text" />
          <input v-else-if="row.type === 'name_glob'" v-model="row.pattern" class="text-input rule-editor__grow" placeholder="例如 *.2024.*，只匹配文件名" data-test="rule-condition-pattern" />
          <input v-else-if="row.type === 'name_regex'" v-model="row.pattern" class="text-input rule-editor__grow" maxlength="200" placeholder="正则表达式，最多 200 个字符" data-test="rule-condition-pattern" />
          <input v-else-if="row.type === 'extension_in'" v-model="row.list" class="text-input rule-editor__grow" placeholder="扩展名，用逗号分隔，例如 mkv, mp4" data-test="rule-condition-list" />
          <input v-else-if="row.type === 'video_codec_in'" v-model="row.list" class="text-input rule-editor__grow" placeholder="编码，用逗号分隔，例如 hevc, h264" data-test="rule-condition-list" />
          <select v-else-if="row.type === 'has_gps'" v-model="row.value" class="select-input rule-editor__grow" data-test="rule-condition-gps">
            <option :value="true">有地理位置</option>
            <option :value="false">没有地理位置</option>
          </select>
          <span v-else class="rule-editor__range">
            <input v-model="row.min" type="number" min="0" class="number-input" placeholder="下限" data-test="rule-condition-min" />
            <span>至</span>
            <input v-model="row.max" type="number" min="0" class="number-input" placeholder="上限" data-test="rule-condition-max" />
            <span>{{ unitOf(row.type) }}</span>
          </span>
          <button type="button" class="btn btn--secondary btn--sm" :disabled="form.conditions.length === 1" data-test="rule-condition-remove" @click="form.conditions.splice(index, 1)">移除</button>
          <p v-if="rowMessage('conditions', index)" class="rule-editor__error" data-test="rule-condition-error">{{ rowMessage('conditions', index) }}</p>
        </div>
        <button type="button" class="btn btn--secondary btn--sm" data-test="rule-condition-add" @click="form.conditions.push(blankCondition())">添加条件</button>
        <p v-if="fieldMessage('conditions')" class="rule-editor__error">{{ fieldMessage('conditions') }}</p>
      </section>

      <section class="rule-editor__section">
        <h3>动作 <small>只加不撤：你之后手动去掉的不会再加回来</small></h3>
        <div v-for="(row, index) in form.actions" :key="row.key" class="rule-editor__row" data-test="rule-action">
          <select v-model="row.type" class="select-input rule-editor__type" data-test="rule-action-type" @change="row.target_id = ''">
            <option value="add_tag">加标签</option>
            <option v-if="form.media_kind === 'video'" value="add_to_collection">加入作品集</option>
          </select>
          <select v-model="row.target_id" class="select-input rule-editor__grow" data-test="rule-action-target">
            <option value="" disabled>{{ row.type === 'add_to_collection' ? '选择作品集' : '选择标签' }}</option>
            <option v-for="option in targetOptions(row.type)" :key="option.id" :value="option.id">{{ option.label }}</option>
          </select>
          <button type="button" class="btn btn--secondary btn--sm" :disabled="form.actions.length === 1" data-test="rule-action-remove" @click="form.actions.splice(index, 1)">移除</button>
          <p v-if="rowMessage('actions', index)" class="rule-editor__error" data-test="rule-action-error">{{ rowMessage('actions', index) }}</p>
        </div>
        <button type="button" class="btn btn--secondary btn--sm" data-test="rule-action-add" @click="form.actions.push(blankAction())">添加动作</button>
        <p v-if="fieldMessage('actions')" class="rule-editor__error">{{ fieldMessage('actions') }}</p>
      </section>

      <p v-if="generalMessage" class="rule-editor__error" role="alert" data-test="rule-error">{{ generalMessage }}</p>
    </div>
    <template #footer>
      <button type="button" class="btn btn--secondary" data-test="rule-cancel" @click="$emit('cancel')">取消</button>
      <button type="button" class="btn btn--primary" :disabled="saving" data-test="rule-save" @click="$emit('save', toInput())">{{ saving ? '保存中…' : '保存' }}</button>
    </template>
  </DialogShell>
</template>

<script>
import { SelectDirectory } from '../../../wailsjs/go/main/App';
import { blankAction, blankCondition, CONDITION_TYPES, conditionTypesFor, fieldRow, formToInput, parseRuleError, ruleToForm } from '../../utils/automationRules.js';
import { notifyError } from '../../utils/feedback.js';
import DialogShell from '../ui/DialogShell.vue';

export default {
  name: 'AutomationRuleEditor',
  components: { DialogShell },
  props: {
    // 要编辑的规则（列表里的一项）；null 为新建。
    rule: { type: Object, default: null },
    tags: { type: Array, default: () => [] },
    collections: { type: Array, default: () => [] },
    saving: { type: Boolean, default: false },
    // 保存失败时由父组件传回：{ code, field, message }。
    error: { type: Object, default: null }
  },
  emits: ['save', 'cancel'],
  data() {
    return { form: ruleToForm(this.rule) };
  },
  computed: {
    title() {
      return this.form.id ? '编辑规则' : '新建规则';
    },
    conditionTypes() {
      return conditionTypesFor(this.form.media_kind);
    },
    // 没有落到具体字段或行上的错误。
    generalMessage() {
      if (!this.error?.message) return '';
      const field = this.error.field || '';
      if (['name', 'media_kind', 'conditions', 'actions'].includes(field) || fieldRow(field)) return '';
      return this.error.message;
    }
  },
  methods: {
    blankAction,
    blankCondition,
    unitOf(type) {
      return CONDITION_TYPES.find(item => item.type === type)?.unit || '';
    },
    fieldMessage(field) {
      return this.error?.field === field ? this.error.message : '';
    },
    rowMessage(list, index) {
      const row = fieldRow(this.error?.field);
      return row && row.list === list && row.index === index ? this.error.message : '';
    },
    targetOptions(type) {
      if (type === 'add_to_collection') {
        return this.collections.map(item => ({ id: item.id, label: item.name }));
      }
      // 两个自动标签由应用维护，规则不能用。
      return this.tags.filter(tag => !tag.automatic_kind).map(tag => ({ id: tag.id, label: tag.namespace ? `${tag.namespace} / ${tag.name}` : tag.name }));
    },
    // 换媒体类型：去掉不适用的条件与「加入作品集」，至少留一行。
    onKindChange() {
      const allowed = new Set(this.conditionTypes.map(item => item.type));
      this.form.conditions = this.form.conditions.filter(row => allowed.has(row.type));
      if (!this.form.conditions.length) this.form.conditions.push(blankCondition());
      if (this.form.media_kind !== 'video') {
        this.form.actions = this.form.actions.filter(row => row.type !== 'add_to_collection');
        if (!this.form.actions.length) this.form.actions.push(blankAction());
      }
    },
    async pickFolder(row) {
      try {
        const dir = await SelectDirectory();
        if (dir) row.path = dir;
      } catch (err) {
        notifyError(`选择文件夹失败：${parseRuleError(err).message}`);
      }
    },
    toInput() {
      return formToInput(this.form);
    }
  }
};
</script>

<style scoped>
.rule-editor { display: grid; }
.rule-editor__head { display: grid; grid-template-columns: 1fr 140px; gap: var(--space-3) 14px; align-items: end; }
.rule-editor__field { display: grid; gap: 6px; font-size: var(--fs-secondary); }
.rule-editor__field span { color: var(--text-secondary); }
.rule-editor__enabled { grid-column: 1 / -1; font-size: var(--fs-secondary); }
.rule-editor__section { display: grid; gap: var(--space-2); margin-top: var(--space-4); }
.rule-editor__section > .btn { justify-self: start; }
.rule-editor__section h3 { font-size: var(--fs-body); font-weight: var(--fw-semibold); }
.rule-editor__section h3 small { margin-left: var(--space-2); color: var(--text-muted); font-weight: var(--fw-regular); font-size: var(--fs-caption); }
.rule-editor__row { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-2); }
.rule-editor__type { flex: 0 0 130px; }
.rule-editor__grow { flex: 1 1 220px; min-width: 0; }
.rule-editor__range { display: inline-flex; align-items: center; gap: 6px; flex: 1 1 220px; color: var(--text-secondary); font-size: var(--fs-secondary); }
.rule-editor__range .number-input { width: 110px; }
.rule-editor__error { flex-basis: 100%; color: var(--danger-text); font-size: var(--fs-caption); }
</style>
