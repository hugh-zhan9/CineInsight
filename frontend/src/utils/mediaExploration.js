import { newRequestID } from './requestID.js';
import { OpenMediaExploration, GetMediaExplorationItems, UpdateMediaExploration } from '../../wailsjs/go/main/App';

function unwrap(result) {
  if (result?.status === 'ok' && result.data) return result.data;
  const error = new Error(result?.error?.message || '探索操作未完成，请重试');
  error.code = result?.error?.code || 'dependency_unavailable';
  throw error;
}

export const mediaExplorationAPI = {
  open: scope => OpenMediaExploration(scope).then(unwrap),
  items: request => GetMediaExplorationItems(request).then(unwrap),
  update: request => UpdateMediaExploration(request).then(unwrap)
};

export const explorationContext = state => ({ database_epoch: state.database_epoch, scope: state.scope, scene_id: state.scene_id });

export function explorationAssetURL(path, state, entryID, source = '') {
  const url = new URL(path, 'http://exploration.local');
  url.searchParams.set('exploration_scope', state.scope);
  url.searchParams.set('exploration_epoch', state.database_epoch);
  url.searchParams.set('exploration_scene', state.scene_id);
  url.searchParams.set('exploration_entry', String(entryID));
  if (source) url.searchParams.set('exploration_source', source);
  return url.pathname + url.search;
}

// One serial mutation owner. Transport uncertainty blocks every later mutation
// until an explicit receipt check; presentation and camera buffers survive it.
export function createExplorationController(api = mediaExplorationAPI, onSnapshot = () => {}) {
  let state = null, tail = Promise.resolve(), uncertain = null, failed = false;
  const presented = new Set();
  let view = null;
  const serial = run => {
    const result = tail.then(run);
    tail = result.catch(() => {});
    return result;
  };
  const adopt = next => { state = next; onSnapshot(next, view); return next; };
  const settle = request => {
    if (request.action === 'present') for (const id of request.entry_ids) presented.delete(id);
    if (request.action === 'save_view' && view === request.view) view = null;
  };
  async function perform(action, payload = {}) {
    if (uncertain || failed) throw new Error('浏览进度未保存，请先核对并重试');
    if (!state) throw new Error('探索尚未准备好');
    const request = { ...explorationContext(state), round_id: state.round_id, expected_revision: state.revision, request_id: `${state.revision}:${newRequestID()}`, action, ...payload };
    try {
      const next = await api.update(request);
      settle(request); return adopt(next);
    } catch (error) {
      if (!error.code) uncertain = request;
      failed = action !== 'sample' || !['source_changed', 'duration_unknown', 'exploration_unavailable', 'invalid_request'].includes(error.code);
      throw error;
    }
  }
  async function flushNow() {
    if (uncertain || failed) throw new Error('浏览进度未保存，请先核对并重试');
    while (presented.size) await perform('present', { entry_ids: [...presented].slice(0, 256) });
    if (view) await perform('save_view', { view });
    return state;
  }
  return {
    get state() { return state; },
    get pending() { return presented.size > 0 || !!view || !!uncertain || failed; },
    get blocked() { return failed || !!uncertain; },
    open(scope) { return serial(async () => { if (state) await flushNow(); const next = await api.open(scope); return adopt(next); }); },
    presented(ids) { if (!state) return; const allowed = new Set(state.entries.filter(item => !item.presented).map(item => item.entry_id)); for (const id of ids) if (allowed.has(id)) presented.add(id); },
    view(value) { view = value; },
    flush() { return serial(flushNow); },
    change(action, payload = {}) { return serial(async () => { await flushNow(); return perform(action, payload); }); },
    recover() { return serial(async () => {
      if (!state) return null;
      const latest = await api.open(state.scope);
      if (uncertain) {
        const request = uncertain;
        if (latest.last_receipt?.request_id === request.request_id) {
          settle(request); uncertain = null; failed = false; adopt(latest);
        } else if (latest.revision === request.expected_revision && latest.scene_id === request.scene_id && latest.database_epoch === request.database_epoch) {
          // User-triggered retry with the original identity, never a new UUID.
          const recovered = await api.update(request);
          settle(request); uncertain = null; failed = false; adopt(recovered);
        } else throw new Error('场景已由其他操作改变，尚未确认的浏览记录仍保留；可刷新场景');
      } else {
        if (latest.scene_id !== state.scene_id || latest.database_epoch !== state.database_epoch) throw new Error('场景已变化，请刷新场景；未保存记录不会写入另一墙');
        failed = false; adopt(latest);
      }
      return flushNow();
    }); },
    reloadDiscardingPending() { return serial(async () => {
      const next = await api.open(state?.scope || 'all');
      presented.clear(); view = null; uncertain = null; failed = false;
      return adopt(next);
    }); }
  };
}

// Shared six-request scheduler for visible thumbnails. Cancelled consumers never
// receive a late image; leaving a scene aborts running fetches as well as queued ones.
export function createExplorationImageLoader(limit = 6) {
  let active = 0;
  const pending = [], running = new Set();
  function pump() {
    while (active < limit && pending.length) {
      pending.sort((a, b) => b.priority - a.priority);
      const job = pending.shift(); if (job.controller.signal.aborted) continue;
      active++; running.add(job);
      Promise.resolve().then(() => fetch(job.url, { signal: job.controller.signal, cache: 'no-store' }))
        .then(response => { if (!response.ok) throw new Error('缩略图暂不可用'); return response.blob(); })
        .then(blob => { if (!job.controller.signal.aborted) job.resolve(blob); })
        .catch(error => job.reject(error))
        .finally(() => { active--; running.delete(job); pump(); });
    }
  }
  return {
    load(url, priority = 0) {
      const controller = new AbortController();
      let job;
      const promise = new Promise((resolve, reject) => { job = { url, priority, controller, resolve, reject }; pending.push(job); pump(); });
      const cancel = () => { controller.abort(); job.reject(new DOMException('已取消', 'AbortError')); };
      return { promise, cancel, setPriority(value) { job.priority = value; } };
    },
    cancelAll() { for (const job of [...pending, ...running]) { job.controller.abort(); job.reject(new DOMException('已取消', 'AbortError')); } pending.length = 0; },
    get active() { return active; }
  };
}
