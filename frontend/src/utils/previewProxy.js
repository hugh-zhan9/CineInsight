// 预览自动代理的请求方（2026-10-13「直接播 + 自动代理」评审修订）。
// 后端按 owner 记账：同一部视频可以有多个宿主在等，最后一个 owner 撤销了才取消；同时在等的最多 2 部。
// 内嵌播放器自己跟踪（它还要管手动生成），会话读者（字幕工作台、版本对比的每一侧）用这里的跟随器。
import { CancelPreviewProxy, CreatePlaybackProxy, RequestPreviewProxy } from '../../wailsjs/go/main/App';
import { PLAYBACK_PROXY_CODE_LABELS, advanceProxyTracking, playbackProxyItemStateText, proxyRequestErrorText } from './playbackProxy.js';

let ownerSeq = 0;

// newPreviewProxyOwner：每个宿主实例一个，在本次运行内唯一。
export function newPreviewProxyOwner(kind) {
  ownerSeq += 1;
  return `${kind}-${Date.now().toString(36)}-${ownerSeq}`;
}

// trackPreviewProxy 为一部视频排代理并跟进度。manual 为真时走 CreatePlaybackProxy（stop 只停跟踪，手动的不取消），
// 否则 RequestPreviewProxy(videoID, owner)，stop 时撤销这个 owner（等入队请求回来再发，免得撤销先到）。
// onChange({ phase, text })：pending（排队 / 生成中）、ready、unknown（整轮结束却没有这一项的结果，以会话为准）、
// failed（text 为失败文案）、notice（被上限拒绝或任务被取消）。返回 { stop(), active() }。
export function trackPreviewProxy(videoID, { owner, manual = false, onChange }) {
  const id = Number(videoID);
  let tracking = { videoID: id, state: { phase: 'submitting' } };
  let stopped = false;
  let settled = false;
  let off = null;
  const emit = change => { if (!stopped) onChange(change); };
  const unsubscribe = () => { off?.(); off = null; };
  const finish = (change) => { settled = true; tracking = null; unsubscribe(); emit(change); };
  const apply = (status, fromEvent) => {
    if (stopped || settled || !status) return;
    const next = advanceProxyTracking(tracking, status, id, { fromEvent });
    tracking = next.tracking;
    if (!next.finished) {
      if (tracking) emit({ phase: 'pending', text: playbackProxyItemStateText(tracking.state) || '正在加入队列' });
      return;
    }
    const outcome = next.finished.phase === 'done' ? next.finished.outcome : '';
    if (outcome === 'failed') finish({ phase: 'failed', text: `生成播放代理失败：${PLAYBACK_PROXY_CODE_LABELS[next.finished.result.code] || next.finished.result.code}` });
    else if (outcome === 'cancelled') finish({ phase: 'notice', text: '播放代理任务已取消。' });
    else finish({ phase: outcome === 'ready' ? 'ready' : 'unknown', text: '' });
  };
  if (window.runtime?.EventsOn) {
    const handle = window.runtime.EventsOn('playback-proxy-state', status => apply(status, true));
    if (typeof handle === 'function') off = handle;
  }
  emit({ phase: 'pending', text: '正在加入队列' });
  let requestFailed = false;
  const request = Promise.resolve()
    .then(() => (manual ? CreatePlaybackProxy(id) : RequestPreviewProxy(id, owner)))
    .then(status => apply(status || { running: false }, false))
    .catch(err => {
      requestFailed = true;
      const { notice, error } = proxyRequestErrorText(err);
      finish(notice ? { phase: 'notice', text: notice } : { phase: 'failed', text: error });
    });
  return {
    active: () => !stopped && !settled,
    stop() {
      if (stopped) return;
      const wasSettled = settled;
      stopped = true;
      unsubscribe();
      if (manual || wasSettled) return;
      request
        .then(() => (requestFailed ? null : CancelPreviewProxy(id, owner)))
        .catch(err => console.warn('撤销自动播放代理请求失败:', err));
    }
  };
}

// createPreviewProxyFollower：会话读者的跟随器。follow(session) 在会话为 proxy-preparing 时按 owner 自动请求
// （已在跟同一部时不重复）；generate(videoID) 是手动「生成播放代理」（proxy-manual 或自动失败后）；
// 结果为 ready / unknown 时调 onReady(videoID) 让调用方重取会话；stop() 在关闭时撤销。
export function createPreviewProxyFollower({ kind, onState, onReady }) {
  const owner = newPreviewProxyOwner(kind);
  let handle = null;
  let videoID = 0;
  const start = (id, manual) => {
    handle?.stop();
    videoID = id;
    handle = trackPreviewProxy(id, {
      owner,
      manual,
      onChange: change => {
        onState(change);
        if (change.phase === 'ready' || change.phase === 'unknown') onReady(id);
      }
    });
  };
  return {
    owner,
    follow(session) {
      const id = Number(session?.video_id || 0);
      if (session?.mode !== 'proxy-preparing' || !id || (handle?.active() && videoID === id)) return;
      start(id, false);
    },
    generate(id) { if (Number(id)) start(Number(id), true); },
    stop() { handle?.stop(); handle = null; videoID = 0; }
  };
}
