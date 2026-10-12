// 同一时间只允许一个内嵌播放器在播（D-UI-11）：开始播放的播放器登记为「当前播放者」，并暂停上一个，
// 免得抽屉和整页详情里的两个播放器同时出声。播放器暂停或卸载时注销自己。
// player 是任何带 pause() 的对象（InlineVideoPlayer 每个实例一个句柄）。
// 待播队列的内嵌播放器（QueueInlinePlayer）也登记在这里：它的队列会话状态仍归自己，被暂停时照常向队列上报 paused。
let current = null;

export function claimActivePlayer(player) {
  if (!player || current === player) return;
  const previous = current;
  current = player;
  previous?.pause();
}

export function releaseActivePlayer(player) {
  if (current === player) current = null;
}

export function currentActivePlayer() {
  return current;
}
