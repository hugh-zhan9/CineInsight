<template>
  <section class="failure-details" aria-label="完整失败记录">
    <button type="button" class="btn-link" :disabled="busy" @click="open">查看完整失败记录</button>
    <template v-if="opened">
      <AppBanner v-if="error" tone="danger" title="失败记录操作未完成" :message="error" />
      <AppBanner v-if="warning || !complete" tone="warning" :title="!complete ? '失败记录不完整' : '任务提示'" :message="warning || '部分诊断记录未能保存，不能把当前列表当作全部失败。'" />
      <label>诊断时间段 <select v-model.number="runId" class="select-input" @change="load(true)"><option v-for="run in runs" :key="run.id" :value="run.id">{{ time(run.started_at) }} — {{ run.finished_at ? time(run.finished_at) : '进行中' }} · {{ runLabel(run.state) }}</option></select></label>
      <p v-if="!runs.length">尚无完整历史记录。升级前的失败不会补写成历史批次。</p>
      <template v-else>
        <p>共 {{ total }} 条失败记录。AI 时间段表示连续后台运行期间，并不代表全库处理完成。</p>
        <ul class="failure-details__list"><li v-for="item in items" :key="item.id"><label><input v-model="selected" type="checkbox" :value="item.id" />视频 #{{ item.media_id }} · {{ item.error_summary }} · {{ time(item.failed_at) }}</label></li></ul>
        <button v-if="cursor" type="button" class="btn btn--secondary btn--sm" :disabled="busy" @click="load(false)">加载更多失败</button>
        <button type="button" class="btn btn--secondary btn--sm" :disabled="busy || !complete || !selected.length" @click="preview(false)">重试所选（{{ selected.length }}）</button>
        <button type="button" class="btn btn--secondary btn--sm" :disabled="busy || !complete || !total" @click="preview(true)">重试本次全部失败</button>
      </template>
      <div v-if="confirmation" role="status">
        <p>已冻结本次集合：可提交 {{ confirmation.eligible_count }} 项，跳过 {{ confirmation.skipped_count }} 项。之后新增的失败不会混入。</p>
        <button type="button" class="btn btn--primary" :disabled="busy" @click="confirm">确认重试</button>
        <button type="button" class="btn btn--secondary" :disabled="busy" @click="confirmation = null">取消</button>
      </div>
      <div v-if="result" role="status">
        <p>重试提交：已接受 {{ result.accepted }}，跳过 {{ result.skipped }}，提交失败 {{ result.failed }} / {{ result.total }}。已接受表示进入原任务，不代表处理成功。</p>
        <button v-if="result.state === 'running'" type="button" class="btn btn--secondary btn--sm" @click="cancel">停止继续提交</button>
        <ul><li v-for="item in outcomes" :key="item.item_id">视频 #{{ item.media_id }} · {{ resultLabel(item.status) }} · {{ reasonLabel(item.reason) }}</li></ul>
        <button v-if="resultNextOffset" type="button" class="btn btn--secondary btn--sm" @click="poll(resultNextOffset)">更多提交结果</button>
      </div>
    </template>
  </section>
</template>
<script setup>
import { ref, onBeforeUnmount, watch } from 'vue';
import {
  ListTaskFailureRuns, ListTaskFailures, PreviewTaskFailureRetry,
  ConfirmTaskFailureRetry, GetTaskFailureRetryResults, CancelTaskFailureRetry
} from '../../wailsjs/go/main/App';
import AppBanner from './ui/AppBanner.vue';
import { functionalPolishError } from '../utils/functionalPolishErrors.js';

const props = defineProps({ taskKey: { type: String, required: true } });
const opened = ref(false), busy = ref(false), error = ref(''), warning = ref('');
const complete = ref(true), runs = ref([]), runId = ref(0), items = ref([]);
const selected = ref([]), cursor = ref(0), total = ref(0), confirmation = ref(null);
const result = ref(null), outcomes = ref([]), resultNextOffset = ref(0);
let generation = 0, operation = '', timer, lastResultOffset = 0;

function time(value) { return new Date(value).toLocaleString(); }
function runLabel(value) {
  return ({ running: '进行中', completed: '已结束', interrupted: '上次中断', cancelled: '已取消' })[value] || '状态未知';
}
function resultLabel(value) {
  return ({ accepted: '已接受', skipped: '已跳过', failed: '提交失败' })[value] || '状态未知';
}
function reasonLabel(value) {
  return ({
    queued: '已交给原任务', cancelled: '已停止提交', failure_changed: '失败状态已变化',
    source_changed: '源文件已变化，请重新分析', offline: '文件离线，请恢复连接',
    already_running: '任务已在进行中', already_queued: '已经排队',
    succeeded_or_queued: '已成功或排队', deleted_or_out_of_scope: '已删除或不在收录范围',
    failure_no_longer_current: '原失败已失效', enqueue_failed: '入队失败，可重新预览重试', stopping: '任务正在停止'
  })[value] || '状态已变化，请刷新查看';
}
function clearOperation() {
  clearTimeout(timer);
  result.value = null;
  outcomes.value = [];
  resultNextOffset.value = 0;
  lastResultOffset = 0;
  operation = '';
}
function clear() {
  generation++;
  clearOperation();
  opened.value = false;
  confirmation.value = null;
}
watch(() => props.taskKey, clear);
onBeforeUnmount(clear);

async function open() {
  const ticket = ++generation;
  opened.value = true;
  busy.value = true;
  error.value = '';
  try {
    const data = await ListTaskFailureRuns(props.taskKey);
    if (ticket !== generation) return;
    runs.value = data.runs;
    warning.value = data.warning;
    runId.value = data.runs[0]?.id || 0;
    if (runId.value) await load(true);
  } catch (err) {
    if (ticket === generation) error.value = functionalPolishError(err);
  } finally {
    if (ticket === generation) busy.value = false;
  }
}
async function load(reset) {
  const ticket = reset ? ++generation : generation;
  busy.value = true;
  error.value = '';
  if (reset) {
    items.value = [];
    selected.value = [];
    cursor.value = 0;
    confirmation.value = null;
    clearOperation();
  }
  try {
    const data = await ListTaskFailures({ task_key: props.taskKey, run_id: runId.value, cursor_id: reset ? 0 : cursor.value, limit: 50 });
    if (ticket !== generation) return;
    items.value = reset ? data.items : [...items.value, ...data.items];
    cursor.value = data.next_cursor;
    total.value = data.total;
    complete.value = data.records_complete;
  } catch (err) {
    if (ticket === generation) error.value = functionalPolishError(err);
  } finally {
    if (ticket === generation) busy.value = false;
  }
}
async function preview(all) {
  const ticket = generation;
  busy.value = true;
  error.value = '';
  try {
    const request = { run_id: runId.value };
    if (!all) request.item_ids = [...selected.value];
    const data = await PreviewTaskFailureRetry(request);
    if (ticket === generation) confirmation.value = data;
  } catch (err) {
    if (ticket === generation) error.value = functionalPolishError(err);
  } finally {
    if (ticket === generation) busy.value = false;
  }
}
async function confirm() {
  if (busy.value || !confirmation.value) return;
  const ticket = generation;
  const token = confirmation.value.token;
  busy.value = true;
  error.value = '';
  try {
    const id = await ConfirmTaskFailureRetry(token);
    if (ticket !== generation) return;
    clearOperation();
    operation = id;
    confirmation.value = null;
    await poll(0);
  } catch (err) {
    if (ticket === generation) error.value = functionalPolishError(err);
  } finally {
    if (ticket === generation) busy.value = false;
  }
}
async function poll(offset = 0) {
  const ticket = generation, id = operation;
  if (!id) return;
  clearTimeout(timer);
  try {
    const data = await GetTaskFailureRetryResults(id, offset, 50);
    if (ticket !== generation || id !== operation) return;
    result.value = data;
    const merged = new Map(outcomes.value.map(item => [item.item_id, item]));
    for (const item of data.items) merged.set(item.item_id, item);
    outcomes.value = [...merged.values()].sort((a, b) => a.item_id - b.item_id);
    if (offset >= lastResultOffset) {
      lastResultOffset = offset;
      resultNextOffset.value = data.next_offset;
    }
    if (data.state === 'running') timer = setTimeout(() => poll(lastResultOffset), 500);
  } catch (err) {
    if (ticket === generation) error.value = functionalPolishError(err);
  }
}
async function cancel() {
  const ticket = generation;
  try {
    await CancelTaskFailureRetry(operation);
    if (ticket === generation) await poll(lastResultOffset);
  } catch (err) {
    if (ticket === generation) error.value = functionalPolishError(err);
  }
}
</script>

<style scoped>
.failure-details { margin-block: var(--space-3); }
.failure-details__list { max-height: 300px; overflow: auto; padding-inline-start: var(--space-4); }
.failure-details button { margin: var(--space-1); }
</style>
