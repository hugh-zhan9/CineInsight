export function toggleSelectedTagId(selectedTagIds, tagId) {
  const id = Number(tagId);
  if (!Number.isFinite(id) || id <= 0) {
    return Array.isArray(selectedTagIds) ? selectedTagIds.slice() : [];
  }
  const current = Array.isArray(selectedTagIds) ? selectedTagIds.map(Number) : [];
  if (current.includes(id)) {
    return current.filter(item => item !== id);
  }
  return [...current, id];
}

export function uniqueTagsById(tags) {
  const seen = new Set();
  const result = [];
  for (const tag of Array.isArray(tags) ? tags : []) {
    const id = Number(tag?.id);
    if (!Number.isFinite(id) || id <= 0 || seen.has(id)) continue;
    seen.add(id);
    result.push(tag);
  }
  return result;
}

export function selectedTagsFromIds(tags, selectedTagIds) {
  const selected = new Set((Array.isArray(selectedTagIds) ? selectedTagIds : []).map(Number));
  return uniqueTagsById(tags).filter(tag => selected.has(Number(tag.id)));
}
