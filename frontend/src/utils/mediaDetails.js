export function createDetailNavigator(initialEntry) {
  const stack = initialEntry ? [{ ...initialEntry }] : [];
  return {
    current() {
      return stack.length ? { ...stack[stack.length - 1] } : null;
    },
    push(entry) {
      if (!entry?.type || !Number(entry?.id)) throw new Error('invalid detail entry');
      stack.push({ type: entry.type, id: Number(entry.id) });
      return this.current();
    },
    back() {
      if (stack.length > 1) stack.pop();
      return this.current();
    },
    canGoBack() {
      return stack.length > 1;
    },
    reset(entry) {
      stack.splice(0, stack.length);
      if (entry) stack.push({ type: entry.type, id: Number(entry.id) });
      return this.current();
    }
  };
}

export function createVideoDetailsDraft(details) {
  const rating = details?.video?.personal_rating;
  return {
    displayTitle: details?.video?.display_title || '',
    originalTitle: details?.video?.original_title || '',
    description: details?.video?.description || '',
    personalRating: rating === null || rating === undefined ? '' : String(rating),
    personIDs: uniqueIDs((details?.people || []).map(item => item?.person?.id)),
    collectionIDs: uniqueIDs((details?.collections || []).map(item => item?.collection?.id))
  };
}

export function validateRatingDraft(value) {
  if (value === '' || value === null || value === undefined) return null;
  const rating = Number(value);
  if (!Number.isFinite(rating) || rating < 0 || rating > 10 || Math.abs(rating * 2 - Math.round(rating * 2)) > 1e-9) {
    throw new Error('评分必须是 0–10 之间的 0.5 倍数');
  }
  return rating;
}

export function toggleEntityID(ids, id, force) {
  const numericID = Number(id);
  const result = uniqueIDs(ids);
  const exists = result.includes(numericID);
  const shouldInclude = force === undefined ? !exists : !!force;
  if (shouldInclude && !exists) result.push(numericID);
  if (!shouldInclude && exists) return result.filter(item => item !== numericID);
  return result;
}

export function mergePersonCandidates(currentCandidates, searchResults, selectedIDs) {
  const selected = new Set(uniqueIDs(selectedIDs));
  const merged = [];
  const seen = new Set();
  for (const item of [...(searchResults || []), ...(currentCandidates || []).filter(candidate => selected.has(Number(candidate?.person?.id)))]) {
    const id = Number(item?.person?.id);
    if (!Number.isInteger(id) || id <= 0 || seen.has(id)) continue;
    seen.add(id); merged.push(item);
  }
  return merged;
}

export function mergeCollectionCandidates(currentCandidates, searchResults, selectedIDs) {
  const selected = new Set(uniqueIDs(selectedIDs));
  const merged = [];
  const seen = new Set();
  for (const item of [...(searchResults || []), ...(currentCandidates || []).filter(candidate => selected.has(Number(candidate?.collection?.id)))]) {
    const id = Number(item?.collection?.id);
    if (!Number.isInteger(id) || id <= 0 || seen.has(id)) continue;
    seen.add(id); merged.push(item);
  }
  return merged;
}

export function formatFrameRate(avgFrameRate, realFrameRate) {
  for (const raw of [avgFrameRate, realFrameRate]) {
    const value = String(raw || '').trim();
    if (!value) continue;
    const parts = value.split('/');
    const numerator = Number(parts[0]);
    const denominator = parts.length === 1 ? 1 : Number(parts[1]);
    const frameRate = numerator / denominator;
    if (!Number.isFinite(frameRate) || numerator <= 0 || denominator <= 0) continue;
    return `${frameRate.toFixed(3).replace(/\.?0+$/, '')} fps`;
  }
  return '未知帧率';
}

export function moveCollectionMember(members, fromIndex, toIndex) {
  const result = [...(members || [])];
  if (fromIndex < 0 || toIndex < 0 || fromIndex >= result.length || toIndex >= result.length || fromIndex === toIndex) {
    return result;
  }
  const [moved] = result.splice(fromIndex, 1);
  result.splice(toIndex, 0, moved);
  return result;
}

export function patchVideoFromDetails(video, details) {
  const updated = details?.video || {};
  return {
    ...video,
    ...updated,
    tags: Array.isArray(updated.tags) ? updated.tags : (video?.tags || [])
  };
}

export function detailPlaybackStartMs({ entryID, rootVideoID, explicitStartTimeMs, rootResumePositionSeconds, nestedResumePositionSeconds }) {
  const isRootVideo = Number(entryID) > 0 && Number(entryID) === Number(rootVideoID);
  const explicit = Number(explicitStartTimeMs);
  if (isRootVideo && explicitStartTimeMs !== null && explicitStartTimeMs !== undefined && Number.isFinite(explicit) && explicit >= 0) {
    return explicit;
  }
  const resumeSeconds = Number(isRootVideo ? rootResumePositionSeconds : nestedResumePositionSeconds);
  return Number.isFinite(resumeSeconds) && resumeSeconds > 0 ? resumeSeconds * 1000 : 0;
}

function uniqueIDs(ids) {
  return [...new Set((ids || []).map(Number).filter(id => Number.isInteger(id) && id > 0))];
}
