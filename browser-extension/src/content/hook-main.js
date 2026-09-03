// 页面主世界的挂钩：认出 MSE 播放，并在用户开启后录下喂给播放器的数据。
//
// 为什么需要它：有些站点把媒体喂给 MediaSource，video.src 只是一个 blob: 地址，
// 网络层看到的是一堆没有上下文的分片甚至什么都看不到。这时候唯一还能拿到完整
// 媒体的地方，就是页面把数据交给播放器的那一刻。
//
// 三条硬规矩：
//   1. 挂钩必须透明——原函数原样调用、返回值原样返回、this 与参数一个不改；
//   2. 我们自己的异常绝不能冒到页面上，页面播放不能因为装了这个扩展而坏掉；
//   3. 录制默认关。它要在页面内存里攒下整段媒体，这个代价得用户自己点头。

(() => {
  const CHANNEL = 'cineinsight-grabber';
  // 页面内存里最多攒这么多。到顶就停录并如实上报，不偷偷丢数据。
  const MAX_CAPTURE_BYTES = 2 * 1024 * 1024 * 1024;

  const state = {
    capturing: false,
    chunks: [],
    bytes: 0,
    mimeType: '',
    truncated: false
  };

  const send = (payload) => {
    try {
      window.postMessage({ channel: CHANNEL, direction: 'to-extension', ...payload }, '*');
    } catch {
      // 页面把 postMessage 改坏了也不能连累它自己的播放。
    }
  };

  const safely = (fn) => {
    try {
      fn();
    } catch {
      // 见规矩 2。
    }
  };

  // ---- MediaSource：认出"这个页面在用 MSE 播" ----
  const nativeMediaSource = window.MediaSource;
  if (nativeMediaSource && nativeMediaSource.prototype) {
    const nativeAddSourceBuffer = nativeMediaSource.prototype.addSourceBuffer;
    nativeMediaSource.prototype.addSourceBuffer = function addSourceBuffer(mimeType) {
      const sourceBuffer = nativeAddSourceBuffer.call(this, mimeType);
      safely(() => {
        state.mimeType = state.mimeType || String(mimeType || '');
        send({ type: 'mse-detected', mimeType: String(mimeType || '') });
      });
      return sourceBuffer;
    };
  }

  const nativeSourceBuffer = window.SourceBuffer;
  if (nativeSourceBuffer && nativeSourceBuffer.prototype) {
    const nativeAppend = nativeSourceBuffer.prototype.appendBuffer;
    nativeSourceBuffer.prototype.appendBuffer = function appendBuffer(data) {
      safely(() => {
        if (!state.capturing || state.truncated || !data) return;
        const bytes = data instanceof ArrayBuffer ? new Uint8Array(data.slice(0)) : copyView(data);
        if (!bytes) return;
        if (state.bytes + bytes.length > MAX_CAPTURE_BYTES) {
          state.truncated = true;
          send({ type: 'mse-capture-status', bytes: state.bytes, truncated: true });
          return;
        }
        state.chunks.push(bytes);
        state.bytes += bytes.length;
        // 每攒够 16 MiB 报一次，别把消息通道刷爆。
        if (state.chunks.length % 64 === 0) {
          send({ type: 'mse-capture-status', bytes: state.bytes, truncated: false });
        }
      });
      return nativeAppend.call(this, data);
    };
  }

  // ---- blob: 地址还原成真实来源 ----
  const nativeCreateObjectURL = URL.createObjectURL;
  URL.createObjectURL = function createObjectURL(source) {
    const url = nativeCreateObjectURL.call(this, source);
    safely(() => {
      const isMediaSource = nativeMediaSource && source instanceof nativeMediaSource;
      send({
        type: 'blob-source',
        url,
        sourceKind: isMediaSource ? 'mediasource' : (source && source.type) || 'blob',
        size: !isMediaSource && source && typeof source.size === 'number' ? source.size : 0
      });
    });
    return url;
  };

  // ---- 来自扩展的指令 ----
  window.addEventListener('message', (event) => {
    const data = event.data;
    if (!data || data.channel !== CHANNEL || data.direction !== 'to-page') return;
    safely(() => {
      if (data.type === 'set-capture') {
        state.capturing = Boolean(data.enabled);
        if (!state.capturing) resetCapture();
        send({ type: 'mse-capture-status', bytes: state.bytes, truncated: state.truncated, capturing: state.capturing });
        return;
      }
      if (data.type === 'export-capture') exportCapture(data.filename);
    });
  });

  // 导出走页面自己的下载：数据本来就在页面里，搬到扩展再存一遍，
  // 几个 GB 的东西要在两个进程之间复制一次，得不偿失也更容易崩。
  function exportCapture(filename) {
    if (state.chunks.length === 0) {
      send({ type: 'mse-export-failed', message: '还没录到任何数据：开启捕获后需要把视频从头重新播一遍' });
      return;
    }
    const blob = new Blob(state.chunks, { type: state.mimeType || 'video/mp4' });
    const url = nativeCreateObjectURL.call(URL, blob);
    const anchor = document.createElement('a');
    anchor.href = url;
    anchor.download = filename || 'capture.mp4';
    anchor.style.display = 'none';
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    send({ type: 'mse-export-done', bytes: blob.size, truncated: state.truncated });
    setTimeout(() => URL.revokeObjectURL(url), 60_000);
  }

  function resetCapture() {
    state.chunks = [];
    state.bytes = 0;
    state.truncated = false;
  }

  function copyView(view) {
    if (!ArrayBuffer.isView(view)) return null;
    return new Uint8Array(view.buffer.slice(view.byteOffset, view.byteOffset + view.byteLength));
  }
})();
