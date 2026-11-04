import { GetMediaProcessing, ReleaseMediaProcessing, StartMediaProcessing } from '../../wailsjs/go/main/App';

export function mediaProcessingError(error) { return String(error?.message || error || '').replace(/^Error:\s*/, '').replace(/^(?:processing|compression|storage_plan)_[a-z_]+:\s*/i, ''); }

// 只保留本次窗口的请求；旧Start回包同样必须释放，不能遗留后台工作。
export function createMediaProcessingController(onUpdate) {
  const ids = new Set(), starts = new Set(); let generation = 0, closed = false, timer = null, closing = null;
  const release = async id => { if (!id) return; await ReleaseMediaProcessing(id); ids.delete(id); };
  return {
    async start(request) {
      if (closed) return null;
      const ticket = ++generation; clearTimeout(timer);
      const pending = Promise.resolve().then(() => StartMediaProcessing(request)).then(view => { ids.add(view.id); return view; });
      starts.add(pending);
      let view;
      try { view = await pending; } finally { starts.delete(pending); }
      // close 等待所有 Start 注册完，再统一释放；不能让窗口先关闭而请求仍在飞行中。
      if (closed) return null;
      if (ticket !== generation) { await release(view.id); return null; }
      onUpdate(view);
      const poll = async () => {
        if (closed || ticket !== generation) return;
        try {
          const next = await GetMediaProcessing(view.id);
          if (closed || ticket !== generation) return;
          onUpdate(next);
          if (next.state === 'queued' || next.state === 'running') timer = setTimeout(poll, 250);
        } catch (error) { if (!closed && ticket === generation) onUpdate({ ...view, state: 'failed', error: String(error) }); }
      };
      if (view.state === 'queued' || view.state === 'running') timer = setTimeout(poll, 250);
      return view;
    },
    release,
    invalidate() { generation++; clearTimeout(timer); },
    async close() {
      if (closing) return closing;
      closed = true; generation++; clearTimeout(timer);
      closing = (async () => {
        await Promise.allSettled([...starts]);
        const results = await Promise.allSettled([...ids].map(release));
        const failed = results.find(result => result.status === 'rejected'); if (failed) throw failed.reason;
      })();
      try { await closing; } finally { closing = null; }
    }
  };
}
