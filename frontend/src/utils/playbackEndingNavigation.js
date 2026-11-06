// A source-bound handoff: validation before and after retiring the old player.
// The caller owns navigation cancellation; this function never starts playback itself.
export async function selectPlaybackEnding(id, { resolveSource, retire, isCurrent, navigate }) {
  const validated = await resolveSource(id);
  if (!isCurrent()) return false;
  if (await retire?.() === false || !isCurrent()) return false;
  const rechecked = await resolveSource(id);
  if (!isCurrent()) return false;
  if (!validated?.source_version || validated.video_id !== id || rechecked?.video_id !== id || validated.source_version !== rechecked.source_version) throw new Error('原片已变化，请重新选择');
  return navigate(id, validated.source_version);
}
