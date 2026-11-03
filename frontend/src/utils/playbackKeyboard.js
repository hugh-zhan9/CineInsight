export function isPlaybackKeyboardControl(event) { return Boolean(event.target?.closest?.('input, textarea, select, button, media-volume-range, [contenteditable="true"], [data-test="playback-transcript"]')); }

// Handle shortcuts in the focused player only; subtitle form controls keep their keys.
export function handlePlaybackKey(event, video, range = null) {
  if (!video || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || isPlaybackKeyboardControl(event)) return;
  // The library play button owns Space; its focused arrow keys still control the video.
  if (event.key === ' ' && event.target?.closest?.('media-play-button')) return;
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
    const minimum = range ? range.startMS / 1000 : 0;
    const maximum = range ? range.endMS / 1000 : video.duration;
    if (!Number.isFinite(minimum) || !Number.isFinite(maximum) || minimum < 0 || maximum <= minimum) return;
    video.currentTime = Math.max(minimum, Math.min(maximum, video.currentTime + (event.key === 'ArrowRight' ? 5 : -5)));
  }
}
