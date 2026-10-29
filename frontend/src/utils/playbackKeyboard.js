// Handle shortcuts in the focused player only; subtitle form controls keep their keys.
export function handlePlaybackKey(event, video) {
  if (!video || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || event.target?.closest?.('input, textarea, select, button, [contenteditable="true"]')) return;
  if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', ' '].includes(event.key)) return;
  event.preventDefault();
  event.stopPropagation();
  if (event.key === ' ') {
    if (event.repeat) return;
    if (video.paused) Promise.resolve(video.play()).catch(() => {});
    else video.pause();
  } else if (event.key === 'ArrowUp' || event.key === 'ArrowDown') {
    video.volume = Math.max(0, Math.min(1, video.volume + (event.key === 'ArrowUp' ? 0.05 : -0.05)));
    if (event.key === 'ArrowUp') video.muted = false;
  } else if (Number.isFinite(video.duration) && video.duration > 0) {
    video.currentTime = Math.max(0, Math.min(video.duration, video.currentTime + (event.key === 'ArrowRight' ? 5 : -5)));
  }
}
