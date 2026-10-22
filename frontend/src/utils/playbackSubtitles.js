// WebVTT cue text is escaped, never interpreted as HTML or VTT markup.
export function subtitleVTT(cues, offsetSeconds = 0) {
  const offset = Math.round(Math.max(-60, Math.min(60, Number(offsetSeconds) || 0)) * 1000);
  const timestamp = value => {
    const ms = Math.max(0, Math.round(value));
    return `${String(Math.floor(ms / 3600000)).padStart(2, '0')}:${String(Math.floor(ms / 60000) % 60).padStart(2, '0')}:${String(Math.floor(ms / 1000) % 60).padStart(2, '0')}.${String(ms % 1000).padStart(3, '0')}`;
  };
  const text = value => String(value).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/\r/g, '');
  return `WEBVTT\n\n${(cues || []).filter(cue => Number(cue.end_ms) + offset > 0).map((cue, index) => `${index + 1}\n${timestamp(Number(cue.start_ms) + offset)} --> ${timestamp(Number(cue.end_ms) + offset)}\n${text(cue.text)}\n`).join('\n')}`;
}
