// WebVTT cue text is escaped, never interpreted as HTML or VTT markup.
export const SUBTITLE_PREFERENCES_KEY = 'cineinsight-subtitle-display';
const PREFERENCE_VALUES = {
  size: ['small', 'medium', 'large', 'xlarge'],
  background: ['none', 'translucent', 'solid'],
  position: ['bottom', 'middle', 'top']
};
export function normalizeSubtitlePreferences(value) {
  const defaults = { size: 'medium', background: 'translucent', position: 'bottom' };
  return Object.fromEntries(Object.entries(defaults).map(([key, fallback]) => [key, PREFERENCE_VALUES[key].includes(value?.[key]) ? value[key] : fallback]));
}
export function readSubtitlePreferences() {
  try { return normalizeSubtitlePreferences(JSON.parse(window.localStorage.getItem(SUBTITLE_PREFERENCES_KEY))); }
  catch { return normalizeSubtitlePreferences(null); }
}
export function writeSubtitlePreferences(value) {
  try {
    const normalized = normalizeSubtitlePreferences(value);
    window.localStorage.setItem(SUBTITLE_PREFERENCES_KEY, JSON.stringify(normalized));
    window.dispatchEvent(new CustomEvent('subtitle-display-changed', { detail: normalized }));
    return true;
  }
  catch { return false; }
}
export function subtitleOffsetMS(seconds) {
  const value = Number(seconds);
  return Math.round(Math.max(-60, Math.min(60, Number.isFinite(value) ? value : 0)) * 1000);
}
// Stable original cue IDs survive time projection; prefix end times allow overlap
// lookup without scanning a long transcript on every timeupdate.
export function projectTranscript(cues, offsetSeconds = 0) {
  const offset = subtitleOffsetMS(offsetSeconds);
  const rows = (cues || []).map((cue, id) => ({ id, startMS: Math.max(0, cue.start_ms + offset), endMS: cue.end_ms + offset, text: cue.text }))
    .filter(row => row.endMS > 0).sort((a, b) => a.startMS - b.startMS || a.id - b.id);
  let maxEndMS = 0;
  return rows.map(row => ({ ...row, maxEndMS: maxEndMS = Math.max(maxEndMS, row.endMS) }));
}
export function activeTranscriptRows(rows, timeMS) {
  let low = 0, high = rows.length;
  while (low < high) { const mid = (low + high) >>> 1; if (rows[mid].startMS <= timeMS) low = mid + 1; else high = mid; }
  const ids = [];
  for (let i = low - 1; i >= 0 && rows[i].maxEndMS > timeMS; i--) if (rows[i].endMS > timeMS) ids.push(rows[i].id);
  return ids.reverse();
}
export function subtitleSeekPosition(row, range = null) {
  if (!row || (range && (row.startMS < range.startMS || row.startMS >= range.endMS))) return null;
  return row.startMS / 1000;
}
export function subtitleVTT(cues, offsetSeconds = 0) {
  const offset = subtitleOffsetMS(offsetSeconds);
  const timestamp = value => {
    const ms = Math.max(0, Math.round(value));
    return `${String(Math.floor(ms / 3600000)).padStart(2, '0')}:${String(Math.floor(ms / 60000) % 60).padStart(2, '0')}:${String(Math.floor(ms / 1000) % 60).padStart(2, '0')}.${String(ms % 1000).padStart(3, '0')}`;
  };
  const text = value => String(value).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/\r/g, '');
  return `WEBVTT\n\n${(cues || []).filter(cue => Number(cue.end_ms) + offset > 0).map((cue, index) => `${index + 1}\n${timestamp(Number(cue.start_ms) + offset)} --> ${timestamp(Number(cue.end_ms) + offset)}\n${text(cue.text)}\n`).join('\n')}`;
}
