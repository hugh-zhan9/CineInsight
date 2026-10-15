// 保存顺序跨组件实例保留，避免关闭详情后立刻重开时读到旧断点。
// 只留进行中/失败的请求，成功即释放；视频行仍是唯一进度来源。
import { PlayVideo, UpdateVideoWatchProgress } from '../../wailsjs/go/main/App';

const pending = new Map();

export function saveVideoWatchProgress(...args) {
  const id = Number(args[0]);
  const previous = pending.get(id);
  const entry = { args, failed: false, promise: null };
  entry.promise = (previous?.promise || Promise.resolve()).catch(() => {}).then(() => UpdateVideoWatchProgress(...args));
  pending.set(id, entry);
  entry.promise.then(() => {
    if (pending.get(id) === entry) pending.delete(id);
  }, () => { entry.failed = true; });
  return entry.promise;
}

export async function waitForVideoWatchProgress(videoID) {
  const id = Number(videoID);
  let entry = pending.get(id);
  // 只有进入此方法前已经失败的保存才重试：这是用户后续一次明确的播放操作。
  if (entry?.failed) {
    saveVideoWatchProgress(...entry.args);
    entry = pending.get(id);
  }
  while (entry) {
    await entry.promise;
    entry = pending.get(id);
  }
}

export async function playVideoAfterProgress(videoID) {
  await waitForVideoWatchProgress(videoID);
  return PlayVideo(videoID);
}
