// App routes subtitle tools to the persistent library owner, like the existing
// queue-open request. The player never owns an editing session or its draft.
export const SUBTITLE_TOOL_REQUEST = 'cineinsight:subtitle-tool';
export function requestSubtitleTool(videoID, action, isCurrent) {
  return new Promise((resolve, reject) => {
    const detail = { videoID, action, isCurrent, resolve, reject, handled: false };
    window.dispatchEvent(new CustomEvent(SUBTITLE_TOOL_REQUEST, { detail }));
    if (!detail.handled) reject(new Error('字幕工具宿主尚未就绪'));
  });
}
