import { SearchDialogueVideoGroups, ListDialogueVideoHits, SearchVisualResults, SearchImageResults, PreviewSearchClips, StopPlaybackQueue, SearchLibraryVideoPage, ListPeople, ListCollections } from '../../wailsjs/go/main/App';
import { experienceData } from './experienceResult.js';
import { clipTopicAPI } from './clipTopics.js';
export const searchResultsAPI = {
  ...clipTopicAPI,
  dialogue: async request => experienceData(await SearchDialogueVideoGroups(request)),
  hits: async request => experienceData(await ListDialogueVideoHits(request)),
  visual: async request => experienceData(await SearchVisualResults(request)),
  image: async request => experienceData(await SearchImageResults(request)),
  preview: async request => experienceData(await PreviewSearchClips(request)),
  stop: token => StopPlaybackQueue(token),
  video: request => SearchLibraryVideoPage(request),
  person: (keyword, cursor, limit) => ListPeople(keyword, cursor?.name || '', cursor?.id || 0, limit),
  collection: (keyword, cursor, limit) => ListCollections(keyword, cursor?.name || '', cursor?.id || 0, limit)
};
export function uniqueSearchHits(previous, incoming) {
  const result = [...previous], seen = new Set(previous.map(searchResultKey));
  for (const hit of incoming) { const key = searchResultKey(hit); if (!seen.has(key)) { seen.add(key); result.push(hit); } }
  return result;
}
export function searchResultKey(hit) { return hit.segment_id ? `dialogue:${hit.segment_id}` : `${hit.source}:${hit.video_id}:${hit.start_ms}:${hit.end_ms}`; }
export function rankedSearchGroups(hits) {
  const groups = new Map();
  for (const hit of hits || []) {
    if (!groups.has(hit.video_id)) groups.set(hit.video_id, { video_id: hit.video_id, title: hit.title, hits: [] });
    const group = groups.get(hit.video_id);
    const prior = group.hits.find(item => item.start_ms === hit.start_ms && item.source_version === hit.source_version);
    if (!prior) group.hits.push({ ...hit });
    else { prior.end_ms = Math.max(prior.end_ms, hit.end_ms); prior.preview_end_ms = Math.max(prior.preview_end_ms, hit.preview_end_ms); }
  }
  return [...groups.values()];
}
