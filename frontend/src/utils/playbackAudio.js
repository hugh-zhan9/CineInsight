// Shared local preference for application-owned playback, including previews.
const key = 'cineinsight-playback-audio';
const initialized = new WeakSet();
export function readPlaybackAudio() {
  try {
    const value = JSON.parse(localStorage.getItem(key));
    if (value && Number.isFinite(value.volume) && value.volume >= 0 && value.volume <= 1 && typeof value.muted === 'boolean') return value;
  } catch { /* Missing/unavailable preferences retain the original first-use default. */ }
  return { volume: 1, muted: true };
}
export function restorePlaybackAudio(video) {
  if (!video || initialized.has(video)) return;
  const value = readPlaybackAudio();
  video.volume = value.volume; video.muted = value.muted;
  initialized.add(video);
}
export function rememberPlaybackAudio(video) {
  if (!video || !initialized.has(video)) return;
  const { volume, muted } = video;
  if (!Number.isFinite(volume) || volume < 0 || volume > 1 || typeof muted !== 'boolean') return;
  try { localStorage.setItem(key, JSON.stringify({ volume, muted })); } catch { /* Storage failure must not interrupt playback. */ }
}
