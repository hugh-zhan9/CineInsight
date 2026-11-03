import { ListClipTopics, SaveClipTopic, ListClipTopicMembers, EditClipTopic, DeleteClipTopic, SaveClipBookmark, GetBookmarkTopicReferences, PreviewClipQueue, AppendClipQueue, DiscardClipQueuePreview, GetPlaybackQueue } from '../../wailsjs/go/main/App';
import { experienceData } from './experienceResult.js';
export const clipTopicAPI = {
  list: async request => experienceData(await ListClipTopics(request)),
  save: async request => experienceData(await SaveClipTopic(request)),
  members: async request => experienceData(await ListClipTopicMembers(request)),
  edit: async request => experienceData(await EditClipTopic(request)),
  remove: async (id, revision) => experienceData(await DeleteClipTopic(id, revision)),
  saveBookmark: async request => experienceData(await SaveClipBookmark(request)),
  references: async (id, revision) => experienceData(await GetBookmarkTopicReferences(id, revision)),
  queue: () => GetPlaybackQueue({ limit: 1 }),
  preview: async request => experienceData(await PreviewClipQueue(request)),
  append: async request => experienceData(await AppendClipQueue(request)),
  discard: async token => experienceData(await DiscardClipQueuePreview(token))
};
export function clipError(error) { return error?.code ? error.message : '操作未完成，请刷新后重试'; }
export { clipTime } from './playbackClip.js';
