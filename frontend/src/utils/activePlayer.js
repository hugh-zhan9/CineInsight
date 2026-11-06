import { NotifyPlaybackActivity } from '../../wailsjs/go/main/App';
// 同一时间只允许一个内嵌播放器在播（D-UI-11）：开始播放的播放器登记为「当前播放者」，并暂停上一个，
// 免得抽屉和整页详情里的两个播放器同时出声。播放器暂停或卸载时注销自己。
// player 是任何带 pause() 的对象（InlineVideoPlayer 每个实例一个句柄）。
// 待播队列的内嵌播放器（QueueInlinePlayer）也登记在这里：它的队列会话状态仍归自己，被暂停时照常向队列上报 paused。
let current = null;
let activityTimer = null;
const activityIDs = new WeakMap();
function notifyActivity(player, playing) {
  if (!player || !globalThis.window?.go?.main?.App?.NotifyPlaybackActivity) return;
  if (!activityIDs.has(player)) activityIDs.set(player, `playback-${crypto.randomUUID()}`);
  Promise.resolve(NotifyPlaybackActivity(activityIDs.get(player), playing)).catch(() => {});
}

export function claimActivePlayer(player) {
  if (!player) return;
  if(current===player){notifyActivity(player,true);if(!activityTimer&&globalThis.window?.go?.main?.App?.NotifyPlaybackActivity)activityTimer=setInterval(()=>notifyActivity(current,true),2000);return;}
  const previous = current;
  current = player;
  previous?.pause();
  notifyActivity(previous, false); notifyActivity(player, true);
  clearInterval(activityTimer);
  if (globalThis.window?.go?.main?.App?.NotifyPlaybackActivity) activityTimer = setInterval(() => notifyActivity(current, true), 2000);
}

export function releaseActivePlayer(player) {
  if (current === player) { notifyActivity(player, false); current = null; clearInterval(activityTimer); activityTimer = null; }
}

export function currentActivePlayer() {
  return current;
}

let handoff=Promise.resolve();let handoffVersion=0;
const retiring=new Set();
// A detached native element may still be draining; a later explicit play waits
// for that cleanup and may retry a previously failed stop once.
export function retireActivePlayer(player,cleanup){
 const record={cleanup,failed:false,promise:null};retiring.add(record);releaseActivePlayer(player);
 const run=()=>{record.failed=false;record.promise=Promise.resolve().then(cleanup);record.promise.then(()=>retiring.delete(record),()=>{record.failed=true;});return record.promise;};record.run=run;return run();
}
export function prepareActivePlayer(player){
 const version=++handoffVersion;
 const next=handoff.catch(()=>{}).then(async()=>{
  for(const record of [...retiring])await(record.failed?record.run():record.promise);
  if(version!==handoffVersion)throw new DOMException('播放已取消','AbortError');
  if(current!==player)await current?.pause();
  if(version!==handoffVersion)throw new DOMException('播放已取消','AbortError');
  if(current!==player){notifyActivity(current,false);current=player;notifyActivity(player,true);}
 });handoff=next;return next;
}
export function installPlaybackBarrier(media,owner){
 if(!media)return;media.activePlayerOwner=owner;
 if(media.nativePlayback){media.beforePlay=()=>prepareActivePlayer(owner);return;}
 if(media._playBarrier)return;const original=media.play.bind(media);media._playBarrier=true;
 media.play=async()=>{await prepareActivePlayer(owner);try{return await original();}catch(error){releaseActivePlayer(owner);throw error;}};
}
