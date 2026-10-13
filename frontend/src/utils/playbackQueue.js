import { EditPlaybackQueue, GetPlaybackQueue, PlayPlaybackQueue } from '../../wailsjs/go/main/App';
import { confirmAction } from '../utils/feedback.js';
import { findCommand } from './commandRegistry.js';

export const queueStatusLabel = status => ({ idle: '尚未播放', starting: '正在准备', playing: '正在播放', paused: '已暂停', ended: '已播放结束', stopped: '已停止', dispatched: '已派发，请手动下一项', interrupted: '上次播放已中断', failed: '播放失败' })[status] || '状态未知';
export const queuePlayerLabel = player => ({ system: '系统默认（手动）', inline: '应用内', iina: '专用 IINA 窗口' })[player] || player;
export function openPlaybackQueue() { findCommand('action:playback-queue')?.run(); }

// Capture the exact revision shown in confirmation. Conflicts are surfaced;
// never silently retry an edit against a wider/different queue.
export async function addToPlaybackQueue({ videoIDs = [], collectionID = 0, startVideoID = 0, startAfter = false, replace = false } = {}) {
  const before = await GetPlaybackQueue({ limit: 1 });
  if (replace && !await confirmAction({ title: '从本集顺序播放', message: `将替换待播队列中的 ${before.total} 项，并停止当前队列播放。原视频不会删除。`, confirmText: '替换并播放' })) return false;
  await EditPlaybackQueue({ expected_revision: before.state.revision, action: collectionID ? (replace ? 'replace_collection' : 'append_collection') : 'append_videos', video_ids: collectionID ? [] : [...videoIDs], collection_id: collectionID, start_video_id: startVideoID, start_after: startAfter });
  if (replace) {
    const after = await GetPlaybackQueue({ revision: before.state.revision + 1, limit: 1 });
    if (!after.items?.length) throw new Error('作品集当前没有可播放的视频');
    await PlayPlaybackQueue(after.items[0].id, after.state.revision);
  }
  return true;
}

const QUEUE_PAGE_LIMIT = 200;
const QUEUE_READ_ATTEMPTS = 3;
const isQueueChanged = error => String(error?.message || error).includes('queue_changed');

// 某部视频在待播队列里的条目（同一部可能被加过多次）。按同一个 revision 分页读完整个队列，
// 读到一半队列变了（后端报 queue_changed）就从头再读，最多 3 次。
export async function findQueueEntriesForVideo(videoID) {
  const target = Number(videoID);
  for (let attempt = 1; ; attempt += 1) {
    try {
      let page = await GetPlaybackQueue({ limit: QUEUE_PAGE_LIMIT });
      const revision = page.state.revision;
      const entryIDs = [];
      for (;;) {
        for (const item of page.items || []) if (Number(item.video_id) === target) entryIDs.push(item.id);
        if (!page.has_more) return { revision, entryIDs };
        page = await GetPlaybackQueue({ revision, cursor_position: page.cursor_position, cursor_id: page.cursor_id, limit: QUEUE_PAGE_LIMIT });
      }
    } catch (error) {
      if (!isQueueChanged(error) || attempt >= QUEUE_READ_ATTEMPTS) throw error;
    }
  }
}

// 把一部视频从待播队列里拿掉（它在队列里的每一条都删）。每删一条 revision 加一；
// 中途队列被别处改了，就按最新的队列重来一次。返回删掉的条数。
export async function removeVideoFromPlaybackQueue(videoID) {
  for (let attempt = 1; ; attempt += 1) {
    const { revision, entryIDs } = await findQueueEntriesForVideo(videoID);
    let current = revision;
    try {
      for (const entryID of entryIDs) {
        await EditPlaybackQueue({ expected_revision: current, action: 'remove', entry_id: entryID });
        current += 1;
      }
      return entryIDs.length;
    } catch (error) {
      if (!isQueueChanged(error) || attempt >= QUEUE_READ_ATTEMPTS) throw error;
    }
  }
}
