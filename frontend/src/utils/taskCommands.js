// 命令面板任务组的命令构造（D-029 任务组）。状态只有两个来源：登记表快照
// （GetBackgroundTasks / 事件 background-tasks，运行中的任务 key）与空闲门总览
// （GetIdleSchedulerStatus / 事件 idle-scheduler-state，等待空闲的任务清单）。
//
// 放在这里而不是面板里：面板只渲染注册表，任务与绑定的对应关系是业务知识。

import {
  CancelFaceAnalysis, CancelImageAITagging, CancelImageEXIFBackfill, CancelImageSemanticIndex, CancelLocalMetadataBackfill,
  CancelFrameHashBackfill, CancelPerceptualHashBackfill, CancelPlaybackProxyTask, CancelSemanticIndex, CancelSubtitle, CancelTechnicalBackfill,
  CreateDatabaseBackup, RunGatedTaskNow, StartImageAITagging, StartImageEXIFBackfill, StartImageSemanticIndex,
  StartFrameHashBackfill, StartLocalMetadataBackfill, StartPerceptualHashBackfill, StartTechnicalBackfill, TriggerAITagging
} from '../../wailsjs/go/main/App';
import { BACKGROUND_TASK_LABELS, idleWaitReasonLabel, isIdleGateNotWaitingError } from './idleScheduling.js';

// 各任务的显式启动 / 取消绑定。只收零参绑定：超分要选文件与倍率、字幕要选视频、
// 语义索引要选构建范围、清理分析要选阈值，命令面板不替用户编造这些参数，
// 这类任务因此只在运行中或等待空闲时出现在任务组里。
//
// local_metadata 一个 key 对应补全与写出两条 worker（后端按计数登记）。这里的
// 启动与取消都只作用于「补全」，写出仍只从片库页的管理菜单发起。
//
// 每个绑定都包一层箭头函数：模块初始化时不去读绑定本身，测试里只需要模拟真正
// 被调用到的那几个方法。
const TASK_BINDINGS = {
  subtitle: { cancel: () => CancelSubtitle() },
  phash: { start: () => StartPerceptualHashBackfill(), cancel: () => CancelPerceptualHashBackfill() },
  frame_hash: { start: () => StartFrameHashBackfill(), cancel: () => CancelFrameHashBackfill() },
  technical: { start: () => StartTechnicalBackfill(), cancel: () => CancelTechnicalBackfill() },
  local_metadata: { start: () => StartLocalMetadataBackfill(), cancel: () => CancelLocalMetadataBackfill() },
  semantic: { cancel: () => CancelSemanticIndex() },
  // 人脸分析的启动要选范围（视频/图片/全部），按 D-029 的口径不进面板；
  // 但它在跑的时候得能取消，与字幕、语义索引同处理。
  face: { cancel: () => CancelFaceAnalysis() },
  // 播放代理的启动一律要选对象（单个视频 / 选中 / 当前筛选），按 D-029 的口径
  // 不进面板；跑起来之后要能取消，与字幕、语义索引、人脸同处理。
  proxy: { cancel: () => CancelPlaybackProxyTask() },
  image_semantic: { start: () => StartImageSemanticIndex(), cancel: () => CancelImageSemanticIndex() },
  ai_tagging: { start: () => TriggerAITagging() },
  image_ai_tagging: { start: () => StartImageAITagging(), cancel: () => CancelImageAITagging() },
  exif: { start: () => StartImageEXIFBackfill(), cancel: () => CancelImageEXIFBackfill() },
  backup: { start: () => CreateDatabaseBackup() }
};

function taskLabel(taskKey) {
  return BACKGROUND_TASK_LABELS[taskKey] || taskKey;
}

// 「立即运行」恰好落在任务刚被放行之后会收到 idle_gate_task_not_waiting，
// 那不是失败，只是没什么可放行的了（与设置页、图片任务面板同口径）。
async function runGatedTaskNow(taskKey) {
  try {
    await RunGatedTaskNow(taskKey);
  } catch (err) {
    if (!isIdleGateNotWaitingError(err)) throw err;
  }
}

// running：登记表里正在跑的 task key 数组。waiting：空闲门 status.waiting 数组。
// onSettled：每条命令执行完（成功或失败）后调用，用来重新拉一次两项状态。
export function buildTaskCommands({ running = [], waiting = [], onSettled = null } = {}) {
  const runningKeys = new Set((running || []).map(key => String(key)));
  const waitingReasons = new Map();
  for (const item of waiting || []) {
    if (item?.task_key) waitingReasons.set(String(item.task_key), item.reason);
  }

  const settle = result => {
    if (typeof onSettled !== 'function') return result;
    return Promise.resolve(result).finally(onSettled);
  };

  const keys = [];
  for (const key of Object.keys(BACKGROUND_TASK_LABELS)) {
    if (runningKeys.has(key) || waitingReasons.has(key) || TASK_BINDINGS[key]?.start) keys.push(key);
  }
  // 运行中与等待空闲的任务可能不在标签表里（后端加了新 key 而前端还没同步文案），
  // 它们仍要出现在面板里，只是显示成裸 key。
  for (const key of [...runningKeys, ...waitingReasons.keys()]) {
    if (!keys.includes(key)) keys.push(key);
  }

  return keys.map(key => {
    const name = taskLabel(key);
    const bindings = TASK_BINDINGS[key] || {};
    const keywords = [key, '任务', 'task'];

    if (runningKeys.has(key)) {
      const cancel = bindings.cancel;
      return {
        id: `task:${key}`,
        group: 'task',
        label: cancel ? `${name} · 运行中 · 取消` : `${name} · 运行中`,
        keywords: cancel ? [...keywords, '取消', 'cancel'] : keywords,
        enabled: () => !!cancel,
        run: () => settle(cancel ? cancel() : undefined)
      };
    }

    if (waitingReasons.has(key)) {
      return {
        id: `task:${key}`,
        group: 'task',
        label: `${name} · 等待空闲（${idleWaitReasonLabel(waitingReasons.get(key))}） · 立即运行`,
        keywords: [...keywords, '等待空闲', '立即运行', 'idle'],
        run: () => settle(runGatedTaskNow(key))
      };
    }

    return {
      id: `task:${key}`,
      group: 'task',
      label: `${name} · 启动`,
      keywords: [...keywords, '启动', 'start'],
      run: () => settle(bindings.start())
    };
  });
}
