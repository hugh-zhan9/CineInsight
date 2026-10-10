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
