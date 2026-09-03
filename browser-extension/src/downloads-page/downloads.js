// 下载管理页。
//
// 为什么它不是弹窗里的一块：弹窗一失焦就关，长任务的进度必须有个关不掉的地方看。

import { MSG, TASK_STATE } from '../common/constants.js';
import { formatBytes, formatPercent, formatSpeed } from '../common/format.js';

const listEl = document.getElementById('list');
const emptyEl = document.getElementById('empty');
const summaryEl = document.getElementById('summary');
const messageEl = document.getElementById('message');

// 速度是自己算的：任务里只有累计字节数，两次刷新之间的差值除以时间才是速度。
const speedSamples = new Map(); // taskId -> { bytes, at }

document.getElementById('clear-finished').addEventListener('click', async () => {
  const tasks = await send({ type: MSG.LIST_TASKS });
  const finished = tasks.filter((task) => task.state === TASK_STATE.DONE || task.state === TASK_STATE.CANCELED);
  for (const task of finished) await send({ type: MSG.REMOVE_TASK, taskId: task.id });
  await render();
});

chrome.runtime.onMessage.addListener((message) => {
  if (message && message.type === MSG.TASKS_CHANGED) render().catch(() => {});
  return false;
});

render().catch((error) => showMessage(error.message, true));
setInterval(() => render().catch(() => {}), 1000);

async function send(message) {
  const response = await chrome.runtime.sendMessage(message);
  if (!response) throw new Error('后台没有响应，试着重载扩展');
  if (!response.ok) throw new Error(response.error);
  return response.data;
}

async function render() {
  const tasks = await send({ type: MSG.LIST_TASKS });
  listEl.replaceChildren();
  emptyEl.hidden = tasks.length > 0;

  const running = tasks.filter((task) => task.state === TASK_STATE.RUNNING || task.state === 'writing').length;
  const queued = tasks.filter((task) => task.state === TASK_STATE.QUEUED).length;
  summaryEl.textContent = `进行中 ${running} · 排队 ${queued} · 共 ${tasks.length}`;

  for (const task of tasks) listEl.appendChild(renderTask(task));
}

function renderTask(task) {
  const card = document.createElement('div');
  card.className = 'card';

  const head = document.createElement('div');
  head.className = 'card-head';
  const name = document.createElement('span');
  name.className = 'task-name truncate';
  name.textContent = task.filename;
  name.title = task.url;
  head.appendChild(name);

  const state = document.createElement('span');
  state.className = `state small row-end ${stateClass(task.state)}`;
  state.textContent = stateLabel(task.state);
  head.appendChild(state);
  card.appendChild(head);

  const progress = task.progress || {};
  const bar = document.createElement('div');
  bar.className = 'progress';
  const fill = document.createElement('span');
  fill.style.width = formatPercent(progress.completedItems, progress.totalItems);
  bar.appendChild(fill);
  card.appendChild(bar);

  const meta = document.createElement('div');
  meta.className = 'muted small';
  meta.textContent = describe(task);
  card.appendChild(meta);

  if (task.error) {
    const error = document.createElement('div');
    error.className = 'small';
    error.style.color = 'var(--danger)';
    error.textContent = task.error;
    card.appendChild(error);
  }

  card.appendChild(renderActions(task));
  return card;
}

function describe(task) {
  const progress = task.progress || {};
  const parts = [];
  if (progress.totalItems > 0) {
    parts.push(`${formatPercent(progress.completedItems, progress.totalItems)}（${progress.completedItems}/${progress.totalItems}）`);
  }
  if (progress.bytesWritten) parts.push(formatBytes(progress.bytesWritten));
  if (task.state === TASK_STATE.RUNNING) {
    const speed = sampleSpeed(task.id, progress.bytesWritten || 0);
    if (speed) parts.push(formatSpeed(speed));
  } else {
    speedSamples.delete(task.id);
  }
  if (task.variantLabel) parts.push(task.variantLabel);
  return parts.join(' · ') || '等待开始';
}

function sampleSpeed(taskId, bytes) {
  const now = Date.now();
  const previous = speedSamples.get(taskId);
  speedSamples.set(taskId, { bytes, at: now });
  if (!previous || now === previous.at) return 0;
  const delta = bytes - previous.bytes;
  if (delta <= 0) return 0;
  return (delta * 1000) / (now - previous.at);
}

function renderActions(task) {
  const actions = document.createElement('div');
  actions.className = 'row';
  actions.style.marginTop = '8px';

  if (task.state === TASK_STATE.RUNNING) {
    actions.appendChild(button('暂停', '', () => act({ type: MSG.PAUSE_TASK, taskId: task.id })));
  }
  if (task.state === TASK_STATE.PAUSED || task.state === TASK_STATE.FAILED) {
    const label = task.resume && task.resume.bytesWritten > 0 ? '继续' : '重试';
    actions.appendChild(button(label, 'primary', () => act({ type: MSG.RESUME_TASK, taskId: task.id })));
  }
  if (task.state === TASK_STATE.RUNNING || task.state === TASK_STATE.QUEUED || task.state === TASK_STATE.PAUSED) {
    actions.appendChild(button('取消', 'danger', () => act({ type: MSG.CANCEL_TASK, taskId: task.id })));
  }
  actions.appendChild(button('移除', '', () => act({ type: MSG.REMOVE_TASK, taskId: task.id })));

  const source = document.createElement('a');
  source.className = 'muted small row-end truncate';
  source.style.maxWidth = '50%';
  source.href = task.pageUrl || task.url;
  source.target = '_blank';
  source.rel = 'noreferrer';
  source.textContent = task.pageUrl || task.url;
  actions.appendChild(source);
  return actions;
}

async function act(message) {
  try {
    await send(message);
    await render();
  } catch (error) {
    showMessage(error.message, true);
  }
}

function stateLabel(state) {
  switch (state) {
    case TASK_STATE.QUEUED: return '排队中';
    case TASK_STATE.RUNNING: return '下载中';
    case 'writing': return '保存中';
    case TASK_STATE.PAUSED: return '已暂停';
    case TASK_STATE.DONE: return '已完成';
    case TASK_STATE.FAILED: return '失败';
    case TASK_STATE.CANCELED: return '已取消';
    default: return state;
  }
}

function stateClass(state) {
  if (state === TASK_STATE.DONE) return 'state-done';
  if (state === TASK_STATE.FAILED) return 'state-failed';
  if (state === TASK_STATE.PAUSED || state === TASK_STATE.QUEUED) return 'state-paused';
  return '';
}

function button(label, extraClass, onClick) {
  const el = document.createElement('button');
  el.textContent = label;
  if (extraClass) el.className = extraClass;
  el.addEventListener('click', () => onClick());
  return el;
}

function showMessage(text, isError = false) {
  messageEl.hidden = false;
  messageEl.textContent = text;
  messageEl.classList.toggle('error', Boolean(isError));
}
